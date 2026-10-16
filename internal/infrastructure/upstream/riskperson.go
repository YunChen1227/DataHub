package upstream

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/datahub/relay/internal/domain/model"
)

// 依据文档：docs/风险人员V108.pdf (应诺尔产品接口文档「风险人员V108」)。

// RiskPersonAPIKey is the fixed product code (PDF §1.4 apiKey「固定：idRiskTagV108」)。
const RiskPersonAPIKey = "idRiskTagV108"

// riskPersonCodeLen 是 resultCode 的位数 (PDF §2.3「返回码值为 25 位」，从左到右
// 第 1~25 位依次为 是否涉刑 / 近5年涉破坏金融管理秩序案 / … / 是否正常)。
const riskPersonCodeLen = 25

// RiskPersonConfig holds the 风险人员V108 endpoint + 我方在应诺尔侧的凭证。
//
// 地址 (PDF §2.1「https://{{域名}}/enol/api/v1/doCheck」)：
//   - 生产域名：enol.com.cn  → https://enol.com.cn/enol/api/v1/doCheck
//   - 测试域名：testenol.cn  → https://testenol.cn/enol/api/v1/doCheck
//
// 文档自相矛盾：§3.2 Demo 的 url 是 https://api.enolfax.com/enol/api/v1/doCheck、
// apiKey 写成另一产品 loanRiskTagV23_2_1、encryptionType 注释列了 1~4 四种——均与
// §1.4/§2.1 不一致 (同 sffx 身份风险V107 文档)。故地址由配置 baseURL 注入，apiKey 与
// 加密类型以 §1.4 表为准 (1 明文【默认】/ 2 md5)。
type RiskPersonConfig struct {
	BaseURL        string // https://{域名}/enol/api/v1/doCheck
	AppID          string
	Secret         string
	APIKey         string // 默认 idRiskTagV108
	EncryptionType int    // 1=明文(默认, §1.4【默认】), 2=MD5: name/idCard 传 MD5 摘要值
}

// RiskPersonClient implements port.UpstreamPort for 风险人员V108.
//
// 同 enol 信封 + signGama MD5 加签 (PDF §3.1)。业务参数 (§2.2 body 参数表)：
// name/idCard 必填、tradeNo 选填——**没有 mobile**，与兄弟产品 sffx 身份风险V107
// 同口径。tradeNo 不注入：下游 x1 契约无业务单号字段可透传，sign 只覆盖实发参数。
// 响应 result 为 {"resultCode":"<25 位 0/1>"} (线上实为 riskCode，见 normalizeRiskPersonResult)，
// 整体经下游 result.range 透出。
// 本产品文档只定义了一个业务接口 (§2.1 doCheck)，已全部实现。
type RiskPersonClient struct {
	cfg  RiskPersonConfig
	http *http.Client
}

// NewRiskPerson builds a 风险人员V108 client.
func NewRiskPerson(cfg RiskPersonConfig, httpClient *http.Client) *RiskPersonClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if cfg.APIKey == "" {
		cfg.APIKey = RiskPersonAPIKey
	}
	if cfg.EncryptionType == 0 {
		cfg.EncryptionType = 1
	}
	return &RiskPersonClient{cfg: cfg, http: httpClient}
}

// 文档与线上不一致：PDF §2.3 响应示例与 result 参数表的键是 resultCode，而 2026-10-16
// 生产实测返回 {"riskCode":"0000000000000000000000001"}。两种都认；对外 range 统一输出
// resultCode (对外手册 3.1.5 的契约)，其余字段原样透出。
const (
	riskPersonCodeKey       = "resultCode"
	riskPersonCodeKeyOnWire = "riskCode"
)

// normalizeRiskPersonResult 取出命中码并返回对外透出的 result：键已是 resultCode 时原样
// 返回 raw (保字段顺序)；只有 riskCode 时改名为 resultCode 后重新序列化。
func normalizeRiskPersonResult(raw json.RawMessage) (string, json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return "", nil, false
	}
	out := raw
	v, ok := m[riskPersonCodeKey]
	if !ok {
		if v, ok = m[riskPersonCodeKeyOnWire]; !ok {
			return "", nil, false
		}
		delete(m, riskPersonCodeKeyOnWire)
		m[riskPersonCodeKey] = v
		renamed, err := json.Marshal(m)
		if err != nil {
			return "", nil, false
		}
		out = renamed
	}
	var code string
	if err := json.Unmarshal(v, &code); err != nil || !validRiskPersonCode(code) {
		return "", nil, false
	}
	return code, out, true
}

// Query normalizes per PDF §1.6 busiCode 表：
//
//	10   查询成功【计费】     → 001 (Range = result 富对象 JSON)
//	1000 数据未查得【计费】   → 999 (**查无也计费**，由 billing.TableFor("fxry") 决定，
//	                            归一码本身仍如实为 999)
//	1001~1009 / 未知码 / 全局 code=-1 → 上游侧错误，不计费
//
// busiCode 10 但 resultCode 缺失或含 0/1 以外字符 → 上游侧错误 (拿不到有效结论不得计费)。
func (c *RiskPersonClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	body := map[string]string{}
	if req.Name != "" {
		body["name"] = encodePII(req.Name, c.cfg.EncryptionType)
	}
	if req.IDCard != "" {
		body["idCard"] = encodePII(req.IDCard, c.cfg.EncryptionType)
	}
	er, err := enolDoCheck(ctx, c.http, c.cfg.BaseURL, c.cfg.AppID, c.cfg.Secret, c.cfg.APIKey,
		c.cfg.EncryptionType, body, req.Reqid, "riskperson")
	if err != nil {
		return nil, err
	}
	switch er.Data.BusiCode {
	case enolBusiSuccess:
		code, result, ok := normalizeRiskPersonResult(er.Data.Result)
		if !ok {
			return nil, busiErrf(er.Data.BusiCode, "resultCode/riskCode 缺失或非法: "+string(er.Data.Result), er.SeqNo, er.SeqNo)
		}
		if len(code) != riskPersonCodeLen {
			slog.Warn("riskperson resultCode 位数与文档不符",
				"len", len(code), "want", riskPersonCodeLen, "seqNo", er.SeqNo)
		}
		return &model.UpstreamResult{
			Code:  "001",
			Msg:   "成功",
			UID:   er.SeqNo,
			Reqid: req.Reqid,
			LogID: er.SeqNo, // 只有 seqNo(交易流水号)一个上游标识，UID/LogID 同填供后台对账
			Range: sanitizeRange(result),
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

func validRiskPersonCode(s string) bool {
	return s != "" && strings.Trim(s, "01") == ""
}

// Requery: 对账查询接口文档未定义，返回 Reachable=false，台账保持 PENDING 由对账兜底。
func (c *RiskPersonClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}
