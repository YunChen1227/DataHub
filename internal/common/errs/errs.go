// Package errs defines the gateway-level error codes (DESIGN §5.3) and a
// transport-agnostic AppError carrying the head.errorCode / head.errorMsg pair.
package errs

import "errors"

// Code is the value written to head.errorCode.
type Code string

const (
	OK                  Code = "0"
	MissingAppKey       Code = "401001" // 缺少/非法 appKey
	SignatureInvalid    Code = "401002" // 签名校验失败
	LicenseInactive     Code = "401003" // license 已停用/过期
	ServiceQuotaEmpty   Code = "429001" // 维度①无余额（余额不足，请充值）
	UpstreamQuotaEmpty  Code = "429002" // 维度②达成本上限
	ParamInvalid        Code = "400001" // 参数校验失败
	UpstreamNotExecuted Code = "502001" // 上游真未执行/未返回
	UpstreamBusiness    Code = "502002" // 上游业务失败（我方原因）
)

// defaultMsg maps each code to its canonical client-facing message.
var defaultMsg = map[Code]string{
	OK:                  "success",
	MissingAppKey:       "缺少或非法 appKey",
	SignatureInvalid:    "签名校验失败",
	LicenseInactive:     "license 已停用或过期",
	ServiceQuotaEmpty:   "余额不足，请充值",
	UpstreamQuotaEmpty:  "服务繁忙，请稍后重试",
	ParamInvalid:        "参数校验失败",
	UpstreamNotExecuted: "上游未执行，请凭 reqid 重试",
	UpstreamBusiness:    "上游业务处理失败",
}

// AppError is the canonical error used across all layers. The global HTTP
// handler unwraps it into the head structure.
type AppError struct {
	Code Code
	Msg  string
	Err  error // optional underlying cause, preserved for logging
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return string(e.Code) + ": " + e.Msg + ": " + e.Err.Error()
	}
	return string(e.Code) + ": " + e.Msg
}

func (e *AppError) Unwrap() error { return e.Err }

// New builds an AppError; an empty msg falls back to the canonical message.
func New(code Code, msg string) *AppError {
	if msg == "" {
		msg = defaultMsg[code]
	}
	return &AppError{Code: code, Msg: msg}
}

// Wrap is New with an underlying cause attached.
func Wrap(code Code, msg string, err error) *AppError {
	ae := New(code, msg)
	ae.Err = err
	return ae
}

// AsAppError coerces any error into an *AppError, defaulting unknown errors to
// an internal upstream-business failure so the client always gets a head code.
func AsAppError(err error) *AppError {
	if err == nil {
		return nil
	}
	var ae *AppError
	if errors.As(err, &ae) {
		return ae
	}
	return Wrap(UpstreamBusiness, defaultMsg[UpstreamBusiness], err)
}
