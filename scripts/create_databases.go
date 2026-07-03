//go:build ignore

// Create missing PostgreSQL databases listed in config (no DROP / no migrate).
// Tries connecting to postgres maintenance DB and CREATE DATABASE IF NOT EXISTS
// semantics (check pg_database first).
//
// Usage:
//
//	CONFIG_FILE=config.aliyun.prod.yaml go run ./scripts/create_databases.go
//	CONFIG_FILE=config.aliyun.prod.yaml go run ./scripts/create_databases.go zlf blk
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

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

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

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
		fmt.Printf("== %s: ensure database %s ==\n", v, dbName)
		if err := ensureDatabase(ctx, fvv, dbName); err != nil {
			fatal("%s: %v", dbName, err)
		}
	}
	fmt.Println("\nDone.")
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

func ensureDatabase(ctx context.Context, fv fileVersion, newDB string) error {
	pool, err := pgxpool.New(ctx, dsn(fv, "postgres"))
	if err != nil {
		return fmt.Errorf("connect postgres maintenance db: %w", err)
	}
	defer pool.Close()

	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, newDB,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check exists: %w", err)
	}
	if exists {
		fmt.Printf("  database %s already exists\n", newDB)
		return nil
	}
	if _, err := pool.Exec(ctx, "CREATE DATABASE "+quoteIdent(newDB)); err != nil {
		return fmt.Errorf("CREATE DATABASE: %w (need RDS high-privilege account or create in Aliyun console)", err)
	}
	fmt.Printf("  created database %s\n", newDB)
	return nil
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
