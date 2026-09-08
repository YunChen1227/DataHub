package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/datahub/relay/internal/domain/model"
)

// IDRiskAPIKey is the fixed product code for 身份风险V107 (应诺尔 PDF §1.4 apiKey
// 「固定：idRiskTagV107」)。
const IDRiskAPIKey = "idRiskTagV107"

// 身份风险V107 busiCode (应诺尔 PDF §1.6 busiCode 表)。仅 10/1000 是业务结果，
// 其余 (1001~1009) 是我方在应诺尔侧的账户/参数/系统问题，归为上游侧错误。
const (
	idriskBusiSuccess  = 10   // 查询成功【计费】
	idriskBusiNotFound = 1000 // 数据未查得【计费】——**查无也收费**, 见 billing.billNotFoundRoutes
)

// IDRiskConfig holds the 身份风险V107 endpoint + 我方在应诺尔侧的凭证。与 gama
// (x1 伽马分层分) / blacklist (blk 黑名单因子V35) 同为应诺尔 enol 端点
// (POST /enol/api/v1/doCheck)，仅 apiKey / 业务参数集合 / 响应体不同。
//
// 地址 (应诺尔 PDF §2.1「https://{{域名}}/enol/api/v1/doCheck」)：
//   - 生产域名：enol.com.cn      → https://enol.com.cn/enol/api/v1/doCheck
//   - 测试域名：testenol.cn      → https://testenol.cn/enol/api/v1/doCheck
//
// 文档自相矛盾：§3.2 Demo 的 url 写成 https://api.enolfax.com/enol/api/v1/doCheck，
// 与 §2.1 的域名表不一致 (且 Demo 的 apiKey 还写着另一个产品 loanRiskTagV23_2_1)。
// 故地址一律由配置 baseURL 注入、不在代码里写死，联调时以上游实际给的域名为准。
type IDRiskConfig struct {
	BaseURL        string // https://{域名}/enol/api/v1/doCheck
	AppID          string
	Secret         string
	APIKey         string // 默认 idRiskTagV107
	EncryptionType int    // 1=明文, 2=MD5(默认): name/idCard 传 MD5 摘要值
}

// IDRiskClient implements port.UpstreamPort for the 身份风险V107 provider.
//
// 与 GamaClient/BlacklistClient 同信封 (gamaEnvelope) 同 MD5 加签 (signGama:
// body 参数按键名 ASCII 升序拼 "键值"、末尾接 secret、取 MD5 小写 hex，
// 应诺尔 PDF §3.1)。区别：
//   - apiKey = idRiskTagV107；
//   - 业务参数只有 name + idCard 两项必填 (§2.2 body 参数表)，**没有 mobile**——
//     不沿用其它路由的三要素默认集合；
//   - 响应 result 为富对象 {"detail":["A0"]} (风险类型码 A~P + 风险周期码 0~6)，
//     原样序列化成 JSON 字符串经下游 result.range 透出 (blk 模式)。
//
// 未实现的上游参数：tradeNo (§2.2「业务单号」，标**选填**)。本网关自带内部幂等
// 流水号 reqid，下游 x1 契约里没有业务单号字段可透传 (orchestrator 调
// quota.Begin 时 tradeNo 恒为空串)，故与 gama/blacklist 一致不注入该字段——
// sign 严格只对实际发出的 body 参数加签。
//
// 本产品文档只定义了一个业务接口 (§2.1 doCheck)，已全部实现，无遗漏接口。
type IDRiskClient struct {
	cfg  IDRiskConfig
	http *http.Client
}

// NewIDRisk builds a 身份风险V107 client.
func NewIDRisk(cfg IDRiskConfig, httpClient *http.Client) *IDRiskClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if cfg.APIKey == "" {
		cfg.APIKey = IDRiskAPIKey
	}
	if cfg.EncryptionType == 0 {
		cfg.EncryptionType = 2
	}
	return &IDRiskClient{cfg: cfg, http: httpClient}
}

// idriskResponse mirrors 应诺尔 PDF §1.5 响应结构 (与 gama/blacklist 同形)。
type idriskResponse struct {
	Code  int    `json:"code"`  // 0 成功 / -1 响应异常 (§1.6)
	Msg   string `json:"msg"`   // 返回文字描述
	SeqNo string `json:"seqNo"` // 交易流水号——唯一的上游标识, 落审计对账用
	Data  struct {
		BusiCode int             `json:"busiCode"`
		BusiMsg  string          `json:"busiMsg"`
		Result   json.RawMessage `json:"result"` // 富对象 {"detail":["A0"]}
	} `json:"data"`
}

// Query performs the signed POST to 应诺尔 doCheck (身份风险V107) and normalizes the
// response: busiCode 10 → "001" 查得 (Range = result 富对象 JSON 字符串),
// 1000 → "999" 查无 (**该路由查无也计费**, 由 billing.TableFor("sffx") 决定,
// 归一码本身仍如实为 999), 其余 → error (上游侧异常, 触发 re-query/对账, 不计费)。
func (c *IDRiskClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	// body 只放本产品文档 §2.2 定义的业务参数 (非空才放)；encryptionType=2 时对
	// PII 取 MD5 摘要 (§1.4 参数加密类型)。name/idCard 由网关校验器前置保证非空。
	body := map[string]string{}
	if req.Name != "" {
		body["name"] = encodePII(req.Name, c.cfg.EncryptionType)
	}
	if req.IDCard != "" {
		body["idCard"] = encodePII(req.IDCard, c.cfg.EncryptionType)
	}
	env := gamaEnvelope{
		EncryptionType: c.cfg.EncryptionType,
		AppID:          c.cfg.AppID,
		Sign:           signGama(body, c.cfg.Secret),
		APIKey:         c.cfg.APIKey,
		Body:           body,
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal idrisk request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build idrisk request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json; charset=utf-8")

	slog.Debug("idrisk request",
		"url", c.cfg.BaseURL,
		"appId", c.cfg.AppID,
		"apiKey", c.cfg.APIKey,
		"encryptionType", c.cfg.EncryptionType,
		"sign", env.Sign,
		"reqid", req.Reqid,
	)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("idrisk call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read idrisk body: %w", err)
	}
	slog.Debug("idrisk response", "status", resp.StatusCode, "raw", string(raw))

	var ir idriskResponse
	if err := json.Unmarshal(raw, &ir); err != nil {
		return nil, fmt.Errorf("decode idrisk body: %w", err)
	}
	// 全局返回码 (§1.6): 0 成功, -1 响应异常。非 0 一律上游侧错误, 带 seqNo 落审计。
	if ir.Code != 0 {
		return nil, busiErrf(ir.Code, ir.Msg, ir.SeqNo, ir.SeqNo)
	}

	switch ir.Data.BusiCode {
	case idriskBusiSuccess:
		return &model.UpstreamResult{
			Code:  "001",
			Msg:   "成功",
			UID:   ir.SeqNo,
			Reqid: req.Reqid,
			LogID: ir.SeqNo, // 只有 seqNo(交易流水号)一个上游标识，UID/LogID 同填供后台对账
			Range: sanitizeRange(ir.Data.Result),
		}, nil
	case idriskBusiNotFound:
		return &model.UpstreamResult{
			Code:  "999",
			Msg:   "未查得",
			UID:   ir.SeqNo,
			Reqid: req.Reqid,
			LogID: ir.SeqNo,
		}, nil
	default:
		// 1001 账户余额不足 / 1002 账户信息不存在 / 1003 appId 异常 / 1004 产品编号
		// 异常 / 1005 账号信息异常 / 1006 透支余额已达上限 / 1007 数据请求异常 /
		// 1009 服务尚未开通 (§1.6) 均为我方在应诺尔侧的账户/参数/系统问题，视为上游
		// 侧错误：不计费，交由 orchestrator 走 re-query/对账兜底。失败也带上游
		// seqNo(交易流水号)落审计供对账追查。
		return nil, busiErrf(ir.Data.BusiCode, ir.Data.BusiMsg, ir.SeqNo, ir.SeqNo)
	}
}

// Requery: 身份风险V107 doCheck 以 tradeNo 幂等, 真正的对账查询接口待联调。在此之前
// 返回 Reachable=false, 记录保持 PENDING 由对账兜底 (与 gama/blacklist 一致)。
func (c *IDRiskClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}
