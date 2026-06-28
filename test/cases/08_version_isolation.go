//go:build ignore

// 08_version_isolation: 验证按「域」隔离 + v8/v9 共享 license。
//   - 在 v9 后台建的用户：v9 与 v8 (同属 v8v9 域，共享 license) 都可见/可鉴权；
//     x1 等其它域看不到。
//   - 用未知版本路径访问后台 -> 404。
//
// Run: go run test/cases/08_version_isolation.go
package main

import (
	"net/http"
	"strings"

	"github.com/datahub/relay/test/harness"
)

func base() map[string]string {
	return map[string]string{"mobile": "13809091009", "idCard": "330129199109094312", "name": "张三"}
}

func main() {
	rec := harness.NewRecorder("08_version_isolation", "域隔离 + v8/v9 共享 license")
	defer rec.Finish()

	token, raw := harness.AdminLogin()
	rec.Check("登录(正确)", "返回 token", token != "", raw)
	if token == "" {
		return
	}
	auth := harness.AuthHeader(token)

	// 1. 在 v9 版本后台建用户。
	st, m, cr := harness.Call(http.MethodPost, harness.AdminBase("v9")+"/users",
		map[string]any{"name": "iso-v9-临时", "mobile": "13800003333"}, auth)
	user, _ := m["user"].(map[string]any)
	licenseID, _ := user["licenseId"].(string)
	appKey, _ := user["appKey"].(string)
	secret, _ := m["secret"].(string)
	rec.Check("v9 建用户", "返回 user + secret", st == 200 && appKey != "" && secret != "", cr)
	if licenseID == "" {
		return
	}
	defer harness.Call(http.MethodDelete, harness.AdminBase("v9")+"/users/"+licenseID, nil, auth)

	// 2. 该用户在 v9 路由可成功鉴权查得。
	rv9 := harness.Query("v9", appKey, secret, base(), nil)
	rec.Check("v9 新用户可查得", "errorCode=0 & body.code=001",
		rv9.ErrorCode == "0" && rv9.BodyCode == "001", rv9.Raw)

	// 3. 同一 appKey 在 x1 路由应查无账户 -> 505004（不同域，license 隔离）；
	//    但在 v8 路由可成功鉴权查得（v8/v9 同域共享 license）。
	rx1 := harness.Query("x1", appKey, secret, base(), nil)
	rec.Check("x1(不同域)看不到 v9 用户", "errorCode=505004", rx1.ErrorCode == "505004", rx1.Raw)
	rv8 := harness.Query("v8", appKey, secret, base(), nil)
	rec.Check("v8(同域)可用 v9 创建的 license 查得", "errorCode=0 & body.code=001",
		rv8.ErrorCode == "0" && rv8.BodyCode == "001", rv8.Raw)

	// 4. 该用户在 v9 与 v8 后台都可查到（共享 license），x1 后台查不到（不同域）。
	stG, _, _ := harness.Call(http.MethodGet, harness.AdminBase("v9")+"/users/"+licenseID, nil, auth)
	rec.Check("v9 后台可查到该用户", "HTTP 200", stG == 200, "status不为200")
	stV8, _, _ := harness.Call(http.MethodGet, harness.AdminBase("v8")+"/users/"+licenseID, nil, auth)
	rec.Check("v8 后台可查到该用户(共享 license)", "HTTP 200", stV8 == 200, "status不为200")
	stX, _, _ := harness.Call(http.MethodGet, harness.AdminBase("x1")+"/users/"+licenseID, nil, auth)
	rec.Check("x1 后台查不到 v9 用户(不同域)", "HTTP 404", stX == 404, "status不为404")

	// 5. 未知版本路径 -> 404。
	stU, _, ur := harness.Call(http.MethodGet, "/admin/api/v7/users", nil, auth)
	rec.Check("未知版本后台路径", "HTTP 404", stU == 404 && strings.Contains(ur, "版本"), "status="+itoa(stU)+" "+ur)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
