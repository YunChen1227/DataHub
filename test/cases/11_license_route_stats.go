//go:build ignore

// 11_license_route_stats: 验证 v8/v9 共用同一 license（同一套 appKey/secret），
// 但调用次数(totalCalls)/成功查得数(serviceUsed)/操作日志按各自路由独立统计。
//
// 步骤：
//  1. 经管理后台在 v8 路由下新建一个用户 -> 得到 appKey A + secret S。
//  2. 在 v9 路由下用同样的检索能看到同一个 A（证明 license 共享，非各自一份）。
//  3. A/S 同时能在 v8 与 v9 两条路由鉴权通过（共享 license）。
//  4. 对 v8 发 2 次查得 + 1 次查无；对 v9 发 1 次查得。
//  5. /quotaV8(A/S): serviceUsed=2, totalCalls=3（查无也计调用次数）。
//     /quotaV9(A/S): serviceUsed=1, totalCalls=1。证明同一 license 下两路由计数独立。
//
// Run: go run test/cases/11_license_route_stats.go
package main

import (
	"fmt"
	"net/http"

	"github.com/datahub/relay/test/harness"
)

func foundBody() map[string]string {
	return map[string]string{"mobile": "13809091009", "idCard": "330129199109094312", "name": "张三"}
}

func notFoundBody() map[string]string {
	return map[string]string{"mobile": "13800000000", "idCard": "330129199109094312", "name": "张三"}
}

func main() {
	rec := harness.NewRecorder("11_license_route_stats", "v8/v9 共享 license + 路由独立统计")
	defer rec.Finish()

	token, raw := harness.AdminLogin()
	if token == "" {
		rec.Fail("管理员登录", "拿到 token", "", "登录失败: "+raw)
		return
	}
	auth := harness.AuthHeader(token)

	// 1. 在 v8 路由下新建用户。
	st, m, body := harness.Call(http.MethodPost, harness.AdminBase("v8")+"/users",
		map[string]string{"name": "共享license测试", "mobile": "13700007777"}, auth)
	if st != 200 {
		rec.Fail("v8 新建用户", "HTTP 200", fmt.Sprintf("HTTP %d", st), body)
		return
	}
	user, _ := m["user"].(map[string]any)
	appKey, _ := user["appKey"].(string)
	secret, _ := m["secret"].(string)
	if appKey == "" || secret == "" {
		rec.Fail("v8 新建用户返回 appKey/secret", "非空", fmt.Sprintf("appKey=%q secret空=%v", appKey, secret == ""), body)
		return
	}
	rec.Pass("v8 新建用户", "返回 appKey + secret", appKey)

	// 2. 同一用户应能在 v9 路由下检索到（license 共享，非各自一份）。
	_, lm, lraw := harness.Call(http.MethodGet, harness.AdminBase("v9")+"/users?q="+appKey, nil, auth)
	rec.Check("v9 能检索到 v8 新建的用户(license 共享)", "v9 用户列表含该 appKey",
		containsAppKey(lm, appKey), lraw)

	// 3. A/S 在 v8 与 v9 两条路由都能鉴权通过（共享 license）。
	r8 := harness.Query("v8", appKey, secret, foundBody(), nil)
	rec.Check("共享 license 在 v8 鉴权通过", "errorCode=0 & body.code=001",
		r8.ErrorCode == "0" && r8.BodyCode == "001", r8.Raw)
	r9 := harness.Query("v9", appKey, secret, foundBody(), nil)
	rec.Check("共享 license 在 v9 鉴权通过", "errorCode=0 & body.code=001",
		r9.ErrorCode == "0" && r9.BodyCode == "001", r9.Raw)

	// 4. 再对 v8 补一次查得 + 一次查无（此前 v8 已 1 次查得；v9 已 1 次查得）。
	//    目标累计：v8 -> 2 查得 + 1 查无 (totalCalls=3, serviceUsed=2)
	//             v9 -> 1 查得          (totalCalls=1, serviceUsed=1)
	r8b := harness.Query("v8", appKey, secret, foundBody(), nil)
	rec.Check("v8 第二次查得", "errorCode=0 & body.code=001",
		r8b.ErrorCode == "0" && r8b.BodyCode == "001", r8b.Raw)
	r8nf := harness.Query("v8", appKey, secret, notFoundBody(), nil)
	rec.Check("v8 查无", "errorCode=0 & body.code=999",
		r8nf.ErrorCode == "0" && r8nf.BodyCode == "999", r8nf.Raw)

	// 5. 校验两路由计数独立。
	v8Used := harness.ServiceUsed("v8", appKey, secret)
	v8Calls := harness.TotalCalls("v8", appKey, secret)
	v9Used := harness.ServiceUsed("v9", appKey, secret)
	v9Calls := harness.TotalCalls("v9", appKey, secret)
	fmt.Printf("  v8: used=%v calls=%v | v9: used=%v calls=%v\n", v8Used, v8Calls, v9Used, v9Calls)

	rec.Check("v8 成功查得数 == 2", "serviceUsed(v8)=2",
		v8Used == 2, fmt.Sprintf("%v", v8Used))
	rec.Check("v8 调用次数 == 3 (含查无)", "totalCalls(v8)=3",
		v8Calls == 3, fmt.Sprintf("%v", v8Calls))
	rec.Check("v9 成功查得数 == 1 (与 v8 独立)", "serviceUsed(v9)=1",
		v9Used == 1, fmt.Sprintf("%v", v9Used))
	rec.Check("v9 调用次数 == 1 (与 v8 独立)", "totalCalls(v9)=1",
		v9Calls == 1, fmt.Sprintf("%v", v9Calls))
}

// containsAppKey reports whether an admin /users response contains the appKey.
func containsAppKey(m map[string]any, appKey string) bool {
	users, ok := m["users"].([]any)
	if !ok {
		return false
	}
	for _, u := range users {
		if um, ok := u.(map[string]any); ok {
			if k, _ := um["appKey"].(string); k == appKey {
				return true
			}
		}
	}
	return false
}
