package memory

import (
	"context"
	"testing"
	"time"

	"github.com/datahub/relay/internal/domain/model"
)

// seedAudit 追加一条审计行 (仅填聚合/过滤用到的字段)。
func seedAudit(t *testing.T, s *Store, ver, appKey, reqid string, called, found, billed, cache bool, when time.Time) {
	t.Helper()
	rec := &model.AuditRecord{
		RequestID: "req-" + reqid, Version: ver, AppKey: appKey, Reqid: reqid,
		CalledUpstream: called, FoundData: found, Billed: billed, FromCache: cache,
		UpstreamUID: "uid-" + reqid, UpstreamLogID: "log-" + reqid, UpstreamCode: "001",
		CreatedAt: when,
	}
	if err := s.AppendAudit(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
}

func TestBillingSummaryAndCount(t *testing.T) {
	s := New()
	now := time.Now()
	// 路由 r1：客户 A 三条 (查得2 计费2 调用3)，客户 B 一条 (查无但计费=查询计费)。
	seedAudit(t, s, "r1", "A", "a1", true, true, true, false, now)
	seedAudit(t, s, "r1", "A", "a2", true, true, true, false, now)
	seedAudit(t, s, "r1", "A", "a3", true, false, false, false, now) // 查无不计费
	seedAudit(t, s, "r1", "B", "b1", true, false, true, false, now)  // 查无但计费
	// 另一路由 r2，不应混入 r1 聚合。
	seedAudit(t, s, "r2", "A", "x1", true, true, true, false, now)

	aggs, err := s.BillingSummary(context.Background(), model.AuditFilter{Version: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*model.BillingAgg{}
	for _, a := range aggs {
		got[a.AppKey] = a
	}
	if a := got["A"]; a == nil || a.Calls != 3 || a.Found != 2 || a.Billed != 2 {
		t.Fatalf("客户A 聚合错误: %+v", a)
	}
	if b := got["B"]; b == nil || b.Calls != 1 || b.Found != 0 || b.Billed != 1 {
		t.Fatalf("客户B 聚合错误 (查询计费应 billed=1 found=0): %+v", b)
	}

	// CountAudits 应忽略 Limit/Offset，按 filter 统计 r1 全部 4 行。
	c, err := s.CountAudits(context.Background(), model.AuditFilter{Version: "r1", Limit: 1, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if c != 4 {
		t.Fatalf("CountAudits=%d, want 4", c)
	}
}

func TestReqidAndUpstreamFilters(t *testing.T) {
	s := New()
	now := time.Now()
	seedAudit(t, s, "r1", "A", "k1", true, true, true, false, now)  // 真调上游
	seedAudit(t, s, "r1", "A", "k2", false, true, true, true, now)  // 走缓存回放
	seedAudit(t, s, "r1", "A", "k3", true, false, false, false, now)

	// reqid 精确匹配 (reqid 或 requestId)。
	byReqid, _ := s.ListAudits(context.Background(), model.AuditFilter{Version: "r1", Reqid: "k2"})
	if len(byReqid) != 1 || byReqid[0].Reqid != "k2" {
		t.Fatalf("按 reqid 过滤失败: %d", len(byReqid))
	}
	byReqID, _ := s.ListAudits(context.Background(), model.AuditFilter{Version: "r1", Reqid: "req-k1"})
	if len(byReqID) != 1 || byReqID[0].Reqid != "k1" {
		t.Fatalf("按 requestId 过滤失败: %d", len(byReqID))
	}

	// 上游对账口径：called_upstream=true 且排除走缓存 → 只剩 k1、k3。
	called := true
	up, _ := s.ListAudits(context.Background(), model.AuditFilter{Version: "r1", CalledUpstream: &called, ExcludeCache: true})
	if len(up) != 2 {
		t.Fatalf("上游过滤应得 2 行 (排除走缓存), got %d", len(up))
	}
	for _, a := range up {
		if a.FromCache {
			t.Fatal("走缓存行不应出现在上游对账")
		}
		if !a.CalledUpstream {
			t.Fatal("未调上游行不应出现在上游对账")
		}
	}
}
