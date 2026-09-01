package application

import (
	"context"
	"testing"

	"github.com/datahub/relay/internal/domain/model"
)

// 计费口径与报文形态必须解耦：body.code 只由上游归一码决定，是否累计成功查得数
// 只由该路由的 billing.DecisionTable 决定。两者在 blk 上必然分叉——
// docs/黑名单因子V35.pdf §2.1 把「1000 未查得」也标了【计费】。
//
// 早期实现用 decision.Returned 兼作报文分支条件，一旦按文档把 blk 的 999 改成计费，
// 下游就会把查无收到 body.code=001。本测试同时钉住这两侧。
func TestBillingScopeDecoupledFromWireCode(t *testing.T) {
	cases := []struct {
		route        string
		upstreamCode string
		wantBodyCode string // 下游看到的 body.code
		wantUsed     int64  // 成功查得数（= 计费条数）
		why          string
	}{
		{
			route: "x1", upstreamCode: "001", wantBodyCode: "001", wantUsed: 1,
			why: "伽马 10 查询成功【计费】",
		},
		{
			route: "x1", upstreamCode: "999", wantBodyCode: "999", wantUsed: 0,
			why: "伽马 1000 数据未查得，文档未标计费",
		},
		{
			route: "blk", upstreamCode: "001", wantBodyCode: "001", wantUsed: 1,
			why: "黑名单 10 查询成功【计费】",
		},
		{
			route: "blk", upstreamCode: "999", wantBodyCode: "999", wantUsed: 1,
			why: "黑名单 1000 未查得【计费】——查无照样收费，但下游仍应看到 999",
		},
	}

	for _, tc := range cases {
		t.Run(tc.route+"/"+tc.upstreamCode, func(t *testing.T) {
			f := newCacheFixture(t, tc.route, tc.upstreamCode, false)
			resp := f.query(t, "rq-1", map[string]string{
				"name": "张三", "idCard": "330129199109094312", "mobile": "13800000000",
			})

			if resp.Body == nil || resp.Body.Code != tc.wantBodyCode {
				t.Fatalf("body.code = %v, 期望 %s（%s）", bodyCode(resp), tc.wantBodyCode, tc.why)
			}
			used, calls := f.counters(t, tc.route)
			if used != tc.wantUsed {
				t.Fatalf("成功查得数 = %d, 期望 %d（%s）", used, tc.wantUsed, tc.why)
			}
			if calls != 1 {
				t.Fatalf("调用上游次数 = %d, 期望 1", calls)
			}

			rec := f.auditOf(t, "rq-1")
			if rec == nil {
				t.Fatal("缺审计记录")
			}
			if rec.Billed != (tc.wantUsed == 1) {
				t.Fatalf("审计 billed = %v, 期望 %v（%s）", rec.Billed, tc.wantUsed == 1, tc.why)
			}
			// found_data 跟的是「有没有查到数据」，与计费各走各的：blk 查无计费那行
			// 必须是 billed=true 而 found_data=false，对账时才分得清。
			if rec.FoundData != model.IsFoundCode(tc.upstreamCode) {
				t.Fatalf("审计 found_data = %v, 期望 %v", rec.FoundData, model.IsFoundCode(tc.upstreamCode))
			}
		})
	}
}

// 幂等重放的报文形态也要看台账里的上游归一码，而不是 counted_service：blk 的查无
// 计费行 counted_service=true，用它分支会把重放从 999 错升成 001。
func TestReplayWireCodeFollowsUpstreamCode(t *testing.T) {
	f := newCacheFixture(t, "blk", "999", false)
	body := map[string]string{"name": "张三", "idCard": "330129199109094312", "mobile": "13800000000"}

	first := f.query(t, "rq-1", body)
	if first.Body.Code != "999" {
		t.Fatalf("首查 body.code = %s, 期望 999", first.Body.Code)
	}

	ledgers, err := f.store.ListByState(context.Background(), model.StateBilled, 10)
	if err != nil {
		t.Fatalf("ListByState: %v", err)
	}
	if len(ledgers) != 1 {
		t.Fatalf("台账 %d 条, 期望 1", len(ledgers))
	}
	l := ledgers[0]
	if !l.CountedService {
		t.Fatal("blk 查无应计费：counted_service 期望 true")
	}
	if l.UpstreamCode != "999" {
		t.Fatalf("台账 upstream_code = %q, 期望 999", l.UpstreamCode)
	}

	replayed := f.orch.replay(l, "rq-2", &model.AuditRecord{}, 0)
	if replayed.Body.Code != "999" {
		t.Fatalf("重放 body.code = %s, 期望 999（计费标志不得改变报文形态）", replayed.Body.Code)
	}
}

func bodyCode(r *model.QueryResponse) any {
	if r.Body == nil {
		return nil
	}
	return r.Body.Code
}
