//go:build ignore

// 02_x1_query: 主接口 POST /v1/openapi/zlx/querySrmxX1 全场景（成功/查无/各类
// 鉴权与参数错误/SUSPENDED）。SUSPENDED 通过管理端临时建一个用户并停用后验证，
// 用完即删。
//
// Run: go run test/cases/02_x1_query.go
package main

import (
	"net/http"

	"github.com/datahub/relay/test/harness"
)

func base() map[string]string {
	return map[string]string{"mobile": "13809091009", "idCard": "330129199109094312", "name": "张三"}
}

func main() {
	rec := harness.NewRecorder("02_x1_query", "x1 主接口全场景")
	defer rec.Finish()

	// 1. 成功
	r := harness.QueryX1(harness.AppKey, harness.Secret, base(), nil)
	rec.Check("成功查得", "errorCode=0 & body.code=001 & range=7",
		r.ErrorCode == "0" && r.BodyCode == "001" && r.Range == "7", r.Raw)

	// 2. 查无（mock: mobile=13800000000 -> busiCode 1000）
	nf := base()
	nf["mobile"] = "13800000000"
	r = harness.QueryX1(harness.AppKey, harness.Secret, nf, nil)
	rec.Check("查无结果", "errorCode=0 & body.code=999", r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)

	// 3. 错误签名 -> 505002，无 body
	r = harness.QueryX1(harness.AppKey, harness.Secret, base(), map[string]any{"sign": "deadbeef"})
	rec.Check("错误签名", "errorCode=505002 且无 body", r.ErrorCode == "505002" && r.BodyCode == "", r.Raw)

	// 4. 未知 appKey -> 505004
	r = harness.QueryX1("nonexistent-appkey", harness.Secret, base(), nil)
	rec.Check("未知 appKey", "errorCode=505004", r.ErrorCode == "505004", r.Raw)

	// 5. 缺失 appKey -> 505001
	r = harness.QueryX1("", harness.Secret, base(), map[string]any{"appKey": ""})
	rec.Check("缺失 appKey", "errorCode=505001", r.ErrorCode == "505001", r.Raw)

	// 6. 手机号非法 -> 505062
	badm := base()
	badm["mobile"] = "139xx"
	r = harness.QueryX1(harness.AppKey, harness.Secret, badm, nil)
	rec.Check("手机号非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	// 7. 身份证非法 -> 505062
	badi := base()
	badi["idCard"] = "12345"
	r = harness.QueryX1(harness.AppKey, harness.Secret, badi, nil)
	rec.Check("身份证非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	// 8. 二次成功（验证可重复）
	r = harness.QueryX1(harness.AppKey, harness.Secret, base(), nil)
	rec.Check("二次成功查得", "errorCode=0 & body.code=001", r.ErrorCode == "0" && r.BodyCode == "001", r.Raw)

	// 9. SUSPENDED 用户 -> 505007（借管理端临时用户）
	testSuspended(rec)
}

func testSuspended(rec *harness.Recorder) {
	token, raw := harness.AdminLogin()
	if token == "" {
		rec.Skip("SUSPENDED 用户拦截", "errorCode=505007", "管理员登录失败: "+raw)
		return
	}
	auth := harness.AuthHeader(token)
	adminBase := harness.AdminBase("x1")

	// create
	st, m, cr := harness.Call(http.MethodPost, adminBase+"/users",
		map[string]any{"name": "x1-suspend-临时", "mobile": "13800002222"}, auth)
	if st != 200 {
		rec.Skip("SUSPENDED 用户拦截", "errorCode=505007", "建用户失败: "+cr)
		return
	}
	user, _ := m["user"].(map[string]any)
	licenseID, _ := user["licenseId"].(string)
	appKey, _ := user["appKey"].(string)
	secret, _ := m["secret"].(string)
	defer harness.Call(http.MethodDelete, adminBase+"/users/"+licenseID, nil, auth)

	// 健全性：新用户可成功查得
	r := harness.QueryX1(appKey, secret, base(), nil)
	rec.Check("新建用户可查得", "errorCode=0 & body.code=001", r.ErrorCode == "0" && r.BodyCode == "001", r.Raw)

	// suspend
	stp, _, pr := harness.Call(http.MethodPatch, adminBase+"/users/"+licenseID,
		map[string]any{"status": "SUSPENDED"}, auth)
	if stp != 200 {
		rec.Skip("SUSPENDED 用户拦截", "errorCode=505007", "停用失败: "+pr)
		return
	}
	r = harness.QueryX1(appKey, secret, base(), nil)
	rec.Check("SUSPENDED 用户拦截", "errorCode=505007", r.ErrorCode == "505007", r.Raw)
}
