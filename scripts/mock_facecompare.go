//go:build ignore

// Mock 人脸身份证比对一所 (数脉) upstream implementing
// POST /v4/face_id_card/yisuo/compare (form) for rlbd1/rlbd2 full-link testing.
// Run: go run scripts/mock_facecompare.go
//
// Verifies sign = md5(appid&timestamp&app_security), then routes:
//   - bad sign / unknown appid -> code 400 参数错误 (上游侧错误 -> 网关 505062)
//   - idcard == unpaidIDCard    -> code 200 / incorrect 107 (照片质量不合格, 不收费)
//   - otherwise                 -> code 200 / incorrect 100 (比对成功) + rich data
package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
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

// demoCreds 允许 rlbd1/rlbd2 等多路由共用同一 mock，各自独立 appId/appSecret。
var demoCreds = map[string]string{
	"demo-rlbd1-appid": "demo-rlbd1-secret",
	"demo-rlbd2-appid": "demo-rlbd2-secret",
}

func main() {
	addr := env("MOCK_FACECOMPARE_ADDR", ":9117")
	// 约定「不收费」触发用身份证号（合法 18 位格式，供测试驱动 incorrect=107 场景）。
	unpaidIDCard := env("RLBD1_UNPAID_IDCARD", "000000000000000007")

	http.HandleFunc("/v4/face_id_card/yisuo/compare", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotAppID := r.FormValue("appid")
		timestamp := r.FormValue("timestamp")
		sign := r.FormValue("sign")
		idcard := r.FormValue("idcard")
		name := r.FormValue("name")

		appSecret, ok := demoCreds[gotAppID]
		want := md5hex(gotAppID + "&" + timestamp + "&" + appSecret)

		var resp map[string]any
		switch {
		case !ok || sign != want:
			resp = map[string]any{"msg": "参数错误", "success": false, "code": 400, "data": map[string]any{}}
		case idcard == unpaidIDCard:
			resp = map[string]any{
				"msg": "成功", "success": true, "code": 200,
				"data": map[string]any{
					"order_no":  "rlbd1-mock-107",
					"score":     0,
					"msg":       "照片质量不合格",
					"incorrect": 107,
				},
			}
		default:
			resp = map[string]any{
				"msg": "成功", "success": true, "code": 200,
				"data": map[string]any{
					"order_no":  "rlbd1-mock-001",
					"score":     932.26,
					"msg":       "系统判断为同一人",
					"incorrect": 100,
					"sex":       "男",
					"birthday":  "19930123",
					"address":   "江西省吉安地区遂川县",
				},
			}
		}
		if d, ok := resp["data"].(map[string]any); ok {
			log.Printf("facecompare <- name=%s idcard=%s -> code=%v incorrect=%v", name, idcard, resp["code"], d["incorrect"])
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	})

	fmt.Printf("mock 人脸身份证比对一所 upstream listening on %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
