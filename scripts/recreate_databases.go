//go:build ignore

// Drop legacy tables and re-apply migrations on dev_db + prod_db (Aliyun RDS).
// Usage:
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

type fileConfig struct {
	Storage struct {
		MigrationsDir string `yaml:"migrationsDir"`
	} `yaml:"storage"`
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

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
	devDB := fc.Database.Name
	if devDB == "" {
		devDB = "dev_db"
	}
	prodDB := "prod_db"

	recreateSQL, err := os.ReadFile("scripts/recreate_schema.sql")
	if err != nil {
		fatal("read recreate_schema.sql: %v", err)
	}

	// 1) dev_db: drop old tables
	fmt.Println("== dev_db: drop legacy tables ==")
	if err := execSQL(ctx, dsn(fc, devDB), string(recreateSQL)); err != nil {
		fatal("dev_db recreate: %v", err)
	}
	if err := migrateAndSeed(ctx, fc, devDB, migDir); err != nil {
		fatal("dev_db migrate: %v", err)
	}
	fmt.Println("dev_db OK")

	// 2) prod_db: create if missing, then migrate
	fmt.Println("== prod_db: ensure database exists ==")
	if err := ensureDatabase(ctx, fc, devDB, prodDB); err != nil {
		fatal("create prod_db: %v", err)
	}
	fmt.Println("== prod_db: drop legacy tables (if any) ==")
	if err := execSQL(ctx, dsn(fc, prodDB), string(recreateSQL)); err != nil {
		fatal("prod_db recreate: %v", err)
	}
	if err := migrateAndSeed(ctx, fc, prodDB, migDir); err != nil {
		fatal("prod_db migrate: %v", err)
	}
	fmt.Println("prod_db OK")
	fmt.Println("\nDone. dev_db + prod_db rebuilt with v0.7 schema.")
}

func dsn(fc fileConfig, dbName string) string {
	port := fc.Database.Port
	if port == 0 {
		port = 5432
	}
	ssl := fc.Database.SSLMode
	if ssl == "" {
		ssl = "disable"
	}
	maxConns := fc.Database.MaxConns
	if maxConns == 0 {
		maxConns = 10
	}
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s pool_max_conns=%d",
		fc.Database.Host, port, fc.Database.User, fc.Database.Password, dbName, ssl, maxConns,
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

func ensureDatabase(ctx context.Context, fc fileConfig, adminDB, newDB string) error {
	pool, err := pgxpool.New(ctx, dsn(fc, adminDB))
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
		fmt.Printf("database %s already exists\n", newDB)
		return nil
	}
	// CREATE DATABASE cannot run inside a transaction block; use a single Exec.
	if _, err := pool.Exec(ctx, "CREATE DATABASE "+quoteIdent(newDB)); err != nil {
		return err
	}
	fmt.Printf("created database %s\n", newDB)
	return nil
}

func migrateAndSeed(ctx context.Context, fc fileConfig, dbName, migDir string) error {
	store, err := postgres.New(ctx, dsn(fc, dbName))
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
