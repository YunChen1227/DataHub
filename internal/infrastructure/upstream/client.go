// Package upstream hosts the data-provider adapters and a Router that selects
// the active provider (DESIGN §6). Providers implement port.UpstreamPort and
// normalize their native response into model.UpstreamResult ("001"查得/"999"查无).
package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/datahub/relay/internal/domain/model"
)

// IncomeClsConfig holds the income_cls endpoint + merchant credentials
// (income_cls.md: account/key 由商户提供).
type IncomeClsConfig struct {
	BaseURL string // e.g. http://{server}:{port}/yrzx/finan/net/10w/v9
	Account string
	Key     string
}

// IncomeClsClient implements port.UpstreamPort for the income_cls provider.
type IncomeClsClient struct {
	cfg  IncomeClsConfig
	http *http.Client
}

// NewIncomeCls builds an income_cls client. The *http.Client SHOULD carry a
// connection pool + explicit timeouts + circuit breaker (DESIGN §12).
func NewIncomeCls(cfg IncomeClsConfig, httpClient *http.Client) *IncomeClsClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &IncomeClsClient{cfg: cfg, http: httpClient}
}

// incomeClsResponse mirrors the income_cls JSON body (income_cls.md §返回参数).
type incomeClsResponse struct {
	Code   string `json:"code"`
	Msg    string `json:"msg"`
	UID    string `json:"uid"`
	Reqid  string `json:"reqid"`
	Verify string `json:"verify"`
	Result struct {
		Range string `json:"range"`
	} `json:"result"`
}

// Query performs the signed GET to income_cls (income_cls.md §对接方式).
func (c *IncomeClsClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	req.Account = c.cfg.Account
	req.Verify = SignIncomeCls(req, c.cfg.Account, c.cfg.Key)

	q := url.Values{}
	q.Set("account", req.Account)
	q.Set("idCard", req.IDCard)
	q.Set("name", req.Name)
	q.Set("mobile", req.Mobile)
	q.Set("reqid", req.Reqid)
	q.Set("verify", req.Verify)

	endpoint := c.cfg.BaseURL + "?" + q.Encode()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build income_cls request: %w", err)
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("income_cls call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read income_cls body: %w", err)
	}
	var ur incomeClsResponse
	if err := json.Unmarshal(raw, &ur); err != nil {
		return nil, fmt.Errorf("decode income_cls body: %w", err)
	}
	// income_cls Code 已是 "001"/"999" 口径，直接采用 (DESIGN §6.1).
	return &model.UpstreamResult{
		Code:   ur.Code,
		Msg:    ur.Msg,
		UID:    ur.UID,
		Reqid:  ur.Reqid,
		Range:  ur.Result.Range,
		Verify: ur.Verify,
	}, nil
}

// Requery is the idempotent re-query by reqid (DESIGN §7.3). 待联调确认 §15.3 的
// 单记录/对账查询接口前，返回 Reachable=false，记录保持 PENDING 由对账兜底。
func (c *IncomeClsClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}
