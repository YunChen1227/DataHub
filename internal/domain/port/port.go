// Package port declares the outbound interfaces (hexagonal "ports") the domain
// depends on. Infrastructure adapters implement them; the domain never imports
// infrastructure, keeping the dependency arrow pointing inward.
package port

import (
	"context"

	"github.com/datahub/relay/internal/domain/model"
)

// LicenseRepository loads license/identity rows (DESIGN §11.1).
type LicenseRepository interface {
	FindByAppID(ctx context.Context, appID string) (*model.LicenseView, error)
}

// QuotaRepository performs atomic两维度 counting (DESIGN §7.5). All mutating
// methods MUST be atomic (Redis+Lua or DB conditional update) to避免超卖.
type QuotaRepository interface {
	// ServiceQuota returns 维度① total/used.
	ServiceQuota(ctx context.Context, licenseID string) (total, used int64, err error)
	// IncServiceUsed increments 维度① used by 1.
	IncServiceUsed(ctx context.Context, licenseID string) error
	// TryReserveUpstream atomically checks 维度② remaining>0 and reserved++.
	// Returns false when the cost ceiling is reached.
	TryReserveUpstream(ctx context.Context, licenseID string) (ok bool, err error)
	// CommitUpstream moves a reservation to committed (reserved--, committed++).
	CommitUpstream(ctx context.Context, licenseID string) error
	// ReleaseUpstream cancels a reservation (reserved--).
	ReleaseUpstream(ctx context.Context, licenseID string) error
}

// LedgerRepository is the append-only billing台账 store (DESIGN §11.3).
type LedgerRepository interface {
	// FindByReqid returns the ledger for (appID, reqid) or (nil, nil) if absent.
	FindByReqid(ctx context.Context, appID, reqid string) (*model.Ledger, error)
	// Append inserts a new PENDING ledger and back-fills the assigned ID.
	Append(ctx context.Context, l *model.Ledger) error
	// UpdateState settles a ledger to BILLED/UNBILLED with the count flags.
	UpdateState(ctx context.Context, id int64, state model.BillingState, countedService, countedUpstream bool) error
	// ListByState powers the re-query worker and reconciliation job.
	ListByState(ctx context.Context, state model.BillingState, limit int) ([]*model.Ledger, error)
}

// UpstreamPort talks to the income_cls provider (DESIGN §6).
type UpstreamPort interface {
	Query(ctx context.Context, req *model.UpstreamRequest) (*model.UpstreamResult, error)
	// Requery is the idempotent re-query by reqid (never double-charges).
	Requery(ctx context.Context, reqid string) (*model.RequeryResult, error)
}

// SecretProvider supplies secrets from KMS/Vault (DESIGN §11.4); never logged.
type SecretProvider interface {
	AppSecret(ctx context.Context, licenseID string) (string, error)
	UpstreamCredentials(ctx context.Context) (account, key string, err error)
}

// SignatureVerifier validates the client MD5 signature (DESIGN §8.1 / PDF §3.1).
type SignatureVerifier interface {
	Verify(req *model.SignedRequest, appSecret string) bool
}

// --- Admin console ports (DESIGN §16) ---

// AdminUserRepository stores operator accounts (DESIGN §16.1).
type AdminUserRepository interface {
	FindAdmin(ctx context.Context, username string) (*model.AdminUser, error)
	PutAdmin(ctx context.Context, a *model.AdminUser) error
}

// UserAdminRepository manages普通用户 (license) lifecycle + quota + per-user IP
// whitelist + bound secret for the admin console (DESIGN §16.2).
type UserAdminRepository interface {
	ListUsers(ctx context.Context) ([]*model.UserDetail, error)
	GetUser(ctx context.Context, licenseID string) (*model.UserDetail, error)
	// CreateUser persists a new license + quota + bound secret (plaintext secret
	// is passed in; the adapter is responsible for at-rest encryption, §11.4).
	CreateUser(ctx context.Context, d *model.UserDetail, secret string) error
	UpdateUser(ctx context.Context, licenseID string, status string, serviceTotal, upstreamTotal int64, ipWhitelist []string) error
	DeleteUser(ctx context.Context, licenseID string) error
	RotateSecret(ctx context.Context, licenseID, secret string) error
}

// AuditRepository is the append-only audit log store (DESIGN §16.3/§16.5).
type AuditRepository interface {
	AppendAudit(ctx context.Context, rec *model.AuditRecord) error
	ListAudits(ctx context.Context, f model.AuditFilter) ([]*model.AuditRecord, error)
}

// GlobalIPRepository stores the global IP whitelist (DESIGN §16.4).
type GlobalIPRepository interface {
	GetGlobalIP(ctx context.Context) ([]string, error)
	SetGlobalIP(ctx context.Context, cidrs []string) error
}
