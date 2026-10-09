package upstream

import (
	"context"
	"net/http"

	"github.com/datahub/relay/internal/domain/model"
)

// 依据文档：借贷申请意向V22【TD】.pdf (应诺尔产品接口文档「多头申请变量 TD 版」)。

// LoanIntentAPIKey is the fixed product code (PDF §1.4 apiKey「固定：loanRiskTagV22」)。
const LoanIntentAPIKey = "loanRiskTagV22"

// LoanIntentConfig holds the 借贷申请意向V22【TD】 endpoint + 我方在应诺尔侧的凭证。
//
// 地址 (PDF §2.1「https://{{域名}}/enol/api/v1/doCheck」)：
//   - 生产域名：api.enolfax.com → https://api.enolfax.com/enol/api/v1/doCheck
//   - 测试域名：testenol.cn     → https://testenol.cn/enol/api/v1/doCheck
//
// 注意：本产品 PDF §1.4 请求结构表**没有 encryptionType 字段** (请求示例也不带)，
// 故不提供该配置项，信封里也不发送；PII 明文。
type LoanIntentConfig struct {
	BaseURL string // https://{域名}/enol/api/v1/doCheck
	AppID   string
	Secret  string
	APIKey  string // 默认 loanRiskTagV22
}

// LoanIntentClient implements port.UpstreamPort for 借贷申请意向V22【TD】.
//
// 与 x1/blk/sffx 同端点同 MD5 加签 (signGama：body 非空参数按键名 ASCII 升序拼
// "键值"、末尾接 secret、取 MD5 小写 hex，PDF §3.1)。
// 文档自相矛盾：§3.1 第 2 条给的待签名字符串示例「name张三mobile…idCard…secret」
// 并非 ASCII 升序，而同节第 1 条规则与 Java/C#/PHP/python 示例代码都先排序再拼接——
// 以规则与示例代码为准 (升序)。
//
// 业务参数 (§2.2)：name/idCard/mobile 必填，tradeNo 选填 (不注入，见 enolThreeFactorBody)。
// 响应 result 为 TC_* 多头申请变量富对象，整体经下游 result.range 透出。
// 本产品文档只定义了一个业务接口 (§2.1 doCheck)，已全部实现。
type LoanIntentClient struct {
	cfg  LoanIntentConfig
	http *http.Client
}

// NewLoanIntent builds a 借贷申请意向V22【TD】 client.
func NewLoanIntent(cfg LoanIntentConfig, httpClient *http.Client) *LoanIntentClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if cfg.APIKey == "" {
		cfg.APIKey = LoanIntentAPIKey
	}
	return &LoanIntentClient{cfg: cfg, http: httpClient}
}

// Query normalizes per PDF §1.6 busiCode 表：
//
//	10   查得结果【计费】 → 001 (Range = result 富对象 JSON)
//	1000 未查得 (无计费标注) → 999，不计费
//	1001~1009 / 未知码 / 全局 code=-1 → 上游侧错误，不计费
func (c *LoanIntentClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	er, err := enolDoCheck(ctx, c.http, c.cfg.BaseURL, c.cfg.AppID, c.cfg.Secret, c.cfg.APIKey,
		0, enolThreeFactorBody(req), req.Reqid, "loanintent")
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
func (c *LoanIntentClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}
