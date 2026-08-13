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
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/datahub/relay/internal/domain/model"
)

// IncomeAgConfig 是收入A_g版 (grgjj) 上游的 endpoint + 我方在该上游侧的凭证。
// 协议依据：docs/收入A_g版--ShowDoc.html（查询）+ 获取秘钥 ShowDoc
// (https://www.showdoc.com.cn/p/eb985dca7743ec9fef0636e7ab4957c2) + 上游官方 demo
// yrzx_common_demo。
//
// 两步协议（关键：3DES 密钥不是静态配置，而是先向「获取秘钥」接口动态换取）：
//  1. 获取秘钥 GET {host}/yrzx/secKey/info?account&reqid&verify
//     verify = MD5(account + reqid + key).toUpperCase()；响应 result.key 为「采用
//     3des+base64 加密方式」下发的动态 3DES 会话密钥（用商户 key 解密得真实密钥，见
//     deriveSessionKey），24 小时后失效需重新获取。
//  2. 查询 POST {host}/yrzx/common/v2/credit/v2，请求体 {account,type,data,reqid,verify}：
//     data   = Base64(3DES/ECB/PKCS5(加密前业务JSON {name,cid,mobile}))，密钥=步骤1的 key；
//     verify = MD5(account + 加密前业务JSON串 + reqid + type + key).toUpperCase()，key=SignKey。
//     响应 result 为 Base64(3DES 密文)，用同一动态密钥解密得 {cbjfzt,jfjs,jfsj}。
//
// 凭证：SignKey(商户 key) 用于两步的 MD5 verify 加签以及换取动态 3DES 密钥；3DES
// 密钥本身由上游动态下发，无需配置。StaticTripleDESKey 仅为联调/本地 mock 预置固定
// 密钥时的可选覆盖（配置里填了就跳过「获取秘钥」直接用它）。
type IncomeAgConfig struct {
	BaseURL   string // 查询接口 URL，如 https://host:port/yrzx/common/v2/credit/v2
	SecKeyURL string // 可选：获取秘钥接口 URL；缺省由 BaseURL 的 host 拼 /yrzx/secKey/info
	Account   string // 我方在上游侧的账户 (account)
	SignKey   string // 商户 key：MD5 verify 加签 + 换取动态 3DES 密钥（上游文档里的 key）
	Type      string // 接口类型；收入A_g版固定 "1106"，NewIncomeAg 缺省即填

	// StaticTripleDESKey 可选：填了则跳过「获取秘钥」直接用它当 3DES 密钥（Base64、
	// 解码后 24 字节）。生产留空走动态获取；本地 mock/联调可预置。
	StaticTripleDESKey string
}

// IncomeAgClient implements port.UpstreamPort for the 收入A_g版 (grgjj) provider。
type IncomeAgClient struct {
	cfg  IncomeAgConfig
	http *http.Client

	// 动态 3DES 密钥缓存（24h 失效）。mu 保护 key/exp，避免并发重复换取。
	mu     sync.Mutex
	key    []byte    // 已归一的 24 字节 DESede 密钥
	keyExp time.Time // 缓存过期时间（留足提前量）
}

// secKeyTTL：上游称密钥 24h 失效，本地缓存留 1h 提前量重取，避免边界期用到过期 key。
const secKeyTTL = 23 * time.Hour

// NewIncomeAg builds a 收入A_g版 upstream client（type 缺省 1106）。
func NewIncomeAg(cfg IncomeAgConfig, httpClient *http.Client) *IncomeAgClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if cfg.Type == "" {
		cfg.Type = "1106" // 接口类型，收入A_g版固定值 (ShowDoc §输入参数)
	}
	if cfg.SecKeyURL == "" {
		cfg.SecKeyURL = deriveSecKeyURL(cfg.BaseURL)
	}
	return &IncomeAgClient{cfg: cfg, http: httpClient}
}

// deriveSecKeyURL 由查询 URL 的 scheme+host 拼出「获取秘钥」URL（/yrzx/secKey/info）。
// 解析失败时按字符串替换兜底（查询路径固定为 /yrzx/common/v2/credit/v2）。
func deriveSecKeyURL(baseURL string) string {
	if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
		u.Path = "/yrzx/secKey/info"
		u.RawQuery = ""
		return u.String()
	}
	if i := strings.Index(baseURL, "/yrzx/"); i >= 0 {
		return baseURL[:i] + "/yrzx/secKey/info"
	}
	return baseURL
}

// incomeAgData 是加密前的业务 JSON (ShowDoc §data输入参数)：name/cid/mobile 均必填。
// cid 即身份证号（上游字段名为 cid，不是 idCard），字段名逐字对齐上游契约。
type incomeAgData struct {
	Name   string `json:"name"`
	Cid    string `json:"cid"`
	Mobile string `json:"mobile"`
}

// incomeAgRequest 是查询 POST 请求体外层信封 (ShowDoc §输入参数)。
type incomeAgRequest struct {
	Account string `json:"account"`
	Type    string `json:"type"`
	Data    string `json:"data"`
	Reqid   string `json:"reqid"`
	Verify  string `json:"verify"`
}

// incomeAgResponse 是查询响应外层 (ShowDoc §返回参数)。result 为加密字符串，需 3DES
// 解密。reqid 是我方回显，uid 才是上游流水号（唯一上游标识，供后台对账）。
type incomeAgResponse struct {
	Code   string `json:"code"`
	Msg    string `json:"msg"`
	UID    string `json:"uid"`
	Reqid  string `json:"reqid"`
	Result string `json:"result"`
	Verify string `json:"verify"`
}

// secKeyResponse 是「获取秘钥」响应。result.key 即动态 3DES 密钥。
type secKeyResponse struct {
	Code   string `json:"code"`
	Msg    string `json:"msg"`
	UID    string `json:"uid"`
	Reqid  string `json:"reqid"`
	Result struct {
		Key string `json:"key"`
	} `json:"result"`
	Verify string `json:"verify"`
}

// Query 先拿到 3DES 密钥（静态覆盖或动态获取，带缓存），再组装并发送签名 POST，
// 归一化响应：code 001 → 查得 (range = 解密后 result 的 compact JSON，透出
// {cbjfzt,jfjs,jfsj})，999 → 查无；其余 (002/003/004/009/011/012/013/020 等) →
// 上游侧错误 (不计费，交由 orchestrator 走复查/对账兜底)。
func (c *IncomeAgClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	reqid := req.Reqid
	if len(reqid) > 20 {
		reqid = reqid[:20] // 上游约束 reqid ≤20 位
	}

	key, err := c.tripleDESKeyFor(ctx, reqid)
	if err != nil {
		return nil, err // getKey 已按网络/业务失败返回合适的错误类型（含上游标识）
	}

	// 加密前业务 JSON 串：加密与加签必须用**同一份字节**——上游会解密 data 得到该串
	// 再重算 verify 比对 (加密版本.java: data=enc(str)，verify=MD5(account+str+...))。
	plain, err := json.Marshal(incomeAgData{Name: req.Name, Cid: req.IDCard, Mobile: req.Mobile})
	if err != nil {
		return nil, fmt.Errorf("marshal incomeag data: %w", err)
	}

	data, err := tripleDESEncryptBase64Key(plain, key)
	if err != nil {
		return nil, fmt.Errorf("encrypt incomeag data: %w", err)
	}
	verify := signIncomeAg(c.cfg.Account, string(plain), reqid, c.cfg.Type, c.cfg.SignKey)

	body, err := json.Marshal(incomeAgRequest{
		Account: c.cfg.Account,
		Type:    c.cfg.Type,
		Data:    data,
		Reqid:   reqid,
		Verify:  verify,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal incomeag request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build incomeag request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json;charset=utf-8")

	slog.Debug("incomeag request", "url", c.cfg.BaseURL, "account", c.cfg.Account, "reqid", reqid, "type", c.cfg.Type)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("incomeag call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read incomeag body: %w", err)
	}
	slog.Debug("incomeag response", "status", resp.StatusCode, "raw", string(raw))

	var ir incomeAgResponse
	if err := json.Unmarshal(raw, &ir); err != nil {
		return nil, fmt.Errorf("decode incomeag body: %w", err)
	}

	switch ir.Code {
	case "001":
		rng, err := c.decodeResult(ir.Result, key)
		if err != nil {
			// 已应答成功码但结果解密失败——密钥可能已失效，作废缓存下次重取；按上游侧
			// 错误处理，带上 uid 供对账追查。
			c.invalidateKey()
			return nil, busiErr("001", "result 解密失败: "+err.Error(), ir.UID, ir.UID)
		}
		return &model.UpstreamResult{
			Code:  "001",
			Msg:   "成功",
			UID:   ir.UID,
			Reqid: reqid,
			LogID: ir.UID, // 上游仅 uid(流水号) 一个标识，UID/LogID 同填供后台对账 (reqid 是我方回显不算)
			Range: rng,
		}, nil
	case "999":
		return &model.UpstreamResult{
			Code:  "999",
			Msg:   "无结果返回",
			UID:   ir.UID,
			Reqid: reqid,
			LogID: ir.UID,
		}, nil
	default:
		// 011 verify 错误 / 013 校验签名错误 常因动态密钥或签名过期——作废缓存下次重取。
		if ir.Code == "011" || ir.Code == "013" {
			c.invalidateKey()
		}
		// 002/003/004/009/011/012/013/020 等均为我方在上游侧的账户/参数/系统问题，
		// 视为上游侧错误：不计费。失败也带上游 uid 落审计供对账追查（禁止裸 fmt.Errorf）。
		return nil, busiErr(ir.Code, ir.Msg, ir.UID, ir.UID)
	}
}

// tripleDESKeyFor 返回本次请求使用的 24 字节 3DES 密钥：配置了静态密钥则用它；否则
// 走「获取秘钥」接口（带缓存，24h 内复用）。
func (c *IncomeAgClient) tripleDESKeyFor(ctx context.Context, reqid string) ([]byte, error) {
	if strings.TrimSpace(c.cfg.StaticTripleDESKey) != "" {
		return tripleDESKey(c.cfg.StaticTripleDESKey) // Base64→24 字节（联调/mock 覆盖）
	}
	return c.getKey(ctx, reqid)
}

// getKey 换取动态 3DES 密钥并缓存。命中未过期缓存直接返回；否则 GET /yrzx/secKey/info，
// verify=MD5(account+reqid+key).toUpperCase()，取 result.key 归一为 24 字节。
func (c *IncomeAgClient) getKey(ctx context.Context, reqid string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.key) == 24 && time.Now().Before(c.keyExp) {
		return c.key, nil
	}

	// 获取秘钥用独立的短流水号（与查询 reqid 区分，≤20 位）。
	skReqid := "sk" + reqid
	if len(skReqid) > 20 {
		skReqid = skReqid[len(skReqid)-20:]
	}
	verify := signSecKey(c.cfg.Account, skReqid, c.cfg.SignKey)

	u, err := url.Parse(c.cfg.SecKeyURL)
	if err != nil {
		return nil, fmt.Errorf("parse secKey url: %w", err)
	}
	q := u.Query()
	q.Set("account", c.cfg.Account)
	q.Set("reqid", skReqid)
	q.Set("verify", verify)
	u.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build secKey request: %w", err)
	}
	slog.Debug("incomeag secKey request", "url", c.cfg.SecKeyURL, "account", c.cfg.Account, "reqid", skReqid)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("secKey call: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read secKey body: %w", err)
	}
	slog.Debug("incomeag secKey response", "status", resp.StatusCode, "raw", string(raw))

	var sr secKeyResponse
	if err := json.Unmarshal(raw, &sr); err != nil {
		return nil, fmt.Errorf("decode secKey body (http %d): %w", resp.StatusCode, err)
	}
	if sr.Code != "001" {
		// 获取秘钥失败（002 账号不存在/004 未授权/013 签名错误…）→ 上游侧错误，带 uid 落审计。
		return nil, busiErr(sr.Code, "获取秘钥失败: "+sr.Msg, sr.UID, sr.UID)
	}

	// result.key 按 ShowDoc「获取密钥……采用 3des+base64 加密方式」用商户 key 解密得到
	// 真实会话密钥（deriveSessionKey 内含明文回退），再用于查询 data 的 3DES 加解密。
	key, err := deriveSessionKey(sr.Result.Key, c.cfg.SignKey)
	if err != nil {
		return nil, busiErr("E_SECKEY", "获取秘钥归一失败: "+err.Error(), sr.UID, sr.UID)
	}
	c.key = key
	c.keyExp = time.Now().Add(secKeyTTL)
	return key, nil
}

// invalidateKey 作废缓存的动态密钥，令下次请求重新获取。
func (c *IncomeAgClient) invalidateKey() {
	c.mu.Lock()
	c.key = nil
	c.keyExp = time.Time{}
	c.mu.Unlock()
}

// decodeResult 用 3DES 密钥解开响应 result 密文并把明文归整为 compact JSON 透出下游
// body.result.range（json 对象口径：原样透出 {cbjfzt,jfjs,jfsj}，不丢字段）。上游偶发
// result 为空串（查得但无业务体）时返回空 range，不视为错误。
func (c *IncomeAgClient) decodeResult(result string, key []byte) (string, error) {
	if strings.TrimSpace(result) == "" {
		return "", nil
	}
	plain, err := tripleDESDecryptBase64Key(result, key)
	if err != nil {
		return "", err
	}
	// 透传：把解密后的业务 JSON 压紧后整体作为 range（含 cbjfzt/jfjs/jfsj）。
	return compactJSON(plain), nil
}

// Requery: 收入A_g版上游以 reqid 幂等，真正的对账查询接口待联调。在此之前返回
// Reachable=false，记录保持 PENDING 由对账兜底（与既有上游一致）。
func (c *IncomeAgClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}

// signIncomeAg 计算收入A_g版查询请求签名 (ShowDoc §输入参数 / demo 加密版本.java)：
//
//	verify = MD5(account + 加密前业务JSON串 + reqid + type + key).toUpperCase()
func signIncomeAg(account, plainJSON, reqid, typ, key string) string {
	sum := md5.Sum([]byte(account + plainJSON + reqid + typ + key))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// signSecKey 计算「获取秘钥」请求签名 (获取秘钥 ShowDoc §输入参数)：
//
//	verify = MD5(account + reqid + key).toUpperCase()
func signSecKey(account, reqid, key string) string {
	sum := md5.Sum([]byte(account + reqid + key))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}
