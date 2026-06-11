// Package model holds the framework-agnostic core types shared across all
// layers (DESIGN §2/§5/§11). It depends on nothing but the standard library so
// it never participates in import cycles.
package model

// QueryCommand is the parsed client request body (PDF body / DESIGN §5.1).
type QueryCommand struct {
	Name    string `json:"name"`
	IDCard  string `json:"idCard"`
	Mobile  string `json:"mobile"`
	TradeNo string `json:"tradeNo"`
}

// SignedRequest carries the request envelope material needed for MD5 signature
// verification (PDF §1.4 / DESIGN §8.1). BodyParams are the non-empty business
// params (string) used to recompute the signature; appId/apiKey/sign/
// encryptionType do not participate in signing.
type SignedRequest struct {
	AppID          string
	Sign           string
	APIKey         string
	EncryptionType int
	BodyParams     map[string]string
}

// LicenseView is the authenticated client identity + status (DESIGN §7.1).
type LicenseView struct {
	LicenseID   string
	AppID       string
	ClientUUID  string
	Status      string   // ACTIVE / SUSPENDED / EXPIRED
	IPWhitelist []string // 每用户 IP 白名单 (DESIGN §16.4); 空表示不限制
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
//   - Returned → upstream produced查得数据 (维度① count, = busiCode 10).
//
// The two are kept separate so the口径 can diverge by config (DESIGN §7.4):
// 999 查无结果 is Charged=true, Returned=false.
type BillingDecision struct {
	Charged  bool
	Returned bool
	Result   *UpstreamResult
}

// Ledger is the append-only billing record (DESIGN §11.3).
type Ledger struct {
	ID              int64
	AppID           string
	TradeNo         string
	Reqid           string
	RequestID       string
	UpstreamCode    string
	BusiCode        int
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

// DoCheckResponse is the unified client response envelope (PDF §1.5 / DESIGN §5).
//   - Code  : 全局返回码 0 成功 / -1 响应异常.
//   - SeqNo : 交易流水号 (= 本服务 requestId, §9).
//   - Data  : 业务响应体 (省略于 code=-1).
type DoCheckResponse struct {
	Code  int          `json:"code"`
	Msg   string       `json:"msg"`
	SeqNo string       `json:"seqNo"`
	Data  *DoCheckData `json:"data,omitempty"`
}

// DoCheckData is the data object (PDF §1.5).
type DoCheckData struct {
	BusiCode int          `json:"busiCode"`
	BusiMsg  string       `json:"busiMsg"`
	Result   *ScoreResult `json:"result,omitempty"`
}

// ScoreResult is the result content (PDF §2.3): score 取值范围 1-51.
type ScoreResult struct {
	Score string `json:"score"`
}
