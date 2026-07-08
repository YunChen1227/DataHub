// Package parse validates and normalises the client request into a normalized
// upstream request shape (接口文档-经济能力.doc §3.1.3: mobile/idCard 必填, name
// 选填). The provider-specific verify/sign is filled later by the upstream client.
package parse

import (
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/datahub/relay/internal/common/errs"
	"github.com/datahub/relay/internal/domain/model"
)

var (
	mobileRe = regexp.MustCompile(`^1\d{10}$`)
	idCardRe = regexp.MustCompile(`^\d{17}[\dX]$`)
	// 统一社会信用代码 (GB 32100)：18 位，字符集不含 I/O/S/V/Z（不做校验位运算）。
	creditCodeRe = regexp.MustCompile(`^[0-9A-HJ-NPQRTUWXY]{2}\d{6}[0-9A-HJ-NPQRTUWXY]{10}$`)
)

// Parse runs参数校验; failures return busiCode 1007 数据请求异常 (我方拦截, 不调
// 上游/不计费). It generates an internal upstream reqid (≤20).
func Parse(cmd *model.QueryCommand) (*model.UpstreamRequest, error) {
	if cmd == nil {
		return nil, errs.New(errs.BusiDataRequestErr, "请求体为空")
	}
	name := strings.TrimSpace(cmd.Name) // 选填
	mobile := strings.TrimSpace(cmd.Mobile)
	idCard := strings.ToUpper(strings.TrimSpace(cmd.IDCard))

	if !mobileRe.MatchString(mobile) {
		return nil, errs.New(errs.BusiDataRequestErr, "mobile 格式非法")
	}
	if !idCardRe.MatchString(idCard) {
		return nil, errs.New(errs.BusiDataRequestErr, "idCard 格式非法")
	}

	return &model.UpstreamRequest{
		IDCard: idCard,
		Name:   name,
		Mobile: mobile,
		Reqid:  NewReqid(),
	}, nil
}

// ParseWithName 校验个人三要素且 name 必填。用于上游要求姓名必传的路由
// (zlf 租赁分：文档 §2.5 name 必；blk 黑名单V35：name 参与 MD5 摘要匹配)——
// 网关校验口径必须与上游要求一致，前置拦截而非透传给上游报错（对外手册承诺
// "参数非法不调用上游、不计费"）。
func ParseWithName(cmd *model.QueryCommand) (*model.UpstreamRequest, error) {
	up, err := Parse(cmd)
	if err != nil {
		return nil, err
	}
	if up.Name == "" {
		return nil, errs.New(errs.BusiDataRequestErr, "name 不能为空")
	}
	return up, nil
}

// ParseCreditCode 校验 swfp 入参 (creditCode 必填，统一社会信用代码；字段名对齐
// 上游证通 entcreditapi 的 args.creditCode——2026-07-08 上游 E1000 报错明确指出
// 必填字段名为 creditCode，不是官方 demo 文档示例里的 entInfo，不臆造中间层字段
// 名)。失败返回 busiCode 1007 数据请求异常 (我方拦截, 不调上游/不计费)。
func ParseCreditCode(cmd *model.QueryCommand) (*model.UpstreamRequest, error) {
	if cmd == nil {
		return nil, errs.New(errs.BusiDataRequestErr, "请求体为空")
	}
	creditCode := strings.ToUpper(strings.TrimSpace(cmd.CreditCode))
	if !creditCodeRe.MatchString(creditCode) {
		return nil, errs.New(errs.BusiDataRequestErr, "creditCode 格式非法")
	}
	return &model.UpstreamRequest{
		CreditCode: creditCode,
		Reqid:      NewReqid(),
	}, nil
}

// reqidSeq guarantees in-process uniqueness even when the wall clock does not
// advance between two rapid calls (Windows time.Now() can have coarse ~ms
// granularity, so consecutive UnixNano() values may be identical and cause
// reqid collisions → idempotency replay).
var reqidSeq atomic.Uint64

// NewReqid generates an internal upstream reqid（base36 时间戳 + 进程内自增序号，
// ≤20 位，满足各上游 reqid ≤20 的约束并保证同进程内绝不重复）。
func NewReqid() string {
	ts := strconv.FormatInt(time.Now().UnixNano(), 36) // ≤13 位
	seq := strconv.FormatUint(reqidSeq.Add(1)%46656, 36) // 1–3 位 (36^3)
	r := ts + seq
	if len(r) > 20 {
		r = r[:20]
	}
	return r
}
