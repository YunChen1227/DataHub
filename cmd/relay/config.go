package main

import (
	"fmt"
	"os"
	"time"

	"github.com/datahub/relay/internal/domain/model"
	"gopkg.in/yaml.v3"
)

// upstreamConfig holds a single version's upstream endpoint + 我方在该上游侧的
// 凭证。kind 决定使用哪种上游客户端：gama(伽马, x1) | income(经济能力, v9/v8) |
// rental(租赁分V2-D, zlf)。
type upstreamConfig struct {
	kind    string // gama | income | rental
	baseURL string
	// gama (伽马) / blacklist (黑名单因子V35) 凭证 (同为应诺尔 enol 端点)
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
// 独立 Redis)。三版本对外接口完全一致，仅靠路由名区分。
type versionConfig struct {
	upstream upstreamConfig
	db       dbConfig
	redis    redisConfig
}

// dsn builds a libpq key/value DSN (safe for passwords with special chars).
func (d dbConfig) dsn() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s pool_max_conns=%d",
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
	demoSeed        bool // 是否在 postgres 启动时注入 demo license（生产应 false）

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

// fileVersion mirrors one entry under versions: in config.yaml.
type fileVersion struct {
	Upstream fileUpstream `yaml:"upstream"`
	Database fileDatabase `yaml:"database"`
	Redis    fileRedis    `yaml:"redis"`
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
		Seed      *bool  `yaml:"seed"` // 默认 true；生产 postgres 建议 false
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
		demoSeed:        demoSeedOr(fc.Demo.Seed, true),

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
		cfg.versions[v] = versionConfig{
			upstream: upstreamConfig{
				kind:           def(fv.Upstream.Kind, defaultKind(v)),
				baseURL:        fv.Upstream.BaseURL,
				appID:          fv.Upstream.AppID,
				appSecret:      fv.Upstream.AppSecret,
				apiKey:         fv.Upstream.APIKey, // 空值由各 client 自行默认 (gama/blacklist)
				encryptionType: fv.Upstream.EncryptionType,
				account:        fv.Upstream.Account,
				key:            fv.Upstream.Key,

				institutionID: fv.Upstream.InstitutionID,
				aesKey:        fv.Upstream.AESKey,
				service:       fv.Upstream.Service,
				mode:          fv.Upstream.Mode,
				oss: ossConfig{
					endpoint:        fv.Upstream.OSS.Endpoint,
					accessKeyID:     fv.Upstream.OSS.AccessKeyID,
					accessKeySecret: fv.Upstream.OSS.AccessKeySecret,
					bucket:          fv.Upstream.OSS.Bucket,
					objectPrefix:    def(fv.Upstream.OSS.ObjectPrefix, "approve_files/"),
				},
				licenseFile: fv.Upstream.LicenseFile,
				licenseType: fv.Upstream.LicenseType,
			},
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

// defaultKind picks the upstream client family by version: x1→gama, zlf→rental,
// blk→blacklist, others→income.
func defaultKind(version string) string {
	switch version {
	case "x1":
		return "gama"
	case "zlf":
		return "rental"
	case "blk":
		return "blacklist"
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
