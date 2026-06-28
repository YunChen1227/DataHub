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
// IP 准入自 v0.7 起移交阿里云 ECS 安全组，网关不再做 IP 白名单。
type LicenseView struct {
	LicenseID  string
	AppKey     string
	ClientUUID string
	Status     string // ACTIVE / SUSPENDED / EXPIRED
}

// Active reports whether the license may call the service.
func (l *LicenseView) Active() bool { return l != nil && l.Status == "ACTIVE" }

// UpstreamRequest carries the参数 the upstream client needs to build its signed
// request (DESIGN §6). 唯一上游伽马使用 IDCard/Name/Mobile, Reqid 为内部幂等流水号。
type UpstreamRequest struct {
	IDCard string
	Name   string
	Mobile string
	Reqid  string
}

// UpstreamResult is the normalized upstream response (DESIGN §6). 唯一上游伽马把原生
// 响应归一化为此形态; Code 统一为 ("001" 查得 / "999" 查无) so billing + downstream body 统一。
type UpstreamResult struct {
	Code   string // "001" 查得 / "999" 查无
	Msg    string
	UID    string // 上游流水号 (伽马 seqNo)
	Reqid  string
	Range  string // 收入模型评分
	Verify string // 上游签名 (伽马为空)
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
//   - Resolved → 上游给出了确定结论（查得或查无）→ 台账 BILLED；否则 UNBILLED。
//   - Returned → upstream produced查得数据 (成功查得数 +1, = busiCode 10).
//
// The two are kept separate so the口径 can diverge by config (DESIGN §7.4):
// 999 查无结果 is Resolved=true, Returned=false.
type BillingDecision struct {
	Resolved bool
	Returned bool
	Result   *UpstreamResult
}

// Ledger is the append-only billing record (DESIGN §11.3). Version 标记产生该
// 台账的路由 (x1/v9/v8/zlf/blk)，使共享同一 license 的 v8/v9 在域库内幂等/统计相互独立。
type Ledger struct {
	ID             int64
	AppKey         string
	Version        string // 路由名 (= 调用的版本)，幂等键 (app_key, version, reqid) 的一部分
	TradeNo        string
	Reqid          string
	RequestID      string
	UpstreamCode   string
	BusiCode       int
	UpstreamUID    string
	UpstreamLogID  string
	State          BillingState
	CountedService bool
}

// ServiceQuotaView is the client-facing snapshot (DESIGN §5.2). 无额度限制，
// 按路由独立统计：Used = 累计成功查得数, Calls = 累计调用上游次数。
type ServiceQuotaView struct {
	Status string
	Used   int64 // 成功查得数据次数（累计，busiCode 10）
	Calls  int64 // 调用上游次数（累计，CalledUpstream）
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

// QueryBody is the x1 业务响应体 (本服务 x1 契约). 字段口径沿用旧版 v9：
// code 001 查得 / 999 查无；result.range 为收入模型评分。
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

// Versions is the canonical ordered list of service versions. 各版本对外接口
// 完全一致 (x1 信封格式)，仅靠路由名区分，各自独立上游 + 独立数据库 + 独立
// license/调用记录/统计。x1 同时充当后台登录的控制面 (admin 账号 + JWT)。
// zlf 转接租赁分V2-D (守信 shouxin168) 上游；blk 转接黑名单因子V35 (应诺尔 enol) 上游。
// 注：Versions 是「路由」维度；存储/license 按「域」(Domains) 聚合——v8/v9 同属 v8v9 域，
// 共用一套 license，但统计/日志仍按各自路由独立 (见 RouteDomain)。
var Versions = []string{"x1", "v9", "v8", "zlf", "blk"}

// Domains is the canonical ordered list of license 域 (存储边界)。每个域独占一套
// DB + Redis + license 表；v8/v9 合并为 v8v9 域共用同一 license，其余域名即路由名。
var Domains = []string{"x1", "v8v9", "zlf", "blk"}

// RouteDomain maps a route (version) to its license 域。v8/v9 → v8v9 (共用 license)，
// 其余路由各自独立成域。域决定连哪套存储；路由决定上游与统计/日志的 route 作用域。
func RouteDomain(route string) string {
	switch route {
	case "v8", "v9":
		return "v8v9"
	default:
		return route
	}
}

// ValidVersion reports whether v is one of the supported service versions (routes).
func ValidVersion(v string) bool {
	for _, x := range Versions {
		if x == v {
			return true
		}
	}
	return false
}
