// Package errs defines the internal business codes (busiCode) and a
// transport-agnostic AppError. Two outbound code spaces are derived from it:
//   - 上游伽马 (PDF §2.1 busiCode) is parsed INTO these constants by the gama
//     upstream client.
//   - 下游对客户 (接口文档-经济能力.doc §1.5) head.errorCode is derived via
//     ErrorCode(); the business result (查得/查无) is carried in body.code.
package errs

import "errors"

// Downstream gateway head.errorCode values (接口文档-经济能力.doc §1/§3).
//   - "0"   = 调用成功（含查得/查无，业务结果落在 body.code 001/999）。
//   - 非"0" = 网关级错误（鉴权/配额/参数/系统），此时只返回 head、无 body。
const (
	ErrorCodeOK = "0"
)

// BusiCode is the internal business code (also the 伽马上游 busiCode space).
type BusiCode int

const (
	BusiSuccess         BusiCode = 10   // 查询成功【计成功查得数】(伽马 busiCode 10)
	BusiNotFound        BusiCode = 1000 // 数据未查得 (伽马 busiCode 1000)
	BusiAccountNotExist BusiCode = 1002 // 账户信息不存在（appKey 查无 license）
	BusiAppIDInvalid    BusiCode = 1003 // appKey 异常（缺少/非法 appKey）
	BusiProductInvalid  BusiCode = 1004 // 产品编号异常（保留，下游已不使用）
	BusiAccountAbnormal BusiCode = 1005 // 账号信息异常（签名校验失败 / IP 不在白名单）
	BusiDataRequestErr  BusiCode = 1007 // 数据请求异常（参数/上游我方原因/内部错误/超时未决）
	BusiServiceNotOpen  BusiCode = 1009 // 服务尚未开通（license 停用/过期/未开通）
)

// errorCodeByBusi maps an internal busiCode to the下游 head.errorCode
// (接口文档-经济能力.doc). 0/505062 沿用 .doc 示例，其余按 5050xx 归类。
var errorCodeByBusi = map[BusiCode]string{
	BusiAccountNotExist: "505004",
	BusiAppIDInvalid:    "505001",
	BusiProductInvalid:  "505003",
	BusiAccountAbnormal: "505002",
	BusiServiceNotOpen:  "505007",
	BusiDataRequestErr:  "505062",
}

// ErrorCode returns the下游 head.errorCode for an error busiCode (默认 505062).
func ErrorCode(code BusiCode) string {
	if s, ok := errorCodeByBusi[code]; ok {
		return s
	}
	return "505062"
}

// defaultMsg maps each busiCode to its canonical client-facing message.
var defaultMsg = map[BusiCode]string{
	BusiSuccess:         "success",
	BusiNotFound:        "数据未查得",
	BusiAccountNotExist: "账户信息不存在",
	BusiAppIDInvalid:    "appId 异常",
	BusiProductInvalid:  "产品编号异常",
	BusiAccountAbnormal: "账号信息异常",
	BusiDataRequestErr:  "数据请求异常",
	BusiServiceNotOpen:  "服务尚未开通",
}

// Msg returns the canonical message for a busiCode.
func Msg(code BusiCode) string { return defaultMsg[code] }

// AppError is the canonical error used across all layers. The API layer unwraps
// it into a PDF envelope (code=0, data.busiCode/busiMsg).
type AppError struct {
	Busi BusiCode
	Msg  string
	Err  error // optional underlying cause, preserved for logging
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return e.Msg + ": " + e.Err.Error()
	}
	return e.Msg
}

func (e *AppError) Unwrap() error { return e.Err }

// New builds an AppError; an empty msg falls back to the canonical message.
func New(code BusiCode, msg string) *AppError {
	if msg == "" {
		msg = defaultMsg[code]
	}
	return &AppError{Busi: code, Msg: msg}
}

// Wrap is New with an underlying cause attached.
func Wrap(code BusiCode, msg string, err error) *AppError {
	ae := New(code, msg)
	ae.Err = err
	return ae
}

// AsAppError coerces any error into an *AppError, defaulting unknown errors to
// 数据请求异常 (1007) so the client always gets a busiCode.
func AsAppError(err error) *AppError {
	if err == nil {
		return nil
	}
	var ae *AppError
	if errors.As(err, &ae) {
		return ae
	}
	return Wrap(BusiDataRequestErr, defaultMsg[BusiDataRequestErr], err)
}
