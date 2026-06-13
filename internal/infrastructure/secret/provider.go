// Package secret provides secrets (DESIGN §11.4). Production MUST back app
// secrets with KMS/Vault and never log or persist plaintext.
package secret

import "context"

// appSecretSource reads a user's bound MD5 secret by licenseID (DESIGN §16.2).
// The memory store implements it; production swaps in an encrypted store.
type appSecretSource interface {
	GetAppSecret(ctx context.Context, licenseID string) (string, error)
}

// StoreProvider implements port.SecretProvider. 客户下游 secrets 从用户存储动态
// 读取（管理员新建/轮换的用户即时生效）。上游 provider 凭证经进程配置注入到各
// upstream client，不走此处。
type StoreProvider struct {
	source appSecretSource
}

// NewStore builds a store-backed secret provider.
func NewStore(source appSecretSource) *StoreProvider {
	return &StoreProvider{source: source}
}

func (p *StoreProvider) AppSecret(ctx context.Context, licenseID string) (string, error) {
	return p.source.GetAppSecret(ctx, licenseID)
}
