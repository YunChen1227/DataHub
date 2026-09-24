// Package model holds the framework-agnostic core types shared across all
// layers (DESIGN §2/§5/§11). It depends on nothing but the standard library so
// it never participates in import cycles.
package model

import "fmt"

// QueryCommand is the parsed client request body. 个人三要素路由用 mobile(必)/
// idCard(必)/name(选)；各路由由自己的参数校验器决定必填口径 (parse.Parse 等)。
type QueryCommand struct {
	Mobile string `json:"mobile"`
	IDCard string `json:"idCard"`
	Name   string `json:"name"`
	// rlbd1 (人脸身份证比对) 入参：image(base64) 与 url 二选一，配合 name/idCard。
	Image string `json:"image"`
	URL   string `json:"url"`
	// sfzhy (身份证三要素核验) 入参：人像照片 base64(≤50K)，配合 name/idCard。
	ProfilePicture string `json:"profilePicture"`
	// xfjy (消费交易特征) 入参：授权书编号 authlet，配合 name/idcard/mobile
	// （字段名对齐上游 data-bean params：name/idcard/mobile/authlet）。
	Authlet string `json:"authlet"`
	// tsfx (投诉分析识别名单) 入参：仅 mobile。命中级别 poly 已不再是下游入参——
	// 网关对每次请求固定并发查询 C1/C2/C3 三档并整合结果 (见 upstream/complaint.go)。
	// 本字段保留仅为兼容仍携带 poly 的旧客户端，服务端一律忽略、不校验、不参与查询。
	Poly string `json:"poly"`
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
// request (DESIGN §6). 个人三要素路由用 IDCard/Name/Mobile。Reqid 为内部幂等流水号。
type UpstreamRequest struct {
	IDCard string
	Name   string
	Mobile string
	// rlbd1 (人脸身份证比对) 用 Image(base64) 或 URL (二选一) + Name/IDCard。
	Image string
	URL   string
	// sfzhy (身份证三要素核验) 用 ProfilePicture(base64) + Name/IDCard。
	ProfilePicture string
	// xfjy (消费交易特征) 用 Authlet(终端授权书编号) + Name/IDCard/Mobile。
	Authlet string
	// tsfx (投诉分析识别名单) 用 Mobile；命中级别 C1/C2/C3 由上游客户端内部逐档
	// 固定查询，不再来自下游 (见 upstream/complaint.go)。Poly 保留但已不填/不使用。
	Poly  string
	Reqid string
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

// 上游归一码：所有上游客户端把各自的业务码归一到这三个值之一，下游 body.code
// 直接回显。不在此列的一律是「上游侧错误」，由客户端返回 *UpstreamError。
const (
	CodeFound    = "001" // 查得数据
	CodeNotFound = "999" // 查无结果（上游给出了确定结论，只是没数据）
	CodePartial  = "002" // 部分数据源成功/失败（仅多源路由产生）
)

// IsFoundCode 报告归一码是否为「查得数据」。**这是报文形态的判据，不是计费判据**：
// 是否计费由 billing.DecisionTable 按路由决定（blk 的查无也计费）。
func IsFoundCode(code string) bool { return code == CodeFound }

// BillingDecision is the verdict the billing engine produces.
//   - Resolved → 上游给出了确定结论（查得或查无）→ 台账 BILLED；否则 UNBILLED。
//   - Returned → 本次按上游口径**应计费**（成功查得数 +1）。
//
// The two are kept separate so the口径 can diverge by config (DESIGN §7.4):
// 999 查无结果默认 Resolved=true, Returned=false——但 blk 这类上游对查无也收费的
// 路由会把 999 也置 Returned=true (billing.TableFor)，故 Returned 不可当作
// 「是否查得」使用，判查得请用 IsFoundCode。
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
	// FromCache 标记本条台账来自「自然月结果缓存」命中（未调用上游）。命中行照常
	// 计费 (CountedService 随查得/查无)，但 UpstreamUID/LogID 是首次回源时的原值，
	// 对账时须凭本列排除，避免把同一笔上游订单号重复报给上游。
	FromCache bool
}

// LedgerSettlement 是一次台账终态结算要写回的全部字段 (DESIGN §7.3 step 2)。
//
// 上游归一码与订单号/请求号必须随结算一起落库：
//   - UpstreamCode 是幂等重放时判「查得还是查无」的唯一依据。不能用 CountedService
//     代替——blk 这类路由的查无也计费，两者会分叉 (billing.TableFor)。
//   - UpstreamUID/LogID 是向上游对账时定位那笔订单的键。缺了它们，台账只能证明
//     「我方认为该收费」，无法证明「上游那边是哪一笔」。
type LedgerSettlement struct {
	State          BillingState
	CountedService bool
	UpstreamCode   string // 归一码 001/999/002；上游未给出结论(PENDING/失败)时为空
	UpstreamUID    string
	UpstreamLogID  string
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
	Code string `json:"code"`
	Msg  string `json:"msg"`
	// UID 是**我方**交易流水号 (与 head.logId 同值)，供下游向我方对账。它**不是**上游
	// 订单号——上游标识只落审计，不随响应外泄 (见 mapping.Found 注释)。
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
// 黑名单因子V35 (应诺尔 enol) 上游；rlbd1/rlbd2 转接人脸身份证比对一所
// (数脉 facecompare 上游，name+idCard+image|url 入参，见 upstream/facecompare.go；
// rlbd1/rlbd2 同一上游接口、各自独立的 appId/appSecret 与独立库/统计)；
// sfzhy 转接身份证三要素核验 (idverify 上游，name+idCard+profilePicture 入参，
// 见 upstream/idverify.go)；xfjy 转接消费交易特征 (consumetxn 上游 data-bean，
// JSON POST + MD5 sign，name/idcard/mobile/authlet 入参，有查得/查无，
// 见 upstream/consumetxn.go)；tsfx 转接投诉分析识别名单 (complaint 上游 kfongtech，
// JSON POST + AES 加密 param + MD5 sign，下游仅 mobile 入参、命中级别 C1/C2/C3 由
// 客户端逐档并发查询后整合成 poly 字典透出 result.range，data gzip 压缩，调用成功即
// 计费，见 upstream/complaint.go)；
// lxf 转接灵犀分 score_195_v1 (lxscore 上游 fullink，JSON POST + DES/CBC 签名，
// name/mobile/idCardNo 取 MD5 摘要，响应 data 为 DES 密文、解密得 300-900 评分，
// 评分经 result.range 透出，见 upstream/lxscore.go)；
// grgjj 转接收入A_g版 (incomeag 上游 yrzx，JSON POST + data 走 3DES/ECB/PKCS5+base64
// 加密、verify=MD5(account+加密前JSON串+reqid+type+key)，name/cid/mobile 入参，
// 响应 result 为 3DES 密文、解密得 {cbjfzt,jfjs,jfsj} 经 result.range 透出，
// 见 upstream/incomeag.go)；
// grsb 转接背景评估 BJPG-01 (bgpg 上游，JSON POST + data 走 AES/CBC/PKCS5+base64
// 加密、密钥为 encryptKey 的 hex 解码值、IV 固定 "0000000000000000"，请求头带
// accountId/prodId，入参仅 idCard+name 两项 (无 mobile)，响应 data 为同一套 AES
// 密文、解密得 {xm,sfz,jfdw,grsf,jfjs,cbjfzt,jfsj} 全字段经 result.range 透出，
// 见 upstream/bgpg.go)；
// sfsm 转接身份证实名核验 (idcheck 上游，数脉 id_card/check，与 rlbd1/rlbd2 同一
// 服务商同一套签名：form POST + sign=md5(appid&timestamp&app_security)，入参仅
// name+idCard 两项均必填 (无 mobile)，响应 data 的 {result,desc,sex,birthday,address}
// 经 result.range 透出、result 0 一致/1 不一致均为收费结论，见 upstream/idcheck.go)；
// sffx 转接身份风险V107 (idrisk 上游，应诺尔 enol，与 x1/blk 同端点同信封：JSON POST
// + apiKey=idRiskTagV107、encryptionType=2 (PII 走 MD5)，入参仅 name+idCard 两项
// 均必填 (无 mobile)，响应 result 富对象 {detail:[风险类型码+周期码]} 序列化经
// result.range 透出；busiCode 10 查得 / 1000 未查得**均计费**，见 upstream/idrisk.go)；
// dtjd 转接多头借贷行为 (multiloan 上游，守信 shouxin168，与 zlf/rental 同一端点同一
// 信封：AES/ECB/PKCS5 加密 biz_data + form POST，service=financial_rent_service、
// mode=mode_loan_intent_v1，入参 name+idCard+mobile 三要素均必填；响应 resp_data 为约
// 700 个 als_* 多头因子 + Rule_* 决策字段的富对象，整体序列化经 result.range 透出；
// SW0000 查得计费 / SW0002 查无不计费 / **SW0001 认证失败上游标【收费】但我方按查无
// 且不向下游计费**(落 warn 人工对账)，见 upstream/multiloan.go)；
// snhmd 转接司南黑名单 (compassblack 上游，守信 shouxin168，与 zlf/dtjd 同一端点同一
// 信封：AES/ECB/PKCS5 加密 biz_data + form POST，service 同为 financial_rent_service、
// **mode=mode_compass_black** 是同端点区分产品的唯一位，入参 name+idCard+mobile 三要素
// 均必填；响应 resp_data 为 black_list + black_tag04..12 共 10 个 "0"/"1" 标签的对象，
// 整体序列化经 result.range 透出 (black_list=0 未命中亦属查得结论，计费)；SW0000 查得
// 计费 / SW0002 查无不计费 / **SW0001 认证失败本产品标【不收费】→ 按上游侧错误处理**
// (与兄弟产品 dtjd 的同码口径相反，禁止互相套用)，见 upstream/compassblack.go)；
// dtly 转接多头履约行为 (manyoverdue 上游，守信 shouxin168，与 zlf/dtjd/snhmd 同一端点
// 同一信封：AES/ECB/PKCS5 加密 biz_data + form POST，service 同为 financial_rent_service、
// **mode=mode_many_overdue_behavior** 是同端点区分产品的唯一位，入参 name+idCard+mobile
// 三要素均必填；响应 resp_data 为357 个 xyp_* 履约/逾期因子 (含 xyp_model_score_
// high/mid/low 三个星耀Pro 评分，范围 [350,950]、未命中 -1) 的扁平富对象，整体序列化经
// result.range 透出；SW0000 查得计费 / SW0002 查无不计费 / **SW0001 认证失败本产品标
// 【不收费】→ 按上游侧错误处理** (同 snhmd，与 dtjd 相反)，见 upstream/manyoverdue.go)。
// 注：Versions 是「路由」维度；存储/license 按「域」(Domains) 聚合——v8/v9 同属
// v8v9 域共用一套 license，其余路由各自独立成域 (见 RouteDomain)。跨域使用 license
// 一律鉴权失败 (505004 账户信息不存在)。
var Versions = []string{"x1", "v9", "v8", "zlf", "blk", "rlbd1", "rlbd2", "sfzhy", "xfjy", "tsfx", "lxf", "grgjj", "grsb", "sfsm", "sffx", "dtjd", "snhmd", "dtly"}

// Domains is the canonical ordered list of license 域 (存储边界)。每个域独占一套
// DB + Redis + license 表；v8/v9 合并为 v8v9 域共用同一 license，其余域名即路由名。
var Domains = []string{"x1", "v8v9", "zlf", "blk", "rlbd1", "rlbd2", "sfzhy", "xfjy", "tsfx", "lxf", "grgjj", "grsb", "sfsm", "sffx", "dtjd", "snhmd", "dtly"}

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
	case "grgjj":
		return "y89grgjj"
	case "grsb":
		return "y890grsb"
	case "sfsm":
		return "y890sfsm"
	case "sffx":
		return "y890sffx"
	case "dtjd":
		return "y890dtjd"
	case "snhmd":
		return "y89snhmd"
	case "dtly":
		return "y890dtly"
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
