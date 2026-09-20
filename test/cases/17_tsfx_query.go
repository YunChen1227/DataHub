//go:build ignore

// 17_tsfx_query: tsfx 版本 POST /v1/openapi/zlx/querySrmxTSFX（x1 信封格式；
// 内部对接投诉分析识别名单 kfongtech mock）。全场景：下游仅 mobile 入参，网关对每次
// 请求并发查询 C1/C2/C3 三档并整合成 poly 字典经 range 透出；至少一档成功即计费(001)；
// 上游错误码归一 error；鉴权与参数错误走网关级 errorCode。
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
	// 下游只需传 mobile；poly 已不再是入参 (网关内部固定查 C1/C2/C3)。
	return map[string]string{
		"mobile": "13809091009",
	}
}

// hasAllPolys 判断整合后的 range 字典是否三档齐全 (C1/C2/C3)。
func hasAllPolys(rangeStr string) bool {
	return strings.Contains(rangeStr, "C1") &&
		strings.Contains(rangeStr, "C2") &&
		strings.Contains(rangeStr, "C3")
}

func main() {
	rec := harness.NewRecorder("17_tsfx_query", "tsfx 主接口全场景 (投诉分析识别名单 kfongtech)")
	defer rec.Finish()

	r := harness.Query(version, harness.AppKeyFor(version), harness.Secret, base(), nil)
	rec.Check("成功查得(命中)：三档整合", "errorCode=0 & body.code=001 & range 含 C1/C2/C3 与 forbid",
		r.ErrorCode == "0" && r.BodyCode == "001" && hasAllPolys(r.Range) && strings.Contains(r.Range, "forbid"), r.Raw)

	// 未命中手机号 13800000000：三档调用仍成功计费(001)，各档 forbid=0。
	nf := base()
	nf["mobile"] = "13800000000"
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, nf, nil)
	rec.Check("未命中仍计费：三档整合", "errorCode=0 & body.code=001 & range 含 C1/C2/C3 与 forbid",
		r.ErrorCode == "0" && r.BodyCode == "001" && hasAllPolys(r.Range) && strings.Contains(r.Range, "forbid"), r.Raw)

	// 旧客户端仍携带 poly：一律忽略、不影响结果 (仍返回三档整合 001)。
	withPoly := base()
	withPoly["poly"] = "C9" // 非法枚举也不再拦截：poly 不是入参
	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, withPoly, nil)
	rec.Check("携带 poly 被忽略", "errorCode=0 & body.code=001 & range 含 C1/C2/C3",
		r.ErrorCode == "0" && r.BodyCode == "001" && hasAllPolys(r.Range), r.Raw)

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

	r = harness.Query(version, harness.AppKeyFor(version), harness.Secret, base(), nil)
	rec.Check("二次成功查得", "errorCode=0 & body.code=001 & range 含 callee",
		r.ErrorCode == "0" && r.BodyCode == "001" && strings.Contains(r.Range, "callee"), r.Raw)
}
