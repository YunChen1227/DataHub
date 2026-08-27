//go:build ignore

// Mock 身份证实名核验 (数脉) upstream implementing
// POST /v4/id_card/check (form) for sfsm full-link testing.
// Run: go run scripts/mock_idcheck.go
//
// Verifies sign = md5(appid&timestamp&app_security), then routes:
//   - bad sign / unknown appid   -> code 400 参数错误 (上游侧错误 -> 网关 505062)
//   - idcard == notFoundIDCard   -> code 200 / result 2 无记录 (查无, 不收费)
//   - idcard == mismatchIDCard   -> code 200 / result 1 不一致 (收费, 仍是查得)
//   - idcard == unpaidIDCard     -> code 603 余额不足 (上游侧错误, 不收费)
//   - otherwise                  -> code 200 / result 0 一致 (收费) + rich data
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

// demoCreds 与 test/route.ps1 的 sfsm 配置块保持一致（全部为假值，仅本地可用）。
var demoCreds = map[string]string{
	"demo-sfsm-appid": "demo-sfsm-secret",
}

func main() {
	addr := env("MOCK_IDCHECK_ADDR", ":9127")
	// 约定的测试身份证号（均为合法 18 位格式，用来驱动各归一化分支）。
	notFoundIDCard := env("SFSM_NOTFOUND_IDCARD", "000000000000000404")
	mismatchIDCard := env("SFSM_MISMATCH_IDCARD", "000000000000000001")
	unpaidIDCard := env("SFSM_UNPAID_IDCARD", "000000000000000603")

	http.HandleFunc("/v4/id_card/check", func(w http.ResponseWriter, r *http.Request) {
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
		case name == "" || idcard == "":
			// 上游参数表 name/idcard 均必填；网关本应前置拦截，此处兜底以暴露漏拦。
			resp = map[string]any{"msg": "参数错误", "success": false, "code": 400, "data": map[string]any{}}
		case idcard == unpaidIDCard:
			resp = map[string]any{"msg": "余额不足请充值", "success": false, "code": 603, "data": map[string]any{}}
		case idcard == notFoundIDCard:
			resp = map[string]any{
				"msg": "成功", "success": true, "code": 200,
				"data": map[string]any{
					"result":   2,
					"order_no": "sfsm-mock-999",
					"desc":     "无记录",
				},
			}
		case idcard == mismatchIDCard:
			resp = map[string]any{
				"msg": "成功", "success": true, "code": 200,
				"data": map[string]any{
					"result":   1,
					"order_no": "sfsm-mock-mismatch",
					"desc":     "不一致",
					"sex":      "男",
					"birthday": "199***20",
					"address":  "江西省**市**区",
				},
			}
		default:
			resp = map[string]any{
				"msg": "成功", "success": true, "code": 200,
				"data": map[string]any{
					"result":   0,
					"order_no": "sfsm-mock-001",
					"desc":     "一致",
					"sex":      "男",
					"birthday": "199***20",
					"address":  "江西省**市**区",
				},
			}
		}
		if d, ok := resp["data"].(map[string]any); ok {
			log.Printf("idcheck <- name=%s idcard=%s -> code=%v result=%v", name, idcard, resp["code"], d["result"])
		} else {
			log.Printf("idcheck <- name=%s idcard=%s -> code=%v", name, idcard, resp["code"])
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	})

	fmt.Printf("mock 身份证实名核验 upstream listening on %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
