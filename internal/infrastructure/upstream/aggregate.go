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
// port 是实际的上游客户端 (每个子源自带独立 endpoint/凭证)。Optional 标记可选
// 子源：下游请求 scope=basic 时跳过（不调用、不出现在结果段里），如 swfp 的
// 源5 销项数据；缺省 false（恒调用，既有路由零行为变化）。
type LabeledUpstream struct {
	Label    string
	Port     port.UpstreamPort
	Optional bool
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
type aggSection struct {
	Status string          `json:"status"`          // ok=查得 / empty=查无 / error=该源失败
	Data   json.RawMessage `json:"data,omitempty"`  // 查得时透出子源 result.range 原样
	Error  string          `json:"error,omitempty"` // status=error 时的原因摘要
}

func (a *Aggregator) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	// 单源：纯直通，行为与未聚合前完全一致。
	if len(a.sources) == 1 {
		return a.sources[0].Port.Query(ctx, req)
	}

	// scope=basic 时跳过 optional 子源（不调用、不出段）；其余取值/未配置 optional
	// 的路由行为不变。跳过后仅剩单源也走多源聚合分支（保证 range 仍是分段结构）。
	sources := a.sources
	if req != nil && req.Scope == model.ScopeBasic {
		kept := make([]LabeledUpstream, 0, len(sources))
		for _, s := range sources {
			if !s.Optional {
				kept = append(kept, s)
			}
		}
		if len(kept) > 0 {
			sources = kept
		}
	}

	type sub struct {
		label   string
		section aggSection
		uid     string
		logid   string
		code    string
		msg     string
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
			if err != nil {
				var ue *model.UpstreamError
				if errors.As(err, &ue) {
					sb.uid, sb.logid, sb.code, sb.msg = ue.UID, ue.LogID, ue.Code, ue.Msg
				} else {
					sb.msg = err.Error()
				}
			} else if res != nil {
				sb.uid, sb.logid = res.UID, res.LogID
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
			b.WriteString(fmt.Sprintf(" | %s: code=%s %s", r.label, r.code, r.msg))
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
func classify(res *model.UpstreamResult, err error) aggSection {
	if err != nil {
		return aggSection{Status: "error", Error: err.Error()}
	}
	if res == nil {
		return aggSection{Status: "error", Error: "子源返回空结果"}
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
		return aggSection{Status: "error", Error: fmt.Sprintf("子源返回非预期 code=%s msg=%s", res.Code, res.Msg)}
	}
}

// Requery：单源直通；多源聚合暂不做逐源对账，返回 Reachable=false 保持 PENDING
// 由对账兜底 (与聚合前 entcredit 行为一致)。
func (a *Aggregator) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	if len(a.sources) == 1 {
		return a.sources[0].Port.Requery(ctx, reqid)
	}
	return &model.RequeryResult{Reachable: false}, nil
}
