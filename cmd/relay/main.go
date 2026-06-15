// Command relay is the entrypoint for the经济能力查询转接服务. It wires the
// hexagonal layers together and starts the HTTP server + background workers.
//
// Dev defaults use in-memory adapters; production should swap in Redis+Lua and a
// relational DB (DESIGN §7.5 / §11) and load secrets from KMS/Vault.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/datahub/relay/internal/api"
	"github.com/datahub/relay/internal/application"
	"github.com/datahub/relay/internal/domain/admin"
	"github.com/datahub/relay/internal/domain/auth"
	"github.com/datahub/relay/internal/domain/billing"
	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/domain/port"
	"github.com/datahub/relay/internal/domain/quota"
	"github.com/datahub/relay/internal/infrastructure/persistence/memory"
	"github.com/datahub/relay/internal/infrastructure/persistence/postgres"
	redisq "github.com/datahub/relay/internal/infrastructure/persistence/redis"
	"github.com/datahub/relay/internal/infrastructure/secret"
	"github.com/datahub/relay/internal/infrastructure/upstream"
	"github.com/datahub/relay/internal/job"
)

func main() {
	level := slog.LevelInfo
	if lv := os.Getenv("LOG_LEVEL"); strings.EqualFold(lv, "debug") {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("load config failed", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// --- persistence backend selection (DESIGN §11) ---
	// memory  : in-memory adapters (dev/default, fast e2e).
	// postgres: durable PG (license/ledger/audit/admin/IP) + Redis+Lua quota.
	var (
		licenseRepo port.LicenseRepository
		ledgerRepo  port.LedgerRepository
		quotaRepo   port.QuotaRepository
		auditRepo   port.AuditRepository
		adminRepo   port.AdminUserRepository
		userRepo    port.UserAdminRepository
		ipRepo      port.GlobalIPRepository
		secrets     port.SecretProvider
		cleanup     = func() {}
	)

	switch cfg.storageDriver {
	case "postgres":
		pg, err := postgres.New(ctx, cfg.pgDSN())
		if err != nil {
			logger.Error("postgres connect failed", "err", err)
			os.Exit(1)
		}
		if err := postgres.ApplyMigrations(ctx, pg.Pool(), cfg.migrationsDir); err != nil {
			logger.Error("apply migrations failed", "err", err)
			os.Exit(1)
		}
		if err := postgres.SeedDemo(ctx, pg); err != nil {
			logger.Error("seed demo failed", "err", err)
			os.Exit(1)
		}
		rq, err := redisq.New(ctx, redisq.Options{
			Addr:     cfg.redisAddr,
			Username: cfg.redisUsername,
			Password: cfg.redisPassword,
			DB:       cfg.redisDB,
			PoolSize: cfg.redisPoolSize,
		}, pg)
		if err != nil {
			logger.Error("redis connect failed", "err", err)
			os.Exit(1)
		}
		licenseRepo, ledgerRepo, auditRepo = pg, pg, pg
		adminRepo, userRepo, ipRepo = pg, pg, pg
		quotaRepo = rq
		secrets = secret.NewStore(pg)
		cleanup = func() { rq.Close(); pg.Close() }
		logger.Info("storage backend ready", "driver", "postgres", "redis", cfg.redisAddr)
	default:
		store := memory.New()
		seedDemo(store)
		licenseRepo, ledgerRepo, auditRepo = store, store, store
		adminRepo, userRepo, ipRepo = store, store, store
		quotaRepo = store
		secrets = secret.NewStore(store)
		logger.Info("storage backend ready", "driver", "memory")
	}
	defer cleanup()

	httpClient := &http.Client{Timeout: cfg.upstreamTimeout}
	gamaClient := upstream.NewGama(upstream.GamaConfig{
		BaseURL: cfg.gamaBaseURL,
		AppID:   cfg.gamaAppID,
		Secret:  cfg.gamaSecret,
		APIKey:  cfg.gamaAPIKey,
	}, httpClient)
	incomeClient := upstream.NewIncomeCls(upstream.IncomeClsConfig{
		BaseURL: cfg.incomeBaseURL,
		Account: cfg.incomeAccount,
		Key:     cfg.incomeKey,
	}, httpClient)
	upRouter, err := upstream.NewRouter(cfg.upstreamProvider, map[string]port.UpstreamPort{
		upstream.ProviderGama:      gamaClient,
		upstream.ProviderIncomeCls: incomeClient,
	})
	if err != nil {
		logger.Error("upstream router init failed", "err", err)
		os.Exit(1)
	}
	logger.Info("upstream provider selected", "active", upRouter.Active())

	// --- domain services ---
	verifier := auth.Md5Verifier{}
	authSvc := auth.New(licenseRepo, secrets, verifier)
	quotaSvc := quota.New(quotaRepo, ledgerRepo)
	billSvc := billing.New(billing.DefaultTable())
	adminSvc := admin.New(adminRepo, userRepo, auditRepo, ipRepo, admin.Config{
		JWTSecret: cfg.adminJWTSecret,
		TokenTTL:  cfg.adminTokenTTL,
	})

	orch := application.NewQueryOrchestrator(authSvc, quotaSvc, billSvc, upRouter, auditRepo, logger)

	// --- background workers (DESIGN §7.6) ---
	// bootstrap the initial admin operator (DESIGN §16.1).
	if err := adminSvc.BootstrapAdmin(ctx, cfg.adminUser, cfg.adminPass); err != nil {
		logger.Error("bootstrap admin failed", "err", err)
	} else {
		logger.Info("admin console ready", "loginUser", cfg.adminUser, "spaDir", cfg.spaDir)
	}

	// --- HTTP server ---
	server := api.NewServer(orch, adminSvc, ipRepo, cfg.spaDir)
	httpServer := &http.Server{
		Addr:              cfg.addr,
		Handler:           server.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	requery := job.NewRequeryWorker(ledgerRepo, licenseRepo, upRouter, billSvc, quotaSvc, cfg.requeryInterval, logger)
	recon := job.NewReconciliationJob(ledgerRepo, upRouter, cfg.reconInterval, logger)
	go requery.Run(ctx)
	go recon.Run(ctx)

	go func() {
		logger.Info("relay listening", "addr", cfg.addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("http server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
}

func seedDemo(store *memory.Store) {
	store.SeedLicense(&model.LicenseView{
		LicenseID:  "LIC-DEMO-0001",
		AppKey:     "y89098io",
		ClientUUID: "demo-client-uuid",
		Status:     "ACTIVE",
	}, "demo-app-secret", "Demo 商户", 100000, 100000)
}
