package upstream

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/datahub/relay/internal/domain/model"
)

// 多头履约行为 (dtly) 默认业务参数 (docs/上游对接_多头履约行为_守信_钉钉文档整理.md §2.5)。
// 与 zlf 租赁分、dtjd 多头借贷行为、snhmd 司南黑名单同一供应商 (守信 shouxin168)、
// 同一端点、同一信封，**仅 mode 与响应主体不同**——service 与 dtjd/snhmd 相同
// (financial_rent_service)，mode 是同端点多产品的唯一区分位，抄错会**静默**查到另一个
// 产品的数据 (不报错，更难发现)。
const (
	manyOverdueDefaultService = "financial_rent_service"
	manyOverdueDefaultMode    = "mode_many_overdue_behavior"
)

// 多头履约行为响应状态码 (文档 §4「响应状态码对照表」整表 19 码逐码落地，含「是否收费」列)：
//
//	SW0000 认证成功         收费    → 001 查得，计费
//	SW0001 认证失败         不收费  → error (见下 ★：与 dtjd 口径相反)
//	SW0002 查询无记录       不收费  → 999 查无，不计费
//	SW0003 通道超时或异常   不收费  → error
//	SW0017 biz_data参数错误 不收费  → error
//	SW0018 参数错误         不收费  → error
//	SW0030 签名为空         不收费  → error
//	SW0031 客户公钥为空     不收费  → error
//	SW0032 验签失败         不收费  → error
//	SW0033 验签错误         不收费  → error
//	SW0034 解密失败         不收费  → error
//	SW0040 产品未开通       不收费  → error
//	SW0041 调用量已达上限   不收费  → error
//	SW0042 账户余额不足     不收费  → error
//	SW1009 姓名格式有误     不收费  → error
//	SW1010 身份证号格式有误 不收费  → error
//	SW1011 卡号格式有误     不收费  → error
//	SW1012 手机号格式有误   不收费  → error
//	SW9999 系统错误         不收费  → error
//
// 另：SW0000 但 resp_data 空/无法解析 → error (拿不到数据不得计费)；文档未列的新码
// (开放式约定) → error。
//
// ★ **SW0001「认证失败」本产品文档标【不收费】**，故按最朴素的「上游侧错误」处理即可：
// 返回 busiErr，不计费，走复查/对账兜底 (与 snhmd 同，**与 dtjd 相反**——dtjd 那份文档
// 同一个 SW0001 标的是【收费】，因此它被迫归一成 999 + slog.Warn 人工对账，见
// multiloan.go 头部 ★)。同供应商、同端点、同一个状态码，不同产品的计费列相反：这正是
// 「逐产品看文档，禁止复用兄弟路由的结论」。
// 也因此本路由的 999 只来自 SW0002 (不收费)，故 billing.billNotFoundRoutes **不登记** dtly。
const (
	manyOverdueCodeSuccess  = "SW0000" // 认证成功 (收费)
	manyOverdueCodeNotFound = "SW0002" // 查询无记录 (不收费)
)

// ManyOverdueConfig holds the 多头履约行为 (守信 shouxin168) 上游 endpoint + 凭证。
// AESKey/InstitutionID 由上游商务分配 (**可能与 zlf/dtjd/snhmd 不同，不要假定共用**)；
// LicenseURL/LicenseType 为我方启动时上传授权书到 OSS 后缓存的固定值，所有查询复用
// (与 rental/multiloan/compassblack 共用 cmd/relay/main.go 的 uploadAuthLicense)。
type ManyOverdueConfig struct {
	BaseURL       string
	InstitutionID string
	AESKey        string
	Service       string // 默认 financial_rent_service
	Mode          string // 默认 mode_many_overdue_behavior
	LicenseURL    string // 授权书 OSS 地址 (启动上传后缓存)
	LicenseType   int    // 0:图片(jpg/jpeg/png/bmp) 1:pdf
}

// ManyOverdueClient implements port.UpstreamPort for the 多头履约行为 provider:
// 业务数据 JSON 经 AES/ECB/PKCS5Padding + Base64 得 biz_data, 与 institution_id 一起以
// form 表单 POST 提交; 响应归一化为 ("001" 查得 / "999" 查无 / error)。
type ManyOverdueClient struct {
	cfg    ManyOverdueConfig
	key    []byte // 解出的 AES 密钥 (16/24/32 字节)
	keyErr error  // 密钥形态非法时的原因, 每次 Query 直接返回, 不静默降级
	http   *http.Client
}

// NewManyOverdue builds a 多头履约行为 upstream client。构造期就把 AES 密钥形态验算出来
// (见 manyOverdueAESKey)：非法立刻记 error 并在启动日志里告警，而不是等到 aes.NewCipher
// 报 invalid key size——那种错只在运行时的加密日志里出现，容易"上线后一个请求都没发出去"。
func NewManyOverdue(cfg ManyOverdueConfig, httpClient *http.Client) *ManyOverdueClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if cfg.Service == "" {
		cfg.Service = manyOverdueDefaultService
	}
	if cfg.Mode == "" {
		cfg.Mode = manyOverdueDefaultMode
	}
	key, err := manyOverdueAESKey(cfg.AESKey)
	if err != nil {
		slog.Warn("manyoverdue AES 密钥形态非法, 该路由的查询会直接失败", "err", err)
	}
	return &ManyOverdueClient{cfg: cfg, key: key, keyErr: err, http: httpClient}
}

// manyOverdueAESKey 推导 AES 密钥字节。AES 只接受 16/24/32 字节，上游给的凭证串可能是
// 原始 ASCII (兄弟产品 zlf 的沙箱密钥 Q0ymUIe1t26ZfG7s 就是 16 字节 ASCII)、也可能是
// hex 或 Base64 文本 (如 64 个十六进制字符 = 32 字节 AES-256，必须 hex 解码，**不是**
// 64 字节 ASCII)。故依次尝试「原始字节 → hex 解码 → Base64 解码」，取第一个落在合法
// 长度上的形态；全都不合法**立刻报错**，绝不把非法长度交给 aes.NewCipher。
// 长度恰为 16/24/32 的串一律按原始字节采用 (不去猜它是不是 hex)。
func manyOverdueAESKey(key string) ([]byte, error) {
	if key == "" {
		return nil, fmt.Errorf("manyoverdue aesKey 为空 (请在 config 的 versions.dtly.upstreams[].aesKey 配置上游分配的密钥)")
	}
	if raw := []byte(key); validAESKeyLen(len(raw)) {
		return raw, nil
	}
	if b, err := hex.DecodeString(key); err == nil && validAESKeyLen(len(b)) {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(key); err == nil && validAESKeyLen(len(b)) {
		return b, nil
	}
	return nil, fmt.Errorf("manyoverdue aesKey 长度非法: 原始 %d 字节, hex/Base64 解码后也不是 16/24/32 字节", len(key))
}

// manyOverdueBizData is the 明文业务数据 (文档 §2.5), JSON 化后做 AES 加密。
// 七个字段上游**全部标必传**，一个都不能少；字段名逐字照抄——ident_number 是下划线、
// licenseUrl/licenseType 是小驼峰，上游原样混用，不许"纠正"成统一风格。
type manyOverdueBizData struct {
	Name        string `json:"name"`
	IdentNumber string `json:"ident_number"`
	Phone       string `json:"phone"`
	Service     string `json:"service"`
	Mode        string `json:"mode"`
	LicenseURL  string `json:"licenseUrl"`
	LicenseType int    `json:"licenseType"`
}

// manyOverdueResponse is the 上游响应外层结构 (文档 §3.2.1)。resp_data 在字段表里标注为
// String、但 §3.2.3 成功示例里是对象——文档自相矛盾，故用 RawMessage 延迟解析并两种
// 形态都兼容 (见 normalizeManyOverdueData)，不挑一个赌。timestamp 同样标 String 而
// 示例是数字，用 RawMessage 收下、不参与业务判断。
type manyOverdueResponse struct {
	RespCode  string          `json:"resp_code"`
	RespMsg   string          `json:"resp_msg"`
	RespOrder string          `json:"resp_order"`
	Timestamp json.RawMessage `json:"timestamp"`
	RespData  json.RawMessage `json:"resp_data"`
}

// Query performs the AES-encrypted form POST to 多头履约行为 and normalizes the
// response per the §4 码表 (见文件头常量块)。
func (c *ManyOverdueClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	if c.keyErr != nil {
		return nil, c.keyErr
	}
	biz := manyOverdueBizData{
		Name:        req.Name,
		IdentNumber: req.IDCard,
		Phone:       req.Mobile,
		Service:     c.cfg.Service,
		Mode:        c.cfg.Mode,
		LicenseURL:  c.cfg.LicenseURL,
		LicenseType: c.cfg.LicenseType,
	}
	plain, err := json.Marshal(biz)
	if err != nil {
		return nil, fmt.Errorf("marshal manyoverdue biz_data: %w", err)
	}
	cipher, err := aesECBEncryptBase64(plain, c.key)
	if err != nil {
		return nil, fmt.Errorf("encrypt manyoverdue biz_data: %w", err)
	}

	form := url.Values{}
	form.Set("institution_id", c.cfg.InstitutionID)
	form.Set("biz_data", cipher)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build manyoverdue request: %w", err)
	}
	// 文档 §2.2 给了 multipart/form-data 与 x-www-form-urlencoded 两种格式, 取后者
	// (与兄弟产品 zlf 实测通过的形态一致)。
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Accept", "application/json;charset=utf-8")

	// 出站请求日志 (不含 AES 密钥与明文 PII), 便于与上游逐字段核对。
	slog.Debug("manyoverdue request",
		"url", c.cfg.BaseURL,
		"institutionId", c.cfg.InstitutionID,
		"service", c.cfg.Service,
		"mode", c.cfg.Mode,
		"reqid", req.Reqid,
	)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("manyoverdue call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read manyoverdue body: %w", err)
	}
	slog.Debug("manyoverdue response", "status", resp.StatusCode, "raw", string(raw))

	var mr manyOverdueResponse
	if err := json.Unmarshal(raw, &mr); err != nil {
		return nil, fmt.Errorf("decode manyoverdue body: %w", err)
	}

	switch mr.RespCode {
	case manyOverdueCodeSuccess:
		// 成功码但主体拿不到/解不开 → 一律 error 不计费: 我方拿不到数据就不该向客户
		// 收钱 (billing-scope skill 第四节第 5 条)。
		rng := normalizeManyOverdueData(mr.RespData)
		if rng == "" {
			return nil, busiErr(mr.RespCode, "上游返回 "+manyOverdueCodeSuccess+" 但 resp_data 为空或无法解析", mr.RespOrder, mr.RespOrder)
		}
		// 注意: 文档 §3.2.3 的成功示例里绝大多数因子是空串、三个模型分是 "-1"(未命中),
		// 这同样是**有效结论**, 属查得, 必须计费; 不得因为值大多为空就改判 999
		// (与 blk/snhmd 同理)。只有 resp_data 整体缺失/null/空对象才算拿不到数据。
		return &model.UpstreamResult{
			Code:  "001",
			Msg:   "成功",
			UID:   mr.RespOrder,
			Reqid: req.Reqid,
			LogID: mr.RespOrder, // 只有 resp_order(订单号)一个上游标识, UID/LogID 同填供后台对账
			Range: rng,
		}, nil
	case manyOverdueCodeNotFound:
		return &model.UpstreamResult{
			Code:  "999",
			Msg:   "查无结果",
			UID:   mr.RespOrder,
			Reqid: req.Reqid,
			LogID: mr.RespOrder,
		}, nil
	default:
		// SW0001 (本产品标不收费) / SW0003 / SW001x / SW003x / SW004x / SW10xx / SW9999
		// 与文档未列的新码均视为上游侧错误: 不计费, 走复查/对账兜底。失败也带上游
		// resp_order(订单号) 落审计供对账追查 (禁止用裸 fmt.Errorf 把标识丢进字符串)。
		return nil, busiErr(mr.RespCode, mr.RespMsg, mr.RespOrder, mr.RespOrder)
	}
}

// Requery: 多头履约行为暂未提供对账查询接口, 返回 Reachable=false, 记录保持 PENDING
// 由复查/对账兜底 (与 rental/multiloan/compassblack/gama/income 一致)。
func (c *ManyOverdueClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}

// normalizeManyOverdueData 把 resp_data 归一成一个紧凑 JSON 字符串, 供下游 result.range
// 整体透出 (与 blk/sffx/dtjd/snhmd 同口径)。主体是357 个扁平因子: xyp_cpl00xx 多头
// 履约/逾期因子、xyp_t01/t02/t03td/t04 分层履约因子、xyp_model_score_high/mid/low 三个
// 星耀Pro 评分 ([350,950], 未命中 -1)、xyp_var1..10 预留字段——**不做字段白名单、不改写
// 字段名、不解释区间语义** (绝大多数因子是区间化档位, 映射表见文档附录 A), 上游新增因子
// 无需改代码即可透传。
//
// 文档 §3.2.1 标 resp_data 为 String、§3.2.3 示例给的是对象, 两种形态都兼容：是 JSON
// 字符串时先解一层引号再压缩, 是对象时直接压缩。空对象 {} / 空串 / null 视为拿不到
// 数据, 返回空串让调用方按「不计费」处理。
func normalizeManyOverdueData(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	// String 形态: 先解开外层 JSON 字符串, 再按其内容是否为非空 JSON 对象判断。
	if strings.HasPrefix(trimmed, `"`) {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return ""
		}
		return compactManyOverdueObject(json.RawMessage(strings.TrimSpace(s)))
	}
	return compactManyOverdueObject(raw)
}

// compactManyOverdueObject 压缩 JSON 对象并拒绝空对象/非对象 (上游给了成功码但没有因子)。
func compactManyOverdueObject(raw json.RawMessage) string {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil || len(probe) == 0 {
		return ""
	}
	return compactJSON(raw)
}
