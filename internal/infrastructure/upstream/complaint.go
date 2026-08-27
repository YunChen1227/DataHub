package upstream

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/datahub/relay/internal/domain/model"
)

// 投诉分析识别名单 (kfongtech) 固定业务参数 (接口文档 §1.1.1)。
const (
	complaintMethod  = "api.complaint.query" // 产品名
	complaintVersion = "1.0.0"               // 产品版本号
	complaintSuccess = "0000"                // code=0000 调用成功
)

// ComplaintConfig holds the 投诉分析识别名单 endpoint + 我方在 kfongtech 侧的凭证。
// 外层信封 {apiKey, param(AES 加密业务参数), sign}，响应 data 为 gzip 压缩结果集。
type ComplaintConfig struct {
	BaseURL    string // https://api.kfongtech.com/inlet/api
	APIKey     string // 上游分配的 Apikey (外层明文字段，即 demo 的 api_key)
	AESKey     string // 兼容保留，已不再使用：AES key/iv 按 demo 由 SignSecret 派生
	SignSecret string // 上游分配的 appSecret (即 demo 的 api_secret)：派生 AES key/iv 并计算 sign
}

// ComplaintClient implements port.UpstreamPort for the 投诉分析识别名单 provider.
// 归一化口径 (用户确认「调用成功即计费」)：code=0000 → "001"查得计费，命中状态
// (forbid) 随解压后的结果数组经下游 result.range 透出；其余 code → error (不计费，
// 走复查/对账兜底)。本上游无独立「查无(999)」业务码——未命中体现在记录级 forbid=0。
type ComplaintClient struct {
	cfg  ComplaintConfig
	http *http.Client
}

// NewComplaint builds a 投诉分析识别名单 client.
func NewComplaint(cfg ComplaintConfig, httpClient *http.Client) *ComplaintClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &ComplaintClient{cfg: cfg, http: httpClient}
}

// complaintEnvelope 是外层 JSON 信封 (接口文档 §1.1.1 请求体参数)。
type complaintEnvelope struct {
	APIKey string `json:"apiKey"`
	Param  string `json:"param"`
	Sign   string `json:"sign"`
}

type complaintResponse struct {
	Code  string `json:"code"`  // 0000 成功
	Msg   string `json:"msg"`   // 描述
	Token string `json:"token"` // 上游请求/日志号 (uid=logId 对账用)
	Data  string `json:"data"`  // gzip(base64) 压缩后的结果集
}

// Query performs the signed+encrypted POST to kfongtech inlet/api and normalizes
// the response。成功(code=0000) → "001"查得，Range = 解压后的结果数组 JSON 字符串。
func (c *ComplaintClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	// 业务参数明文：与上游 demo 的 predata 一致——按 key 升序拼成 k=v&k=v...（非 JSON）。
	biz := map[string]string{
		"method":  complaintMethod,
		"version": complaintVersion,
		"poly":    req.Poly,
		"mobile":  req.Mobile,
	}
	plain := sortComplaintParams(biz)

	// AES 密钥/IV 由 appSecret 派生（见下方 deriveComplaintKeyIV 说明）。
	aesKey, aesIV := deriveComplaintKeyIV(c.cfg.SignSecret)
	param, err := encryptParam([]byte(plain), aesKey, aesIV)
	if err != nil {
		return nil, fmt.Errorf("encrypt complaint param: %w", err)
	}

	// 外层 sign 对「业务参数 + apiKey」计算（demo 在加密后再 predata.put("apiKey", ...) 加签）。
	signParams := make(map[string]string, len(biz)+1)
	for k, v := range biz {
		signParams[k] = v
	}
	signParams["apiKey"] = c.cfg.APIKey

	env := complaintEnvelope{
		APIKey: c.cfg.APIKey,
		Param:  param,
		Sign:   signComplaint(signParams, c.cfg.SignSecret),
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal complaint request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build complaint request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	slog.Debug("complaint request",
		"url", c.cfg.BaseURL,
		"apiKey", c.cfg.APIKey,
		"poly", req.Poly,
		"sign", env.Sign,
		"reqid", req.Reqid,
	)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("complaint call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read complaint body: %w", err)
	}
	slog.Debug("complaint response", "status", resp.StatusCode, "raw", string(raw))

	var cr complaintResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return nil, fmt.Errorf("decode complaint body: %w", err)
	}
	if cr.Code != complaintSuccess {
		// 1001/1002/1010/1012/1013/1099/8000 均为我方账户/参数/系统/通道问题，视为
		// 上游侧错误：不计费，交由 orchestrator 走 re-query/对账兜底。失败也带上游
		// token(请求号)落审计供对账追查 (uid=logId 同填)。
		return nil, busiErr(cr.Code, cr.Msg, cr.Token, cr.Token)
	}

	// code=0000：data 为 base64(gzip(JSON 数组))，解压后原样透出到 result.range。
	rangeJSON, err := decodeComplaintData(cr.Data)
	if err != nil {
		return nil, busiErr(cr.Code, "解析结果集失败: "+err.Error(), cr.Token, cr.Token)
	}
	return &model.UpstreamResult{
		Code:  "001",
		Msg:   "成功",
		UID:   cr.Token,
		Reqid: req.Reqid,
		LogID: cr.Token, // 上游仅 token 一个标识，UID/LogID 同填供后台对账
		Range: rangeJSON,
	}, nil
}

// Requery: kfongtech inlet/api 未提供独立对账查询接口，联调前返回 Reachable=false，
// 记录保持 PENDING 由对账兜底 (与其它上游一致)。
func (c *ComplaintClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}

// decodeComplaintData base64 解码后 gzip 解压 data，返回压缩后的结果数组 JSON 字符串
// (紧凑化，供 result.range 透出)。空 data 返回空串。
func decodeComplaintData(data string) (string, error) {
	data = strings.TrimSpace(data)
	if data == "" {
		return "", nil
	}
	gz, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return "", fmt.Errorf("gzip reader: %w", err)
	}
	defer zr.Close()
	plain, err := io.ReadAll(zr)
	if err != nil {
		return "", fmt.Errorf("gzip read: %w", err)
	}
	return sanitizeRange(json.RawMessage(plain)), nil
}

// ---------------------------------------------------------------------------
// 加密/加签算法——对齐上游官方 demo (docs/投诉分析识别/demo/demo1)：
//   - AES 密钥/IV 由 appSecret 派生：
//       aesKey = MD5(appSecret) 转大写后取下标 [8,24) 的 16 个字符 (Util + AesUtils)
//       aesIV  = MD5(aesKey)    转大写后取下标 [8,24) 的 16 个字符
//   - param = AES/CBC/PKCS7Padding(sortParam(业务参数), aesKey, aesIV) 后转小写 hex
//   - sign  = MD5(appSecret + sortParam(业务参数 + apiKey)) 小写 hex (Util.signParam)
//     sortParam：按 key ASCII 升序、剔除空值与 "sign"，拼成 k1=v1&k2=v2&...
// mock_complaint.go 与本实现镜像，故本地全链路测试可通过。
// ---------------------------------------------------------------------------

// complaintMD5Upper 返回 MD5(s) 的大写 hex (对齐 demo Util.MD5(...).toUpperCase())。
func complaintMD5Upper(s string) string {
	sum := md5.Sum([]byte(s))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// deriveComplaintKeyIV 由 appSecret 派生 AES 的 key 与 iv (各 16 字节，见上方说明)。
func deriveComplaintKeyIV(secret string) (key, iv string) {
	key = complaintMD5Upper(secret)[8:24]
	iv = complaintMD5Upper(key)[8:24]
	return key, iv
}

// sortComplaintParams 按 key ASCII 升序拼成 k1=v1&k2=v2&...，剔除空值与 "sign"
// (对齐 demo Util.sortParam)。
func sortComplaintParams(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || v == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(params[k])
	}
	return sb.String()
}

// encryptParam AES/CBC/PKCS7Padding 加密业务参数明文，返回小写 hex (demo AesUtils.encrypt)。
func encryptParam(plaintext []byte, key, iv string) (string, error) {
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return "", fmt.Errorf("aes new cipher (key len=%d): %w", len(key), err)
	}
	// AES 分组 16 字节时 PKCS5 与 PKCS7 填充等价。
	padded := pkcs5Pad(plaintext, block.BlockSize())
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, []byte(iv)).CryptBlocks(out, padded)
	return hex.EncodeToString(out), nil
}

// signComplaint 计算外层 sign = MD5(secret + sortParam(params)) 小写 hex (demo Util.signParam)。
func signComplaint(params map[string]string, secret string) string {
	sum := md5.Sum([]byte(secret + sortComplaintParams(params)))
	return hex.EncodeToString(sum[:])
}
