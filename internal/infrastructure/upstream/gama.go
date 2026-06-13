package upstream

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/datahub/relay/internal/domain/model"
)

// GamaAPIKey is the fixed product code for 伽马分层分 (伽马PDF §1.5 apiKey 固定值).
const GamaAPIKey = "gama_ctmz_layer_score"

// 伽马 busiCode (伽马PDF §2.1). 仅 10/1000 是业务结果, 其余是上游侧错误。
const (
	gamaBusiSuccess  = 10
	gamaBusiNotFound = 1000
)

// GamaConfig holds the 伽马 endpoint + 我方在伽马侧的凭证 (appId/secret 由商务分配).
type GamaConfig struct {
	BaseURL string // https://{域名}/enol/api/v1/doCheck
	AppID   string
	Secret  string
	APIKey  string // 默认 gama_ctmz_layer_score
}

// GamaClient implements port.UpstreamPort for the 伽马分层分 provider (伽马PDF).
type GamaClient struct {
	cfg  GamaConfig
	http *http.Client
}

// NewGama builds a 伽马 client.
func NewGama(cfg GamaConfig, httpClient *http.Client) *GamaClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if cfg.APIKey == "" {
		cfg.APIKey = GamaAPIKey
	}
	return &GamaClient{cfg: cfg, http: httpClient}
}

type gamaEnvelope struct {
	EncryptionType int               `json:"encryptionType"`
	AppID          string            `json:"appId"`
	Sign           string            `json:"sign"`
	APIKey         string            `json:"apiKey"`
	Body           map[string]string `json:"body"`
}

type gamaResponse struct {
	Code  int    `json:"code"`
	Msg   string `json:"msg"`
	SeqNo string `json:"seqNo"`
	Data  struct {
		BusiCode int    `json:"busiCode"`
		BusiMsg  string `json:"busiMsg"`
		Result   struct {
			Score string `json:"score"`
		} `json:"result"`
	} `json:"data"`
}

// Query performs the signed POST to 伽马 doCheck (伽马PDF §2.2) and normalizes the
// response: busiCode 10 → "001"查得, 1000 → "999"查无, 其余 → error (上游侧异常,
// 触发我方 re-query/对账, 不计费).
func (c *GamaClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	body := map[string]string{
		"name":    req.Name,
		"mobile":  req.Mobile,
		"idCard":  req.IDCard,
		"tradeNo": req.Reqid, // 用我方 reqid 作为伽马 tradeNo（幂等键）
	}
	env := gamaEnvelope{
		EncryptionType: 1,
		AppID:          c.cfg.AppID,
		Sign:           signGama(body, c.cfg.Secret),
		APIKey:         c.cfg.APIKey,
		Body:           body,
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal gama request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build gama request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gama call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read gama body: %w", err)
	}
	var gr gamaResponse
	if err := json.Unmarshal(raw, &gr); err != nil {
		return nil, fmt.Errorf("decode gama body: %w", err)
	}
	if gr.Code != 0 {
		return nil, fmt.Errorf("gama 响应异常 code=%d msg=%s", gr.Code, gr.Msg)
	}

	switch gr.Data.BusiCode {
	case gamaBusiSuccess:
		return &model.UpstreamResult{
			Code:  "001",
			Msg:   "成功",
			UID:   gr.SeqNo,
			Reqid: req.Reqid,
			Range: gr.Data.Result.Score,
		}, nil
	case gamaBusiNotFound:
		return &model.UpstreamResult{
			Code:  "999",
			Msg:   "查无结果",
			UID:   gr.SeqNo,
			Reqid: req.Reqid,
		}, nil
	default:
		// 1001/1002/1003/1006/1007/1009… 均为我方在伽马侧的账户/参数/系统问题,
		// 视为上游侧错误：不计费, 交由 orchestrator 走 re-query/对账兜底。
		return nil, fmt.Errorf("gama 上游错误 busiCode=%d msg=%s", gr.Data.BusiCode, gr.Data.BusiMsg)
	}
}

// Requery: 伽马 doCheck 以 tradeNo 幂等, 真正的对账查询接口待联调 (§15.3)。在此之前
// 返回 Reachable=false, 记录保持 PENDING 由对账兜底。
func (c *GamaClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}

// signGama is the 伽马 MD5 加签 (伽马PDF §3.1): body 非空业务参数按键名 ASCII 升序
// 拼接 key+value, 末尾追加 secret, 取 MD5 小写 hex.
func signGama(params map[string]string, secret string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteString(params[k])
	}
	sb.WriteString(secret)
	sum := md5.Sum([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}
