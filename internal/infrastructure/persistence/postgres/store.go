// Package postgres is the production persistence adapter (DESIGN §11). It backs
// the durable repositories — license, billing ledger, audit log, admin users,
// IP whitelist — on PostgreSQL via pgxpool. The dual-dimension quota counters
// live in Redis (see persistence/redis) with this store as the durable mirror.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/datahub/relay/internal/domain/model"
)

// Store implements the durable persistence ports on PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

// New opens a pgx pool against the given DSN and verifies connectivity.
func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pg pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Pool exposes the underlying pool (used by the migration runner).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// --- port.LicenseRepository ---

func (s *Store) FindByAppKey(ctx context.Context, appKey string) (*model.LicenseView, error) {
	const q = `SELECT license_id, app_key, client_uuid, status, COALESCE(ip_whitelist, '{}')
	             FROM license WHERE app_key=$1`
	var v model.LicenseView
	err := s.pool.QueryRow(ctx, q, appKey).Scan(&v.LicenseID, &v.AppKey, &v.ClientUUID, &v.Status, &v.IPWhitelist)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// GetAppSecret backs the store-backed SecretProvider (DESIGN §16.2/§11.4). The
// column stores the at-rest value; dev keeps it plaintext.
func (s *Store) GetAppSecret(ctx context.Context, licenseID string) (string, error) {
	var secret string
	err := s.pool.QueryRow(ctx, `SELECT app_secret_enc FROM license WHERE license_id=$1`, licenseID).Scan(&secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return secret, nil
}

// --- port.LedgerRepository ---

const ledgerCols = `id, app_key, COALESCE(trade_no,''), reqid, request_id,
	COALESCE(upstream_code,''), COALESCE(busi_code,0), COALESCE(upstream_uid,''),
	COALESCE(upstream_logid,''), state, counted_service, counted_upstream`

func scanLedger(row pgx.Row) (*model.Ledger, error) {
	var l model.Ledger
	var state string
	err := row.Scan(&l.ID, &l.AppKey, &l.TradeNo, &l.Reqid, &l.RequestID,
		&l.UpstreamCode, &l.BusiCode, &l.UpstreamUID, &l.UpstreamLogID,
		&state, &l.CountedService, &l.CountedUpstream)
	if err != nil {
		return nil, err
	}
	l.State = model.BillingState(state)
	return &l, nil
}

func (s *Store) FindByReqid(ctx context.Context, appKey, reqid string) (*model.Ledger, error) {
	q := `SELECT ` + ledgerCols + ` FROM billing_ledger WHERE app_key=$1 AND reqid=$2`
	l, err := scanLedger(s.pool.QueryRow(ctx, q, appKey, reqid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return l, nil
}

func (s *Store) Append(ctx context.Context, l *model.Ledger) error {
	const q = `INSERT INTO billing_ledger
		(app_key, trade_no, reqid, request_id, upstream_code, busi_code,
		 upstream_uid, upstream_logid, state, counted_service, counted_upstream)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`
	return s.pool.QueryRow(ctx, q,
		l.AppKey, l.TradeNo, l.Reqid, l.RequestID, l.UpstreamCode, l.BusiCode,
		l.UpstreamUID, l.UpstreamLogID, string(l.State), l.CountedService, l.CountedUpstream,
	).Scan(&l.ID)
}

func (s *Store) UpdateState(ctx context.Context, id int64, state model.BillingState, countedService, countedUpstream bool) error {
	const q = `UPDATE billing_ledger
		SET state=$2, counted_service=$3, counted_upstream=$4, settled_at=now()
		WHERE id=$1`
	_, err := s.pool.Exec(ctx, q, id, string(state), countedService, countedUpstream)
	return err
}

func (s *Store) ListByState(ctx context.Context, state model.BillingState, limit int) ([]*model.Ledger, error) {
	q := `SELECT ` + ledgerCols + ` FROM billing_ledger WHERE state=$1 ORDER BY id`
	args := []any{string(state)}
	if limit > 0 {
		q += ` LIMIT $2`
		args = append(args, limit)
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Ledger
	for rows.Next() {
		l, err := scanLedger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// --- quota durable mirror (read by Redis quota repo + admin views) ---

// QuotaSnapshot is the durable quota state for a license (DESIGN §11.2).
type QuotaSnapshot struct {
	ServiceTotal      int64
	ServiceUsed       int64
	UpstreamTotal     int64
	UpstreamCommitted int64
	UpstreamReserved  int64
}

// QuotaSnapshot reads both quota dimensions for a license.
func (s *Store) QuotaSnapshot(ctx context.Context, licenseID string) (QuotaSnapshot, error) {
	const q = `SELECT dim, total, used_or_committed, reserved FROM quota WHERE license_id=$1`
	rows, err := s.pool.Query(ctx, q, licenseID)
	if err != nil {
		return QuotaSnapshot{}, err
	}
	defer rows.Close()
	var snap QuotaSnapshot
	for rows.Next() {
		var dim string
		var total, usedOrCommitted, reserved int64
		if err := rows.Scan(&dim, &total, &usedOrCommitted, &reserved); err != nil {
			return QuotaSnapshot{}, err
		}
		switch dim {
		case "SERVICE":
			snap.ServiceTotal = total
			snap.ServiceUsed = usedOrCommitted
		case "UPSTREAM":
			snap.UpstreamTotal = total
			snap.UpstreamCommitted = usedOrCommitted
			snap.UpstreamReserved = reserved
		}
	}
	return snap, rows.Err()
}

// QuotaCounters returns the durable quota counters as primitives (consumed by
// the Redis quota repo for seeding + total checks without type coupling).
func (s *Store) QuotaCounters(ctx context.Context, licenseID string) (svcTotal, svcUsed, upTotal, upCommitted, upReserved int64, err error) {
	snap, err := s.QuotaSnapshot(ctx, licenseID)
	if err != nil {
		return 0, 0, 0, 0, 0, err
	}
	return snap.ServiceTotal, snap.ServiceUsed, snap.UpstreamTotal, snap.UpstreamCommitted, snap.UpstreamReserved, nil
}

// AddServiceUsed write-throughs a 维度① delta to the durable quota row.
func (s *Store) AddServiceUsed(ctx context.Context, licenseID string, delta int64) error {
	const q = `UPDATE quota SET used_or_committed = GREATEST(used_or_committed + $2, 0), updated_at=now()
	             WHERE license_id=$1 AND dim='SERVICE'`
	_, err := s.pool.Exec(ctx, q, licenseID, delta)
	return err
}

// AddUpstream write-throughs 维度② committed/reserved deltas to the durable row.
func (s *Store) AddUpstream(ctx context.Context, licenseID string, committedDelta, reservedDelta int64) error {
	const q = `UPDATE quota
		SET used_or_committed = GREATEST(used_or_committed + $2, 0),
		    reserved          = GREATEST(reserved + $3, 0),
		    updated_at        = now()
		WHERE license_id=$1 AND dim='UPSTREAM'`
	_, err := s.pool.Exec(ctx, q, licenseID, committedDelta, reservedDelta)
	return err
}
