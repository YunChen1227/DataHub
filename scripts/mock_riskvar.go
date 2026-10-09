//go:build ignore

// Mock 风险变量V11 upstream implementing enol /enol/api/v1/doCheck for fxbl
// full-link testing. Run: go run scripts/mock_riskvar.go
//
// Verifies apiKey=loanRiskTagV11, encryptionType=1 (明文), MD5 sign over body params
// + secret, then routes:
//   - bad apiKey                       -> busiCode 1004 产品编号异常
//   - encryptionType != 1              -> busiCode 1007 数据请求异常
//   - bad sign                         -> busiCode 1005 账号信息异常
//   - missing name / idCard / mobile   -> busiCode 1007 数据请求异常 (兜底暴露漏拦)
//   - mobile == unpaidMobile           -> busiCode 1001 账户余额不足 (上游侧错误, 不计费)
//   - mobile == notFoundMobile         -> busiCode 1000 数据未查得 (**查无也计费**)
//   - otherwise                        -> busiCode 10 + result{als_*, Rule_*} (查得)
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

func main() {
	addr := env("MOCK_RISKVAR_ADDR", ":9130")
	secret := env("FXBL_APP_SECRET", "demo-fxbl-secret")
	notFound := env("FXBL_NOTFOUND_MOBILE", "13800000000")
	unpaid := env("FXBL_UNPAID_MOBILE", "13800001001")

	http.HandleFunc("/enol/api/v1/doCheck", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var env struct {
			EncryptionType int               `json:"encryptionType"`
			AppID          string            `json:"appId"`
			Sign           string            `json:"sign"`
			APIKey         string            `json:"apiKey"`
			Body           map[string]string `json:"body"`
		}
		_ = json.Unmarshal(raw, &env)

		resp := map[string]any{"code": 0, "msg": "请求成功", "seqNo": "fxbl-mock-001"}
		data := map[string]any{}
		mobile := env.Body["mobile"]

		switch {
		case env.APIKey != "loanRiskTagV11":
			data["busiCode"], data["busiMsg"] = 1004, "产品编号异常"
		case env.EncryptionType != 1:
			data["busiCode"], data["busiMsg"] = 1007, "数据请求异常"
		case signGama(env.Body, secret) != env.Sign:
			data["busiCode"], data["busiMsg"] = 1005, "账号信息异常"
		case env.Body["name"] == "" || env.Body["idCard"] == "" || mobile == "":
			data["busiCode"], data["busiMsg"] = 1007, "数据请求异常"
		case mobile == unpaid:
			data["busiCode"], data["busiMsg"] = 1001, "账户余额不足"
		case mobile == notFound:
			data["busiCode"], data["busiMsg"] = 1000, "数据未查得"
		default:
			data["busiCode"], data["busiMsg"] = 10, "success"
			data["result"] = map[string]any{
				"als_m12_id_max_monnum": "6", "als_m6_id_avg_monnum": "2.75",
				"als_m1_id_pdl_allnum": "2", "Rule_final_decision": "Accept",
				"Rule_final_weight": "60", "Rule_name_QJF077": "在非银机构申请次数较多",
				"Rule_weight_QJF077": "60",
			}
		}
		resp["data"] = data
		log.Printf("riskvar <- name=%s mobile=%s apiKey=%s enc=%d -> busiCode=%v",
			env.Body["name"], mobile, env.APIKey, env.EncryptionType, data["busiCode"])
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	})

	fmt.Printf("mock 风险变量V11 upstream listening on %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
