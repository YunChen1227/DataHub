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
	"syscall"
	"time"

	"github.com/datahub/relay/internal/api"
	"github.com/datahub/relay/internal/application"
	"github.com/datahub/relay/internal/domain/admin"
	"github.com/datahub/relay/internal/domain/auth"
	"github.com/datahub/relay/internal/domain/billing"
	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/domain/quota"
	"github.com/datahub/relay/internal/job"
	"github.com/datahub/relay/internal/infrastructure/persistence/memory"
	"github.com/datahub/relay/internal/infrastructure/secret"
	"github.com/datahub/relay/internal/infrastructure/upstream"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg := loadConfig()

	// --- infrastructure adapters (dev: in-memory) ---
	store := memory.New()
	seedDemo(store)

	secrets := secret.NewStore(store, cfg.upstreamAccount, cfg.upstreamKey)

	httpClient := &http.Client{Timeout: cfg.upstreamTimeout}
	upClient := upstream.New(upstream.Config{BaseURL: cfg.upstreamBaseURL}, secrets, httpClient)

	// --- domain services ---
	verifier := auth.Md5Verifier{}
	authSvc := auth.New(store, secrets, verifier)
	quotaSvc := quota.New(store, store)
	billSvc := billing.New(billing.DefaultTable())
	adminSvc := admin.New(store, store, store, store, admin.Config{
		JWTSecret: cfg.adminJWTSecret,
		TokenTTL:  cfg.adminTokenTTL,
	})

	orch := application.NewQueryOrchestrator(authSvc, quotaSvc, billSvc, upClient, store, logger)

	// --- background workers (DESIGN §7.6) ---
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// bootstrap the initial admin operator (DESIGN §16.1).
	if err := adminSvc.BootstrapAdmin(ctx, cfg.adminUser, cfg.adminPass); err != nil {
		logger.Error("bootstrap admin failed", "err", err)
	} else {
		logger.Info("admin console ready", "loginUser", cfg.adminUser, "spaDir", cfg.spaDir)
	}

	// --- HTTP server ---
	server := api.NewServer(orch, adminSvc, store, cfg.spaDir)
	httpServer := &http.Server{
		Addr:              cfg.addr,
		Handler:           server.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	requery := job.NewRequeryWorker(store, store, upClient, billSvc, quotaSvc, cfg.requeryInterval, logger)
	recon := job.NewReconciliationJob(store, upClient, cfg.reconInterval, logger)
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
		AppID:      "y89098io",
		ClientUUID: "demo-client-uuid",
		Status:     "ACTIVE",
	}, "demo-app-secret", "Demo 商户", 100000, 100000)
}
