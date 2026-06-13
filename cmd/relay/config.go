package main

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// config holds runtime knobs. Sensitive values (上游/admin 凭证) live in a YAML
// config file (config.yaml, .gitignore'd), never hardcoded. Path defaults to
// ./config.yaml and is overridable via CONFIG_FILE.
type config struct {
	addr string

	// 上游 provider 路由 (DESIGN §6): gama | income_cls。
	upstreamProvider string
	upstreamTimeout  time.Duration

	// 上游A: 伽马分层分 (伽马PDF)。
	gamaBaseURL string
	gamaAppID   string
	gamaSecret  string
	gamaAPIKey  string

	// 上游B: income_cls。
	incomeBaseURL string
	incomeAccount string
	incomeKey     string

	demoAppSecret   string
	requeryInterval time.Duration
	reconInterval   time.Duration

	// admin console (DESIGN §16)
	adminUser      string
	adminPass      string
	adminJWTSecret string
	adminTokenTTL  time.Duration
	spaDir         string
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

// fileConfig mirrors the YAML structure of config.yaml.
type fileConfig struct {
	Addr     string `yaml:"addr"`
	Upstream struct {
		Provider string   `yaml:"provider"`
		Timeout  duration `yaml:"timeout"`
		Gama     struct {
			BaseURL   string `yaml:"baseURL"`
			AppID     string `yaml:"appId"`
			AppSecret string `yaml:"appSecret"`
			APIKey    string `yaml:"apiKey"`
		} `yaml:"gama"`
		IncomeCls struct {
			BaseURL string `yaml:"baseURL"`
			Account string `yaml:"account"`
			Key     string `yaml:"key"`
		} `yaml:"incomeCls"`
	} `yaml:"upstream"`
	Billing struct {
		RequeryInterval duration `yaml:"requeryInterval"`
		ReconInterval   duration `yaml:"reconInterval"`
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
	} `yaml:"demo"`
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
		// no config file present: proceed with structural defaults only
		// (secrets stay empty — never defaulted in code).
		fmt.Fprintf(os.Stderr, "warning: %s not found; using non-sensitive defaults, secrets empty\n", path)
	}

	cfg := config{
		addr:             def(fc.Addr, ":8080"),
		upstreamProvider: def(fc.Upstream.Provider, "gama"),
		upstreamTimeout:  durOr(fc.Upstream.Timeout, 4*time.Second),

		gamaBaseURL: fc.Upstream.Gama.BaseURL,
		gamaAppID:   fc.Upstream.Gama.AppID,
		gamaSecret:  fc.Upstream.Gama.AppSecret,
		gamaAPIKey:  def(fc.Upstream.Gama.APIKey, "gama_ctmz_layer_score"),

		incomeBaseURL: fc.Upstream.IncomeCls.BaseURL,
		incomeAccount: fc.Upstream.IncomeCls.Account,
		incomeKey:     fc.Upstream.IncomeCls.Key,

		demoAppSecret:   fc.Demo.AppSecret,
		requeryInterval: durOr(fc.Billing.RequeryInterval, 10*time.Second),
		reconInterval:   durOr(fc.Billing.ReconInterval, 5*time.Minute),

		adminUser:      def(fc.Admin.BootstrapUser, "admin"),
		adminPass:      fc.Admin.BootstrapPass,
		adminJWTSecret: fc.Admin.JWTSecret,
		adminTokenTTL:  durOr(fc.Admin.TokenTTL, 8*time.Hour),
		spaDir:         def(fc.Admin.SPADir, "web/admin/dist"),
	}
	return cfg, nil
}

func def(v, fallback string) string {
	if v == "" {
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
