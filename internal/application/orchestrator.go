// Package application wires the domain services into the主调用流程 (DESIGN §4).
// It owns transaction/flow orchestration only — no business rules live here.
package application

import (
	"context"
	"log/slog"

	"github.com/datahub/relay/internal/common/appctx"
	"github.com/datahub/relay/internal/common/errs"
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
	log      *slog.Logger
}

func NewQueryOrchestrator(a *auth.Service, q *quota.Service, b *billing.Service, up port.UpstreamPort, log *slog.Logger) *QueryOrchestrator {
	if log == nil {
		log = slog.Default()
	}
	return &QueryOrchestrator{auth: a, quota: q, billing: b, upstream: up, log: log}
}

// Handle runs the full request lifecycle and returns a ready-to-serialize
// QueryResult. It returns an error only for auth/quota/param pre-checks (mapped
// to a head error by the API layer); business outcomes are encoded in the result.
func (o *QueryOrchestrator) Handle(ctx context.Context, signed *model.SignedRequest, cmd *model.QueryCommand) (*model.QueryResult, error) {
	requestID := appctx.RequestID(ctx)
	log := o.log.With("requestId", requestID, "reqid", cmd.Reqid)

	// 1. License + signature.
	lic, err := o.auth.Authenticate(ctx, signed)
	if err != nil {
		log.Warn("auth failed", "err", err)
		return nil, err
	}
	log = log.With("appKey", lic.AppKey)

	// 2. 维度① balance pre-check (no balance → 429001, no upstream call).
	if err := o.quota.CheckServiceQuota(ctx, lic.LicenseID); err != nil {
		log.Info("service quota check rejected", "err", err)
		return nil, err
	}

	// 3. Param validation + build upstream request (our-side拦截, before reserve).
	upReq, err := parse.Parse(cmd)
	if err != nil {
		log.Info("param invalid", "err", err)
		return nil, err
	}

	// 4. Idempotency + 维度② reservation.
	token, existing, err := o.quota.ReserveUpstream(ctx, lic, cmd.Reqid, requestID)
	if err != nil {
		log.Info("reserve rejected", "err", err)
		return nil, err
	}
	if existing != nil {
		log.Info("idempotent hit, replaying cached billed result")
		return o.replay(existing, requestID), nil
	}

	// 5. Call upstream; on timeout/no-response → idempotent re-query by reqid.
	result, callErr := o.upstream.Query(ctx, upReq)
	if callErr != nil {
		log.Warn("upstream call failed, re-querying by reqid", "err", callErr)
		rr, rqErr := o.upstream.Requery(ctx, cmd.Reqid)
		if rqErr != nil || rr == nil || !rr.Reachable {
			// 无确定结论：保持 PENDING，交对账兜底，不在此结算 (DESIGN §7.6).
			log.Warn("re-query unresolved, leaving PENDING for reconciliation", "err", rqErr)
			return mapping.ErrorResponse(errs.UpstreamNotExecuted, "", requestID), nil
		}
		decision := o.billing.FromRequery(rr)
		return o.settleAndRespond(ctx, token, decision, requestID, log), nil
	}

	// 6. Decide + settle on the确定结论.
	decision := o.billing.Decide(result)
	return o.settleAndRespond(ctx, token, decision, requestID, log), nil
}

// settleAndRespond applies the billing verdict and maps the client response.
func (o *QueryOrchestrator) settleAndRespond(ctx context.Context, token *quota.ReserveToken, d *model.BillingDecision, requestID string, log *slog.Logger) *model.QueryResult {
	if err := o.quota.Settle(ctx, token, d); err != nil {
		// Settlement failure must not silently drop a charged upstream call;
		// reconciliation (DESIGN §7.6) will reconcile from upstream records.
		log.Error("settle failed", "err", err)
	}
	if d.Charged && d.Result != nil {
		log.Info("billed", "range", d.Result.Range, "upstreamCode", d.Result.Code)
		// TODO(§8.1): optionally compute response HMAC for body.verify.
		return mapping.Success(d.Result, requestID, "")
	}
	log.Info("unbilled (our-side / not charged)")
	return mapping.ErrorResponse(errs.UpstreamBusiness, "", requestID)
}

// replay reconstructs a response from an already-BILLED ledger. The full body is
// not cached yet, so only the upstream code/uid are echoed.
// TODO: cache the full result body keyed by reqid for byte-identical replays.
func (o *QueryOrchestrator) replay(l *model.Ledger, requestID string) *model.QueryResult {
	return mapping.Success(&model.UpstreamResult{
		Code:  l.UpstreamCode,
		Msg:   "成功(幂等命中)",
		UID:   l.UpstreamUID,
		Reqid: l.Reqid,
	}, requestID, "")
}

// QuotaQuery serves the客户配额查询 route (DESIGN §5.2).
func (o *QueryOrchestrator) QuotaQuery(ctx context.Context, signed *model.SignedRequest) (*model.ServiceQuotaView, *model.LicenseView, error) {
	lic, err := o.auth.Authenticate(ctx, signed)
	if err != nil {
		return nil, nil, err
	}
	view, err := o.quota.ServiceQuotaView(ctx, lic)
	if err != nil {
		return nil, lic, err
	}
	return view, lic, nil
}
