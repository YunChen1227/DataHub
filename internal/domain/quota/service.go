// Package quota tracks the 成功查得数 statistic and drives the 台账 state machine
// (PENDING → BILLED/UNBILLED, DESIGN §7). v0.6 起取消所有额度限制与维度②上游计数：
// 不做任何次数上限拦截，仅在查得数据 (busiCode 10) 时累计 serviceUsed。
package quota

import (
	"context"

	"github.com/datahub/relay/internal/common/errs"
	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/domain/port"
)

// ReserveToken is the handle returned by Begin and consumed by Settle.
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

// ServiceQuotaView powers the /quota route (DESIGN §5.2). 无额度限制，仅返回
// 累计成功查得数 (used = 查得数据次数)。
func (s *Service) ServiceQuotaView(ctx context.Context, lic *model.LicenseView) (*model.ServiceQuotaView, error) {
	used, err := s.quota.ServiceUsed(ctx, lic.LicenseID)
	if err != nil {
		return nil, errs.Wrap(errs.BusiDataRequestErr, "查询失败", err)
	}
	return &model.ServiceQuotaView{Status: lic.Status, Used: used}, nil
}

// Begin is the §7.3 step 1: idempotency check + open a PENDING ledger.
//   - When a BILLED ledger already exists for reqid, it returns (nil, existing,
//     nil) so the caller can replay the cached result.
//   - Otherwise it writes a PENDING ledger and returns a settlement token.
//
// 无额度限制：不再做任何上游预留，仅驱动台账 PENDING→BILLED/UNBILLED 状态机与幂等。
func (s *Service) Begin(ctx context.Context, lic *model.LicenseView, reqid, tradeNo, requestID string) (*ReserveToken, *model.Ledger, error) {
	if existing, err := s.ledger.FindByReqid(ctx, lic.AppKey, reqid); err == nil && existing != nil {
		if existing.State == model.StateBilled {
			return nil, existing, nil
		}
		// PENDING/UNBILLED: fall through to (re)open; reqid idempotency at the
		// upstream guarantees no double-query on the re-query/recon path.
	}

	l := &model.Ledger{
		AppKey:    lic.AppKey,
		TradeNo:   tradeNo,
		Reqid:     reqid,
		RequestID: requestID,
		State:     model.StatePending,
	}
	if err := s.ledger.Append(ctx, l); err != nil {
		return nil, nil, errs.Wrap(errs.BusiDataRequestErr, "台账写入失败", err)
	}
	return &ReserveToken{LicenseID: lic.LicenseID, LedgerID: l.ID, Reqid: reqid}, nil, nil
}

// Settle is the §7.3 step 2 terminal settlement based on the确定结论.
//   - Resolved → ledger BILLED; 查得数据(Returned) 时累计成功查得数。
//   - Unresolved → ledger UNBILLED。
func (s *Service) Settle(ctx context.Context, token *ReserveToken, d *model.BillingDecision) error {
	if token == nil || d == nil {
		return errs.New(errs.BusiDataRequestErr, "无效结算上下文")
	}
	if d.Resolved {
		if d.Returned {
			if err := s.quota.IncServiceUsed(ctx, token.LicenseID); err != nil {
				return errs.Wrap(errs.BusiDataRequestErr, "成功查得数累计失败", err)
			}
		}
		return s.ledger.UpdateState(ctx, token.LedgerID, model.StateBilled, d.Returned)
	}
	return s.ledger.UpdateState(ctx, token.LedgerID, model.StateUnbilled, false)
}
