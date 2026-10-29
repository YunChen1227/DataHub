package upstream

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/datahub/relay/internal/domain/model"
)

// 依据文档：docs/申请反欺诈定制模型_冠辰科技_202610.pdf (阿里云风险识别 SAF
// 「API文档-申请反欺诈定制模型-冠辰科技_202610」)。

// 默认值 (PDF §2.1 / §2.3 / §3.1 / §3.2)。
const (
	AntiFraudDefaultBaseURL = "https://saf.cn-shanghai.aliyuncs.com/"
	AntiFraudService        = "anti_fraud_customed" // §3.1「阿里云Service定义」
	AntiFraudScene          = "gc_cust_202610"      // §3.2 scene「必传」样例值
	// 文档自相矛盾：§2.3 公共参数表「本版本对应为 2019-05-21」、§2.4 Demo 写死
	// "2019-05-21"，而 §3.1 请求示例 URL 写 Version=2020-07-06。以参数表与 Demo 为准，
	// 配置可覆盖 (apiKey 字段)。
	AntiFraudAPIVersion = "2019-05-21"
)

// §3.3 Code 返回码。仅 200 是业务结论 (「请求正常，返回该状态时计费」)，其余均为
// 上游侧错误。
const antiFraudCodeOK = 200

// §3.4 score：-1 表示无法判断风险 or 未查得；其余 [200,800] 为查得评分。
const (
	antiFraudScoreNotFound = -1
	antiFraudScoreMin      = 200
	antiFraudScoreMax      = 800
)

// AntiFraudConfig holds the 申请反欺诈-定制模型 endpoint + 我方阿里云 RAM 子账号凭证
// (PDF §1：AccessKeyID/AccessKeySecret，需授予 AliyunYundunSAFFullAccess)。
//
// 地址 (PDF §2.1)：https://saf.cn-shanghai.aliyuncs.com，HTTPS + POST。文档只给了这
// 一个地址，没有单独的测试环境。
type AntiFraudConfig struct {
	BaseURL         string
	AccessKeyID     string
	AccessKeySecret string
	Service         string // 默认 anti_fraud_customed
	Scene           string // 默认 gc_cust_202610
	Version         string // 默认 2019-05-21
}

// AntiFraudClient implements port.UpstreamPort for 申请反欺诈-定制模型 (阿里云 SAF)。
//
// 协议 (PDF §2.3/§2.4)：阿里云 POP RPC 风格，公共参数 + Action=ExecuteRequest +
// Service + ServiceParameters(JSON)，HMAC-SHA1 签名 (SignatureVersion 1.0)，参数以
// form 形式 POST。业务参数 (§3.2)：mobile / mobileMd5 / mobileSha256 三选一必传 +
// scene 必传。按接入约定下游只传明文 mobile，网关原样以 mobile 发上游，不发另两种摘要。
// 响应 Data 富对象 ({"score":"258"}) 整体经下游 result.range 透出。
// 本文档只定义了一个业务接口 (ExecuteRequest + anti_fraud_customed)，已全部实现。
type AntiFraudClient struct {
	cfg  AntiFraudConfig
	http *http.Client
	now  func() time.Time
}

// NewAntiFraud builds a 申请反欺诈-定制模型 client.
func NewAntiFraud(cfg AntiFraudConfig, httpClient *http.Client) *AntiFraudClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = AntiFraudDefaultBaseURL
	}
	if cfg.Service == "" {
		cfg.Service = AntiFraudService
	}
	if cfg.Scene == "" {
		cfg.Scene = AntiFraudScene
	}
	if cfg.Version == "" {
		cfg.Version = AntiFraudAPIVersion
	}
	return &AntiFraudClient{cfg: cfg, http: httpClient, now: time.Now}
}

// antiFraudResponse 兼容两种形态：业务应答 (§3.3 示例 Code 为数字 200) 与阿里云 POP
// 网关错误 (签名/鉴权失败时 Code 为字符串，如 "SignatureDoesNotMatch")。
type antiFraudResponse struct {
	Code      json.RawMessage `json:"Code"`
	Message   string          `json:"Message"`
	RequestID string          `json:"RequestId"`
	Data      json.RawMessage `json:"Data"`
}

// Query normalizes per PDF §3.3/§3.4：
//
//	Code=200 且 score ≠ -1           → 001 (Range = Data 富对象 JSON)【计费】
//	Code=200 且 score = -1           → 999 (§3.3「200 … 计费」：**查无也计费**，由
//	                                   billing.TableFor("fqz") 决定，归一码仍如实为 999)
//	Code=200 但 Data/score 缺失或非数值 → 上游侧错误 (拿不到有效结论不得计费)
//	400/402/403/404/500 / POP 网关错误 → 上游侧错误，不计费
//
// 上游只有 RequestId 一个标识，UID/LogID 同填。
func (c *AntiFraudClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	svcParams, err := json.Marshal(map[string]string{
		"mobile": req.Mobile,
		"scene":  c.cfg.Scene,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal antifraud ServiceParameters: %w", err)
	}
	nonce, err := antiFraudNonce()
	if err != nil {
		return nil, err
	}
	params := map[string]string{
		"AccessKeyId":       c.cfg.AccessKeyID,
		"Format":            "JSON",
		"SignatureMethod":   "HMAC-SHA1",
		"SignatureVersion":  "1.0",
		"SignatureNonce":    nonce,
		"Timestamp":         c.now().UTC().Format("2006-01-02T15:04:05Z"),
		"Version":           c.cfg.Version,
		"Action":            "ExecuteRequest",
		"Service":           c.cfg.Service,
		"ServiceParameters": string(svcParams),
	}
	params["Signature"] = signAliyunPOP(http.MethodPost, params, c.cfg.AccessKeySecret)

	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build antifraud request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")

	slog.Debug("antifraud request",
		"url", c.cfg.BaseURL,
		"accessKeyId", c.cfg.AccessKeyID,
		"service", c.cfg.Service,
		"scene", c.cfg.Scene,
		"version", c.cfg.Version,
		"reqid", req.Reqid,
	)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("antifraud call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read antifraud body: %w", err)
	}
	slog.Debug("antifraud response", "status", resp.StatusCode, "raw", string(raw))

	var ar antiFraudResponse
	if err := json.Unmarshal(raw, &ar); err != nil {
		return nil, fmt.Errorf("decode antifraud body (http %d): %w", resp.StatusCode, err)
	}
	code := antiFraudCodeString(ar.Code)
	if code == "" {
		return nil, busiErr(strconv.Itoa(resp.StatusCode), "响应缺少 Code: "+string(raw), ar.RequestID, ar.RequestID)
	}
	if code != strconv.Itoa(antiFraudCodeOK) {
		// 400 ServiceParameters 不合法 / 402 日 QPS 超规格限流 / 403 权限不足、服务未开通
		// 或到期 / 404 Service 不合法 / 500 系统错误 (§3.3)，以及 POP 网关的签名/鉴权
		// 错误：上游侧错误，不计费，带 RequestId 落审计供对账追查。
		return nil, busiErr(code, ar.Message, ar.RequestID, ar.RequestID)
	}

	data, score, ok := parseAntiFraudData(ar.Data)
	if !ok {
		return nil, busiErr(code, "Data.score 缺失或非法: "+string(ar.Data), ar.RequestID, ar.RequestID)
	}
	if score == antiFraudScoreNotFound {
		return &model.UpstreamResult{
			Code:  "999",
			Msg:   "未查得",
			UID:   ar.RequestID,
			Reqid: req.Reqid,
			LogID: ar.RequestID, // 只有 RequestId 一个上游标识，UID/LogID 同填供后台对账
		}, nil
	}
	if score < antiFraudScoreMin || score > antiFraudScoreMax {
		// §3.4「如有特定的分值段服务，以实际沟通为准」：区间外的分值仍视为查得结论。
		slog.Warn("antifraud score 超出文档区间 [200,800]", "score", score, "requestId", ar.RequestID)
	}
	return &model.UpstreamResult{
		Code:  "001",
		Msg:   "成功",
		UID:   ar.RequestID,
		Reqid: req.Reqid,
		LogID: ar.RequestID,
		Range: sanitizeRange(data),
	}, nil
}

// Requery: 文档未定义对账查询接口，返回 Reachable=false，台账保持 PENDING 由对账兜底。
func (c *AntiFraudClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}

// antiFraudCodeString 把 Code 统一成字符串：数字 200 → "200"，字符串原样。
func antiFraudCodeString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String()
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

// parseAntiFraudData 取出 Data 富对象与 score。Data 可能是对象，也可能是 JSON 字符串
// 包着的对象；score 文档示例为字符串 "258"，数字形态同样接受。
func parseAntiFraudData(raw json.RawMessage) (json.RawMessage, int, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, 0, false
	}
	data := raw
	var inner string
	if err := json.Unmarshal(raw, &inner); err == nil {
		data = json.RawMessage(inner)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil || m == nil {
		return nil, 0, false
	}
	sv, ok := m["score"]
	if !ok {
		return nil, 0, false
	}
	var s string
	if err := json.Unmarshal(sv, &s); err != nil {
		s = string(sv)
	}
	score, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return nil, 0, false
	}
	return data, score, true
}

// signAliyunPOP 计算阿里云 POP RPC 签名 (PDF §2.4 computeSignature)：参数按 key 字典序
// 排序后 percentEncode(k)=percentEncode(v) 以 & 拼接，StringToSign = METHOD&%2F&
// percentEncode(规范化串)，HMAC-SHA1(secret+"&") 后 Base64。
func signAliyunPOP(method string, params map[string]string, secret string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k == "Signature" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, popEncode(k)+"="+popEncode(params[k]))
	}
	toSign := method + "&" + popEncode("/") + "&" + popEncode(strings.Join(pairs, "&"))
	mac := hmac.New(sha1.New, []byte(secret+"&"))
	mac.Write([]byte(toSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// popEncode 是阿里云规定的 percent-encode：URL 编码后 "+"→"%20"、"*"→"%2A"、"%7E"→"~"。
func popEncode(s string) string {
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "+", "%20")
	e = strings.ReplaceAll(e, "*", "%2A")
	return strings.ReplaceAll(e, "%7E", "~")
}

func antiFraudNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("antifraud nonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}
