// Command relay is the entrypoint for the经济能力查询转接服务. It wires the
// hexagonal layers together and starts the HTTP server + background workers.
//
// 三版本 (x1/v9/v8) 对外接口完全一致，仅靠路由名区分；每个版本各自独立装配
// 一套依赖 (独立上游 + 独立数据库 + 独立 Redis + 独立 license/台账/审计/后台数据)。
// Dev defaults use in-memory adapters; production swaps in Redis+Lua + 独立 PG。
package main

import (
	"context"
	"fmt"
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

// versionStack is one fully-wired version (独立 orchestrator + 后台服务 + 复查 worker)。
type versionStack struct {
	orch    *application.QueryOrchestrator
	admin   *admin.Service
	requery *job.RequeryWorker
	cleanup func()
}

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

	httpClient := &http.Client{Timeout: cfg.upstreamTimeout}

	// --- build one independent stack per version (x1/v9/v8) ---
	apiStacks := make(map[string]*api.VersionStack, len(model.Versions))
	built := make(map[string]*versionStack, len(model.Versions))
	cleanups := make([]func(), 0, len(model.Versions))
	for _, v := range model.Versions {
		st, err := buildStack(ctx, cfg, v, httpClient, logger)
		if err != nil {
			logger.Error("build version stack failed", "version", v, "err", err)
			os.Exit(1)
		}
		built[v] = st
		apiStacks[v] = &api.VersionStack{Orch: st.orch, Admin: st.admin}
		if st.cleanup != nil {
			cleanups = append(cleanups, st.cleanup)
		}
		go st.requery.Run(ctx)
		logger.Info("version stack ready", "version", v, "driver", cfg.storageDriver,
			"upstream", cfg.versions[v].upstream.kind)
	}
	defer func() {
		for _, c := range cleanups {
			c()
		}
	}()

	// 控制面：后台统一登录 + JWT 校验走 x1 版本的 admin 服务。
	control := built["x1"].admin
	if control == nil {
		logger.Error("x1 stack not built; cannot start admin control plane")
		os.Exit(1)
	}
	if err := control.BootstrapAdmin(ctx, cfg.adminUser, cfg.adminPass); err != nil {
		logger.Error("bootstrap admin failed", "err", err)
	} else {
		logger.Info("admin console ready", "loginUser", cfg.adminUser, "spaDir", cfg.spaDir)
	}

	// --- HTTP server ---
	server := api.NewServer(apiStacks, control, cfg.spaDir)
	httpServer := &http.Server{
		Addr:              cfg.addr,
		Handler:           server.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

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

// buildStack wires every dependency for a single version (独立存储后端 + 独立上游)。
func buildStack(ctx context.Context, cfg config, version string, httpClient *http.Client, logger *slog.Logger) (*versionStack, error) {
	vc := cfg.versions[version]

	var (
		licenseRepo port.LicenseRepository
		ledgerRepo  port.LedgerRepository
		quotaRepo   port.QuotaRepository
		auditRepo   port.AuditRepository
		adminRepo   port.AdminUserRepository
		userRepo    port.UserAdminRepository
		secrets     port.SecretProvider
		cleanup     = func() {}
	)

	switch cfg.storageDriver {
	case "postgres":
		if vc.db.name == "" {
			return nil, fmt.Errorf("version %s: database.name 未配置", version)
		}
		pg, err := postgres.New(ctx, vc.db.dsn())
		if err != nil {
			return nil, fmt.Errorf("postgres connect: %w", err)
		}
		if err := postgres.ApplyMigrations(ctx, pg.Pool(), cfg.migrationsDir); err != nil {
			pg.Close()
			return nil, fmt.Errorf("apply migrations: %w", err)
		}
		if err := postgres.SeedDemo(ctx, pg); err != nil {
			pg.Close()
			return nil, fmt.Errorf("seed demo: %w", err)
		}
		rq, err := redisq.New(ctx, redisq.Options{
			Addr:     vc.redis.addr,
			Username: vc.redis.username,
			Password: vc.redis.password,
			DB:       vc.redis.db,
			PoolSize: vc.redis.poolSize,
		}, pg)
		if err != nil {
			pg.Close()
			return nil, fmt.Errorf("redis connect: %w", err)
		}
		licenseRepo, ledgerRepo, auditRepo = pg, pg, pg
		adminRepo, userRepo = pg, pg
		quotaRepo = rq
		secrets = secret.NewStore(pg)
		cleanup = func() { rq.Close(); pg.Close() }
	default:
		store := memory.New()
		seedDemo(store, cfg.demoAppSecret)
		licenseRepo, ledgerRepo, auditRepo = store, store, store
		adminRepo, userRepo = store, store
		quotaRepo = store
		secrets = secret.NewStore(store)
	}

	upRouter, err := buildUpstream(version, vc.upstream, httpClient)
	if err != nil {
		cleanup()
		return nil, err
	}

	verifier := auth.Md5Verifier{}
	authSvc := auth.New(licenseRepo, secrets, verifier)
	quotaSvc := quota.New(quotaRepo, ledgerRepo)
	billSvc := billing.New(billing.DefaultTable())
	adminSvc := admin.New(adminRepo, userRepo, auditRepo, admin.Config{
		JWTSecret: cfg.adminJWTSecret,
		TokenTTL:  cfg.adminTokenTTL,
	})
	orch := application.NewQueryOrchestrator(authSvc, quotaSvc, billSvc, upRouter, auditRepo, logger.With("version", version))
	requery := job.NewRequeryWorker(ledgerRepo, licenseRepo, upRouter, billSvc, quotaSvc, cfg.requeryInterval, logger.With("version", version))

	return &versionStack{orch: orch, admin: adminSvc, requery: requery, cleanup: cleanup}, nil
}

// buildUpstream constructs the version's upstream client behind a 1-provider Router.
func buildUpstream(version string, uc upstreamConfig, httpClient *http.Client) (*upstream.Router, error) {
	switch uc.kind {
	case upstream.ProviderIncome:
		client := upstream.NewIncome(upstream.IncomeConfig{
			BaseURL: uc.baseURL,
			Account: uc.account,
			Key:     uc.key,
			Version: version,
		}, httpClient)
		return upstream.NewRouter(upstream.ProviderIncome, map[string]port.UpstreamPort{
			upstream.ProviderIncome: client,
		})
	case upstream.ProviderGama, "":
		client := upstream.NewGama(upstream.GamaConfig{
			BaseURL: uc.baseURL,
			AppID:   uc.appID,
			Secret:  uc.appSecret,
			APIKey:  uc.apiKey,
		}, httpClient)
		return upstream.NewRouter(upstream.ProviderGama, map[string]port.UpstreamPort{
			upstream.ProviderGama: client,
		})
	default:
		return nil, fmt.Errorf("version %s: unknown upstream kind %q", version, uc.kind)
	}
}

// seedDemo registers the dev demo license (appKey y89098io) in a memory store so
// the e2e/admin flows have a known client per version.
func seedDemo(store *memory.Store, demoSecret string) {
	store.SeedLicense(&model.LicenseView{
		LicenseID:  "LIC-DEMO-0001",
		AppKey:     "y89098io",
		ClientUUID: "demo-client-uuid",
		Status:     "ACTIVE",
	}, demoSecret, "Demo 商户", "13800001234")
}
