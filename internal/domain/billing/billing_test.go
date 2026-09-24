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
//   - docs/身份风险V107(1).pdf §1.6：10 查询成功【计费】/ 1000 数据未查得【计费】
//   - docs/伽马分层分_定制版.pdf §2.1：10 查询成功【计费】/ 1000 数据未查得（不计费）
//
// 三者是同一供应商（应诺尔 enol）的同一端点、同一 busiCode 语义，唯独计费口径不同
// （blk/sffx 的查无收费，x1 的不收）——最容易被"看起来一样就复用"的直觉改错，
// 故单列测试。
func TestTableFor_PerRouteChargeScope(t *testing.T) {
	cases := []struct {
		route        string
		code         string
		wantReturned bool
		why          string
	}{
		{"blk", "001", true, "黑名单 10 查询成功【计费】"},
		{"blk", "999", true, "黑名单 1000 未查得【计费】"},
		{"sffx", "001", true, "身份风险V107 10 查询成功【计费】"},
		{"sffx", "999", true, "身份风险V107 1000 数据未查得【计费】"},
		{"x1", "001", true, "伽马 10 查询成功【计费】"},
		{"x1", "999", false, "伽马 1000 数据未查得，文档未标计费"},
		{"zlf", "999", false, "租赁分 SW0002 查无记录 不收费"},
		{"xfjy", "999", false, "消费交易特征 result=1 未查得（不计费）"},
		{"grsb", "999", false, "背景评估 2-404 没有查询到数据 不计费"},
		{"lxf", "999", false, "灵犀分 分数=-1 查得失败"},
		// dtjd 多头借贷行为：SW0002 查询无记录 不收费。注意 SW0001 认证失败被上游标
		// 【收费】且也归一到 999——但本路由按「下游查无 + 我方不计费」处理，故 dtjd
		// **绝不能**进 billNotFoundRoutes（一进就把 SW0002 也变成计费，对下游多收钱）。
		{"dtjd", "001", true, "多头借贷 SW0000 认证成功【收费】"},
		{"dtjd", "999", false, "多头借贷 SW0002 查询无记录 不收费（SW0001 虽被上游标收费也不向下游计费）"},
		// snhmd 司南黑名单：同供应商同端点，但码表的计费列与 dtjd 不同——本产品
		// SW0001 认证失败标【不收费】，客户端直接按上游侧错误返回（不归一到 999），
		// 故本路由的 999 **只**来自 SW0002 不收费，同样不进 billNotFoundRoutes。
		{"snhmd", "001", true, "司南黑名单 SW0000 认证成功【收费】（black_list=0 未命中亦属查得）"},
		{"snhmd", "999", false, "司南黑名单 SW0002 查询无记录 不收费"},
		// dtly 多头履约行为：同供应商同端点，SW0001 的计费列与 snhmd 相同、与 dtjd
		// 相反——本产品标【不收费】，客户端按上游侧错误返回，故 999 只来自 SW0002，
		// 同样不进 billNotFoundRoutes。
		{"dtly", "001", true, "多头履约 SW0000 认证成功【收费】（因子大多为空/模型分 -1 亦属查得）"},
		{"dtly", "999", false, "多头履约 SW0002 查询无记录 不收费"},
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
