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

// --- 旧版 v9 下游响应 (income_cls.md §返回参数) ---

// v9CodeMsg is the income_cls.md §返回码字典.
var v9CodeMsg = map[string]string{
	"001": "成功",
	"999": "查无结果",
	"002": "账号不存在",
	"003": "余额不足",
	"004": "请给该账户授权",
	"005": "身份证号为空或格式错误",
	"006": "姓名格式错误",
	"008": "reqid为空或长度大于20",
	"009": "账号为空",
	"011": "verify为空或格式错误",
	"012": "接口错误，请联系提供商",
	"013": "校验签名错误",
	"020": "参数为空或格式错误",
}

// V9Msg returns the canonical message for a v9 返回码 (income_cls.md §返回码字典).
func V9Msg(code string) string { return v9CodeMsg[code] }

// V9CodeFromBusi maps an internal busiCode to the旧版 v9 返回码字典
// (income_cls.md). 业务态 001/999 由 settle 直接给出; 此处覆盖鉴权/配额/系统类。
func V9CodeFromBusi(b errs.BusiCode) string {
	switch b {
	case errs.BusiSuccess:
		return "001"
	case errs.BusiNotFound:
		return "999"
	case errs.BusiAccountNotExist:
		return "002" // 账号不存在
	case errs.BusiServiceNotOpen:
		return "004" // 请给该账户授权（服务未开通）
	case errs.BusiAppIDInvalid:
		return "009" // 账号为空/异常
	case errs.BusiAccountAbnormal:
		return "013" // 校验签名错误 / 账号信息异常
	default:
		return "012" // 接口错误，请联系提供商（含 1007 上游我方原因/超时未决）
	}
}

// V9Found builds a旧版 v9 查得响应 (code 001 + result.range)。verify 由调用方按
// 客户 key 预先计算后传入。
func V9Found(r *model.UpstreamResult, reqid, verify string) *model.V9Response {
	resp := &model.V9Response{Code: "001", Msg: V9Msg("001"), Reqid: reqid, Verify: verify}
	if r != nil {
		if r.UID != "" {
			resp.UID = r.UID
		}
		resp.Result = &model.V9Result{Range: r.Range}
	}
	return resp
}

// V9NotFound builds a旧版 v9 查无响应 (code 999, 无 result 节点)。
func V9NotFound(r *model.UpstreamResult, reqid, verify string) *model.V9Response {
	resp := &model.V9Response{Code: "999", Msg: V9Msg("999"), Reqid: reqid, Verify: verify}
	if r != nil && r.UID != "" {
		resp.UID = r.UID
	}
	return resp
}

// V9Error builds a旧版 v9 错误响应 (无 result; verify 留空)。msg 为空时取字典默认。
func V9Error(code, msg, reqid string) *model.V9Response {
	if msg == "" {
		msg = V9Msg(code)
	}
	return &model.V9Response{Code: code, Msg: msg, Reqid: reqid}
}
