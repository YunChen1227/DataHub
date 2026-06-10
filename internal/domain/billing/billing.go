// Package billing turns an upstream result into a charge verdict (DESIGN §7.4).
// The decision table is config-driven so it can be aligned with the upstream's
// actual扣费口径 without code changes.
package billing

import "github.com/datahub/relay/internal/domain/model"

// DecisionTable maps upstream code → whether the upstream charged us.
type DecisionTable struct {
	chargedCodes map[string]bool
}

// DefaultTable reflects DESIGN §7.4: only 001/999 are charged.
func DefaultTable() *DecisionTable {
	return &DecisionTable{chargedCodes: map[string]bool{
		"001": true, // 成功
		"999": true, // 查无结果（上游已执行查询并扣费）
	}}
}

// NewTable builds a table from an explicit set of charged codes (config).
func NewTable(chargedCodes map[string]bool) *DecisionTable {
	cp := make(map[string]bool, len(chargedCodes))
	for k, v := range chargedCodes {
		cp[k] = v
	}
	return &DecisionTable{chargedCodes: cp}
}

// IsCharged reports whether the upstream code implies a real扣费.
func (t *DecisionTable) IsCharged(code string) bool { return t.chargedCodes[code] }

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

// Decide evaluates a direct upstream response. Returned (维度①) and Charged
// (维度②) move together under the current dictionary but are kept distinct.
func (s *Service) Decide(r *model.UpstreamResult) *model.BillingDecision {
	if r == nil {
		return &model.BillingDecision{Charged: false, Returned: false}
	}
	charged := s.table.IsCharged(r.Code)
	return &model.BillingDecision{Charged: charged, Returned: charged, Result: r}
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
