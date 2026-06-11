// Package jwt is a minimal HS256 JWT implementation (DESIGN §16.1) with zero
// external dependencies. It supports signing and verifying compact tokens with
// a subject and expiry — sufficient for the admin console session.
package jwt

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var (
	ErrMalformed = errors.New("jwt: malformed token")
	ErrSignature = errors.New("jwt: bad signature")
	ErrExpired   = errors.New("jwt: token expired")
)

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

// Claims is the JWT payload used by the admin console.
type Claims struct {
	Sub string `json:"sub"` // admin username
	Exp int64  `json:"exp"` // unix seconds
	Iat int64  `json:"iat"`
}

var b64 = base64.RawURLEncoding

// Sign issues an HS256 token for sub valid for ttl.
func Sign(secret, sub string, ttl time.Duration) (string, int64, error) {
	now := time.Now()
	exp := now.Add(ttl).Unix()
	h, _ := json.Marshal(header{Alg: "HS256", Typ: "JWT"})
	c, _ := json.Marshal(Claims{Sub: sub, Exp: exp, Iat: now.Unix()})
	signingInput := b64.EncodeToString(h) + "." + b64.EncodeToString(c)
	sig := mac(secret, signingInput)
	return signingInput + "." + sig, exp, nil
}

// Verify validates the signature + expiry and returns the claims.
func Verify(secret, token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrMalformed
	}
	signingInput := parts[0] + "." + parts[1]
	if !hmac.Equal([]byte(mac(secret, signingInput)), []byte(parts[2])) {
		return nil, ErrSignature
	}
	raw, err := b64.DecodeString(parts[1])
	if err != nil {
		return nil, ErrMalformed
	}
	var c Claims
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, ErrMalformed
	}
	if time.Now().Unix() >= c.Exp {
		return nil, ErrExpired
	}
	return &c, nil
}

func mac(secret, input string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(input))
	return b64.EncodeToString(m.Sum(nil))
}
