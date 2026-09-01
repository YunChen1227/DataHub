// Package quota tracks the 成功查得数 statistic and drives the 台账 state machine
// (PENDING → BILLED/UNBILLED, DESIGN §7). v0.6 起取消所有额度限制与维度②上游计数：
// 不做任何次数上限拦截，仅在查得数据 (busiCode 10) 时累计 serviceUsed。
package quota

import (
	"context"

	"github.com/datahub/relay/internal/common/errs"
	"github.com/datahub/relay/internal/domain/cache"
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
//
// reqidIsFresh：reqid 由本服务在本次请求内新生成（parse.NewReqid，当前所有路由
// 均如此）时传 true——新生成的 reqid 不可能命中历史台账，幂等查询必 miss，直接
// 跳过这次纯浪费的 DB 读（关键路径 -1 次 SELECT）。仅当未来出现「客户传入 reqid」
// 的路由时才传 false 走完整幂等检查 + 重放。
func (s *Service) Begin(ctx context.Context, lic *model.LicenseView, route, reqid, tradeNo, requestID string, reqidIsFresh bool) (*ReserveToken, *model.Ledger, error) {
	if !reqidIsFresh {
		if existing, err := s.ledger.FindByReqid(ctx, lic.AppKey, route, reqid); err == nil && existing != nil {
			if existing.State == model.StateBilled {
				return nil, existing, nil
			}
			// PENDING/UNBILLED: fall through to (re)open; reqid idempotency at the
			// upstream guarantees no double-query on the re-query/recon path.
		}
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
	st := model.LedgerSettlement{State: model.StateUnbilled}
	if d.Result != nil {
		st.UpstreamCode, st.UpstreamUID, st.UpstreamLogID = d.Result.Code, d.Result.UID, d.Result.LogID
	}
	if d.Resolved {
		if d.Returned {
			if err := s.quota.IncServiceUsed(ctx, token.LicenseID, token.Route); err != nil {
				return errs.Wrap(errs.BusiDataRequestErr, "成功查得数累计失败", err)
			}
		}
		st.State, st.CountedService = model.StateBilled, d.Returned
	}
	return s.ledger.Settle(ctx, token.LedgerID, st)
}

// SettleCached 结算一次「自然月结果缓存」命中（domain/cache）。与 Settle 的两处关键
// 差异：
//
//   - **不累计调用上游次数**：命中确实没调上游。于是 serviceUsed(收入侧) 与
//     totalCalls(成本侧) 的差额天然等于缓存替你省下的上游调用量，不必另加计数器。
//   - **一次 INSERT 写成终态台账**，不走「先 PENDING 后 UPDATE」：命中路径不存在
//     「上游是否已扣费未知」的窗口——结论在读到缓存那一刻就已确定，没有需要 PENDING
//     锚点保护的崩溃窗口。顺带省掉了关键路径上那次同步 INSERT (见 orchestrator.runCore)。
//
// 计费口径与回源完全一致：查得(001) 计成功查得数，查无(999) 不计。台账 FromCache=true
// 标记本行没有独立的上游订单号，对账时须排除（见 migrations/0007）。
func (s *Service) SettleCached(ctx context.Context, lic *model.LicenseView, route, reqid, requestID string, e *cache.Entry) error {
	if lic == nil || e == nil {
		return errs.New(errs.BusiDataRequestErr, "无效缓存结算上下文")
	}
	// 计数先于台账，与 Settle 的顺序一致。
	if e.Found() {
		if err := s.quota.IncServiceUsed(ctx, lic.LicenseID, route); err != nil {
			return errs.Wrap(errs.BusiDataRequestErr, "成功查得数累计失败", err)
		}
	}
	l := &model.Ledger{
		AppKey:         lic.AppKey,
		Version:        route,
		Reqid:          reqid,
		RequestID:      requestID,
		UpstreamCode:   e.Code,
		UpstreamUID:    e.UID,
		UpstreamLogID:  e.LogID,
		State:          model.StateBilled,
		CountedService: e.Found(),
		FromCache:      true,
	}
	if err := s.ledger.Append(ctx, l); err != nil {
		return errs.Wrap(errs.BusiDataRequestErr, "缓存命中台账写入失败", err)
	}
	return nil
}
