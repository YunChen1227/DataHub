package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/datahub/relay/internal/domain/model"
)

// 应诺尔 enol doCheck 的 busiCode 表 (借贷申请意向V22【TD】/风险变量V11/信用全景V21
// 三份 PDF §1.6 同一张表)。仅 10/1000 是业务结果，1001~1009 是我方在应诺尔侧的
// 账户/参数/系统问题。**各产品对 1000 是否计费的标注不同，计费口径见各 client。**
const (
	enolBusiSuccess  = 10
	enolBusiNotFound = 1000
)

// enolEnvelope 是 enol doCheck 请求信封 (PDF §1.4 请求结构)。与 gamaEnvelope 的区别：
// encryptionType 带 omitempty——借贷申请意向V22【TD】的请求结构表里**没有**
// encryptionType 字段，传 0 即不出现在报文里；风险变量V11/信用全景V21 传 1 (明文)。
type enolEnvelope struct {
	EncryptionType int               `json:"encryptionType,omitempty"`
	AppID          string            `json:"appId"`
	Sign           string            `json:"sign"`
	APIKey         string            `json:"apiKey"`
	Body           map[string]string `json:"body"`
}

// enolResponse mirrors PDF §1.5 响应结构。
type enolResponse struct {
	Code  int    `json:"code"`  // 0 成功 / -1 响应异常 (§1.6)
	Msg   string `json:"msg"`   // 返回文字描述
	SeqNo string `json:"seqNo"` // 交易流水号——唯一的上游标识，UID/LogID 同填落审计
	Data  struct {
		BusiCode int             `json:"busiCode"`
		BusiMsg  string          `json:"busiMsg"`
		Result   json.RawMessage `json:"result"`
	} `json:"data"`
}

// enolThreeFactorBody 组装 name/idCard/mobile 三项 body 参数 (三份 PDF §2.2 均标必填，
// 由网关 ParseWithName 前置保证非空)。PII 明文——这三份文档的加密类型只定义了
// 1=明文。tradeNo (业务单号，选填) 不注入：下游 x1 契约无业务单号字段可透传，与
// gama/blacklist/idrisk 一致，sign 只覆盖实际发出的 body 参数。
func enolThreeFactorBody(req *model.UpstreamRequest) map[string]string {
	body := map[string]string{}
	if req.Name != "" {
		body["name"] = req.Name
	}
	if req.IDCard != "" {
		body["idCard"] = req.IDCard
	}
	if req.Mobile != "" {
		body["mobile"] = req.Mobile
	}
	return body
}

// enolDoCheck 发出签名 JSON POST 并解析响应信封。全局 code≠0 直接返回带 seqNo 的
// *model.UpstreamError；busiCode 的归一交给调用方 (各产品计费口径不同)。
func enolDoCheck(ctx context.Context, hc *http.Client, baseURL, appID, secret, apiKey string, encryptionType int, body map[string]string, reqid, logName string) (*enolResponse, error) {
	env := enolEnvelope{
		EncryptionType: encryptionType,
		AppID:          appID,
		Sign:           signGama(body, secret),
		APIKey:         apiKey,
		Body:           body,
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal %s request: %w", logName, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build %s request: %w", logName, err)
	}
	httpReq.Header.Set("Content-Type", "application/json; charset=utf-8")

	slog.Debug(logName+" request",
		"url", baseURL,
		"appId", appID,
		"apiKey", apiKey,
		"encryptionType", encryptionType,
		"sign", env.Sign,
		"reqid", reqid,
	)

	resp, err := hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s call: %w", logName, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s body: %w", logName, err)
	}
	slog.Debug(logName+" response", "status", resp.StatusCode, "raw", string(raw))

	var er enolResponse
	if err := json.Unmarshal(raw, &er); err != nil {
		return nil, fmt.Errorf("decode %s body: %w", logName, err)
	}
	if er.Code != 0 {
		return nil, busiErrf(er.Code, er.Msg, er.SeqNo, er.SeqNo)
	}
	return &er, nil
}
