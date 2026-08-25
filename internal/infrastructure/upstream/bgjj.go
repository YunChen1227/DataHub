package upstream

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // 上游备用公积金源契约指定 MD5 加签，非我方选型
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/datahub/relay/internal/domain/model"
	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// BgJJConfig 是「备用公积金」源 (bgjj) 的 endpoint + 我方在该上游侧的凭证。
// 协议依据：docs/备用公积金1/ 下官方 java demo (TestDemo/SignUtil/HttpUtil/AESUtil)
// 与对接说明 txt。该源与 grgjj 主源 (收入A_g版 incomeag) 提供**同一种数据**(公积金
// 缴存)，作为**优先级更低的备源**参与串行寻源 (主源查无/失败才回落到本源)。
//
// 协议 (JSON POST + HTTPS 双向认证 P12)：
//   - 请求体 {merchant_id, timestamp, dsorderid, params, sign}；
//   - params 走**明文对象** {name, idcard, mobile} (demo 默认 isEncrpt=false；HTTPS
//     已防中间人，AES 为可选项，本实现用明文——与已实测通过的路径一致)；
//   - sign = MD5("k1=v1&k2=v2&...&key=merchantKey")：顶层键 ASCII 升序、剔空值与
//     sign，value 为 Java map toString 形态 (params 段即 {name=..., idcard=..., mobile=...})；
//   - 传输走双向 TLS：客户端 P12 证书 (certPath/certPass) 作为 client cert。
//
// 响应 {code, message, data, orderid, dsorderid}：
//   - code=100 查询成功 → data {date, score, jfzt}；
//   - code=201 查无记录 → data {}；
//   - 其余 (301 非白名单IP 等) → 上游侧错误。
//
// 字段映射 (归一到 grgjj 既有下游契约 {cbjfzt, jfjs, jfsj}，使下游无从察觉数据来自
// 哪个源)：jfzt→cbjfzt (缴费状态)、score→jfjs (缴存基数评分)、date→jfsj (缴费时间)。
type BgJJConfig struct {
	BaseURL     string // 查询接口 URL，如 https://pf.jeoho.com/api/nlv2/zl4
	MerchantID  string // 商户号 (merchant_id)
	MerchantKey string // 商户密钥 (MD5 加签 key)
	CertPath    string // P12 客户端证书文件路径 (双向认证)；空则用传入的 http.Client (mock/联调)
	CertPass    string // P12 证书密码
}

// BgJJClient implements port.UpstreamPort for the 备用公积金 (bgjj) provider。
type BgJJClient struct {
	cfg  BgJJConfig
	http *http.Client
}

// NewBgJJ builds a 备用公积金 upstream client。certPath 非空时加载 P12 客户端证书构建
// 独立的双向认证 http.Client；为空 (mock/memory 联调，明文 HTTP) 时复用传入的 client。
func NewBgJJ(cfg BgJJConfig, httpClient *http.Client) (*BgJJClient, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if strings.TrimSpace(cfg.CertPath) != "" {
		client, err := newP12Client(cfg.CertPath, cfg.CertPass, httpClient.Timeout)
		if err != nil {
			return nil, fmt.Errorf("bgjj 加载 P12 证书失败: %w", err)
		}
		httpClient = client
	}
	return &BgJJClient{cfg: cfg, http: httpClient}, nil
}

// newP12Client 从 .p12 文件加载客户端证书，构建启用双向 TLS 的 http.Client。
func newP12Client(certPath, certPass string, timeout time.Duration) (*http.Client, error) {
	raw, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("读取 P12 文件 %s: %w", certPath, err)
	}
	key, cert, caCerts, err := pkcs12.DecodeChain(raw, certPass)
	if err != nil {
		return nil, fmt.Errorf("解析 P12 (密码是否正确?): %w", err)
	}
	tlsCert := tls.Certificate{
		Certificate: [][]byte{cert.Raw},
		PrivateKey:  key,
		Leaf:        cert,
	}
	for _, ca := range caCerts {
		tlsCert.Certificate = append(tlsCert.Certificate, ca.Raw)
	}
	if timeout <= 0 {
		timeout = 6 * time.Second
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates: []tls.Certificate{tlsCert},
				MinVersion:   tls.VersionTLS12,
			},
		},
	}, nil
}

// bgjjResponse 是查询响应外层。orderid 是上游订单号 (唯一上游标识，供后台对账)；
// dsorderid 是我方生成回显 (不算上游标识)。
type bgjjResponse struct {
	Code      string          `json:"code"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data"`
	OrderID   string          `json:"orderid"`
	DsOrderID string          `json:"dsorderid"`
}

// bgjjData 是 code=100 时的业务数据。字段名逐字对齐上游 demo/实测响应。
type bgjjData struct {
	Date  string `json:"date"`  // 缴费时间 → jfsj
	Score string `json:"score"` // 缴存基数评分 → jfjs
	Jfzt  string `json:"jfzt"`  // 缴费状态 → cbjfzt
}

// Query 组装明文 params 请求并加签发送，归一化响应：code=100 → 查得 (range 归一为
// {cbjfzt,jfjs,jfsj} 的 compact JSON，与 grgjj 主源同形)；201 → 查无；其余 → 上游侧
// 错误 (不计费，带 orderid 落审计供对账)。
func (c *BgJJClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	// 业务参数：明文三要素。key 顺序 name→idcard→mobile 必须与加签一致 (见 signBgJJ)。
	params := []kv{
		{"name", req.Name},
		{"idcard", req.IDCard},
		{"mobile", req.Mobile},
	}

	ts := time.Now().UnixMilli()
	dsorderid := fmt.Sprintf("%d%s", ts, randAlnum(11))

	// 顶层系统参数 (merchant_id/timestamp/dsorderid) + params(明文对象)。
	top := []kv{
		{"dsorderid", dsorderid},
		{"merchant_id", c.cfg.MerchantID},
		{"params", javaMapString(params)}, // 仅用于加签 (Java map toString 形态)
		{"timestamp", fmt.Sprintf("%d", ts)},
	}
	sign := signBgJJ(top, c.cfg.MerchantKey)

	// 实际请求体：params 是明文 JSON 对象 (非加签用的 toString 形态)，键序与加签一致。
	bodyMap := map[string]any{
		"merchant_id": c.cfg.MerchantID,
		"timestamp":   ts,
		"dsorderid":   dsorderid,
		"params":      orderedParamsJSON(params),
		"sign":        sign,
	}
	body, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("marshal bgjj request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build bgjj request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json;charset=UTF-8")

	slog.Debug("bgjj request", "url", c.cfg.BaseURL, "merchant", c.cfg.MerchantID, "dsorderid", dsorderid)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("bgjj call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read bgjj body: %w", err)
	}
	slog.Debug("bgjj response", "status", resp.StatusCode, "raw", string(raw))

	var br bgjjResponse
	if err := json.Unmarshal(raw, &br); err != nil {
		return nil, fmt.Errorf("decode bgjj body: %w", err)
	}

	switch br.Code {
	case "100": // 查询成功 → 查得
		rng, err := c.normalizeRange(br.Data)
		if err != nil {
			// 已应答成功码但业务数据异常，按上游侧错误处理，带 orderid 供对账。
			return nil, busiErr("100", "data 解析失败: "+err.Error(), br.OrderID, br.OrderID)
		}
		return &model.UpstreamResult{
			Code:  "001",
			Msg:   "成功",
			UID:   br.OrderID,
			Reqid: req.Reqid,
			LogID: br.OrderID, // 上游仅 orderid 一个标识，UID/LogID 同填 (dsorderid 是我方回显不算)
			Range: rng,
		}, nil
	case "201": // 查无记录
		return &model.UpstreamResult{
			Code:  "999",
			Msg:   "无结果返回",
			UID:   br.OrderID,
			Reqid: req.Reqid,
			LogID: br.OrderID,
		}, nil
	default:
		// 301 非白名单IP / 其余 → 上游侧错误 (不计费)。失败也带 orderid 落审计供对账
		// 追查 (禁止裸 fmt.Errorf)。
		return nil, busiErr(br.Code, br.Message, br.OrderID, br.OrderID)
	}
}

// normalizeRange 把备用源的 {date,score,jfzt} 归一为 grgjj 既有下游契约
// {cbjfzt,jfjs,jfsj} 的 compact JSON——严格白名单 (只输出契约字段)，使下游 result.range
// 与主源 (incomeag) **完全同形**，无从察觉数据来自哪个源。上游偶发 data 为空对象
// (查得但无业务体) 时返回空 range，不视为错误。
func (c *BgJJClient) normalizeRange(data json.RawMessage) (string, error) {
	if len(data) == 0 || string(data) == "null" || string(data) == "{}" {
		return "", nil
	}
	var d bgjjData
	if err := json.Unmarshal(data, &d); err != nil {
		return "", err
	}
	// 语义映射 (逐字段核对)：jfzt→cbjfzt、score→jfjs、date→jfsj。
	out, err := json.Marshal(map[string]string{
		"cbjfzt": d.Jfzt,
		"jfjs":   d.Score,
		"jfsj":   d.Date,
	})
	if err != nil {
		return "", err
	}
	return compactJSON(out), nil
}

// Requery: 备用公积金源以 dsorderid 幂等，真正的对账查询接口待联调。在此之前返回
// Reachable=false，记录保持 PENDING 由对账兜底 (与既有上游一致)。
func (c *BgJJClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}

// kv 是一个有序键值对 (加签与请求体都需固定键序)。
type kv struct {
	k string
	v string
}

// signBgJJ 计算备用公积金源签名 (对齐官方 demo SignUtil.getSign)：
//
//	sign = MD5("k1=v1&k2=v2&...&key=merchantKey")
//
// 其中键按 ASCII 升序、剔除空值与 sign。传入的 top 必须已是升序键序。
func signBgJJ(top []kv, merchantKey string) string {
	sorted := make([]kv, len(top))
	copy(sorted, top)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].k < sorted[j].k })
	var b strings.Builder
	for _, p := range sorted {
		if p.k == "" || p.v == "" || p.k == "sign" {
			continue
		}
		b.WriteString(p.k)
		b.WriteByte('=')
		b.WriteString(p.v)
		b.WriteByte('&')
	}
	b.WriteString("key=")
	b.WriteString(merchantKey)
	sum := md5.Sum([]byte(b.String())) //nolint:gosec
	return hex.EncodeToString(sum[:])  // 小写 hex (demo DigestUtils.md5Hex)
}

// javaMapString 把有序键值对序列化为 Java LinkedHashMap.toString() 形态
// ("{k1=v1, k2=v2}")——加签时 params 段的取值必须与上游服务端解析后 toString 一致。
func javaMapString(pairs []kv) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, p := range pairs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p.k)
		b.WriteByte('=')
		b.WriteString(p.v)
	}
	b.WriteByte('}')
	return b.String()
}

// orderedParamsJSON 返回一个保持键序的 json.RawMessage，使请求体里的 params 对象键序
// (name→idcard→mobile) 与加签用的 toString 一致 (上游解析后 toString 才能重算出同一 sign)。
func orderedParamsJSON(pairs []kv) json.RawMessage {
	var b strings.Builder
	b.WriteByte('{')
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(p.k)
		vb, _ := json.Marshal(p.v)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return json.RawMessage(b.String())
}
