// Package memory is an in-process implementation of the persistence ports for
// local development and tests. Production MUST swap in Redis+Lua for the quota
// counters and a relational DB for the ledger (DESIGN §7.5 / §11), using the
// migrations under /migrations.
//
// All quota mutations hold a single mutex, which makes them atomic and faithful
// to the "检查并预留" semantics — sufficient for a single-process dev server.
package memory

import (
	"context"
	"sync"

	"github.com/datahub/relay/internal/domain/model"
)

type quotaRow struct {
	serviceTotal   int64
	serviceUsed    int64
	upstreamTotal  int64
	upstreamCommit int64
	upstreamReserv int64
}

// Store implements port.LicenseRepository, port.QuotaRepository,
// port.LedgerRepository and port.NonceCache.
type Store struct {
	mu sync.Mutex

	licensesByAppKey map[string]*model.LicenseView
	quotas           map[string]*quotaRow      // keyed by licenseID
	ledgerByReqid    map[string]*model.Ledger  // keyed by appKey|reqid
	ledgerByID       map[int64]*model.Ledger   // keyed by ledger ID
	nonces           map[string]struct{}       // keyed by appKey|nonce
	seq              int64
}

// New returns an empty store.
func New() *Store {
	return &Store{
		licensesByAppKey: make(map[string]*model.LicenseView),
		quotas:           make(map[string]*quotaRow),
		ledgerByReqid:    make(map[string]*model.Ledger),
		ledgerByID:       make(map[int64]*model.Ledger),
		nonces:           make(map[string]struct{}),
	}
}

// SeedLicense registers a demo license with both quota dimensions (dev helper).
func (s *Store) SeedLicense(lic *model.LicenseView, serviceTotal, upstreamTotal int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.licensesByAppKey[lic.AppKey] = lic
	s.quotas[lic.LicenseID] = &quotaRow{serviceTotal: serviceTotal, upstreamTotal: upstreamTotal}
}

// --- port.LicenseRepository ---

func (s *Store) FindByAppKey(_ context.Context, appKey string) (*model.LicenseView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lic, ok := s.licensesByAppKey[appKey]
	if !ok {
		return nil, nil
	}
	cp := *lic
	return &cp, nil
}

// --- port.QuotaRepository ---

func (s *Store) ServiceQuota(_ context.Context, licenseID string) (int64, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.quotas[licenseID]
	if q == nil {
		return 0, 0, nil
	}
	return q.serviceTotal, q.serviceUsed, nil
}

func (s *Store) IncServiceUsed(_ context.Context, licenseID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q := s.quotas[licenseID]; q != nil {
		q.serviceUsed++
	}
	return nil
}

func (s *Store) TryReserveUpstream(_ context.Context, licenseID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.quotas[licenseID]
	if q == nil {
		return false, nil
	}
	if q.upstreamTotal-q.upstreamCommit-q.upstreamReserv <= 0 {
		return false, nil
	}
	q.upstreamReserv++
	return true, nil
}

func (s *Store) CommitUpstream(_ context.Context, licenseID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q := s.quotas[licenseID]; q != nil {
		if q.upstreamReserv > 0 {
			q.upstreamReserv--
		}
		q.upstreamCommit++
	}
	return nil
}

func (s *Store) ReleaseUpstream(_ context.Context, licenseID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q := s.quotas[licenseID]; q != nil && q.upstreamReserv > 0 {
		q.upstreamReserv--
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

func (s *Store) UpdateState(_ context.Context, id int64, state model.BillingState, countedService, countedUpstream bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l := s.ledgerByID[id]; l != nil {
		l.State = state
		l.CountedService = countedService
		l.CountedUpstream = countedUpstream
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

// --- port.NonceCache ---

func (s *Store) SeenWithinWindow(_ context.Context, appKey, nonce string) (bool, error) {
	if nonce == "" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := appKey + "|" + nonce
	if _, ok := s.nonces[key]; ok {
		return true, nil
	}
	s.nonces[key] = struct{}{}
	return false, nil
}
