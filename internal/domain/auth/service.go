// Package auth performs client License authentication + MD5 signature
// verification (接口文档-经济能力.doc 网关 appKey/appSecret / DESIGN §8.1). The
// MD5 加签 carries no nonce or timestamp, so replay defense relies on HTTPS +
// IP 白名单 + appKey/reqid 幂等.
package auth

import (
	"context"
	"sync"
	"time"

	"github.com/datahub/relay/internal/common/errs"
	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/domain/port"
)

// cacheTTL 是 license+secret 进程内缓存的存活时间。后台增删改/轮换密钥会通过
// Invalidate 即时失效（同进程回调，见 admin.Service.WithLicenseChangeHook）；
// TTL 只是多实例部署/回调遗漏时的兜底上界。
const cacheTTL = 10 * time.Second

// licenseWithSecretFinder 是可选的快路径接口：license 与 app_secret_enc 在同一张
// 表的同一行，实现方（memory/postgres store）可一次查询同时取回两者，省一次 DB
// 往返。未实现时回退为 FindByAppKey + AppSecret 两次调用（保留 SecretProvider
// 抽象，未来接 KMS 时密钥不再与 license 同源）。
type licenseWithSecretFinder interface {
	FindByAppKeyWithSecret(ctx context.Context, appKey string) (*model.LicenseView, string, error)
}

// cacheEntry 是一条已验证存在的 license + 密钥缓存。
type cacheEntry struct {
	view      model.LicenseView
	secret    string
	expiresAt time.Time
}

// Service validates incoming signed requests. license+secret 带进程内 TTL 缓存
// （每域一个 Service 实例，v8/v9 共享同一实例故共享缓存），绝大多数请求鉴权
// 零 DB 读；未命中时优先走单查询快路径。
type Service struct {
	licenses port.LicenseRepository
	secrets  port.SecretProvider
	verifier port.SignatureVerifier

	mu    sync.RWMutex
	cache map[string]cacheEntry // appKey -> entry
}

func New(licenses port.LicenseRepository, secrets port.SecretProvider, verifier port.SignatureVerifier) *Service {
	return &Service{licenses: licenses, secrets: secrets, verifier: verifier, cache: make(map[string]cacheEntry)}
}

// Invalidate 移除某个 appKey 的缓存（后台修改状态/轮换密钥/删除用户时回调，
// 保证变更即时生效，不必等 TTL）。appKey 为空时不做任何事。
func (s *Service) Invalidate(appKey string) {
	if appKey == "" {
		return
	}
	s.mu.Lock()
	delete(s.cache, appKey)
	s.mu.Unlock()
}

// Authenticate runs the verification order and returns the license view. It
// returns an *errs.AppError (busiCode 1003/1002/1009/1005) on any failure —
// none of which count 维度①/②.
func (s *Service) Authenticate(ctx context.Context, req *model.SignedRequest) (*model.LicenseView, error) {
	// 1. appKey present (otherwise 1003 appKey 异常).
	if req == nil || req.AppKey == "" {
		return nil, errs.New(errs.BusiAppIDInvalid, "")
	}

	// 2+4 数据面：license + secret（缓存 → 单查询快路径 → 两次调用回退）。
	lic, secret, err := s.lookup(ctx, req.AppKey)
	if err != nil {
		return nil, err
	}
	if lic == nil {
		// 不做负缓存：未知 appKey 每次都查库（避免误缓存把新建用户挡在门外）。
		return nil, errs.New(errs.BusiAccountNotExist, "")
	}

	// 3. license ACTIVE / in validity window (otherwise 1009 服务尚未开通).
	// 注意校验顺序不变：先状态后签名，错误码语义与既有对外文档一致。
	if !lic.Active() {
		return nil, errs.New(errs.BusiServiceNotOpen, "")
	}

	// 4. recompute signature with server-side secret and constant-time compare
	//    (otherwise 1005 账号信息异常).
	if secret == "" {
		return nil, errs.New(errs.BusiAccountAbnormal, "无法获取密钥")
	}
	if !s.verifier.Verify(req, secret) {
		return nil, errs.New(errs.BusiAccountAbnormal, "")
	}

	return lic, nil
}

// lookup 取 license+secret：缓存命中直接返回；未命中查库并回填缓存。
// 缓存的是「存在的行」本身（含 SUSPENDED 等状态），状态判断在调用方进行——
// 这样停用的账户也吃缓存，不会被高频重试打到数据库。
func (s *Service) lookup(ctx context.Context, appKey string) (*model.LicenseView, string, error) {
	now := time.Now()
	s.mu.RLock()
	e, ok := s.cache[appKey]
	s.mu.RUnlock()
	if ok && now.Before(e.expiresAt) {
		cp := e.view
		return &cp, e.secret, nil
	}

	var lic *model.LicenseView
	var secret string
	if f, ok := s.licenses.(licenseWithSecretFinder); ok {
		// 快路径：同一行一次取回 license + secret（省一次 DB 往返）。
		v, sec, err := f.FindByAppKeyWithSecret(ctx, appKey)
		if err != nil {
			return nil, "", errs.Wrap(errs.BusiAccountNotExist, "", err)
		}
		lic, secret = v, sec
	} else {
		v, err := s.licenses.FindByAppKey(ctx, appKey)
		if err != nil {
			return nil, "", errs.Wrap(errs.BusiAccountNotExist, "", err)
		}
		lic = v
		if lic != nil {
			sec, err := s.secrets.AppSecret(ctx, lic.LicenseID)
			if err != nil {
				return nil, "", errs.Wrap(errs.BusiAccountAbnormal, "无法获取密钥", err)
			}
			secret = sec
		}
	}
	if lic == nil {
		return nil, "", nil
	}

	s.mu.Lock()
	s.cache[appKey] = cacheEntry{view: *lic, secret: secret, expiresAt: now.Add(cacheTTL)}
	s.mu.Unlock()
	cp := *lic
	return &cp, secret, nil
}
