//go:build ignore

// Mock 身份证三要素核验 upstream implementing POST /api/idCardThreeElements (JSON)
// for sfzhy full-link testing. Run: go run scripts/mock_idverify.go
//
// Verifies signature = SHA256(升序 "k=v&k=v..." + "&AppSecret=" + 商户密钥), then:
//   - bad sign / appId       -> Code 405 / 404 (IsCharge=false -> 网关 505062)
//   - idcard == errIDCard    -> Code 461 请求照片大小不符合要求 (IsCharge=false)
//   - otherwise              -> Code 0 + Data{Result,ResultMessage,ImageScore}
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func main() {
	addr := env("MOCK_IDVERIFY_ADDR", ":9118")
	appID := env("SFZHY_APP_ID", "demo-sfzhy-appid")
	appSecret := env("SFZHY_APP_SECRET", "demo-sfzhy-secret")
	// 约定「上游错误」触发用身份证号（合法 18 位格式），驱动 Code=461 场景。
	errIDCard := env("SFZHY_ERR_IDCARD", "000000000000000007")

	http.HandleFunc("/api/idCardThreeElements", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			AppID          string `json:"appId"`
			OutBizNo       string `json:"outBizNo"`
			Name           string `json:"name"`
			IDCard         string `json:"idCard"`
			ProfilePicture string `json:"profilePicture"`
			Timestamp      int64  `json:"timestamp"`
			Signature      string `json:"signature"`
		}
		_ = json.Unmarshal(raw, &req)

		params := map[string]string{
			"appId":          req.AppID,
			"outBizNo":       req.OutBizNo,
			"name":           req.Name,
			"idCard":         req.IDCard,
			"profilePicture": req.ProfilePicture,
			"timestamp":      strconv.FormatInt(req.Timestamp, 10),
		}
		keys := make([]string, 0, len(params))
		for k := range params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var sb strings.Builder
		for i, k := range keys {
			if i > 0 {
				sb.WriteString("&")
			}
			sb.WriteString(k)
			sb.WriteString("=")
			sb.WriteString(params[k])
		}
		sb.WriteString("&AppSecret=")
		sb.WriteString(appSecret)
		want := sha256hex(sb.String())

		var resp map[string]any
		switch {
		case req.AppID != appID:
			resp = map[string]any{"Code": 404, "Message": "appId不存在", "IsCharge": false, "ErrorAddress": "00000", "RequestId": "sfzhy-mock-404"}
		case req.Signature != want:
			resp = map[string]any{"Code": 405, "Message": "签名校验错误", "IsCharge": false, "ErrorAddress": "00000", "RequestId": "sfzhy-mock-405"}
		case req.IDCard == errIDCard:
			resp = map[string]any{"Code": 461, "Message": "请求照片大小不符合要求", "IsCharge": false, "ErrorAddress": "00000", "RequestId": "sfzhy-mock-461"}
		default:
			resp = map[string]any{
				"Code":     0,
				"Message":  "请求成功",
				"IsCharge": true,
				"OutBizNo": req.OutBizNo,
				"Data": map[string]any{
					"Result":        1,
					"ResultMessage": "姓名与身份证号匹配，识别为同一人",
					"ImageScore":    932.26,
				},
				"RequestId": "sfzhy-mock-0",
			}
		}
		log.Printf("idverify <- name=%s idcard=%s -> Code=%v", req.Name, req.IDCard, resp["Code"])
		w.Header().Set("Content-Type", "application/json;charset=UTF-8")
		_ = json.NewEncoder(w).Encode(resp)
	})

	fmt.Printf("mock 身份证三要素核验 upstream listening on %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
