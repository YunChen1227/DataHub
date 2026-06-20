//go:build ignore

// 05_ip_whitelist: 全局 IP 白名单（管理端）与 per-user 白名单的拦截效果。设为一个
// 不匹配本机来源 IP 的 CIDR 后，x1 应被拦 505002、v9 应被拦 012；用 defer 复原全局
// 白名单，避免影响后续用例。
//
// Run: go run test/cases/05_ip_whitelist.go
package main

import (
	"net/http"

	"github.com/datahub/relay/test/harness"
)

// 一个几乎不可能匹配本机来源 IP 的 CIDR（本机通常是 127.0.0.1 / ::1）。
const blockCIDR = "10.0.0.0/8"

func base() map[string]string {
	return map[string]string{"mobile": "13809091009", "idCard": "330129199109094312", "name": "张三"}
}

func getGlobalIP(auth map[string]string) []string {
	_, m, _ := harness.Call(http.MethodGet, "/admin/api/ip-whitelist", nil, auth)
	out := []string{}
	if arr, ok := m["cidrs"].([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func setGlobalIP(auth map[string]string, cidrs []string) {
	harness.Call(http.MethodPut, "/admin/api/ip-whitelist", map[string]any{"cidrs": cidrs}, auth)
}

func main() {
	rec := harness.NewRecorder("05_ip_whitelist", "全局/Per-user IP 白名单拦截")
	defer rec.Finish()

	token, raw := harness.AdminLogin()
	if token == "" {
		rec.Skip("IP 白名单", "管理端可配置白名单", "管理员登录失败: "+raw)
		return
	}
	auth := harness.AuthHeader(token)

	original := getGlobalIP(auth)
	defer setGlobalIP(auth, original) // 复原，保证后续用例不受影响

	// --- 全局白名单：设为不匹配 CIDR，应拦截 ---
	setGlobalIP(auth, []string{blockCIDR})

	r := harness.QueryX1(harness.AppKey, harness.Secret, base(), nil)
	rec.Check("全局白名单拦截 x1", "errorCode=505002", r.ErrorCode == "505002", r.Raw)

	rq := harness.ShortReqid("ip")
	v := harness.SignV9(harness.AppKey, "330129199109094312", "13809091009", rq, harness.Secret)
	v9 := harness.QueryV9(harness.AppKey, "330129199109094312", "张三", "13809091009", rq, v)
	rec.Check("全局白名单拦截 v9", "code=012", v9.Code == "012", v9.Raw)

	// 复原全局白名单后应恢复正常
	setGlobalIP(auth, original)
	r = harness.QueryX1(harness.AppKey, harness.Secret, base(), nil)
	rec.Check("复原全局白名单后放行", "errorCode=0", r.ErrorCode == "0", r.Raw)

	// --- per-user 白名单：临时用户设不匹配 CIDR，应拦截 ---
	testPerUser(rec, auth)
}

func testPerUser(rec *harness.Recorder, auth map[string]string) {
	st, m, cr := harness.Call(http.MethodPost, "/admin/api/users",
		map[string]any{"name": "ip-临时", "ipWhitelist": []string{}}, auth)
	if st != 200 {
		rec.Skip("per-user 白名单拦截", "errorCode=505002", "建用户失败: "+cr)
		return
	}
	user, _ := m["user"].(map[string]any)
	licenseID, _ := user["licenseId"].(string)
	appKey, _ := user["appKey"].(string)
	secret, _ := m["secret"].(string)
	defer harness.Call(http.MethodDelete, "/admin/api/users/"+licenseID, nil, auth)

	// 设 per-user 白名单为不匹配 CIDR
	harness.Call(http.MethodPatch, "/admin/api/users/"+licenseID,
		map[string]any{"ipWhitelist": []string{blockCIDR}}, auth)
	r := harness.QueryX1(appKey, secret, base(), nil)
	rec.Check("per-user 白名单拦截", "errorCode=505002", r.ErrorCode == "505002", r.Raw)
}
