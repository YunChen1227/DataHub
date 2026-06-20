// Package memory is an in-process implementation of the persistence ports for
// local development and tests. Production MUST swap in Redis+Lua for the quota
// counters and a relational DB for the ledger/audit (DESIGN §7.5 / §11 / §16),
// using the migrations under /migrations.
//
// All mutations hold a single mutex, which makes them atomic and faithful to the
// "检查并预留" semantics — sufficient for a single-process dev server.
package memory

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/datahub/relay/internal/domain/model"
)

type quotaRow struct {
	serviceUsed int64 // 累计成功查得数（busiCode 10）
}

// licenseRec is the store-internal aggregate for a普通用户 (DESIGN §7.1/§16.2).
type licenseRec struct {
	view      model.LicenseView
	name      string
	secret    string // 客户 MD5 加签 secret（开发期明文; 生产加密, §11.4）
	createdAt time.Time
}

// Store implements the persistence ports for license/quota/ledger plus the
// admin console ports (admin users / audit / global IP).
type Store struct {
	mu sync.Mutex

	licenses    map[string]*licenseRec // licenseID -> rec
	appKeyIndex map[string]string      // appKey -> licenseID
	quotas      map[string]*quotaRow   // licenseID -> quota

	ledgerByReqid map[string]*model.Ledger // appKey|reqid
	ledgerByID    map[int64]*model.Ledger

	audits   []*model.AuditRecord
	admins   map[string]*model.AdminUser // username -> admin
	globalIP []string

	seq      int64
	auditSeq int64
	adminSeq int64
}

// New returns an empty store.
func New() *Store {
	return &Store{
		licenses:      make(map[string]*licenseRec),
		appKeyIndex:   make(map[string]string),
		quotas:        make(map[string]*quotaRow),
		ledgerByReqid: make(map[string]*model.Ledger),
		ledgerByID:    make(map[int64]*model.Ledger),
		admins:        make(map[string]*model.AdminUser),
	}
}

// SeedLicense registers a demo license with a bound secret (dev helper).
func (s *Store) SeedLicense(lic *model.LicenseView, secret, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.licenses[lic.LicenseID] = &licenseRec{
		view:      *lic,
		name:      name,
		secret:    secret,
		createdAt: time.Now(),
	}
	s.appKeyIndex[lic.AppKey] = lic.LicenseID
	s.quotas[lic.LicenseID] = &quotaRow{}
}

// --- port.LicenseRepository ---

func (s *Store) FindByAppKey(_ context.Context, appKey string) (*model.LicenseView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	licenseID, ok := s.appKeyIndex[appKey]
	if !ok {
		return nil, nil
	}
	rec := s.licenses[licenseID]
	if rec == nil {
		return nil, nil
	}
	cp := rec.view
	cp.IPWhitelist = append([]string(nil), rec.view.IPWhitelist...)
	return &cp, nil
}

// GetAppSecret backs the store-backed SecretProvider (DESIGN §16.2/§11.4).
func (s *Store) GetAppSecret(_ context.Context, licenseID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec := s.licenses[licenseID]; rec != nil {
		return rec.secret, nil
	}
	return "", nil
}

// --- port.QuotaRepository ---

func (s *Store) ServiceUsed(_ context.Context, licenseID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.quotas[licenseID]
	if q == nil {
		return 0, nil
	}
	return q.serviceUsed, nil
}

func (s *Store) IncServiceUsed(_ context.Context, licenseID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q := s.quotas[licenseID]; q != nil {
		q.serviceUsed++
	}
	return nil
}

// --- port.LedgerRepository ---

func (s *Store) FindByReqid(_ context.Context, appKey, reqid string) (*model.Ledger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.ledgerByReqid[appKey+"|"+reqid]
	if !ok {
		return nil, nil
	}
	cp := *l
	return &cp, nil
}

func (s *Store) Append(_ context.Context, l *model.Ledger) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	l.ID = s.seq
	stored := *l
	s.ledgerByID[l.ID] = &stored
	s.ledgerByReqid[l.AppKey+"|"+l.Reqid] = &stored
	return nil
}

func (s *Store) UpdateState(_ context.Context, id int64, state model.BillingState, countedService bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l := s.ledgerByID[id]; l != nil {
		l.State = state
		l.CountedService = countedService
	}
	return nil
}

func (s *Store) ListByState(_ context.Context, state model.BillingState, limit int) ([]*model.Ledger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*model.Ledger, 0, limit)
	for _, l := range s.ledgerByID {
		if l.State == state {
			cp := *l
			out = append(out, &cp)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

var errAppKeyExists = errors.New("appKey 已存在")
