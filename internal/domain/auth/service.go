// Package auth performs client License authentication + HMAC-SHA256 signature
// verification with replay protection (DESIGN §8.1).
package auth

import (
	"context"
	"strconv"
	"time"

	"github.com/datahub/relay/internal/common/errs"
	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/domain/port"
)

// Service validates incoming signed requests.
type Service struct {
	licenses port.LicenseRepository
	secrets  port.SecretProvider
	verifier port.SignatureVerifier
	nonces   port.NonceCache
	// skew is the allowed timestamp tolerance window (DESIGN §8.1 step 2).
	skew time.Duration
}

func New(licenses port.LicenseRepository, secrets port.SecretProvider, verifier port.SignatureVerifier, nonces port.NonceCache, skew time.Duration) *Service {
	if skew <= 0 {
		skew = 5 * time.Minute
	}
	return &Service{licenses: licenses, secrets: secrets, verifier: verifier, nonces: nonces, skew: skew}
}

// Authenticate runs the §8.1 verification order and returns the license view.
// It returns an *errs.AppError (401xxx) on any failure — none of which count
// 维度①/②.
func (s *Service) Authenticate(ctx context.Context, req *model.SignedRequest) (*model.LicenseView, error) {
	if req == nil || req.AppKey == "" {
		return nil, errs.New(errs.MissingAppKey, "")
	}

	// 1. appKey exists and license ACTIVE / in validity window.
	lic, err := s.licenses.FindByAppKey(ctx, req.AppKey)
	if err != nil {
		return nil, errs.Wrap(errs.MissingAppKey, "", err)
	}
	if lic == nil {
		return nil, errs.New(errs.MissingAppKey, "")
	}
	if !lic.Active() {
		return nil, errs.New(errs.LicenseInactive, "")
	}

	// 2. timestamp within tolerance window (replay defense).
	if !s.timestampFresh(req.Timestamp) {
		return nil, errs.New(errs.SignatureInvalid, "时间戳超出容差窗口")
	}

	// 3. nonce not seen within the window (dedupe).
	if s.nonces != nil {
		replay, nerr := s.nonces.SeenWithinWindow(ctx, req.AppKey, req.Nonce)
		if nerr != nil {
			return nil, errs.Wrap(errs.SignatureInvalid, "nonce 校验失败", nerr)
		}
		if replay {
			return nil, errs.New(errs.SignatureInvalid, "重放请求(nonce 已使用)")
		}
	}

	// 4. recompute signature with server-side appSecret and constant-time compare.
	secret, err := s.secrets.AppSecret(ctx, lic.LicenseID)
	if err != nil {
		return nil, errs.Wrap(errs.SignatureInvalid, "无法获取密钥", err)
	}
	if !s.verifier.Verify(req, secret) {
		return nil, errs.New(errs.SignatureInvalid, "")
	}

	return lic, nil
}

func (s *Service) timestampFresh(ts string) bool {
	if ts == "" {
		return false
	}
	ms, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	reqTime := time.UnixMilli(ms)
	delta := time.Since(reqTime)
	if delta < 0 {
		delta = -delta
	}
	return delta <= s.skew
}
