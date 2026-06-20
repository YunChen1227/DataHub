package auth

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/datahub/relay/internal/domain/model"
)

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

// SignV9 computes the旧版 v9 (income_cls.md §输入参数) request signature:
//
//	verify = MD5(account + idCard + mobile + reqid + key).toUpperCase()
//
// key 为客户 appSecret。第三方旧契约口径，不可更改。
func SignV9(account, idCard, mobile, reqid, key string) string {
	sum := md5.Sum([]byte(account + idCard + mobile + reqid + key))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// SignV9Response signs the旧版 v9 响应 over its是签名字段 (income_cls.md §返回参数:
// code、uid 为是签名)：
//
//	verify = MD5(code + uid + key).toUpperCase()
//
// 注：income_cls.md 未给出响应签名的精确公式，此处沿用"是签名字段顺序+key"的一致口径，
// 待与旧版实现联调确认。
func SignV9Response(code, uid, key string) string {
	sum := md5.Sum([]byte(code + uid + key))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// EqualFoldSig compares two signatures case-insensitively in constant time.
func EqualFoldSig(a, b string) bool {
	a = strings.ToUpper(strings.TrimSpace(a))
	b = strings.ToUpper(strings.TrimSpace(b))
	if a == "" || len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
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
