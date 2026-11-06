// Package config 负责加载 Job Center 配置：YAML 文件 + 环境变量覆盖（环境变量优先）。
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration 支持 YAML 中以 "10s"、"1m30s" 形式书写的时长。
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(strings.TrimSpace(n.Value))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", n.Value, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) D() time.Duration { return time.Duration(d) }

type AdminClient struct {
	Secret string `yaml:"secret"`
	// MaxPerms 该客户端可代表操作员行使的权限上限；["*"] 表示不限。
	MaxPerms []string `yaml:"max_perms"`
}

type Config struct {
	NodeID string `yaml:"node_id"`
	HTTP   struct {
		ExecutorAddr    string   `yaml:"executor_addr"`
		AdminAddr       string   `yaml:"admin_addr"`
		MaxBodyBytes    int64    `yaml:"max_body_bytes"`
		ShutdownTimeout Duration `yaml:"shutdown_timeout"`
	} `yaml:"http"`
	Database struct {
		DSN             string   `yaml:"dsn"`
		MaxOpenConns    int      `yaml:"max_open_conns"`
		MaxIdleConns    int      `yaml:"max_idle_conns"`
		ConnMaxLifetime Duration `yaml:"conn_max_lifetime"`
	} `yaml:"database"`
	Security struct {
		CredentialPepper string                 `yaml:"credential_pepper"`
		AdminClients     map[string]AdminClient `yaml:"admin_clients"`
		TimestampSkew    Duration               `yaml:"timestamp_skew"`
		NonceTTL         Duration               `yaml:"nonce_ttl"`
	} `yaml:"security"`
	WebSocket struct {
		AuthTimeout        Duration `yaml:"auth_timeout"`
		MaxMessageBytes    int64    `yaml:"max_message_bytes"`
		PingInterval       Duration `yaml:"ping_interval"`
		PingTimeout        Duration `yaml:"ping_timeout"`
		WriteTimeout       Duration `yaml:"write_timeout"`
		SendQueueSize      int      `yaml:"send_queue_size"`
		MaxConnections     int      `yaml:"max_connections"`
		MaxConnsPerService int      `yaml:"max_conns_per_service"`
		RatePerSecond      int      `yaml:"rate_per_second"`
		RateBurst          int      `yaml:"rate_burst"`
		LeaseTTL           Duration `yaml:"lease_ttl"`
		LogBatchMaxEntries int      `yaml:"log_batch_max_entries"`
	} `yaml:"websocket"`
	Instance struct {
		HeartbeatInterval Duration `yaml:"heartbeat_interval"`
		// SuspectAfter 超过该时长未收到心跳：ONLINE → SUSPECT（默认 3 个心跳周期）。
		SuspectAfter Duration `yaml:"suspect_after"`
		// OfflineAfter 进入 SUSPECT（或断线）后超过该时长未恢复：→ OFFLINE。
		OfflineAfter  Duration `yaml:"offline_after"`
		SweepInterval Duration `yaml:"sweep_interval"`
	} `yaml:"instance"`
	Scheduler struct {
		// DefaultTimezone 为空 = Job Center 服务器本地时区（TZ 环境变量或 /etc/localtime）。
		DefaultTimezone string   `yaml:"default_timezone"`
		TickInterval    Duration `yaml:"tick_interval"`
		LeaseTTL        Duration `yaml:"lease_ttl"`
		LostGrace       Duration `yaml:"lost_grace"`
		TimeoutGrace    Duration `yaml:"timeout_grace"`
	} `yaml:"scheduler"`
	Archive struct {
		Dir              string   `yaml:"dir"`
		DailyAt          string   `yaml:"daily_at"` // HH:MM local
		LogRetentionDays int      `yaml:"log_retention_days"`
		ExecRetentionDays int     `yaml:"execution_retention_days"`
	} `yaml:"archive"`
	Log struct {
		Level  string `yaml:"level"`
		Format string `yaml:"format"` // json|text
	} `yaml:"log"`
}

// Default 返回带默认值的配置。
func Default() *Config {
	c := &Config{}
	c.HTTP.ExecutorAddr = ":8080"
	c.HTTP.AdminAddr = "127.0.0.1:8081"
	c.HTTP.MaxBodyBytes = 1 << 20
	c.HTTP.ShutdownTimeout = Duration(15 * time.Second)
	c.Database.MaxOpenConns = 50
	c.Database.MaxIdleConns = 10
	c.Database.ConnMaxLifetime = Duration(30 * time.Minute)
	c.Security.TimestampSkew = Duration(300 * time.Second)
	c.Security.NonceTTL = Duration(10 * time.Minute)
	ws := &c.WebSocket
	ws.AuthTimeout = Duration(5 * time.Second)
	ws.MaxMessageBytes = 1 << 20
	ws.PingInterval = Duration(20 * time.Second)
	ws.PingTimeout = Duration(40 * time.Second)
	ws.WriteTimeout = Duration(10 * time.Second)
	ws.SendQueueSize = 256
	ws.MaxConnections = 10000
	ws.MaxConnsPerService = 200
	ws.RatePerSecond = 200
	ws.RateBurst = 400
	ws.LeaseTTL = Duration(60 * time.Second)
	ws.LogBatchMaxEntries = 100
	c.Instance.HeartbeatInterval = Duration(10 * time.Second)
	c.Instance.SuspectAfter = Duration(30 * time.Second)
	c.Instance.OfflineAfter = Duration(90 * time.Second)
	c.Instance.SweepInterval = Duration(5 * time.Second)
	c.Scheduler.TickInterval = Duration(time.Second)
	c.Scheduler.LeaseTTL = Duration(15 * time.Second)
	c.Scheduler.LostGrace = Duration(60 * time.Second)
	c.Scheduler.TimeoutGrace = Duration(60 * time.Second)
	c.Archive.Dir = "./data/archive"
	c.Archive.DailyAt = "03:30"
	c.Archive.LogRetentionDays = 365
	c.Archive.ExecRetentionDays = 365
	c.Log.Level = "info"
	c.Log.Format = "json"
	return c
}

// Load 读取 YAML（path 为空则只用默认值），再应用环境变量覆盖并校验。
func Load(path string) (*Config, error) {
	c := Default()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := yaml.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	if err := c.ApplyEnv(os.LookupEnv); err != nil {
		return nil, err
	}
	if c.NodeID == "" {
		h, _ := os.Hostname()
		c.NodeID = fmt.Sprintf("%s-%d", h, os.Getpid())
	}
	return c, c.Validate()
}

// ApplyEnv 应用 JC_* 环境变量覆盖。lookup 便于测试注入。
func (c *Config) ApplyEnv(lookup func(string) (string, bool)) error {
	str := func(k string, dst *string) {
		if v, ok := lookup(k); ok && v != "" {
			*dst = v
		}
	}
	str("JC_NODE_ID", &c.NodeID)
	str("JC_HTTP_EXECUTOR_ADDR", &c.HTTP.ExecutorAddr)
	str("JC_HTTP_ADMIN_ADDR", &c.HTTP.AdminAddr)
	str("JC_DB_DSN", &c.Database.DSN)
	str("JC_CREDENTIAL_PEPPER", &c.Security.CredentialPepper)
	str("JC_DEFAULT_TIMEZONE", &c.Scheduler.DefaultTimezone)
	str("JC_LOG_LEVEL", &c.Log.Level)
	str("JC_LOG_FORMAT", &c.Log.Format)
	str("JC_ARCHIVE_DIR", &c.Archive.Dir)
	if v, ok := lookup("JC_ADMIN_CLIENT_SECRETS"); ok && v != "" {
		// 格式：client:secret[,client:secret]；环境变量登记的客户端权限上限为 ["*"]，
		// 需要更细的上限时使用 YAML。
		if c.Security.AdminClients == nil {
			c.Security.AdminClients = map[string]AdminClient{}
		}
		for _, pair := range strings.Split(v, ",") {
			id, secret, ok := strings.Cut(strings.TrimSpace(pair), ":")
			if !ok || id == "" || secret == "" {
				return fmt.Errorf("JC_ADMIN_CLIENT_SECRETS: invalid entry %q (want client:secret)", pair)
			}
			ac := c.Security.AdminClients[id]
			ac.Secret = secret
			if len(ac.MaxPerms) == 0 {
				ac.MaxPerms = []string{"*"}
			}
			c.Security.AdminClients[id] = ac
		}
	}
	if v, ok := lookup("JC_WS_MAX_MESSAGE_BYTES"); ok && v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("JC_WS_MAX_MESSAGE_BYTES: %w", err)
		}
		c.WebSocket.MaxMessageBytes = n
	}
	return nil
}

func (c *Config) Validate() error {
	var errs []error
	if c.Database.DSN == "" {
		errs = append(errs, errors.New("database.dsn (JC_DB_DSN) is required"))
	}
	if len(c.Security.CredentialPepper) < 16 {
		errs = append(errs, errors.New("security.credential_pepper (JC_CREDENTIAL_PEPPER) must be at least 16 bytes"))
	}
	for id, ac := range c.Security.AdminClients {
		if len(ac.Secret) < 16 {
			errs = append(errs, fmt.Errorf("security.admin_clients.%s.secret must be at least 16 bytes", id))
		}
	}
	in := c.Instance
	if in.HeartbeatInterval <= 0 || in.SuspectAfter <= in.HeartbeatInterval || in.OfflineAfter <= 0 || in.SweepInterval <= 0 {
		errs = append(errs, errors.New("instance: require heartbeat_interval>0, suspect_after>heartbeat_interval, offline_after>0, sweep_interval>0"))
	}
	if c.WebSocket.MaxMessageBytes < 1024 {
		errs = append(errs, errors.New("websocket.max_message_bytes must be >= 1024"))
	}
	return errors.Join(errs...)
}
