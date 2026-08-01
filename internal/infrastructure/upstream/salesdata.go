package upstream

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/datahub/relay/internal/domain/model"
)

// 销项数据上游 (docs/销项数据接口文档V1.0.docx) 业务接口与结果码。
// 外层信封 {AppID, ReqData}：ReqData = Base64(AES(内层 JSON))，请求与应答同构。
// 本客户端只调两个月度汇总接口 (近24个月)；发票明细 InvoiceDetail (分页全量)
// 不在下游 swfp 契约 (docs/税票分析接口文档.xlsx) 覆盖范围内，不接。
const (
	salesBizInvoiceSummary = "monthlyInvoiceSummryInfo" // 月度开票汇总（上游拼写如此）
	salesBizDownstream     = "monthlyDownstreamInfo"    // 月度下游企业（购方 Top3）

	salesCodeSuccess = "0000" // 成功
	salesCodeEmpty   = "0001" // 当前税号无关联数据（查无）
)

// SalesDataConfig holds the 销项数据 endpoint + 我方凭证。BaseURL 为业务接口前缀
// (如 http://api2.crestv.com:32313/api/ws)，调用时追加 /<业务类型>。AppKey 既是
// 鉴权凭证也是 AES 密钥（文档 §3.1：ReqData 用 AppKey 做 AES 加密）。
type SalesDataConfig struct {
	BaseURL string
	AppID   string
	AppKey  string // AES 密钥（16/24/32 字节）
}

// SalesDataClient implements port.UpstreamPort for the 销项数据 provider。
// 一次 Query 内并发意义不大（仅两个接口），顺序调用月度开票汇总 + 月度下游企业，
// 合并为一个数据对象归一：任一接口 0000 → "001"(Range=合并 JSON)；全部 0001 →
// "999" 查无；其余（0002 超时/未知码/网络失败）→ error（不计费，走复查/对账）。
// 应答中的计费字段 charge/taxAmountTotal/firstTime 仅落我方日志备查，不透出下游。
type SalesDataClient struct {
	cfg  SalesDataConfig
	http *http.Client
}

// NewSalesData builds a 销项数据 client.
func NewSalesData(cfg SalesDataConfig, httpClient *http.Client) *SalesDataClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &SalesDataClient{cfg: cfg, http: httpClient}
}

// salesEnvelope 是外层报文 (文档 §3.1，请求与应答同构)。
type salesEnvelope struct {
	AppID   string `json:"AppID,omitempty"`
	ReqData string `json:"ReqData"`
}

// salesInner 是内层业务应答 (文档 §3.2)。data 结构随业务接口不同，延迟解析。
type salesInner struct {
	Code           string          `json:"code"`
	Msg            string          `json:"msg"`
	TaxAmountTotal string          `json:"taxAmountTotal"`
	FirstTime      string          `json:"firstTime"`
	Charge         json.RawMessage `json:"charge"` // 文档标称字符串，防御数字形态
	Data           json.RawMessage `json:"data"`
}

// Query calls the two monthly interfaces and merges their data node into one
// object：{"salesInvoice":[...], "summaryIndicators":{...}, "monthlyDownstreamInfo":[...]}。
// 字段级白名单/契约映射由上层 swfp 契约层完成，本客户端只做协议适配与归一。
func (c *SalesDataClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	merged := map[string]json.RawMessage{}
	okCnt, emptyCnt := 0, 0
	var firstErr error

	for _, biz := range []string{salesBizInvoiceSummary, salesBizDownstream} {
		inner, err := c.callBiz(ctx, biz, req)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		switch inner.Code {
		case salesCodeSuccess:
			okCnt++
			// data 节点内的各键 (salesInvoice/summaryIndicators/monthlyDownstreamInfo)
			// 平铺进合并对象；两个接口键名不重叠。
			var dataMap map[string]json.RawMessage
			if len(inner.Data) > 0 && json.Unmarshal(inner.Data, &dataMap) == nil {
				for k, v := range dataMap {
					merged[k] = v
				}
			}
			slog.Info("salesdata 应答计费标志", "biz", biz, "charge", string(inner.Charge),
				"taxAmountTotal", inner.TaxAmountTotal, "firstTime", inner.FirstTime, "reqid", req.Reqid)
		case salesCodeEmpty:
			emptyCnt++
		default:
			// 0002 超时及后续扩展错误码：上游侧失败（已应答，带业务码）。
			if firstErr == nil {
				firstErr = busiErr(inner.Code, fmt.Sprintf("接口 %s 失败: %s", biz, inner.Msg), "", "")
			}
		}
	}

	switch {
	case okCnt > 0:
		plain, err := json.Marshal(merged)
		if err != nil {
			return nil, fmt.Errorf("salesdata 合并序列化: %w", err)
		}
		// 上游应答无订单号/请求号字段，UID/LogID 无可填（非漏填）。
		return &model.UpstreamResult{Code: "001", Msg: "成功", Reqid: req.Reqid, Range: string(plain)}, nil
	case firstErr != nil:
		return nil, firstErr
	case emptyCnt > 0:
		return &model.UpstreamResult{Code: "999", Msg: "查无结果", Reqid: req.Reqid}, nil
	default:
		return nil, fmt.Errorf("salesdata 无任何应答")
	}
}

// callBiz 调一个业务接口并解出内层应答。
// TODO 联调适配：文档 §3.1 只写「使用 AppKey 进行 AES 加密并 Base64」，未给出分组
// 模式/填充/IV——按国内同类协议最常见的 AES/ECB/PKCS5Padding 实现（与 rental 上游
// 一致，复用 aesecb.go）；应答 ReqData 以同密钥解密，解不开时退回按明文 JSON 解析。
// 若联调报解密/验签错误，只改本函数。
func (c *SalesDataClient) callBiz(ctx context.Context, biz string, req *model.UpstreamRequest) (*salesInner, error) {
	// 内层请求：taxpayerIdNum = 下游 creditCode（企业税号 = 统一社会信用代码）。
	inner, err := json.Marshal(map[string]string{"taxpayerIdNum": req.CreditCode})
	if err != nil {
		return nil, fmt.Errorf("marshal salesdata inner: %w", err)
	}
	cipherText, err := aesECBEncryptBase64(inner, []byte(c.cfg.AppKey))
	if err != nil {
		return nil, fmt.Errorf("encrypt salesdata ReqData: %w", err)
	}
	payload, err := json.Marshal(salesEnvelope{AppID: c.cfg.AppID, ReqData: cipherText})
	if err != nil {
		return nil, fmt.Errorf("marshal salesdata envelope: %w", err)
	}

	fullURL := strings.TrimRight(c.cfg.BaseURL, "/") + "/" + biz
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build salesdata request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	slog.Debug("salesdata request", "url", fullURL, "appId", c.cfg.AppID, "reqid", req.Reqid)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("salesdata call (%s): %w", biz, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read salesdata body: %w", err)
	}
	slog.Debug("salesdata response", "biz", biz, "status", resp.StatusCode, "len", len(raw))

	plain := decodeSalesBody(raw, []byte(c.cfg.AppKey))
	var si salesInner
	if err := json.Unmarshal(plain, &si); err != nil {
		return nil, fmt.Errorf("decode salesdata inner (%s, http %d): %w", biz, resp.StatusCode, err)
	}
	if si.Code == "" {
		return nil, fmt.Errorf("salesdata 应答缺少 code (%s, http %d)", biz, resp.StatusCode)
	}
	return &si, nil
}

// decodeSalesBody 解出内层应答明文：应答外层含 ReqData 时按 AES 解密；解不开或
// 无外层时按原文返回（联调容错：部分环境可能直接回明文内层）。
func decodeSalesBody(raw, key []byte) []byte {
	var env salesEnvelope
	if err := json.Unmarshal(raw, &env); err == nil && env.ReqData != "" {
		if plain, err := aesECBDecryptBase64(env.ReqData, key); err == nil && json.Valid(plain) {
			return plain
		}
		// ReqData 可能本身就是 base64 的明文 JSON（无加密环境）。
		if plain, err := base64.StdEncoding.DecodeString(env.ReqData); err == nil && json.Valid(plain) {
			return plain
		}
	}
	return raw
}

// Requery: 销项数据上游未提供对账查询接口，未联调前返回 Reachable=false，
// 记录保持 PENDING 由对账兜底 (与其余上游一致)。
func (c *SalesDataClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}
