package auth

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/datahub/relay/internal/domain/model"
)

// 旧版对外 v9 下游契约 (account/key/verify 大写 MD5) 已下线：三版本对外统一为
// x1 信封格式 (appKey + 小写 MD5 加签)，仅靠路由名区分。经济能力上游侧的大写
// MD5 签名逻辑迁入 infrastructure/upstream/income.go。

// Md5Verifier implements port.SignatureVerifier per DESIGN §8.1 / PDF §3.1:
//
//	待签名串 = 对 body 非空业务参数按参数名 ASCII 升序拼接 (name+value)…，末尾追加 secret
//	sign     = MD5(待签名串) 的小写 hex
//
// appId / apiKey / sign / encryptionType 不参与拼接。
type Md5Verifier struct{}

func (Md5Verifier) Verify(req *model.SignedRequest, secret string) bool {
	if req == nil || secret == "" || req.Sign == "" {
		return false
	}
	expected := Sign(req.BodyParams, secret)
	got := strings.ToLower(strings.TrimSpace(req.Sign))
	if len(expected) != len(got) {
		return false
	}
	// constant-time compare to avoid timing side channels.
	return subtle.ConstantTimeCompare([]byte(expected), []byte(got)) == 1
}

// Sign computes the client MD5 signature over the non-empty body params
// (DESIGN §8.1). Keys are sorted by ASCII ascending; empty values are skipped.
func Sign(params map[string]string, secret string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if v == "" {
			continue // 剔除值为空的参数
		}
		keys = append(keys, k)
	}
	sort.Strings(keys) // ASCII 升序（相同首字符依次比较后续字符）

	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteString(params[k])
	}
	sb.WriteString(secret)

	sum := md5.Sum([]byte(sb.String()))
	return hex.EncodeToString(sum[:]) // 小写 hex
}
