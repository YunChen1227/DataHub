// Package model holds the framework-agnostic core types shared across all
// layers (DESIGN §2/§5/§11). It depends on nothing but the standard library so
// it never participates in import cycles.
package model

import "fmt"

// QueryCommand is the parsed client request body. 个人三要素路由用 mobile(必)/
// idCard(必)/name(选)；swfp (税务发票聚合) 用 creditCode(统一社会信用代码，必)——
// 字段名直接对齐上游真实入参名 (证通 entcreditapi args.creditCode；2026-07-08
// 上游 E1000 报错明确指出必填字段名为 creditCode，官方 demo 文档里的 entInfo
// 示例字段名与四产品聚合接口实际契约不符，以服务器报错为准)，本服务做接口
// 转发，下游客户入参必须与上游契约一致，不臆造中间层字段名。各路由由自己的参数
// 校验器决定必填口径 (parse.Parse / parse.ParseCreditCode)。
type QueryCommand struct {
	Mobile     string `json:"mobile"`
	IDCard     string `json:"idCard"`
	Name       string `json:"name"`
	CreditCode string `json:"creditCode"`
	// rlbd1 (人脸身份证比对) 入参：image(base64) 与 url 二选一，配合 name/idCard。
	Image string `json:"image"`
	URL   string `json:"url"`
	// sfzhy (身份证三要素核验) 入参：人像照片 base64(≤50K)，配合 name/idCard。
	ProfilePicture string `json:"profilePicture"`
	// xfjy (消费交易特征) 入参：授权书编号 authlet，配合 name/idcard/mobile
	// （字段名对齐上游 data-bean params：name/idcard/mobile/authlet）。
	Authlet string `json:"authlet"`
	// tsfx (投诉分析识别名单) 入参：命中级别策略 poly（C1 高危/C2 敏感/C3 一般），
	// 配合 mobile（字段名对齐上游 kfongtech api.complaint.query 的 poly/mobile）。
	Poly string `json:"poly"`
	// swfp (税务发票聚合) 可选入参：调用范围。"all"(缺省)=全部数据源(含源5 销项
	// 数据)；"basic"=仅基础数据源(源1-4 发票/税务聚合)，不调可选源。字符串，非空时
	// 参与 MD5 加签。
	Scope string `json:"scope"`
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
// request (DESIGN §6). 个人三要素路由用 IDCard/Name/Mobile；swfp 用 CreditCode
// (统一社会信用代码，对齐上游 args.creditCode)。Reqid 为内部幂等流水号。
type UpstreamRequest struct {
	IDCard     string
	Name       string
	Mobile     string
	CreditCode string
	// rlbd1 (人脸身份证比对) 用 Image(base64) 或 URL (二选一) + Name/IDCard。
	Image string
	URL   string
	// sfzhy (身份证三要素核验) 用 ProfilePicture(base64) + Name/IDCard。
	ProfilePicture string
	// xfjy (消费交易特征) 用 Authlet(终端授权书编号) + Name/IDCard/Mobile。
	Authlet string
	// tsfx (投诉分析识别名单) 用 Poly(命中级别 C1/C2/C3) + Mobile。
	Poly string
	// swfp 调用范围（parse.ParseCreditCode 归一化后恒为 "all"/"basic"）："basic"
	// 时聚合器跳过标记为 optional 的子源（源5 销项数据），仅调基础源。
	Scope string
	Reqid string
}

// Scope 取值（swfp 调用范围）。
const (
	ScopeAll   = "all"   // 全部数据源（含可选源），缺省
	ScopeBasic = "basic" // 仅基础数据源（跳过 optional 子源）
)

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

// UpstreamError 表示上游"已应答但以业务码明确拒绝/失败"的错误（区别于网络不可达）。
// 它承载上游返回的可追查标识，供 orchestrator 写入审计——即便请求最终落 PENDING，
// 也能凭 UID(上游订单号) / LogID(上游请求号) 向上游对账、向上追查失败原因。
// 上游客户端在遇到"非成功业务码"时应返回本类型（而非裸 fmt.Errorf），字段尽量填全：
//   - Code：上游业务/状态码原值（如 "461"/"1002"/"SW0001"/"4"）
//   - Msg ：上游返回的错误消息
//   - UID ：上游订单号（对账用，如 OutBizNo/seqNo/respOrder/orderNo）
//   - LogID：上游请求/日志号（对账用，如 RequestId/reqno）
// 纯网络/传输失败（上游不可达、读超时）不用本类型——那时没有上游标识可填。
type UpstreamError struct {
	Code  string
	Msg   string
	UID   string
	LogID string
	Err   error // 可选底层原因
}

func (e *UpstreamError) Error() string {
	s := fmt.Sprintf("上游业务失败 code=%s msg=%s", e.Code, e.Msg)
	if e.UID != "" {
		s += " uid=" + e.UID
	}
	if e.LogID != "" {
		s += " logId=" + e.LogID
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *UpstreamError) Unwrap() error { return e.Err }

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

// Versions is the canonical ordered list of service versions (routes). 各版本对外
// 接口完全一致 (x1 信封格式)，仅靠路由名区分，各自独立上游。x1 同时充当后台登录
// 的控制面 (admin 账号 + JWT)。zlf 转接租赁分V2-D (守信 shouxin168) 上游；blk 转接
// 黑名单因子V35 (应诺尔 enol) 上游；swfp 聚合税务+发票四产品码 (企业维度,
// creditCode 入参, 见 upstream/entcredit.go)；rlbd1/rlbd2 转接人脸身份证比对一所
// (数脉 facecompare 上游，name+idCard+image|url 入参，见 upstream/facecompare.go；
// rlbd1/rlbd2 同一上游接口、各自独立的 appId/appSecret 与独立库/统计)；
// sfzhy 转接身份证三要素核验 (idverify 上游，name+idCard+profilePicture 入参，
// 见 upstream/idverify.go)；xfjy 转接消费交易特征 (consumetxn 上游 data-bean，
// JSON POST + MD5 sign，name/idcard/mobile/authlet 入参，有查得/查无，
// 见 upstream/consumetxn.go)；tsfx 转接投诉分析识别名单 (complaint 上游 kfongtech，
// JSON POST + AES 加密 param + MD5 sign，mobile/poly 入参，data gzip 压缩，
// 调用成功即计费、命中状态经 result.range 透出，见 upstream/complaint.go)；
// lxf 转接灵犀分 score_195_v1 (lxscore 上游 fullink，JSON POST + DES/CBC 签名，
// name/mobile/idCardNo 取 MD5 摘要，响应 data 为 DES 密文、解密得 300-900 评分，
// 评分经 result.range 透出，见 upstream/lxscore.go)。
// 注：Versions 是「路由」维度；存储/license 按「域」(Domains) 聚合——v8/v9 同属
// v8v9 域共用一套 license，其余路由各自独立成域 (见 RouteDomain)。跨域使用 license
// 一律鉴权失败 (505004 账户信息不存在)。
var Versions = []string{"x1", "v9", "v8", "zlf", "blk", "swfp", "rlbd1", "rlbd2", "sfzhy", "xfjy", "tsfx", "lxf"}

// Domains is the canonical ordered list of license 域 (存储边界)。每个域独占一套
// DB + Redis + license 表；v8/v9 合并为 v8v9 域共用同一 license，其余域名即路由名。
var Domains = []string{"x1", "v8v9", "zlf", "blk", "swfp", "rlbd1", "rlbd2", "sfzhy", "xfjy", "tsfx", "lxf"}

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

// DemoAppKey returns the per-域 dev demo license appKey（开发/测试专用；生产库
// 不播种 demo）。各域 demo 凭证互不相同，保证 demo token 无法跨域使用；v8/v9
// 同属 v8v9 域，共用同一个 demo appKey。
func DemoAppKey(route string) string {
	switch RouteDomain(route) {
	case "x1":
		return "y89098io"
	case "v8v9":
		return "y890v8v9"
	case "zlf":
		return "y8909zlf"
	case "blk":
		return "y8909blk"
	case "swfp":
		return "y890swfp"
	case "rlbd1":
		return "y89rlbd1"
	case "rlbd2":
		return "y89rlbd2"
	case "sfzhy":
		return "y89sfzhy"
	case "xfjy":
		return "y890xfjy"
	case "tsfx":
		return "y89tsfx"
	case "lxf":
		return "y8909lxf"
	default:
		return "demo-" + route
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
