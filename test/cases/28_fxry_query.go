//go:build ignore

// 28_fxry_query: fxry 版本 POST /v1/openapi/zlx/querySrmxFXRY（x1 信封格式；
// 内部对接风险人员V108 mock，应诺尔 enol JSON POST + MD5 加签，encryptionType=1 明文）。
// 全场景：查得(001，命中风险 / 「是否正常」位命中都是查得)/查无(999，busiCode 1000，
// **本路由查无也计费**)/上游侧错误(1001 余额不足)/鉴权与参数错误。
//
// 该上游 body 参数表只有 name + idCard 两项必填（tradeNo 选填、本网关不透传），
// **不要手机号**，故 base() 不带 mobile，查无场景用约定的查无身份证号
// （mock 的 FXRY_NOTFOUND_IDCARD）。
//
// Run: go run test/cases/28_fxry_query.go
package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/datahub/relay/test/harness"
)

const version = "fxry"

// 以下三个身份证号与 scripts/mock_riskperson.go 的 env 缺省值一致（均为合法 18 位格式）。
const (
	notFoundIDCard = "000000000000001000" // 驱动上游 busiCode=1000 数据未查得
	normalIDCard   = "000000000000000010" // 驱动上游 busiCode=10 + 第 25 位「是否正常」=1
	unpaidIDCard   = "000000000000001001" // 驱动上游 busiCode=1001 余额不足（不计费）
)

func base() map[string]string {
	return map[string]string{"idCard": "310000199001010010", "name": "张三"}
}

func main() {
	rec := harness.NewRecorder("28_fxry_query", "fxry 主接口全场景 (风险人员V108, enol JSON POST + MD5 加签)")
	defer rec.Finish()

	appKey := harness.AppKeyFor(version)

	r := harness.Query(version, appKey, harness.Secret, base(), nil)
	rec.Check("成功查得命中风险 (仅 name+idCard, 不传手机号)",
		"errorCode=0 & body.code=001 & range.resultCode 为 25 位命中码",
		r.ErrorCode == "0" && r.BodyCode == "001" && resultCodeEquals(r.Range, "1000000000001000000000000"), r.Raw)

	// 「是否正常」位命中 (第 25 位=1) 是上游给出的确定结论且 busiCode 仍是 10——必须归一为
	// 查得(001)，不能因为"没有风险"就当查无。
	nr := base()
	nr["idCard"] = normalIDCard
	r = harness.Query(version, appKey, harness.Secret, nr, nil)
	rec.Check("正常人员仍为查得计费", "errorCode=0 & body.code=001 & 第 25 位=1",
		r.ErrorCode == "0" && r.BodyCode == "001" && resultCodeEquals(r.Range, "0000000000000000000000001"), r.Raw)

	// busiCode 1000 数据未查得：报文形态照旧是 999，计费由 billing.billNotFoundRoutes 决定，
	// 查无前后 serviceUsed 必须 +1。
	before := harness.ServiceUsed(version, appKey, harness.Secret)
	nf := base()
	nf["idCard"] = notFoundIDCard
	r = harness.Query(version, appKey, harness.Secret, nf, nil)
	rec.Check("查无结果 (busiCode 1000)", "errorCode=0 & body.code=999",
		r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)
	after := harness.ServiceUsed(version, appKey, harness.Secret)
	rec.Check("查无也计费", "serviceUsed +1", before >= 0 && after == before+1,
		fmt.Sprintf("before=%v after=%v", before, after))

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

	// 本上游不要手机号：带上格式非法的 mobile 既不被拦截，也不得进上游 body
	// （mock 收到 mobile 会回 1007 → 505062，借此暴露多传）。
	extra := base()
	extra["mobile"] = "139xx"
	r = harness.Query(version, appKey, harness.Secret, extra, nil)
	rec.Check("多余 mobile 不参与校验也不透传", "errorCode=0 & body.code=001",
		r.ErrorCode == "0" && r.BodyCode == "001", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, base(), nil)
	rec.Check("响应不含上游流水号", "range 含 resultCode 且整份 raw 不含 seqNo / fxry-mock",
		strings.Contains(r.Range, "resultCode") &&
			!strings.Contains(r.Raw, "seqNo") &&
			!strings.Contains(r.Raw, "fxry-mock"), r.Raw)
}

// resultCodeEquals 断言 range 是上游 result 对象的 JSON 且 resultCode 原样透出。
func resultCodeEquals(raw, want string) bool {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return false
	}
	return m["resultCode"] == want
}
