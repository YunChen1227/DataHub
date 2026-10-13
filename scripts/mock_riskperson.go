//go:build ignore

// Mock 风险人员V108 upstream implementing enol /enol/api/v1/doCheck for fxry
// full-link testing. Run: go run scripts/mock_riskperson.go
//
// Verifies apiKey=idRiskTagV108 and MD5 sign over body params (MD5-hashed PII
// when encryptionType=2) + secret, then routes:
//   - bad apiKey                  -> busiCode 1004 产品编号异常
//   - bad sign                    -> busiCode 1005 账号信息异常
//   - missing name / idCard       -> busiCode 1007 数据请求异常 (兜底暴露漏拦)
//   - mobile present in body      -> busiCode 1007 (本产品参数表无 mobile，暴露多传)
//   - idCard == unpaidIDCard      -> busiCode 1001 账户余额不足 (上游侧错误, 不计费)
//   - idCard == notFoundIDCard    -> busiCode 1000 数据未查得 (**查无也计费**)
//   - idCard == normalIDCard      -> busiCode 10 + resultCode 第 25 位=1 (是否正常)
//   - otherwise                   -> busiCode 10 + resultCode 第 1/13 位=1 (涉刑+涉案)
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
	addr := env("MOCK_RISKPERSON_ADDR", ":9132")
	secret := env("FXRY_APP_SECRET", "demo-fxry-secret")
	notFound := env("FXRY_NOTFOUND_IDCARD", "000000000000001000")
	normal := env("FXRY_NORMAL_IDCARD", "000000000000000010")
	unpaid := env("FXRY_UNPAID_IDCARD", "000000000000001001")

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

		resp := map[string]any{"code": 0, "msg": "请求成功", "seqNo": "fxry-mock-001"}
		data := map[string]any{}
		idCard := env.Body["idCard"]
		is := func(plain string) bool {
			if env.EncryptionType == 2 {
				return idCard == md5hex(plain)
			}
			return idCard == plain
		}

		switch {
		case env.APIKey != "idRiskTagV108":
			data["busiCode"], data["busiMsg"] = 1004, "产品编号异常"
		case signGama(env.Body, secret) != env.Sign:
			data["busiCode"], data["busiMsg"] = 1005, "账号信息异常"
		case env.Body["name"] == "" || idCard == "":
			data["busiCode"], data["busiMsg"] = 1007, "数据请求异常"
		case env.Body["mobile"] != "":
			data["busiCode"], data["busiMsg"] = 1007, "数据请求异常"
		case is(unpaid):
			data["busiCode"], data["busiMsg"] = 1001, "账户余额不足"
		case is(notFound):
			data["busiCode"], data["busiMsg"] = 1000, "数据未查得"
		case is(normal):
			data["busiCode"], data["busiMsg"] = 10, "success"
			data["result"] = map[string]any{"resultCode": "0000000000000000000000001"}
		default:
			data["busiCode"], data["busiMsg"] = 10, "success"
			data["result"] = map[string]any{"resultCode": "1000000000001000000000000"}
		}
		resp["data"] = data
		log.Printf("riskperson <- name=%s idCard=%s apiKey=%s enc=%d -> busiCode=%v",
			env.Body["name"], idCard, env.APIKey, env.EncryptionType, data["busiCode"])
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	})

	fmt.Printf("mock 风险人员V108 upstream listening on %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
