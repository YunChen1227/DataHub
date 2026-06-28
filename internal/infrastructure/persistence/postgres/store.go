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
	const q = `SELECT license_id, app_key, client_uuid, status FROM license WHERE app_key=$1`
	var v model.LicenseView
	err := s.pool.QueryRow(ctx, q, appKey).Scan(&v.LicenseID, &v.AppKey, &v.ClientUUID, &v.Status)
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

const ledgerCols = `id, app_key, COALESCE(version,''), COALESCE(trade_no,''), reqid, request_id,
	COALESCE(upstream_code,''), COALESCE(busi_code,0), COALESCE(upstream_uid,''),
	COALESCE(upstream_logid,''), state, counted_service`

func scanLedger(row pgx.Row) (*model.Ledger, error) {
	var l model.Ledger
	var state string
	err := row.Scan(&l.ID, &l.AppKey, &l.Version, &l.TradeNo, &l.Reqid, &l.RequestID,
		&l.UpstreamCode, &l.BusiCode, &l.UpstreamUID, &l.UpstreamLogID,
		&state, &l.CountedService)
	if err != nil {
		return nil, err
	}
	l.State = model.BillingState(state)
	return &l, nil
}

func (s *Store) FindByReqid(ctx context.Context, appKey, route, reqid string) (*model.Ledger, error) {
	q := `SELECT ` + ledgerCols + ` FROM billing_ledger WHERE app_key=$1 AND version=$2 AND reqid=$3`
	l, err := scanLedger(s.pool.QueryRow(ctx, q, appKey, route, reqid))
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
		(app_key, version, trade_no, reqid, request_id, upstream_code, busi_code,
		 upstream_uid, upstream_logid, state, counted_service)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`
	return s.pool.QueryRow(ctx, q,
		l.AppKey, l.Version, l.TradeNo, l.Reqid, l.RequestID, l.UpstreamCode, l.BusiCode,
		l.UpstreamUID, l.UpstreamLogID, string(l.State), l.CountedService,
	).Scan(&l.ID)
}

func (s *Store) UpdateState(ctx context.Context, id int64, state model.BillingState, countedService bool) error {
	const q = `UPDATE billing_ledger
		SET state=$2, counted_service=$3, settled_at=now()
		WHERE id=$1`
	_, err := s.pool.Exec(ctx, q, id, string(state), countedService)
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

// --- per-route 计数 durable mirror (read by Redis quota repo + admin views) ---
// 计数按 (license_id, route, dim) 独立；dim='SERVICE'(成功查得数) / 'CALL'(调用次数)。
// 累加用 UPSERT，行按需创建 (无需建用户时预插)。

func (s *Store) countOf(ctx context.Context, licenseID, route, dim string) (int64, error) {
	const q = `SELECT COALESCE(used_or_committed,0) FROM quota WHERE license_id=$1 AND route=$2 AND dim=$3`
	var n int64
	err := s.pool.QueryRow(ctx, q, licenseID, route, dim).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

func (s *Store) addCount(ctx context.Context, licenseID, route, dim string, delta int64) error {
	const q = `INSERT INTO quota (license_id, route, dim, used_or_committed, updated_at)
		VALUES ($1,$2,$3,GREATEST($4,0),now())
		ON CONFLICT (license_id, route, dim)
		DO UPDATE SET used_or_committed = GREATEST(quota.used_or_committed + $4, 0), updated_at=now()`
	_, err := s.pool.Exec(ctx, q, licenseID, route, dim, delta)
	return err
}

// ServiceUsedCount reads the cumulative 成功查得数 for (license, route).
func (s *Store) ServiceUsedCount(ctx context.Context, licenseID, route string) (int64, error) {
	return s.countOf(ctx, licenseID, route, "SERVICE")
}

// AddServiceUsed write-throughs a 成功查得数 delta for (license, route).
func (s *Store) AddServiceUsed(ctx context.Context, licenseID, route string, delta int64) error {
	return s.addCount(ctx, licenseID, route, "SERVICE", delta)
}

// TotalCallsCount reads the cumulative 调用次数 for (license, route).
func (s *Store) TotalCallsCount(ctx context.Context, licenseID, route string) (int64, error) {
	return s.countOf(ctx, licenseID, route, "CALL")
}

// AddTotalCalls write-throughs a 调用次数 delta for (license, route).
func (s *Store) AddTotalCalls(ctx context.Context, licenseID, route string, delta int64) error {
	return s.addCount(ctx, licenseID, route, "CALL", delta)
}
