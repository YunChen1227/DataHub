//go:build ignore

// Create/recreate + migrate + seed the per-domain databases on the Aliyun RDS
// instance (datahub_x1_db / datahub_v8v9_db / datahub_zlf_db / datahub_blk_db, or
// whatever names the config's versions.*.database.name specify). 存储按「域」隔离：
// v8/v9 共用 v8v9 域库——v8 在 config 中不单列 database，故此处自动跳过，v8v9 域库
// 由 owner 路由 v9 创建。Usage:
//
//	CONFIG_FILE=config.aliyun.e2e.yaml go run ./scripts/recreate_databases.go
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"

	"github.com/datahub/relay/internal/infrastructure/persistence/postgres"
)

type fileVersion struct {
	Database struct {
		Host     string `yaml:"host"`
		Port     int    `yaml:"port"`
		Name     string `yaml:"name"`
		User     string `yaml:"user"`
		Password string `yaml:"password"`
		SSLMode  string `yaml:"sslmode"`
		MaxConns int    `yaml:"maxConns"`
	} `yaml:"database"`
}

type fileConfig struct {
	Storage struct {
		MigrationsDir string `yaml:"migrationsDir"`
	} `yaml:"storage"`
	Versions map[string]fileVersion `yaml:"versions"`
}

// versionOrder keeps a deterministic processing order matching model.Versions.
var versionOrder = []string{"x1", "v9", "v8", "zlf", "blk"}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	path := os.Getenv("CONFIG_FILE")
	if path == "" {
		path = "config.aliyun.e2e.yaml"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		fatal("read config: %v", err)
	}
	var fc fileConfig
	if err := yaml.Unmarshal(raw, &fc); err != nil {
		fatal("parse config: %v", err)
	}
	migDir := fc.Storage.MigrationsDir
	if migDir == "" {
		migDir = "migrations"
	}

	recreateSQL, err := os.ReadFile("scripts/recreate_schema.sql")
	if err != nil {
		fatal("read recreate_schema.sql: %v", err)
	}

	for _, v := range versionOrder {
		fv, ok := fc.Versions[v]
		if !ok || fv.Database.Name == "" {
			fmt.Printf("== %s: no database configured, skipping ==\n", v)
			continue
		}
		dbName := fv.Database.Name
		fmt.Printf("== %s: ensure database %s exists ==\n", v, dbName)
		// Bootstrap connection: use this version's own host/user but connect to the
		// default 'postgres' maintenance DB to issue CREATE DATABASE if needed.
		if err := ensureDatabase(ctx, fv, "postgres", dbName); err != nil {
			// Aliyun RDS sometimes blocks the postgres maintenance DB; fall back to
			// assuming the database already exists.
			fmt.Printf("  (ensureDatabase warning for %s: %v; assuming it exists)\n", dbName, err)
		}
		fmt.Printf("== %s: drop legacy tables on %s ==\n", v, dbName)
		if err := execSQL(ctx, dsn(fv, dbName), string(recreateSQL)); err != nil {
			fatal("%s recreate: %v", v, err)
		}
		if err := migrateAndSeed(ctx, fv, dbName, migDir); err != nil {
			fatal("%s migrate: %v", v, err)
		}
		fmt.Printf("%s (%s) OK\n", v, dbName)
	}
	fmt.Println("\nDone. All configured version databases rebuilt.")
}

func dsn(fv fileVersion, dbName string) string {
	port := fv.Database.Port
	if port == 0 {
		port = 5432
	}
	ssl := fv.Database.SSLMode
	if ssl == "" {
		ssl = "disable"
	}
	maxConns := fv.Database.MaxConns
	if maxConns == 0 {
		maxConns = 10
	}
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s pool_max_conns=%d",
		fv.Database.Host, port, fv.Database.User, fv.Database.Password, dbName, ssl, maxConns,
	)
}

func execSQL(ctx context.Context, connDSN, sqlText string) error {
	pool, err := pgxpool.New(ctx, connDSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	for _, stmt := range splitStatements(sqlText) {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("%w\nstmt: %s", err, stmt)
		}
	}
	return nil
}

func ensureDatabase(ctx context.Context, fv fileVersion, adminDB, newDB string) error {
	pool, err := pgxpool.New(ctx, dsn(fv, adminDB))
	if err != nil {
		return err
	}
	defer pool.Close()
	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, newDB,
	).Scan(&exists); err != nil {
		return err
	}
	if exists {
		fmt.Printf("  database %s already exists\n", newDB)
		return nil
	}
	if _, err := pool.Exec(ctx, "CREATE DATABASE "+quoteIdent(newDB)); err != nil {
		return err
	}
	fmt.Printf("  created database %s\n", newDB)
	return nil
}

func migrateAndSeed(ctx context.Context, fv fileVersion, dbName, migDir string) error {
	store, err := postgres.New(ctx, dsn(fv, dbName))
	if err != nil {
		return err
	}
	defer store.Close()
	if err := postgres.ApplyMigrations(ctx, store.Pool(), migDir); err != nil {
		return err
	}
	return postgres.SeedDemo(ctx, store)
}

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

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
