package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func validEnv() map[string]string {
	return map[string]string{
		"APP_ENV":       "development",
		"DATABASE_URL":  "postgres://znero_app:s3cretPASS@localhost:5432/znero_dev?sslmode=disable",
		"KAFKA_BROKERS": "localhost:9092",
	}
}

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse(env(validEnv()))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("default host = %q, want loopback 127.0.0.1", cfg.Server.Host)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("default port = %d, want 8080", cfg.Server.Port)
	}
	if cfg.Server.Addr() != "127.0.0.1:8080" {
		t.Errorf("Addr() = %q", cfg.Server.Addr())
	}
	if cfg.Log.Format != "text" {
		t.Errorf("dev log format = %q, want text", cfg.Log.Format)
	}
	if cfg.Kafka.TestTopic != "znero.dev.test" || !cfg.Kafka.ConsumerEnabled {
		t.Errorf("unexpected kafka defaults: %+v", cfg.Kafka)
	}
	if cfg.ReadyCheckTimeout != 2*time.Second {
		t.Errorf("ReadyCheckTimeout = %v", cfg.ReadyCheckTimeout)
	}
	if len(cfg.Server.CORSOrigins) != 0 {
		t.Errorf("CORS must default to no origins, got %v", cfg.Server.CORSOrigins)
	}
	if !cfg.IsDevelopment() {
		t.Error("IsDevelopment() should be true")
	}
}

func TestParseProductionDefaultsToJSONLogs(t *testing.T) {
	e := validEnv()
	e["APP_ENV"] = "production"
	cfg, err := Parse(env(e))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.Format != "json" {
		t.Errorf("production log format = %q, want json", cfg.Log.Format)
	}
	if cfg.IsDevelopment() {
		t.Error("IsDevelopment() must be false in production")
	}
}

func TestParseOverrides(t *testing.T) {
	e := validEnv()
	e["APP_PORT"] = "9090"
	e["APP_HOST"] = "127.0.0.2"
	e["SHUTDOWN_TIMEOUT"] = "3s"
	e["KAFKA_BROKERS"] = "a:9092, b:9093"
	e["CORS_ALLOWED_ORIGINS"] = "http://localhost:5173, https://app.example.com/"
	e["KAFKA_CONSUMER_ENABLED"] = "false"
	e["LOG_LEVEL"] = "DEBUG"
	cfg, err := Parse(env(e))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 9090 || cfg.Server.Host != "127.0.0.2" || cfg.Server.ShutdownTimeout != 3*time.Second {
		t.Errorf("server overrides not applied: %+v", cfg.Server)
	}
	if got := strings.Join(cfg.Kafka.Brokers, "|"); got != "a:9092|b:9093" {
		t.Errorf("brokers = %q", got)
	}
	if got := strings.Join(cfg.Server.CORSOrigins, "|"); got != "http://localhost:5173|https://app.example.com" {
		t.Errorf("origins = %q", got)
	}
	if cfg.Kafka.ConsumerEnabled {
		t.Error("consumer should be disabled")
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("level = %q", cfg.Log.Level)
	}
}

func TestParseValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(map[string]string)
		wantMsg string
	}{
		{"missing env", func(m map[string]string) { delete(m, "APP_ENV") }, "APP_ENV is required"},
		{"bad env", func(m map[string]string) { m["APP_ENV"] = "staging" }, "APP_ENV"},
		{"missing database url", func(m map[string]string) { delete(m, "DATABASE_URL") }, "DATABASE_URL is required"},
		{"database url wrong scheme", func(m map[string]string) { m["DATABASE_URL"] = "mysql://u:p@h/db" }, "DATABASE_URL must be a URL"},
		{"missing brokers", func(m map[string]string) { delete(m, "KAFKA_BROKERS") }, "KAFKA_BROKERS is required"},
		{"broker without port", func(m map[string]string) { m["KAFKA_BROKERS"] = "localhost" }, "not host:port"},
		{"broker bad port", func(m map[string]string) { m["KAFKA_BROKERS"] = "localhost:99999" }, "invalid port"},
		{"port not number", func(m map[string]string) { m["APP_PORT"] = "abc" }, "APP_PORT"},
		{"port out of range", func(m map[string]string) { m["APP_PORT"] = "70000" }, "out of range"},
		{"bad duration", func(m map[string]string) { m["SHUTDOWN_TIMEOUT"] = "soon" }, "SHUTDOWN_TIMEOUT"},
		{"negative duration", func(m map[string]string) { m["HTTP_READ_TIMEOUT"] = "-1s" }, "must be positive"},
		{"bad bool", func(m map[string]string) { m["KAFKA_CONSUMER_ENABLED"] = "maybe" }, "not a boolean"},
		{"bad log level", func(m map[string]string) { m["LOG_LEVEL"] = "loud" }, "LOG_LEVEL"},
		{"bad log format", func(m map[string]string) { m["LOG_FORMAT"] = "xml" }, "LOG_FORMAT"},
		{"wildcard cors", func(m map[string]string) { m["CORS_ALLOWED_ORIGINS"] = "*" }, "no wildcards"},
		{"cors with path", func(m map[string]string) { m["CORS_ALLOWED_ORIGINS"] = "http://a.com/x" }, "exact origin"},
		{"min conns > max", func(m map[string]string) { m["DB_MIN_CONNS"] = "20"; m["DB_MAX_CONNS"] = "5" }, "must not exceed"},
		{"bad topic", func(m map[string]string) { m["KAFKA_TEST_TOPIC"] = "bad topic!" }, "KAFKA_TEST_TOPIC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := validEnv()
			tt.mutate(e)
			cfg, err := Parse(env(e))
			if err == nil {
				t.Fatalf("expected error, got config %+v", cfg)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestParseReportsAllErrorsAtOnce(t *testing.T) {
	_, err := Parse(env(map[string]string{"APP_PORT": "x"}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"APP_ENV", "DATABASE_URL", "KAFKA_BROKERS", "APP_PORT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %s: %v", want, err)
		}
	}
}

func TestSecretsNeverLeak(t *testing.T) {
	cfg, err := Parse(env(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if red := cfg.Database.RedactedDatabaseURL(); strings.Contains(red, "s3cretPASS") {
		t.Errorf("redacted URL leaks password: %s", red)
	}
	if v := cfg.LogValue().String(); strings.Contains(v, "s3cretPASS") {
		t.Errorf("LogValue leaks password: %s", v)
	}

	// An invalid DATABASE_URL must not be echoed back in the error either.
	e := validEnv()
	e["DATABASE_URL"] = "mysql://admin:topsecret@host/db"
	_, err = Parse(env(e))
	if err == nil || strings.Contains(err.Error(), "topsecret") {
		t.Errorf("error must exist and must not leak the value: %v", err)
	}
}

func TestFindEnvFileWalksUpwardsAndHonoursExplicitPath(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "backend", "cmd", "api")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(root, ".env")
	if err := os.WriteFile(envPath, []byte("APP_ENV=test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	chdir(t, nested)
	got := findEnvFile("")
	// Compare after resolving symlinks (macOS/Windows temp dirs can be aliased).
	wantResolved, _ := filepath.EvalSymlinks(envPath)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("findEnvFile() = %q, want %q", got, envPath)
	}

	if explicit := findEnvFile(filepath.Join(root, "other.env")); explicit != filepath.Join(root, "other.env") {
		t.Errorf("explicit path not honoured: %q", explicit)
	}
}

func TestFindEnvFileNone(t *testing.T) {
	chdir(t, t.TempDir())
	if got := findEnvFile(""); got != "" {
		// A stray .env in a parent of the temp dir would be a test-environment problem, not a bug.
		t.Skipf("found unrelated .env at %q", got)
	}
}

// chdir changes the working directory for one test (t.Chdir needs Go 1.24+).
func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}
