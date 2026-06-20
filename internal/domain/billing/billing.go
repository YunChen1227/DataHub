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
//   - RESOLVED_CODES = {001, 999}（上游确定结论）
//   - RETURNED_CODES = {001}（仅查得数据才累计成功查得数）
func DefaultTable() *DecisionTable {
	return &DecisionTable{
		resolvedCodes: map[string]bool{
			"001": true, // 成功
			"999": true, // 查无结果（上游已给出确定结论）
		},
		returnedCodes: map[string]bool{
			"001": true, // 仅查得数据才累计成功查得数
		},
	}
}

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
