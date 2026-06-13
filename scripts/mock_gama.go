//go:build ignore

// Mock 伽马分层分 upstream implementing 伽马PDF /enol/api/v1/doCheck for
// full-link testing. Run: go run scripts/mock_gama.go
//
// Verifies MD5 sign over body params + secret, then routes:
//   - mobile == 13800000000 -> busiCode 1000 (数据未查得)
//   - bad sign              -> busiCode 1005
//   - otherwise             -> busiCode 10 + result.score "7"
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

func main() {
	addr := env("MOCK_GAMA_ADDR", ":9112")
	secret := env("GAMA_APP_SECRET", "demo-gama-secret")

	http.HandleFunc("/enol/api/v1/doCheck", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var env struct {
			AppID  string            `json:"appId"`
			Sign   string            `json:"sign"`
			APIKey string            `json:"apiKey"`
			Body   map[string]string `json:"body"`
		}
		_ = json.Unmarshal(raw, &env)

		resp := map[string]any{"code": 0, "msg": "请求成功", "seqNo": "gama-" + env.Body["tradeNo"]}
		data := map[string]any{}

		switch {
		case signGama(env.Body, secret) != env.Sign:
			data["busiCode"], data["busiMsg"] = 1005, "账号信息异常"
		case env.Body["mobile"] == "13800000000":
			data["busiCode"], data["busiMsg"] = 1000, "数据未查得"
		default:
			data["busiCode"], data["busiMsg"] = 10, "success"
			data["result"] = map[string]string{"score": "7"}
		}
		resp["data"] = data
		log.Printf("gama <- tradeNo=%s mobile=%s -> busiCode=%v", env.Body["tradeNo"], env.Body["mobile"], data["busiCode"])
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	})

	fmt.Printf("mock 伽马 upstream listening on %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
