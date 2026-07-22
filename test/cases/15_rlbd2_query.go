//go:build ignore

// 15_rlbd2_query: rlbd2 版本 POST /v1/openapi/zlx/querySrmxRLBD2（x1 信封格式；
// 内部对接人脸身份证比对一所 mock，与 rlbd1 同一上游接口，仅 appId/appSecret 不同）。
// 全场景：成功查得(富对象 JSON range)/上游不收费码归一 error/鉴权与参数错误。
// 人脸比对无「查无」概念，故不测 999。
//
// Run: go run test/cases/15_rlbd2_query.go
package main

import (
	"strings"

	"github.com/datahub/relay/test/harness"
)

const version = "rlbd2"

// 与 scripts/mock_facecompare.go 约定的「不收费」触发身份证号（合法 18 位格式）。
const unpaidIDCard = "000000000000000007"

func base() map[string]string {
	return map[string]string{
		"name":   "张三",
		"idCard": "330129199109094312",
		"url":    "https://example.com/face.jpg",
	}
}

func main() {
	rec := harness.NewRecorder("15_rlbd2_query", "rlbd2 主接口全场景 (人脸身份证比对一所, 独立凭证)")
	defer rec.Finish()

	r := harness.Query(version, harness.AppKeyFor(version), harness.Secret, base(), nil)
	rec.Check("成功查得", "errorCode=0 & body.code=001 & range 含 incorrect",
		r.ErrorCode == "0" && r.BodyCode == "001" && strings.Contains(r.Range, "incorrect"), r.Raw)

	// 上游不收费结论（照片质量不合格 incorrect=107）→ 归一为上游侧错误 → 505062。
	up := base()
	up["idCard"] = unpaidIDCard
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, up, nil)
	rec.Check("上游不收费码归一 error", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

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

	// name 必填：网关前置拦截。
	noName := base()
	delete(noName, "name")
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, noName, nil)
	rec.Check("缺 name 拦截", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	// image 与 url 二选一必填：都缺 → 前置拦截。
	noImg := base()
	delete(noImg, "url")
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, noImg, nil)
	rec.Check("缺 image/url 拦截", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	// 跨域隔离：rlbd1 的 demo appKey 不能用于 rlbd2 路由（各自独立 license）。
	r = harness.Query(version, harness.AppKeyFor("rlbd1"), harness.Secret, base(), nil)
	rec.Check("rlbd1 凭证跨域到 rlbd2 被拒", "errorCode=505004", r.ErrorCode == "505004", r.Raw)

	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, base(), nil)
	rec.Check("二次成功查得", "errorCode=0 & body.code=001 & range 含 order_no",
		r.ErrorCode == "0" && r.BodyCode == "001" && strings.Contains(r.Range, "order_no"), r.Raw)
}
