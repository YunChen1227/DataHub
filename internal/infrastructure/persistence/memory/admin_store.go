package memory

import (
	"context"
	"sort"

	"github.com/datahub/relay/internal/domain/model"
)

// --- port.AdminUserRepository (DESIGN §16.1) ---

func (s *Store) FindAdmin(_ context.Context, username string) (*model.AdminUser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.admins[username]
	if !ok {
		return nil, nil
	}
	cp := *a
	return &cp, nil
}

func (s *Store) PutAdmin(_ context.Context, a *model.AdminUser) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adminSeq++
	a.ID = s.adminSeq
	cp := *a
	s.admins[a.Username] = &cp
	return nil
}

// --- port.UserAdminRepository (DESIGN §16.2) ---

func (s *Store) ListUsers(_ context.Context) ([]*model.UserDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*model.UserDetail, 0, len(s.licenses))
	for id := range s.licenses {
		out = append(out, s.userDetailLocked(id))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) GetUser(_ context.Context, licenseID string) (*model.UserDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.licenses[licenseID] == nil {
		return nil, nil
	}
	return s.userDetailLocked(licenseID), nil
}

func (s *Store) CreateUser(_ context.Context, d *model.UserDetail, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.appKeyIndex[d.AppKey]; ok {
		return errAppKeyExists
	}
	s.licenses[d.LicenseID] = &licenseRec{
		view: model.LicenseView{
			LicenseID:   d.LicenseID,
			AppKey:      d.AppKey,
			ClientUUID:  d.ClientUUID,
			Status:      d.Status,
			IPWhitelist: append([]string(nil), d.IPWhitelist...),
		},
		name:      d.Name,
		secret:    secret,
		createdAt: d.CreatedAt,
	}
	s.appKeyIndex[d.AppKey] = d.LicenseID
	s.quotas[d.LicenseID] = &quotaRow{serviceTotal: d.ServiceTotal, upstreamTotal: d.UpstreamTotal}
	return nil
}

func (s *Store) UpdateUser(_ context.Context, licenseID, status string, serviceTotal, upstreamTotal int64, ipWhitelist []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.licenses[licenseID]
	if rec == nil {
		return nil
	}
	rec.view.Status = status
	rec.view.IPWhitelist = append([]string(nil), ipWhitelist...)
	if q := s.quotas[licenseID]; q != nil {
		q.serviceTotal = serviceTotal
		q.upstreamTotal = upstreamTotal
	}
	return nil
}

func (s *Store) DeleteUser(_ context.Context, licenseID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec := s.licenses[licenseID]; rec != nil {
		delete(s.appKeyIndex, rec.view.AppKey)
	}
	delete(s.licenses, licenseID)
	delete(s.quotas, licenseID)
	return nil
}

func (s *Store) RotateSecret(_ context.Context, licenseID, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec := s.licenses[licenseID]; rec != nil {
		rec.secret = secret
	}
	return nil
}

// userDetailLocked builds a UserDetail; caller MUST hold s.mu.
func (s *Store) userDetailLocked(licenseID string) *model.UserDetail {
	rec := s.licenses[licenseID]
	if rec == nil {
		return nil
	}
	q := s.quotas[licenseID]
	if q == nil {
		q = &quotaRow{}
	}
	return &model.UserDetail{
		LicenseID:         licenseID,
		AppKey:            rec.view.AppKey,
		Name:              rec.name,
		Status:            rec.view.Status,
		ClientUUID:        rec.view.ClientUUID,
		ServiceTotal:      q.serviceTotal,
		ServiceUsed:       q.serviceUsed,
		UpstreamTotal:     q.upstreamTotal,
		UpstreamCommitted: q.upstreamCommit,
		UpstreamReserved:  q.upstreamReserv,
		IPWhitelist:       append([]string(nil), rec.view.IPWhitelist...),
		CreatedAt:         rec.createdAt,
	}
}

// --- port.AuditRepository (DESIGN §16.3) ---

func (s *Store) AppendAudit(_ context.Context, rec *model.AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auditSeq++
	rec.ID = s.auditSeq
	cp := *rec
	s.audits = append(s.audits, &cp)
	return nil
}

func (s *Store) ListAudits(_ context.Context, f model.AuditFilter) ([]*model.AuditRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*model.AuditRecord, 0, f.Limit)
	skipped := 0
	// newest first.
	for i := len(s.audits) - 1; i >= 0; i-- {
		a := s.audits[i]
		if f.AppKey != "" && a.AppKey != f.AppKey {
			continue
		}
		if f.BusiCode != nil && a.BusiCode != *f.BusiCode {
			continue
		}
		if skipped < f.Offset {
			skipped++
			continue
		}
		cp := *a
		out = append(out, &cp)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out, nil
}

// --- port.GlobalIPRepository (DESIGN §16.4) ---

func (s *Store) GetGlobalIP(_ context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.globalIP...), nil
}

func (s *Store) SetGlobalIP(_ context.Context, cidrs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.globalIP = append([]string(nil), cidrs...)
	return nil
}
