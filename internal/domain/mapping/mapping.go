// Package mapping builds the unified client response envelope (DESIGN §5.1).
package mapping

import (
	"time"

	"github.com/datahub/relay/internal/common/errs"
	"github.com/datahub/relay/internal/domain/model"
)

// Success maps a charged upstream result into the head+body envelope.
// verify is the optional response HMAC over the body (DESIGN §8.1, may be "").
func Success(r *model.UpstreamResult, requestID, verify string) *model.QueryResult {
	now := time.Now().UnixMilli()
	return &model.QueryResult{
		Head: model.Head{
			ErrorCode: string(errs.OK),
			RequestID: requestID,
			LogID:     requestID,
			ErrorMsg:  "success",
			Timestamp: now,
		},
		Body: &model.Body{
			Code:   r.Code,
			Msg:    r.Msg,
			Result: &model.RangeResult{Range: r.Range},
			UID:    r.UID,
			Reqid:  r.Reqid,
			Verify: verify,
		},
	}
}

// ErrorResponse builds a head-only error envelope for any gateway error code
// (DESIGN §5.1 异常响应 / §5.3).
func ErrorResponse(code errs.Code, msg, requestID string) *model.QueryResult {
	if msg == "" {
		msg = errs.New(code, "").Msg
	}
	return &model.QueryResult{
		Head: model.Head{
			ErrorCode: string(code),
			RequestID: requestID,
			LogID:     requestID,
			ErrorMsg:  msg,
			Timestamp: time.Now().UnixMilli(),
		},
	}
}
