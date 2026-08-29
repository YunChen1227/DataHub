package application

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/datahub/relay/internal/domain/cache"
	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/domain/port"
	"github.com/datahub/relay/internal/domain/quota"
)

// bookTask 是一次请求响应后的记账工作单：台账结算（可选）+ 审计落库 + 写结果缓存
// （可选）。这些对构造下游响应毫无贡献，从关键路径剥离（DESIGN 异步记账）。
type bookTask struct {
	token    *quota.ReserveToken    // 与 decision 成对；nil = 无需结算（鉴权/参数失败、幂等重放、PENDING）
	decision *model.BillingDecision // 上游确定结论；PENDING 场景为 nil（台账留待复查/对账结算）
	rec      *model.AuditRecord     // 恒非 nil：每次请求都写审计
	// cacheHit 是自然月缓存命中的结算单：命中路径没开 PENDING 台账，由记账器一次
	// INSERT 写成终态 (quota.SettleCached)。与 token/decision 互斥。
	cacheHit *cachedSettlement
	// cacheSet 是回源后待写入缓存的条目。**尽力而为**：队列满降级同步时会被丢弃，
	// 详见 Submit。
	cacheSet *cacheWrite
}

// cachedSettlement 是一次自然月缓存命中的结算上下文（记账器补齐终态台账与计数）。
type cachedSettlement struct {
	lic       *model.LicenseView
	route     string
	reqid     string
	requestID string
	entry     *cache.Entry
}

// cacheWrite 是一条待写入的缓存条目：key 与 TTL 已由 cache.Policy 在关键路径上算好
// （纯 CPU，微秒级），记账器只负责那次 Redis 往返。
type cacheWrite struct {
	key   string
	entry *cache.Entry
	ttl   time.Duration
}

// Bookkeeper 把结算 + 审计移出请求关键路径：Handle 构造完响应即入队返回，
// 常驻 worker 用独立 context（请求 ctx 响应后即取消，不能复用）落库。
//
// 可靠性口径：
//   - 背压：队列满或已关闭时降级为「同步执行」——宁可让该请求慢几毫秒，绝不
//     静默丢弃计费台账/审计记录。
//   - 停机：Close() 停止接收并 drain 全部余量后返回（主流程在 HTTP Shutdown
//     后调用，复用现有 10s 优雅停机窗口）。
//   - 进程崩溃窗口：队列中未落库的审计/计数丢失，但 PENDING 台账已在响应前
//     同步写入（崩溃安全锚点），由复查/对账兜底终态化——与 DESIGN §7.3 一致。
//   - 与 RequeryWorker 的边界：本 worker 是同步路径结算的唯一执行者；
//     RequeryWorker 只处理「复查可达」的 PENDING 台账（当前各上游 Requery 均为
//     stub 不可达）。若未来实现真实 Requery，需为其加台账年龄下限，避免与
//     在队列中的毫秒级新 PENDING 抢结算。
type Bookkeeper struct {
	quota *quota.Service
	audit port.AuditRepository
	cache port.ResultCache // 自然月结果缓存；nil 时跳过写缓存
	log   *slog.Logger

	mu     sync.RWMutex // 保护 closed 与 tasks 的发送/关闭竞态
	closed bool
	tasks  chan bookTask
	wg     sync.WaitGroup
}

// NewBookkeeper 启动 workers 个常驻记账协程（queueSize/workers ≤0 时取缺省
// 1024/2）。quota 或 audit 可为 nil（对应操作跳过）。
func NewBookkeeper(q *quota.Service, audit port.AuditRepository, queueSize, workers int, log *slog.Logger) *Bookkeeper {
	if queueSize <= 0 {
		queueSize = 1024
	}
	if workers <= 0 {
		workers = 2
	}
	if log == nil {
		log = slog.Default()
	}
	b := &Bookkeeper{quota: q, audit: audit, log: log, tasks: make(chan bookTask, queueSize)}
	for i := 0; i < workers; i++ {
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			for t := range b.tasks {
				b.process(t)
			}
		}()
	}
	return b
}

// WithResultCache 挂接结果缓存写入端。未挂接时 cacheSet 任务被忽略。
func (b *Bookkeeper) WithResultCache(c port.ResultCache) *Bookkeeper {
	b.cache = c
	return b
}

// Submit 入队一个记账任务；队列满或已关闭时同步执行（背压降级，不丢任务）。
func (b *Bookkeeper) Submit(t bookTask) {
	b.mu.RLock()
	if !b.closed {
		select {
		case b.tasks <- t:
			b.mu.RUnlock()
			return
		default:
			// 队列满：降级同步。
		}
	}
	b.mu.RUnlock()
	// 降级同步路径会在**响应写回之前**执行，这里的每一次 IO 都直接加到下游看到的
	// 耗时里。计费台账/审计不能丢，只能认这几毫秒；写缓存则直接丢弃——丢一条缓存
	// 只是损失一次上游调用，绝不允许它在背压时反过来拖慢响应。
	t.cacheSet = nil
	b.process(t)
}

// Close 停止接收新任务并 drain 队列（阻塞至全部落库）。幂等。
func (b *Bookkeeper) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	close(b.tasks)
	b.mu.Unlock()
	b.wg.Wait()
}

// process 执行一个任务。必须用独立 context——请求 ctx 在响应写回后即被取消，
// 复用它会导致所有异步落库统一报 context canceled（异步化的经典坑）。
func (b *Bookkeeper) process(t bookTask) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if b.quota != nil && t.token != nil && t.decision != nil {
		if err := b.quota.Settle(ctx, t.token, t.decision); err != nil {
			b.log.Error("async settle failed", "reqid", t.token.Reqid, "err", err)
		}
	}
	if b.quota != nil && t.cacheHit != nil {
		h := t.cacheHit
		if err := b.quota.SettleCached(ctx, h.lic, h.route, h.reqid, h.requestID, h.entry); err != nil {
			b.log.Error("async cached settle failed", "reqid", h.reqid, "err", err)
		}
	}
	if b.cache != nil && t.cacheSet != nil {
		// warn 而非 error：写缓存是尽力而为，失败只意味着下次同一查询仍会回源。
		if err := b.cache.Set(ctx, t.cacheSet.key, t.cacheSet.entry, t.cacheSet.ttl); err != nil {
			b.log.Warn("async result cache set failed", "err", err)
		}
	}
	if b.audit != nil && t.rec != nil {
		if err := b.audit.AppendAudit(ctx, t.rec); err != nil {
			b.log.Error("async audit append failed", "requestId", t.rec.RequestID, "err", err)
		}
	}
}
