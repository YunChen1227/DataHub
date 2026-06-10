// Package parse validates and normalises the client request into an upstream
// request shape (DESIGN §5.1 fields + §6.1 mapping). The verify(MD5) field is
// filled later by the upstream client.
package parse

import (
	"regexp"
	"strings"

	"github.com/datahub/relay/internal/common/errs"
	"github.com/datahub/relay/internal/domain/model"
)

var (
	mobileRe = regexp.MustCompile(`^1\d{10}$`)
	idCardRe = regexp.MustCompile(`^\d{17}[\dX]$`)
)

// Parse runs参数校验; failures return 400001 (我方拦截, 不调上游/不计费).
func Parse(cmd *model.QueryCommand) (*model.UpstreamRequest, error) {
	if cmd == nil {
		return nil, errs.New(errs.ParamInvalid, "请求体为空")
	}
	mobile := strings.TrimSpace(cmd.Mobile)
	idCard := strings.ToUpper(strings.TrimSpace(cmd.IDCard))
	reqid := strings.TrimSpace(cmd.Reqid)

	if !mobileRe.MatchString(mobile) {
		return nil, errs.New(errs.ParamInvalid, "mobile 格式非法")
	}
	if !idCardRe.MatchString(idCard) {
		return nil, errs.New(errs.ParamInvalid, "idCard 格式非法")
	}
	if reqid == "" || len(reqid) > 20 {
		return nil, errs.New(errs.ParamInvalid, "reqid 必填且 ≤20 位")
	}

	return &model.UpstreamRequest{
		IDCard: idCard,
		Name:   strings.TrimSpace(cmd.Name),
		Mobile: mobile,
		Reqid:  reqid,
	}, nil
}
