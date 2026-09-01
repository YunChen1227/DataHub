package upstream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/datahub/relay/internal/domain/model"
)

// IDVerifyConfig holds the 身份证三要素核验 endpoint + 我方凭证。
// AppID = 上游 appId（平台分配的调用方 Id），AppSecret = 上游商户密钥
// （拼入 "&AppSecret=" 后参与 SHA256 签名）。
type IDVerifyConfig struct {
	BaseURL   string // https://api.cqcucc.com:8443/api/idCardThreeElements
	AppID     string
	AppSecret string
}

// IDVerifyClient implements port.UpstreamPort for the 身份证三要素核验 provider.
// 协议: POST JSON (application/json;charset=UTF-8); 签名 = SHA256(按 key 升序拼接
// 的 "k=v&k=v..." + "&AppSecret=" + 商户密钥) 小写 hex。响应 Data 富对象
// (Result/ResultMessage/ImageScore) 原样序列化经下游 result.range 透出。
type IDVerifyClient struct {
	cfg  IDVerifyConfig
	http *http.Client
}

// NewIDVerify builds a 身份证三要素核验 client.
func NewIDVerify(cfg IDVerifyConfig, httpClient *http.Client) *IDVerifyClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &IDVerifyClient{cfg: cfg, http: httpClient}
}

type idVerifyResponse struct {
	Code         int             `json:"Code"`
	Message      string          `json:"Message"`
	IsCharge     bool            `json:"IsCharge"`
	ErrorAddress string          `json:"ErrorAddress"`
	OutBizNo     string          `json:"OutBizNo"`
	Data         json.RawMessage `json:"Data"`
	RequestId    string          `json:"RequestId"`
}

// Query performs the signed JSON POST to 身份证三要素核验 and normalizes the
// response 严格以上游的**权威计费标志 IsCharge** 为准 (接口文档 §3 返回结果表:
// 「IsCharge 计费标志 本次请求是否计费 true:计费,false:不计费」)：
//   - Code=0 且 IsCharge=true  → "001" 查得(计费)，Range = Data 富对象 JSON 字符串
//     (Result/ResultMessage/ImageScore)。文档 §5.2 的 Result 0–5 均标「计费」，
//     正常情况恒走此分支。
//   - Code=0 但 IsCharge=false → "999"：上游明说本次不收费，我方也不得计费。
//     文档未列出这种组合，出现即说明上游口径变了，落 warn 供对账排查。
//   - Code≠0                   → error (账户/参数/照片/系统等上游侧异常，不计费，
//     触发 re-query/对账)。文档 §4.2.2 失败示例的 IsCharge 恒为 false；若竟为
//     true 说明上游对失败也扣了我方的费，同样落 warn 供人工对账。
//
// 不用 Code 推断计费：IsCharge 是上游逐笔下发的结论，Code/Result 码表只是它的
// 文档快照，上游改口径时先变的是 IsCharge。
func (c *IDVerifyClient) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	ts := time.Now().UnixMilli()
	tsStr := strconv.FormatInt(ts, 10)
	// outBizNo 由本服务生成的唯一订单标识（复用内部 reqid），上游原样回传供对账。
	outBizNo := req.Reqid

	// 参与签名的业务字段（除 signature 外全部参与），json key 为小驼峰。
	// timestamp 在 body 中以数值发送，签名串中以其字符串形态参与（取值一致）。
	params := map[string]string{
		"appId":          c.cfg.AppID,
		"outBizNo":       outBizNo,
		"name":           req.Name,
		"idCard":         req.IDCard,
		"profilePicture": req.ProfilePicture,
		"timestamp":      tsStr,
	}
	signature := signIDVerify(params, c.cfg.AppSecret)

	body := map[string]any{
		"appId":          c.cfg.AppID,
		"outBizNo":       outBizNo,
		"name":           req.Name,
		"idCard":         req.IDCard,
		"profilePicture": req.ProfilePicture,
		"timestamp":      ts,
		"signature":      signature,
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal idverify request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("build idverify request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json;charset=UTF-8")

	slog.Debug("idverify request",
		"url", c.cfg.BaseURL,
		"appId", c.cfg.AppID,
		"outBizNo", outBizNo,
		"timestamp", tsStr,
		"signature", signature,
		"pictureLen", len(req.ProfilePicture),
		"reqid", req.Reqid,
	)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("idverify call: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read idverify body: %w", err)
	}
	slog.Debug("idverify response", "status", resp.StatusCode, "raw", string(respBody))

	var ir idVerifyResponse
	if err := json.Unmarshal(respBody, &ir); err != nil {
		return nil, fmt.Errorf("decode idverify body: %w", err)
	}
	if ir.Code != 0 {
		// 401–463 客户端错误 / 501–504 服务端错误：均为我方在上游侧的账户/参数/
		// 照片/系统问题，视为上游侧错误 (不计费)，由 orchestrator 走 re-query/对账。
		// 失败也带上游标识(RequestId=请求号 / OutBizNo=订单号)落审计，供对账追查。
		if ir.IsCharge {
			slog.Warn("idverify 上游对失败请求标记了计费，我方按不计费返回，请人工对账",
				"code", ir.Code, "outBizNo", ir.OutBizNo, "requestId", ir.RequestId, "reqid", req.Reqid)
		}
		msg := ir.Message
		if ir.ErrorAddress != "" {
			msg += " errAddr=" + ir.ErrorAddress
		}
		return nil, busiErrf(ir.Code, msg, ir.OutBizNo, ir.RequestId)
	}

	uid := ir.OutBizNo
	if uid == "" {
		uid = ir.RequestId
	}
	if !ir.IsCharge {
		// 上游给了结论却明说不收费——我方同步不计费。归一为 999 (确定结论、不计费)
		// 而不是错误：这是一次正常应答，不该触发 re-query/对账兜底。
		slog.Warn("idverify 上游返回 Code=0 但 IsCharge=false，按不计费归一为 999",
			"outBizNo", ir.OutBizNo, "requestId", ir.RequestId, "reqid", req.Reqid)
		return &model.UpstreamResult{
			Code: model.CodeNotFound,
			// mapping.NotFound 会把 Msg 原样写进下游 body.msg，故此处只能写对客
			// 措辞——「上游/计费标志」这类内部归因留在上面的 warn 里。
			Msg:   "查无核验结论",
			UID:   uid,
			Reqid: req.Reqid,
			LogID: ir.RequestId,
		}, nil
	}
	// LogID 恒填上游 RequestId（请求号），成功也要能在后台「上游logId」列对账追查。
	return &model.UpstreamResult{
		Code:  "001",
		Msg:   "成功",
		UID:   uid,
		Reqid: req.Reqid,
		LogID: ir.RequestId,
		Range: sanitizeRange(ir.Data),
	}, nil
}

// Requery: 身份证三要素核验暂无幂等对账查询接口，未联调前返回 Reachable=false，
// 记录保持 PENDING 由对账兜底 (与既有上游一致)。
func (c *IDVerifyClient) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	_ = ctx
	_ = reqid
	return &model.RequeryResult{Reachable: false}, nil
}

// signIDVerify 计算 signature = SHA256(升序 "k=v&k=v..." + "&AppSecret=" + 密钥)
// 小写 hex。注意字面量 "&AppSecret=" 的大小写（A、S 大写）须与上游一致。
func signIDVerify(params map[string]string, appSecret string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteString("&")
		}
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(params[k])
	}
	sb.WriteString("&AppSecret=")
	sb.WriteString(appSecret)
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}
