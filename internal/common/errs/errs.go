// Package errs defines the PDF business codes (busiCode, DESIGN §5.3) and a
// transport-agnostic AppError carrying the data.busiCode / data.busiMsg pair.
package errs

import "errors"

// Global response code values (PDF §1.6 / DESIGN §5.0).
const (
	CodeOK    = 0  // 业务态一律 0（成败在 busiCode 表达）
	CodeError = -1 // 响应异常（请求体不可解析 / 系统级异常）
)

// BusiCode is the value written to data.busiCode (PDF §5.3).
type BusiCode int

const (
	BusiSuccess         BusiCode = 10   // 查询成功【计费】
	BusiNotFound        BusiCode = 1000 // 数据未查得
	BusiNoBalance       BusiCode = 1001 // 账户余额不足（维度①无余额）
	BusiAccountNotExist BusiCode = 1002 // 账户信息不存在（appId 查无 license）
	BusiAppIDInvalid    BusiCode = 1003 // appId 异常（缺少/非法 appId）
	BusiProductInvalid  BusiCode = 1004 // 产品编号异常（apiKey 不匹配）
	BusiAccountAbnormal BusiCode = 1005 // 账号信息异常（签名校验失败）
	BusiOverdraftLimit  BusiCode = 1006 // 透支余额已达上限（维度②达成本上限）
	BusiDataRequestErr  BusiCode = 1007 // 数据请求异常（参数/上游我方原因/内部错误/超时未决）
	BusiServiceNotOpen  BusiCode = 1009 // 服务尚未开通（license 停用/过期/未开通）
)

// defaultMsg maps each busiCode to its canonical client-facing message.
var defaultMsg = map[BusiCode]string{
	BusiSuccess:         "success",
	BusiNotFound:        "数据未查得",
	BusiNoBalance:       "账户余额不足",
	BusiAccountNotExist: "账户信息不存在",
	BusiAppIDInvalid:    "appId 异常",
	BusiProductInvalid:  "产品编号异常",
	BusiAccountAbnormal: "账号信息异常",
	BusiOverdraftLimit:  "透支余额已达上限",
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
