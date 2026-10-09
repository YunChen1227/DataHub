//go:build ignore

// Mock 信用全景V21 upstream implementing enol /enol/api/v1/doCheck for xyqj
// full-link testing. Run: go run scripts/mock_creditpano.go
//
// Verifies apiKey=loanRiskTagV21, encryptionType=1 (明文), MD5 sign over body params
// + secret, then routes:
//   - bad apiKey                       -> busiCode 1004 产品编号异常
//   - encryptionType != 1              -> busiCode 1007 数据请求异常
//   - bad sign                         -> busiCode 1005 账号信息异常
//   - missing name / idCard / mobile   -> busiCode 1007 数据请求异常 (兜底暴露漏拦)
//   - mobile == unpaidMobile           -> busiCode 1001 账户余额不足 (上游侧错误, 不计费)
//   - mobile == notFoundMobile         -> busiCode 1000 数据未查得 (不计费)
//   - mobile == freeMobile             -> busiCode 10 + fee_status "0" (上游不收费 → 999)
//   - otherwise                        -> busiCode 10 + fee_status "1" (查得计费)
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
	addr := env("MOCK_CREDITPANO_ADDR", ":9131")
	secret := env("XYQJ_APP_SECRET", "demo-xyqj-secret")
	notFound := env("XYQJ_NOTFOUND_MOBILE", "13800000000")
	unpaid := env("XYQJ_UNPAID_MOBILE", "13800001001")
	free := env("XYQJ_FREE_MOBILE", "13800000010")

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

		resp := map[string]any{"code": 0, "msg": "请求成功", "seqNo": "xyqj-mock-001"}
		data := map[string]any{}
		mobile := env.Body["mobile"]
		result := func(fee string) map[string]any {
			return map[string]any{
				"xyp_model_score_high": "668", "xyp_model_score_mid": "765", "xyp_model_score_low": "602",
				"xyp_cpl0001": "1", "xyp_cpl0073": "0.58", "xyp_t01aazhzz": "", "fee_status": fee,
			}
		}

		switch {
		case env.APIKey != "loanRiskTagV21":
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
		case mobile == free:
			data["busiCode"], data["busiMsg"] = 10, "success"
			data["result"] = result("0")
		default:
			data["busiCode"], data["busiMsg"] = 10, "success"
			data["result"] = result("1")
		}
		resp["data"] = data
		log.Printf("creditpano <- name=%s mobile=%s apiKey=%s enc=%d -> busiCode=%v",
			env.Body["name"], mobile, env.APIKey, env.EncryptionType, data["busiCode"])
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	})

	fmt.Printf("mock 信用全景V21 upstream listening on %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
