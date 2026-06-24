package upstream

import (
	"context"
	"fmt"

	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/domain/port"
)

// Provider identifiers for the upstream Router (DESIGN §6). 每个版本 stack 持有
// 一个单 provider 的 Router：x1→gama(伽马), v9/v8→income(经济能力)。
const (
	ProviderGama   = "gama"
	ProviderIncome = "income"
)

// Router selects the active data provider and delegates port.UpstreamPort calls
// to it. Providers are kept separated behind one interface so the active one is
// switchable by config without touching the application layer (DESIGN §6.0).
type Router struct {
	active    string
	providers map[string]port.UpstreamPort
}

// NewRouter builds a router over the registered providers with an active key.
func NewRouter(active string, providers map[string]port.UpstreamPort) (*Router, error) {
	if providers[active] == nil {
		return nil, fmt.Errorf("upstream router: active provider %q not registered", active)
	}
	return &Router{active: active, providers: providers}, nil
}

// Active returns the active provider key (for logging/health).
func (r *Router) Active() string { return r.active }

func (r *Router) Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error) {
	return r.providers[r.active].Query(ctx, req)
}

func (r *Router) Requery(ctx context.Context, reqid string) (*model.RequeryResult, error) {
	return r.providers[r.active].Requery(ctx, reqid)
}
