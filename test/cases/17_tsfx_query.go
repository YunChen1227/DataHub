//go:build ignore

// 17_tsfx_query: tsfx 版本 POST /v1/openapi/zlx/querySrmxTSFX（x1 信封格式；
// 内部对接投诉分析识别名单 kfongtech mock）。全场景：调用成功即计费(001，命中状态
// forbid 随结果数组经 range 透出)/上游错误码归一 error/鉴权与参数错误。
// 注：本上游无独立「查无(999)」业务码——未命中体现在记录级 forbid=0，仍为 001 计费。
//
// Run: go run test/cases/17_tsfx_query.go
package main

import (
	"strings"

	"github.com/datahub/relay/test/harness"
)

const version = "tsfx"

func base() map[string]string {
	return map[string]string{
		"mobile": "13809091009",
		"poly":   "C1", // 命中级别策略 (C1 高危 / C2 敏感 / C3 一般)
	}
}

func main() {
	rec := harness.NewRecorder("17_tsfx_query", "tsfx 主接口全场景 (投诉分析识别名单 kfongtech)")
	defer rec.Finish()

	r := harness.Query(version, harness.AppKeyFor(version), harness.Secret, base(), nil)
	rec.Check("成功查得(命中)", "errorCode=0 & body.code=001 & range 含 forbid",
		r.ErrorCode == "0" && r.BodyCode == "001" && strings.Contains(r.Range, "forbid"), r.Raw)

	// 未命中手机号 13800000000：调用仍成功计费(001)，range 里 forbid=0。
	nf := base()
	nf["mobile"] = "13800000000"
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, nf, nil)
	rec.Check("未命中仍计费", "errorCode=0 & body.code=001 & range 含 forbid",
		r.ErrorCode == "0" && r.BodyCode == "001" && strings.Contains(r.Range, "forbid"), r.Raw)

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

	// poly 缺失：命中级别必填，网关前置拦截。
	noPoly := base()
	delete(noPoly, "poly")
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, noPoly, nil)
	rec.Check("缺失 poly", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	// poly 非法枚举 (须 C1/C2/C3)：网关前置拦截。
	badPoly := base()
	badPoly["poly"] = "C9"
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, badPoly, nil)
	rec.Check("poly 非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, base(), nil)
	rec.Check("二次成功查得", "errorCode=0 & body.code=001 & range 含 callee",
		r.ErrorCode == "0" && r.BodyCode == "001" && strings.Contains(r.Range, "callee"), r.Raw)
}
