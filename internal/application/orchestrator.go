// Package application wires the domain services into the主调用流程 (DESIGN §4).
// It owns transaction/flow orchestration only — no business rules live here.
package application

import (
	"context"
	"log/slog"
	"time"

	"github.com/datahub/relay/internal/common/appctx"
	"github.com/datahub/relay/internal/common/errs"
	"github.com/datahub/relay/internal/common/ipfilter"
	"github.com/datahub/relay/internal/common/mask"
	"github.com/datahub/relay/internal/domain/auth"
	"github.com/datahub/relay/internal/domain/billing"
	"github.com/datahub/relay/internal/domain/mapping"
	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/domain/parse"
	"github.com/datahub/relay/internal/domain/port"
	"github.com/datahub/relay/internal/domain/quota"
)

// QueryOrchestrator implements the §4 sequence.
type QueryOrchestrator struct {
	auth     *auth.Service
	quota    *quota.Service
	billing  *billing.Service
	upstream port.UpstreamPort
	audit    port.AuditRepository
	log      *slog.Logger
}

func NewQueryOrchestrator(a *auth.Service, q *quota.Service, b *billing.Service, up port.UpstreamPort, audit port.AuditRepository, log *slog.Logger) *QueryOrchestrator {
	if log == nil {
		log = slog.Default()
	}
	return &QueryOrchestrator{auth: a, quota: q, billing: b, upstream: up, audit: audit, log: log}
}

// Handle runs the full request lifecycle and returns a ready-to-serialize
// DoCheckResponse. All known outcomes (auth/quota/param/business) are encoded as
// code=0 + data.busiCode (PDF §5.3). A rich audit record (DESIGN §16.3) is
// written for every request via a deferred hook.
func (o *QueryOrchestrator) Handle(ctx context.Context, signed *model.SignedRequest, cmd *model.QueryCommand) *model.DoCheckResponse {
	requestID := appctx.RequestID(ctx)
	clientIP := appctx.ClientIP(ctx)
	start := time.Now()
	log := o.log.With("requestId", requestID, "tradeNo", cmd.TradeNo, "clientIp", clientIP)

	rec := &model.AuditRecord{
		RequestID:  requestID,
		AppID:      signed.AppID,
		TradeNo:    cmd.TradeNo,
		ClientIP:   clientIP,
		NameMask:   mask.Name(cmd.Name),
		IDCardMask: mask.IDCard(cmd.IDCard),
		MobileMask: mask.Mobile(cmd.Mobile),
	}
	var resp *model.DoCheckResponse
	defer func() {
		if resp != nil {
			if resp.Data != nil {
				rec.BusiCode = resp.Data.BusiCode
				rec.BusiMsg = resp.Data.BusiMsg
			} else {
				rec.BusiMsg = resp.Msg
			}
		}
		rec.FoundData = rec.BusiCode == int(errs.BusiSuccess)
		rec.LatencyMs = time.Since(start).Milliseconds()
		rec.CreatedAt = time.Now()
		if o.audit != nil {
			if err := o.audit.AppendAudit(ctx, rec); err != nil {
				log.Error("append audit failed", "err", err)
			}
		}
	}()

	// 1. License + appId/apiKey + signature.
	lic, err := o.auth.Authenticate(ctx, signed)
	if err != nil {
		ae := errs.AsAppError(err)
		rec.ErrMsg = ae.Error()
		log.Warn("auth failed", "busiCode", ae.Busi, "err", err)
		resp = mapping.Busi(ae.Busi, ae.Msg, requestID)
		return resp
	}
	log = log.With("appId", lic.AppID)

	// 1b. Per-user IP whitelist (DESIGN §16.4): reject when source IP not allowed.
	if !ipfilter.Allowed(clientIP, lic.IPWhitelist) {
		rec.ErrMsg = "IP 不在白名单"
		log.Warn("per-user ip rejected", "clientIp", clientIP)
		resp = mapping.Busi(errs.BusiAccountAbnormal, "IP 不在白名单", requestID)
		return resp
	}

	// 2. 维度① balance pre-check (no balance → 1001, no upstream call).
	if err := o.quota.CheckServiceQuota(ctx, lic.LicenseID); err != nil {
		ae := errs.AsAppError(err)
		rec.ErrMsg = ae.Error()
		log.Info("service quota rejected", "busiCode", ae.Busi)
		resp = mapping.Busi(ae.Busi, ae.Msg, requestID)
		return resp
	}

	// 3. Param validation + build upstream request (我方拦截, before reserve).
	upReq, err := parse.Parse(cmd)
	if err != nil {
		ae := errs.AsAppError(err)
		rec.ErrMsg = ae.Error()
		log.Info("param invalid", "err", err)
		resp = mapping.Busi(ae.Busi, ae.Msg, requestID)
		return resp
	}
	rec.Reqid = upReq.Reqid
	log = log.With("reqid", upReq.Reqid)

	// 4. Idempotency + 维度② reservation.
	token, existing, err := o.quota.ReserveUpstream(ctx, lic, upReq.Reqid, cmd.TradeNo, requestID)
	if err != nil {
		ae := errs.AsAppError(err)
		rec.ErrMsg = ae.Error()
		log.Info("reserve rejected", "busiCode", ae.Busi)
		resp = mapping.Busi(ae.Busi, ae.Msg, requestID)
		return resp
	}
	if existing != nil {
		log.Info("idempotent hit, replaying cached billed result")
		rec.CalledUpstream = true
		rec.Billed = existing.CountedService
		resp = o.replay(existing, requestID)
		return resp
	}

	// 5. Call upstream; on timeout/no-response → idempotent re-query by reqid.
	result, callErr := o.upstream.Query(ctx, upReq)
	if callErr != nil {
		log.Warn("upstream call failed, re-querying by reqid", "err", callErr)
		rr, rqErr := o.upstream.Requery(ctx, upReq.Reqid)
		if rqErr != nil || rr == nil || !rr.Reachable {
			rec.ErrMsg = "上游超时/复查未决，PENDING 待对账"
			log.Warn("re-query unresolved, leaving PENDING for reconciliation", "err", rqErr)
			resp = mapping.Busi(errs.BusiDataRequestErr, "", requestID)
			return resp
		}
		decision := o.billing.FromRequery(rr)
		resp = o.settleAndRespond(ctx, token, decision, requestID, rec, log)
		return resp
	}

	// 6. Decide + settle on the确定结论.
	decision := o.billing.Decide(result)
	resp = o.settleAndRespond(ctx, token, decision, requestID, rec, log)
	return resp
}

// settleAndRespond applies the billing verdict and maps the client response
// (DESIGN §6.2/§7.4): 001→10(查得数据), 999→1000(数据未查得), 其余→1007.
func (o *QueryOrchestrator) settleAndRespond(ctx context.Context, token *quota.ReserveToken, d *model.BillingDecision, requestID string, rec *model.AuditRecord, log *slog.Logger) *model.DoCheckResponse {
	if err := o.quota.Settle(ctx, token, d); err != nil {
		log.Error("settle failed", "err", err)
	}
	if d.Result != nil {
		rec.CalledUpstream = true
		rec.UpstreamCode = d.Result.Code
		rec.UpstreamUID = d.Result.UID
		rec.UpstreamLogID = d.Result.LogID
	}
	rec.Billed = d.Returned
	switch {
	case d.Charged && d.Returned && d.Result != nil:
		log.Info("billed (查得数据)", "range", d.Result.Range, "upstreamCode", d.Result.Code)
		return mapping.Success(d.Result.Range, requestID)
	case d.Charged && !d.Returned:
		log.Info("billed (查无结果, 计维度②不计维度①)")
		return mapping.Busi(errs.BusiNotFound, "", requestID)
	default:
		log.Info("unbilled (our-side / not charged)")
		return mapping.Busi(errs.BusiDataRequestErr, "", requestID)
	}
}

// replay reconstructs a response from an already-BILLED ledger. The full result
// body is not cached yet, so a查得数据 replay echoes busiCode 10 with an empty
// score (TODO: cache the full result keyed by reqid for byte-identical replays).
func (o *QueryOrchestrator) replay(l *model.Ledger, requestID string) *model.DoCheckResponse {
	if l.CountedService {
		return mapping.Success("", requestID)
	}
	return mapping.Busi(errs.BusiNotFound, "", requestID)
}

// QuotaQuery serves the客户配额查询 route (DESIGN §5.2).
func (o *QueryOrchestrator) QuotaQuery(ctx context.Context, signed *model.SignedRequest) (*model.ServiceQuotaView, *model.LicenseView, error) {
	lic, err := o.auth.Authenticate(ctx, signed)
	if err != nil {
		return nil, nil, err
	}
	if !ipfilter.Allowed(appctx.ClientIP(ctx), lic.IPWhitelist) {
		return nil, lic, errs.New(errs.BusiAccountAbnormal, "IP 不在白名单")
	}
	view, err := o.quota.ServiceQuotaView(ctx, lic)
	if err != nil {
		return nil, lic, err
	}
	return view, lic, nil
}
