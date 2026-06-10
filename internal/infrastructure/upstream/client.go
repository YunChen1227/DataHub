// Package upstream is the HTTP adapter for the income_cls provider (DESIGN §6),
// implementing port.UpstreamPort with MD5 signing, explicit timeouts and an
// idempotent re-query by reqid.
package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/domain/port"
)

// Config holds the upstream endpoint + connection knobs.
type Config struct {
	BaseURL string // e.g. http://{server}:{port}/yrzx/finan/net/10w/v9
}

// Client implements port.UpstreamPort.
type Client struct {
	cfg     Config
	secrets port.SecretProvider
	http    *http.Client
}

// New builds an upstream client. The *http.Client SHOULD be configured with a
// connection pool + explicit connect/read timeouts + circuit breaker (DESIGN §12).
func New(cfg Config, secrets port.SecretProvider, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{cfg: cfg, secrets: secrets, http: httpClient}
}

// upstreamResponse mirrors the income_cls JSON body.
type upstreamResponse struct {
	Code   string `json:"code"`
	Msg    string `json:"msg"`
	UID    string `json:"uid"`
	Reqid  string `json:"reqid"`
	Verify string `json:"verify"`
	LogID  string `json:"logId"`
	Result struct {
		Range string `json:"range"`
	} `json:"result"`
}

// Query performs the signed GET to the upstream (DESIGN §6).
func (c *Client) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	account, key, err := c.secrets.UpstreamCredentials(ctx)
	if err != nil {
		return nil, fmt.Errorf("load upstream credentials: %w", err)
	}
	req.Account = account
	req.Verify = Sign(req, account, key)

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
		return nil, fmt.Errorf("build upstream request: %w", err)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		// network/timeout error → caller triggers Requery (DESIGN §7.3).
		return nil, fmt.Errorf("upstream call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read upstream body: %w", err)
	}
	var ur upstreamResponse
	if err := json.Unmarshal(raw, &ur); err != nil {
		return nil, fmt.Errorf("decode upstream body: %w", err)
	}
	return &model.UpstreamResult{
		Code:  ur.Code,
		Msg:   ur.Msg,
		UID:   ur.UID,
		Reqid: ur.Reqid,
		Range: ur.Result.Range,
		LogID: ur.LogID,
	}, nil
}

// Requery is the idempotent re-query by reqid (DESIGN §7.3). It MUST hit the
// upstream's single-record/对账查询 interface (待联调确认 §15.3) which never
// double-charges. Until that interface is confirmed, this returns Reachable=false
// so the record stays PENDING for the reconciliation job to settle.
func (c *Client) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	// TODO(§15.3): call upstream single-query-by-reqid once the contract is fixed.
	return &model.RequeryResult{Reachable: false}, nil
}
