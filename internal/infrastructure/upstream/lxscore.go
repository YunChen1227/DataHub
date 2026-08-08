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
	"strconv"
	"strings"
	"time"

	"github.com/datahub/relay/internal/domain/model"
)

// 灵犀分 score_195_v1 (fullink) 契约常量，逐字对齐
// docs/灵犀分-score_195_v1-接口文档.pdf。
const (
	// lxScoreField 是 data 解密后业务对象里唯一的字段名（文档 §2.4 业务参数表）。
	// 上游把产品名直接当字段名，原样照抄不做"规范化"。
	lxScoreField = "score_195_v1"

	// lxScoreStatusOK：外层 status=200 代表接口请求成功（文档 §2.4 + 计费规则第 1 条）。
	lxScoreStatusOK = 200

	// lxScoreNotFound：status=200 但分数为 -1 表示查得失败（文档「计费规则」第 3 条）。
	lxScoreNotFound = "-1"

	// lxScoreEmptyNameMD5 是空姓名的默认传参（文档 §2.2 name 说明给定的固定值，
	// 即 MD5("")）。姓名缺省时按上游要求填该值，而不是省略字段。
	lxScoreEmptyNameMD5 = "d41d8cd98f00b204e9800998ecf8427e"
)

// LXScoreConfig holds the 灵犀分 endpoint + 我方在 fullink 侧的凭证。
// 三项凭证（customerId / customerProdId / encryptKey）由上游以邮件下发
// （文档「注意事项」第 2 条）。encryptKey 同时用于 sign 加密与 data 解密。
type LXScoreConfig struct {
	BaseURL        string // https://lxf.fullink.tech/report/encode
	CustomerID     string // customerId 商户 code
	CustomerProdID string // customerProdId 产品 code
	EncryptKey     string // encryptKey：DES 密钥，兼作 sign 加密与 data 解密
}

// LXScoreClient implements port.UpstreamPort for the 灵犀分 score_195_v1 provider.
//
// 协议：POST JSON，八个字段全必传（文档 §2.2）；name/mobile/idCardNo 传 MD5 摘要；
// sign = DES/CBC(按 ASCII 升序拼成的 k=v&… 串, encryptKey) 大写 hex（见 descbc.go
// 对编码形态的反推说明）。
//
// 归一（严格按文档「计费规则」三条）：
//   - status=200 且分数 ≠ "-1" → "001" 查得（计费），Range = 分数字符串（300-900）；
//   - status=200 且分数 = "-1" → "999" 查无（不计费）；
//   - status ≠ 200            → 上游侧错误（不计费，走复查/对账兜底）。
//
// 上游标识：响应体只有 status/msg/data/internalErrorCode，**不返回任何订单号或
// 请求号**；唯一能与上游对账的键是我方生成、上游落库并用于重复请求判定的
// customerRequestId（错误示例 "重复请求 : 2f78d83e…" 回显的就是它）。因此
// UID/LogID 同填 customerRequestId，保证成功/查无/失败三条路径审计都不为空。
type LXScoreClient struct {
	cfg  LXScoreConfig
	http *http.Client
}

// NewLXScore builds a 灵犀分 client.
func NewLXScore(cfg LXScoreConfig, httpClient *http.Client) *LXScoreClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &LXScoreClient{cfg: cfg, http: httpClient}
}

// lxScoreRequest 是请求体（文档 §2.2 参数表 / §2.3 请求示例）。
type lxScoreRequest struct {
	CustomerID        string `json:"customerId"`
	CustomerProdID    string `json:"customerProdId"`
	CustomerRequestID string `json:"customerRequestId"`
	Name              string `json:"name"`
	Mobile            string `json:"mobile"`
	IDCardNo          string `json:"idCardNo"`
	Timestamp         int64  `json:"timestamp"`
	Sign              string `json:"sign"`
}

// lxScoreResponse 是外层通用响应（文档 §2.4 通用参数）。
type lxScoreResponse struct {
	Status            int    `json:"status"`
	Msg               string `json:"msg"`
	Data              string `json:"data"` // DES 密文，需用 encryptKey 解密
	InternalErrorCode string `json:"internalErrorCode"`
}

// Query performs the signed POST to 灵犀分 /report/encode and normalizes the response.
func (c *LXScoreClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	key, err := desKeyBytes(c.cfg.EncryptKey)
	if err != nil {
		// 凭证形态不合法属于我方配置问题，且上游压根没被调用——没有上游标识可带，
		// 用裸 error（orchestrator 归一为 505062，不计费）。
		return nil, fmt.Errorf("lxscore encryptKey 非法: %w", err)
	}

	// customerRequestId：商户请求编号，需保证唯一（文档 §2.2）。直接用我方内部
	// reqid——它本就全局唯一，且是本上游唯一可对账的键。
	customerRequestID := req.Reqid

	// name/mobile/idCardNo 传 MD5 摘要（文档 §2.2）。姓名缺省时按上游给定的默认值
	// 传 MD5("")——md5("") 本身就等于该常量，这里显式断言以便文档对照。
	params := map[string]string{
		"customerId":        c.cfg.CustomerID,
		"customerProdId":    c.cfg.CustomerProdID,
		"customerRequestId": customerRequestID,
		"name":              md5Hex(req.Name),
		"mobile":            md5Hex(req.Mobile),
		"idCardNo":          md5Hex(req.IDCard),
		"timestamp":         strconv.FormatInt(time.Now().UnixMilli(), 10),
	}
	if req.Name == "" {
		params["name"] = lxScoreEmptyNameMD5
	}

	sign, err := desEncryptHex([]byte(lxScoreSignStr(params)), key)
	if err != nil {
		return nil, fmt.Errorf("lxscore 计算 sign: %w", err)
	}

	ts, _ := strconv.ParseInt(params["timestamp"], 10, 64)
	payload, err := json.Marshal(lxScoreRequest{
		CustomerID:        params["customerId"],
		CustomerProdID:    params["customerProdId"],
		CustomerRequestID: params["customerRequestId"],
		Name:              params["name"],
		Mobile:            params["mobile"],
		IDCardNo:          params["idCardNo"],
		Timestamp:         ts,
		Sign:              sign,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal lxscore request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build lxscore request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json; charset=utf-8")

	slog.Debug("lxscore request",
		"url", c.cfg.BaseURL,
		"customerId", c.cfg.CustomerID,
		"customerProdId", c.cfg.CustomerProdID,
		"customerRequestId", customerRequestID,
		"reqid", req.Reqid,
	)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("lxscore call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read lxscore body: %w", err)
	}
	slog.Debug("lxscore response", "status", resp.StatusCode, "raw", string(raw))

	var lr lxScoreResponse
	if err := json.Unmarshal(raw, &lr); err != nil {
		return nil, fmt.Errorf("decode lxscore body (http %d): %w", resp.StatusCode, err)
	}

	if lr.Status != lxScoreStatusOK {
		// 附录错误码表 9031001/2031103/2031204/2031208/2031209/2031212/2031217/
		// 2031218/2031219/2031225 均为我方账户、产品、参数、IP 白名单或上游系统问题，
		// 一律视为上游侧错误：不计费，交 orchestrator 走复查/对账兜底。失败也带上
		// customerRequestId 落审计，供向上游追查（含 2031225 重复请求）。
		code := lr.InternalErrorCode
		if code == "" {
			code = strconv.Itoa(lr.Status)
		}
		return nil, busiErr(code, lr.Msg, customerRequestID, customerRequestID)
	}

	// status=200：data 为 DES 密文，解密后取 score_195_v1。
	if strings.TrimSpace(lr.Data) == "" {
		// 上游称成功但未给报告——按「无报告」归一为查无，不计费。
		return &model.UpstreamResult{
			Code: "999", Msg: "未查得", UID: customerRequestID,
			Reqid: req.Reqid, LogID: customerRequestID,
		}, nil
	}
	score, err := c.decodeScore(lr.Data, key)
	if err != nil {
		// 能解出 status=200 却解不开 data，属于协议/密钥异常：报错而非静默当查无，
		// 避免把集成问题伪装成正常业务结果。code 用短标记（审计 upstream_code 列有长度上限）。
		return nil, busiErr("E_DATA", "解析报告数据失败: "+err.Error(),
			customerRequestID, customerRequestID)
	}
	if score == lxScoreNotFound {
		return &model.UpstreamResult{
			Code: "999", Msg: "未查得", UID: customerRequestID,
			Reqid: req.Reqid, LogID: customerRequestID,
		}, nil
	}
	return &model.UpstreamResult{
		Code:  "001",
		Msg:   "成功",
		UID:   customerRequestID,
		Reqid: req.Reqid,
		LogID: customerRequestID,
		Range: score, // 【300-900】分数越高风险越低
	}, nil
}

// Requery: 灵犀分未提供独立的对账查询接口（文档只有 /report/encode 一个接口，且
// 重复的 customerRequestId 会被 2031225 拒绝），故联调前返回 Reachable=false，
// 记录保持 PENDING 由对账兜底（与其余上游一致）。
func (c *LXScoreClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}

// decodeScore 解密 data 并取出 score_195_v1 字段值。
func (c *LXScoreClient) decodeScore(data string, key []byte) (string, error) {
	plain, err := desDecryptHex(data, key)
	if err != nil {
		return "", err
	}
	// data 解密示例：{"score_195_v1": "600"}。分数上游标称 string，但个别实现会回
	// 数字形态，故用 json.Number 两种都认。
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(plain, &obj); err != nil {
		return "", fmt.Errorf("解析业务 JSON: %w", err)
	}
	node, ok := obj[lxScoreField]
	if !ok {
		return "", fmt.Errorf("业务数据缺少 %s 字段", lxScoreField)
	}
	var s string
	if err := json.Unmarshal(node, &s); err == nil {
		return strings.TrimSpace(s), nil
	}
	var n json.Number
	if err := json.Unmarshal(node, &n); err == nil {
		return n.String(), nil
	}
	return "", fmt.Errorf("%s 字段类型非法", lxScoreField)
}

// lxScoreSignStr 按文档 §2.2 参数表的固定字段顺序拼待签名串（**非字母序**）：
//
//	customerId=…&customerProdId=…&customerRequestId=…&name=…&mobile=…&idCardNo=…&timestamp=…
//
// 与文档示例开头 "customerId=xxx&customerProdId=xxx&customerRequestId=xxx&" 一致。
// 经直连上游联调验证：字母序拼串会被上游判 2031208 签名验证失败；改用文档字段
// 顺序后签名通过（改到 IP 白名单校验 2031209），故此处必须锁定文档顺序而非
// sort.Strings 的 ASCII 升序（升序会把 idCardNo/mobile/name 排到 name 之前）。
// sign 自身不参与签名。
func lxScoreSignStr(params map[string]string) string {
	order := []string{
		"customerId", "customerProdId", "customerRequestId",
		"name", "mobile", "idCardNo", "timestamp",
	}
	parts := make([]string, 0, len(order))
	for _, k := range order {
		parts = append(parts, k+"="+params[k])
	}
	return strings.Join(parts, "&")
}

// md5Hex 返回小写 hex MD5 摘要（文档 §2.2：name/mobile/idCardNo 传 md5/sha256，
// 由 name 缺省值 d41d8cd98f00b204e9800998ecf8427e = MD5("") 可确认取 MD5）。
func md5Hex(v string) string {
	sum := md5.Sum([]byte(v))
	return hex.EncodeToString(sum[:])
}
