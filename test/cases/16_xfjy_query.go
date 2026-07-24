//go:build ignore

// 16_xfjy_query: xfjy 版本 POST /v1/openapi/zlx/querySrmxXFJY（x1 信封格式；
// 内部对接消费交易特征 data-bean mock）。全场景：成功查得(富对象 JSON range)/
// 查无(999)/上游错误码归一 error/鉴权与参数错误。消费交易特征区分查得/查无，
// 故测 001 与 999。
//
// Run: go run test/cases/16_xfjy_query.go
package main

import (
	"strings"

	"github.com/datahub/relay/test/harness"
)

const version = "xfjy"

func base() map[string]string {
	return map[string]string{
		"name":   "张三",
		"idCard": "330129199109094312",
		"mobile": "13809091009",
	}
}

func main() {
	rec := harness.NewRecorder("16_xfjy_query", "xfjy 主接口全场景 (消费交易特征 data-bean)")
	defer rec.Finish()

	r := harness.Query(version, harness.AppKeyFor(version), harness.Secret, base(), nil)
	rec.Check("成功查得", "errorCode=0 & body.code=001 & range 含 consumeLevel",
		r.ErrorCode == "0" && r.BodyCode == "001" && strings.Contains(r.Range, "consumeLevel"), r.Raw)

	// 查无：约定手机号 13800000000 触发上游 data.result=1 → 归一 999 (不计费)。
	nf := base()
	nf["mobile"] = "13800000000"
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, nf, nil)
	rec.Check("查无结果", "errorCode=0 & body.code=999", r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)

	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, base(), map[string]any{"sign": "deadbeef"})
	rec.Check("错误签名", "errorCode=505002 且无 body", r.ErrorCode == "505002" && r.BodyCode == "", r.Raw)

	r = harness.Query(version, "nonexistent-appkey", harness.Secret, base(), nil)
	rec.Check("未知 appKey", "errorCode=505004", r.ErrorCode == "505004", r.Raw)

	r = harness.Query(version, "", harness.Secret, base(), map[string]any{"appKey": ""})
	rec.Check("缺失 appKey", "errorCode=505001", r.ErrorCode == "505001", r.Raw)

	// 手机号格式非法：网关前置拦截，不调用上游。
	badm := base()
	badm["mobile"] = "139xx"
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, badm, nil)
	rec.Check("手机号非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	// 身份证格式非法：网关前置拦截。
	badi := base()
	badi["idCard"] = "12345"
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, badi, nil)
	rec.Check("身份证非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	// 无任何查询要素 (name/idCard/mobile 均缺失)：网关前置拦截，不调用上游。
	empty := map[string]string{"authlet": "abc123"}
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, empty, nil)
	rec.Check("无查询要素拦截", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	// 仅 idCard（其余选填省略）应被接受，走成功查得。
	only := map[string]string{"idCard": "330129199109094312"}
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, only, nil)
	rec.Check("仅 idCard 查得", "errorCode=0 & body.code=001",
		r.ErrorCode == "0" && r.BodyCode == "001", r.Raw)

	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, base(), nil)
	rec.Check("二次成功查得", "errorCode=0 & body.code=001 & range 含 txnCount6m",
		r.ErrorCode == "0" && r.BodyCode == "001" && strings.Contains(r.Range, "txnCount6m"), r.Raw)
}
