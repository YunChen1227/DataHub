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
	totalCalls  int64 // 累计调用上游次数（CalledUpstream）
}

// licenseRec is the store-internal aggregate for a普通用户 (DESIGN §7.1/§16.2).
type licenseRec struct {
	view            model.LicenseView
	name            string
	mobile          string
	secret          string // 客户 MD5 加签 secret（开发期明文; 生产加密, §11.4）
	secretCreatedAt time.Time
	validTo         time.Time
	createdAt       time.Time
}

// Store implements the persistence ports for license/quota/ledger plus the
// admin console ports (admin users / audit).
type Store struct {
	mu sync.Mutex

	licenses    map[string]*licenseRec // licenseID -> rec
	appKeyIndex map[string]string      // appKey -> licenseID
	quotas      map[string]*quotaRow   // licenseID|route -> quota (按路由独立计数)

	ledgerByReqid map[string]*model.Ledger // appKey|version|reqid
	ledgerByID    map[int64]*model.Ledger

	audits []*model.AuditRecord
	admins map[string]*model.AdminUser // username -> admin

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
func (s *Store) SeedLicense(lic *model.LicenseView, secret, name, mobile string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.licenses[lic.LicenseID] = &licenseRec{
		view:            *lic,
		name:            name,
		mobile:          mobile,
		secret:          secret,
		secretCreatedAt: now,
		validTo:         now.AddDate(10, 0, 0),
		createdAt:       now,
	}
	s.appKeyIndex[lic.AppKey] = lic.LicenseID
	// 计数行 (licenseID|route) 由首次累加时按需创建。
}

// quotaKey scopes a counter row by (licenseID, route).
func quotaKey(licenseID, route string) string { return licenseID + "|" + route }

// quotaRowLocked returns (creating if needed) the counter row; caller holds s.mu.
func (s *Store) quotaRowLocked(licenseID, route string) *quotaRow {
	k := quotaKey(licenseID, route)
	q := s.quotas[k]
	if q == nil {
		q = &quotaRow{}
		s.quotas[k] = q
	}
	return q
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

func (s *Store) ServiceUsed(_ context.Context, licenseID, route string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q := s.quotas[quotaKey(licenseID, route)]; q != nil {
		return q.serviceUsed, nil
	}
	return 0, nil
}

func (s *Store) IncServiceUsed(_ context.Context, licenseID, route string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quotaRowLocked(licenseID, route).serviceUsed++
	return nil
}

func (s *Store) TotalCalls(_ context.Context, licenseID, route string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q := s.quotas[quotaKey(licenseID, route)]; q != nil {
		return q.totalCalls, nil
	}
	return 0, nil
}

func (s *Store) IncTotalCalls(_ context.Context, licenseID, route string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quotaRowLocked(licenseID, route).totalCalls++
	return nil
}

// --- port.LedgerRepository ---

func ledgerKey(appKey, version, reqid string) string { return appKey + "|" + version + "|" + reqid }

func (s *Store) FindByReqid(_ context.Context, appKey, route, reqid string) (*model.Ledger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.ledgerByReqid[ledgerKey(appKey, route, reqid)]
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
	s.ledgerByReqid[ledgerKey(l.AppKey, l.Version, l.Reqid)] = &stored
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
