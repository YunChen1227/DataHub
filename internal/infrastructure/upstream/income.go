package upstream

import (
	"context"
	"crypto/md5"
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

// IncomeConfig holds the 经济能力 (income_cls) upstream endpoint + 我方在该上游侧的
// 凭证 (account/key 由上游商户分配)。v9/v8 复用同一实现，仅 baseURL/account/key 不同。
type IncomeConfig struct {
	BaseURL string // 上游版本路径，例如 https://{域名}/yrzx/finan/net/10w/v9
	Account string // 我方在上游侧的账户
	Key     string // 我方在上游侧的签名密钥
	Version string // x1/v9/v8 仅用于日志区分
}

// IncomeClient implements port.UpstreamPort for the 经济能力 provider (income_cls
// 契约): HTTP GET, account/key 验签, 返回 code/msg/uid/result.range。
type IncomeClient struct {
	cfg  IncomeConfig
	http *http.Client
}

// NewIncome builds an income (经济能力) upstream client.
func NewIncome(cfg IncomeConfig, httpClient *http.Client) *IncomeClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &IncomeClient{cfg: cfg, http: httpClient}
}

type incomeResponse struct {
	Code   string `json:"code"`
	Msg    string `json:"msg"`
	UID    string `json:"uid"`
	Reqid  string `json:"reqid"`
	Verify string `json:"verify"`
	Result struct {
		Range string `json:"range"`
	} `json:"result"`
}

// Query performs the signed GET to the income upstream and normalizes the
// response: code 001 → 查得, 999 → 查无, 其余 (002/003/004/013/012…) → error
// (我方账户/参数/系统侧异常, 不计费, 交由 orchestrator 走 re-query/对账兜底)。
func (c *IncomeClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	reqid := req.Reqid
	if len(reqid) > 20 {
		reqid = reqid[:20] // 上游约束 reqid ≤20
	}
	verify := signIncome(c.cfg.Account, req.IDCard, req.Mobile, reqid, c.cfg.Key)

	q := url.Values{}
	q.Set("account", c.cfg.Account)
	q.Set("idCard", req.IDCard)
	if req.Name != "" {
		q.Set("name", req.Name)
	}
	q.Set("mobile", req.Mobile)
	q.Set("reqid", reqid)
	q.Set("verify", verify)
	full := c.cfg.BaseURL + "?" + q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, fmt.Errorf("build income request: %w", err)
	}

	slog.Debug("income request",
		"version", c.cfg.Version,
		"url", c.cfg.BaseURL,
		"account", c.cfg.Account,
		"reqid", reqid,
	)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("income call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read income body: %w", err)
	}
	slog.Debug("income response", "version", c.cfg.Version, "status", resp.StatusCode, "raw", string(raw))

	var ir incomeResponse
	if err := json.Unmarshal(raw, &ir); err != nil {
		return nil, fmt.Errorf("decode income body: %w", err)
	}

	switch ir.Code {
	case "001":
		return &model.UpstreamResult{
			Code:  "001",
			Msg:   "成功",
			UID:   ir.UID,
			Reqid: reqid,
			Range: ir.Result.Range,
		}, nil
	case "999":
		return &model.UpstreamResult{
			Code:  "999",
			Msg:   "查无结果",
			UID:   ir.UID,
			Reqid: reqid,
		}, nil
	default:
		// 002/003/004/005/006/008/009/011/012/013/020 等均为我方在上游侧的账户/
		// 参数/系统问题, 视为上游侧错误：不计费, 交由 orchestrator 走对账兜底。
		return nil, fmt.Errorf("income 上游错误 code=%s msg=%s", ir.Code, ir.Msg)
	}
}

// Requery: 经济能力上游以 reqid 幂等, 真正的对账查询接口待联调。在此之前返回
// Reachable=false, 记录保持 PENDING 由对账兜底。
func (c *IncomeClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}

// signIncome computes the income upstream request signature (income_cls §输入参数):
//
//	verify = MD5(account + idCard + mobile + reqid + key).toUpperCase()
func signIncome(account, idCard, mobile, reqid, key string) string {
	sum := md5.Sum([]byte(account + idCard + mobile + reqid + key))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}
