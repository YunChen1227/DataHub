//go:build ignore

// Mock 黑名单因子V35 upstream implementing enol /enol/api/v1/doCheck for blk
// full-link testing. Run: go run scripts/mock_blacklist.go
//
// Verifies apiKey=blackIntV35 and MD5 sign over body params (MD5-hashed PII when
// encryptionType=2) + secret, then routes:
//   - bad sign / apiKey          -> busiCode 1005
//   - mobile MD5(13800000000)    -> busiCode 1000 (未查得)
//   - otherwise                  -> busiCode 10 + rich result object
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
	addr := env("MOCK_BLACKLIST_ADDR", ":9115")
	secret := env("BLK_APP_SECRET", "demo-blk-secret")
	notFoundMobile := md5hex("13800000000")

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

		resp := map[string]any{"code": 0, "msg": "请求成功", "seqNo": "blk-mock-001"}
		data := map[string]any{}

		switch {
		case env.APIKey != "blackIntV35":
			data["busiCode"], data["busiMsg"] = 1003, "产品编号异常"
		case signGama(env.Body, secret) != env.Sign:
			data["busiCode"], data["busiMsg"] = 1005, "账号信息异常"
		case env.Body["mobile"] == notFoundMobile:
			data["busiCode"], data["busiMsg"] = 1000, "未查得"
		default:
			data["busiCode"], data["busiMsg"] = 10, "success"
			data["result"] = map[string]any{
				"whether_hit": 1,
				"hit_grade":   3,
				"hit_type": []map[string]any{
					{"scene": "P1", "m1": 0, "m3": 1, "m6": 2},
				},
			}
		}
		resp["data"] = data
		log.Printf("blacklist <- mobile=%s apiKey=%s enc=%d -> busiCode=%v",
			env.Body["mobile"], env.APIKey, env.EncryptionType, data["busiCode"])
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	})

	fmt.Printf("mock 黑名单因子V35 upstream listening on %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
