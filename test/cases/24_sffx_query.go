//go:build ignore

// 24_sffx_query: sffx 版本 POST /v1/openapi/zlx/querySrmxSFFX（x1 信封格式；
// 内部对接身份风险V107 mock，应诺尔 enol JSON POST + MD5 加签，PII 走 MD5 摘要）。
// 全场景：查得(001，命中风险 / 无风险 A0 都是查得)/查无(999，busiCode 1000)/
// 上游侧错误(1001 余额不足)/鉴权与参数错误。
//
// 该上游 body 参数表只有 name + idCard 两项必填（tradeNo 选填、本网关不透传），
// **不要手机号**，故 base() 不带 mobile，查无场景用约定的查无身份证号
// （mock 的 SFFX_NOTFOUND_IDCARD）。
//
// Run: go run test/cases/24_sffx_query.go
package main

import (
	"encoding/json"
	"strings"

	"github.com/datahub/relay/test/harness"
)

const version = "sffx"

// 以下三个身份证号与 scripts/mock_idrisk.go 的 env 缺省值一致（均为合法 18 位格式）。
const (
	notFoundIDCard = "000000000000001000" // 驱动上游 busiCode=1000 数据未查得
	noRiskIDCard   = "000000000000000010" // 驱动上游 busiCode=10 + detail ["A0"] 无风险
	unpaidIDCard   = "000000000000001001" // 驱动上游 busiCode=1001 余额不足（不计费）
)

func base() map[string]string {
	return map[string]string{"idCard": "310000199001010010", "name": "张三"}
}

func main() {
	rec := harness.NewRecorder("24_sffx_query", "sffx 主接口全场景 (身份风险V107, enol JSON POST + MD5 加签)")
	defer rec.Finish()

	appKey := harness.AppKeyFor(version)

	r := harness.Query(version, appKey, harness.Secret, base(), nil)
	rec.Check("成功查得命中风险 (仅 name+idCard, 不传手机号)",
		"errorCode=0 & body.code=001 & range.detail 为风险码数组",
		r.ErrorCode == "0" && r.BodyCode == "001" && detailEquals(r.Range, "C2", "J3"), r.Raw)

	// 「无风险」(detail 只有 A0) 是上游给出的确定结论且 busiCode 仍是 10——必须归一为
	// 查得(001)，不能因为"没查到风险"就当查无，否则该收的钱收不到、对账对不平。
	nr := base()
	nr["idCard"] = noRiskIDCard
	r = harness.Query(version, appKey, harness.Secret, nr, nil)
	rec.Check("无风险 A0 仍为查得计费", "errorCode=0 & body.code=001 & range.detail=[A0]",
		r.ErrorCode == "0" && r.BodyCode == "001" && detailEquals(r.Range, "A0"), r.Raw)

	// busiCode 1000 数据未查得：报文形态照旧是 999（**计费与否是另一张表**——本路由
	// 文档给 1000 标了【计费】，由 billing.billNotFoundRoutes 处理，不影响这里的 999）。
	nf := base()
	nf["idCard"] = notFoundIDCard
	r = harness.Query(version, appKey, harness.Secret, nf, nil)
	rec.Check("查无结果 (busiCode 1000)", "errorCode=0 & body.code=999",
		r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)

	// 1001~1009 一律视为上游侧错误：不计费，网关对外 505062。
	up := base()
	up["idCard"] = unpaidIDCard
	r = harness.Query(version, appKey, harness.Secret, up, nil)
	rec.Check("上游 1001 账户余额不足", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, base(), map[string]any{"sign": "deadbeef"})
	rec.Check("错误签名", "errorCode=505002 且无 body", r.ErrorCode == "505002" && r.BodyCode == "", r.Raw)

	r = harness.Query(version, "nonexistent-appkey", harness.Secret, base(), nil)
	rec.Check("未知 appKey", "errorCode=505004", r.ErrorCode == "505004", r.Raw)

	r = harness.Query(version, "", harness.Secret, base(), map[string]any{"appKey": ""})
	rec.Check("缺失 appKey", "errorCode=505001", r.ErrorCode == "505001", r.Raw)

	// 上游 body 参数表 name + idCard 均必填：网关前置拦截，不调用上游/不计费。
	noName := base()
	delete(noName, "name")
	r = harness.Query(version, appKey, harness.Secret, noName, nil)
	rec.Check("缺 name 拦截", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	noID := base()
	delete(noID, "idCard")
	r = harness.Query(version, appKey, harness.Secret, noID, nil)
	rec.Check("缺 idCard 拦截", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	badID := base()
	badID["idCard"] = "12345"
	r = harness.Query(version, appKey, harness.Secret, badID, nil)
	rec.Check("身份证非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	// 本上游不要手机号：带上一个格式非法的 mobile 也不该被拦截，证明校验器口径与
	// 上游参数表一致（不臆造多余必填/多余校验，也不把 mobile 塞进上游 body 把签名算错）。
	extra := base()
	extra["mobile"] = "139xx"
	r = harness.Query(version, appKey, harness.Secret, extra, nil)
	rec.Check("多余 mobile 不参与校验", "errorCode=0 & body.code=001",
		r.ErrorCode == "0" && r.BodyCode == "001", r.Raw)

	// 上游流水号 seqNo 只落审计（UID/LogID），整份下游响应里都不该出现它——
	// 既覆盖 result.range，也覆盖 body.uid（uid 是我方内部请求号，非上游流水号）。
	r = harness.Query(version, appKey, harness.Secret, base(), nil)
	rec.Check("响应不含上游流水号", "range 含 detail 且整份 raw 不含 seqNo / sffx-mock",
		strings.Contains(r.Range, "detail") &&
			!strings.Contains(r.Raw, "seqNo") &&
			!strings.Contains(r.Raw, "sffx-mock"), r.Raw)
}

// detailEquals 断言 range 是上游 result 富对象的 JSON 且 detail 数组与期望逐项一致
// （风险类型码 A~P + 风险周期码 0~6 原样透出，未被改写或截断）。
func detailEquals(raw string, want ...string) bool {
	if raw == "" {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return false
	}
	got, ok := m["detail"].([]any)
	if !ok || len(got) != len(want) {
		return false
	}
	for i, w := range want {
		if got[i] != w {
			return false
		}
	}
	return true
}
