//go:build ignore

// 18_lxf_query: lxf 版本 POST /v1/openapi/zlx/querySrmxLXF（x1 信封格式；
// 内部对接灵犀分 score_195_v1 fullink mock）。全场景：查得(001，300-900 评分经
// range 透出)/查无(999，上游分数 -1)/鉴权与参数错误。
//
// Run: go run test/cases/18_lxf_query.go
package main

import (
	"strconv"

	"github.com/datahub/relay/test/harness"
)

const version = "lxf"

func base() map[string]string {
	return map[string]string{
		"mobile": "13809091009",
		"idCard": "440308199901011234",
		"name":   "张三",
	}
}

// scoreInRange 断言 range 是 300-900 的评分（文档 §2.4 业务参数：分数越高风险越低）。
func scoreInRange(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n >= 300 && n <= 900
}

func main() {
	rec := harness.NewRecorder("18_lxf_query", "lxf 主接口全场景 (灵犀分 score_195_v1 fullink)")
	defer rec.Finish()

	r := harness.Query(version, harness.AppKeyFor(version), harness.Secret, base(), nil)
	rec.Check("成功查得", "errorCode=0 & body.code=001 & range 为 300-900 评分",
		r.ErrorCode == "0" && r.BodyCode == "001" && scoreInRange(r.Range), r.Raw)

	// name 选填：上游 name 缺省时按文档传 MD5("")，仍应查得。
	noName := base()
	delete(noName, "name")
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, noName, nil)
	rec.Check("缺 name 仍查得", "errorCode=0 & body.code=001",
		r.ErrorCode == "0" && r.BodyCode == "001" && scoreInRange(r.Range), r.Raw)

	// 查无手机号 13800000000：上游回分数 -1 → 归一 999，不计费。
	nf := base()
	nf["mobile"] = "13800000000"
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, nf, nil)
	rec.Check("查无", "errorCode=0 & body.code=999", r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)

	// 上游业务失败（mock 对 13700000000 回 status=500 / 2031225 重复请求拒绝）：
	// 归一为 505062 不计费。审计须带上游 code/uid/logId（customerRequestId）——
	// 失败也要可向上游对账追查。
	ue := base()
	ue["mobile"] = "13700000000"
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, ue, nil)
	rec.Check("上游业务失败归一", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, base(), map[string]any{"sign": "deadbeef"})
	rec.Check("错误签名", "errorCode=505002 且无 body", r.ErrorCode == "505002" && r.BodyCode == "", r.Raw)

	r = harness.Query(version, "nonexistent-appkey", harness.Secret, base(), nil)
	rec.Check("未知 appKey", "errorCode=505004", r.ErrorCode == "505004", r.Raw)

	r = harness.Query(version, "", harness.Secret, base(), map[string]any{"appKey": ""})
	rec.Check("缺失 appKey", "errorCode=505001", r.ErrorCode == "505001", r.Raw)

	// mobile 必填且须合法：网关前置拦截，不调用上游。
	badm := base()
	badm["mobile"] = "139xx"
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, badm, nil)
	rec.Check("手机号非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	noMobile := base()
	delete(noMobile, "mobile")
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, noMobile, nil)
	rec.Check("缺失 mobile", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	// idCard 必填且须合法：网关前置拦截。
	badID := base()
	badID["idCard"] = "12345"
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, badID, nil)
	rec.Check("身份证非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	noID := base()
	delete(noID, "idCard")
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, noID, nil)
	rec.Check("缺失 idCard", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, base(), nil)
	rec.Check("二次成功查得", "errorCode=0 & body.code=001 & range 为 300-900 评分",
		r.ErrorCode == "0" && r.BodyCode == "001" && scoreInRange(r.Range), r.Raw)
}
