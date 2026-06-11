// Package parse validates and normalises the client request into an upstream
// request shape (PDF body / DESIGN §5.1 fields + §6.1 mapping). The verify(MD5)
// field is filled later by the upstream client.
package parse

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/datahub/relay/internal/common/errs"
	"github.com/datahub/relay/internal/domain/model"
)

var (
	mobileRe = regexp.MustCompile(`^1\d{10}$`)
	idCardRe = regexp.MustCompile(`^\d{17}[\dX]$`)
)

// Parse runs参数校验; failures return busiCode 1007 数据请求异常 (我方拦截, 不调
// 上游/不计费, DESIGN §5.3). It also derives the upstream reqid from tradeNo.
func Parse(cmd *model.QueryCommand) (*model.UpstreamRequest, error) {
	if cmd == nil {
		return nil, errs.New(errs.BusiDataRequestErr, "请求体为空")
	}
	name := strings.TrimSpace(cmd.Name)
	mobile := strings.TrimSpace(cmd.Mobile)
	idCard := strings.ToUpper(strings.TrimSpace(cmd.IDCard))

	if name == "" {
		return nil, errs.New(errs.BusiDataRequestErr, "name 必填")
	}
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
		Reqid:  DeriveReqid(cmd.TradeNo),
	}, nil
}

// DeriveReqid maps客户 tradeNo → 上游 reqid (≤20, DESIGN §5.1/§9.1). When tradeNo
// is empty an internal reqid is generated; when too long it is truncated.
func DeriveReqid(tradeNo string) string {
	t := strings.TrimSpace(tradeNo)
	if t == "" {
		// 内部生成 reqid（base36 时间戳，≤13 位，满足 ≤20）。
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	if len(t) > 20 {
		return t[:20]
	}
	return t
}
