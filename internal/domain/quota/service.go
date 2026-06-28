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

// ReserveToken is the handle returned by Begin and consumed by Settle. Route
// 标记路由作用域，使共享 license 的 v8/v9 统计相互独立。
type ReserveToken struct {
	LicenseID string
	Route     string
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

// ServiceQuotaView powers the /quota route (DESIGN §5.2). 无额度限制，按路由
// 独立返回累计成功查得数 (used) 与累计调用上游次数 (calls)。
func (s *Service) ServiceQuotaView(ctx context.Context, lic *model.LicenseView, route string) (*model.ServiceQuotaView, error) {
	used, err := s.quota.ServiceUsed(ctx, lic.LicenseID, route)
	if err != nil {
		return nil, errs.Wrap(errs.BusiDataRequestErr, "查询失败", err)
	}
	calls, err := s.quota.TotalCalls(ctx, lic.LicenseID, route)
	if err != nil {
		return nil, errs.Wrap(errs.BusiDataRequestErr, "查询失败", err)
	}
	return &model.ServiceQuotaView{Status: lic.Status, Used: used, Calls: calls}, nil
}

// Begin is the §7.3 step 1: idempotency check + open a PENDING ledger.
//   - When a BILLED ledger already exists for reqid, it returns (nil, existing,
//     nil) so the caller can replay the cached result.
//   - Otherwise it writes a PENDING ledger and returns a settlement token.
//
// 无额度限制：不再做任何上游预留，仅驱动台账 PENDING→BILLED/UNBILLED 状态机与幂等。
// route 标记路由作用域 (共享 license 的 v8/v9 幂等/统计相互独立)。
func (s *Service) Begin(ctx context.Context, lic *model.LicenseView, route, reqid, tradeNo, requestID string) (*ReserveToken, *model.Ledger, error) {
	if existing, err := s.ledger.FindByReqid(ctx, lic.AppKey, route, reqid); err == nil && existing != nil {
		if existing.State == model.StateBilled {
			return nil, existing, nil
		}
		// PENDING/UNBILLED: fall through to (re)open; reqid idempotency at the
		// upstream guarantees no double-query on the re-query/recon path.
	}

	l := &model.Ledger{
		AppKey:    lic.AppKey,
		Version:   route,
		TradeNo:   tradeNo,
		Reqid:     reqid,
		RequestID: requestID,
		State:     model.StatePending,
	}
	if err := s.ledger.Append(ctx, l); err != nil {
		return nil, nil, errs.Wrap(errs.BusiDataRequestErr, "台账写入失败", err)
	}
	return &ReserveToken{LicenseID: lic.LicenseID, Route: route, LedgerID: l.ID, Reqid: reqid}, nil, nil
}

// Settle is the §7.3 step 2 terminal settlement based on the确定结论.
//   - d.Result != nil → 上游已应答 (查得/查无, = CalledUpstream) → 累计调用次数。
//   - Resolved → ledger BILLED; 查得数据(Returned) 时累计成功查得数。
//   - Unresolved → ledger UNBILLED。
//
// 计数按 token.Route 独立 (共享 license 的 v8/v9 互不影响)。每个台账仅结算一次
// (同步路径或复查 worker)，故计数不会重复。
func (s *Service) Settle(ctx context.Context, token *ReserveToken, d *model.BillingDecision) error {
	if token == nil || d == nil {
		return errs.New(errs.BusiDataRequestErr, "无效结算上下文")
	}
	if d.Result != nil {
		if err := s.quota.IncTotalCalls(ctx, token.LicenseID, token.Route); err != nil {
			return errs.Wrap(errs.BusiDataRequestErr, "调用次数累计失败", err)
		}
	}
	if d.Resolved {
		if d.Returned {
			if err := s.quota.IncServiceUsed(ctx, token.LicenseID, token.Route); err != nil {
				return errs.Wrap(errs.BusiDataRequestErr, "成功查得数累计失败", err)
			}
		}
		return s.ledger.UpdateState(ctx, token.LedgerID, model.StateBilled, d.Returned)
	}
	return s.ledger.UpdateState(ctx, token.LedgerID, model.StateUnbilled, false)
}
