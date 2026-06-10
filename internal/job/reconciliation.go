package job

import (
	"context"
	"log/slog"
	"time"

	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/domain/port"
)

// ReconciliationJob is the最终兜底 that makes 维度② 不漏计/不空计 (DESIGN §7.6).
// It periodically pulls the upstream对账单 (or single-record query) and forces
// every record to a terminal state aligned with the upstream扣费记录 — the
// single source of truth.
type ReconciliationJob struct {
	ledger   port.LedgerRepository
	upstream port.UpstreamPort
	interval time.Duration
	log      *slog.Logger
}

func NewReconciliationJob(ledger port.LedgerRepository, upstream port.UpstreamPort, interval time.Duration, log *slog.Logger) *ReconciliationJob {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	if log == nil {
		log = slog.Default()
	}
	return &ReconciliationJob{ledger: ledger, upstream: upstream, interval: interval, log: log}
}

func (j *ReconciliationJob) Run(ctx context.Context) {
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			j.log.Info("reconciliation job stopped")
			return
		case <-ticker.C:
			j.tick(ctx)
		}
	}
}

// tick implements the §7.6 reconciliation outline. The upstream对账文件/单笔查询
// contract is待联调确认 (§15.3); until then this only surfaces超期 PENDING backlog.
func (j *ReconciliationJob) tick(ctx context.Context) {
	pending, err := j.ledger.ListByState(ctx, model.StatePending, 1000)
	if err != nil {
		j.log.Error("recon list pending failed", "err", err)
		return
	}
	if len(pending) > 0 {
		j.log.Warn("reconciliation backlog", "pendingCount", len(pending))
	}
	// TODO(§7.6 / §15.3):
	//  1) pull upstream对账单 or query each by reqid.
	//  2) 上游已扣费&本地未计 → 强制 committed++ (补计) + 告警.
	//  3) 本地已计&上游无扣费 → 冲正 committed-- + 告警.
	//  4) 超期 PENDING → 依上游记录裁决为 BILLED/UNBILLED, 清 reserved.
}
