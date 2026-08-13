//go:build ignore

// 19_grgjj_query: grgjj 版本 POST /v1/openapi/zlx/querySrmxGRGJJ（x1 信封格式；
// 内部对接收入A_g版 yrzx mock，3DES 加密 data + MD5 加签）。全场景：查得(001，range
// 为解密后 {cbjfzt,jfjs,jfsj} 的 JSON)/查无(999)/鉴权与参数错误。
//
// Run: go run test/cases/19_grgjj_query.go
package main

import (
	"strings"

	"github.com/datahub/relay/test/harness"
)

const version = "grgjj"

func base() map[string]string {
	return map[string]string{
		"mobile": "13809091009",
		"idCard": "330129199109094312",
		"name":   "张三",
	}
}

func main() {
	rec := harness.NewRecorder("19_grgjj_query", "grgjj 主接口全场景 (收入A_g版 yrzx, 3DES data)")
	defer rec.Finish()

	appKey := harness.AppKeyFor(version)

	r := harness.Query(version, appKey, harness.Secret, base(), nil)
	rec.Check("成功查得", "errorCode=0 & body.code=001 & range 含 cbjfzt (解密后业务对象)",
		r.ErrorCode == "0" && r.BodyCode == "001" && strings.Contains(r.Range, "cbjfzt"), r.Raw)

	// 查无手机号 13800000000：上游回 code 999，不计费。
	nf := base()
	nf["mobile"] = "13800000000"
	r = harness.Query(version, appKey, harness.Secret, nf, nil)
	rec.Check("查无结果", "errorCode=0 & body.code=999", r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, base(), map[string]any{"sign": "deadbeef"})
	rec.Check("错误签名", "errorCode=505002 且无 body", r.ErrorCode == "505002" && r.BodyCode == "", r.Raw)

	r = harness.Query(version, "nonexistent-appkey", harness.Secret, base(), nil)
	rec.Check("未知 appKey", "errorCode=505004", r.ErrorCode == "505004", r.Raw)

	r = harness.Query(version, "", harness.Secret, base(), map[string]any{"appKey": ""})
	rec.Check("缺失 appKey", "errorCode=505001", r.ErrorCode == "505001", r.Raw)

	// data 三要素 name/cid/mobile 均必填：网关前置拦截，不调用上游。
	badm := base()
	badm["mobile"] = "139xx"
	r = harness.Query(version, appKey, harness.Secret, badm, nil)
	rec.Check("手机号非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	noMobile := base()
	delete(noMobile, "mobile")
	r = harness.Query(version, appKey, harness.Secret, noMobile, nil)
	rec.Check("缺失 mobile", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	badID := base()
	badID["idCard"] = "12345"
	r = harness.Query(version, appKey, harness.Secret, badID, nil)
	rec.Check("身份证非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	noID := base()
	delete(noID, "idCard")
	r = harness.Query(version, appKey, harness.Secret, noID, nil)
	rec.Check("缺失 idCard", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	noName := base()
	delete(noName, "name")
	r = harness.Query(version, appKey, harness.Secret, noName, nil)
	rec.Check("缺 name 拦截", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, base(), nil)
	rec.Check("二次成功查得", "errorCode=0 & body.code=001 & range 含 jfjs",
		r.ErrorCode == "0" && r.BodyCode == "001" && strings.Contains(r.Range, "jfjs"), r.Raw)
}
