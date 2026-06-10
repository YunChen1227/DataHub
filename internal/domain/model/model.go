// Package model holds the framework-agnostic core types shared across all
// layers (DESIGN §2/§5/§11). It depends on nothing but the standard library so
// it never participates in import cycles.
package model

// QueryCommand is the parsed client request body (DESIGN §5.1).
type QueryCommand struct {
	Mobile string `json:"mobile"`
	IDCard string `json:"idCard"`
	Name   string `json:"name"`
	Reqid  string `json:"reqid"`
}

// SignedRequest carries the raw transport材料 needed for HMAC verification
// (DESIGN §8.1). Body is the exact bytes the client signed.
type SignedRequest struct {
	AppKey    string
	Timestamp string
	Nonce     string
	Sign      string
	Body      []byte
}

// LicenseView is the authenticated client identity + status (DESIGN §7.1).
type LicenseView struct {
	LicenseID  string
	AppKey     string
	ClientUUID string
	Status     string // ACTIVE / SUSPENDED / EXPIRED
}

// Active reports whether the license may call the service.
func (l *LicenseView) Active() bool { return l != nil && l.Status == "ACTIVE" }

// UpstreamRequest is the GET request sent to income_cls (DESIGN §6).
type UpstreamRequest struct {
	Account string
	IDCard  string
	Name    string
	Mobile  string
	Reqid   string
	Verify  string // MD5 signature, filled by the upstream client
}

// UpstreamResult is the parsed upstream response (DESIGN §6).
type UpstreamResult struct {
	Code  string
	Msg   string
	UID   string
	Reqid string
	Range string
	LogID string
}

// RequeryResult is the outcome of an idempotent re-query (DESIGN §7.3).
// Reachable=false means the upstream could not be reached此刻; the ledger stays
// PENDING for the reconciliation job to settle.
type RequeryResult struct {
	Reachable bool
	Result    *UpstreamResult // nil when upstream confirms "未执行/未扣费"
}

// BillingState is the ledger lifecycle state (DESIGN §7.3). There is no UNKNOWN
// terminal state — PENDING is always resolved by re-query or reconciliation.
type BillingState string

const (
	StatePending  BillingState = "PENDING"
	StateBilled   BillingState = "BILLED"
	StateUnbilled BillingState = "UNBILLED"
)

// BillingDecision is the verdict the billing engine produces.
//   - Charged  → upstream actually charged us (维度② commit).
//   - Returned → upstream produced a business return (维度① count).
//
// With the current code dictionary the two move together, but they are kept
// separate so the口径 can diverge by config (DESIGN §7.4 note).
type BillingDecision struct {
	Charged  bool
	Returned bool
	Result   *UpstreamResult
}

// Ledger is the append-only billing record (DESIGN §11.3).
type Ledger struct {
	ID              int64
	AppKey          string
	Reqid           string
	RequestID       string
	UpstreamCode    string
	UpstreamUID     string
	UpstreamLogID   string
	State           BillingState
	CountedService  bool
	CountedUpstream bool
}

// ServiceQuotaView is the client-facing 维度① snapshot (DESIGN §5.2).
type ServiceQuotaView struct {
	Status    string
	Total     int64
	Used      int64
	Remaining int64
}

// QueryResult is the unified client response envelope (DESIGN §5.1).
type QueryResult struct {
	Head Head  `json:"head"`
	Body *Body `json:"body,omitempty"`
}

type Head struct {
	ErrorCode string `json:"errorCode"`
	RequestID string `json:"requestId"`
	LogID     string `json:"logId"`
	Time      int64  `json:"time,omitempty"`
	ErrorMsg  string `json:"errorMsg"`
	Timestamp int64  `json:"timestamp"`
}

type Body struct {
	Code   string       `json:"code"`
	Msg    string       `json:"msg"`
	Result *RangeResult `json:"result,omitempty"`
	UID    string       `json:"uid,omitempty"`
	Reqid  string       `json:"reqid,omitempty"`
	Verify string       `json:"verify,omitempty"`
}

type RangeResult struct {
	Range string `json:"range"`
}
