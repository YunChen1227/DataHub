// Package secret provides secrets (DESIGN §11.4). The static provider reads from
// process config/env for local dev; production MUST back this with KMS/Vault and
// never log or persist plaintext.
package secret

import "context"

// StaticProvider implements port.SecretProvider from in-memory values.
type StaticProvider struct {
	appSecrets      map[string]string // licenseID -> appSecret (HMAC key)
	upstreamAccount string
	upstreamKey     string
}

// NewStatic builds a static provider.
func NewStatic(appSecrets map[string]string, upstreamAccount, upstreamKey string) *StaticProvider {
	cp := make(map[string]string, len(appSecrets))
	for k, v := range appSecrets {
		cp[k] = v
	}
	return &StaticProvider{appSecrets: cp, upstreamAccount: upstreamAccount, upstreamKey: upstreamKey}
}

func (p *StaticProvider) AppSecret(_ context.Context, licenseID string) (string, error) {
	return p.appSecrets[licenseID], nil
}

func (p *StaticProvider) UpstreamCredentials(_ context.Context) (string, string, error) {
	return p.upstreamAccount, p.upstreamKey, nil
}
