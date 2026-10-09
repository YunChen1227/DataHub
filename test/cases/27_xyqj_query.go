//go:build ignore

// 27_xyqj_query: xyqj 版本 POST /v1/openapi/zlx/querySrmxXYQJ（x1 信封格式；
// 内部对接信用全景V21 mock，应诺尔 enol JSON POST + MD5 加签，encryptionType=1 明文）。
// 全场景：查得(001，fee_status=1)/上游不收费(999，fee_status=0)/查无(999，busiCode 1000)/
// 上游侧错误(1001 余额不足)/鉴权与参数错误。上游 body 参数表 name + idCard + mobile 三项均必填。
//
// Run: go run test/cases/27_xyqj_query.go
package main

import (
	"encoding/json"
	"strings"

	"github.com/datahub/relay/test/harness"
)

const version = "xyqj"

// 以下手机号与 scripts/mock_creditpano.go 的 env 缺省值一致。
const (
	notFoundMobile = "13800000000" // 驱动上游 busiCode=1000 数据未查得（不计费）
	unpaidMobile   = "13800001001" // 驱动上游 busiCode=1001 余额不足（不计费）
	freeMobile     = "13800000010" // 驱动上游 busiCode=10 + fee_status=0（上游不收费 → 999）
)

func base() map[string]string {
	return map[string]string{"name": "张三", "idCard": "360429199806073112", "mobile": "13209101123"}
}

func main() {
	rec := harness.NewRecorder("27_xyqj_query", "xyqj 主接口全场景 (信用全景V21, enol JSON POST + MD5 加签)")
	defer rec.Finish()

	appKey := harness.AppKeyFor(version)

	r := harness.Query(version, appKey, harness.Secret, base(), nil)
	rec.Check("成功查得 (fee_status=1)", "errorCode=0 & body.code=001 & range 含 xyp_model_score_high",
		r.ErrorCode == "0" && r.BodyCode == "001" && rangeHas(r.Range, "xyp_model_score_high"), r.Raw)

	// fee_status 是上游逐笔下发的计费标志：上游说不收费，我方也不得计费，归一为 999。
	before := harness.ServiceUsed(version, appKey, harness.Secret)
	fr := base()
	fr["mobile"] = freeMobile
	r = harness.Query(version, appKey, harness.Secret, fr, nil)
	rec.Check("fee_status=0 → 999 不计费", "errorCode=0 & body.code=999",
		r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)

	nf := base()
	nf["mobile"] = notFoundMobile
	r = harness.Query(version, appKey, harness.Secret, nf, nil)
	rec.Check("查无结果 (busiCode 1000)", "errorCode=0 & body.code=999",
		r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)
	after := harness.ServiceUsed(version, appKey, harness.Secret)
	rec.Check("fee_status=0 与 1000 查无都不计入成功查得数", "delta == 0", after == before, "成功查得数发生了变化")

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

	r = harness.Query(version, appKey, harness.Secret, base(), nil)
	rec.Check("响应不含上游流水号", "整份 raw 不含 seqNo / xyqj-mock",
		r.BodyCode == "001" && !strings.Contains(r.Raw, "seqNo") && !strings.Contains(r.Raw, "xyqj-mock"), r.Raw)
}

func rangeHas(raw, key string) bool {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}
