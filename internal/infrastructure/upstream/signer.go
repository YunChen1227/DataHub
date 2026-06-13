package upstream

import (
	"crypto/md5"
	"encoding/hex"
	"strings"

	"github.com/datahub/relay/internal/domain/model"
)

// SignIncomeCls computes the income_cls MD5 signature (income_cls.md §输入参数):
//
//	verify = MD5(account + idCard + mobile + reqid + key).toUpperCase()
//
// The upstream is a third party, so this口径 cannot be changed.
func SignIncomeCls(req *model.UpstreamRequest, account, key string) string {
	raw := account + req.IDCard + req.Mobile + req.Reqid + key
	sum := md5.Sum([]byte(raw))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}
