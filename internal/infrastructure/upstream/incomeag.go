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
	"strings"

	"github.com/datahub/relay/internal/domain/model"
)

// IncomeAgConfig 是收入A_g版 (grgjj) 上游的 endpoint + 我方在该上游侧的凭证。
// 协议依据：docs/收入A_g版--ShowDoc.html + 上游官方 demo yrzx_common_demo
// (Main.java / 加密版本.java / ThreeDesUtil.java)。
//   - HTTP POST，Content-Type application/json；请求体 {account,type,data,reqid,verify}。
//   - data   = Base64(3DES/ECB/PKCS5(加密前业务JSON {name,cid,mobile}))，密钥=TripleDESKey。
//   - verify = MD5(account + 加密前业务JSON串 + reqid + type + key).toUpperCase()，key=SignKey。
//   - 响应 result 为 Base64(3DES 密文)，用同一 TripleDESKey 解密得 {cbjfzt,jfjs,jfsj}。
// 注意本上游有两把独立凭证：SignKey(MD5 加签用的 key) 与 TripleDESKey(3DES 加解密
// 用的密钥)，二者不同，均由上游商户分配。
type IncomeAgConfig struct {
	BaseURL      string // 接口 URL，如 http://server:port/yrzx/common/v2/credit/v2
	Account      string // 我方在上游侧的账户 (account)
	SignKey      string // MD5 verify 加签密钥 (上游文档/demo 里的 key)
	TripleDESKey string // 3DES 密钥 (Base64 编码，解码后 24 字节)，加密 data + 解密 result
	Type         string // 接口类型；收入A_g版固定 "1106"，NewIncomeAg 缺省即填
}

// IncomeAgClient implements port.UpstreamPort for the 收入A_g版 (grgjj) provider:
// JSON POST + 3DES 加密 data + MD5 加签，返回 code/msg/uid/result（result 为 3DES 密文）。
type IncomeAgClient struct {
	cfg  IncomeAgConfig
	http *http.Client
}

// NewIncomeAg builds a 收入A_g版 upstream client（type 缺省 1106）。
func NewIncomeAg(cfg IncomeAgConfig, httpClient *http.Client) *IncomeAgClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if cfg.Type == "" {
		cfg.Type = "1106" // 接口类型，收入A_g版固定值 (ShowDoc §输入参数)
	}
	return &IncomeAgClient{cfg: cfg, http: httpClient}
}

// incomeAgData 是加密前的业务 JSON (ShowDoc §data输入参数)：name/cid/mobile 均必填。
// cid 即身份证号（上游字段名为 cid，不是 idCard），字段名逐字对齐上游契约。
type incomeAgData struct {
	Name   string `json:"name"`
	Cid    string `json:"cid"`
	Mobile string `json:"mobile"`
}

// incomeAgRequest 是 POST 请求体外层信封 (ShowDoc §输入参数)。
type incomeAgRequest struct {
	Account string `json:"account"`
	Type    string `json:"type"`
	Data    string `json:"data"`
	Reqid   string `json:"reqid"`
	Verify  string `json:"verify"`
}

// incomeAgResponse 是响应外层 (ShowDoc §返回参数)。result 为加密字符串，需 3DES 解密。
// reqid 是我方回显，uid 才是上游流水号（唯一上游标识，供后台对账）。
type incomeAgResponse struct {
	Code   string `json:"code"`
	Msg    string `json:"msg"`
	UID    string `json:"uid"`
	Reqid  string `json:"reqid"`
	Result string `json:"result"`
	Verify string `json:"verify"`
}

// Query 组装并发送签名 POST，归一化响应：code 001 → 查得 (range = 解密后 result 的
// compact JSON，透出 {cbjfzt,jfjs,jfsj})，999 → 查无；其余 (002 账号不存在/003 余额
// 不足/004 未授权/020 参数错误/009 账号为空/011 verify 错误/012 接口错误/013 校验签名
// 错误 等) → 上游侧错误 (不计费，交由 orchestrator 走复查/对账兜底)。
func (c *IncomeAgClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	reqid := req.Reqid
	if len(reqid) > 20 {
		reqid = reqid[:20] // 上游约束 reqid ≤20 位
	}

	// 加密前业务 JSON 串：加密与加签必须用**同一份字节**——上游会解密 data 得到该串
	// 再重算 verify 比对 (加密版本.java: data=enc(str)，verify=MD5(account+str+...))。
	plain, err := json.Marshal(incomeAgData{Name: req.Name, Cid: req.IDCard, Mobile: req.Mobile})
	if err != nil {
		return nil, fmt.Errorf("marshal incomeag data: %w", err)
	}

	data, err := tripleDESEncryptBase64(plain, c.cfg.TripleDESKey)
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
		rng, err := c.decodeResult(ir.Result)
		if err != nil {
			// 已应答成功码但结果解密失败——按上游侧错误处理，带上 uid 供对账追查。
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
		// 002/003/004/009/011/012/013/020 等均为我方在上游侧的账户/参数/系统问题，
		// 视为上游侧错误：不计费。失败也带上游 uid 落审计供对账追查（禁止裸 fmt.Errorf）。
		return nil, busiErr(ir.Code, ir.Msg, ir.UID, ir.UID)
	}
}

// decodeResult 用 3DES 密钥解开响应 result 密文并把明文归整为 compact JSON 透出下游
// body.result.range（json 对象口径：原样透出 {cbjfzt,jfjs,jfsj}，不丢字段）。上游偶发
// result 为空串（查得但无业务体）时返回空 range，不视为错误。
func (c *IncomeAgClient) decodeResult(result string) (string, error) {
	if strings.TrimSpace(result) == "" {
		return "", nil
	}
	plain, err := tripleDESDecryptBase64(result, c.cfg.TripleDESKey)
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

// signIncomeAg 计算收入A_g版请求签名 (ShowDoc §输入参数 / demo 加密版本.java)：
//
//	verify = MD5(account + 加密前业务JSON串 + reqid + type + key).toUpperCase()
func signIncomeAg(account, plainJSON, reqid, typ, key string) string {
	sum := md5.Sum([]byte(account + plainJSON + reqid + typ + key))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}
