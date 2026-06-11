// Package billing turns an upstream result into a charge verdict (DESIGN §7.4).
// The decision table is config-driven so it can be aligned with the upstream's
// actual扣费口径 without code changes.
package billing

import "github.com/datahub/relay/internal/domain/model"

// DecisionTable separates two independent verdicts per upstream code (DESIGN §7.4):
//   - chargedCodes  → 上游对我方扣费（维度②，我方成本）。
//   - returnedCodes → 查得数据（维度①，对用户计费，= busiCode 10）。
//
// 两者解耦：999 查无结果 上游已扣费(charged) 但非查得数据(not returned)，故计维度②、不计维度①。
type DecisionTable struct {
	chargedCodes  map[string]bool
	returnedCodes map[string]bool
}

// DefaultTable reflects DESIGN §7.4:
//   - CHARGED_CODES  = {001, 999}（上游扣费）
//   - RETURNED_CODES = {001}（仅查得数据才对用户计费）
func DefaultTable() *DecisionTable {
	return &DecisionTable{
		chargedCodes: map[string]bool{
			"001": true, // 成功
			"999": true, // 查无结果（上游已执行查询并扣费）
		},
		returnedCodes: map[string]bool{
			"001": true, // 仅查得数据才计维度①（对用户计费）
		},
	}
}

// NewTable builds a table from explicit charged/returned code sets (config).
func NewTable(chargedCodes, returnedCodes map[string]bool) *DecisionTable {
	return &DecisionTable{
		chargedCodes:  copySet(chargedCodes),
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

// IsCharged reports whether the upstream code implies a real扣费 (维度②).
func (t *DecisionTable) IsCharged(code string) bool { return t.chargedCodes[code] }

// IsReturned reports whether the upstream code means查得数据 (维度①, busiCode 10).
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

// Decide evaluates a direct upstream response. Charged (维度②, 上游扣费) and
// Returned (维度①, 查得数据→对用户计费) are decided independently: 999 查无结果
// is Charged=true, Returned=false (DESIGN §7.4).
func (s *Service) Decide(r *model.UpstreamResult) *model.BillingDecision {
	if r == nil {
		return &model.BillingDecision{Charged: false, Returned: false}
	}
	return &model.BillingDecision{
		Charged:  s.table.IsCharged(r.Code),
		Returned: s.table.IsReturned(r.Code),
		Result:   r,
	}
}

// FromRequery evaluates an idempotent re-query outcome (DESIGN §7.3).
//   - Reachable + charged code → BILLED (维度② 不漏计).
//   - Reachable + non-charged   → UNBILLED.
//   - Unreachable               → no decision yet (caller keeps PENDING for
//     reconciliation); represented as not-charged/not-returned.
func (s *Service) FromRequery(rr *model.RequeryResult) *model.BillingDecision {
	if rr == nil || !rr.Reachable || rr.Result == nil {
		return &model.BillingDecision{Charged: false, Returned: false}
	}
	return s.Decide(rr.Result)
}
