// Package reqid implements the requestId generation rule (DESIGN §9.2):
//
//	bodyHash  = SHA-256(body) 前 8 个 hex 字符
//	seed      = ts + "|" + clientUuid + "|" + SHA-256(body)
//	core      = Base32( SHA-256(seed) ) 前 10 位
//	requestId = ts(Base36) + "-" + clientShort + "-" + bodyHash + "-" + core
package reqid

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"strconv"
	"strings"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// Generate builds a sortable, readable, collision-resistant requestId.
//   - ts:          arrival time in unix millis (sortable prefix).
//   - clientShort: client UUID / appKey (falls back to "anon" upstream).
//   - body:        raw request body bytes (empty for GET).
func Generate(ts int64, clientShort string, body []byte) string {
	bodySum := sha256.Sum256(body)
	bodyHex := hex.EncodeToString(bodySum[:])
	bodyHash := bodyHex[:8]

	seed := strconv.FormatInt(ts, 10) + "|" + clientShort + "|" + bodyHex
	coreSum := sha256.Sum256([]byte(seed))
	core := b32.EncodeToString(coreSum[:])[:10]

	return strconv.FormatInt(ts, 36) + "-" + ClientShort(clientShort) + "-" + bodyHash + "-" + core
}

// ClientShort normalises a client identifier into a compact log-friendly token.
func ClientShort(client string) string {
	client = strings.TrimSpace(client)
	if client == "" {
		return "anon"
	}
	if len(client) > 8 {
		return client[:8]
	}
	return client
}
