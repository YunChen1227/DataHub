package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/datahub/relay/internal/domain/model"
)

// 租赁分V2-D 默认业务参数 (布尔/守信文档 §2.5)。
const (
	rentalDefaultService = "buer_unique_service"
	rentalDefaultMode    = "mode_rent_score_v2_d"
)

// 租赁分V2-D 响应状态码 (文档 §4)。仅 SW0000 收费 (查得), SW0002 查无, 其余均为
// 上游侧错误 (认证/验签/解密/限额/参数/系统), 不计费, 交由 orchestrator 兜底。
const (
	rentalCodeSuccess  = "SW0000" // 查询成功 (收费)
	rentalCodeNotFound = "SW0002" // 查无记录 (不收费)
)

// RentalConfig holds the 租赁分V2-D (守信 shouxin168) 上游 endpoint + 凭证。AES 密钥/
// institution_id 由上游商务分配; LicenseURL/LicenseType 为我方启动时上传授权书到
// OSS 后缓存的固定值, 所有查询复用。
type RentalConfig struct {
	BaseURL       string
	InstitutionID string
	AESKey        string
	Service       string // 默认 buer_unique_service
	Mode          string // 默认 mode_rent_score_v2_d
	LicenseURL    string // 授权书 OSS 地址 (启动上传后缓存)
	LicenseType   int    // 0:图片(jpg/jpeg/png/bmp) 1:pdf
}

// RentalClient implements port.UpstreamPort for the 租赁分V2-D provider: 业务数据
// JSON 经 AES/ECB/PKCS5Padding + Base64 得 biz_data, 与 institution_id 一起以
// form 表单 POST 提交; 响应归一化为 ("001" 查得 / "999" 查无)。
type RentalClient struct {
	cfg  RentalConfig
	http *http.Client
}

// NewRental builds a 租赁分V2-D upstream client.
func NewRental(cfg RentalConfig, httpClient *http.Client) *RentalClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if cfg.Service == "" {
		cfg.Service = rentalDefaultService
	}
	if cfg.Mode == "" {
		cfg.Mode = rentalDefaultMode
	}
	return &RentalClient{cfg: cfg, http: httpClient}
}

// rentalBizData is the明文业务数据 (文档 §2.5), JSON 化后做 AES 加密。
type rentalBizData struct {
	Name        string `json:"name"`
	Phone       string `json:"phone"`
	IdentNumber string `json:"ident_number"`
	Service     string `json:"service"`
	Mode        string `json:"mode"`
	LicenseURL  string `json:"licenseUrl"`
	LicenseType int    `json:"licenseType"`
}

// rentalResponse is the上游响应外层结构 (文档 §3.2)。resp_data 既可能是对象也可能是
// 字符串/空, 用 RawMessage 延迟解析。
type rentalResponse struct {
	RespCode  string          `json:"resp_code"`
	RespMsg   string          `json:"resp_msg"`
	RespOrder string          `json:"resp_order"`
	Timestamp json.RawMessage `json:"timestamp"`
	RespData  json.RawMessage `json:"resp_data"`
}

// Query performs the AES-encrypted form POST to 租赁分V2-D and normalizes the
// response: SW0000 → "001" 查得 (Range=score1), SW0002 → "999" 查无, 其余 → error。
func (c *RentalClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	biz := rentalBizData{
		Name:        req.Name,
		Phone:       req.Mobile,
		IdentNumber: req.IDCard,
		Service:     c.cfg.Service,
		Mode:        c.cfg.Mode,
		LicenseURL:  c.cfg.LicenseURL,
		LicenseType: c.cfg.LicenseType,
	}
	plain, err := json.Marshal(biz)
	if err != nil {
		return nil, fmt.Errorf("marshal rental biz_data: %w", err)
	}
	cipher, err := aesECBEncryptBase64(plain, []byte(c.cfg.AESKey))
	if err != nil {
		return nil, fmt.Errorf("encrypt rental biz_data: %w", err)
	}

	form := url.Values{}
	form.Set("institution_id", c.cfg.InstitutionID)
	form.Set("biz_data", cipher)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build rental request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Accept", "application/json;charset=utf-8")

	// 出站请求日志 (不含 AES 密钥/明文), 便于与上游逐字段核对。
	slog.Debug("rental request",
		"url", c.cfg.BaseURL,
		"institutionId", c.cfg.InstitutionID,
		"service", c.cfg.Service,
		"mode", c.cfg.Mode,
		"reqid", req.Reqid,
	)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("rental call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read rental body: %w", err)
	}
	slog.Debug("rental response", "status", resp.StatusCode, "raw", string(raw))

	var rr rentalResponse
	if err := json.Unmarshal(raw, &rr); err != nil {
		return nil, fmt.Errorf("decode rental body: %w", err)
	}

	switch rr.RespCode {
	case rentalCodeSuccess:
		return &model.UpstreamResult{
			Code:  "001",
			Msg:   "成功",
			UID:   rr.RespOrder,
			Reqid: req.Reqid,
			Range: parseRentalScore(rr.RespData),
		}, nil
	case rentalCodeNotFound:
		return &model.UpstreamResult{
			Code:  "999",
			Msg:   "查无结果",
			UID:   rr.RespOrder,
			Reqid: req.Reqid,
		}, nil
	default:
		// SW0001/SW0003/SW003x/SW004x/SW001x/SW10xx/SW9999 等均为我方在上游侧的
		// 认证/验签/解密/限额/参数/系统问题, 视为上游侧错误: 不计费, 兜底处理。
		return nil, fmt.Errorf("rental 上游错误 resp_code=%s resp_msg=%s", rr.RespCode, rr.RespMsg)
	}
}

// Requery: 租赁分V2-D 暂未提供对账查询接口, 返回 Reachable=false, 记录保持 PENDING
// 由复查/对账兜底 (与 gama/income 一致)。
func (c *RentalClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}

// parseRentalScore extracts score1 from resp_data (对象 {"score1": 546.6}) and
// formats it as a string for下游 result.range 透出。无法解析时返回空串。
func parseRentalScore(data json.RawMessage) string {
	if len(data) == 0 {
		return ""
	}
	var body struct {
		Score1 *float64 `json:"score1"`
	}
	if err := json.Unmarshal(data, &body); err != nil || body.Score1 == nil {
		return ""
	}
	return strconv.FormatFloat(*body.Score1, 'f', -1, 64)
}
