package main

import (
	"fmt"
	"os"
	"time"

	"github.com/datahub/relay/internal/domain/model"
	"gopkg.in/yaml.v3"
)

// upstreamConfig holds a single 上游子源 endpoint + 我方在该上游侧的凭证。一条路由
// 的上游是 []upstreamConfig：单源路由列表长度 1，聚合路由 (swfp) 长度 N，每条自带
// 完整凭证。kind 决定使用哪种上游客户端：gama(伽马, x1) | income(经济能力, v9/v8) |
// rental(租赁分V2-D, zlf) | blacklist(黑名单因子V35, blk) | entcredit(税务发票聚合, swfp)。
type upstreamConfig struct {
	kind    string // gama | income | rental | blacklist | entcredit | facecompare | idverify
	baseURL string
	// gama (伽马) / blacklist (黑名单因子V35) / entcredit (税务发票聚合) 凭证
	appID          string
	appSecret      string
	apiKey         string
	encryptionType int // blacklist: 2=MD5(默认); gama 固定明文
	// income (经济能力) 凭证
	account string
	key     string
	// rental (租赁分V2-D / 守信) 凭证 + 授权书
	institutionID string
	aesKey        string
	service       string
	mode          string
	oss           ossConfig
	licenseFile   string // 固定授权书本地文件, 启动时上传 OSS
	licenseType   int    // 0:图片 1:pdf
	// entcredit (swfp / 证通 entcreditapi) 凭证：与 gama/blacklist 的 appId/appSecret
	// 语义不同（HMAC-SHA256 签名 + 机构维度鉴权），单列专属字段，不复用 appID/appSecret。
	orgCode         string // 机构代码
	accessKeyID     string // AK
	secretAccessKey string // SK，Base64 编码，签名前需 Base64 解码取原始字节
	product         string // entcredit: 本子源的单个产品码 (P0130081/83/82/84)
	// label 是本子源在聚合 range 里的段名 (如 invoice1/tax1)；聚合路由 (len>1) 用，
	// 单源路由可省。为空时由 client 按 product/下标缺省。
	label string
}

// ossConfig holds aliyun OSS 凭证 for uploading the租赁分授权书 (rental 专用)。
type ossConfig struct {
	endpoint        string
	accessKeyID     string
	accessKeySecret string
	bucket          string
	objectPrefix    string
}

// dbConfig is a single version's PostgreSQL connection (独立数据库)。
type dbConfig struct {
	host     string
	port     int
	name     string
	user     string
	password string
	sslmode  string
	maxConns int
}

// redisConfig is a single version's Redis logical DB (独立计数器)。
type redisConfig struct {
	addr     string
	username string
	password string
	db       int
	poolSize int
}

// versionConfig is the full per-version dependency config (独立上游 + 独立库 +
// 独立 Redis)。各路由对外接口完全一致，仅靠路由名区分。upstreams 是本路由的上游
// 子源列表：单源长度 1，聚合路由 (swfp) 长度 N。
type versionConfig struct {
	upstreams []upstreamConfig
	db        dbConfig
	redis     redisConfig
}

// upstreamKind returns the route-level upstream kind (取首个子源；loadConfig 已校验
// 同一路由所有子源 kind 一致)。空列表时返回 ""。
func (v versionConfig) upstreamKind() string {
	if len(v.upstreams) == 0 {
		return ""
	}
	return v.upstreams[0].kind
}

// dsn builds a libpq key/value DSN (safe for passwords with special chars).
func (d dbConfig) dsn() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s connect_timeout=10 pool_max_conns=%d",
		d.host, d.port, d.user, d.password, d.name, d.sslmode, d.maxConns,
	)
}

// config holds runtime knobs. Sensitive values (上游/admin 凭证) live in a YAML
// config file (config.yaml, .gitignore'd), never hardcoded. Path defaults to
// ./config.yaml and is overridable via CONFIG_FILE.
type config struct {
	addr string

	upstreamTimeout time.Duration
	requeryInterval time.Duration
	demoAppSecret   string
	demoSeed        bool // 是否在 postgres 启动时注入 demo license（默认 false；0004 迁移已从生产清除 demo，勿在生产开启）

	// admin console (DESIGN §16). 后台登录/JWT 走统一控制面 (x1)。
	adminUser      string
	adminPass      string
	adminJWTSecret string
	adminTokenTTL  time.Duration
	spaDir         string

	// 存储后端选择 (DESIGN §11): memory | postgres。
	storageDriver string
	migrationsDir string

	// 每版本独立配置 (x1/v9/v8)。
	versions map[string]versionConfig
}

// duration parses Go-style duration strings (e.g. "4s", "5m", "8h") from YAML.
type duration time.Duration

func (d *duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	if s == "" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = duration(parsed)
	return nil
}

// fileUpstream mirrors a version's upstream YAML block.
type fileUpstream struct {
	Kind           string `yaml:"kind"`
	BaseURL        string `yaml:"baseURL"`
	AppID          string `yaml:"appId"`
	AppSecret      string `yaml:"appSecret"`
	APIKey         string `yaml:"apiKey"`
	EncryptionType int    `yaml:"encryptionType"`
	Account        string `yaml:"account"`
	Key            string `yaml:"key"`
	// rental (租赁分V2-D) 专用
	InstitutionID string  `yaml:"institutionId"`
	AESKey        string  `yaml:"aesKey"`
	Service       string  `yaml:"service"`
	Mode          string  `yaml:"mode"`
	OSS           fileOSS `yaml:"oss"`
	LicenseFile   string  `yaml:"licenseFile"`
	LicenseType   int     `yaml:"licenseType"`
	// entcredit (swfp / 证通 entcreditapi) 专用
	OrgCode         string `yaml:"orgCode"`
	AccessKeyID     string `yaml:"accessKeyId"`
	SecretAccessKey string `yaml:"secretAccessKey"`
	Product         string `yaml:"product"` // entcredit: 本子源的单个产品码
	// label：本子源在聚合 range 里的段名 (invoice1/tax1…)；聚合路由用，单源可省。
	Label string `yaml:"label"`
}

// fileOSS mirrors the rental upstream's oss YAML block.
type fileOSS struct {
	Endpoint        string `yaml:"endpoint"`
	AccessKeyID     string `yaml:"accessKeyId"`
	AccessKeySecret string `yaml:"accessKeySecret"`
	Bucket          string `yaml:"bucket"`
	ObjectPrefix    string `yaml:"objectPrefix"`
}

// fileDatabase mirrors a version's database YAML block.
type fileDatabase struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Name     string `yaml:"name"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	SSLMode  string `yaml:"sslmode"`
	MaxConns int    `yaml:"maxConns"`
}

// fileRedis mirrors a version's redis YAML block.
type fileRedis struct {
	Addr     string `yaml:"addr"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	PoolSize int    `yaml:"poolSize"`
}

// fileVersion mirrors one entry under versions: in config.yaml. 上游用 upstreams:
// 列表 (单源长度 1，聚合路由 swfp 长度 N)；旧的单块 upstream: 仍解析以向后兼容。
type fileVersion struct {
	Upstreams []fileUpstream `yaml:"upstreams"`
	Upstream  fileUpstream   `yaml:"upstream"` // 向后兼容：upstreams 为空时包成单元素列表
	Database  fileDatabase   `yaml:"database"`
	Redis     fileRedis      `yaml:"redis"`
}

// fileConfig mirrors the YAML structure of config.yaml.
type fileConfig struct {
	Addr     string `yaml:"addr"`
	Upstream struct {
		Timeout duration `yaml:"timeout"`
	} `yaml:"upstream"`
	Billing struct {
		RequeryInterval duration `yaml:"requeryInterval"`
	} `yaml:"billing"`
	Admin struct {
		BootstrapUser string   `yaml:"bootstrapUser"`
		BootstrapPass string   `yaml:"bootstrapPass"`
		JWTSecret     string   `yaml:"jwtSecret"`
		TokenTTL      duration `yaml:"tokenTTL"`
		SPADir        string   `yaml:"spaDir"`
	} `yaml:"admin"`
	Demo struct {
		AppSecret string `yaml:"appSecret"`
		Seed      *bool  `yaml:"seed"` // 默认 false；开发/演示 postgres 可设 true（e2e 由建库脚本 SEED_DEMO=1 播种）
	} `yaml:"demo"`
	Storage struct {
		Driver        string `yaml:"driver"`
		MigrationsDir string `yaml:"migrationsDir"`
	} `yaml:"storage"`
	Versions map[string]fileVersion `yaml:"versions"`
}

// loadConfig reads the YAML config file and applies non-sensitive structural
// defaults. It fails fast if an explicitly requested file is missing/invalid.
func loadConfig() (config, error) {
	path := os.Getenv("CONFIG_FILE")
	explicit := path != ""
	if path == "" {
		path = "config.yaml"
	}

	var fc fileConfig
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(raw, &fc); err != nil {
			return config{}, fmt.Errorf("parse config %s: %w", path, err)
		}
	case explicit:
		return config{}, fmt.Errorf("read config %s: %w", path, err)
	default:
		fmt.Fprintf(os.Stderr, "warning: %s not found; using non-sensitive defaults, secrets empty\n", path)
	}

	cfg := config{
		addr:            def(fc.Addr, ":8080"),
		upstreamTimeout: durOr(fc.Upstream.Timeout, 4*time.Second),
		requeryInterval: durOr(fc.Billing.RequeryInterval, 10*time.Second),
		demoAppSecret:   def(fc.Demo.AppSecret, "demo-app-secret"),
		demoSeed:        demoSeedOr(fc.Demo.Seed, false),

		adminUser:      def(fc.Admin.BootstrapUser, "admin"),
		adminPass:      fc.Admin.BootstrapPass,
		adminJWTSecret: fc.Admin.JWTSecret,
		adminTokenTTL:  durOr(fc.Admin.TokenTTL, 8*time.Hour),
		spaDir:         def(fc.Admin.SPADir, "web/admin/dist"),

		storageDriver: def(fc.Storage.Driver, "memory"),
		migrationsDir: def(fc.Storage.MigrationsDir, "migrations"),

		versions: make(map[string]versionConfig, len(model.Versions)),
	}

	for _, v := range model.Versions {
		fv, ok := fc.Versions[v]
		if !ok {
			// version 未在配置中给出：memory 模式仍可启用 (无需 DB/上游凭证)。
			continue
		}

		// 上游子源列表：优先 upstreams:；为空时退回旧的单块 upstream: (向后兼容)。
		files := fv.Upstreams
		if len(files) == 0 {
			files = []fileUpstream{fv.Upstream}
		}
		ups := make([]upstreamConfig, 0, len(files))
		for _, fu := range files {
			ups = append(ups, toUpstreamConfig(fu, v))
		}
		// 同一路由所有子源 kind 必须一致 (入参校验器/信封按路由统一，见 buildRouteStack)。
		for i := 1; i < len(ups); i++ {
			if ups[i].kind != ups[0].kind {
				return config{}, fmt.Errorf("version %s: upstreams 各子源 kind 必须一致 (%q vs %q)", v, ups[0].kind, ups[i].kind)
			}
		}

		cfg.versions[v] = versionConfig{
			upstreams: ups,
			db: dbConfig{
				host:     fv.Database.Host,
				port:     intOr(fv.Database.Port, 5432),
				name:     fv.Database.Name,
				user:     fv.Database.User,
				password: fv.Database.Password,
				sslmode:  def(fv.Database.SSLMode, "disable"),
				maxConns: intOr(fv.Database.MaxConns, 10),
			},
			redis: redisConfig{
				addr:     fv.Redis.Addr,
				username: fv.Redis.Username,
				password: fv.Redis.Password,
				db:       fv.Redis.DB,
				poolSize: intOr(fv.Redis.PoolSize, 10),
			},
		}
	}
	return cfg, nil
}

// toUpstreamConfig maps one YAML 上游子源块到 upstreamConfig，kind 空时按路由缺省。
func toUpstreamConfig(fu fileUpstream, version string) upstreamConfig {
	return upstreamConfig{
		kind:           def(fu.Kind, defaultKind(version)),
		baseURL:        fu.BaseURL,
		appID:          fu.AppID,
		appSecret:      fu.AppSecret,
		apiKey:         fu.APIKey, // 空值由各 client 自行默认 (gama/blacklist)
		encryptionType: fu.EncryptionType,
		account:        fu.Account,
		key:            fu.Key,

		institutionID: fu.InstitutionID,
		aesKey:        fu.AESKey,
		service:       fu.Service,
		mode:          fu.Mode,
		oss: ossConfig{
			endpoint:        fu.OSS.Endpoint,
			accessKeyID:     fu.OSS.AccessKeyID,
			accessKeySecret: fu.OSS.AccessKeySecret,
			bucket:          fu.OSS.Bucket,
			objectPrefix:    def(fu.OSS.ObjectPrefix, "approve_files/"),
		},
		licenseFile: fu.LicenseFile,
		licenseType: fu.LicenseType,

		orgCode:         fu.OrgCode,
		accessKeyID:     fu.AccessKeyID,
		secretAccessKey: fu.SecretAccessKey,
		product:         fu.Product,
		label:           fu.Label,
	}
}

// defaultKind picks the upstream client family by version: x1→gama, zlf→rental,
// blk→blacklist, swfp→entcredit, rlbd1/rlbd2→facecompare, sfzhy→idverify, others→income.
func defaultKind(version string) string {
	switch version {
	case "x1":
		return "gama"
	case "zlf":
		return "rental"
	case "blk":
		return "blacklist"
	case "swfp":
		return "entcredit"
	case "rlbd1", "rlbd2":
		return "facecompare"
	case "sfzhy":
		return "idverify"
	default:
		return "income"
	}
}

func def(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func demoSeedOr(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}

func intOr(v, fallback int) int {
	if v == 0 {
		return fallback
	}
	return v
}

func durOr(d duration, fallback time.Duration) time.Duration {
	if d == 0 {
		return fallback
	}
	return time.Duration(d)
}
