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
	"sync"

	"github.com/datahub/relay/internal/domain/model"
)

// 投诉分析识别名单 (kfongtech) 固定业务参数 (接口文档 §1.1.1)。
const (
	complaintMethod  = "api.complaint.query" // 产品名
	complaintVersion = "1.0.0"               // 产品版本号
	complaintSuccess = "0000"                // code=0000 调用成功
)

// complaintPolys 是本路由每次请求都要逐一查询的命中级别策略 (C1 高危/C2 敏感/
// C3 一般)。下游不再传 poly——网关对**每次**请求都把三档各查一次并整合结果透出
// (见 Query)。顺序即 result.range 字典的键序 (json.Marshal 对 map 键升序输出)。
var complaintPolys = []string{"C1", "C2", "C3"}

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
//
// **多命中级别整合**：上游一次调用只查一个 poly，但本客户端对每次下游请求都并发
// 查询 C1/C2/C3 三档、把结果整合成一个按 poly 归档的字典经 result.range 透出，形如
//
//	{"C1":{"forbid":0,"hit":"未命中","callee":"..."},
//	 "C2":{"forbid":1,"hit":"命中","callee":"..."},
//	 "C3":{"forbid":1,"hit":"命中","callee":"..."}}
//
// 对编排器 (application) 这仍是**一次逻辑查询**：一条台账、一次结算、下游计一次成功
// 查得数。代价是对上游 kfongtech 产生 3 次「调用成功即计费」的调用 (无法避免——上游
// 无批量多档接口)。三档只要有一档成功即归一 001 并计费，失败的档在字典里以
// forbid=-1/"无法判定" 呈现；三档全失败才返回上游错误 (不计费，走复查/对账兜底)。
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

// complaintRecord 是上游结果数组里的一条记录 (接口文档 §1.1.5)：callee 被查号码的
// 脱敏/哈希标识，forbid 命中状态 (0未命中/1命中/-1异常)。
type complaintRecord struct {
	Callee string `json:"callee"`
	Forbid int    `json:"forbid"`
}

// polyOutcome 是单个命中级别策略在整合字典里的值：forbid 原值 + 可读命中说明 hit；
// callee 透出被查号码的脱敏标识；该档单独失败时 error 说明原因 (其余档仍有结果)。
type polyOutcome struct {
	Forbid int    `json:"forbid"`
	Hit    string `json:"hit"`
	Callee string `json:"callee,omitempty"`
	Error  string `json:"error,omitempty"`
}

// complaintHitText 把 forbid 映射为可读命中说明 (对齐接口文档 §1.1.5)。
func complaintHitText(forbid int) string {
	switch forbid {
	case 1:
		return "命中"
	case 0:
		return "未命中"
	default:
		return "无法判定"
	}
}

// Query 对每次下游请求并发查询 C1/C2/C3 三个命中级别，把结果整合成按 poly 归档的
// 字典经 result.range 透出 (见类型注释)。三档只要有一档成功即归一 001 (计费)；全部
// 失败才返回上游错误 (不计费)。对编排器仍是一次逻辑查询——台账/结算/下游计费各一次。
func (c *ComplaintClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	type polyResult struct {
		rec   complaintRecord
		token string
		err   error
	}
	results := make([]polyResult, len(complaintPolys))
	var wg sync.WaitGroup
	for i, poly := range complaintPolys {
		wg.Add(1)
		go func(i int, poly string) {
			defer wg.Done()
			rec, token, err := c.queryPoly(ctx, req.Mobile, poly, req.Reqid)
			results[i] = polyResult{rec: rec, token: token, err: err}
		}(i, poly)
	}
	wg.Wait()

	merged := make(map[string]polyOutcome, len(complaintPolys))
	tokens := make([]string, 0, len(complaintPolys))
	var firstErr error
	successCount := 0
	for i, poly := range complaintPolys {
		r := results[i]
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			// 单档失败：不把上游错误细节透给下游 (避免泄露上游标识/内情)，仅标"查询失败"。
			merged[poly] = polyOutcome{Forbid: -1, Hit: complaintHitText(-1), Error: "查询失败"}
			continue
		}
		successCount++
		if r.token != "" {
			tokens = append(tokens, poly+":"+r.token)
		}
		merged[poly] = polyOutcome{Forbid: r.rec.Forbid, Hit: complaintHitText(r.rec.Forbid), Callee: r.rec.Callee}
	}

	if successCount == 0 {
		// 三档全失败：无任何确定结论，返回首个上游错误，交由 orchestrator 走复查/
		// 对账兜底 (不计费)。firstErr 必为 *model.UpstreamError 或网络错误。
		return nil, firstErr
	}

	rangeJSON, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("marshal merged complaint result: %w", err)
	}
	// 各成功档的上游 token 以 "poly:token" 拼接落审计 (uid=logId 同填)，供逐档对账。
	token := strings.Join(tokens, ",")
	return &model.UpstreamResult{
		Code:  "001",
		Msg:   "成功",
		UID:   token,
		Reqid: req.Reqid,
		LogID: token,
		Range: string(rangeJSON),
	}, nil
}

// queryPoly 执行单个命中级别的签名+加密 POST，解析出该档的首条记录 (callee/forbid)。
// 成功但结果集为空视为未命中 (forbid=0，对齐文档「未命中亦 001」)。上游非 0000 或
// 解析失败返回 *model.UpstreamError (带上游 token 供对账)。
func (c *ComplaintClient) queryPoly(ctx context.Context, mobile, poly, reqid string) (complaintRecord, string, error) {
	// 业务参数明文：与上游 demo 的 predata 一致——按 key 升序拼成 k=v&k=v...（非 JSON）。
	biz := map[string]string{
		"method":  complaintMethod,
		"version": complaintVersion,
		"poly":    poly,
		"mobile":  mobile,
	}
	plain := sortComplaintParams(biz)

	// AES 密钥/IV 由 appSecret 派生（见下方 deriveComplaintKeyIV 说明）。
	aesKey, aesIV := deriveComplaintKeyIV(c.cfg.SignSecret)
	param, err := encryptParam([]byte(plain), aesKey, aesIV)
	if err != nil {
		return complaintRecord{}, "", fmt.Errorf("encrypt complaint param: %w", err)
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
		return complaintRecord{}, "", fmt.Errorf("marshal complaint request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL, bytes.NewReader(payload))
	if err != nil {
		return complaintRecord{}, "", fmt.Errorf("build complaint request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	slog.Debug("complaint request",
		"url", c.cfg.BaseURL,
		"apiKey", c.cfg.APIKey,
		"poly", poly,
		"sign", env.Sign,
		"reqid", reqid,
	)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return complaintRecord{}, "", fmt.Errorf("complaint call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return complaintRecord{}, "", fmt.Errorf("read complaint body: %w", err)
	}
	slog.Debug("complaint response", "status", resp.StatusCode, "poly", poly, "raw", string(raw))

	var cr complaintResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return complaintRecord{}, "", fmt.Errorf("decode complaint body: %w", err)
	}
	if cr.Code != complaintSuccess {
		// 1001/1002/1010/1012/1013/1099/8000 均为我方账户/参数/系统/通道问题，视为
		// 上游侧错误：不计费。失败也带上游 token(请求号)落审计供对账追查 (uid=logId 同填)。
		return complaintRecord{}, cr.Token, busiErr(cr.Code, cr.Msg, cr.Token, cr.Token)
	}

	// code=0000：data 为 base64(gzip(JSON 数组))，解压+脱敏后取首条记录。
	rangeJSON, err := decodeComplaintData(cr.Data)
	if err != nil {
		return complaintRecord{}, cr.Token, busiErr(cr.Code, "解析结果集失败: "+err.Error(), cr.Token, cr.Token)
	}
	rec, ok := firstComplaintRecord(rangeJSON)
	if !ok {
		// 成功但无记录：按未命中处理 (文档：未命中仍 001，forbid=0)。
		rec = complaintRecord{Forbid: 0}
	}
	return rec, cr.Token, nil
}

// firstComplaintRecord 解析结果数组 JSON 字符串并返回首条记录。空串/空数组/非法
// JSON 返回 ok=false，由调用方决定兜底口径。
func firstComplaintRecord(rangeJSON string) (complaintRecord, bool) {
	if strings.TrimSpace(rangeJSON) == "" {
		return complaintRecord{}, false
	}
	var arr []complaintRecord
	if err := json.Unmarshal([]byte(rangeJSON), &arr); err != nil || len(arr) == 0 {
		return complaintRecord{}, false
	}
	return arr[0], true
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
