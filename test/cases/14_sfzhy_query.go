//go:build ignore

// 14_sfzhy_query: sfzhy 版本 POST /v1/openapi/zlx/querySrmxSFZHY（x1 信封格式；
// 内部对接身份证三要素核验 mock）。全场景：成功查得(富对象 JSON range)/上游
// 错误码归一 error/鉴权与参数错误。三要素核验无「查无」概念，故不测 999。
//
// Run: go run test/cases/14_sfzhy_query.go
package main

import (
	"strings"

	"github.com/datahub/relay/test/harness"
)

const version = "sfzhy"

// 与 scripts/mock_idverify.go 约定的「上游错误」触发身份证号（合法 18 位格式）。
const errIDCard = "000000000000000007"

func base() map[string]string {
	return map[string]string{
		"name":           "张三",
		"idCard":         "420101198012010011",
		"profilePicture": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
	}
}

func main() {
	rec := harness.NewRecorder("14_sfzhy_query", "sfzhy 主接口全场景 (身份证三要素核验)")
	defer rec.Finish()

	r := harness.Query(version, harness.AppKeyFor(version), harness.Secret, base(), nil)
	rec.Check("成功查得", "errorCode=0 & body.code=001 & range 含 Result",
		r.ErrorCode == "0" && r.BodyCode == "001" && strings.Contains(r.Range, "Result"), r.Raw)

	// 上游错误码（照片不符合要求 Code=461）→ 归一为上游侧错误 → 505062。
	up := base()
	up["idCard"] = errIDCard
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, up, nil)
	rec.Check("上游错误码归一 error", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, base(), map[string]any{"sign": "deadbeef"})
	rec.Check("错误签名", "errorCode=505002 且无 body", r.ErrorCode == "505002" && r.BodyCode == "", r.Raw)

	r = harness.Query(version, "nonexistent-appkey", harness.Secret, base(), nil)
	rec.Check("未知 appKey", "errorCode=505004", r.ErrorCode == "505004", r.Raw)

	r = harness.Query(version, "", harness.Secret, base(), map[string]any{"appKey": ""})
	rec.Check("缺失 appKey", "errorCode=505001", r.ErrorCode == "505001", r.Raw)

	// 身份证格式非法：网关前置拦截，不调用上游。
	badi := base()
	badi["idCard"] = "12345"
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, badi, nil)
	rec.Check("身份证非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	// 15 位身份证号应被接受（上游支持 15/18 位），走成功查得。
	id15 := base()
	id15["idCard"] = "420101800120100"
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, id15, nil)
	rec.Check("15 位身份证查得", "errorCode=0 & body.code=001",
		r.ErrorCode == "0" && r.BodyCode == "001", r.Raw)

	// name 必填：网关前置拦截。
	noName := base()
	delete(noName, "name")
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, noName, nil)
	rec.Check("缺 name 拦截", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	// profilePicture 必填：缺失 → 前置拦截。
	noPic := base()
	delete(noPic, "profilePicture")
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, noPic, nil)
	rec.Check("缺 profilePicture 拦截", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, base(), nil)
	rec.Check("二次成功查得", "errorCode=0 & body.code=001 & range 含 ImageScore",
		r.ErrorCode == "0" && r.BodyCode == "001" && strings.Contains(r.Range, "ImageScore"), r.Raw)
}
