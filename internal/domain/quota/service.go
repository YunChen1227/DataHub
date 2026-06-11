// Package quota implements 双维度配额 (总量买断) with the
// 预留 → 以上游确定结论结算 model (DESIGN §7).
package quota

import (
	"context"

	"github.com/datahub/relay/internal/common/errs"
	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/domain/port"
)

// ReserveToken is the handle returned by ReserveUpstream and consumed by Settle.
type ReserveToken struct {
	LicenseID string
	LedgerID  int64
	Reqid     string
}

// Service coordinates quota repository + ledger.
type Service struct {
	quota  port.QuotaRepository
	ledger port.LedgerRepository
}

func New(quota port.QuotaRepository, ledger port.LedgerRepository) *Service {
	return &Service{quota: quota, ledger: ledger}
}

// CheckServiceQuota enforces 维度① (DESIGN §7.7): no balance → busiCode 1001
// 账户余额不足 and the upstream is NOT called.
func (s *Service) CheckServiceQuota(ctx context.Context, licenseID string) error {
	total, used, err := s.quota.ServiceQuota(ctx, licenseID)
	if err != nil {
		return errs.Wrap(errs.BusiDataRequestErr, "配额查询失败", err)
	}
	if total-used <= 0 {
		return errs.New(errs.BusiNoBalance, "")
	}
	return nil
}

// ServiceQuotaView powers the /quota route (DESIGN §5.2). 维度② is never exposed.
func (s *Service) ServiceQuotaView(ctx context.Context, lic *model.LicenseView) (*model.ServiceQuotaView, error) {
	total, used, err := s.quota.ServiceQuota(ctx, lic.LicenseID)
	if err != nil {
		return nil, errs.Wrap(errs.BusiDataRequestErr, "配额查询失败", err)
	}
	rem := total - used
	if rem < 0 {
		rem = 0
	}
	return &model.ServiceQuotaView{Status: lic.Status, Total: total, Used: used, Remaining: rem}, nil
}

// ReserveUpstream is the §7.3 step 1: idempotency check + atomic 维度② reserve.
//   - When a BILLED ledger already exists for reqid, it returns (nil, existing,
//     nil) so the caller can replay the cached result without re-charging.
//   - Otherwise it reserves one upstream unit and writes a PENDING ledger.
func (s *Service) ReserveUpstream(ctx context.Context, lic *model.LicenseView, reqid, tradeNo, requestID string) (*ReserveToken, *model.Ledger, error) {
	if existing, err := s.ledger.FindByReqid(ctx, lic.AppID, reqid); err == nil && existing != nil {
		if existing.State == model.StateBilled {
			return nil, existing, nil
		}
		// PENDING/UNBILLED: fall through to (re)reserve; the re-query/recon path
		// guarantees the upstream is not double-charged thanks to reqid idempotency.
	}

	ok, err := s.quota.TryReserveUpstream(ctx, lic.LicenseID)
	if err != nil {
		return nil, nil, errs.Wrap(errs.BusiDataRequestErr, "配额预留失败", err)
	}
	if !ok {
		// 维度②达成本上限 → busiCode 1006 透支余额已达上限 (DESIGN §7.7).
		return nil, nil, errs.New(errs.BusiOverdraftLimit, "")
	}

	l := &model.Ledger{
		AppID:     lic.AppID,
		TradeNo:   tradeNo,
		Reqid:     reqid,
		RequestID: requestID,
		State:     model.StatePending,
	}
	if err := s.ledger.Append(ctx, l); err != nil {
		// best-effort release so a failed ledger write does not leak reservation.
		_ = s.quota.ReleaseUpstream(ctx, lic.LicenseID)
		return nil, nil, errs.Wrap(errs.BusiDataRequestErr, "台账写入失败", err)
	}
	return &ReserveToken{LicenseID: lic.LicenseID, LedgerID: l.ID, Reqid: reqid}, nil, nil
}

// Settle is the §7.3 step 2 terminal settlement based on the确定结论.
//   - Charged → commit 维度② + count 维度① + ledger BILLED.
//   - Not charged → release 维度② reservation + ledger UNBILLED.
func (s *Service) Settle(ctx context.Context, token *ReserveToken, d *model.BillingDecision) error {
	if token == nil || d == nil {
		return errs.New(errs.BusiDataRequestErr, "无效结算上下文")
	}
	if d.Charged {
		if err := s.quota.CommitUpstream(ctx, token.LicenseID); err != nil {
			return errs.Wrap(errs.BusiDataRequestErr, "维度②提交失败", err)
		}
		if d.Returned {
			if err := s.quota.IncServiceUsed(ctx, token.LicenseID); err != nil {
				return errs.Wrap(errs.BusiDataRequestErr, "维度①计数失败", err)
			}
		}
		return s.ledger.UpdateState(ctx, token.LedgerID, model.StateBilled, d.Returned, true)
	}

	if err := s.quota.ReleaseUpstream(ctx, token.LicenseID); err != nil {
		return errs.Wrap(errs.BusiDataRequestErr, "维度②释放失败", err)
	}
	return s.ledger.UpdateState(ctx, token.LedgerID, model.StateUnbilled, false, false)
}
