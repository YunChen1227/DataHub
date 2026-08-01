package upstream

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/datahub/relay/internal/domain/model"
)

const testCreditCode = "92500233MA60R5KW8M"

// contractOut mirrors the 契约输出结构 for assertions.
type contractOut struct {
	Invoice      map[string]map[string]json.RawMessage `json:"发票数据聚合"`
	Tax          map[string]map[string]json.RawMessage `json:"税务数据聚合"`
	SourceStatus map[string]string                     `json:"sourceStatus"`
}

// entInvoiceData 构造一份证通发票段解码明细（含 xlsx 外字段 yxhpje，应被白名单剔除）。
func entInvoiceData() string {
	return `{"nsrjbxx":{"nsrsbh":"` + testCreditCode + `","nsrmc":"某某公司","kyrq":"2018-01-01"},
	"nsrfpxx":{"kphzxxList":[{"ssyf":"2026-05","kpqj":"2026-05-31","nsrsbh":"` + testCreditCode + `",
	"ljkpcs":"3","kpje":"100.00","ljse":"13.00","yxhpje":"9","dykptslp":"null"}],
	"syhzxxList":[{"ssyf":"2025-05","nsrsbh":"` + testCreditCode + `","xfmc":"某销方","ljkpjebhs":"172.28"}]}}`
}

func entTaxData() string {
	return `{"nsrjbxx":{"nsrsbh":"` + testCreditCode + `","nsrmc":"某某公司"},
	"nsrswxx":{"sbsjList":[{"sssjq":"2026-01-01","nsrsbh":"` + testCreditCode + `","ynse":1.71,"sbqx":"2026-03-16"}],
	"zsbxxList":[{"sssjq":"2025-01-01","zsxm":"财务报表","sjje":0,"jkzt":"无需扣款"}]}}`
}

// salesData 构造一份源5 (销项数据) 合并明细（summaryIndicators 应被整体丢弃）。
func salesData() string {
	return `{"salesInvoice":[{"belongMonth":202403,"invoiceAmtMonth":1234.56,"taxAmtMonth":160.5,
	"invoiceCntMonth":8,"invoiceHighAmtMonth":500,"allInvoiceHighAmtMonth":600,
	"redInvoiceAmtMonth":-10,"redTaxAmtMonth":-1.3,"redInvoiceCntMonth":1,
	"nullifiedInvoiceAmtMonth":0,"nullifiedInvoiceCntMonth":0,"nullTaxAmtMonth":0,
	"invoiceDayMonth":5,"blueInvoiceDayMonth":4,"latestInvoiceDate":"20240328","noTradeRcordDay":3}],
	"summaryIndicators":{"inputL1ySaleActualAmt":99999},
	"monthlyDownstreamInfo":[{"belongMonth":202403,"buyerName":"某购方","buyerTaxpayerIdNum":"91500000XXXX",
	"tradeAmtRankMonth":1,"tradeAmtMonth":800,"taxAmtMonth":104,"invoiceCntMonth":2,
	"invoiceCntPctMonth":0.25,"tradeAmtPctMonth":0.648,"redInvoiceAmtMonth":0,"redInvoiceCntMonth":0,
	"redTaxAmtMonth":0,"nullifiedInvoiceAmtMonth":0,"nullifiedInvoiceCntMonth":0,"nullTaxAmtMonth":0}]}`
}

// buildContract 组一个五源桩聚合器 + 契约层。
func buildContract(t *testing.T, salesErr bool) *SwfpContract {
	t.Helper()
	ok := func(data string) fakePort {
		return fakePort{res: &model.UpstreamResult{Code: "001", Msg: "成功", UID: "ORD-1", LogID: "ORD-1", Range: data}}
	}
	sales := fakePort{res: &model.UpstreamResult{Code: "001", Msg: "成功", Range: salesData()}}
	if salesErr {
		sales = fakePort{err: &model.UpstreamError{Code: "0002", Msg: "请求超时"}}
	}
	agg, err := NewAggregator([]LabeledUpstream{
		{Label: "invoice1", Port: ok(entInvoiceData())},
		{Label: "invoice2", Port: ok(entInvoiceData())},
		{Label: "tax1", Port: ok(entTaxData())},
		{Label: "tax2", Port: ok(entTaxData())},
		{Label: "sales", Port: sales, Optional: true},
	})
	if err != nil {
		t.Fatalf("NewAggregator: %v", err)
	}
	return NewSwfpContract(agg)
}

func queryContract(t *testing.T, c *SwfpContract, scope string) (*model.UpstreamResult, contractOut) {
	t.Helper()
	res, err := c.Query(context.Background(), &model.UpstreamRequest{CreditCode: testCreditCode, Scope: scope, Reqid: "r1"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	var out contractOut
	if err := json.Unmarshal([]byte(res.Range), &out); err != nil {
		t.Fatalf("契约 range 不是合法 JSON: %v\n%s", err, res.Range)
	}
	return res, out
}

// TestSwfpContractAllOK 全查得：xlsx 两段结构、按源分组、白名单过滤、源5 字段映射。
func TestSwfpContractAllOK(t *testing.T) {
	res, out := queryContract(t, buildContract(t, false), model.ScopeAll)
	if res.Code != "001" {
		t.Fatalf("want 001, got %s", res.Code)
	}
	for _, s := range []string{"源1", "源2", "源3", "源4", "源5"} {
		if out.SourceStatus[s] != "ok" {
			t.Fatalf("sourceStatus[%s]=%q, want ok (all=%v)", s, out.SourceStatus[s], out.SourceStatus)
		}
	}

	// 发票段 nsrjbxx：源1/源2 存在，xlsx 全字段补空（如 hybmdl 上游缺失 → ""）。
	var jb map[string]string
	if err := json.Unmarshal(out.Invoice["nsrjbxx"]["源1"], &jb); err != nil {
		t.Fatalf("nsrjbxx.源1: %v", err)
	}
	if jb["nsrmc"] != "某某公司" || jb["hybmdl"] != "" {
		t.Fatalf("nsrjbxx 白名单/补空不符: %v", jb)
	}
	if len(jb) != len(swfpNsrjbxxFields) {
		t.Fatalf("nsrjbxx 字段数=%d, want %d (xlsx 全字段)", len(jb), len(swfpNsrjbxxFields))
	}

	// kphzxxList：源1/源2 来自证通（yxhpje 被剔除），源5 来自销项映射。
	var kphz1 []map[string]string
	if err := json.Unmarshal(out.Invoice["kphzxxList"]["源1"], &kphz1); err != nil {
		t.Fatalf("kphzxxList.源1: %v", err)
	}
	if _, leaked := kphz1[0]["yxhpje"]; leaked {
		t.Fatalf("xlsx 外字段 yxhpje 泄漏: %v", kphz1[0])
	}
	if kphz1[0]["ljkpcs"] != "3" {
		t.Fatalf("kphzxxList.源1 数据不符: %v", kphz1[0])
	}

	var kphz5 []map[string]string
	if err := json.Unmarshal(out.Invoice["kphzxxList"]["源5"], &kphz5); err != nil {
		t.Fatalf("kphzxxList.源5: %v", err)
	}
	row := kphz5[0]
	if row["ssyf"] != "2024-03" || row["kpqj"] != "2024-03-31" || row["zjybkpsj"] != "2024-03-28" {
		t.Fatalf("源5 日期归一不符: %v", row)
	}
	if row["kpje"] != "1234.56" || row["nsrsbh"] != testCreditCode || row["hpje"] != "-10" {
		t.Fatalf("源5 字段映射不符: %v", row)
	}

	// xyhzxxList.源5：购方映射 + 缺失字段 (gfsl) 补空。
	var xyhz5 []map[string]string
	if err := json.Unmarshal(out.Invoice["xyhzxxList"]["源5"], &xyhz5); err != nil {
		t.Fatalf("xyhzxxList.源5: %v", err)
	}
	if xyhz5[0]["gfnsrmc"] != "某购方" || xyhz5[0]["gfsl"] != "" || xyhz5[0]["kpjezb"] != "0.648" {
		t.Fatalf("xyhzxxList.源5 映射不符: %v", xyhz5[0])
	}

	// 税务段：源3/源4；zsbxxList 数字转字符串、缺失字段补空。
	var zsb []map[string]string
	if err := json.Unmarshal(out.Tax["zsbxxList"]["源3"], &zsb); err != nil {
		t.Fatalf("zsbxxList.源3: %v", err)
	}
	if zsb[0]["sjje"] != "0" || zsb[0]["jkjzrq"] != "" {
		t.Fatalf("zsbxxList 转换不符: %v", zsb[0])
	}

	// summaryIndicators (xlsx 之外) 不得出现在任何地方。
	if strings.Contains(res.Range, "summaryIndicators") || strings.Contains(res.Range, "inputL1ySaleActualAmt") {
		t.Fatalf("xlsx 外内容 summaryIndicators 泄漏")
	}
}

// TestSwfpContractScopeBasic scope=basic：源5 不调用、不出段、sourceStatus 无源5。
func TestSwfpContractScopeBasic(t *testing.T) {
	res, out := queryContract(t, buildContract(t, false), model.ScopeBasic)
	if res.Code != "001" {
		t.Fatalf("want 001, got %s", res.Code)
	}
	if _, present := out.SourceStatus["源5"]; present {
		t.Fatalf("scope=basic 不应含源5: %v", out.SourceStatus)
	}
	if _, present := out.Invoice["kphzxxList"]["源5"]; present {
		t.Fatalf("scope=basic 不应有源5 数据")
	}
	if _, present := out.Invoice["kphzxxList"]["源1"]; !present {
		t.Fatalf("基础源数据缺失")
	}
}

// TestSwfpContractSalesError 源5 失败：002 部分成功，sourceStatus 标 error，
// 基础源数据照常返回且不泄漏失败详情（error 文本不透出）。
func TestSwfpContractSalesError(t *testing.T) {
	res, out := queryContract(t, buildContract(t, true), model.ScopeAll)
	if res.Code != "002" {
		t.Fatalf("want 002, got %s", res.Code)
	}
	if out.SourceStatus["源5"] != "error" || out.SourceStatus["源1"] != "ok" {
		t.Fatalf("sourceStatus 不符: %v", out.SourceStatus)
	}
	if strings.Contains(res.Range, "请求超时") || strings.Contains(res.Range, "0002") {
		t.Fatalf("失败详情不应透出下游: %s", res.Range)
	}
	if _, present := out.Invoice["syhzxxList"]["源1"]; !present {
		t.Fatalf("成功源数据缺失")
	}
}
