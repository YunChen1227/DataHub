package billing

import (
	"testing"

	"github.com/datahub/relay/internal/domain/model"
)

// TestDecide_BillingScope verifies the口径: 成功查得数 only counts 查得数据 (001);
// 999 查无结果 is Resolved (确定结论 → BILLED) but NOT Returned (不累计查得数).
func TestDecide_BillingScope(t *testing.T) {
	svc := New(DefaultTable())

	cases := []struct {
		name         string
		code         string
		wantResolved bool // 上游确定结论 → 台账 BILLED
		wantReturned bool // 查得数据 → 累计成功查得数
	}{
		{"001 查得数据", "001", true, true},
		{"999 查无结果", "999", true, false},
		{"003 我方原因失败", "003", false, false},
		{"012 接口错误", "012", false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := svc.Decide(&model.UpstreamResult{Code: c.code})
			if d.Resolved != c.wantResolved {
				t.Errorf("code=%s Resolved(确定结论)=%v, want %v", c.code, d.Resolved, c.wantResolved)
			}
			if d.Returned != c.wantReturned {
				t.Errorf("code=%s Returned(成功查得数)=%v, want %v", c.code, d.Returned, c.wantReturned)
			}
		})
	}
}

// TestTableFor_PerRouteChargeScope 钉住「哪条路由对查无也收费」。这张表直接对应
// 上游文档里的计费标注，改动前必须先回文档核对：
//   - docs/黑名单因子V35.pdf §2.1：10 查询成功【计费】/ 1000 未查得【计费】
//   - docs/伽马分层分_定制版.pdf §2.1：10 查询成功【计费】/ 1000 数据未查得（不计费）
//
// 两者是同一供应商的同一端点、同一 busiCode 语义，唯独计费口径不同——最容易被
// "看起来一样就复用"的直觉改错，故单列测试。
func TestTableFor_PerRouteChargeScope(t *testing.T) {
	cases := []struct {
		route        string
		code         string
		wantReturned bool
		why          string
	}{
		{"blk", "001", true, "黑名单 10 查询成功【计费】"},
		{"blk", "999", true, "黑名单 1000 未查得【计费】"},
		{"x1", "001", true, "伽马 10 查询成功【计费】"},
		{"x1", "999", false, "伽马 1000 数据未查得，文档未标计费"},
		{"zlf", "999", false, "租赁分 SW0002 查无记录 不收费"},
		{"xfjy", "999", false, "消费交易特征 result=1 未查得（不计费）"},
		{"grsb", "999", false, "背景评估 2-404 没有查询到数据 不计费"},
		{"lxf", "999", false, "灵犀分 分数=-1 查得失败"},
	}

	for _, c := range cases {
		t.Run(c.route+"/"+c.code, func(t *testing.T) {
			d := New(TableFor(c.route)).Decide(&model.UpstreamResult{Code: c.code})
			if !d.Resolved {
				t.Fatalf("code=%s 应为确定结论", c.code)
			}
			if d.Returned != c.wantReturned {
				t.Errorf("route=%s code=%s 计费=%v, want %v（%s）", c.route, c.code, d.Returned, c.wantReturned, c.why)
			}
		})
	}
}

// TableFor 每次都要返回独立的表，否则给某条路由加计费码会污染其它路由。
func TestTableForReturnsIndependentTables(t *testing.T) {
	blk := TableFor("blk")
	x1 := TableFor("x1")
	if !blk.IsReturned("999") {
		t.Fatal("blk 的 999 应计费")
	}
	if x1.IsReturned("999") {
		t.Fatal("x1 的 999 不应计费——TableFor 返回了共享的 map")
	}
	if DefaultTable().IsReturned("999") {
		t.Fatal("DefaultTable 被污染：999 不应计费")
	}
}
