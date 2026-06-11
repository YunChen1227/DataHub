// Package secret provides secrets (DESIGN §11.4). Production MUST back app
// secrets with KMS/Vault and never log or persist plaintext.
package secret

import "context"

// appSecretSource reads a user's bound MD5 secret by licenseID (DESIGN §16.2).
// The memory store implements it; production swaps in an encrypted store.
type appSecretSource interface {
	GetAppSecret(ctx context.Context, licenseID string) (string, error)
}

// StoreProvider implements port.SecretProvider. App secrets are looked up
// dynamically from the user store (so admin-created/rotated users work), while
// the upstream account/key come from process config.
type StoreProvider struct {
	source          appSecretSource
	upstreamAccount string
	upstreamKey     string
}

// NewStore builds a store-backed secret provider.
func NewStore(source appSecretSource, upstreamAccount, upstreamKey string) *StoreProvider {
	return &StoreProvider{source: source, upstreamAccount: upstreamAccount, upstreamKey: upstreamKey}
}

func (p *StoreProvider) AppSecret(ctx context.Context, licenseID string) (string, error) {
	return p.source.GetAppSecret(ctx, licenseID)
}

func (p *StoreProvider) UpstreamCredentials(_ context.Context) (string, string, error) {
	return p.upstreamAccount, p.upstreamKey, nil
}
