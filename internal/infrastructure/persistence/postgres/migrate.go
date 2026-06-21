package postgres

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ApplyMigrations runs every *.sql under dir exactly once, tracked in a
// schema_migrations table. Each file is split into individual statements on ';'
// (comment lines starting with '--' are stripped first) because pgx's extended
// protocol executes a single statement per Exec.
func ApplyMigrations(ctx context.Context, pool *pgxpool.Pool, dir string) error {
	// Best-effort: managed PG (e.g. Aliyun accounts in pg_rds_superuser) often
	// ships PG15+ where the `public` schema lacks CREATE for the app account.
	// Self-grant when permitted; ignored elsewhere (real DDL errors still surface).
	_, _ = pool.Exec(ctx, `GRANT CREATE ON SCHEMA public TO CURRENT_USER`)

	if _, err := pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
	); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read migrations dir %s: %w", dir, err)
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, name := range files {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, name,
		).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if exists {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		for _, stmt := range splitStatements(string(raw)) {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				_ = tx.Rollback(ctx)
				return fmt.Errorf("apply %s: %w", name, err)
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit %s: %w", name, err)
		}
	}
	return nil
}

// splitStatements strips '--' comment lines and splits SQL on ';'.
func splitStatements(sqlText string) []string {
	var b strings.Builder
	for _, line := range strings.Split(sqlText, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	var out []string
	for _, part := range strings.Split(b.String(), ";") {
		if s := strings.TrimSpace(part); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// SeedDemo inserts the dev demo license (appKey y89098io) idempotently so the
// e2e/admin flows have a known client. Mirrors memory.seedDemo.
func SeedDemo(ctx context.Context, s *Store) error {
	const insLicense = `INSERT INTO license
		(license_id, app_key, app_secret_enc, client_uuid, name, mobile, status, valid_from, valid_to, secret_created_at)
		VALUES ('LIC-DEMO-0001','y89098io','demo-app-secret','demo-client-uuid','Demo 商户','13800001234','ACTIVE', now(), now() + interval '3650 days', now())
		ON CONFLICT (license_id) DO NOTHING`
	if _, err := s.pool.Exec(ctx, insLicense); err != nil {
		return err
	}
	const insQuota = `INSERT INTO quota (license_id, dim) VALUES ('LIC-DEMO-0001','SERVICE')
		ON CONFLICT (license_id, dim) DO NOTHING`
	if _, err := s.pool.Exec(ctx, insQuota); err != nil {
		return err
	}
	return nil
}
