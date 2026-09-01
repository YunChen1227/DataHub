// Package application wires the domain services into the主调用流程 (DESIGN §4).
// It owns transaction/flow orchestration only — no business rules live here.
package application

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/datahub/relay/internal/common/appctx"
	"github.com/datahub/relay/internal/common/errs"
	"github.com/datahub/relay/internal/common/mask"
	"github.com/datahub/relay/internal/domain/auth"
	"github.com/datahub/relay/internal/domain/billing"
	"github.com/datahub/relay/internal/domain/cache"
	"github.com/datahub/relay/internal/domain/mapping"
	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/domain/parse"
	"github.com/datahub/relay/internal/domain/port"
	"github.com/datahub/relay/internal/domain/quota"
)

// QueryOrchestrator implements the §4 sequence. route 标记本编排器服务的路由
// (x1/v9/v8/zlf/blk/rlbd1/...)，用于把统计/台账/审计按路由作用域隔离 (共享 license 的 v8/v9)。
type QueryOrchestrator struct {
	route    string
	auth     *auth.Service
	quota    *quota.Service
	billing  *billing.Service
	upstream port.UpstreamPort
	audit    port.AuditRepository
	books    *Bookkeeper // 异步记账（结算+审计）；nil 时退化为同步（测试/未装配）
	parseFn  func(*model.QueryCommand) (*model.UpstreamRequest, error)
	// cache/cachePolicy 是「自然月结果缓存」（domain/cache）；nil 时全部行为与未引入
	// 缓存时完全一致。仅个人三要素路由 (x1/v8/v9) 可挂接——见 WithResultCache。
	cache       port.ResultCache
	cachePolicy cache.Policy
	log         *slog.Logger
}

func NewQueryOrchestrator(route string, a *auth.Service, q *quota.Service, b *billing.Service, up port.UpstreamPort, audit port.AuditRepository, log *slog.Logger) *QueryOrchestrator {
	if log == nil {
		log = slog.Default()
	}
	return &QueryOrchestrator{route: route, auth: a, quota: q, billing: b, upstream: up, audit: audit, parseFn: parse.Parse, log: log}
}

// WithParser 替换本路由的参数校验器 (默认 parse.Parse 个人三要素)。企业维度等
// 非三要素入参的路由在装配时调用 (如 rlbd1 → parse.ParseFace)。
func (o *QueryOrchestrator) WithParser(fn func(*model.QueryCommand) (*model.UpstreamRequest, error)) *QueryOrchestrator {
	if fn != nil {
		o.parseFn = fn
	}
	return o
}

// WithBookkeeper 挂接异步记账器：结算 + 审计移出响应关键路径（每请求省 3-5 次
// 串行 DB 写）。未挂接时保持旧行为（同步落库）。
func (o *QueryOrchestrator) WithBookkeeper(b *Bookkeeper) *QueryOrchestrator {
	o.books = b
	return o
}

// WithResultCache 挂接「自然月结果缓存」：同一人在同一自然月内的重复查询直接回放
// 本月首查结果，跨月才回源上游。未挂接时行为与引入缓存前完全一致。
//
// 铁律：只有**入参恰好等于缓存身份要素 (name/idCard/mobile)** 的路由才能挂接，即
// 用 parse.Parse / parse.ParseWithName 校验的个人三要素路由。rlbd1/rlbd2/sfzhy 这
// 类入参含人像照片、xfjy 含授权书编号、tsfx 含命中级别策略的路由**绝不可挂接**：
// 缓存身份不含这些字段，会把「换了照片/换了策略的另一次查询」错判为同一次。装配
// 侧另有白名单把关 (cmd/relay/main.go cacheableRoutes)。
func (o *QueryOrchestrator) WithResultCache(c port.ResultCache, p cache.Policy) *QueryOrchestrator {
	if c != nil {
		o.cache = c
		o.cachePolicy = p
	}
	return o
}

// Handle runs the full request lifecycle and returns a ready-to-serialize
// QueryResponse (接口文档-经济能力.doc head/body). 网关级失败落在 head.errorCode;
// 查得/查无落在 body.code (001/999). A rich audit record (DESIGN §16.3) is
// written for every request via a deferred hook.
func (o *QueryOrchestrator) Handle(ctx context.Context, signed *model.SignedRequest, cmd *model.QueryCommand) *model.QueryResponse {
	requestID := appctx.RequestID(ctx)
	clientIP := appctx.ClientIP(ctx)
	start := time.Now()
	log := o.log.With("requestId", requestID, "clientIp", clientIP)
	lat := func() int64 { return time.Since(start).Milliseconds() }

	rec := &model.AuditRecord{
		RequestID:  requestID,
		Version:    o.route,
		AppKey:     signed.AppKey,
		ClientIP:   clientIP,
		NameMask:   mask.Name(cmd.Name),
		IDCardMask: mask.IDCard(cmd.IDCard),
		MobileMask: mask.Mobile(cmd.Mobile),
	}
	// 结算 + 审计 + 写缓存在响应构造完成后统一提交（异步记账，见 Bookkeeper）。
	// books 的各字段由 runCore 按走到哪条路径填入：拿到上游确定结论填 token/decision，
	// 缓存命中填 cacheHit，回源到确定结论填 cacheSet；鉴权/参数失败则只剩审计。
	books := bookTask{rec: rec}
	defer func() {
		rec.FoundData = rec.BusiCode == int(errs.BusiSuccess)
		rec.LatencyMs = lat()
		rec.CreatedAt = time.Now()
		o.submitBooks(books, log)
	}()

	fail := func(busi errs.BusiCode, msg string) *model.QueryResponse {
		rec.BusiCode = int(busi)
		rec.BusiMsg = msg
		return mapping.Error(busi, msg, requestID, lat())
	}

	// 1. License + appKey + signature.
	lic, err := o.auth.Authenticate(ctx, signed)
	if err != nil {
		ae := errs.AsAppError(err)
		rec.ErrMsg = ae.Error()
		log.Warn("auth failed", "busiCode", ae.Busi, "err", err)
		return fail(ae.Busi, ae.Msg)
	}
	log = log.With("appKey", lic.AppKey)

	// 2. 无额度限制：不做余额拦截，仅在查得数据时累计成功查得数 (见 Settle)。

	// 3. Param validation + build upstream request (我方拦截, before reserve).
	upReq, err := o.parseFn(cmd)
	if err != nil {
		ae := errs.AsAppError(err)
		rec.ErrMsg = ae.Error()
		log.Info("param invalid", "err", err)
		return fail(ae.Busi, ae.Msg)
	}
	rec.Reqid = upReq.Reqid
	log = log.With("reqid", upReq.Reqid)

	// 4-6. 自然月缓存 + 幂等 + 开台账 + 上游 (结算/写缓存移交异步记账).
	out := o.runCore(ctx, lic, upReq, requestID, rec, log)
	books.token, books.decision = out.settleTok, out.settleDec
	books.cacheSet = out.cacheSet
	if out.cached != nil {
		books.cacheHit = &cachedSettlement{
			lic: lic, route: o.route, reqid: upReq.Reqid, requestID: requestID, entry: out.cached,
		}
	}
	return o.respondX1(out, requestID, rec, lat())
}

// submitBooks 提交记账任务：装配了 Bookkeeper 时异步（入队即返回）；否则同步
// 落库（保持旧行为，供测试与未装配场景）。同步路径用独立 ctx——本方法在响应
// 即将写回时执行，请求 ctx 生命周期已不可依赖。
func (o *QueryOrchestrator) submitBooks(t bookTask, log *slog.Logger) {
	if o.books != nil {
		o.books.Submit(t)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if t.token != nil && t.decision != nil {
		if err := o.quota.Settle(ctx, t.token, t.decision); err != nil {
			log.Error("settle failed", "err", err)
		}
	}
	if t.cacheHit != nil {
		h := t.cacheHit
		if err := o.quota.SettleCached(ctx, h.lic, h.route, h.reqid, h.requestID, h.entry); err != nil {
			log.Error("cached settle failed", "err", err)
		}
	}
	// 未装配 Bookkeeper 的同步路径不写缓存：本方法在响应写回前执行，写缓存会直接
	// 加到下游看到的耗时里。缓存条目丢了只是损失一次上游调用（生产恒装配 Bookkeeper）。
	if o.audit != nil {
		if err := o.audit.AppendAudit(ctx, t.rec); err != nil {
			log.Error("append audit failed", "err", err)
		}
	}
}

// queryOutcome is the normalized result of the post-auth core flow, shared by
// the x1 (head/body) and v9 (income_cls.md) response mappers.
type queryOutcome struct {
	decision *model.BillingDecision // settled verdict (查得/查无/未扣费)
	existing *model.Ledger          // idempotent hit (already BILLED)
	appErr   *errs.AppError         // reserve/upstream-unresolved failure
	// settleTok/settleDec 是移交异步记账的结算工作单（上游给出确定结论时成对
	// 填入；PENDING/重放/失败路径为 nil，台账留待复查/对账）。
	settleTok *quota.ReserveToken
	settleDec *model.BillingDecision
	// cached 是自然月结果缓存命中的条目：此时既未开台账也未调上游，终态台账与
	// 成功查得数由记账器异步补齐 (quota.SettleCached)。
	cached *cache.Entry
	// cacheSet 是回源拿到确定结论后待异步写入缓存的条目 (best-effort)。
	cacheSet *cacheWrite
}

// runCore runs the shared §4 steps after authentication: 幂等命中、开台账、上游
// 调用(+按 reqid 复查)。It updates the audit record's flow fields; settlement is
// handed off to the async Bookkeeper (settleTok/settleDec)；wire-format mapping
// is left to the caller.
func (o *QueryOrchestrator) runCore(ctx context.Context, lic *model.LicenseView, upReq *model.UpstreamRequest, requestID string, rec *model.AuditRecord, log *slog.Logger) queryOutcome {
	// 0. 自然月结果缓存。命中则直接回放：跳过同步开台账 (省 1 次 PG INSERT)、跳过
	// 上游调用 (省 200ms~2s + 一次上游费用)，故命中路径比未引入缓存时更快。
	// now 只取一次，保证 key 的月份桶与 TTL 出自同一时刻（否则月末临界点可能给
	// 8 月的 key 配上整个 9 月的 TTL，白占一个月内存）。
	now := time.Now()
	var cacheKey string
	if o.cache != nil {
		cacheKey = o.cachePolicy.Key(cache.IdentityOf(upReq), now)
		if hit := o.cacheLookup(ctx, cacheKey, log); hit != nil {
			rec.FromCache = true
			rec.UpstreamCode = hit.Code
			rec.UpstreamUID = hit.UID
			rec.UpstreamLogID = hit.LogID
			rec.Billed = hit.Found()
			// CalledUpstream 保持 false：本次确实没调上游，from_cache 列负责解释
			// 「未调上游却计费」这件事 (migrations/0007)。
			log.Info("自然月缓存命中，跳过上游", "cacheHit", true, "upstreamCode", hit.Code,
				"srcRequestId", hit.SrcRequestID)
			return queryOutcome{cached: hit}
		}
	}

	// reqidIsFresh=true：reqid 由本次请求内部新生成（parse.NewReqid），幂等查询
	// 必 miss，跳过该次 DB 读（关键路径优化，见 quota.Begin 注释）。
	token, existing, err := o.quota.Begin(ctx, lic, o.route, upReq.Reqid, "", requestID, true)
	if err != nil {
		ae := errs.AsAppError(err)
		rec.ErrMsg = ae.Error()
		log.Info("begin ledger failed", "busiCode", ae.Busi)
		return queryOutcome{appErr: ae}
	}
	if existing != nil {
		log.Info("idempotent hit, replaying cached billed result")
		rec.CalledUpstream = true
		rec.Billed = existing.CountedService
		return queryOutcome{existing: existing}
	}

	result, callErr := o.upstream.Query(ctx, upReq)
	var decision *model.BillingDecision
	if callErr != nil {
		// 失败也全量落审计：若上游"已应答但以业务码拒绝"(model.UpstreamError)，
		// 把上游返回的 code/uid(订单号)/logId(请求号) 记进审计，便于向上游对账追查
		// (即便随后 PENDING)。纯网络不可达没有这些标识，仅记 ErrMsg。
		var ue *model.UpstreamError
		if errors.As(callErr, &ue) {
			rec.CalledUpstream = true // 上游确已应答(业务失败)，属"已调用上游"
			rec.UpstreamCode = ue.Code
			rec.UpstreamUID = ue.UID
			rec.UpstreamLogID = ue.LogID
		}
		rec.ErrMsg = callErr.Error()
		log.Warn("upstream call failed, re-querying by reqid", "err", callErr)
		rr, rqErr := o.upstream.Requery(ctx, upReq.Reqid)
		if rqErr != nil || rr == nil || !rr.Reachable {
			// 保留上游错误详情(rec.ErrMsg 已含 code/msg/uid/logId)，追加未决说明。
			if rec.ErrMsg == "" {
				rec.ErrMsg = "上游超时/复查未决，PENDING 待对账"
			} else {
				rec.ErrMsg += " | 复查未决，PENDING 待对账"
			}
			log.Warn("re-query unresolved, leaving PENDING for reconciliation", "err", rqErr)
			return queryOutcome{appErr: errs.New(errs.BusiDataRequestErr, "")}
		}
		decision = o.billing.FromRequery(rr)
	} else {
		decision = o.billing.Decide(result)
	}

	// 结算移出关键路径：这里只装配结算工作单，实际 Settle（Redis 计数 + PG 镜像
	// + 台账 UPDATE）由 Bookkeeper 在响应写回后异步执行。
	if decision.Result != nil {
		rec.CalledUpstream = true
		rec.UpstreamCode = decision.Result.Code
		rec.UpstreamUID = decision.Result.UID
		rec.UpstreamLogID = decision.Result.LogID
	}
	rec.Billed = decision.Returned

	// 回源拿到确定结论 → 交给记账器在响应写回后异步写入缓存。只缓存确定结论
	// (001 查得 / 999 查无)：上游错误/PENDING 缓存下来会把一次偶发故障固化成整月
	// 的错误答案 (见 domain/cache 包注释铁律 3)。
	out := queryOutcome{decision: decision, settleTok: token, settleDec: decision}
	if cacheKey != "" && decision.Resolved && cache.Cacheable(decision.Result) {
		out.cacheSet = &cacheWrite{
			key:   cacheKey,
			entry: cache.EntryOf(decision.Result, requestID, now),
			ttl:   o.cachePolicy.TTL(now),
		}
	}
	return out
}

// cacheLookup 查缓存。**任何失败一律当作未命中**并记 warn：缓存是旁路，Redis 抖动
// 不得传导成下游请求失败——最坏结果只是多打一次上游。
func (o *QueryOrchestrator) cacheLookup(ctx context.Context, key string, log *slog.Logger) *cache.Entry {
	hit, err := o.cache.Get(ctx, key)
	if err != nil {
		log.Warn("查结果缓存失败，按未命中回源", "err", err)
		return nil
	}
	return hit
}

// respondX1 maps a queryOutcome to the x1 head/body response (DESIGN §6.2/§7.4):
// 查得→body.code 001, 查无→body.code 999, 其余→head.errorCode.
//
// 报文形态只看**上游归一码**，不看计费口径：是否累计成功查得数由 decision.Returned
// 决定，两者在 blk 这类「上游对查无也收费」的路由上必然分叉 (billing.TableFor)。
// 早期版本用 Returned 兼作分支条件，会把这类路由的查无错报成查得。
func (o *QueryOrchestrator) respondX1(out queryOutcome, requestID string, rec *model.AuditRecord, latencyMs int64) *model.QueryResponse {
	switch {
	case out.cached != nil:
		return o.replayCached(out.cached, rec.Reqid, requestID, rec, latencyMs)
	case out.existing != nil:
		return o.replay(out.existing, requestID, rec, latencyMs)
	case out.appErr != nil:
		rec.BusiCode = int(out.appErr.Busi)
		rec.BusiMsg = out.appErr.Msg
		return mapping.Error(out.appErr.Busi, out.appErr.Msg, requestID, latencyMs)
	}
	d := out.decision
	switch {
	case d.Resolved && model.IsFoundCode(codeOf(d.Result)):
		rec.BusiCode = int(errs.BusiSuccess)
		rec.BusiMsg = "success"
		return mapping.Found(d.Result, requestID, latencyMs)
	case d.Resolved:
		rec.BusiCode = int(errs.BusiNotFound)
		rec.BusiMsg = "查无结果"
		return mapping.NotFound(d.Result, requestID, latencyMs)
	default:
		rec.BusiCode = int(errs.BusiDataRequestErr)
		rec.ErrMsg = "上游未扣费/我方原因"
		return mapping.Error(errs.BusiDataRequestErr, "", requestID, latencyMs)
	}
}

// replayCached 回放一条自然月缓存命中，走与回源完全相同的映射 (mapping.Found/
// NotFound)，下游拿到的报文结构与首查一致。
//
// 字段口径：uid/logId 用缓存里的**原值**（对账时能追回上游那笔订单），reqid 用**本次
// 请求新生成的流水号**——下游看到的流水号每次唯一，不会被下游的去重逻辑误判成重复
// 报文。head.logId 同样是本次 requestId。
func (o *QueryOrchestrator) replayCached(e *cache.Entry, reqid, requestID string, rec *model.AuditRecord, latencyMs int64) *model.QueryResponse {
	r := e.Result(reqid)
	if e.Found() {
		rec.BusiCode = int(errs.BusiSuccess)
		rec.BusiMsg = "success"
		return mapping.Found(r, requestID, latencyMs)
	}
	rec.BusiCode = int(errs.BusiNotFound)
	rec.BusiMsg = "查无结果"
	return mapping.NotFound(r, requestID, latencyMs)
}

// codeOf 取上游归一码，result 为空时回空串（走查无分支）。
func codeOf(r *model.UpstreamResult) string {
	if r == nil {
		return ""
	}
	return r.Code
}

// replay reconstructs a response from an already-BILLED ledger. The full result
// body is not cached yet, so a查得数据 replay echoes body.code 001 with an empty
// range (TODO: cache the full result keyed by reqid for byte-identical replays).
//
// 分支依据是台账里的**上游归一码**而非 counted_service：blk 这类路由的查无也计费，
// 用计费标志分支会把重放报文从 999 错升成 001（老台账没有 upstream_code 时才退回
// counted_service）。
func (o *QueryOrchestrator) replay(l *model.Ledger, requestID string, rec *model.AuditRecord, latencyMs int64) *model.QueryResponse {
	// 幂等重放也回填台账里的上游标识，保证「上游uid/上游logId」列不因命中缓存而空。
	rec.CalledUpstream = true
	rec.UpstreamUID = l.UpstreamUID
	rec.UpstreamLogID = l.UpstreamLogID
	found := l.CountedService
	if l.UpstreamCode != "" {
		found = model.IsFoundCode(l.UpstreamCode)
	}
	if found {
		rec.BusiCode = int(errs.BusiSuccess)
		rec.BusiMsg = "success"
		// 不传 UID：上游订单号只进上面的审计字段，body.uid 由 mapping 填我方流水号。
		return mapping.Found(&model.UpstreamResult{Code: "001", Reqid: l.Reqid}, requestID, latencyMs)
	}
	rec.BusiCode = int(errs.BusiNotFound)
	rec.BusiMsg = "查无结果"
	return mapping.NotFound(&model.UpstreamResult{Code: "999", Reqid: l.Reqid}, requestID, latencyMs)
}

// QuotaQuery serves the客户配额查询 route (DESIGN §5.2).
func (o *QueryOrchestrator) QuotaQuery(ctx context.Context, signed *model.SignedRequest) (*model.ServiceQuotaView, *model.LicenseView, error) {
	lic, err := o.auth.Authenticate(ctx, signed)
	if err != nil {
		return nil, nil, err
	}
	view, err := o.quota.ServiceQuotaView(ctx, lic, o.route)
	if err != nil {
		return nil, lic, err
	}
	return view, lic, nil
}
