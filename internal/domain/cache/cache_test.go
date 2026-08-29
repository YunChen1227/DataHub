package cache

import (
	"errors"
	"testing"
	"time"

	"github.com/datahub/relay/internal/domain/model"
)

func mustPolicy(t *testing.T, group string, jitter time.Duration) Policy {
	t.Helper()
	p, err := NewPolicy(group, "test-pepper", jitter)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	return p
}

var demoID = Identity{Name: "张三", IDCard: "11010119900307001X", Mobile: "13800001234"}

// TestNewPolicyRejectsEmptyPepper 没有 pepper 的指纹等于裸 SHA-256，身份证空间可枚举
// 反查——必须硬失败而非静默降级（包注释铁律 2）。
func TestNewPolicyRejectsEmptyPepper(t *testing.T) {
	for _, pepper := range []string{"", "   "} {
		if _, err := NewPolicy("x1", pepper, 0); !errors.Is(err, ErrNoPepper) {
			t.Fatalf("pepper=%q 应报 ErrNoPepper, got %v", pepper, err)
		}
	}
}

// TestKeyMonthBucketBoundary 自然月边界必须精确落在 +08:00 的月初零点：8/31 最后一秒
// 与 9/1 零点必须落到不同的 key，否则「跨月重新回源」这条规则就失效了。
func TestKeyMonthBucketBoundary(t *testing.T) {
	p := mustPolicy(t, "x1", 0)
	augEnd := time.Date(2026, 8, 31, 23, 59, 59, 0, cst)
	sepStart := time.Date(2026, 9, 1, 0, 0, 0, 0, cst)

	kAug, kSep := p.Key(demoID, augEnd), p.Key(demoID, sepStart)
	if kAug == kSep {
		t.Fatalf("跨月未换 key: %s", kAug)
	}
	if want := "qc:x1:202608:"; kAug[:len(want)] != want {
		t.Fatalf("8 月 key 前缀=%q, want %q", kAug[:len(want)], want)
	}
	if want := "qc:x1:202609:"; kSep[:len(want)] != want {
		t.Fatalf("9 月 key 前缀=%q, want %q", kSep[:len(want)], want)
	}
	// 同月内不同时刻必须命中同一条。
	if p.Key(demoID, time.Date(2026, 8, 1, 0, 0, 0, 0, cst)) != kAug {
		t.Fatal("同月不同时刻 key 不一致")
	}
}

// TestKeyUsesShanghaiNotUTC 时区错 8 小时会让月份边界整体漂移：UTC 8/31 16:30 在
// 东八区已是 9/1 00:30，必须落到 9 月桶。
func TestKeyUsesShanghaiNotUTC(t *testing.T) {
	p := mustPolicy(t, "x1", 0)
	utcTime := time.Date(2026, 8, 31, 16, 30, 0, 0, time.UTC)
	if want := "qc:x1:202609:"; p.Key(demoID, utcTime)[:len(want)] != want {
		t.Fatalf("UTC 输入未按 +08:00 归月: %s", p.Key(demoID, utcTime))
	}
}

// TestIdentityNormalisation 空白与身份证大小写归一化后必须命中同一条缓存。
func TestIdentityNormalisation(t *testing.T) {
	p := mustPolicy(t, "x1", 0)
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, cst)

	raw := IdentityOf(&model.UpstreamRequest{Name: "  张三 ", IDCard: " 11010119900307001x ", Mobile: " 13800001234 "})
	if got, want := p.Key(raw, now), p.Key(demoID, now); got != want {
		t.Fatalf("归一化后 key 不一致:\n got=%s\nwant=%s", got, want)
	}
}

// TestKeyVariesByIdentityGroupAndPepper 身份要素、共享组、pepper 任一不同都必须换 key。
func TestKeyVariesByIdentityGroupAndPepper(t *testing.T) {
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, cst)
	base := mustPolicy(t, "x1", 0)
	baseKey := base.Key(demoID, now)

	noName := demoID
	noName.Name = ""
	if base.Key(noName, now) == baseKey {
		t.Fatal("姓名选填但参与上游入参，传与不传必须是不同的缓存条目")
	}

	otherMobile := demoID
	otherMobile.Mobile = "13900001234"
	if base.Key(otherMobile, now) == baseKey {
		t.Fatal("手机号不同应换 key")
	}

	if mustPolicy(t, "v9", 0).Key(demoID, now) == baseKey {
		t.Fatal("不同共享组应换 key（x1 与 v8v9 上游产品不同，不能混用结果）")
	}

	other, err := NewPolicy("x1", "another-pepper", 0)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	if other.Key(demoID, now) == baseKey {
		t.Fatal("不同 pepper 应换 key")
	}
}

// TestTTLToMonthEnd 无抖动时 TTL 必须精确等于「距下个自然月零点」，跨年也不例外。
func TestTTLToMonthEnd(t *testing.T) {
	p := mustPolicy(t, "x1", 0)
	cases := []struct {
		name string
		now  time.Time
		want time.Duration
	}{
		{"月中", time.Date(2026, 8, 15, 0, 0, 0, 0, cst), 17 * 24 * time.Hour},
		{"月初零点", time.Date(2026, 8, 1, 0, 0, 0, 0, cst), 31 * 24 * time.Hour},
		{"跨年", time.Date(2026, 12, 31, 0, 0, 0, 0, cst), 24 * time.Hour},
		{"闰年二月", time.Date(2028, 2, 1, 0, 0, 0, 0, cst), 29 * 24 * time.Hour},
	}
	for _, c := range cases {
		if got := p.TTL(c.now); got != c.want {
			t.Errorf("%s: TTL=%v, want %v", c.name, got, c.want)
		}
	}
}

// TestTTLNeverNonPositive 月末最后一刻写入也必须给出正的 TTL（SETEX 不接受 <=0）。
func TestTTLNeverNonPositive(t *testing.T) {
	for _, jitter := range []time.Duration{0, 12 * time.Hour} {
		p := mustPolicy(t, "x1", jitter)
		// 月末最后一纳秒：距下月零点不足 1 秒。
		now := time.Date(2026, 9, 1, 0, 0, 0, 0, cst).Add(-time.Nanosecond)
		if got := p.TTL(now); got <= 0 {
			t.Fatalf("jitter=%v: TTL=%v, 必须为正", jitter, got)
		}
	}
}

// TestTTLJitterBounds 抖动必须落在 [到月末, 到月末+jitter) 区间内——超出上界会让上月
// key 多驻留内存，低于下界会在本月内提前失效导致意外回源。
func TestTTLJitterBounds(t *testing.T) {
	const jitter = 12 * time.Hour
	p := mustPolicy(t, "x1", jitter)
	now := time.Date(2026, 8, 15, 0, 0, 0, 0, cst)
	base := 17 * 24 * time.Hour

	var sawJitter bool
	for i := 0; i < 200; i++ {
		got := p.TTL(now)
		if got < base || got >= base+jitter {
			t.Fatalf("TTL=%v 越界 [%v, %v)", got, base, base+jitter)
		}
		if got != base {
			sawJitter = true
		}
	}
	if !sawJitter {
		t.Fatal("配置了抖动但 200 次取样全等于基线，抖动没生效")
	}
}

// TestCacheable 只有确定结论 (001 查得 / 999 查无) 可入缓存；上游错误码不缓存，
// 否则一次偶发故障会被固化成整月的错误答案（包注释铁律 3）。
func TestCacheable(t *testing.T) {
	cases := map[string]bool{"001": true, "999": true, "002": false, "013": false, "": false}
	for code, want := range cases {
		if got := Cacheable(&model.UpstreamResult{Code: code}); got != want {
			t.Errorf("Cacheable(%q)=%v, want %v", code, got, want)
		}
	}
	if Cacheable(nil) {
		t.Error("Cacheable(nil) 必须为 false")
	}
}

// TestEntryRoundTrip 回放必须保留上游标识 (uid/logId 供对账) 但换成本次请求的 reqid
// (下游看到的流水号每次唯一，不会被下游去重逻辑误判成重复报文)。
func TestEntryRoundTrip(t *testing.T) {
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, cst)
	src := &model.UpstreamResult{
		Code: "001", Msg: "成功", UID: "up-uid-1", Reqid: "first-reqid",
		Range: "42", LogID: "up-log-1",
	}
	e := EntryOf(src, "first-request-id", now)
	if !e.Found() || e.SrcRequestID != "first-request-id" || e.CachedAt != now.Unix() {
		t.Fatalf("EntryOf 未装配完整: %+v", e)
	}

	got := e.Result("second-reqid")
	if got.UID != "up-uid-1" || got.LogID != "up-log-1" || got.Range != "42" || got.Code != "001" {
		t.Fatalf("回放丢失上游标识/结果: %+v", got)
	}
	if got.Reqid != "second-reqid" {
		t.Fatalf("回放 reqid=%q, want 本次请求的 second-reqid", got.Reqid)
	}

	notFound := EntryOf(&model.UpstreamResult{Code: "999", Msg: "查无结果"}, "rid", now)
	if notFound.Found() {
		t.Fatal("999 不应算查得")
	}
}
