//go:build ignore

// 12_swfp_query: swfp 版本 POST /v1/openapi/zlx/querySrmxSWFP（x1 信封格式；
// 企业维度入参 creditCode；内部聚合税务发票四产品码 mock）。全场景：四份全查得(001,
// range 四段 ok)/全部查无(999)/部分失败(002, range 含 error 段)/鉴权与参数错误。
//
// Run: go run test/cases/12_swfp_query.go
package main

import (
	"encoding/json"
	"strings"

	"github.com/datahub/relay/test/harness"
)

const version = "swfp"

// mock_entcredit.go 约定的场景驱动值（合法统一社会信用代码格式）。
const (
	creditCodeNormal  = "92500233MA60R5KW8M" // 四产品全部查得
	creditCodeEmpty   = "91110000EMPTYEMPT0" // 四产品全部查无
	creditCodePartial = "91110000PARTFA0001" // P0130083 失败，其余查得 → 002
)

func body(creditCode string) map[string]string {
	return map[string]string{"creditCode": creditCode}
}

func main() {
	rec := harness.NewRecorder("12_swfp_query", "swfp 主接口全场景 (税务发票四产品聚合)")
	defer rec.Finish()

	appKey := harness.AppKeyFor(version)

	r := harness.Query(version, appKey, harness.Secret, body(creditCodeNormal), nil)
	rec.Check("四份全查得", "errorCode=0 & body.code=001 & range 四段均 ok",
		r.ErrorCode == "0" && r.BodyCode == "001" && sectionsAllOK(r.Range), r.Raw)

	r = harness.Query(version, appKey, harness.Secret, body(creditCodeEmpty), nil)
	rec.Check("全部查无", "errorCode=0 & body.code=999", r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, body(creditCodePartial), nil)
	rec.Check("部分数据源失败", "errorCode=0 & body.code=002 & range 含 error 段与 ok 段",
		r.ErrorCode == "0" && r.BodyCode == "002" && sectionPartial(r.Range), r.Raw)

	r = harness.Query(version, appKey, harness.Secret, body(creditCodeNormal), map[string]any{"sign": "deadbeef"})
	rec.Check("错误签名", "errorCode=505002 且无 body", r.ErrorCode == "505002" && r.BodyCode == "", r.Raw)

	r = harness.Query(version, "nonexistent-appkey", harness.Secret, body(creditCodeNormal), nil)
	rec.Check("未知 appKey", "errorCode=505004", r.ErrorCode == "505004", r.Raw)

	r = harness.Query(version, "", harness.Secret, body(creditCodeNormal), map[string]any{"appKey": ""})
	rec.Check("缺失 appKey", "errorCode=505001", r.ErrorCode == "505001", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, body("12345"), nil)
	rec.Check("creditCode 非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	// 个人三要素入参对 swfp 无效（缺 creditCode → 参数拦截）。
	r = harness.Query(version, appKey, harness.Secret,
		map[string]string{"mobile": "13809091009", "idCard": "330129199109094312"}, nil)
	rec.Check("缺 creditCode 拦截", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, body(creditCodeNormal), nil)
	rec.Check("二次全查得", "errorCode=0 & body.code=001", r.ErrorCode == "0" && r.BodyCode == "001", r.Raw)
}

// sectionsAllOK 校验 range JSON 四段 (invoice1/invoice2/tax1/tax2) 均 status=ok 且带 data。
func sectionsAllOK(raw string) bool {
	m := parseSections(raw)
	if m == nil {
		return false
	}
	for _, key := range []string{"invoice1", "invoice2", "tax1", "tax2"} {
		sec, ok := m[key]
		if !ok || sec.Status != "ok" || len(sec.Data) == 0 {
			return false
		}
	}
	return true
}

// sectionPartial 校验部分失败：invoice2(P0130083) 为 error，其余为 ok 且带数据。
func sectionPartial(raw string) bool {
	m := parseSections(raw)
	if m == nil {
		return false
	}
	if m["invoice2"].Status != "error" {
		return false
	}
	return m["invoice1"].Status == "ok" && m["tax1"].Status == "ok" && m["tax2"].Status == "ok"
}

type section struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data"`
	Error  string          `json:"error"`
}

func parseSections(raw string) map[string]section {
	if raw == "" || !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return nil
	}
	var m map[string]section
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return m
}
