package upstream

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/datahub/relay/internal/domain/model"
)

// BlacklistAPIKey is the fixed product code for 黑名单因子V35 (应诺尔 PDF §1.4 apiKey 固定值).
const BlacklistAPIKey = "blackIntV35"

// 黑名单因子V35 busiCode (应诺尔 PDF §2.1). 仅 10/1000 是业务结果, 其余是上游侧错误。
const (
	blacklistBusiSuccess  = 10
	blacklistBusiNotFound = 1000
)

// BlacklistConfig holds the 黑名单因子V35 endpoint + 我方在应诺尔侧的凭证。与 gama 同
// 为应诺尔 enol 端点 (POST /enol/api/v1/doCheck), 仅 apiKey/加密方式/响应体不同。
type BlacklistConfig struct {
	BaseURL        string // https://{域名}/enol/api/v1/doCheck
	AppID          string
	Secret         string
	APIKey         string // 默认 blackIntV35
	EncryptionType int    // 1=明文, 2=MD5(默认): name/idCard/mobile 传 MD5 摘要值
}

// BlacklistClient implements port.UpstreamPort for the 黑名单因子V35 provider. 与
// GamaClient 同信封/同 MD5 加签 (复用 signGama), 区别: apiKey=blackIntV35,
// encryptionType=2 时对 PII 取 MD5, 响应 result 为富对象 (whether_hit/hit_grade/
// hit_type) 原样序列化为 JSON 字符串经下游 result.range 透出。
type BlacklistClient struct {
	cfg  BlacklistConfig
	http *http.Client
}

// NewBlacklist builds a 黑名单因子V35 client.
func NewBlacklist(cfg BlacklistConfig, httpClient *http.Client) *BlacklistClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if cfg.APIKey == "" {
		cfg.APIKey = BlacklistAPIKey
	}
	if cfg.EncryptionType == 0 {
		cfg.EncryptionType = 2
	}
	return &BlacklistClient{cfg: cfg, http: httpClient}
}

type blacklistResponse struct {
	Code  int    `json:"code"`
	Msg   string `json:"msg"`
	SeqNo string `json:"seqNo"`
	Data  struct {
		BusiCode int             `json:"busiCode"`
		BusiMsg  string          `json:"busiMsg"`
		Result   json.RawMessage `json:"result"`
	} `json:"data"`
}

// Query performs the signed POST to 应诺尔 doCheck (黑名单因子V35) and normalizes the
// response: busiCode 10 → "001"查得 (Range = result 富对象 JSON 字符串), 1000 → "999"
// 查无, 其余 → error (上游侧异常, 触发 re-query/对账, 不计费)。
func (c *BlacklistClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	// body 只放产品文档定义的业务参数 (非空才放); encryptionType=2 时对 PII 取 MD5。
	body := map[string]string{}
	if req.Name != "" {
		body["name"] = c.encodePII(req.Name)
	}
	if req.IDCard != "" {
		body["idCard"] = c.encodePII(req.IDCard)
	}
	if req.Mobile != "" {
		body["mobile"] = c.encodePII(req.Mobile)
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
		return nil, fmt.Errorf("marshal blacklist request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build blacklist request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json; charset=utf-8")

	slog.Debug("blacklist request",
		"url", c.cfg.BaseURL,
		"appId", c.cfg.AppID,
		"apiKey", c.cfg.APIKey,
		"encryptionType", c.cfg.EncryptionType,
		"sign", env.Sign,
		"reqid", req.Reqid,
	)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("blacklist call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read blacklist body: %w", err)
	}
	slog.Debug("blacklist response", "status", resp.StatusCode, "raw", string(raw))

	var br blacklistResponse
	if err := json.Unmarshal(raw, &br); err != nil {
		return nil, fmt.Errorf("decode blacklist body: %w", err)
	}
	if br.Code != 0 {
		return nil, fmt.Errorf("blacklist 响应异常 code=%d msg=%s", br.Code, br.Msg)
	}

	switch br.Data.BusiCode {
	case blacklistBusiSuccess:
		return &model.UpstreamResult{
			Code:  "001",
			Msg:   "成功",
			UID:   br.SeqNo,
			Reqid: req.Reqid,
			Range: compactJSON(br.Data.Result),
		}, nil
	case blacklistBusiNotFound:
		return &model.UpstreamResult{
			Code:  "999",
			Msg:   "未查得",
			UID:   br.SeqNo,
			Reqid: req.Reqid,
		}, nil
	default:
		// 1001/1002/1003/1004/1005/1006/1007/1009 均为我方在应诺尔侧的账户/参数/
		// 系统问题, 视为上游侧错误: 不计费, 交由 orchestrator 走 re-query/对账兜底。
		return nil, fmt.Errorf("blacklist 上游错误 busiCode=%d msg=%s", br.Data.BusiCode, br.Data.BusiMsg)
	}
}

// Requery: 黑名单因子V35 doCheck 以 tradeNo 幂等, 真正的对账查询接口待联调。在此之前
// 返回 Reachable=false, 记录保持 PENDING 由对账兜底 (与 gama 一致)。
func (c *BlacklistClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}

// encodePII MD5-hashes a PII value (小写 hex) when encryptionType=2 (应诺尔 PDF §1.4);
// 明文模式 (1) 原样返回。
func (c *BlacklistClient) encodePII(v string) string {
	if c.cfg.EncryptionType != 2 {
		return v
	}
	sum := md5.Sum([]byte(v))
	return hex.EncodeToString(sum[:])
}

// compactJSON returns a compact JSON string for the upstream result object so it
// can be透出 via下游 result.range。空/非法时返回空串。
func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}
