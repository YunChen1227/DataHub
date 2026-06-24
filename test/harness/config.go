package harness

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// PGConfig / RedisConfig hold the connection settings read from the relay YAML
// so the connectivity case can ping the same Aliyun PG + Redis the relay uses.
type PGConfig struct {
	Version  string
	Host     string
	Port     int
	Name     string
	User     string
	Password string
	SSLMode  string
}

type RedisConfig struct {
	Version  string
	Addr     string
	Username string
	Password string
	DB       int
}

// DSN builds a libpq key/value DSN for pgx.
func (c PGConfig) DSN() string {
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		c.Host, c.Port, c.User, c.Password, c.Name, c.SSLMode)
}

type fileVersion struct {
	Database struct {
		Host     string `yaml:"host"`
		Port     int    `yaml:"port"`
		Name     string `yaml:"name"`
		User     string `yaml:"user"`
		Password string `yaml:"password"`
		SSLMode  string `yaml:"sslmode"`
	} `yaml:"database"`
	Redis struct {
		Addr     string `yaml:"addr"`
		Username string `yaml:"username"`
		Password string `yaml:"password"`
		DB       int    `yaml:"db"`
	} `yaml:"redis"`
}

type yamlConfig struct {
	Versions map[string]fileVersion `yaml:"versions"`
}

// ConfigPath resolves the relay config file (CONFIG_FILE env or the Aliyun e2e
// default) so the connectivity test reads the live PG + Redis targets.
func ConfigPath() string {
	if v := os.Getenv("CONFIG_FILE"); v != "" {
		return v
	}
	return "config.aliyun.e2e.yaml"
}

// LoadStorageConfigs parses the relay YAML for每个版本独立 PG + Redis 连接设置。
// memory 模式 (无 database/redis 段) 返回空切片。
func LoadStorageConfigs() ([]PGConfig, []RedisConfig, error) {
	raw, err := os.ReadFile(ConfigPath())
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", ConfigPath(), err)
	}
	var fc yamlConfig
	if err := yaml.Unmarshal(raw, &fc); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", ConfigPath(), err)
	}
	var pgs []PGConfig
	var rds []RedisConfig
	for _, v := range Versions {
		fv, ok := fc.Versions[v]
		if !ok {
			continue
		}
		if fv.Database.Host != "" {
			port := fv.Database.Port
			if port == 0 {
				port = 5432
			}
			pgs = append(pgs, PGConfig{
				Version: v, Host: fv.Database.Host, Port: port, Name: fv.Database.Name,
				User: fv.Database.User, Password: fv.Database.Password,
				SSLMode: orDefault(fv.Database.SSLMode, "disable"),
			})
		}
		if fv.Redis.Addr != "" {
			rds = append(rds, RedisConfig{
				Version: v, Addr: fv.Redis.Addr, Username: fv.Redis.Username,
				Password: fv.Redis.Password, DB: fv.Redis.DB,
			})
		}
	}
	return pgs, rds, nil
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
