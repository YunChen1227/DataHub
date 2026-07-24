//go:build ignore

// Mock 消费交易特征 (data-bean) upstream implementing POST / (JSON) for xfjy
// full-link testing. Run: go run scripts/mock_consumetxn.go
//
// Verifies sign = MD5(过滤空值后升序 "k=v&k=v..."（procode/sceneid/reqtime/nonce
// 与 params 扁平化后一起）+ "&appkey=" + appkey), then routes:
//   - unknown sceneid / bad sign -> code "1001" 签名校验失败 (上游侧错误 -> 网关 505062)
//   - mobile == notFoundMobile   -> code "0" / data.result "1" 未查得 (不计费 -> 999)
//   - otherwise                  -> code "0" / data.result "0" 查得 + rich resultdata
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

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func main() {
	addr := env("MOCK_CONSUMETXN_ADDR", ":9119")
	sceneID := env("XFJY_SCENEID", "demo-xfjy-sceneid")
	appKey := env("XFJY_APPKEY", "demo-xfjy-appkey")
	// 约定「查无」触发手机号（合法格式），驱动 data.result="1" 场景。
	notFoundMobile := env("XFJY_NOTFOUND_MOBILE", "13800000000")

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Procode string            `json:"procode"`
			SceneID string            `json:"sceneid"`
			Reqtime string            `json:"reqtime"`
			Nonce   string            `json:"nonce"`
			Sign    string            `json:"sign"`
			Params  map[string]string `json:"params"`
		}
		_ = json.Unmarshal(raw, &req)

		fields := map[string]string{
			"procode": req.Procode,
			"sceneid": req.SceneID,
			"reqtime": req.Reqtime,
			"nonce":   req.Nonce,
		}
		for k, v := range req.Params {
			fields[k] = v
		}
		keys := make([]string, 0, len(fields))
		for k, v := range fields {
			if v != "" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		var sb strings.Builder
		for i, k := range keys {
			if i > 0 {
				sb.WriteString("&")
			}
			sb.WriteString(k)
			sb.WriteString("=")
			sb.WriteString(fields[k])
		}
		sb.WriteString("&appkey=")
		sb.WriteString(appKey)
		want := md5hex(sb.String())

		var resp map[string]any
		switch {
		case req.SceneID != sceneID || req.Sign != want:
			resp = map[string]any{"code": "1001", "msg": "签名校验失败", "reqno": "xfjy-mock-1001"}
		case req.Params["mobile"] == notFoundMobile:
			resp = map[string]any{
				"code":  "0",
				"msg":   "请求成功",
				"reqno": "xfjy-mock-999",
				"data":  map[string]any{"result": "1"},
			}
		default:
			resp = map[string]any{
				"code":  "0",
				"msg":   "请求成功",
				"reqno": "xfjy-mock-001",
				"data": map[string]any{
					"result": "0",
					"resultdata": map[string]any{
						"consumeLevel":    "高",
						"txnCount6m":      128,
						"txnAmount6m":     45678.90,
						"activeMonths12m": 11,
						"lastTxnDate":     "2026-06-30",
					},
				},
			}
		}
		if d, ok := resp["data"].(map[string]any); ok {
			log.Printf("consumetxn <- name=%s idcard=%s mobile=%s -> code=%v result=%v",
				req.Params["name"], req.Params["idcard"], req.Params["mobile"], resp["code"], d["result"])
		} else {
			log.Printf("consumetxn <- sceneid=%s -> code=%v", req.SceneID, resp["code"])
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	fmt.Printf("mock 消费交易特征 (data-bean) upstream listening on %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
