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
	"github.com/datahub/relay/internal/infrastructure/oss"
	"github.com/datahub/relay/internal/infrastructure/persistence/memory"
	"github.com/datahub/relay/internal/infrastructure/persistence/postgres"
	redisq "github.com/datahub/relay/internal/infrastructure/persistence/redis"
	"github.com/datahub/relay/internal/infrastructure/secret"
	"github.com/datahub/relay/internal/infrastructure/upstream"
	"github.com/datahub/relay/internal/job"
)

// domainStorage is one license 域的存储后端 (独立 DB+Redis；v8/v9 共用 v8v9 域)。
// 同一域内的多条路由 (如 v8/v9) 复用这一套 repos，共享 license 表，但统计/台账/
// 审计按各自 route 独立 (见 model.RouteDomain)。
type domainStorage struct {
	licenseRepo port.LicenseRepository
	ledgerRepo  port.LedgerRepository
	quotaRepo   port.QuotaRepository
	auditRepo   port.AuditRepository
	adminRepo   port.AdminUserRepository
	userRepo    port.UserAdminRepository
	secrets     port.SecretProvider
	cleanup     func()
}

// routeStack is one fully-wired route (独立 orchestrator + 后台服务 + 复查 worker)，
// 接到其所属域的存储 + 自己的上游客户端。
type routeStack struct {
	orch    *application.QueryOrchestrator
	admin   *admin.Service
	requery *job.RequeryWorker
}

// domainOwner returns the route whose db/redis config seeds a domain's storage
// (域内第一个出现的路由)。v8v9 域 → v9 (model.Versions 中 v9 先于 v8)。
func domainOwner(domain string) string {
	for _, r := range model.Versions {
		if model.RouteDomain(r) == domain {
			return r
		}
	}
	return domain
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

	// --- build one storage backend per license 域 (v8/v9 共用 v8v9 域库) ---
	domainStores := make(map[string]*domainStorage, len(model.Domains))
	cleanups := make([]func(), 0, len(model.Domains))
	for _, domain := range model.Domains {
		ds, err := buildDomainStorage(ctx, cfg, domain, logger)
		if err != nil {
			logger.Error("build domain storage failed", "domain", domain, "err", err)
			os.Exit(1)
		}
		domainStores[domain] = ds
		if ds.cleanup != nil {
			cleanups = append(cleanups, ds.cleanup)
		}
		logger.Info("domain storage ready", "domain", domain, "driver", cfg.storageDriver,
			"owner", domainOwner(domain))
	}
	defer func() {
		for _, c := range cleanups {
			c()
		}
	}()

	// --- build one orchestrator + admin per route, 接到其所属域的存储 + 自己的上游 ---
	apiStacks := make(map[string]*api.VersionStack, len(model.Versions))
	adminByRoute := make(map[string]*admin.Service, len(model.Versions))
	for _, route := range model.Versions {
		ds := domainStores[model.RouteDomain(route)]
		st, err := buildRouteStack(cfg, route, ds, httpClient, logger)
		if err != nil {
			logger.Error("build route stack failed", "route", route, "err", err)
			os.Exit(1)
		}
		apiStacks[route] = &api.VersionStack{Orch: st.orch, Admin: st.admin}
		adminByRoute[route] = st.admin
		go st.requery.Run(ctx)
		logger.Info("route stack ready", "route", route, "domain", model.RouteDomain(route),
			"upstream", cfg.versions[route].upstream.kind)
	}

	// 控制面：后台统一登录 + JWT 校验走 x1 路由的 admin 服务 (x1 域)。
	control := adminByRoute["x1"]
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

// buildDomainStorage opens the storage backend for one license 域 (DB+Redis or
// memory)，使用该域 owner 路由的 db/redis 配置。同一域只建一次，供域内各路由复用。
func buildDomainStorage(ctx context.Context, cfg config, domain string, logger *slog.Logger) (*domainStorage, error) {
	owner := domainOwner(domain)
	vc := cfg.versions[owner]

	switch cfg.storageDriver {
	case "postgres":
		if vc.db.name == "" {
			return nil, fmt.Errorf("domain %s (owner %s): database.name 未配置", domain, owner)
		}
		pg, err := postgres.New(ctx, vc.db.dsn())
		if err != nil {
			return nil, fmt.Errorf("postgres connect: %w", err)
		}
		if err := postgres.ApplyMigrations(ctx, pg.Pool(), cfg.migrationsDir); err != nil {
			pg.Close()
			return nil, fmt.Errorf("apply migrations: %w", err)
		}
		if cfg.demoSeed {
			if err := postgres.SeedDemo(ctx, pg); err != nil {
				pg.Close()
				return nil, fmt.Errorf("seed demo: %w", err)
			}
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
		return &domainStorage{
			licenseRepo: pg, ledgerRepo: pg, quotaRepo: rq, auditRepo: pg,
			adminRepo: pg, userRepo: pg, secrets: secret.NewStore(pg),
			cleanup: func() { rq.Close(); pg.Close() },
		}, nil
	default:
		store := memory.New()
		seedDemo(store, cfg.demoAppSecret)
		return &domainStorage{
			licenseRepo: store, ledgerRepo: store, quotaRepo: store, auditRepo: store,
			adminRepo: store, userRepo: store, secrets: secret.NewStore(store),
			cleanup: func() {},
		}, nil
	}
}

// buildRouteStack wires the per-route dependencies (auth/quota/billing/orchestrator/
// admin/requery) on top of the route's 域存储 + 自己的上游客户端。
func buildRouteStack(cfg config, route string, ds *domainStorage, httpClient *http.Client, logger *slog.Logger) (*routeStack, error) {
	vc := cfg.versions[route]
	log := logger.With("route", route)

	upRouter, err := buildUpstream(route, vc.upstream, httpClient, log)
	if err != nil {
		return nil, err
	}

	verifier := auth.Md5Verifier{}
	authSvc := auth.New(ds.licenseRepo, ds.secrets, verifier)
	quotaSvc := quota.New(ds.quotaRepo, ds.ledgerRepo)
	billSvc := billing.New(billing.DefaultTable())
	adminSvc := admin.New(route, ds.adminRepo, ds.userRepo, ds.auditRepo, admin.Config{
		JWTSecret: cfg.adminJWTSecret,
		TokenTTL:  cfg.adminTokenTTL,
	})
	orch := application.NewQueryOrchestrator(route, authSvc, quotaSvc, billSvc, upRouter, ds.auditRepo, log)
	requery := job.NewRequeryWorker(ds.ledgerRepo, ds.licenseRepo, upRouter, billSvc, quotaSvc, cfg.requeryInterval, log)

	return &routeStack{orch: orch, admin: adminSvc, requery: requery}, nil
}

// buildUpstream constructs the version's upstream client behind a 1-provider Router.
func buildUpstream(version string, uc upstreamConfig, httpClient *http.Client, logger *slog.Logger) (*upstream.Router, error) {
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
	case upstream.ProviderRental:
		// 启动时把固定授权书上传到 OSS, 缓存 licenseUrl 供所有查询复用。OSS/授权书
		// 未配置时 (dev/memory) 留空, 由上游在调用时报错, 不阻塞服务启动。
		licenseURL := ""
		if uc.licenseFile != "" {
			url, err := oss.UploadFile(oss.Config{
				Endpoint:        uc.oss.endpoint,
				AccessKeyID:     uc.oss.accessKeyID,
				AccessKeySecret: uc.oss.accessKeySecret,
				Bucket:          uc.oss.bucket,
				ObjectPrefix:    uc.oss.objectPrefix,
			}, uc.licenseFile)
			if err != nil {
				logger.Warn("rental 授权书上传 OSS 失败, licenseUrl 留空", "err", err)
			} else {
				licenseURL = url
				logger.Info("rental 授权书已上传 OSS", "licenseUrl", licenseURL)
			}
		} else {
			logger.Warn("rental 未配置授权书文件 (licenseFile), licenseUrl 留空")
		}
		client := upstream.NewRental(upstream.RentalConfig{
			BaseURL:       uc.baseURL,
			InstitutionID: uc.institutionID,
			AESKey:        uc.aesKey,
			Service:       uc.service,
			Mode:          uc.mode,
			LicenseURL:    licenseURL,
			LicenseType:   uc.licenseType,
		}, httpClient)
		return upstream.NewRouter(upstream.ProviderRental, map[string]port.UpstreamPort{
			upstream.ProviderRental: client,
		})
	case upstream.ProviderBlacklist:
		client := upstream.NewBlacklist(upstream.BlacklistConfig{
			BaseURL:        uc.baseURL,
			AppID:          uc.appID,
			Secret:         uc.appSecret,
			APIKey:         uc.apiKey,
			EncryptionType: uc.encryptionType,
		}, httpClient)
		return upstream.NewRouter(upstream.ProviderBlacklist, map[string]port.UpstreamPort{
			upstream.ProviderBlacklist: client,
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
