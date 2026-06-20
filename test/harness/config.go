package harness

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// PGConfig / RedisConfig hold the connection settings read from the relay YAML
// so the connectivity case can ping the same Aliyun PG + Redis the relay uses.
type PGConfig struct {
	Host     string
	Port     int
	Name     string
	User     string
	Password string
	SSLMode  string
}

type RedisConfig struct {
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

type yamlConfig struct {
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

// ConfigPath resolves the relay config file (CONFIG_FILE env or the Aliyun e2e
// default) so the connectivity test reads the live PG + Redis targets.
func ConfigPath() string {
	if v := os.Getenv("CONFIG_FILE"); v != "" {
		return v
	}
	return "config.aliyun.e2e.yaml"
}

// LoadStorageConfig parses the relay YAML for PG + Redis connection settings.
func LoadStorageConfig() (PGConfig, RedisConfig, error) {
	raw, err := os.ReadFile(ConfigPath())
	if err != nil {
		return PGConfig{}, RedisConfig{}, fmt.Errorf("read %s: %w", ConfigPath(), err)
	}
	var fc yamlConfig
	if err := yaml.Unmarshal(raw, &fc); err != nil {
		return PGConfig{}, RedisConfig{}, fmt.Errorf("parse %s: %w", ConfigPath(), err)
	}
	pg := PGConfig{
		Host: fc.Database.Host, Port: fc.Database.Port, Name: fc.Database.Name,
		User: fc.Database.User, Password: fc.Database.Password,
		SSLMode: orDefault(fc.Database.SSLMode, "disable"),
	}
	if pg.Port == 0 {
		pg.Port = 5432
	}
	rd := RedisConfig{Addr: fc.Redis.Addr, Username: fc.Redis.Username, Password: fc.Redis.Password, DB: fc.Redis.DB}
	return pg, rd, nil
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
