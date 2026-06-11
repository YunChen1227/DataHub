package main

import (
	"os"
	"strconv"
	"time"
)

// config holds runtime knobs sourced from env with sane dev defaults.
type config struct {
	addr            string
	upstreamBaseURL string
	upstreamAccount string
	upstreamKey     string
	demoAppSecret   string
	upstreamTimeout time.Duration
	requeryInterval time.Duration
	reconInterval   time.Duration

	// admin console (DESIGN §16)
	adminUser      string
	adminPass      string
	adminJWTSecret string
	adminTokenTTL  time.Duration
	spaDir         string
}

func loadConfig() config {
	return config{
		addr:            env("RELAY_ADDR", ":8080"),
		upstreamBaseURL: env("UPSTREAM_BASE_URL", "http://127.0.0.1:9000/yrzx/finan/net/10w/v9"),
		upstreamAccount: env("UPSTREAM_ACCOUNT", "demo-account"),
		upstreamKey:     env("UPSTREAM_KEY", "demo-key"),
		demoAppSecret:   env("DEMO_APP_SECRET", "demo-app-secret"),
		upstreamTimeout: envDuration("UPSTREAM_TIMEOUT", 3*time.Second),
		requeryInterval: envDuration("REQUERY_INTERVAL", 10*time.Second),
		reconInterval:   envDuration("RECON_INTERVAL", 5*time.Minute),

		adminUser:      env("ADMIN_BOOTSTRAP_USER", "admin"),
		adminPass:      env("ADMIN_BOOTSTRAP_PASS", "admin12345"),
		adminJWTSecret: env("ADMIN_JWT_SECRET", "dev-admin-jwt-secret-change-me"),
		adminTokenTTL:  envDuration("ADMIN_TOKEN_TTL", 8*time.Hour),
		spaDir:         env("ADMIN_SPA_DIR", "web/admin/dist"),
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	if ms, err := strconv.Atoi(v); err == nil {
		return time.Duration(ms) * time.Millisecond
	}
	return def
}
