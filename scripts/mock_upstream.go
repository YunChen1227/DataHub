//go:build ignore

// Mock upstream implementing income_cls.md (/yrzx/finan/net/10w/v9) for
// full-link testing. Run: go run scripts/mock_upstream.go
//
//	verify = MD5(account+idCard+mobile+reqid+key).toUpperCase()
//
// Response routing (for deterministic e2e):
//   - mobile == 13800000000  -> code 999 (查无结果)
//   - bad verify             -> code 013 (校验签名错误)
//   - otherwise              -> code 001 + result.range "7"
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

func md5Upper(s string) string {
	sum := md5.Sum([]byte(s))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func main() {
	addr := env("MOCK_ADDR", ":9111")
	account := env("UPSTREAM_ACCOUNT", "demo-account")
	key := env("UPSTREAM_KEY", "demo-key")

	http.HandleFunc("/yrzx/finan/net/10w/v9", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		acc := q.Get("account")
		idCard := q.Get("idCard")
		mobile := q.Get("mobile")
		reqid := q.Get("reqid")
		verify := q.Get("verify")

		expected := md5Upper(acc + idCard + mobile + reqid + key)
		resp := map[string]any{"reqid": reqid}

		switch {
		case acc != account:
			resp["code"], resp["msg"] = "002", "账号不存在"
		case verify != expected:
			resp["code"], resp["msg"] = "013", "校验签名错误"
		case mobile == "13800000000":
			resp["code"], resp["msg"] = "999", "查无结果"
		default:
			uid := "UID-" + reqid
			resp["code"], resp["msg"] = "001", "成功"
			resp["uid"] = uid
			resp["result"] = map[string]string{"range": "7"}
			resp["verify"] = md5Upper("001" + uid + key)
		}
		log.Printf("upstream <- reqid=%s mobile=%s -> code=%v", reqid, mobile, resp["code"])
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	})

	fmt.Printf("mock upstream listening on %s (account=%s)\n", addr, account)
	log.Fatal(http.ListenAndServe(addr, nil))
}
