// Package model holds the framework-agnostic core types shared across all
// layers (DESIGN §2/§5/§11). It depends on nothing but the standard library so
// it never participates in import cycles.
package model

// QueryCommand is the parsed client request body (接口文档-经济能力.doc §3.1.3:
// mobile 必填 / idCard 必填 / name 选填).
type QueryCommand struct {
	Mobile string `json:"mobile"`
	IDCard string `json:"idCard"`
	Name   string `json:"name"`
}

// SignedRequest carries the request envelope material needed for MD5 signature
// verification (接口文档-经济能力.doc 网关 appKey/appSecret / DESIGN §8.1).
// BodyParams are the non-empty business params (string) used to recompute the
// signature; appKey/sign/encryptionType do not participate in signing.
type SignedRequest struct {
	AppKey         string
	Sign           string
	EncryptionType int
	BodyParams     map[string]string
}

// LicenseView is the authenticated client identity + status (DESIGN §7.1).
type LicenseView struct {
	LicenseID   string
	AppKey      string
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

// UpstreamResult is the normalized upstream response (DESIGN §6). Every provider
// (income_cls / 伽马) maps its native response into this shape; Code is in the
// income_cls 口径 ("001" 查得 / "999" 查无) so billing + downstream body 统一。
type UpstreamResult struct {
	Code   string // "001" 查得 / "999" 查无
	Msg    string
	UID    string // 上游流水号 (income_cls uid / 伽马 seqNo)
	Reqid  string
	Range  string // 收入模型评分
	Verify string // 上游签名 (income_cls 透传; 伽马为空)
	LogID  string
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
	AppKey          string
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

// QueryResponse is the unified client response envelope
// (接口文档-经济能力.doc §3.1.4): {head, body}. body 省略于 head 级错误。
type QueryResponse struct {
	Head ResponseHead `json:"head"`
	Body *QueryBody   `json:"body,omitempty"`
}

// ResponseHead is the gateway头部 (接口文档-经济能力.doc §3.1.4).
//   - ErrorCode "0" = 成功（含查得/查无）; 非 0 = 网关级错误。
//   - LogID = 全链路 requestId (§9); Time = 处理耗时 ms; Timestamp = 毫秒时间戳。
type ResponseHead struct {
	ErrorCode string `json:"errorCode"`
	LogID     string `json:"logId"`
	Time      int64  `json:"time"`
	ErrorMsg  string `json:"errorMsg"`
	Timestamp int64  `json:"timestamp"`
}

// QueryBody is the V9 业务响应体 (接口文档-经济能力.doc §3.1.4). 字段口径沿用
// income_cls：code 001 查得 / 999 查无；result.range 为收入模型评分。
type QueryBody struct {
	Code   string       `json:"code"`
	Msg    string       `json:"msg"`
	UID    string       `json:"uid"`
	Reqid  string       `json:"reqid"`
	Verify string       `json:"verify"`
	Result *RangeResult `json:"result,omitempty"`
}

// RangeResult is the result content (接口文档-经济能力.doc §3.1.4): range 评分.
type RangeResult struct {
	Range string `json:"range"`
}
