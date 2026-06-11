// Package auth performs client License authentication + MD5 signature
// verification (DESIGN §8.1 / PDF §3.1). Per PDF the MD5 加签 carries no nonce
// or timestamp, so replay defense relies on HTTPS + IP 白名单 + appId/reqid 幂等.
package auth

import (
	"context"

	"github.com/datahub/relay/internal/common/errs"
	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/domain/port"
)

// APIKey is the fixed product code (PDF §1.5 / §2: apiKey 固定值).
const APIKey = "gama_ctmz_layer_score"

// Service validates incoming signed requests.
type Service struct {
	licenses port.LicenseRepository
	secrets  port.SecretProvider
	verifier port.SignatureVerifier
}

func New(licenses port.LicenseRepository, secrets port.SecretProvider, verifier port.SignatureVerifier) *Service {
	return &Service{licenses: licenses, secrets: secrets, verifier: verifier}
}

// Authenticate runs the §8.1 verification order and returns the license view.
// It returns an *errs.AppError (busiCode 1003/1002/1009/1004/1005) on any
// failure — none of which count 维度①/②.
func (s *Service) Authenticate(ctx context.Context, req *model.SignedRequest) (*model.LicenseView, error) {
	// 1. appId present (otherwise 1003 appId 异常).
	if req == nil || req.AppID == "" {
		return nil, errs.New(errs.BusiAppIDInvalid, "")
	}

	// 2. license exists for appId (otherwise 1002 账户信息不存在).
	lic, err := s.licenses.FindByAppID(ctx, req.AppID)
	if err != nil {
		return nil, errs.Wrap(errs.BusiAccountNotExist, "", err)
	}
	if lic == nil {
		return nil, errs.New(errs.BusiAccountNotExist, "")
	}

	// 3. license ACTIVE / in validity window (otherwise 1009 服务尚未开通).
	if !lic.Active() {
		return nil, errs.New(errs.BusiServiceNotOpen, "")
	}

	// 4. apiKey == fixed product code (otherwise 1004 产品编号异常).
	if req.APIKey != APIKey {
		return nil, errs.New(errs.BusiProductInvalid, "")
	}

	// 5. recompute signature with server-side secret and constant-time compare
	//    (otherwise 1005 账号信息异常).
	secret, err := s.secrets.AppSecret(ctx, lic.LicenseID)
	if err != nil {
		return nil, errs.Wrap(errs.BusiAccountAbnormal, "无法获取密钥", err)
	}
	if !s.verifier.Verify(req, secret) {
		return nil, errs.New(errs.BusiAccountAbnormal, "")
	}

	return lic, nil
}
