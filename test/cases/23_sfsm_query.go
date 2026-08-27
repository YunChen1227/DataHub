//go:build ignore

// 23_sfsm_query: sfsm 版本 POST /v1/openapi/zlx/querySrmxSFSM（x1 信封格式；
// 内部对接身份证实名核验 mock，form POST + md5 签名）。
// 全场景：查得(001，result 0 一致 / 1 不一致 上游都明标收费)/查无(999，result 2
// 无记录)/上游侧错误(603 余额不足)/鉴权与参数错误。
//
// 该上游业务参数表只有 name + idcard 两项且均必填、**不要手机号**，故 base() 不带
// mobile，查无场景用约定的查无身份证号（mock 的 SFSM_NOTFOUND_IDCARD）。
//
// Run: go run test/cases/23_sfsm_query.go
package main

import (
	"encoding/json"
	"strings"

	"github.com/datahub/relay/test/harness"
)

const version = "sfsm"

// 以下三个身份证号与 scripts/mock_idcheck.go 的 env 缺省值一致（均为合法 18 位格式）。
const (
	notFoundIDCard = "000000000000000404" // 驱动上游 result=2 无记录
	mismatchIDCard = "000000000000000001" // 驱动上游 result=1 不一致（仍计费）
	unpaidIDCard   = "000000000000000603" // 驱动上游 code=603 余额不足（不计费）
)

func base() map[string]string {
	return map[string]string{"idCard": "330129199109094312", "name": "张三"}
}

func main() {
	rec := harness.NewRecorder("23_sfsm_query", "sfsm 主接口全场景 (身份证实名核验, form POST + md5 签名)")
	defer rec.Finish()

	appKey := harness.AppKeyFor(version)

	r := harness.Query(version, appKey, harness.Secret, base(), nil)
	rec.Check("成功查得 result=0 一致 (仅 name+idCard, 不传手机号)",
		"errorCode=0 & body.code=001 & range 含 5 个业务字段",
		r.ErrorCode == "0" && r.BodyCode == "001" && rangeHasBizFields(r.Range), r.Raw)

	// 上游把「不一致」也标为收费，是一个确定的核验结论——必须归一为查得(001)，
	// 不能因为 desc=不一致 就当查无，否则该收的钱收不到、对账对不平。
	mis := base()
	mis["idCard"] = mismatchIDCard
	r = harness.Query(version, appKey, harness.Secret, mis, nil)
	rec.Check("result=1 不一致仍为查得计费", "errorCode=0 & body.code=001 & range 含 不一致",
		r.ErrorCode == "0" && r.BodyCode == "001" && strings.Contains(r.Range, "不一致"), r.Raw)

	nf := base()
	nf["idCard"] = notFoundIDCard
	r = harness.Query(version, appKey, harness.Secret, nf, nil)
	rec.Check("查无结果 (result=2 无记录)", "errorCode=0 & body.code=999",
		r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)

	// code≠200 一律视为上游侧错误：不计费，网关对外 505062。
	up := base()
	up["idCard"] = unpaidIDCard
	r = harness.Query(version, appKey, harness.Secret, up, nil)
	rec.Check("上游 603 余额不足", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, base(), map[string]any{"sign": "deadbeef"})
	rec.Check("错误签名", "errorCode=505002 且无 body", r.ErrorCode == "505002" && r.BodyCode == "", r.Raw)

	r = harness.Query(version, "nonexistent-appkey", harness.Secret, base(), nil)
	rec.Check("未知 appKey", "errorCode=505004", r.ErrorCode == "505004", r.Raw)

	r = harness.Query(version, "", harness.Secret, base(), map[string]any{"appKey": ""})
	rec.Check("缺失 appKey", "errorCode=505001", r.ErrorCode == "505001", r.Raw)

	// 上游参数表 name + idcard 均必填：网关前置拦截，不调用上游/不计费。
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
	// 上游参数表一致（不臆造多余必填/多余校验）。
	extra := base()
	extra["mobile"] = "139xx"
	r = harness.Query(version, appKey, harness.Secret, extra, nil)
	rec.Check("多余 mobile 不参与校验", "errorCode=0 & body.code=001",
		r.ErrorCode == "0" && r.BodyCode == "001", r.Raw)

	// 上游订单号 order_no 只落审计（UID/LogID），整份下游响应里都不该出现它——
	// 既覆盖 result.range，也覆盖 body.uid（uid 是我方内部请求号，非上游订单号）。
	r = harness.Query(version, appKey, harness.Secret, base(), nil)
	rec.Check("响应不含上游订单号", "range 含 result 且整份 raw 不含 order_no / sfsm-mock",
		strings.Contains(r.Range, "result") &&
			!strings.Contains(r.Raw, "order_no") &&
			!strings.Contains(r.Raw, "sfsm-mock"), r.Raw)
}

// rangeHasBizFields 断言 range 是上游 data 富对象的 JSON 且业务字段一个不少
// （result/desc/sex/birthday/address 全量透出；上游标识 order_no 已被剥掉）。
func rangeHasBizFields(raw string) bool {
	if raw == "" {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return false
	}
	for _, f := range []string{"result", "desc", "sex", "birthday", "address"} {
		if _, ok := m[f]; !ok {
			return false
		}
	}
	if _, leaked := m["order_no"]; leaked {
		return false
	}
	return true
}
