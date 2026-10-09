//go:build ignore

// 26_fxbl_query: fxbl 版本 POST /v1/openapi/zlx/querySrmxFXBL（x1 信封格式；
// 内部对接风险变量V11 mock，应诺尔 enol JSON POST + MD5 加签，encryptionType=1 明文）。
// 全场景：查得(001)/查无(999，busiCode 1000——**本路由查无也计费**)/上游侧错误
// (1001 余额不足)/鉴权与参数错误。上游 body 参数表 name + idCard + mobile 三项均必填。
//
// Run: go run test/cases/26_fxbl_query.go
package main

import (
	"encoding/json"
	"strings"

	"github.com/datahub/relay/test/harness"
)

const version = "fxbl"

// 以下手机号与 scripts/mock_riskvar.go 的 env 缺省值一致。
const (
	notFoundMobile = "13800000000" // 驱动上游 busiCode=1000 数据未查得（计费）
	unpaidMobile   = "13800001001" // 驱动上游 busiCode=1001 余额不足（不计费）
)

func base() map[string]string {
	return map[string]string{"name": "张三", "idCard": "360429199806073112", "mobile": "17325421010"}
}

func main() {
	rec := harness.NewRecorder("26_fxbl_query", "fxbl 主接口全场景 (风险变量V11, enol JSON POST + MD5 加签)")
	defer rec.Finish()

	appKey := harness.AppKeyFor(version)

	r := harness.Query(version, appKey, harness.Secret, base(), nil)
	rec.Check("成功查得", "errorCode=0 & body.code=001 & range 含 Rule_final_decision",
		r.ErrorCode == "0" && r.BodyCode == "001" && rangeHas(r.Range, "Rule_final_decision"), r.Raw)

	// busiCode 1000 数据未查得：报文形态照旧是 999（计费与否是另一张表——本路由文档给
	// 1000 标了【计费】，由 billing.billNotFoundRoutes 处理，不影响这里的 999）。
	before := harness.ServiceUsed(version, appKey, harness.Secret)
	nf := base()
	nf["mobile"] = notFoundMobile
	r = harness.Query(version, appKey, harness.Secret, nf, nil)
	rec.Check("查无结果 (busiCode 1000)", "errorCode=0 & body.code=999",
		r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)
	after := harness.ServiceUsed(version, appKey, harness.Secret)
	rec.Check("查无也计入成功查得数", "delta == 1", after-before == 1, "before/after 差值不为 1")

	up := base()
	up["mobile"] = unpaidMobile
	r = harness.Query(version, appKey, harness.Secret, up, nil)
	rec.Check("上游 1001 账户余额不足", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, base(), map[string]any{"sign": "deadbeef"})
	rec.Check("错误签名", "errorCode=505002 且无 body", r.ErrorCode == "505002" && r.BodyCode == "", r.Raw)

	r = harness.Query(version, "nonexistent-appkey", harness.Secret, base(), nil)
	rec.Check("未知 appKey", "errorCode=505004", r.ErrorCode == "505004", r.Raw)

	r = harness.Query(version, "", harness.Secret, base(), map[string]any{"appKey": ""})
	rec.Check("缺失 appKey", "errorCode=505001", r.ErrorCode == "505001", r.Raw)

	for _, f := range []string{"name", "idCard", "mobile"} {
		miss := base()
		delete(miss, f)
		r = harness.Query(version, appKey, harness.Secret, miss, nil)
		rec.Check("缺 "+f+" 拦截", "errorCode=505062", r.ErrorCode == "505062", r.Raw)
	}

	bad := base()
	bad["idCard"] = "12345"
	r = harness.Query(version, appKey, harness.Secret, bad, nil)
	rec.Check("身份证非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, base(), nil)
	rec.Check("响应不含上游流水号", "整份 raw 不含 seqNo / fxbl-mock",
		r.BodyCode == "001" && !strings.Contains(r.Raw, "seqNo") && !strings.Contains(r.Raw, "fxbl-mock"), r.Raw)
}

func rangeHas(raw, key string) bool {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}
