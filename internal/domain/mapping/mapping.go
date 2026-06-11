// Package mapping builds the unified client response envelope (PDF §1.5 / DESIGN §5).
package mapping

import (
	"github.com/datahub/relay/internal/common/errs"
	"github.com/datahub/relay/internal/domain/model"
)

// Success builds a查得数据 response (busiCode 10) carrying the score (PDF §2.3).
// score is透传 upstream range (可能为 "0", DESIGN §15.0).
func Success(score, seqNo string) *model.DoCheckResponse {
	return &model.DoCheckResponse{
		Code:  errs.CodeOK,
		Msg:   "请求成功",
		SeqNo: seqNo,
		Data: &model.DoCheckData{
			BusiCode: int(errs.BusiSuccess),
			BusiMsg:  "success",
			Result:   &model.ScoreResult{Score: score},
		},
	}
}

// Busi builds a业务态 response (global code=0) carrying a non-success busiCode
// and message (PDF §5.3, e.g. 1000/1001/1003/1007…).
func Busi(code errs.BusiCode, msg, seqNo string) *model.DoCheckResponse {
	if msg == "" {
		msg = errs.Msg(code)
	}
	return &model.DoCheckResponse{
		Code:  errs.CodeOK,
		Msg:   "请求成功",
		SeqNo: seqNo,
		Data: &model.DoCheckData{
			BusiCode: int(code),
			BusiMsg:  msg,
		},
	}
}

// SystemError builds a全局异常 response (code=-1, no data) for unparseable
// requests or system-level failures (PDF §1.6 / DESIGN §5.1).
func SystemError(msg, seqNo string) *model.DoCheckResponse {
	if msg == "" {
		msg = "响应异常"
	}
	return &model.DoCheckResponse{
		Code:  errs.CodeError,
		Msg:   msg,
		SeqNo: seqNo,
	}
}
