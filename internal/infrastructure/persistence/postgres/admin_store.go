package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/datahub/relay/internal/domain/model"
)

// --- port.AdminUserRepository (DESIGN §16.1) ---

func (s *Store) FindAdmin(ctx context.Context, username string) (*model.AdminUser, error) {
	const q = `SELECT id, username, password_hash, role, created_at FROM admin_user WHERE username=$1`
	var a model.AdminUser
	err := s.pool.QueryRow(ctx, q, username).Scan(&a.ID, &a.Username, &a.PasswordHash, &a.Role, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Store) PutAdmin(ctx context.Context, a *model.AdminUser) error {
	const q = `INSERT INTO admin_user (username, password_hash, role, created_at)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (username) DO UPDATE SET password_hash=EXCLUDED.password_hash, role=EXCLUDED.role
		RETURNING id`
	return s.pool.QueryRow(ctx, q, a.Username, a.PasswordHash, a.Role, a.CreatedAt).Scan(&a.ID)
}

// --- port.UserAdminRepository (DESIGN §16.2) ---

func (s *Store) ListUsers(ctx context.Context) ([]*model.UserDetail, error) {
	const q = `SELECT license_id FROM license ORDER BY created_at DESC`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*model.UserDetail, 0, len(ids))
	for _, id := range ids {
		d, err := s.GetUser(ctx, id)
		if err != nil {
			return nil, err
		}
		if d != nil {
			out = append(out, d)
		}
	}
	return out, nil
}

func (s *Store) GetUser(ctx context.Context, licenseID string) (*model.UserDetail, error) {
	const q = `SELECT license_id, app_key, status, client_uuid, COALESCE(ip_whitelist,'{}'), created_at
	             FROM license WHERE license_id=$1`
	d := &model.UserDetail{}
	err := s.pool.QueryRow(ctx, q, licenseID).Scan(
		&d.LicenseID, &d.AppKey, &d.Status, &d.ClientUUID, &d.IPWhitelist, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// name is stored separately; fetch alongside (kept nullable-safe).
	_ = s.pool.QueryRow(ctx, `SELECT COALESCE(name,'') FROM license WHERE license_id=$1`, licenseID).Scan(&d.Name)
	snap, err := s.QuotaSnapshot(ctx, licenseID)
	if err != nil {
		return nil, err
	}
	d.ServiceTotal = snap.ServiceTotal
	d.ServiceUsed = snap.ServiceUsed
	d.UpstreamTotal = snap.UpstreamTotal
	d.UpstreamCommitted = snap.UpstreamCommitted
	d.UpstreamReserved = snap.UpstreamReserved
	return d, nil
}

func (s *Store) CreateUser(ctx context.Context, d *model.UserDetail, secret string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// uniqueness on app_key surfaces as a constraint error.
	const insLicense = `INSERT INTO license
		(license_id, app_key, app_secret_enc, client_uuid, name, status, valid_from, valid_to, ip_whitelist)
		VALUES ($1,$2,$3,$4,$5,$6, now(), now() + interval '3650 days', $7)`
	if _, err := tx.Exec(ctx, insLicense,
		d.LicenseID, d.AppKey, secret, d.ClientUUID, d.Name, d.Status, d.IPWhitelist); err != nil {
		return err
	}
	const insQuota = `INSERT INTO quota (license_id, dim, total) VALUES ($1,$2,$3)`
	if _, err := tx.Exec(ctx, insQuota, d.LicenseID, "SERVICE", d.ServiceTotal); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, insQuota, d.LicenseID, "UPSTREAM", d.UpstreamTotal); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) UpdateUser(ctx context.Context, licenseID, status string, serviceTotal, upstreamTotal int64, ipWhitelist []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`UPDATE license SET status=$2, ip_whitelist=$3, updated_at=now() WHERE license_id=$1`,
		licenseID, status, ipWhitelist); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE quota SET total=$2, updated_at=now() WHERE license_id=$1 AND dim='SERVICE'`,
		licenseID, serviceTotal); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE quota SET total=$2, updated_at=now() WHERE license_id=$1 AND dim='UPSTREAM'`,
		licenseID, upstreamTotal); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) DeleteUser(ctx context.Context, licenseID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM quota WHERE license_id=$1`, licenseID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM license WHERE license_id=$1`, licenseID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RotateSecret(ctx context.Context, licenseID, secret string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE license SET app_secret_enc=$2, updated_at=now() WHERE license_id=$1`, licenseID, secret)
	return err
}

// --- port.AuditRepository (DESIGN §16.3) ---

func (s *Store) AppendAudit(ctx context.Context, rec *model.AuditRecord) error {
	const q = `INSERT INTO audit_log
		(request_id, app_key, trade_no, reqid, client_ip, called_upstream, found_data,
		 busi_code, busi_msg, upstream_code, upstream_uid, upstream_logid, billed,
		 latency_ms, name_mask, id_card_mask, mobile_mask, err_msg)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		RETURNING id, created_at`
	return s.pool.QueryRow(ctx, q,
		rec.RequestID, rec.AppKey, rec.TradeNo, rec.Reqid, rec.ClientIP, rec.CalledUpstream, rec.FoundData,
		rec.BusiCode, rec.BusiMsg, rec.UpstreamCode, rec.UpstreamUID, rec.UpstreamLogID, rec.Billed,
		rec.LatencyMs, rec.NameMask, rec.IDCardMask, rec.MobileMask, rec.ErrMsg,
	).Scan(&rec.ID, &rec.CreatedAt)
}

func (s *Store) ListAudits(ctx context.Context, f model.AuditFilter) ([]*model.AuditRecord, error) {
	q := `SELECT id, request_id, app_key, COALESCE(trade_no,''), COALESCE(reqid,''),
		COALESCE(client_ip,''), called_upstream, found_data, COALESCE(busi_code,0),
		COALESCE(busi_msg,''), COALESCE(upstream_code,''), COALESCE(upstream_uid,''),
		COALESCE(upstream_logid,''), billed, COALESCE(latency_ms,0),
		COALESCE(name_mask,''), COALESCE(id_card_mask,''), COALESCE(mobile_mask,''),
		COALESCE(err_msg,''), created_at
		FROM audit_log WHERE 1=1`
	args := []any{}
	n := 0
	if f.AppKey != "" {
		n++
		q += " AND app_key=$" + itoa(n)
		args = append(args, f.AppKey)
	}
	if f.BusiCode != nil {
		n++
		q += " AND busi_code=$" + itoa(n)
		args = append(args, *f.BusiCode)
	}
	q += " ORDER BY id DESC"
	if f.Limit > 0 {
		n++
		q += " LIMIT $" + itoa(n)
		args = append(args, f.Limit)
	}
	if f.Offset > 0 {
		n++
		q += " OFFSET $" + itoa(n)
		args = append(args, f.Offset)
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.AuditRecord
	for rows.Next() {
		var r model.AuditRecord
		if err := rows.Scan(&r.ID, &r.RequestID, &r.AppKey, &r.TradeNo, &r.Reqid,
			&r.ClientIP, &r.CalledUpstream, &r.FoundData, &r.BusiCode,
			&r.BusiMsg, &r.UpstreamCode, &r.UpstreamUID, &r.UpstreamLogID, &r.Billed, &r.LatencyMs,
			&r.NameMask, &r.IDCardMask, &r.MobileMask, &r.ErrMsg, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// --- port.GlobalIPRepository (DESIGN §16.4) ---

func (s *Store) GetGlobalIP(ctx context.Context) ([]string, error) {
	var cidrs []string
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(cidrs,'{}') FROM ip_whitelist_global WHERE id=1`).Scan(&cidrs)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return cidrs, nil
}

func (s *Store) SetGlobalIP(ctx context.Context, cidrs []string) error {
	const q = `INSERT INTO ip_whitelist_global (id, cidrs, updated_at) VALUES (1,$1, now())
		ON CONFLICT (id) DO UPDATE SET cidrs=EXCLUDED.cidrs, updated_at=now()`
	_, err := s.pool.Exec(ctx, q, cidrs)
	return err
}

// itoa is a tiny positive-int formatter for $N placeholders.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
