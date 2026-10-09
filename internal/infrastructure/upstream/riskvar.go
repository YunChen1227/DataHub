package upstream

import (
	"context"
	"net/http"

	"github.com/datahub/relay/internal/domain/model"
)

// 依据文档：风险变量V11.pdf (应诺尔产品接口文档「风险变量 V11」)。

// RiskVarAPIKey is the fixed product code (PDF §1.4 apiKey「固定：loanRiskTagV11」)。
const RiskVarAPIKey = "loanRiskTagV11"

// RiskVarConfig holds the 风险变量V11 endpoint + 我方在应诺尔侧的凭证。
//
// 地址 (PDF §2.1「https://{{域名}}/enol/api/v1/doCheck」)：
//   - 生产域名：enol.com.cn  → https://enol.com.cn/enol/api/v1/doCheck
//     (注意与兄弟产品 TD/信用全景的 api.enolfax.com 不同，以本产品文档为准)
//   - 测试域名：testenol.cn  → https://testenol.cn/enol/api/v1/doCheck
//
// encryptionType 固定 1：PDF §1.4 只定义了「1:明文【默认】」，不提供 MD5 模式。
type RiskVarConfig struct {
	BaseURL string // https://{域名}/enol/api/v1/doCheck
	AppID   string
	Secret  string
	APIKey  string // 默认 loanRiskTagV11
}

// RiskVarClient implements port.UpstreamPort for 风险变量V11.
//
// 同 enol 信封 + signGama MD5 加签 (PDF §3.1)。业务参数 (§2.2)：idCard/name/mobile
// 必填，tradeNo 选填 (不注入)。响应 result 为 als_* 多头申请因子 + Rule_final_decision
// (Accept/Reject/Review) / Rule_final_weight / Rule_name_* / Rule_weight_* 决策字段的
// 富对象，整体经下游 result.range 透出。本产品文档只定义了一个业务接口 (§2.1 doCheck)，
// 已全部实现。
type RiskVarClient struct {
	cfg  RiskVarConfig
	http *http.Client
}

// NewRiskVar builds a 风险变量V11 client.
func NewRiskVar(cfg RiskVarConfig, httpClient *http.Client) *RiskVarClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if cfg.APIKey == "" {
		cfg.APIKey = RiskVarAPIKey
	}
	return &RiskVarClient{cfg: cfg, http: httpClient}
}

// Query normalizes per PDF §1.6 busiCode 表：
//
//	10   查询成功【计费】     → 001 (Range = result 富对象 JSON)
//	1000 数据未查得【计费】   → 999 (**查无也计费**，由 billing.TableFor("fxbl") 决定，
//	                            归一码本身仍如实为 999)
//	1001~1009 / 未知码 / 全局 code=-1 → 上游侧错误，不计费
func (c *RiskVarClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	er, err := enolDoCheck(ctx, c.http, c.cfg.BaseURL, c.cfg.AppID, c.cfg.Secret, c.cfg.APIKey,
		1, enolThreeFactorBody(req), req.Reqid, "riskvar")
	if err != nil {
		return nil, err
	}
	switch er.Data.BusiCode {
	case enolBusiSuccess:
		return &model.UpstreamResult{
			Code:  "001",
			Msg:   "成功",
			UID:   er.SeqNo,
			Reqid: req.Reqid,
			LogID: er.SeqNo, // 只有 seqNo(交易流水号)一个上游标识，UID/LogID 同填供后台对账
			Range: sanitizeRange(er.Data.Result),
		}, nil
	case enolBusiNotFound:
		return &model.UpstreamResult{
			Code:  "999",
			Msg:   "未查得",
			UID:   er.SeqNo,
			Reqid: req.Reqid,
			LogID: er.SeqNo,
		}, nil
	default:
		// 1001 账户余额不足 / 1002 账户信息不存在 / 1003 appId 异常 / 1004 产品编号异常
		// / 1005 账号信息异常 / 1006 透支余额已达上限 / 1007 数据请求异常 / 1009 服务
		// 尚未开通 (§1.6)：上游侧错误，不计费，带 seqNo 落审计供对账追查。
		return nil, busiErrf(er.Data.BusiCode, er.Data.BusiMsg, er.SeqNo, er.SeqNo)
	}
}

// Requery: 对账查询接口文档未定义，返回 Reachable=false，台账保持 PENDING 由对账兜底。
func (c *RiskVarClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}
