//go:build ignore

// Mock 经济能力 (income_cls) upstream for v9/v8 full-link testing. Serves the
// GET account/key contract on both version paths. Run: go run scripts/mock_income.go
//
// Verifies verify = MD5(account+idCard+mobile+reqid+key).toUpperCase(), then:
//   - bad verify            -> code 013 (校验签名错误)
//   - mobile == 13800000000 -> code 999 (查无结果)
//   - otherwise             -> code 001 + result.range "7"
package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func signIncome(account, idCard, mobile, reqid, key string) string {
	sum := md5.Sum([]byte(account + idCard + mobile + reqid + key))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func main() {
	addr := env("MOCK_INCOME_ADDR", ":9113")
	account := env("INCOME_ACCOUNT", "demo-income-account")
	key := env("INCOME_KEY", "demo-income-key")

	handler := func(version string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			acc := q.Get("account")
			idCard := q.Get("idCard")
			mobile := q.Get("mobile")
			reqid := q.Get("reqid")
			verify := q.Get("verify")

			resp := map[string]any{"uid": "income-" + version + "-" + reqid, "reqid": reqid}
			switch {
			case acc != account:
				resp["code"], resp["msg"] = "002", "账号不存在"
			case !strings.EqualFold(verify, signIncome(account, idCard, mobile, reqid, key)):
				resp["code"], resp["msg"] = "013", "校验签名错误"
			case mobile == "13800000000":
				resp["code"], resp["msg"] = "999", "查无结果"
			default:
				resp["code"], resp["msg"] = "001", "成功"
				resp["result"] = map[string]string{"range": "7"}
			}
			log.Printf("income[%s] <- reqid=%s mobile=%s -> code=%v", version, reqid, mobile, resp["code"])
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(resp)
		}
	}

	http.HandleFunc("/yrzx/finan/net/10w/v9", handler("v9"))
	http.HandleFunc("/yrzx/finan/net/10w/v8", handler("v8"))

	fmt.Printf("mock 经济能力 upstream listening on %s (v9/v8)\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
