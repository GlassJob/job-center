package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	NodeID   string         `mapstructure:"node_id"`
	HTTP     HTTPConfig     `mapstructure:"http"`
	Database DatabaseConfig `mapstructure:"database"`
	Security SecurityConfig `mapstructure:"security"`
	Log      LogConfig      `mapstructure:"log"`
}

type HTTPConfig struct {
	Addr string `mapstructure:"addr"`
}

type DatabaseConfig struct {
	DSN             string `mapstructure:"dsn"`
	MaxOpenConns    int    `mapstructure:"max_open_conns"`
	MaxIdleConns    int    `mapstructure:"max_idle_conns"`
	ConnMaxLifetime string `mapstructure:"conn_max_lifetime"`
}

func (d DatabaseConfig) ConnMaxLifetimeDuration() time.Duration {
	if d.ConnMaxLifetime == "" {
		return time.Hour
	}
	dur, err := time.ParseDuration(d.ConnMaxLifetime)
	if err != nil {
		return time.Hour
	}
	return dur
}

type SecurityConfig struct {
	CredentialPepper      string `mapstructure:"credential_pepper"`
	TimestampSkewSeconds  int    `mapstructure:"timestamp_skew_seconds"`
	BootstrapClientSecret string `mapstructure:"bootstrap_client_secret"`
	BootstrapClientName   string `mapstructure:"bootstrap_client_name"`
}

func (s SecurityConfig) TimestampSkew() time.Duration {
	if s.TimestampSkewSeconds <= 0 {
		return 5 * time.Minute
	}
	return time.Duration(s.TimestampSkewSeconds) * time.Second
}

type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// Load 读取 YAML，并用 JC_* 环境变量覆盖（Viper AutomaticEnv）。
func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetDefault("node_id", "jc-1")
	v.SetDefault("http.addr", ":8080")
	v.SetDefault("database.max_open_conns", 20)
	v.SetDefault("database.max_idle_conns", 10)
	v.SetDefault("database.conn_max_lifetime", "1h")
	v.SetDefault("security.timestamp_skew_seconds", 300)
	v.SetDefault("security.bootstrap_client_name", "bootstrap")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")

	v.SetEnvPrefix("JC")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	_ = v.BindEnv("database.dsn", "JC_DB_DSN")
	_ = v.BindEnv("http.addr", "JC_HTTP_ADDR")
	_ = v.BindEnv("security.credential_pepper", "JC_CREDENTIAL_PEPPER")
	_ = v.BindEnv("security.bootstrap_client_secret", "JC_BOOTSTRAP_CLIENT_SECRET")
	_ = v.BindEnv("node_id", "JC_NODE_ID")
	_ = v.BindEnv("log.level", "JC_LOG_LEVEL")

	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	if cfg.Database.DSN == "" {
		return nil, fmt.Errorf("database.dsn is required (config or JC_DB_DSN)")
	}
	if cfg.Security.CredentialPepper == "" {
		return nil, fmt.Errorf("security.credential_pepper is required (config or JC_CREDENTIAL_PEPPER)")
	}
	return &cfg, nil
}
