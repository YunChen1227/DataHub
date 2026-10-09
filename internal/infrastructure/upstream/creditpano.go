package upstream

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/datahub/relay/internal/domain/model"
)

// 依据文档：信用全景V21.pdf (应诺尔产品接口文档「信用全景 V21」)。

// CreditPanoAPIKey is the fixed product code (PDF §1.4 apiKey「固定：loanRiskTagV21」)。
// 文档自相矛盾：§2.2 请求示例写成 " loanRiskTagV21" (带前导空格)，以 §1.4 参数表
// 的无空格值为准。
const CreditPanoAPIKey = "loanRiskTagV21"

// CreditPanoConfig holds the 信用全景V21 endpoint + 我方在应诺尔侧的凭证。
//
// 地址 (PDF §2.1「https://{{域名}}/enol/api/v1/doCheck」)：
//   - 生产域名：api.enolfax.com → https://api.enolfax.com/enol/api/v1/doCheck
//   - 测试域名：testenol.cn     → https://testenol.cn/enol/api/v1/doCheck
//
// encryptionType 固定 1：PDF §1.4 只定义了「1:明文【默认】」，不提供 MD5 模式。
type CreditPanoConfig struct {
	BaseURL string // https://{域名}/enol/api/v1/doCheck
	AppID   string
	Secret  string
	APIKey  string // 默认 loanRiskTagV21
}

// CreditPanoClient implements port.UpstreamPort for 信用全景V21.
//
// 同 enol 信封 + signGama MD5 加签 (PDF §3.1)。业务参数 (§2.2)：idCard/name/mobile
// 必填，tradeNo 选填 (不注入)。响应 result 为 xyp_* 信用/履约因子 + 星耀Pro 三个评分
// (xyp_model_score_high/mid/low) + fee_status 的扁平富对象，整体经下游 result.range
// 透出。本产品文档只定义了一个业务接口 (§2.1 doCheck)，已全部实现。
type CreditPanoClient struct {
	cfg  CreditPanoConfig
	http *http.Client
}

// NewCreditPano builds a 信用全景V21 client.
func NewCreditPano(cfg CreditPanoConfig, httpClient *http.Client) *CreditPanoClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if cfg.APIKey == "" {
		cfg.APIKey = CreditPanoAPIKey
	}
	return &CreditPanoClient{cfg: cfg, http: httpClient}
}

// Query normalizes per PDF §1.6 busiCode 表 + result.fee_status「收费状态码 1-收费；
// 0-不收费」。fee_status 是上游逐笔下发的计费标志，**以它为计费唯一判据** (billing-scope
// 铁律 3)：
//
//	10 且 fee_status=1    → 001 查得计费 (Range = result 富对象 JSON)
//	10 且 fee_status=0    → 999 不计费 (上游给了数据却明说不收费；落 warn 供对账)
//	10 且 fee_status 缺失 → 001 (按「10 查询成功【计费】」；落 warn，上游口径可能变了)
//	1000 数据未查得 (无计费标注) → 999 不计费
//	1001~1009 / 未知码 / 全局 code=-1 → 上游侧错误，不计费
func (c *CreditPanoClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	er, err := enolDoCheck(ctx, c.http, c.cfg.BaseURL, c.cfg.AppID, c.cfg.Secret, c.cfg.APIKey,
		1, enolThreeFactorBody(req), req.Reqid, "creditpano")
	if err != nil {
		return nil, err
	}
	switch er.Data.BusiCode {
	case enolBusiSuccess:
		fee, ok := creditPanoFeeStatus(er.Data.Result)
		switch {
		case ok && fee == "0":
			slog.Warn("creditpano busiCode=10 但 fee_status=0，按不计费归一为 999",
				"seqNo", er.SeqNo, "reqid", req.Reqid)
			return &model.UpstreamResult{
				Code:  "999",
				Msg:   "未查得",
				UID:   er.SeqNo,
				Reqid: req.Reqid,
				LogID: er.SeqNo,
			}, nil
		case !ok || fee != "1":
			slog.Warn("creditpano busiCode=10 但 fee_status 缺失或非 0/1，按查得计费处理，请人工对账",
				"feeStatus", fee, "seqNo", er.SeqNo, "reqid", req.Reqid)
		}
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

// creditPanoFeeStatus 取 result.fee_status。文档类型为 String ("1"/"0")，数字形态也兼容。
func creditPanoFeeStatus(raw json.RawMessage) (string, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", false
	}
	v, ok := m["fee_status"]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		return strings.TrimSpace(s), true
	}
	var n json.Number
	if err := json.Unmarshal(v, &n); err == nil {
		return n.String(), true
	}
	return "", false
}

// Requery: 对账查询接口文档未定义，返回 Reachable=false，台账保持 PENDING 由对账兜底。
func (c *CreditPanoClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}
