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
//
// body.uid 恒为**我方**交易流水号 (requestID，与 head.logId 同值)——不是上游订单号。
// 上游订单号/请求号只经 UpstreamResult.UID/LogID 落审计供我方向上游对账，不外泄给
// 下游 (见 README「result.range 不透出上游标识」铁律)。
func Found(r *model.UpstreamResult, requestID string, latencyMs int64) *model.QueryResponse {
	b := &model.QueryBody{Code: "001", Msg: "成功", Reqid: requestID, UID: requestID}
	if r != nil {
		if r.Code != "" {
			b.Code = r.Code
		}
		if r.Msg != "" {
			b.Msg = r.Msg
		}
		if r.Reqid != "" {
			b.Reqid = r.Reqid
		}
		b.Verify = r.Verify
		b.Result = &model.RangeResult{Range: r.Range}
	}
	return &model.QueryResponse{Head: head(errs.ErrorCodeOK, "success", requestID, latencyMs), Body: b}
}

// NotFound builds a确定结论但非查得计费 response: head.errorCode "0" + body.code
// 默认 "999" 查无 (无 result 节点)。聚合路由的 002 部分成功也走此映射——r.Code
// 覆盖 body.code 且 r.Range 非空时透出 result.range (部分数据)；单上游查无的
// Range 恒为空，行为不变 (DESIGN §7.4)。
//
// body.uid 与 Found 同口径：恒为我方交易流水号，不透出上游订单号。
func NotFound(r *model.UpstreamResult, requestID string, latencyMs int64) *model.QueryResponse {
	b := &model.QueryBody{Code: "999", Msg: "查无结果", Reqid: requestID, UID: requestID}
	if r != nil {
		if r.Code != "" {
			b.Code = r.Code
		}
		if r.Msg != "" {
			b.Msg = r.Msg
		}
		if r.Reqid != "" {
			b.Reqid = r.Reqid
		}
		if r.Range != "" {
			b.Result = &model.RangeResult{Range: r.Range}
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
