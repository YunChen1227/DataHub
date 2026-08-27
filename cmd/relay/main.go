// Command relay is the entrypoint for the经济能力查询转接服务. It wires the
// hexagonal layers together and starts the HTTP server + background workers.
//
// 各路由 (x1/v9/v8/zlf/blk) 对外接口完全一致，仅靠路由名区分；存储按「域」装配
// (x1/v8v9/zlf/blk 四域，v8/v9 共用 v8v9 域库与同一套 license，其余路由各自独立
// 一套 DB+Redis+license)。跨域使用 license 一律鉴权失败。
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
	"github.com/datahub/relay/internal/domain/parse"
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
	// auth 是本域共享的鉴权服务（license+secret 进程内缓存）。按域而非按路由建：
	// v8/v9 共用 v8v9 域的同一实例，后台在任一路由改 license 都能命中同一份缓存
	// 失效（admin.WithLicenseChangeHook → auth.Invalidate）。
	auth    *auth.Service
	cleanup func()
}

// routeStack is one fully-wired route (独立 orchestrator + 后台服务 + 复查 worker
// + 异步记账器)，接到其所属域的存储 + 自己的上游客户端。
type routeStack struct {
	orch    *application.QueryOrchestrator
	admin   *admin.Service
	requery *job.RequeryWorker
	books   *application.Bookkeeper
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

// checkStorageIsolation fails fast when两个不同的域被配置成共用同一个 PostgreSQL
// 库或同一个 Redis 逻辑库——那会破坏「各域独立 license/记录」的隔离承诺。
// (v8/v9 同属 v8v9 域，共用其 owner v9 的库属于设计内共享，不在校验之列。)
func checkStorageIsolation(cfg config) error {
	dbSeen := make(map[string]string)    // host:port/name -> domain
	redisSeen := make(map[string]string) // addr/db -> domain
	for _, domain := range model.Domains {
		vc, ok := cfg.versions[domainOwner(domain)]
		if !ok {
			continue
		}
		if vc.db.name != "" {
			key := fmt.Sprintf("%s:%d/%s", vc.db.host, vc.db.port, vc.db.name)
			if prev, dup := dbSeen[key]; dup {
				return fmt.Errorf("域 %s 与 %s 配置了同一个数据库 %s；每个域必须使用独立数据库", prev, domain, key)
			}
			dbSeen[key] = domain
		}
		if vc.redis.addr != "" {
			key := fmt.Sprintf("%s/%d", vc.redis.addr, vc.redis.db)
			if prev, dup := redisSeen[key]; dup {
				return fmt.Errorf("域 %s 与 %s 配置了同一个 Redis 逻辑库 %s；每个域必须使用独立 Redis db", prev, domain, key)
			}
			redisSeen[key] = domain
		}
	}
	return nil
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

	// 上游共享 HTTP client：显式 Transport 提高连接复用率。Go 默认
	// MaxIdleConnsPerHost=2——多源路由一次请求可能并发打同一主机，默认值会导致
	// 反复新建 TCP+TLS 连接（每次 50-200ms 握手），是端到端延迟的大头之一。
	httpClient := &http.Client{
		Timeout: cfg.upstreamTimeout,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			MaxIdleConns:        256,
			MaxIdleConnsPerHost: 64,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 5 * time.Second,
			ForceAttemptHTTP2:   true,
		},
	}

	// --- 存储隔离防呆校验后，按域开库 (v8/v9 共用 v8v9 域库)，再逐路由装配 ---
	if err := checkStorageIsolation(cfg); err != nil {
		logger.Error("storage isolation check failed", "err", err)
		os.Exit(1)
	}

	domainStores := make(map[string]*domainStorage, len(model.Domains))
	cleanups := make([]func(), 0, len(model.Domains))
	defer func() {
		for _, c := range cleanups {
			c()
		}
	}()
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

	apiStacks := make(map[string]*api.VersionStack, len(model.Versions))
	adminByRoute := make(map[string]*admin.Service, len(model.Versions))
	bookkeepers := make([]*application.Bookkeeper, 0, len(model.Versions))
	for _, route := range model.Versions {
		ds := domainStores[model.RouteDomain(route)]
		st, err := buildRouteStack(cfg, route, ds, httpClient, logger)
		if err != nil {
			logger.Error("build route stack failed", "route", route, "err", err)
			os.Exit(1)
		}
		apiStacks[route] = &api.VersionStack{Orch: st.orch, Admin: st.admin}
		adminByRoute[route] = st.admin
		bookkeepers = append(bookkeepers, st.books)
		go st.requery.Run(ctx)
		logger.Info("route stack ready", "route", route, "domain", model.RouteDomain(route),
			"upstream", cfg.versions[route].upstreamKind(), "sources", len(cfg.versions[route].upstreams))
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
	// HTTP 已停止接收新请求后再 drain 异步记账队列：保证在途请求的结算/审计
	// 全部落库（宁可多等几百毫秒，不丢计费凭证），随后 defer 里的 cleanup 才关库。
	for _, b := range bookkeepers {
		b.Close()
	}
	logger.Info("bookkeepers drained")
}

// buildDomainStorage opens the storage backend for one license 域 (DB+Redis or
// memory)，使用该域 owner 路由的 db/redis 配置。同一域只建一次，供域内各路由复用。
// 生产 (postgres) 不播种 demo license；memory (开发) 按域播种各自独立的 demo 凭证
// (model.DemoAppKey；v8/v9 同域共用一个)。
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
			if err := postgres.SeedDemo(ctx, pg, owner); err != nil {
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
		ds := &domainStorage{
			licenseRepo: pg, ledgerRepo: pg, quotaRepo: rq, auditRepo: pg,
			adminRepo: pg, userRepo: pg, secrets: secret.NewStore(pg),
			cleanup: func() { rq.Close(); pg.Close() },
		}
		ds.auth = auth.New(ds.licenseRepo, ds.secrets, auth.Md5Verifier{})
		return ds, nil
	default:
		store := memory.New()
		seedDemo(store, domain, cfg.demoAppSecret)
		ds := &domainStorage{
			licenseRepo: store, ledgerRepo: store, quotaRepo: store, auditRepo: store,
			adminRepo: store, userRepo: store, secrets: secret.NewStore(store),
			cleanup: func() {},
		}
		ds.auth = auth.New(ds.licenseRepo, ds.secrets, auth.Md5Verifier{})
		return ds, nil
	}
}

// buildRouteStack wires the per-route dependencies (auth/quota/billing/orchestrator/
// admin/requery) on top of the route's 域存储 + 自己的上游客户端。
func buildRouteStack(cfg config, route string, ds *domainStorage, httpClient *http.Client, logger *slog.Logger) (*routeStack, error) {
	vc := cfg.versions[route]
	log := logger.With("route", route)

	upClient, routeKind, err := buildUpstreams(route, vc.upstreams, httpClient, log)
	if err != nil {
		return nil, err
	}

	authSvc := ds.auth // 域级共享（含 license 缓存；v8/v9 同域同缓存）
	quotaSvc := quota.New(ds.quotaRepo, ds.ledgerRepo)
	billSvc := billing.New(billing.DefaultTable())
	adminSvc := admin.New(route, ds.adminRepo, ds.userRepo, ds.auditRepo, admin.Config{
		JWTSecret: cfg.adminJWTSecret,
		TokenTTL:  cfg.adminTokenTTL,
	}).WithLicenseChangeHook(authSvc.Invalidate) // 后台改密/停用/删除即时失效鉴权缓存
	// 异步记账：结算 + 审计移出响应关键路径（每请求省 3-5 次串行 DB 写）；
	// 队列满降级同步，优雅停机时 drain（见 main 的 shutdown 顺序）。
	books := application.NewBookkeeper(quotaSvc, ds.auditRepo, 0, 0, log)
	orch := application.NewQueryOrchestrator(route, authSvc, quotaSvc, billSvc, upClient, ds.auditRepo, log).
		WithBookkeeper(books)
	// 网关校验口径必须与该路由上游的真实要求一致（必填字段前置拦截，不透传给
	// 上游报错）。默认 parse.Parse (mobile必/idCard必/name选) 仅适用于与经济能力
	// 同口径的上游 (gama/income)。多源路由 (含混合 kind) 按**首个子源 = 主源** kind
	// 选校验器；约定同一路由各源入参口径取并集、由该校验器统一覆盖 (grgjj 主源
	// incomeag 的 ParseWithName 即 name+idCard+mobile 三要素，备源 bgjj 同口径)。
	switch routeKind {
	case upstream.ProviderRental, upstream.ProviderBlacklist:
		// zlf (租赁分 name 必传) / blk (黑名单V35 name 参与摘要匹配) 均要求姓名必填。
		orch.WithParser(parse.ParseWithName)
	case upstream.ProviderFaceCompare:
		// rlbd1 人脸身份证比对：name+idCard 必填、image|url 二选一（对齐数脉契约）。
		orch.WithParser(parse.ParseFace)
	case upstream.ProviderIDVerify:
		// sfzhy 身份证三要素核验：name+idCard(15/18)+profilePicture 均必填。
		orch.WithParser(parse.ParseIDVerify)
	case upstream.ProviderIDCheck:
		// sfsm 身份证实名核验：上游业务参数表只有 name+idcard 两项且均必填、**无 mobile**。
		orch.WithParser(parse.ParseIDCheck)
	case upstream.ProviderConsumeTxn:
		// xfjy 消费交易特征：上游 params 全选填，网关仅校验格式并要求至少一个
		// 查询要素 (name/idCard/mobile)，对齐上游必填口径不臆造多余必填。
		orch.WithParser(parse.ParseConsumeTxn)
	case upstream.ProviderComplaint:
		// tsfx 投诉分析识别名单：mobile + poly(C1/C2/C3) 均必填 (对齐 kfongtech 契约)。
		orch.WithParser(parse.ParseComplaint)
	case upstream.ProviderLXScore:
		// lxf 灵犀分：上游 name/mobile/idCardNo 三项参数表都标"必传"，但文档 §2.2 明确
		// 姓名缺省时传固定值 MD5("")，故实际必填口径为 mobile+idCardNo，name 选填——
		// 恰与默认 parse.Parse 一致，无需专属校验器（此 case 仅为记录该判断依据）。
	case upstream.ProviderIncomeAg:
		// grgjj 收入A_g版：data 输入参数 name/cid/mobile 均标"是"(必填)，故网关前置
		// 要求三要素齐全 (name 必填、mobile/idCard 合法)——用 ParseWithName。
		orch.WithParser(parse.ParseWithName)
	case upstream.ProviderBgPG:
		// grsb 背景评估 BJPG-01：请求体参数表只有 idCard+name 两项且均必填，**不含
		// mobile**——不能沿用三要素口径，用专属 ParseBgPG。
		orch.WithParser(parse.ParseBgPG)
	}
	requery := job.NewRequeryWorker(ds.ledgerRepo, ds.licenseRepo, upClient, billSvc, quotaSvc, cfg.requeryInterval, log)

	return &routeStack{orch: orch, admin: adminSvc, requery: requery, books: books}, nil
}

// buildUpstreams 把一条路由的上游子源列表装配成一个 port.UpstreamPort，返回装配后的
// 客户端与路由 kind (=首个子源 kind = 主源，供 parser 选择)。装配策略二选一：
//   - 串行寻源 Sourcer (命中即停)：源之间**可互相替代** (同一种数据、不同供应商)，
//     按 priority/成本串行、第一个查得即停、后续不再调用 (省钱)。触发条件：路由内
//     kind 不一致，或任一源显式配置了 priority/costFen/costOn。grgjj (incomeag 主
//   - bgjj 备) 走此路。
//   - 并发聚合 Aggregator：源之间**互补/并列** (各查各的、结果拼段)，或单源直通。
//     其它路由 (含单源) 维持原行为。
func buildUpstreams(route string, ucs []upstreamConfig, httpClient *http.Client, logger *slog.Logger) (port.UpstreamPort, string, error) {
	if len(ucs) == 0 {
		// 路由未在配置中给出 (memory 模式常见)：合成一个按路由缺省 kind 的空 client，
		// 保持"不崩溃"的历史行为——该 client 在被调用前不产生任何副作用。
		ucs = []upstreamConfig{{kind: defaultKind(route)}}
	}

	if useSourcer(ucs) {
		srcs := make([]upstream.Source, 0, len(ucs))
		for i, uc := range ucs {
			client, err := buildClient(route, uc, httpClient, logger)
			if err != nil {
				return nil, "", err
			}
			srcs = append(srcs, upstream.Source{
				Name:     labelFor(uc, i),
				Priority: uc.priority,
				CostFen:  uc.costFen,
				CostOn:   uc.costOn,
				Port:     client,
			})
		}
		sourcer, err := upstream.NewSourcer(srcs, 0)
		if err != nil {
			return nil, "", err
		}
		logger.Info("route 使用串行寻源 (命中即停)", "route", route, "sources", len(srcs))
		return sourcer, ucs[0].kind, nil
	}

	sources := make([]upstream.LabeledUpstream, 0, len(ucs))
	for i, uc := range ucs {
		client, err := buildClient(route, uc, httpClient, logger)
		if err != nil {
			return nil, "", err
		}
		sources = append(sources, upstream.LabeledUpstream{Label: labelFor(uc, i), Port: client})
	}
	agg, err := upstream.NewAggregator(sources)
	if err != nil {
		return nil, "", err
	}
	return agg, ucs[0].kind, nil
}

// useSourcer 判定一条路由是否走串行寻源 (命中即停) 而非并发聚合：源间 kind 不一致
// (可替代的异构供应商)，或任一源显式配置了寻源属性 (priority/costFen/costOn)。
func useSourcer(ucs []upstreamConfig) bool {
	if len(ucs) <= 1 {
		return false
	}
	for i := range ucs {
		if ucs[i].kind != ucs[0].kind || ucs[i].priority != 0 || ucs[i].costFen != 0 || ucs[i].costOn != "" {
			return true
		}
	}
	return false
}

// labelFor 决定子源在聚合 range 里的段名 / 寻源日志里的源名：显式 label 优先，否则
// 回退为中性的 source+下标。
//
// **不得回退成 kind**：聚合路由的段名会随 body.result.range 发给下游，用 kind 当段名
// 等于把上游家族名（gama/blacklist/incomeag…）透给下游，违反「range 不透出任何上游
// 相关字段」。配置里显式写 label 时也请用业务语义名（如 invoice/tax），别写上游名。
func labelFor(uc upstreamConfig, idx int) string {
	if uc.label != "" {
		return uc.label
	}
	return fmt.Sprintf("source%d", idx+1)
}

// buildClient constructs one 上游子源 client (port.UpstreamPort) by kind.
func buildClient(version string, uc upstreamConfig, httpClient *http.Client, logger *slog.Logger) (port.UpstreamPort, error) {
	switch uc.kind {
	case upstream.ProviderIncome:
		client := upstream.NewIncome(upstream.IncomeConfig{
			BaseURL: uc.baseURL,
			Account: uc.account,
			Key:     uc.key,
			Version: version,
			// v9 与 v8 的 verify 公式不同：v9 含 mobile，v8 不含（对方 showdoc
			// 经济能力10W-V8；2026-07-06 实测 v8 带 mobile 签名被拒 013）。
			SignWithMobile: version != "v8",
		}, httpClient)
		return client, nil
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
		return client, nil
	case upstream.ProviderBlacklist:
		client := upstream.NewBlacklist(upstream.BlacklistConfig{
			BaseURL:        uc.baseURL,
			AppID:          uc.appID,
			Secret:         uc.appSecret,
			APIKey:         uc.apiKey,
			EncryptionType: uc.encryptionType,
		}, httpClient)
		return client, nil
	case upstream.ProviderFaceCompare:
		client := upstream.NewFaceCompare(upstream.FaceCompareConfig{
			BaseURL:   uc.baseURL,
			AppID:     uc.appID,
			AppSecret: uc.appSecret,
		}, httpClient)
		return client, nil
	case upstream.ProviderIDVerify:
		client := upstream.NewIDVerify(upstream.IDVerifyConfig{
			BaseURL:   uc.baseURL,
			AppID:     uc.appID,
			AppSecret: uc.appSecret,
		}, httpClient)
		return client, nil
	case upstream.ProviderIDCheck:
		// sfsm 身份证实名核验 (数脉)：appID=appid、appSecret=app_security，
		// 与 facecompare 同一服务商同一套签名。
		client := upstream.NewIDCheck(upstream.IDCheckConfig{
			BaseURL:   uc.baseURL,
			AppID:     uc.appID,
			AppSecret: uc.appSecret,
		}, httpClient)
		return client, nil
	case upstream.ProviderConsumeTxn:
		// xfjy 消费交易特征 (data-bean)：sceneid=appID、appkey=appSecret、
		// procode 默认 fk3002（可经 apiKey 覆盖）。
		client := upstream.NewConsumeTxn(upstream.ConsumeTxnConfig{
			BaseURL: uc.baseURL,
			SceneID: uc.appID,
			AppKey:  uc.appSecret,
			Procode: uc.apiKey,
		}, httpClient)
		return client, nil
	case upstream.ProviderComplaint:
		// tsfx 投诉分析识别名单 (kfongtech)：apiKey=Apikey、aesKey=param AES 密钥、
		// appSecret=sign 密钥 (复用既有凭证字段，不新增 config 字段)。
		client := upstream.NewComplaint(upstream.ComplaintConfig{
			BaseURL:    uc.baseURL,
			APIKey:     uc.apiKey,
			AESKey:     uc.aesKey,
			SignSecret: uc.appSecret,
		}, httpClient)
		return client, nil
	case upstream.ProviderLXScore:
		// lxf 灵犀分 score_195_v1 (fullink)：appId=customerId(商户code)、
		// apiKey=customerProdId(产品code)、appSecret=encryptKey(DES 密钥，兼作
		// sign 加密与 data 解密；复用既有凭证字段，不新增 config 字段)。
		client := upstream.NewLXScore(upstream.LXScoreConfig{
			BaseURL:        uc.baseURL,
			CustomerID:     uc.appID,
			CustomerProdID: uc.apiKey,
			EncryptKey:     uc.appSecret,
		}, httpClient)
		return client, nil
	case upstream.ProviderIncomeAg:
		// grgjj 收入A_g版 (yrzx)：account=账户、key=商户 key（MD5 加签 + 换取动态 3DES
		// 密钥）。3DES 密钥由上游「获取秘钥」接口动态下发，无需配置；aesKey 仅作联调/
		// 本地 mock 的可选静态覆盖（填了则跳过获取秘钥）。type 缺省 1106。
		client := upstream.NewIncomeAg(upstream.IncomeAgConfig{
			BaseURL:            uc.baseURL,
			Account:            uc.account,
			SignKey:            uc.key,
			StaticTripleDESKey: uc.aesKey,
		}, httpClient)
		return client, nil
	case upstream.ProviderBgJJ:
		// grgjj 备用公积金源 (jeoho)：account=merchant_id、key=merchantKey (MD5 加签)、
		// certPath/certPass=P12 客户端证书 (双向认证)。certPath 为空 (mock/memory 联调，
		// 明文 HTTP) 时复用共享 httpClient，不加载证书。
		client, err := upstream.NewBgJJ(upstream.BgJJConfig{
			BaseURL:     uc.baseURL,
			MerchantID:  uc.account,
			MerchantKey: uc.key,
			CertPath:    uc.certPath,
			CertPass:    uc.certPass,
		}, httpClient)
		if err != nil {
			return nil, err
		}
		return client, nil
	case upstream.ProviderBgPG:
		// grsb 背景评估 BJPG-01：account=请求头 accountId、apiKey=请求头 prodId
		// (缺省 BJPG-01)、aesKey=encryptKey (hex 文本，解码后为 AES/CBC 密钥；
		// 形态非法即启动失败，不静默降级)。复用既有凭证字段，不新增 config 字段。
		client, err := upstream.NewBgPG(upstream.BgPGConfig{
			BaseURL:    uc.baseURL,
			AccountID:  uc.account,
			ProdID:     uc.apiKey,
			EncryptKey: uc.aesKey,
		}, httpClient)
		if err != nil {
			return nil, err
		}
		return client, nil
	case upstream.ProviderGama, "":
		client := upstream.NewGama(upstream.GamaConfig{
			BaseURL: uc.baseURL,
			AppID:   uc.appID,
			Secret:  uc.appSecret,
			APIKey:  uc.apiKey,
		}, httpClient)
		return client, nil
	default:
		return nil, fmt.Errorf("version %s: unknown upstream kind %q", version, uc.kind)
	}
}

// seedDemo registers the 域's dev demo license in a memory store so the
// e2e/admin flows have a known client per 域。demo appKey 按域各不相同
// (model.DemoAppKey)，保证 demo 凭证无法跨域使用；v8/v9 同域共用一个。
func seedDemo(store *memory.Store, domain, demoSecret string) {
	up := strings.ToUpper(domain)
	store.SeedLicense(&model.LicenseView{
		LicenseID:  "LIC-DEMO-" + up,
		AppKey:     model.DemoAppKey(domainOwner(domain)),
		ClientUUID: "demo-client-" + domain,
		Status:     "ACTIVE",
	}, demoSecret, "Demo 商户("+up+")", "13800001234")
}
