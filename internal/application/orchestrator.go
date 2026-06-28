// Package application wires the domain services into the主调用流程 (DESIGN §4).
// It owns transaction/flow orchestration only — no business rules live here.
package application

import (
	"context"
	"log/slog"
	"time"

	"github.com/datahub/relay/internal/common/appctx"
	"github.com/datahub/relay/internal/common/errs"
	"github.com/datahub/relay/internal/common/mask"
	"github.com/datahub/relay/internal/domain/auth"
	"github.com/datahub/relay/internal/domain/billing"
	"github.com/datahub/relay/internal/domain/mapping"
	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/domain/parse"
	"github.com/datahub/relay/internal/domain/port"
	"github.com/datahub/relay/internal/domain/quota"
)

// QueryOrchestrator implements the §4 sequence. route 标记本编排器服务的路由
// (x1/v9/v8/zlf/blk)，用于把统计/台账/审计按路由作用域隔离 (共享 license 的 v8/v9)。
type QueryOrchestrator struct {
	route    string
	auth     *auth.Service
	quota    *quota.Service
	billing  *billing.Service
	upstream port.UpstreamPort
	audit    port.AuditRepository
	log      *slog.Logger
}

func NewQueryOrchestrator(route string, a *auth.Service, q *quota.Service, b *billing.Service, up port.UpstreamPort, audit port.AuditRepository, log *slog.Logger) *QueryOrchestrator {
	if log == nil {
		log = slog.Default()
	}
	return &QueryOrchestrator{route: route, auth: a, quota: q, billing: b, upstream: up, audit: audit, log: log}
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
	defer func() {
		rec.FoundData = rec.BusiCode == int(errs.BusiSuccess)
		rec.LatencyMs = lat()
		rec.CreatedAt = time.Now()
		if o.audit != nil {
			if err := o.audit.AppendAudit(ctx, rec); err != nil {
				log.Error("append audit failed", "err", err)
			}
		}
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
	upReq, err := parse.Parse(cmd)
	if err != nil {
		ae := errs.AsAppError(err)
		rec.ErrMsg = ae.Error()
		log.Info("param invalid", "err", err)
		return fail(ae.Busi, ae.Msg)
	}
	rec.Reqid = upReq.Reqid
	log = log.With("reqid", upReq.Reqid)

	// 4-6. Idempotency + reserve + upstream + settle (shared core).
	out := o.runCore(ctx, lic, upReq, requestID, rec, log)
	return o.respondX1(out, requestID, rec, lat())
}

// queryOutcome is the normalized result of the post-auth core flow, shared by
// the x1 (head/body) and v9 (income_cls.md) response mappers.
type queryOutcome struct {
	decision *model.BillingDecision // settled verdict (查得/查无/未扣费)
	existing *model.Ledger          // idempotent hit (already BILLED)
	appErr   *errs.AppError         // reserve/upstream-unresolved failure
}

// runCore runs the shared §4 steps after authentication: 幂等命中、开台账、上游
// 调用(+按 reqid 复查)、结算。It updates the audit record's flow fields and applies
// settlement; wire-format mapping is left to the caller.
func (o *QueryOrchestrator) runCore(ctx context.Context, lic *model.LicenseView, upReq *model.UpstreamRequest, requestID string, rec *model.AuditRecord, log *slog.Logger) queryOutcome {
	token, existing, err := o.quota.Begin(ctx, lic, o.route, upReq.Reqid, "", requestID)
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
		log.Warn("upstream call failed, re-querying by reqid", "err", callErr)
		rr, rqErr := o.upstream.Requery(ctx, upReq.Reqid)
		if rqErr != nil || rr == nil || !rr.Reachable {
			rec.ErrMsg = "上游超时/复查未决，PENDING 待对账"
			log.Warn("re-query unresolved, leaving PENDING for reconciliation", "err", rqErr)
			return queryOutcome{appErr: errs.New(errs.BusiDataRequestErr, "")}
		}
		decision = o.billing.FromRequery(rr)
	} else {
		decision = o.billing.Decide(result)
	}

	if err := o.quota.Settle(ctx, token, decision); err != nil {
		log.Error("settle failed", "err", err)
	}
	if decision.Result != nil {
		rec.CalledUpstream = true
		rec.UpstreamCode = decision.Result.Code
		rec.UpstreamUID = decision.Result.UID
		rec.UpstreamLogID = decision.Result.LogID
	}
	rec.Billed = decision.Returned
	return queryOutcome{decision: decision}
}

// respondX1 maps a queryOutcome to the x1 head/body response (DESIGN §6.2/§7.4):
// 查得→body.code 001(累计成功查得数), 查无→body.code 999, 其余→head.errorCode.
func (o *QueryOrchestrator) respondX1(out queryOutcome, requestID string, rec *model.AuditRecord, latencyMs int64) *model.QueryResponse {
	switch {
	case out.existing != nil:
		return o.replay(out.existing, requestID, rec, latencyMs)
	case out.appErr != nil:
		rec.BusiCode = int(out.appErr.Busi)
		rec.BusiMsg = out.appErr.Msg
		return mapping.Error(out.appErr.Busi, out.appErr.Msg, requestID, latencyMs)
	}
	d := out.decision
	switch {
	case d.Resolved && d.Returned && d.Result != nil:
		rec.BusiCode = int(errs.BusiSuccess)
		rec.BusiMsg = "success"
		return mapping.Found(d.Result, requestID, latencyMs)
	case d.Resolved && !d.Returned:
		rec.BusiCode = int(errs.BusiNotFound)
		rec.BusiMsg = "查无结果"
		return mapping.NotFound(d.Result, requestID, latencyMs)
	default:
		rec.BusiCode = int(errs.BusiDataRequestErr)
		rec.ErrMsg = "上游未扣费/我方原因"
		return mapping.Error(errs.BusiDataRequestErr, "", requestID, latencyMs)
	}
}

// replay reconstructs a response from an already-BILLED ledger. The full result
// body is not cached yet, so a查得数据 replay echoes body.code 001 with an empty
// range (TODO: cache the full result keyed by reqid for byte-identical replays).
func (o *QueryOrchestrator) replay(l *model.Ledger, requestID string, rec *model.AuditRecord, latencyMs int64) *model.QueryResponse {
	if l.CountedService {
		rec.BusiCode = int(errs.BusiSuccess)
		rec.BusiMsg = "success"
		return mapping.Found(&model.UpstreamResult{Code: "001", Reqid: l.Reqid, UID: l.UpstreamUID}, requestID, latencyMs)
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
