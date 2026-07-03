//go:build ignore

// Create missing PostgreSQL databases listed in config (no DROP / no migrate).
// 阿里云 RDS 常无法连接 postgres 维护库；本脚本改连 config 里已有的库
// (默认 x1 的 database.name) 执行 CREATE DATABASE。
//
// Usage:
//
//	CONFIG_FILE=config.aliyun.prod.yaml go run ./scripts/create_databases.go
//	CONFIG_FILE=config.aliyun.prod.yaml go run ./scripts/create_databases.go zlf blk
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"
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
	Versions map[string]fileVersion `yaml:"versions"`
}

var versionOrder = []string{"x1", "v9", "v8", "zlf", "blk"}

const perDBTimeout = 2 * time.Minute

func main() {
	path := os.Getenv("CONFIG_FILE")
	if path == "" {
		path = "config.aliyun.prod.yaml"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		fatal("read config: %v", err)
	}
	var fc fileConfig
	if err := yaml.Unmarshal(raw, &fc); err != nil {
		fatal("parse config: %v", err)
	}

	only := map[string]struct{}{}
	if len(os.Args) > 1 {
		for _, a := range os.Args[1:] {
			only[a] = struct{}{}
		}
	}

	seen := map[string]struct{}{}
	for _, v := range versionOrder {
		if len(only) > 0 {
			if _, ok := only[v]; !ok {
				continue
			}
		}
		fvv, ok := fc.Versions[v]
		if !ok || fvv.Database.Name == "" {
			fmt.Printf("== %s: no database.name, skip ==\n", v)
			continue
		}
		dbName := fvv.Database.Name
		if _, dup := seen[dbName]; dup {
			fmt.Printf("== %s: %s already scheduled, skip ==\n", v, dbName)
			continue
		}
		seen[dbName] = struct{}{}

		ctx, cancel := context.WithTimeout(context.Background(), perDBTimeout)
		bootstrap := bootstrapDBName(fc, dbName)
		fmt.Printf("== %s: ensure database %s (via bootstrap %s) ==\n", v, dbName, bootstrap)
		if err := ensureDatabase(ctx, fvv, bootstrap, dbName); err != nil {
			cancel()
			fatal("%s: %v", dbName, err)
		}
		cancel()
	}
	fmt.Println("\nDone.")
}

// bootstrapDBName picks an existing configured database to connect for CREATE DATABASE
// (Aliyun RDS often blocks the postgres maintenance DB). Skips skip (the target).
func bootstrapDBName(fc fileConfig, skip string) string {
	for _, v := range versionOrder {
		fvv, ok := fc.Versions[v]
		if !ok || fvv.Database.Name == "" || fvv.Database.Name == skip {
			continue
		}
		return fvv.Database.Name
	}
	return "postgres"
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
		maxConns = 2
	}
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s connect_timeout=15 pool_max_conns=%d",
		fv.Database.Host, port, fv.Database.User, fv.Database.Password, dbName, ssl, maxConns,
	)
}

func ensureDatabase(ctx context.Context, fv fileVersion, bootstrapDB, newDB string) error {
	// Fast path: target DB already exists and accepts connections.
	if ok, err := dbReachable(ctx, fv, newDB); err != nil {
		return err
	} else if ok {
		fmt.Printf("  database %s already exists\n", newDB)
		return nil
	}

	pool, err := pgxpool.New(ctx, dsn(fv, bootstrapDB))
	if err != nil {
		return fmt.Errorf("connect bootstrap db %s: %w", bootstrapDB, err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping bootstrap db %s: %w", bootstrapDB, err)
	}

	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, newDB,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check exists: %w", err)
	}
	if exists {
		fmt.Printf("  database %s already exists (catalog)\n", newDB)
		return nil
	}

	if _, err := pool.Exec(ctx, "CREATE DATABASE "+quoteIdent(newDB)); err != nil {
		return fmt.Errorf("CREATE DATABASE: %w (need CREATEDB / RDS 高权限账号，或在控制台手动建库)", err)
	}
	fmt.Printf("  created database %s\n", newDB)
	return nil
}

func dbReachable(ctx context.Context, fv fileVersion, dbName string) (bool, error) {
	pool, err := pgxpool.New(ctx, dsn(fv, dbName))
	if err != nil {
		return false, fmt.Errorf("connect %s: %w", dbName, err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		// Unknown database → not created yet; other errors propagate.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "3D000" {
			return false, nil
		}
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "does not exist") {
			return false, nil
		}
		return false, fmt.Errorf("ping %s: %w", dbName, err)
	}
	return true, nil
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
