package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

const (
	appKeyLen = 12
	secretLen = 32
	saltLen   = 16
	// alphabet excludes ambiguous chars for human-readable appKey.
	alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
)

// GenerateAppKey returns a random, readable client appKey (DESIGN §16.2).
func GenerateAppKey() string { return randAlpha(appKeyLen) }

// GenerateSecret returns a random hex secret for MD5 加签 (DESIGN §16.4).
func GenerateSecret() string {
	b := make([]byte, secretLen/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// HashPassword returns "salt$hash" using salted SHA-256 (DESIGN §16.1).
// NOTE: 生产应换 bcrypt/argon2; salted SHA-256 仅用于开发骨架.
func HashPassword(plain string) string {
	salt := make([]byte, saltLen)
	_, _ = rand.Read(salt)
	sum := sha256.Sum256(append(salt, []byte(plain)...))
	return hex.EncodeToString(salt) + "$" + hex.EncodeToString(sum[:])
}

// VerifyPassword checks plain against a "salt$hash" string in constant time.
func VerifyPassword(plain, stored string) bool {
	i := indexByte(stored, '$')
	if i <= 0 {
		return false
	}
	salt, err := hex.DecodeString(stored[:i])
	if err != nil {
		return false
	}
	sum := sha256.Sum256(append(salt, []byte(plain)...))
	want, err := hex.DecodeString(stored[i+1:])
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(sum[:], want) == 1
}

func randAlpha(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	out := make([]byte, n)
	for i, c := range b {
		out[i] = alphabet[int(c)%len(alphabet)]
	}
	return string(out)
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
