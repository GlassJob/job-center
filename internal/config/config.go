package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	NodeID    string          `mapstructure:"node_id"`
	HTTP      HTTPConfig      `mapstructure:"http"`
	Database  DatabaseConfig  `mapstructure:"database"`
	Security  SecurityConfig  `mapstructure:"security"`
	WebSocket WebSocketConfig `mapstructure:"websocket"`
	Scheduler SchedulerConfig `mapstructure:"scheduler"`
	Log       LogConfig       `mapstructure:"log"`
}

type SchedulerConfig struct {
	Enabled          bool   `mapstructure:"enabled"`
	GenerateInterval string `mapstructure:"generate_interval"`
	ClaimInterval    string `mapstructure:"claim_interval"` // 兼容：未配 claim_max_idle 时作为 maxIdle
	ClaimLead        string `mapstructure:"claim_lead"`
	ClaimMaxIdle     string `mapstructure:"claim_max_idle"`
	QueueInterval    string `mapstructure:"queue_interval"`
	Horizon          string `mapstructure:"horizon"`
	ClaimBatch       int    `mapstructure:"claim_batch"`
	QueueBatch       int    `mapstructure:"queue_batch"`
	ClaimTTL         string `mapstructure:"claim_ttl"`
}

func (c SchedulerConfig) GenerateIntervalDuration() time.Duration {
	return parseDur(c.GenerateInterval, 30*time.Second)
}
func (c SchedulerConfig) ClaimIntervalDuration() time.Duration {
	return parseDur(c.ClaimInterval, time.Second)
}
func (c SchedulerConfig) ClaimLeadDuration() time.Duration {
	return parseDur(c.ClaimLead, time.Second)
}
func (c SchedulerConfig) ClaimMaxIdleDuration() time.Duration {
	if strings.TrimSpace(c.ClaimMaxIdle) != "" {
		return parseDur(c.ClaimMaxIdle, 5*time.Second)
	}
	// 兼容旧 claim_interval：语义改为最长空闲睡
	if strings.TrimSpace(c.ClaimInterval) != "" {
		return parseDur(c.ClaimInterval, 5*time.Second)
	}
	return 5 * time.Second
}
func (c SchedulerConfig) QueueIntervalDuration() time.Duration {
	return parseDur(c.QueueInterval, 2*time.Second)
}
func (c SchedulerConfig) HorizonDuration() time.Duration {
	return parseDur(c.Horizon, time.Hour)
}
func (c SchedulerConfig) ClaimTTLDuration() time.Duration {
	return parseDur(c.ClaimTTL, 30*time.Second)
}

type WebSocketConfig struct {
	AuthTimeout       string `mapstructure:"auth_timeout"`
	PingInterval      string `mapstructure:"ping_interval"`
	PingTimeout       string `mapstructure:"ping_timeout"`
	WriteTimeout      string `mapstructure:"write_timeout"`
	MaxMessageBytes   int64  `mapstructure:"max_message_bytes"`
	SendQueueSize     int    `mapstructure:"send_queue_size"`
	HeartbeatInterval string `mapstructure:"heartbeat_interval"`
	LeaseTTL          string `mapstructure:"lease_ttl"`
}

func (w WebSocketConfig) AuthTimeoutDuration() time.Duration {
	return parseDur(w.AuthTimeout, 10*time.Second)
}
func (w WebSocketConfig) PingIntervalDuration() time.Duration {
	return parseDur(w.PingInterval, 20*time.Second)
}
func (w WebSocketConfig) PingTimeoutDuration() time.Duration {
	return parseDur(w.PingTimeout, 60*time.Second)
}
func (w WebSocketConfig) WriteTimeoutDuration() time.Duration {
	return parseDur(w.WriteTimeout, 10*time.Second)
}
func (w WebSocketConfig) HeartbeatIntervalDuration() time.Duration {
	return parseDur(w.HeartbeatInterval, 10*time.Second)
}
func (w WebSocketConfig) LeaseTTLDuration() time.Duration {
	return parseDur(w.LeaseTTL, 30*time.Second)
}

func parseDur(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return def
	}
	return d
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
	v.SetDefault("websocket.auth_timeout", "10s")
	v.SetDefault("websocket.ping_interval", "20s")
	v.SetDefault("websocket.ping_timeout", "60s")
	v.SetDefault("websocket.write_timeout", "10s")
	v.SetDefault("websocket.max_message_bytes", int64(1048576))
	v.SetDefault("websocket.send_queue_size", 64)
	v.SetDefault("websocket.heartbeat_interval", "10s")
	v.SetDefault("websocket.lease_ttl", "30s")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")
	v.SetDefault("scheduler.enabled", true)
	v.SetDefault("scheduler.generate_interval", "24h")
	v.SetDefault("scheduler.claim_interval", "5s") // 兼容旧字段：作 claim_max_idle
	v.SetDefault("scheduler.claim_lead", "1s")
	v.SetDefault("scheduler.claim_max_idle", "5s")
	v.SetDefault("scheduler.queue_interval", "2s")
	v.SetDefault("scheduler.horizon", "168h") // 7d
	v.SetDefault("scheduler.claim_batch", 50)
	v.SetDefault("scheduler.queue_batch", 50)
	v.SetDefault("scheduler.claim_ttl", "30s")

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
	if cfg.WebSocket.MaxMessageBytes <= 0 {
		cfg.WebSocket.MaxMessageBytes = 1048576
	}
	if cfg.WebSocket.SendQueueSize <= 0 {
		cfg.WebSocket.SendQueueSize = 64
	}
	if cfg.Scheduler.ClaimBatch <= 0 {
		cfg.Scheduler.ClaimBatch = 50
	}
	if cfg.Scheduler.QueueBatch <= 0 {
		cfg.Scheduler.QueueBatch = 50
	}
	return &cfg, nil
}
