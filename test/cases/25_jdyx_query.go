//go:build ignore

// 25_jdyx_query: jdyx 版本 POST /v1/openapi/zlx/querySrmxJDYX（x1 信封格式；
// 内部对接借贷申请意向V22【TD】mock，应诺尔 enol JSON POST + MD5 加签，PII 明文，
// **信封不带 encryptionType**）。
// 全场景：查得(001)/查无(999，busiCode 1000，不计费)/上游侧错误(1001 余额不足)/
// 鉴权与参数错误。上游 body 参数表 name + idCard + mobile 三项均必填。
//
// Run: go run test/cases/25_jdyx_query.go
package main

import (
	"encoding/json"
	"strings"

	"github.com/datahub/relay/test/harness"
)

const version = "jdyx"

// 以下手机号与 scripts/mock_loanintent.go 的 env 缺省值一致。
const (
	notFoundMobile = "13800000000" // 驱动上游 busiCode=1000 未查得
	unpaidMobile   = "13800001001" // 驱动上游 busiCode=1001 余额不足（不计费）
)

func base() map[string]string {
	return map[string]string{"name": "张三", "idCard": "360429199806073112", "mobile": "13309091009"}
}

func main() {
	rec := harness.NewRecorder("25_jdyx_query", "jdyx 主接口全场景 (借贷申请意向V22【TD】, enol JSON POST + MD5 加签)")
	defer rec.Finish()

	appKey := harness.AppKeyFor(version)

	r := harness.Query(version, appKey, harness.Secret, base(), nil)
	rec.Check("成功查得", "errorCode=0 & body.code=001 & range 含 TC_* 变量",
		r.ErrorCode == "0" && r.BodyCode == "001" && rangeHas(r.Range, "TC_LOAN_CNT_3M"), r.Raw)

	nf := base()
	nf["mobile"] = notFoundMobile
	r = harness.Query(version, appKey, harness.Secret, nf, nil)
	rec.Check("查无结果 (busiCode 1000)", "errorCode=0 & body.code=999",
		r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)

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

	// 上游 body 参数表 name / idCard / mobile 均必填：网关前置拦截，不调用上游/不计费。
	for _, f := range []string{"name", "idCard", "mobile"} {
		miss := base()
		delete(miss, f)
		r = harness.Query(version, appKey, harness.Secret, miss, nil)
		rec.Check("缺 "+f+" 拦截", "errorCode=505062", r.ErrorCode == "505062", r.Raw)
	}

	bad := base()
	bad["mobile"] = "139xx"
	r = harness.Query(version, appKey, harness.Secret, bad, nil)
	rec.Check("手机号非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, base(), nil)
	rec.Check("响应不含上游流水号", "整份 raw 不含 seqNo / jdyx-mock",
		r.BodyCode == "001" && !strings.Contains(r.Raw, "seqNo") && !strings.Contains(r.Raw, "jdyx-mock"), r.Raw)
}

func rangeHas(raw, key string) bool {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}
