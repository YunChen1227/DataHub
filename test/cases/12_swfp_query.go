//go:build ignore

// 12_swfp_query: swfp 版本 POST /v1/openapi/zlx/querySrmxSWFP（x1 信封格式；
// 企业维度入参 creditCode + 可选 scope；内部聚合税务发票四产品码 + 销项数据 mock，
// 输出经契约层按 docs/税票分析接口文档.xlsx 整理：发票数据聚合/税务数据聚合 两段 +
// 每字段按源分组 (源1..源5) + sourceStatus）。全场景：五源全查得(001)/scope=basic
// (跳过源5)/全部查无(999)/部分失败(002)/源5失败(002)/鉴权与参数错误。
//
// Run: go run test/cases/12_swfp_query.go
package main

import (
	"encoding/json"
	"strings"

	"github.com/datahub/relay/test/harness"
)

const version = "swfp"

// mock_entcredit.go / mock_salesdata.go 约定的场景驱动值（合法统一社会信用代码格式）。
const (
	creditCodeNormal    = "92500233MA60R5KW8M" // 五源全部查得
	creditCodeEmpty     = "91110000EMPTYEMPT0" // 全部查无
	creditCodePartial   = "91110000PARTFA0001" // P0130083(源2) 失败，其余查得 → 002
	creditCodeSalesFail = "91110000BADFA00001" // 源5 (销项) 失败，其余查得 → 002
)

func body(creditCode string) map[string]string {
	return map[string]string{"creditCode": creditCode}
}

func bodyScope(creditCode, scope string) map[string]string {
	return map[string]string{"creditCode": creditCode, "scope": scope}
}

func main() {
	rec := harness.NewRecorder("12_swfp_query", "swfp 主接口全场景 (税务发票聚合+销项, xlsx 契约输出)")
	defer rec.Finish()

	appKey := harness.AppKeyFor(version)

	r := harness.Query(version, appKey, harness.Secret, body(creditCodeNormal), nil)
	rec.Check("五源全查得", "errorCode=0 & body.code=001 & 契约两段结构 & 源1-源5 均 ok",
		r.ErrorCode == "0" && r.BodyCode == "001" && contractAllOK(r.Range, true), r.Raw)

	r = harness.Query(version, appKey, harness.Secret, bodyScope(creditCodeNormal, "basic"), nil)
	rec.Check("scope=basic 跳过源5", "errorCode=0 & body.code=001 & 无源5 段",
		r.ErrorCode == "0" && r.BodyCode == "001" && contractAllOK(r.Range, false) && !strings.Contains(r.Range, "源5"), r.Raw)

	r = harness.Query(version, appKey, harness.Secret, body(creditCodeEmpty), nil)
	rec.Check("全部查无", "errorCode=0 & body.code=999", r.ErrorCode == "0" && r.BodyCode == "999", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, body(creditCodePartial), nil)
	rec.Check("源2 数据源失败", "errorCode=0 & body.code=002 & sourceStatus.源2=error 且源1 ok",
		r.ErrorCode == "0" && r.BodyCode == "002" && statusOf(r.Range, "源2") == "error" && statusOf(r.Range, "源1") == "ok", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, body(creditCodeSalesFail), nil)
	rec.Check("源5 销项失败", "errorCode=0 & body.code=002 & sourceStatus.源5=error 且源1 ok",
		r.ErrorCode == "0" && r.BodyCode == "002" && statusOf(r.Range, "源5") == "error" && statusOf(r.Range, "源1") == "ok", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, body(creditCodeNormal), map[string]any{"sign": "deadbeef"})
	rec.Check("错误签名", "errorCode=505002 且无 body", r.ErrorCode == "505002" && r.BodyCode == "", r.Raw)

	r = harness.Query(version, "nonexistent-appkey", harness.Secret, body(creditCodeNormal), nil)
	rec.Check("未知 appKey", "errorCode=505004", r.ErrorCode == "505004", r.Raw)

	r = harness.Query(version, "", harness.Secret, body(creditCodeNormal), map[string]any{"appKey": ""})
	rec.Check("缺失 appKey", "errorCode=505001", r.ErrorCode == "505001", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, body("12345"), nil)
	rec.Check("creditCode 非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, bodyScope(creditCodeNormal, "invalid"), nil)
	rec.Check("scope 非法", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	// 个人三要素入参对 swfp 无效（缺 creditCode → 参数拦截）。
	r = harness.Query(version, appKey, harness.Secret,
		map[string]string{"mobile": "13809091009", "idCard": "330129199109094312"}, nil)
	rec.Check("缺 creditCode 拦截", "errorCode=505062", r.ErrorCode == "505062", r.Raw)

	r = harness.Query(version, appKey, harness.Secret, body(creditCodeNormal), nil)
	rec.Check("二次全查得", "errorCode=0 & body.code=001", r.ErrorCode == "0" && r.BodyCode == "001", r.Raw)
}

// contract mirrors the 契约输出结构 (swfpcontract.go)。
type contract struct {
	Invoice      map[string]map[string]json.RawMessage `json:"发票数据聚合"`
	Tax          map[string]map[string]json.RawMessage `json:"税务数据聚合"`
	SourceStatus map[string]string                     `json:"sourceStatus"`
}

func parseContract(raw string) *contract {
	if raw == "" || !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return nil
	}
	var c contract
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil
	}
	return &c
}

// contractAllOK 校验契约结构：两段齐全、基础源 (源1-源4) 全 ok、发票段 nsrjbxx 有
// 源1、税务段 lrbxxList 键存在；withSales=true 时源5 也须 ok 且 kphzxxList 带源5
// （源5 条目须完成字段映射：含 xlsx 契约名 ssyf 而非上游原名 belongMonth）。
func contractAllOK(raw string, withSales bool) bool {
	c := parseContract(raw)
	if c == nil || c.Invoice == nil || c.Tax == nil {
		return false
	}
	for _, s := range []string{"源1", "源2", "源3", "源4"} {
		if c.SourceStatus[s] != "ok" {
			return false
		}
	}
	if _, ok := c.Invoice["nsrjbxx"]["源1"]; !ok {
		return false
	}
	if _, ok := c.Tax["lrbxxList"]; !ok {
		return false
	}
	if !withSales {
		return true
	}
	if c.SourceStatus["源5"] != "ok" {
		return false
	}
	salesRows, ok := c.Invoice["kphzxxList"]["源5"]
	if !ok || !strings.Contains(string(salesRows), "ssyf") || strings.Contains(string(salesRows), "belongMonth") {
		return false
	}
	return true
}

// statusOf 读取 sourceStatus 中某源的状态（缺失返回空串）。
func statusOf(raw, source string) string {
	c := parseContract(raw)
	if c == nil {
		return ""
	}
	return c.SourceStatus[source]
}
