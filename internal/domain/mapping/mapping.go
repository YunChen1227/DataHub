// Package mapping builds the下游客户响应信封 (接口文档-经济能力.doc §3.1.4: head/body).
package mapping

import (
	"time"

	"github.com/datahub/relay/internal/common/errs"
	"github.com/datahub/relay/internal/domain/model"
)

func head(errorCode, errorMsg, requestID string, latencyMs int64) model.ResponseHead {
	return model.ResponseHead{
		ErrorCode: errorCode,
		LogID:     requestID,
		Time:      latencyMs,
		ErrorMsg:  errorMsg,
		Timestamp: time.Now().UnixMilli(),
	}
}

// Found builds a查得数据 response: head.errorCode "0" + body.code "001" + range.
func Found(r *model.UpstreamResult, requestID string, latencyMs int64) *model.QueryResponse {
	b := &model.QueryBody{Code: "001", Msg: "成功", Reqid: requestID}
	if r != nil {
		if r.Code != "" {
			b.Code = r.Code
		}
		if r.Msg != "" {
			b.Msg = r.Msg
		}
		b.UID = r.UID
		if r.Reqid != "" {
			b.Reqid = r.Reqid
		}
		b.Verify = r.Verify
		b.Result = &model.RangeResult{Range: r.Range}
	}
	return &model.QueryResponse{Head: head(errs.ErrorCodeOK, "success", requestID, latencyMs), Body: b}
}

// NotFound builds a查无结果 response: head.errorCode "0" + body.code "999" (无
// result 节点). 查无属正常返回, 不计维度① (DESIGN §7.4).
func NotFound(r *model.UpstreamResult, requestID string, latencyMs int64) *model.QueryResponse {
	b := &model.QueryBody{Code: "999", Msg: "查无结果", Reqid: requestID}
	if r != nil {
		if r.Code != "" {
			b.Code = r.Code
		}
		if r.Msg != "" {
			b.Msg = r.Msg
		}
		b.UID = r.UID
		if r.Reqid != "" {
			b.Reqid = r.Reqid
		}
	}
	return &model.QueryResponse{Head: head(errs.ErrorCodeOK, "success", requestID, latencyMs), Body: b}
}

// Error builds a网关级错误 response: head.errorCode 非0 + errorMsg, 不带 body
// (鉴权/配额/参数/系统类, 接口文档-经济能力.doc 异常返回示例).
func Error(code errs.BusiCode, msg, requestID string, latencyMs int64) *model.QueryResponse {
	if msg == "" {
		msg = errs.Msg(code)
	}
	return &model.QueryResponse{Head: head(errs.ErrorCode(code), msg, requestID, latencyMs)}
}
