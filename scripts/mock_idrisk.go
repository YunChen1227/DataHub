//go:build ignore

// Mock 身份风险V107 upstream implementing enol /enol/api/v1/doCheck for sffx
// full-link testing. Run: go run scripts/mock_idrisk.go
//
// Verifies apiKey=idRiskTagV107 and MD5 sign over body params (MD5-hashed PII
// when encryptionType=2) + secret, then routes:
//   - bad apiKey                  -> busiCode 1004 产品编号异常
//   - bad sign                    -> busiCode 1005 账号信息异常
//   - missing name / idCard       -> busiCode 1007 数据请求异常 (兜底暴露漏拦)
//   - idCard == unpaidIDCard      -> busiCode 1001 账户余额不足 (上游侧错误, 不计费)
//   - idCard == notFoundIDCard    -> busiCode 1000 数据未查得 (**查无也计费**)
//   - idCard == noRiskIDCard      -> busiCode 10 + result{detail:["A0"]} (查得无风险)
//   - otherwise                   -> busiCode 10 + result{detail:["C2","J3"]} (查得命中)
//
// 注意：encryptionType=2 时上游收到的是 MD5 摘要，故这里的身份证判定都比对
// md5hex(明文身份证号)。
package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// signGama 复刻应诺尔 PDF §3.1 加签：body 非空参数按键名 ASCII 升序拼「键值」，
// 末尾接 secret，取 MD5 小写 hex。
func signGama(params map[string]string, secret string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteString(params[k])
	}
	sb.WriteString(secret)
	sum := md5.Sum([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func main() {
	addr := env("MOCK_IDRISK_ADDR", ":9128")
	secret := env("SFFX_APP_SECRET", "demo-sffx-secret")
	// 约定的测试身份证号（均为合法 18 位格式，用来驱动各归一化分支）。
	notFound := md5hex(env("SFFX_NOTFOUND_IDCARD", "000000000000001000"))
	noRisk := md5hex(env("SFFX_NORISK_IDCARD", "000000000000000010"))
	unpaid := md5hex(env("SFFX_UNPAID_IDCARD", "000000000000001001"))

	http.HandleFunc("/enol/api/v1/doCheck", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var env struct {
			AppID          string            `json:"appId"`
			Sign           string            `json:"sign"`
			APIKey         string            `json:"apiKey"`
			EncryptionType int               `json:"encryptionType"`
			Body           map[string]string `json:"body"`
		}
		_ = json.Unmarshal(raw, &env)

		resp := map[string]any{"code": 0, "msg": "请求成功", "seqNo": "sffx-mock-001"}
		data := map[string]any{}
		idCard := env.Body["idCard"]

		switch {
		case env.APIKey != "idRiskTagV107":
			data["busiCode"], data["busiMsg"] = 1004, "产品编号异常"
		case signGama(env.Body, secret) != env.Sign:
			data["busiCode"], data["busiMsg"] = 1005, "账号信息异常"
		case env.Body["name"] == "" || idCard == "":
			// 上游参数表 name/idCard 均必填；网关本应前置拦截，此处兜底以暴露漏拦。
			data["busiCode"], data["busiMsg"] = 1007, "数据请求异常"
		case idCard == unpaid:
			data["busiCode"], data["busiMsg"] = 1001, "账户余额不足"
		case idCard == notFound:
			data["busiCode"], data["busiMsg"] = 1000, "数据未查得"
		case idCard == noRisk:
			// 风险周期码 0 只与 A 组合（文档「0(只存在A0) 无风险」）。
			data["busiCode"], data["busiMsg"] = 10, "查询成功"
			data["result"] = map[string]any{"detail": []string{"A0"}}
		default:
			// C=涉赌人员/周期2=1年内；J=金融诈骗/周期3=1-3年。
			data["busiCode"], data["busiMsg"] = 10, "查询成功"
			data["result"] = map[string]any{"detail": []string{"C2", "J3"}}
		}
		resp["data"] = data
		log.Printf("idrisk <- name=%s idCard=%s apiKey=%s enc=%d -> busiCode=%v",
			env.Body["name"], idCard, env.APIKey, env.EncryptionType, data["busiCode"])
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	})

	fmt.Printf("mock 身份风险V107 upstream listening on %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
