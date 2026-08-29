package application

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datahub/relay/internal/common/appctx"
	"github.com/datahub/relay/internal/domain/auth"
	"github.com/datahub/relay/internal/domain/billing"
	"github.com/datahub/relay/internal/domain/cache"
	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/domain/quota"
	"github.com/datahub/relay/internal/infrastructure/persistence/memory"
	"github.com/datahub/relay/internal/infrastructure/secret"
)

// countingUpstream 统计回源次数，并让每次回源的 uid/reqid 都不同——于是「uid 是否
// 变化」就成了「这一次到底回源了没有」的硬观测，不必依赖日志或时序。
type countingUpstream struct {
	calls atomic.Int64
	code  string // 上游返回的业务码：001 查得 / 999 查无 / 其它=不确定结论
}

func (u *countingUpstream) Query(_ context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	n := u.calls.Add(1)
	r := &model.UpstreamResult{
		Code:  u.code,
		Msg:   "mock",
		UID:   "up-uid-" + strconv.FormatInt(n, 10),
		LogID: "up-log-" + strconv.FormatInt(n, 10),
		Reqid: req.Reqid,
	}
	if u.code == "001" {
		r.Range = "7"
	}
	return r, nil
}

func (u *countingUpstream) Requery(context.Context, string) (*model.RequeryResult, error) {
	return &model.RequeryResult{Reachable: false}, nil
}

// cacheFixture 是一套完整的 x1 调用栈：memory 存储 + 计数上游 + 已启用的月度缓存。
// 每次 query 都新起一个 Bookkeeper 并在 Handle 返回后 Close(drain)，于是走的是**真正
// 的异步写缓存路径**（生产同款），同时又给了测试确定的时序，无需 sleep。
type cacheFixture struct {
	orch   *QueryOrchestrator
	store  *memory.Store
	quota  *quota.Service
	up     *countingUpstream
	rcache *memory.ResultCache
	lic    *model.LicenseView
	secret string
}

func newCacheFixture(t *testing.T, route, upstreamCode string, withCache bool) *cacheFixture {
	t.Helper()
	store := memory.New()
	lic := &model.LicenseView{LicenseID: "LIC-CACHE", AppKey: "ak-cache", ClientUUID: "u1", Status: "ACTIVE"}
	store.SeedLicense(lic, "sec", "缓存测试商户", "13800000000")

	up := &countingUpstream{code: upstreamCode}
	q := quota.New(store, store)
	orch := NewQueryOrchestrator(route,
		auth.New(store, secret.NewStore(store), auth.Md5Verifier{}), q, billing.New(nil), up, store, nil)

	f := &cacheFixture{orch: orch, store: store, quota: q, up: up, lic: lic, secret: "sec"}
	if withCache {
		policy, err := cache.NewPolicy(route, "test-pepper", 0)
		if err != nil {
			t.Fatalf("NewPolicy: %v", err)
		}
		f.rcache = memory.NewResultCache()
		orch.WithResultCache(f.rcache, policy)
	}
	return f
}

// query 发一次完整请求（带签名与 requestId），并等异步记账（结算 + 审计 + 写缓存）
// 全部落地后再返回。
func (f *cacheFixture) query(t *testing.T, requestID string, body map[string]string) *model.QueryResponse {
	t.Helper()
	books := NewBookkeeper(f.quota, f.store, 64, 1, nil)
	if f.rcache != nil {
		books.WithResultCache(f.rcache)
	}
	f.orch.WithBookkeeper(books)

	ctx := appctx.WithRequestID(context.Background(), requestID)
	signed := &model.SignedRequest{AppKey: f.lic.AppKey, Sign: auth.Sign(body, f.secret), BodyParams: body}
	cmd := &model.QueryCommand{Name: body["name"], IDCard: body["idCard"], Mobile: body["mobile"]}

	resp := f.orch.Handle(ctx, signed, cmd)
	books.Close() // drain：等台账/审计/缓存全部落地
	return resp
}

func (f *cacheFixture) auditOf(t *testing.T, requestID string) *model.AuditRecord {
	t.Helper()
	rows, err := f.store.ListAudits(context.Background(), model.AuditFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListAudits: %v", err)
	}
	for _, r := range rows {
		if r.RequestID == requestID {
			return r
		}
	}
	return nil
}

func (f *cacheFixture) counters(t *testing.T, route string) (used, calls int64) {
	t.Helper()
	used, err := f.store.ServiceUsed(context.Background(), f.lic.LicenseID, route)
	if err != nil {
		t.Fatalf("ServiceUsed: %v", err)
	}
	calls, err = f.store.TotalCalls(context.Background(), f.lic.LicenseID, route)
	if err != nil {
		t.Fatalf("TotalCalls: %v", err)
	}
	return used, calls
}

func personBody() map[string]string {
	return map[string]string{"name": "张三", "idCard": "330129199109094312", "mobile": "13809091009"}
}

// TestCacheHitSkipsUpstreamAndStillBills 是本特性的核心契约：
// 同一人在同一自然月内的第二次查询不回源，但照常给客户计费。
func TestCacheHitSkipsUpstreamAndStillBills(t *testing.T) {
	f := newCacheFixture(t, "x1", "001", true)

	r1 := f.query(t, "req-1", personBody())
	if r1.Body == nil || r1.Body.Code != "001" || r1.Body.Result == nil || r1.Body.Result.Range != "7" {
		t.Fatalf("首查未查得: %+v", r1.Body)
	}
	if got := f.up.calls.Load(); got != 1 {
		t.Fatalf("首查回源次数=%d, want 1", got)
	}

	r2 := f.query(t, "req-2", personBody())

	// 1. 不回源。
	if got := f.up.calls.Load(); got != 1 {
		t.Fatalf("二次查询回源次数=%d, want 1（应命中缓存）", got)
	}
	// 2. 结果逐字一致。
	if r2.Body == nil || r2.Body.Code != "001" || r2.Body.Result == nil || r2.Body.Result.Range != "7" {
		t.Fatalf("回放结果不一致: %+v", r2.Body)
	}
	// 3. uid/logId 用缓存原值（能追回上游那笔订单）。
	if r2.Body.UID != r1.Body.UID {
		t.Fatalf("回放 uid=%q, want 首查原值 %q", r2.Body.UID, r1.Body.UID)
	}
	// 4. reqid 每次唯一（下游去重逻辑不会误判成重复报文）。
	if r2.Body.Reqid == "" || r2.Body.Reqid == r1.Body.Reqid {
		t.Fatalf("回放 reqid=%q 必须是本次请求的新流水号（首查 %q）", r2.Body.Reqid, r1.Body.Reqid)
	}
	if r2.Head.LogID != "req-2" {
		t.Fatalf("head.logId=%q, want 本次 requestId req-2", r2.Head.LogID)
	}
	// 5. 计费口径：查得计 serviceUsed（两次都计），但 totalCalls 只记真的调用上游那次。
	used, calls := f.counters(t, "x1")
	if used != 2 {
		t.Fatalf("serviceUsed=%d, want 2（缓存命中照常计费）", used)
	}
	if calls != 1 {
		t.Fatalf("totalCalls=%d, want 1（命中没调上游）", calls)
	}
	// 6. 台账：命中行一次写成 BILLED 终态并标记 from_cache。
	l, err := f.store.FindByReqid(context.Background(), f.lic.AppKey, "x1", r2.Body.Reqid)
	if err != nil || l == nil {
		t.Fatalf("命中未落台账: %v %v", l, err)
	}
	if l.State != model.StateBilled || !l.CountedService || !l.FromCache {
		t.Fatalf("命中台账口径错: state=%s counted=%v fromCache=%v", l.State, l.CountedService, l.FromCache)
	}
	// 7. 审计：fromCache=true 解释了「未调上游却计费」。
	a1, a2 := f.auditOf(t, "req-1"), f.auditOf(t, "req-2")
	if a1 == nil || a2 == nil {
		t.Fatalf("审计缺失: a1=%v a2=%v", a1, a2)
	}
	if a1.FromCache || !a1.CalledUpstream {
		t.Fatalf("回源行标记错: fromCache=%v calledUpstream=%v", a1.FromCache, a1.CalledUpstream)
	}
	if !a2.FromCache || a2.CalledUpstream || !a2.Billed || !a2.FoundData {
		t.Fatalf("命中行标记错: fromCache=%v calledUpstream=%v billed=%v found=%v",
			a2.FromCache, a2.CalledUpstream, a2.Billed, a2.FoundData)
	}
}

// TestCacheHitNotFound 查无(999) 同样缓存一个月（同样花掉了一次上游调用），但不计费。
func TestCacheHitNotFound(t *testing.T) {
	f := newCacheFixture(t, "x1", "999", true)

	if r := f.query(t, "req-1", personBody()); r.Body == nil || r.Body.Code != "999" {
		t.Fatalf("首查应查无: %+v", r.Body)
	}
	r2 := f.query(t, "req-2", personBody())

	if got := f.up.calls.Load(); got != 1 {
		t.Fatalf("查无二次回源次数=%d, want 1（查无也缓存）", got)
	}
	if r2.Body == nil || r2.Body.Code != "999" {
		t.Fatalf("回放应为查无: %+v", r2.Body)
	}
	used, calls := f.counters(t, "x1")
	if used != 0 {
		t.Fatalf("serviceUsed=%d, want 0（查无不计费）", used)
	}
	if calls != 1 {
		t.Fatalf("totalCalls=%d, want 1", calls)
	}
	l, _ := f.store.FindByReqid(context.Background(), f.lic.AppKey, "x1", r2.Body.Reqid)
	if l == nil || l.State != model.StateBilled || l.CountedService || !l.FromCache {
		t.Fatalf("查无命中台账口径错: %+v", l)
	}
}

// TestCacheNotWrittenForUnresolved 上游给出的不确定结论（此处 013 校验签名错误）
// 绝不能入缓存——否则一次偶发的上游故障会被固化成整月的错误答案。
func TestCacheNotWrittenForUnresolved(t *testing.T) {
	f := newCacheFixture(t, "x1", "013", true)

	f.query(t, "req-1", personBody())
	f.query(t, "req-2", personBody())

	if got := f.up.calls.Load(); got != 2 {
		t.Fatalf("不确定结论回源次数=%d, want 2（不得缓存）", got)
	}
	if n := f.rcache.Len(); n != 0 {
		t.Fatalf("缓存条目数=%d, want 0", n)
	}
}

// TestCacheKeyedByIdentity 身份要素任一不同即另一次查询，必须回源。姓名在 x1 是选填，
// 但参与上游入参，所以「补了姓名」也是另一次查询。
func TestCacheKeyedByIdentity(t *testing.T) {
	f := newCacheFixture(t, "x1", "001", true)

	f.query(t, "req-1", personBody())

	renamed := personBody()
	renamed["name"] = "李四"
	f.query(t, "req-2", renamed)
	if got := f.up.calls.Load(); got != 2 {
		t.Fatalf("换姓名后回源次数=%d, want 2", got)
	}

	otherMobile := personBody()
	otherMobile["mobile"] = "13900001234"
	f.query(t, "req-3", otherMobile)
	if got := f.up.calls.Load(); got != 3 {
		t.Fatalf("换手机号后回源次数=%d, want 3", got)
	}

	// 归一化：前后空白与身份证小写 x 必须命中同一条。
	loose := map[string]string{"name": " 张三 ", "idCard": " 330129199109094312 ", "mobile": " 13809091009 "}
	f.query(t, "req-4", loose)
	if got := f.up.calls.Load(); got != 3 {
		t.Fatalf("归一化后回源次数=%d, want 3（应命中首查那条）", got)
	}
}

// TestCacheMonthBoundaryForcesRequery 跨自然月必须重新回源：月份写在 key 里，所以
// 这条规则不依赖 TTL 的精确性。用「把上月的键写满数据、再按本月键查」来模拟跨月。
func TestCacheMonthBoundaryForcesRequery(t *testing.T) {
	policy, err := cache.NewPolicy("x1", "test-pepper", 0)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	id := cache.IdentityOf(&model.UpstreamRequest{Name: "张三", IDCard: "330129199109094312", Mobile: "13809091009"})
	rc := memory.NewResultCache()

	aug := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	entry := cache.EntryOf(&model.UpstreamResult{Code: "001", Range: "7", UID: "aug-uid"}, "req-aug", aug)
	if err := rc.Set(context.Background(), policy.Key(id, aug), entry, policy.TTL(aug)); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// 同月：命中。
	hit, err := rc.Get(context.Background(), policy.Key(id, aug.Add(24*time.Hour)))
	if err != nil || hit == nil || hit.UID != "aug-uid" {
		t.Fatalf("同月应命中: hit=%+v err=%v", hit, err)
	}
	// 次月：同一个人、同一策略，键已不同 → 未命中 → 回源。
	sep := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	hit, err = rc.Get(context.Background(), policy.Key(id, sep))
	if err != nil || hit != nil {
		t.Fatalf("跨月必须未命中: hit=%+v err=%v", hit, err)
	}
}

// TestCacheDisabledKeepsLegacyBehaviour 未挂接缓存时行为与引入缓存前完全一致：
// 每次都回源、每次都记一次调用上游。
func TestCacheDisabledKeepsLegacyBehaviour(t *testing.T) {
	f := newCacheFixture(t, "x1", "001", false)

	f.query(t, "req-1", personBody())
	f.query(t, "req-2", personBody())

	if got := f.up.calls.Load(); got != 2 {
		t.Fatalf("未启用缓存时回源次数=%d, want 2", got)
	}
	used, calls := f.counters(t, "x1")
	if used != 2 || calls != 2 {
		t.Fatalf("未启用缓存计数错: serviceUsed=%d totalCalls=%d, want 2/2", used, calls)
	}
	if a := f.auditOf(t, "req-2"); a == nil || a.FromCache {
		t.Fatalf("未启用缓存时不得标记 fromCache: %+v", a)
	}
}

// TestBookkeeperDropsCacheWriteUnderBackpressure 队列满降级同步时必须丢弃写缓存：
// 该路径在响应写回前执行，每一次 IO 都直接加到下游耗时里。计费凭证不能丢，缓存可以。
func TestBookkeeperDropsCacheWriteUnderBackpressure(t *testing.T) {
	store := memory.New()
	q := quota.New(store, store)
	tok := seedBegin(t, store, q, "r-bp")
	rc := memory.NewResultCache()

	b := NewBookkeeper(q, store, 8, 1, nil).WithResultCache(rc)
	b.Close() // 已关闭 → Submit 走同步降级路径

	dec := &model.BillingDecision{Resolved: true, Returned: true, Result: &model.UpstreamResult{Code: "001"}}
	entry := cache.EntryOf(dec.Result, "req-bp", time.Now())
	b.Submit(bookTask{
		token: tok, decision: dec,
		rec:      &model.AuditRecord{RequestID: "req-bp", Version: "x1", AppKey: "ak-test"},
		cacheSet: &cacheWrite{key: "qc:x1:202608:deadbeef", entry: entry, ttl: time.Hour},
	})

	// 计费凭证必须落地。
	l, _ := store.FindByReqid(context.Background(), "ak-test", "x1", "r-bp")
	if l == nil || l.State != model.StateBilled {
		t.Fatalf("降级同步路径丢了计费台账: %+v", l)
	}
	// 缓存写入必须被丢弃。
	if n := rc.Len(); n != 0 {
		t.Fatalf("降级同步路径写了缓存(%d 条)，会把 Redis 往返加到下游耗时里", n)
	}
}

// TestBookkeeperWritesCacheAsync 正常异步路径必须真的把缓存写进去。
func TestBookkeeperWritesCacheAsync(t *testing.T) {
	store := memory.New()
	q := quota.New(store, store)
	tok := seedBegin(t, store, q, "r-async")
	rc := memory.NewResultCache()

	b := NewBookkeeper(q, store, 8, 1, nil).WithResultCache(rc)
	dec := &model.BillingDecision{Resolved: true, Returned: true, Result: &model.UpstreamResult{Code: "001", Range: "7"}}
	b.Submit(bookTask{
		token: tok, decision: dec,
		rec:      &model.AuditRecord{RequestID: "req-async", Version: "x1", AppKey: "ak-test"},
		cacheSet: &cacheWrite{key: "qc:x1:202608:cafebabe", entry: cache.EntryOf(dec.Result, "req-async", time.Now()), ttl: time.Hour},
	})
	b.Close() // drain

	hit, err := rc.Get(context.Background(), "qc:x1:202608:cafebabe")
	if err != nil || hit == nil || hit.Range != "7" {
		t.Fatalf("异步写缓存未生效: hit=%+v err=%v", hit, err)
	}
}
