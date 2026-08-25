package upstream

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datahub/relay/internal/domain/model"
)

// fakeSource 是一个可编程的 port.UpstreamPort 桩：记录调用次数，按预设返回结果/错误。
type fakeSource struct {
	calls  int32
	result *model.UpstreamResult
	err    error
	delay  time.Duration
}

func (f *fakeSource) Query(ctx context.Context, _ *model.UpstreamRequest) (*model.UpstreamResult, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.result, f.err
}

func (f *fakeSource) Requery(context.Context, string) (*model.RequeryResult, error) {
	return &model.RequeryResult{Reachable: false}, nil
}

func (f *fakeSource) count() int32 { return atomic.LoadInt32(&f.calls) }

func hit(uid string) *model.UpstreamResult {
	return &model.UpstreamResult{Code: "001", UID: uid, LogID: uid, Range: `{"cbjfzt":"1"}`}
}
func empty(uid string) *model.UpstreamResult {
	return &model.UpstreamResult{Code: "999", UID: uid, LogID: uid}
}

func mustSourcer(t *testing.T, srcs []Source) *Sourcer {
	t.Helper()
	s, err := NewSourcer(srcs, time.Second)
	if err != nil {
		t.Fatalf("NewSourcer: %v", err)
	}
	return s
}

// 命中即停：主源查得，低优先级备源零调用。
func TestSourcerHitStopsFirst(t *testing.T) {
	primary := &fakeSource{result: hit("P1")}
	backup := &fakeSource{result: hit("B1")}
	s := mustSourcer(t, []Source{
		{Name: "primary", Priority: 0, Port: primary},
		{Name: "backup", Priority: 10, Port: backup},
	})
	res, err := s.Query(context.Background(), &model.UpstreamRequest{Reqid: "r1"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Code != "001" || res.UID != "P1" {
		t.Fatalf("want 001/P1, got %s/%s", res.Code, res.UID)
	}
	if backup.count() != 0 {
		t.Fatalf("命中即停被破坏：备源被调用了 %d 次", backup.count())
	}
}

// 回落：主源查无，回落到备源并最终查得。
func TestSourcerFallbackToBackup(t *testing.T) {
	primary := &fakeSource{result: empty("P0")}
	backup := &fakeSource{result: hit("B1")}
	s := mustSourcer(t, []Source{
		{Name: "primary", Priority: 0, Port: primary},
		{Name: "backup", Priority: 10, Port: backup},
	})
	res, err := s.Query(context.Background(), &model.UpstreamRequest{Reqid: "r2"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Code != "001" || res.UID != "B1" {
		t.Fatalf("want 001/B1, got %s/%s", res.Code, res.UID)
	}
	if primary.count() != 1 || backup.count() != 1 {
		t.Fatalf("want primary=1 backup=1, got %d/%d", primary.count(), backup.count())
	}
}

// 主源失败也回落，最终查得 001。
func TestSourcerFallbackOnError(t *testing.T) {
	primary := &fakeSource{err: busiErr("301", "非白名单IP", "P-ord", "P-ord")}
	backup := &fakeSource{result: hit("B1")}
	s := mustSourcer(t, []Source{
		{Name: "primary", Priority: 0, Port: primary},
		{Name: "backup", Priority: 10, Port: backup},
	})
	res, err := s.Query(context.Background(), &model.UpstreamRequest{Reqid: "r3"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Code != "001" || res.UID != "B1" {
		t.Fatalf("want 001/B1, got %s/%s", res.Code, res.UID)
	}
}

// 全部源查无 → 999。
func TestSourcerAllEmpty(t *testing.T) {
	s := mustSourcer(t, []Source{
		{Name: "primary", Priority: 0, Port: &fakeSource{result: empty("P0")}},
		{Name: "backup", Priority: 10, Port: &fakeSource{result: empty("B0")}},
	})
	res, err := s.Query(context.Background(), &model.UpstreamRequest{Reqid: "r4"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Code != "999" {
		t.Fatalf("want 999, got %s", res.Code)
	}
	if res.UID == "" || res.LogID == "" {
		t.Fatalf("查无也要带代表源上游标识，got uid=%q logid=%q", res.UID, res.LogID)
	}
}

// 无源查得 + 有失败 → 002。
func TestSourcerNoDataPartialFail(t *testing.T) {
	s := mustSourcer(t, []Source{
		{Name: "primary", Priority: 0, Port: &fakeSource{result: empty("P0")}},
		{Name: "backup", Priority: 10, Port: &fakeSource{err: busiErr("500", "系统异常", "B-ord", "B-ord")}},
	})
	res, err := s.Query(context.Background(), &model.UpstreamRequest{Reqid: "r5"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Code != "002" {
		t.Fatalf("want 002, got %s", res.Code)
	}
}

// 全部源失败 → *model.UpstreamError，且带上代表源上游标识 (失败也要可追查铁律)。
func TestSourcerAllFailedCarriesUpstreamIDs(t *testing.T) {
	s := mustSourcer(t, []Source{
		{Name: "primary", Priority: 0, Port: &fakeSource{err: busiErr("301", "非白名单IP", "P-ord", "P-log")}},
		{Name: "backup", Priority: 10, Port: &fakeSource{err: busiErr("500", "系统异常", "B-ord", "B-log")}},
	})
	_, err := s.Query(context.Background(), &model.UpstreamRequest{Reqid: "r6"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	var ue *model.UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("want *model.UpstreamError, got %T", err)
	}
	if ue.UID != "P-ord" || ue.LogID != "P-log" || ue.Code != "301" {
		t.Fatalf("代表源标识应取第一个失败源，got code=%s uid=%s logid=%s", ue.Code, ue.UID, ue.LogID)
	}
}

// 排序：低 priority 值先查，即便配置顺序颠倒。
func TestSourcerPriorityOrdering(t *testing.T) {
	first := &fakeSource{result: hit("FIRST")}
	second := &fakeSource{result: hit("SECOND")}
	// 配置顺序：high-priority(10) 在前，low-priority(0) 在后；排序后应先查 low。
	s := mustSourcer(t, []Source{
		{Name: "high", Priority: 10, Port: second},
		{Name: "low", Priority: 0, Port: first},
	})
	res, err := s.Query(context.Background(), &model.UpstreamRequest{Reqid: "r7"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.UID != "FIRST" {
		t.Fatalf("应先查 priority 更小的 low 源 (FIRST)，got %s", res.UID)
	}
	if second.count() != 0 {
		t.Fatalf("命中即停：high 源应零调用，got %d", second.count())
	}
}

// 预算耗尽：主源耗时超预算，后续源被跳过，不再无限拖延。
func TestSourcerBudgetExhausted(t *testing.T) {
	slow := &fakeSource{result: empty("SLOW"), delay: 40 * time.Millisecond}
	backup := &fakeSource{result: hit("B1")}
	s, err := NewSourcer([]Source{
		{Name: "slow", Priority: 0, Port: slow},
		{Name: "backup", Priority: 10, Port: backup},
	}, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("NewSourcer: %v", err)
	}
	// slow 源在预算内被 ctx 取消返回错误，预算耗尽后 backup 被 skipped。
	res, qErr := s.Query(context.Background(), &model.UpstreamRequest{Reqid: "r8"})
	// slow 返回 ctx.Err()(非 UpstreamError) → errCnt，backup skipped → errCnt；
	// emptyCnt==0 → 返回 error。
	if qErr == nil && res != nil && res.Code == "001" {
		t.Fatalf("预算耗尽不应仍命中 backup")
	}
	if backup.count() != 0 {
		t.Fatalf("预算耗尽后 backup 应被跳过（零调用），got %d", backup.count())
	}
}

// 单源直通：Sourcer 只有一个源时行为等价于直接调用该源。
func TestSourcerSinglePassthrough(t *testing.T) {
	only := &fakeSource{result: hit("ONLY")}
	s := mustSourcer(t, []Source{{Name: "only", Port: only}})
	res, err := s.Query(context.Background(), &model.UpstreamRequest{Reqid: "r9"})
	if err != nil || res.UID != "ONLY" {
		t.Fatalf("单源直通失败: res=%v err=%v", res, err)
	}
}
