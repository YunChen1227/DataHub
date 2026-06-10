package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"

	"github.com/datahub/relay/internal/domain/model"
)

// HmacSha256Verifier implements port.SignatureVerifier per DESIGN §8.1:
//
//	signingString = appKey + "\n" + timestamp + "\n" + nonce + "\n" + sha256Hex(body)
//	X-Sign        = Base64( HMAC-SHA256(appSecret, signingString) )
type HmacSha256Verifier struct{}

func (HmacSha256Verifier) Verify(req *model.SignedRequest, appSecret string) bool {
	if req == nil || appSecret == "" || req.Sign == "" {
		return false
	}
	expected := sign(req.AppKey, req.Timestamp, req.Nonce, req.Body, appSecret)
	// constant-time compare to avoid timing side channels.
	return hmac.Equal([]byte(expected), []byte(req.Sign))
}

func sign(appKey, timestamp, nonce string, body []byte, appSecret string) string {
	bodySum := sha256.Sum256(body)
	signingString := appKey + "\n" + timestamp + "\n" + nonce + "\n" + hex.EncodeToString(bodySum[:])
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write([]byte(signingString))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
