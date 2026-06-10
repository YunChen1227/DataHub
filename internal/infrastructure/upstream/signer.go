package upstream

import (
	"crypto/md5"
	"encoding/hex"
	"strings"

	"github.com/datahub/relay/internal/domain/model"
)

// Sign computes the upstream MD5 signature (DESIGN §8.2 / income_cls.md):
//
//	verify = MD5(account + idCard + mobile + reqid + key).toUpperCase()
//
// The upstream is a third party, so this口径 cannot be changed.
func Sign(req *model.UpstreamRequest, account, key string) string {
	raw := account + req.IDCard + req.Mobile + req.Reqid + key
	sum := md5.Sum([]byte(raw))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}
