package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/domain/port"
)

// LabeledUpstream 是聚合路由里的一个子源：label 为其在合并 range 里的段名，
// port 是实际的上游客户端 (每个子源自带独立 endpoint/凭证)。
type LabeledUpstream struct {
	Label string
	Port  port.UpstreamPort
}

// Aggregator 把一条路由的 N 个上游子源统一成一个 port.UpstreamPort，对 orchestrator
// 完全透明。len==1 时纯直通 (单源路由零行为变化)；len>1 时并发查询所有子源、逐源
// 归一后按判定表聚合 (add-upstream-multi skill)：
//   - 全部成功应答且 ≥1 份查得 → "001"（计费）
//   - 全部成功应答且全部查无   → "999"
//   - 部分成功部分失败         → "002"（部分数据源成功，不计费，range 带成功段）
//   - 全部失败                 → error（对外 505062，走复查/对账）
type Aggregator struct {
	sources []LabeledUpstream
}

// NewAggregator builds an Aggregator over one route's 上游子源列表 (至少 1 个)。
func NewAggregator(sources []LabeledUpstream) (*Aggregator, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("upstream aggregator: 至少需要一个子源")
	}
	for i := range sources {
		if sources[i].Port == nil {
			return nil, fmt.Errorf("upstream aggregator: 子源 %d (%q) 未初始化", i, sources[i].Label)
		}
		if sources[i].Label == "" {
			sources[i].Label = fmt.Sprintf("source%d", i+1)
		}
	}
	return &Aggregator{sources: sources}, nil
}

// Active 返回子源数量描述，供健康/日志使用。
func (a *Aggregator) Active() string {
	if len(a.sources) == 1 {
		return a.sources[0].Label
	}
	return fmt.Sprintf("aggregate(%d)", len(a.sources))
}

// aggSection 是一个子源归一后的结果，聚合进合并 range 的一段。
//
// **禁止携带任何上游信息**：失败只体现为中性的 status="error"，上游业务码/原始文案/
// 订单号/请求号一概不进 range——它们经 *model.UpstreamError 落审计（后台「上游uid/
// 上游logId/上游code」三列）并打进服务端日志，运营与排障照旧够用，但不外泄给下游。
// 段名同理由装配层给中性名（见 cmd/relay/main.go 的 labelFor），不得暴露上游 kind。
type aggSection struct {
	Status string          `json:"status"`         // ok=查得 / empty=查无 / error=该源失败
	Data   json.RawMessage `json:"data,omitempty"` // 查得时透出子源 result.range 原样
}

func (a *Aggregator) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	// 单源：纯直通，行为与未聚合前完全一致。
	if len(a.sources) == 1 {
		return a.sources[0].Port.Query(ctx, req)
	}

	sources := a.sources

	type sub struct {
		label   string
		section aggSection
		uid     string
		logid   string
		code    string
		msg     string
		reason  string // 仅用于服务端日志与全失败汇总，绝不进 range
	}
	results := make([]sub, len(sources))
	var wg sync.WaitGroup
	for i := range sources {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := sources[i]
			res, err := s.Port.Query(ctx, req)
			sb := sub{label: s.Label, section: classify(res, err)}
			// 失败也要可追查（铁律）：子源"已应答但业务失败"时携带的上游订单号/
			// 请求号在 *model.UpstreamError 里，必须捞出来，不能只从成功 res 取
			// （res 为 nil 会丢标识，正是「上游uid/logId 列为空」的根因）。
			switch {
			case err != nil:
				var ue *model.UpstreamError
				if errors.As(err, &ue) {
					sb.uid, sb.logid, sb.code, sb.msg = ue.UID, ue.LogID, ue.Code, ue.Msg
					sb.reason = ue.Msg
				} else {
					sb.msg = err.Error()
					sb.reason = err.Error()
				}
			case res == nil:
				sb.reason = "子源返回空结果"
			default:
				sb.uid, sb.logid = res.UID, res.LogID
				if sb.section.Status == "error" {
					sb.code, sb.msg = res.Code, res.Msg
					sb.reason = fmt.Sprintf("子源返回非预期 code=%s msg=%s", res.Code, res.Msg)
				}
			}
			results[i] = sb
		}(i)
	}
	wg.Wait()

	var okCnt, emptyCnt, errCnt int
	uid := ""
	logid := ""
	sections := make(map[string]aggSection, len(results))
	for _, r := range results {
		sections[r.label] = r.section
		if uid == "" && r.uid != "" {
			uid = r.uid
		}
		if logid == "" && r.logid != "" {
			logid = r.logid
		}
		switch r.section.Status {
		case "ok":
			okCnt++
		case "empty":
			emptyCnt++
		default:
			errCnt++
			// 失败原因只落服务端日志（range 里只有中性 status），排障看这里。
			slog.Warn("upstream aggregate 子源失败", "reqid", req.Reqid, "source", r.label,
				"code", r.code, "uid", r.uid, "logid", r.logid, "reason", r.reason)
		}
	}
	slog.Debug("upstream aggregate", "reqid", req.Reqid, "ok", okCnt, "empty", emptyCnt, "err", errCnt)

	// 全部失败 → error（对外 505062，交由 orchestrator 走复查/对账）。失败也要可
	// 追查（铁律）：必须返回 *model.UpstreamError（busiErr）把子源上游订单号/请求号
	// (uid/logid) 与业务码带出，orchestrator 才能落进审计的「上游uid/上游logId/上游
	// code」列——禁止用裸 fmt.Errorf（那会丢掉所有上游标识，三列变空）。
	if errCnt == len(results) {
		code := ""
		var b strings.Builder
		b.WriteString(fmt.Sprintf("聚合上游全部数据源失败 (reqid=%s)", req.Reqid))
		for _, r := range results {
			if code == "" && r.code != "" {
				code = r.code
			}
			b.WriteString(fmt.Sprintf(" | %s: code=%s %s", r.label, r.code, r.reason))
		}
		if code == "" {
			code = "aggregate_all_failed"
		}
		return nil, busiErr(code, b.String(), uid, logid)
	}

	switch {
	case errCnt > 0:
		merged, err := json.Marshal(sections)
		if err != nil {
			return nil, fmt.Errorf("聚合序列化失败: %w", err)
		}
		return &model.UpstreamResult{Code: "002", Msg: "部分数据源成功", UID: uid, Reqid: req.Reqid, LogID: logid, Range: string(merged)}, nil
	case okCnt > 0:
		merged, err := json.Marshal(sections)
		if err != nil {
			return nil, fmt.Errorf("聚合序列化失败: %w", err)
		}
		return &model.UpstreamResult{Code: "001", Msg: "成功", UID: uid, Reqid: req.Reqid, LogID: logid, Range: string(merged)}, nil
	default:
		// 全部查无：与单上游 999 一致，range 恒空 (DESIGN §7.4)。
		return &model.UpstreamResult{Code: "999", Msg: "查无结果", UID: uid, Reqid: req.Reqid, LogID: logid}, nil
	}
}

// classify 把一个子源的 (result, err) 归一为聚合段：err→error / 999→empty /
// 001→ok(透出 range) / 其余非预期码→error。
//
// 失败分支只给中性 status，**不带**上游 code/msg/订单号/请求号——那些是上游信息，
// 一律走审计与日志（见 aggSection 注释）。查得分支透出的 res.Range 已由各上游客户端
// 经 sanitizeRange 剥过上游标识，此处直接沿用。
func classify(res *model.UpstreamResult, err error) aggSection {
	if err != nil || res == nil {
		return aggSection{Status: "error"}
	}
	switch res.Code {
	case "999":
		return aggSection{Status: "empty"}
	case "001":
		sec := aggSection{Status: "ok"}
		if res.Range != "" && json.Valid([]byte(res.Range)) {
			sec.Data = json.RawMessage(res.Range)
		}
		return sec
	default:
		return aggSection{Status: "error"}
	}
}

// Requery：单源直通；多源聚合暂不做逐源对账，返回 Reachable=false 保持 PENDING
// 由对账兜底。
func (a *Aggregator) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	if len(a.sources) == 1 {
		return a.sources[0].Port.Requery(ctx, reqid)
	}
	return &model.RequeryResult{Reachable: false}, nil
}
