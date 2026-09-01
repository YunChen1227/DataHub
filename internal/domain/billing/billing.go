// Package billing turns an upstream result into a charge verdict (DESIGN §7.4).
// The decision table is config-driven so it can be aligned with the upstream's
// actual扣费口径 without code changes.
package billing

import "github.com/datahub/relay/internal/domain/model"

// DecisionTable separates two independent verdicts per upstream code (DESIGN §7.4):
//   - resolvedCodes → 上游给出了确定结论（查得或查无）→ 台账 BILLED。
//   - returnedCodes → 查得数据（成功查得数 +1，= busiCode 10）。
//
// 两者解耦：999 查无结果 是确定结论(resolved) 但非查得数据(not returned)。
type DecisionTable struct {
	resolvedCodes map[string]bool
	returnedCodes map[string]bool
}

// DefaultTable reflects DESIGN §7.4:
//   - RESOLVED_CODES = {001, 999, 002}（上游确定结论）
//   - RETURNED_CODES = {001}（仅查得数据才累计成功查得数）
//
// 002 为多上游聚合路由特有：部分数据源成功——确定结论但数据不完整,
// 不计费。单上游路由永不产生 002。
func DefaultTable() *DecisionTable {
	return &DecisionTable{
		resolvedCodes: map[string]bool{
			"001": true, // 成功
			"999": true, // 查无结果（上游已给出确定结论）
			"002": true, // 部分数据源成功（聚合路由；确定结论、不计费）
		},
		returnedCodes: map[string]bool{
			"001": true, // 仅查得数据才累计成功查得数
		},
	}
}

// billNotFoundRoutes 列出「上游对查无也收费」的路由。绝大多数上游只对查得收费
// （DefaultTable），但个别产品的码表把查无也标了【计费】，此时我方必须同步向下游
// 计费，否则上游账单与我方成功查得数长期对不平（我方净亏）。
//
// 逐条给出文档依据，新增前必须先在上游文档里找到明确的计费标注：
//
//   - blk 黑名单因子V35（docs/黑名单因子V35.pdf §2.1 busiCode 表）：
//     「10 查询成功【计费】/ 1000 未查得【计费】」——两个码都带【计费】。
//     注意同一供应商（应诺尔 enol）的 x1 伽马分层分（docs/伽马分层分_定制版.pdf
//     §2.1）是「10 查询成功【计费】/ 1000 数据未查得」——1000 **不带**计费标注。
//     同端点、同信封、同 busiCode 语义，计费口径却不同，故必须按路由分别配置，
//     不能因为"看起来一样"而复用。
var billNotFoundRoutes = map[string]bool{
	"blk": true,
}

// TableFor 返回某条路由的计费码表。默认只有 001 查得计费；billNotFoundRoutes 里的
// 路由额外把 999 查无也计入计费（依据见该表的逐条文档出处）。
//
// 计费口径只影响「是否累计成功查得数 / 台账 counted_service」，**不影响下游报文
// 形态**：下游看到的 body.code 恒由上游归一码决定（001 查得 / 999 查无），
// 见 application.respondX1。两者解耦后，"查无但计费"这种上游口径才能如实落地。
func TableFor(route string) *DecisionTable {
	t := DefaultTable()
	if billNotFoundRoutes[route] {
		t.returnedCodes[model.CodeNotFound] = true
	}
	return t
}

// BillsNotFound 报告该路由的查无结果是否也计费。供装配期做交叉校验用（如结果缓存
// 白名单必须与之互斥，见 cmd/relay/main.go attachResultCache）。
func BillsNotFound(route string) bool { return billNotFoundRoutes[route] }

// NewTable builds a table from explicit resolved/returned code sets (config).
func NewTable(resolvedCodes, returnedCodes map[string]bool) *DecisionTable {
	return &DecisionTable{
		resolvedCodes: copySet(resolvedCodes),
		returnedCodes: copySet(returnedCodes),
	}
}

func copySet(src map[string]bool) map[string]bool {
	cp := make(map[string]bool, len(src))
	for k, v := range src {
		cp[k] = v
	}
	return cp
}

// IsResolved reports whether the upstream code is a确定结论 (查得/查无).
func (t *DecisionTable) IsResolved(code string) bool { return t.resolvedCodes[code] }

// IsReturned reports whether the upstream code means查得数据 (busiCode 10).
func (t *DecisionTable) IsReturned(code string) bool { return t.returnedCodes[code] }

// Service produces BillingDecisions.
type Service struct {
	table *DecisionTable
}

func New(table *DecisionTable) *Service {
	if table == nil {
		table = DefaultTable()
	}
	return &Service{table: table}
}

// Decide evaluates a direct upstream response. Resolved (确定结论) and Returned
// (查得数据→累计成功查得数) are decided independently: 999 查无结果 is
// Resolved=true, Returned=false (DESIGN §7.4).
func (s *Service) Decide(r *model.UpstreamResult) *model.BillingDecision {
	if r == nil {
		return &model.BillingDecision{Resolved: false, Returned: false}
	}
	return &model.BillingDecision{
		Resolved: s.table.IsResolved(r.Code),
		Returned: s.table.IsReturned(r.Code),
		Result:   r,
	}
}

// FromRequery evaluates an idempotent re-query outcome (DESIGN §7.3).
//   - Reachable + resolved code → BILLED.
//   - Reachable + non-resolved  → UNBILLED.
//   - Unreachable               → no decision yet (caller keeps PENDING for
//     reconciliation); represented as not-resolved/not-returned.
func (s *Service) FromRequery(rr *model.RequeryResult) *model.BillingDecision {
	if rr == nil || !rr.Reachable || rr.Result == nil {
		return &model.BillingDecision{Resolved: false, Returned: false}
	}
	return s.Decide(rr.Result)
}
