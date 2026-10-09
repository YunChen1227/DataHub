//go:build ignore

// Mock 借贷申请意向V22【TD】upstream implementing enol /enol/api/v1/doCheck for jdyx
// full-link testing. Run: go run scripts/mock_loanintent.go
//
// Verifies apiKey=loanRiskTagV22, **no encryptionType key in the envelope** (TD 文档
// §1.4 请求结构无该字段), MD5 sign over plaintext body params + secret, then routes:
//   - bad apiKey                       -> busiCode 1004 产品编号异常
//   - envelope 带 encryptionType       -> busiCode 1007 数据请求异常 (暴露协议跑偏)
//   - bad sign                         -> busiCode 1005 账号信息异常
//   - missing name / idCard / mobile   -> busiCode 1007 数据请求异常 (兜底暴露漏拦)
//   - mobile == unpaidMobile           -> busiCode 1001 账户余额不足 (上游侧错误, 不计费)
//   - mobile == notFoundMobile         -> busiCode 1000 未查得 (不计费)
//   - otherwise                        -> busiCode 10 + result{TC_*} (查得)
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
	addr := env("MOCK_LOANINTENT_ADDR", ":9129")
	secret := env("JDYX_APP_SECRET", "demo-jdyx-secret")
	notFound := env("JDYX_NOTFOUND_MOBILE", "13800000000")
	unpaid := env("JDYX_UNPAID_MOBILE", "13800001001")

	http.HandleFunc("/enol/api/v1/doCheck", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var keys map[string]json.RawMessage
		_ = json.Unmarshal(raw, &keys)
		var env struct {
			AppID  string            `json:"appId"`
			Sign   string            `json:"sign"`
			APIKey string            `json:"apiKey"`
			Body   map[string]string `json:"body"`
		}
		_ = json.Unmarshal(raw, &env)
		_, hasEnc := keys["encryptionType"]

		resp := map[string]any{"code": 0, "msg": "请求成功", "seqNo": "jdyx-mock-001"}
		data := map[string]any{}
		mobile := env.Body["mobile"]

		switch {
		case env.APIKey != "loanRiskTagV22":
			data["busiCode"], data["busiMsg"] = 1004, "产品编号异常"
		case hasEnc:
			data["busiCode"], data["busiMsg"] = 1007, "数据请求异常"
		case signGama(env.Body, secret) != env.Sign:
			data["busiCode"], data["busiMsg"] = 1005, "账号信息异常"
		case env.Body["name"] == "" || env.Body["idCard"] == "" || mobile == "":
			data["busiCode"], data["busiMsg"] = 1007, "数据请求异常"
		case mobile == unpaid:
			data["busiCode"], data["busiMsg"] = 1001, "账户余额不足"
		case mobile == notFound:
			data["busiCode"], data["busiMsg"] = 1000, "未查得"
		default:
			data["busiCode"], data["busiMsg"] = 10, "success"
			data["result"] = map[string]any{
				"TC_LOAN_CNT_3M": 9, "TC_GENERAL_CONS_ORG_3M": 1, "TC_BANK_ORG_3M": 2,
				"TC_SMALL_LOAN_ORG_1M": 1, "TC_MULTI_PTF_3M": 6, "TC_MULTI_PTF_1M": 4,
			}
		}
		resp["data"] = data
		log.Printf("loanintent <- name=%s mobile=%s apiKey=%s hasEnc=%v -> busiCode=%v",
			env.Body["name"], mobile, env.APIKey, hasEnc, data["busiCode"])
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	})

	fmt.Printf("mock 借贷申请意向V22【TD】upstream listening on %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
