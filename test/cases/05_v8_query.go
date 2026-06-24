//go:build ignore

// 05_v8_query: v8 版本对外接口 POST /v1/openapi/zlx/querySrmxV8（与 x1 完全一致的
// 信封格式，仅路由名不同；内部对接 v8 独立上游）。全场景：成功/查无/各类鉴权与参数
// 错误/二次成功。
//
// Run: go run test/cases/05_v8_query.go
package main

import (
	"github.com/datahub/relay/test/harness"
)

const version = "v8"

func base() map[string]string {
	return map[string]string{"mobile": "13809091009", "idCard": "330129199109094312", "name": "张三"}
}

func main() {
	rec := harness.NewRecorder("05_v8_query", "v8 主接口全场景 (x1 信封格式)")
	defer rec.Finish()

	// 1. 成功
	r := harness.Query(version, harness.AppKey, harness.Secret, base(), nil)
	rec.Check("成功查得", "errorCode=0 & body.code=001 & range=7",
		r.ErrorCode == "0" && r.BodyCode == "001" && r.Range == "7", r.Raw)

	// 2. 查无（mock income: mobile=13800000000 -> code 999）
	nf := base()
	nf["mobile"] = "13800000000"
	r = harness.Query(version, harness.AppKey, harness.Secret, nf, nil)
	rec.Check("查无结果", "errorCode=0 & body.code=999", r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)

	// 3. 错误签名 -> 505002，无 body
	r = harness.Query(version, harness.AppKey, harness.Secret, base(), map[string]any{"sign": "deadbeef"})
	rec.Check("错误签名", "errorCode=505002 且无 body", r.ErrorCode == "505002" && r.BodyCode == "", r.Raw)

	// 4. 未知 appKey -> 505004
	r = harness.Query(version, "nonexistent-appkey", harness.Secret, base(), nil)
	rec.Check("未知 appKey", "errorCode=505004", r.ErrorCode == "505004", r.Raw)

	// 5. 缺失 appKey -> 505001
	r = harness.Query(version, "", harness.Secret, base(), map[string]any{"appKey": ""})
	rec.Check("缺失 appKey", "errorCode=505001", r.ErrorCode == "505001", r.Raw)

	// 6. 手机号非法 -> 505062
	badm := base()
	badm["mobile"] = "139xx"
	r = harness.Query(version, harness.AppKey, harness.Secret, badm, nil)
	rec.Check("手机号非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	// 7. 身份证非法 -> 505062
	badi := base()
	badi["idCard"] = "12345"
	r = harness.Query(version, harness.AppKey, harness.Secret, badi, nil)
	rec.Check("身份证非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	// 8. 二次成功（验证可重复）
	r = harness.Query(version, harness.AppKey, harness.Secret, base(), nil)
	rec.Check("二次成功查得", "errorCode=0 & body.code=001", r.ErrorCode == "0" && r.BodyCode == "001", r.Raw)
}
