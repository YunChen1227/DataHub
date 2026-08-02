package auth

import (
	"context"
	"testing"

	"github.com/datahub/relay/internal/domain/model"
)

// countingRepo 实现 LicenseRepository + licenseWithSecretFinder，统计 DB 访问次数。
type countingRepo struct {
	fastCalls int
	slowCalls int
	lic       *model.LicenseView
	secret    string
}

func (r *countingRepo) FindByAppKey(_ context.Context, appKey string) (*model.LicenseView, error) {
	r.slowCalls++
	if r.lic != nil && r.lic.AppKey == appKey {
		cp := *r.lic
		return &cp, nil
	}
	return nil, nil
}

func (r *countingRepo) FindByAppKeyWithSecret(_ context.Context, appKey string) (*model.LicenseView, string, error) {
	r.fastCalls++
	if r.lic != nil && r.lic.AppKey == appKey {
		cp := *r.lic
		return &cp, r.secret, nil
	}
	return nil, "", nil
}

// staticSecrets 是不应被调用的 SecretProvider（快路径已带回 secret）。
type staticSecrets struct{ calls int }

func (s *staticSecrets) AppSecret(context.Context, string) (string, error) {
	s.calls++
	return "", nil
}

func signedReq(appKey, secret string, params map[string]string) *model.SignedRequest {
	return &model.SignedRequest{AppKey: appKey, Sign: Sign(params, secret), BodyParams: params}
}

// TestAuthenticateCacheAndInvalidate 锁定鉴权缓存行为：
// 首次未命中走快路径单查询；TTL 内重复请求零 DB 读；Invalidate 后强制回源。
func TestAuthenticateCacheAndInvalidate(t *testing.T) {
	repo := &countingRepo{
		lic:    &model.LicenseView{LicenseID: "L1", AppKey: "ak1", Status: "ACTIVE"},
		secret: "s3cret",
	}
	secrets := &staticSecrets{}
	svc := New(repo, secrets, Md5Verifier{})
	params := map[string]string{"mobile": "13800000000"}

	// 第一次：快路径回源 1 次。
	if _, err := svc.Authenticate(context.Background(), signedReq("ak1", "s3cret", params)); err != nil {
		t.Fatalf("auth #1: %v", err)
	}
	if repo.fastCalls != 1 || repo.slowCalls != 0 || secrets.calls != 0 {
		t.Fatalf("首次应走快路径单查询: fast=%d slow=%d secrets=%d", repo.fastCalls, repo.slowCalls, secrets.calls)
	}

	// 第二、三次：缓存命中，零 DB 读。
	for i := 0; i < 2; i++ {
		if _, err := svc.Authenticate(context.Background(), signedReq("ak1", "s3cret", params)); err != nil {
			t.Fatalf("auth cached: %v", err)
		}
	}
	if repo.fastCalls != 1 {
		t.Fatalf("缓存未生效: fastCalls=%d, want 1", repo.fastCalls)
	}

	// 失效后回源。
	svc.Invalidate("ak1")
	if _, err := svc.Authenticate(context.Background(), signedReq("ak1", "s3cret", params)); err != nil {
		t.Fatalf("auth after invalidate: %v", err)
	}
	if repo.fastCalls != 2 {
		t.Fatalf("Invalidate 后应回源: fastCalls=%d, want 2", repo.fastCalls)
	}

	// 错误签名仍被拦截（缓存不绕过验签）。
	if _, err := svc.Authenticate(context.Background(), &model.SignedRequest{AppKey: "ak1", Sign: "bad", BodyParams: params}); err == nil {
		t.Fatalf("错签必须被拦截")
	}
}

// TestAuthenticateSuspendedCached 停用账户也吃缓存（高频重试不打库），且状态判断正确。
func TestAuthenticateSuspendedCached(t *testing.T) {
	repo := &countingRepo{
		lic:    &model.LicenseView{LicenseID: "L2", AppKey: "ak2", Status: "SUSPENDED"},
		secret: "s",
	}
	svc := New(repo, &staticSecrets{}, Md5Verifier{})
	for i := 0; i < 3; i++ {
		if _, err := svc.Authenticate(context.Background(), signedReq("ak2", "s", nil)); err == nil {
			t.Fatalf("停用账户必须 1009 拒绝")
		}
	}
	if repo.fastCalls != 1 {
		t.Fatalf("停用账户应命中缓存: fastCalls=%d, want 1", repo.fastCalls)
	}
}
