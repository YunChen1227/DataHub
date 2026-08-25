package upstream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/domain/port"
)

// Sourcer 是「多上游串行轮询、命中即停」寻源器 (add-upstream-multi skill 的目标模型)。
// 它把一条路由的 N 个**可互相替代**的上游 (同一种数据、不同供应商) 统一成一个
// port.UpstreamPort，对 orchestrator 完全透明。与并发聚合的 Aggregator 并存，由装配
// 层按路由选用 (混合 kind 或显式配置 priority/cost 的路由走 Sourcer)。
//
// 关键语义 (与 Aggregator 的并发扇出**不同**)：
//   - 按 (priority 升 → 成本升 → 配置顺序) 稳定排序后**串行**遍历；
//   - 第一个查得 (001) 的源即最终结果，后续源一律**不再调用** (命中即停，省钱)；
//   - 查无 (999) / 失败继续下一个源；
//   - 命中即停意味着不存在"部分数据"：只要有源查得就是完整结果 (001、计费)，哪怕
//     前面的源失败过；range 与单源路由**完全同形** (下游无从察觉背后有几个源)。
//
// 判定表 (add-upstream-multi skill)：
//   - 某源查得              → "001"（计费）
//   - 全部源查无 (无失败)   → "999"
//   - 无源查得 + ≥1 源失败  → "002"（未取得数据且部分数据源异常，不计费）
//   - 全部源失败            → error (*model.UpstreamError，对外 505062，走复查/对账)
type Sourcer struct {
	sources []Source
	budget  time.Duration
}

// Source 是寻源器里的一个逻辑源：一次可替代查询的上游客户端 + 其排序/成本属性。
type Source struct {
	Name     string // 轨迹与日志里的源名 (label)
	Priority int    // 越小越先；相等再比成本，再比配置顺序
	CostFen  int    // 该源一次调用的成本 (分)；用于排序与对账
	CostOn   string // "hit"(缺省，仅命中计费) | "call"(调用即计费)
	Port     port.UpstreamPort
}

// DefaultSourcerBudget 是串行寻源的总时延预算缺省值 (add-upstream-multi skill：串行
// 最坏情况是各源耗时相加，须与下游约定超时对齐；预算耗尽不再尝试下一个源)。
const DefaultSourcerBudget = 9 * time.Second

// NewSourcer 构造寻源器并按 (priority 升 → 成本升 → 配置顺序) 稳定排序。未显式给
// priority 时全为 0，自然退化为「成本由低到高、同成本按配置顺序」。budget<=0 时取缺省。
func NewSourcer(sources []Source, budget time.Duration) (*Sourcer, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("upstream sourcer: 至少需要一个子源")
	}
	ordered := make([]Source, len(sources))
	copy(ordered, sources)
	for i := range ordered {
		if ordered[i].Port == nil {
			return nil, fmt.Errorf("upstream sourcer: 子源 %d (%q) 未初始化", i, ordered[i].Name)
		}
		if ordered[i].Name == "" {
			ordered[i].Name = fmt.Sprintf("source%d", i+1)
		}
	}
	// sort.SliceStable 保证 (priority, cost) 相等时维持原始配置顺序。
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority != ordered[j].Priority {
			return ordered[i].Priority < ordered[j].Priority
		}
		return ordered[i].CostFen < ordered[j].CostFen
	})
	if budget <= 0 {
		budget = DefaultSourcerBudget
	}
	return &Sourcer{sources: ordered, budget: budget}, nil
}

// Active 返回源数量描述，供健康/日志使用。
func (s *Sourcer) Active() string {
	if len(s.sources) == 1 {
		return s.sources[0].Name
	}
	return fmt.Sprintf("sourcer(%d)", len(s.sources))
}

// srcTrace 是单个源的一条寻源轨迹 (客户质疑"为什么没查到"时的自证材料 + 成本对账依据)。
type srcTrace struct {
	name    string
	status  string // ok=查得 / empty=查无 / error=失败 / skipped=命中即停或预算耗尽被短路
	reason  string
	code    string
	uid     string
	logid   string
	costFen int
	elapsed time.Duration
}

// Query 串行遍历排序后的源，命中 (001) 即停。见 Sourcer 文档的判定表。
func (s *Sourcer) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	// 单源：纯直通，行为与未寻源前完全一致。
	if len(s.sources) == 1 {
		return s.sources[0].Port.Query(ctx, req)
	}

	deadline := time.Now().Add(s.budget)
	traces := make([]srcTrace, 0, len(s.sources))

	var (
		emptyCnt int
		errCnt   int
		// 全查无/全失败路径的代表上游标识：取第一个非空 (失败也要可追查铁律)。
		fallbackUID, fallbackLogID, fallbackCode string
	)

	for i := range s.sources {
		src := s.sources[i]

		// 总时延预算是硬闸门：预算耗尽不再尝试下一个源 (禁止为多试一个源把下游拖超时)。
		remaining := time.Until(deadline)
		if remaining <= 0 {
			traces = append(traces, srcTrace{name: src.Name, status: "skipped", reason: "总时延预算耗尽"})
			errCnt++ // 预算耗尽等同该源未取得确定结论，计入"未查得"侧
			continue
		}

		cctx, cancel := context.WithTimeout(ctx, remaining)
		start := time.Now()
		res, err := src.Port.Query(cctx, req)
		cancel()
		elapsed := time.Since(start)

		tr := srcTrace{name: src.Name, costFen: src.CostFen, elapsed: elapsed}
		switch {
		case err != nil:
			// 失败也要可追查 (铁律)：子源"已应答但业务失败"时上游订单号/请求号在
			// *model.UpstreamError 里 (此时 res 为 nil，只从 res 取会全空)。
			tr.status = "error"
			var ue *model.UpstreamError
			if errors.As(err, &ue) {
				tr.code, tr.uid, tr.logid, tr.reason = ue.Code, ue.UID, ue.LogID, ue.Msg
			} else {
				tr.reason = err.Error()
			}
			errCnt++
			if fallbackUID == "" && tr.uid != "" {
				fallbackUID, fallbackLogID, fallbackCode = tr.uid, tr.logid, tr.code
			}
		case res == nil:
			tr.status = "error"
			tr.reason = "子源返回空结果"
			errCnt++
		case res.Code == "001":
			// 命中即停：直接返回该源结果 (range 已由各源客户端归一到同一份下游契约)，
			// 后续源全部标 skipped。上游标识取命中源的 UID/LogID。
			tr.status, tr.code, tr.uid, tr.logid = "ok", res.Code, res.UID, res.LogID
			traces = append(traces, tr)
			for j := i + 1; j < len(s.sources); j++ {
				traces = append(traces, srcTrace{name: s.sources[j].Name, status: "skipped", reason: "已命中即停"})
			}
			s.logTraces(req.Reqid, traces)
			return res, nil
		case res.Code == "999":
			tr.status, tr.uid, tr.logid = "empty", res.UID, res.LogID
			emptyCnt++
			if fallbackUID == "" && tr.uid != "" {
				fallbackUID, fallbackLogID = tr.uid, tr.logid
			}
		default:
			tr.status = "error"
			tr.code, tr.uid, tr.logid = res.Code, res.UID, res.LogID
			tr.reason = fmt.Sprintf("非预期 code=%s msg=%s", res.Code, res.Msg)
			errCnt++
			if fallbackUID == "" && tr.uid != "" {
				fallbackUID, fallbackLogID, fallbackCode = tr.uid, tr.logid, tr.code
			}
		}
		traces = append(traces, tr)
	}

	s.logTraces(req.Reqid, traces)

	// 全部源失败 (无一查无) → error。失败也要可追查 (铁律)：返回 *model.UpstreamError
	// (busiErr) 把代表源的 code/uid/logid 带出，禁止裸 fmt.Errorf。
	if emptyCnt == 0 {
		code := fallbackCode
		if code == "" {
			code = "sourcer_all_failed"
		}
		return nil, busiErr(code, fmt.Sprintf("全部数据源失败 (reqid=%s)", req.Reqid), fallbackUID, fallbackLogID)
	}

	// 无源查得 + ≥1 源失败 → 002 (未取得数据且部分数据源异常，不计费)。
	if errCnt > 0 {
		return &model.UpstreamResult{Code: "002", Msg: "未取得数据且部分数据源异常", UID: fallbackUID, Reqid: req.Reqid, LogID: fallbackLogID}, nil
	}

	// 全部源查无 → 999 (range 恒空，与单源一致)。
	return &model.UpstreamResult{Code: "999", Msg: "查无结果", UID: fallbackUID, Reqid: req.Reqid, LogID: fallbackLogID}, nil
}

// logTraces 打印逐源寻源轨迹 (哪些源被短路、为什么)——排障与成本对账依据。
func (s *Sourcer) logTraces(reqid string, traces []srcTrace) {
	for _, t := range traces {
		slog.Debug("sourcer trace",
			"reqid", reqid, "source", t.name, "status", t.status, "reason", t.reason,
			"code", t.code, "uid", t.uid, "logid", t.logid,
			"costFen", t.costFen, "elapsedMs", t.elapsed.Milliseconds())
	}
}

// Requery：多源寻源暂不做逐源对账，返回 Reachable=false 保持 PENDING 由对账兜底
// (与 Aggregator 多源一致)。单源时直通到该源。
func (s *Sourcer) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	if len(s.sources) == 1 {
		return s.sources[0].Port.Requery(ctx, reqid)
	}
	return &model.RequeryResult{Reachable: false}, nil
}
