package upstream

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/datahub/relay/internal/domain/model"
)

// 证通 entcreditapi 平台四个产品码 (docs/发票数据聚合查询-part1/2.pdf 与
// docs/税务数据聚合查询-part1/2.pdf)，共用同一端点
// /ectcispserver/api/entcreditapi/query。协议/鉴权见 docs/java-api-demo 官方示例：
// version/msgId/orgCode/accessKeyId/timestamp/args/signature 表单提交，
// HMAC-SHA256 签名 (SignedRequestsHelper.java)。
const (
	entCreditInvoice1 = "P0130081" // 发票数据聚合-part1
	entCreditInvoice2 = "P0130083" // 发票数据聚合-part2
	entCreditTax1     = "P0130082" // 税务数据聚合-part1
	entCreditTax2     = "P0130084" // 税务数据聚合-part2

	entCreditRequestURI = "/ectcispserver/api/entcreditapi/query"
	entCreditVersion    = "1.0"
	entCreditQueryMode  = "0"
)

// entCreditSectionKey maps 产品码 → range 合并 JSON 里的段名。
var entCreditSectionKey = map[string]string{
	entCreditInvoice1: "invoice1",
	entCreditInvoice2: "invoice2",
	entCreditTax1:     "tax1",
	entCreditTax2:     "tax2",
}

// msgSeq 提供 msgId 的 6 位序号（进程内原子自增，启动时以纳秒时间播种，
// 保证并发子请求与同秒连续请求的 msgId 互不相同，满足平台唯一性要求）。
var msgSeq = func() *atomic.Uint64 {
	var v atomic.Uint64
	v.Store(uint64(time.Now().UnixNano()))
	return &v
}()

// EntCreditConfig holds the 证通 entcreditapi 聚合平台的 endpoint + 我方凭证。
// Endpoint 只含 scheme+host(:port)，不含 requestUri（requestUri 固定，签名时
// 需要把它与 endpoint 分开参与拼接，见 sign()）。四个产品码共用同一凭证。
type EntCreditConfig struct {
	Endpoint        string // 如 https://cisp.zenitera.com（不同环境域名不同，见官方 demo 注释）
	OrgCode         string // 机构代码
	AccessKeyID     string // AK
	SecretAccessKey string // SK（Base64 编码，签名时先 Base64 解码取原始字节）
	Products        []string // 为空时默认四产品全查
}

// EntCreditClient implements port.UpstreamPort for the swfp 聚合路由：并发查询
// N 个产品码，逐源归一后聚合 (add-upstream-multi skill 判定表)：
//   - 全部成功应答且 ≥1 份查得 → "001"（计费）
//   - 全部成功应答且全部查无   → "999"
//   - 部分成功部分失败         → "002"（部分数据源成功，不计费，range 带成功段）
//   - 全部失败                 → error（505062，走复查/对账）
type EntCreditClient struct {
	cfg  EntCreditConfig
	http *http.Client
}

// NewEntCredit builds the 证通 entcreditapi 聚合 client.
func NewEntCredit(cfg EntCreditConfig, httpClient *http.Client) *EntCreditClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	// 官方 demo 的 getSingleSSLConnection() 关闭证书校验；部分环境用自签证书，
	// 这里保留同等行为以避免联调时握手失败（生产如需严格校验可在装配层另传 client）。
	if httpClient.Transport == nil {
		httpClient.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
	}
	if len(cfg.Products) == 0 {
		cfg.Products = []string{entCreditInvoice1, entCreditInvoice2, entCreditTax1, entCreditTax2}
	}
	return &EntCreditClient{cfg: cfg, http: httpClient}
}

// entCreditSection is one 子源的归一结果，聚合进 range JSON 的一段。
type entCreditSection struct {
	Status string          `json:"status"`              // ok=查得 / empty=查无 / error=该源失败
	Raw    string          `json:"rawStatus,omitempty"`  // 上游 resultCode 原值 (透出备查)
	Data   json.RawMessage `json:"data,omitempty"`       // 上游 resultData 原样透出
	Error  string          `json:"error,omitempty"`      // status=error 时的原因摘要
}

// Query 并发调四个产品码并按判定表聚合 (见类型注释)。
func (c *EntCreditClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	type sub struct {
		key     string
		section entCreditSection
		orderNo string
	}
	results := make([]sub, len(c.cfg.Products))
	var wg sync.WaitGroup
	for i, product := range c.cfg.Products {
		wg.Add(1)
		go func(i int, product string) {
			defer wg.Done()
			key := entCreditSectionKey[product]
			if key == "" {
				key = product
			}
			sec, orderNo := c.callProduct(ctx, product, req)
			results[i] = sub{key: key, section: sec, orderNo: orderNo}
		}(i, product)
	}
	wg.Wait()

	var okCnt, emptyCnt, errCnt int
	uid := ""
	sections := make(map[string]entCreditSection, len(results))
	for _, r := range results {
		sections[r.key] = r.section
		if uid == "" && r.orderNo != "" {
			uid = r.orderNo
		}
		switch r.section.Status {
		case "ok":
			okCnt++
		case "empty":
			emptyCnt++
		default:
			errCnt++
		}
	}
	slog.Debug("entcredit aggregate", "reqid", req.Reqid, "ok", okCnt, "empty", emptyCnt, "err", errCnt)

	if errCnt == len(results) {
		return nil, fmt.Errorf("entcredit 全部数据源失败 (reqid=%s)", req.Reqid)
	}

	merged, err := json.Marshal(sections)
	if err != nil {
		return nil, fmt.Errorf("entcredit 聚合序列化失败: %w", err)
	}

	switch {
	case errCnt > 0:
		return &model.UpstreamResult{
			Code:  "002",
			Msg:   "部分数据源成功",
			UID:   uid,
			Reqid: req.Reqid,
			Range: string(merged),
		}, nil
	case okCnt > 0:
		return &model.UpstreamResult{
			Code:  "001",
			Msg:   "成功",
			UID:   uid,
			Reqid: req.Reqid,
			Range: string(merged),
		}, nil
	default:
		return &model.UpstreamResult{
			Code:  "999",
			Msg:   "查无结果",
			UID:   uid,
			Reqid: req.Reqid,
		}, nil
	}
}

// entCreditResponse mirrors 平台响应外层字段 (docs/java-api-demo Main.java +
// 附录错误码表)。resultCode "00000" = 成功；resultData 结构随产品而异，透出原样
// json.RawMessage，段级判定看 <产品码>Status (4=有结果/1=无结果/其余=异常)。
type entCreditResponse struct {
	OrderNo    string          `json:"orderNo"`
	ResultData json.RawMessage `json:"resultData"`
	ResultCode string          `json:"resultCode"`
	ResultDesc string          `json:"resultDesc"`
}

// callProduct 调一个产品码并归一为 section，严格对齐 docs/java-api-demo 官方示例：
//   - 待签名串 = POST\n<endpoint>\n<requestUri>\n<version>\n<msgId>\n<orgCode>\n
//     <accessKeyId>\n<timestamp>\n<args>（args 参与签名时不做 URLEncode）
//   - HMAC-SHA256，密钥为 secretAccessKey 的 Base64 解码结果；签名结果先 Base64
//     编码，再对整体做一次 URLEncode（SignedRequestsHelper.hmac + percentEncodeRfc3986）
//   - 报文以 application/x-www-form-urlencoded 提交；args 字段在放入表单前也需
//     URLEncode（Main.java 注释：加签时不 encode，放入报文时才 encode）
//
// entInfo 是本路由的业务查询参数，与 prodCode 一起放进 args JSON——上游文档示例
// args={"prodCode":"P0010010","entInfo":"<统一社会信用代码>"}。
func (c *EntCreditClient) callProduct(ctx context.Context, product string, req *model.UpstreamRequest) (entCreditSection, string) {
	fail := func(msg string) (entCreditSection, string) {
		return entCreditSection{Status: "error", Error: msg}, ""
	}

	// msgId 格式为「机构代码+8位日期+6位序号」且必须全局唯一（文档 E1006：重复提交
	// 导致 msgId 重复会被拒）。官方 demo 用 HHmmss 充当 6 位序号——但本客户端四个
	// 产品码并发出请求，同一秒内会产生相同 msgId，故序号改用进程内原子自增
	// （以启动纳秒时间播种，降低跨进程/重启撞号概率）。timestamp 字段仍用真实时间
	// （E1014：与服务器时差超 5 分钟被拒）。
	timestamp := time.Now().Format("20060102150405")
	msgID := c.cfg.OrgCode + time.Now().Format("20060102") + fmt.Sprintf("%06d", msgSeq.Add(1)%1000000)

	args, err := json.Marshal(map[string]string{
		"prodCode": product,
		"entInfo":  req.EntInfo,
	})
	if err != nil {
		return fail("marshal args: " + err.Error())
	}

	sig, err := c.sign(entCreditRequestURI, msgID, timestamp, string(args))
	if err != nil {
		return fail("sign: " + err.Error())
	}

	// 官方 demo（Main.java 注释"特别注意"）里 args 与 signature 都会被 URLEncode 两次：
	//   - args：业务 JSON（原始未编码）→ 手动 URLEncoder.encode() 一次 → 表单再编码一次。
	//   - signature：sign() 内部（percentEncodeRfc3986）已编码一次 → 直接放入表单，
	//     表单编码是第二次——sig 变量本身不能再手动编码，否则变成三次。
	// url.Values.Encode() 只编码一次，故 args 需要先手动 QueryEscape 补上第一次；
	// signature 的"第一次"已经在 sign() 内完成，这里直接 Set 走表单的第二次编码。
	form := url.Values{}
	form.Set("version", entCreditVersion)
	form.Set("msgId", msgID)
	form.Set("orgCode", c.cfg.OrgCode)
	form.Set("accessKeyId", c.cfg.AccessKeyID)
	form.Set("transTime", timestamp)
	form.Set("timestamp", timestamp)
	form.Set("args", url.QueryEscape(string(args)))
	form.Set("signature", sig)
	form.Set("queryMode", entCreditQueryMode)

	fullURL := strings.TrimRight(c.cfg.Endpoint, "/") + entCreditRequestURI
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fail("build request: " + err.Error())
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")

	slog.Debug("entcredit request", "url", fullURL, "prodCode", product, "msgId", msgID)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fail("call: " + err.Error())
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fail("read body: " + err.Error())
	}
	slog.Debug("entcredit response", "status", resp.StatusCode, "raw", string(raw))

	var er entCreditResponse
	if err := json.Unmarshal(raw, &er); err != nil {
		return fail(fmt.Sprintf("decode body (http %d): %s", resp.StatusCode, err.Error()))
	}
	// 附录错误码表：00000 = 查询成功；其余 (E0001-E1027) 均视为该数据源失败。
	if er.ResultCode != "00000" {
		return fail(fmt.Sprintf("resultCode=%s desc=%s", er.ResultCode, er.ResultDesc))
	}

	// resultData 结构随产品码变化 (<产品码>Status / <产品码>Data)；状态码
	// 4=查询成功有结果 / 1=查询成功无结果 / 3=查询失败 (docs 附录"状态码"表)。
	var resultData map[string]json.RawMessage
	if err := json.Unmarshal(er.ResultData, &resultData); err != nil {
		return fail("decode resultData: " + err.Error())
	}
	var status string
	if s, ok := resultData[product+"Status"]; ok {
		_ = json.Unmarshal(s, &status)
	}
	switch status {
	case "4":
		dataNode, ok := resultData[product+"Data"]
		if !ok || len(dataNode) == 0 {
			return fail("状态码4但缺少 " + product + "Data")
		}
		return entCreditSection{Status: "ok", Raw: status, Data: dataNode}, er.OrderNo
	case "1":
		return entCreditSection{Status: "empty", Raw: status}, er.OrderNo
	default:
		return fail(fmt.Sprintf("状态码=%s (预期4/1)", status))
	}
}

// sign 复刻 docs/java-api-demo/.../SignedRequestsHelper.java 的算法：
//
//	待签名串 = "POST\n" + endpoint + "\n" + requestUri + "\n" + version + "\n"
//	         + msgId + "\n" + orgCode + "\n" + accessKeyId + "\n" + timestamp + "\n" + args
//	sig      = URLEncode(Base64(HMAC_SHA256(待签名串, Base64Decode(secretAccessKey))))
func (c *EntCreditClient) sign(requestURI, msgID, timestamp, args string) (string, error) {
	keyBytes, err := base64.StdEncoding.DecodeString(c.cfg.SecretAccessKey)
	if err != nil {
		return "", fmt.Errorf("secretAccessKey 不是合法 base64: %w", err)
	}
	toSign := strings.Join([]string{
		http.MethodPost,
		c.cfg.Endpoint,
		requestURI,
		entCreditVersion,
		msgID,
		c.cfg.OrgCode,
		c.cfg.AccessKeyID,
		timestamp,
		args,
	}, "\n")

	mac := hmac.New(sha256.New, keyBytes)
	mac.Write([]byte(toSign))
	b64 := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return url.QueryEscape(b64), nil
}

// Requery: 平台以 orderNo(=msgId) 幂等，对账查询接口待联调。在此之前返回
// Reachable=false, 记录保持 PENDING 由对账兜底 (与其余上游一致)。
func (c *EntCreditClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}
