// Package config loads and validates the application configuration from environment variables.
// A .env file is read for local development; real environment variables always take precedence.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Environment names.
const (
	EnvDevelopment = "development"
	EnvTest        = "test"
	EnvProduction  = "production"
)

type Config struct {
	Env      string
	Server   ServerConfig
	Log      LogConfig
	Database DatabaseConfig
	Kafka    KafkaConfig
	Auth     AuthConfig
	Secrets  SecretsConfig
	// ReadyCheckTimeout bounds each dependency check performed by /readyz.
	ReadyCheckTimeout time.Duration
}

type SecretsConfig struct {
	EncryptionKey string
	KeyID         string
	PreviousKeys  string
}

type ServerConfig struct {
	Host            string
	Port            int
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	BodyLimitBytes  int
	RateLimitMax    int
	RateLimitWindow time.Duration
	CORSOrigins     []string
}

type LogConfig struct {
	Level  string
	Format string
}

type DatabaseConfig struct {
	URL               string
	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
	ConnectTimeout    time.Duration
}

type KafkaConfig struct {
	Brokers         []string
	ClientID        string
	DialTimeout     time.Duration
	RequestTimeout  time.Duration
	TestTopic       string
	ConsumerGroup   string
	ConsumerEnabled bool
}

type AuthConfig struct {
	JWTAccessSecret   string
	JWTAccessDuration time.Duration
	SessionCookieName string
	MFAEncryptionKey  string
}

// Addr returns host:port for the HTTP listener.
func (s ServerConfig) Addr() string {
	return net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
}

// IsDevelopment reports whether development-only features may be enabled.
func (c *Config) IsDevelopment() bool { return c.Env == EnvDevelopment }

// RedactedDatabaseURL returns the database URL with the password masked, safe for logs.
func (d DatabaseConfig) RedactedDatabaseURL() string {
	u, err := url.Parse(d.URL)
	if err != nil {
		return "<unparseable>"
	}
	return u.Redacted()
}

// LogValue implements slog.LogValuer so that logging a Config can never leak secrets.
func (c *Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("env", c.Env),
		slog.String("listen", c.Server.Addr()),
		slog.String("log_level", c.Log.Level),
		slog.String("log_format", c.Log.Format),
		slog.String("database", c.Database.RedactedDatabaseURL()),
		slog.String("kafka_brokers", strings.Join(c.Kafka.Brokers, ",")),
		slog.String("kafka_test_topic", c.Kafka.TestTopic),
		slog.Bool("kafka_consumer_enabled", c.Kafka.ConsumerEnabled),
	)
}

// Load reads the .env file (if one can be found) and then parses the process environment.
func Load() (*Config, error) {
	if path := findEnvFile(os.Getenv("ENV_FILE")); path != "" {
		// godotenv.Load never overrides variables that are already set.
		if err := godotenv.Load(path); err != nil {
			return nil, fmt.Errorf("read env file %q: %w", path, err)
		}
	}
	return Parse(os.Getenv)
}

// findEnvFile returns an explicit path if given, otherwise the nearest .env in the current
// directory or one of its parents (the repo root when running from /backend). Returns "" if none.
func findEnvFile(explicit string) string {
	if explicit != "" {
		return explicit
	}
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for i := 0; i < 6; i++ {
		candidate := filepath.Join(dir, ".env")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// Parse builds a Config from getenv. It reports every problem at once instead of stopping at the first.
func Parse(getenv func(string) string) (*Config, error) {
	p := &parser{getenv: getenv}

	cfg := &Config{
		Env: p.oneOf("APP_ENV", "", EnvDevelopment, EnvTest, EnvProduction),
		Server: ServerConfig{
			Host:            p.str("APP_HOST", "127.0.0.1"),
			Port:            p.intRange("APP_PORT", 8080, 1, 65535),
			ReadTimeout:     p.duration("HTTP_READ_TIMEOUT", 10*time.Second),
			WriteTimeout:    p.duration("HTTP_WRITE_TIMEOUT", 15*time.Second),
			IdleTimeout:     p.duration("HTTP_IDLE_TIMEOUT", 60*time.Second),
			ShutdownTimeout: p.duration("SHUTDOWN_TIMEOUT", 15*time.Second),
			BodyLimitBytes:  p.intRange("HTTP_BODY_LIMIT_BYTES", 1<<20, 1, 64<<20),
			RateLimitMax:    p.intRange("RATE_LIMIT_MAX", 1000, 0, 1_000_000),
			RateLimitWindow: p.duration("RATE_LIMIT_WINDOW", time.Minute),
			CORSOrigins:     p.origins("CORS_ALLOWED_ORIGINS"),
		},
		Log: LogConfig{
			Level:  p.oneOf("LOG_LEVEL", "info", "debug", "info", "warn", "error"),
			Format: "",
		},
		Database: DatabaseConfig{
			URL:               p.databaseURL("DATABASE_URL"),
			MaxConns:          int32(p.intRange("DB_MAX_CONNS", 10, 1, 500)),
			MinConns:          int32(p.intRange("DB_MIN_CONNS", 0, 0, 500)),
			MaxConnLifetime:   p.duration("DB_MAX_CONN_LIFETIME", time.Hour),
			MaxConnIdleTime:   p.duration("DB_MAX_CONN_IDLE_TIME", 30*time.Minute),
			HealthCheckPeriod: p.duration("DB_HEALTH_CHECK_PERIOD", time.Minute),
			ConnectTimeout:    p.duration("DB_CONNECT_TIMEOUT", 5*time.Second),
		},
		Kafka: KafkaConfig{
			Brokers:         p.brokers("KAFKA_BROKERS"),
			ClientID:        p.str("KAFKA_CLIENT_ID", "znero-backend"),
			DialTimeout:     p.duration("KAFKA_DIAL_TIMEOUT", 5*time.Second),
			RequestTimeout:  p.duration("KAFKA_REQUEST_TIMEOUT", 10*time.Second),
			TestTopic:       p.topic("KAFKA_TEST_TOPIC", "znero.dev.test"),
			ConsumerGroup:   p.str("KAFKA_CONSUMER_GROUP", "znero-backend-dev"),
			ConsumerEnabled: p.boolean("KAFKA_CONSUMER_ENABLED", true),
		},
		Auth: AuthConfig{
			JWTAccessSecret:   p.str("JWT_ACCESS_SECRET", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"),
			JWTAccessDuration: p.duration("JWT_ACCESS_DURATION", 15*time.Minute),
			SessionCookieName: p.str("SESSION_COOKIE_NAME", "znero_refresh_token"),
			MFAEncryptionKey:  p.str("MFA_ENCRYPTION_KEY", "5ef83b45188a60122fae1100d5bdb36943939caef953ddfd26f217f2aa929e89"),
		},
		Secrets: SecretsConfig{
			EncryptionKey: p.str("SECRETS_ENCRYPTION_KEY", ""),
			KeyID:         p.str("SECRETS_KEY_ID", "v1"),
			PreviousKeys:  p.str("SECRETS_ENCRYPTION_KEYS_PREVIOUS", ""),
		},
		ReadyCheckTimeout: p.duration("READY_CHECK_TIMEOUT", 2*time.Second),
	}

	// Log format default depends on the environment: JSON in production, text otherwise.
	defaultFormat := "text"
	if cfg.Env == EnvProduction {
		defaultFormat = "json"
	}
	cfg.Log.Format = p.oneOf("LOG_FORMAT", defaultFormat, "json", "text")

	if len(cfg.Auth.JWTAccessSecret) < 32 {
		p.errs = append(p.errs, errors.New("JWT_ACCESS_SECRET must be at least 32 characters"))
	}
	if len(cfg.Auth.MFAEncryptionKey) != 64 {
		p.errs = append(p.errs, errors.New("MFA_ENCRYPTION_KEY must be exactly 64 hex characters (32 bytes)"))
	}

	if cfg.Database.MinConns > cfg.Database.MaxConns {
		p.errs = append(p.errs, fmt.Errorf("DB_MIN_CONNS (%d) must not exceed DB_MAX_CONNS (%d)", cfg.Database.MinConns, cfg.Database.MaxConns))
	}
	if cfg.Server.RateLimitMax > 0 && cfg.Server.RateLimitWindow <= 0 {
		p.errs = append(p.errs, errors.New("RATE_LIMIT_WINDOW must be positive when RATE_LIMIT_MAX is set"))
	}
	if cfg.Server.Host == "" {
		p.errs = append(p.errs, errors.New("APP_HOST must not be empty"))
	}

	if err := errors.Join(p.errs...); err != nil {
		return nil, fmt.Errorf("invalid configuration:\n%w", err)
	}
	return cfg, nil
}

// parser accumulates validation errors while reading values.
type parser struct {
	getenv func(string) string
	errs   []error
}

func (p *parser) fail(format string, args ...any) {
	p.errs = append(p.errs, fmt.Errorf(format, args...))
}

func (p *parser) raw(key string) string { return strings.TrimSpace(p.getenv(key)) }

func (p *parser) str(key, def string) string {
	if v := p.raw(key); v != "" {
		return v
	}
	return def
}

func (p *parser) oneOf(key, def string, allowed ...string) string {
	v := p.raw(key)
	if v == "" {
		if def == "" {
			p.fail("%s is required (one of: %s)", key, strings.Join(allowed, ", "))
		}
		return def
	}
	v = strings.ToLower(v)
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	p.fail("%s=%q is invalid (one of: %s)", key, v, strings.Join(allowed, ", "))
	return def
}

func (p *parser) intRange(key string, def, minV, maxV int) int {
	v := p.raw(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		p.fail("%s=%q is not an integer", key, v)
		return def
	}
	if n < minV || n > maxV {
		p.fail("%s=%d is out of range (%d..%d)", key, n, minV, maxV)
		return def
	}
	return n
}

func (p *parser) duration(key string, def time.Duration) time.Duration {
	v := p.raw(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		p.fail("%s=%q is not a valid duration (examples: 500ms, 5s, 2m)", key, v)
		return def
	}
	if d <= 0 {
		p.fail("%s=%q must be positive", key, v)
		return def
	}
	return d
}

func (p *parser) boolean(key string, def bool) bool {
	v := p.raw(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		p.fail("%s=%q is not a boolean (true/false)", key, v)
		return def
	}
	return b
}

// databaseURL requires a postgres:// URL. The value is never echoed in error messages.
func (p *parser) databaseURL(key string) string {
	v := p.raw(key)
	if v == "" {
		p.fail("%s is required", key)
		return ""
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || strings.Trim(u.Path, "/") == "" {
		p.fail("%s must be a URL like postgres://user:password@host:5432/dbname (value hidden)", key)
		return ""
	}
	return v
}

func (p *parser) brokers(key string) []string {
	v := p.raw(key)
	if v == "" {
		p.fail("%s is required (comma-separated host:port list)", key)
		return nil
	}
	var out []string
	for _, b := range strings.Split(v, ",") {
		b = strings.TrimSpace(b)
		host, port, err := net.SplitHostPort(b)
		if err != nil || host == "" {
			p.fail("%s entry %q is not host:port", key, b)
			continue
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			p.fail("%s entry %q has an invalid port", key, b)
			continue
		}
		out = append(out, b)
	}
	return out
}

// topic validates a Kafka topic name (letters, digits, '.', '_', '-', max 249 chars).
func (p *parser) topic(key, def string) string {
	v := p.str(key, def)
	if len(v) > 249 || v == "." || v == ".." {
		p.fail("%s=%q is not a valid Kafka topic name", key, v)
		return def
	}
	for _, r := range v {
		ok := r == '.' || r == '_' || r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			p.fail("%s=%q contains characters Kafka does not allow", key, v)
			return def
		}
	}
	return v
}

// origins parses an exact-match CORS allow-list. Wildcards are rejected on purpose.
func (p *parser) origins(key string) []string {
	v := p.raw(key)
	if v == "" {
		return nil
	}
	var out []string
	for _, o := range strings.Split(v, ",") {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		u, err := url.Parse(o)
		if o == "*" || strings.Contains(o, "*") || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") {
			p.fail("%s entry %q must be an exact origin like http://localhost:5173 (no wildcards)", key, o)
			continue
		}
		out = append(out, u.Scheme+"://"+u.Host)
	}
	return out
}
