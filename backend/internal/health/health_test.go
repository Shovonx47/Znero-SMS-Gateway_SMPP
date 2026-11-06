package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/znero-international/znero/backend/internal/health"
	"github.com/znero-international/znero/backend/pkg/httpx"
	"github.com/znero-international/znero/backend/pkg/logx"
)

type fakeProbe struct {
	name string
	ping func(ctx context.Context) error
}

func (f fakeProbe) Name() string                   { return f.name }
func (f fakeProbe) Ping(ctx context.Context) error { return f.ping(ctx) }

func up(name string) fakeProbe {
	return fakeProbe{name: name, ping: func(context.Context) error { return nil }}
}

func down(name string, err error) fakeProbe {
	return fakeProbe{name: name, ping: func(context.Context) error { return err }}
}

func newApp(t *testing.T, logs io.Writer, timeout time.Duration, probes ...health.Probe) *fiber.App {
	t.Helper()
	logger, err := logx.New(logs, "debug", "json")
	if err != nil {
		t.Fatal(err)
	}
	app := httpx.NewApp(httpx.AppConfig{
		AllowedMethods: []string{"GET", "HEAD"}, BodyLimitBytes: 1024,
		QuietPaths: []string{"/healthz", "/readyz"},
	}, logger)
	health.NewHandler(health.NewService("test-1.0", timeout, logger, probes...)).Register(app)
	return app
}

func get(t *testing.T, app *fiber.App, path string) (*http.Response, []byte) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest("GET", path, nil), -1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, b
}

type checks map[string]struct {
	Status    string `json:"status"`
	LatencyMS int64  `json:"latency_ms"`
	Reason    string `json:"reason"`
}

func TestHealthzIsAliveEvenWhenDependenciesAreDown(t *testing.T) {
	app := newApp(t, io.Discard, time.Second,
		down("database", errors.New("db gone")), down("kafka", errors.New("kafka gone")))
	resp, body := get(t, app, "/healthz")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	var got struct {
		Success bool `json:"success"`
		Data    struct {
			Status  string `json:"status"`
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Success || got.Data.Status != "ok" || got.Data.Version != "test-1.0" {
		t.Errorf("unexpected body: %s", body)
	}
	if resp.Header.Get("X-Request-ID") == "" {
		t.Error("request id header missing")
	}
}

func TestReadyzAllUp(t *testing.T) {
	app := newApp(t, io.Discard, time.Second, up("database"), up("kafka"))
	resp, body := get(t, app, "/readyz")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	var got struct {
		Success bool `json:"success"`
		Data    struct {
			Status string `json:"status"`
			Checks checks `json:"checks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Success || got.Data.Status != "up" {
		t.Errorf("unexpected body: %s", body)
	}
	for _, name := range []string{"database", "kafka"} {
		if got.Data.Checks[name].Status != "up" {
			t.Errorf("%s should be up: %s", name, body)
		}
	}
}

// The key requirement: Kafka down => readyz reports it separately, database stays "up".
func TestReadyzKafkaDownIsReportedSeparately(t *testing.T) {
	secret := errors.New("dial tcp 127.0.0.1:9092: connectex: connection refused (secret-host)")
	app := newApp(t, io.Discard, time.Second, up("database"), down("kafka", secret))
	resp, body := get(t, app, "/readyz")
	if resp.StatusCode != 503 {
		t.Fatalf("status = %d, want 503; body %s", resp.StatusCode, body)
	}
	var got struct {
		Success bool `json:"success"`
		Error   struct {
			Code    string `json:"code"`
			Details struct {
				Status string `json:"status"`
				Checks checks `json:"checks"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Success || got.Error.Code != "not_ready" || got.Error.Details.Status != "down" {
		t.Errorf("unexpected body: %s", body)
	}
	if got.Error.Details.Checks["database"].Status != "up" {
		t.Errorf("database must be reported up: %s", body)
	}
	kafka := got.Error.Details.Checks["kafka"]
	if kafka.Status != "down" || kafka.Reason != "unreachable" {
		t.Errorf("kafka must be down/unreachable: %s", body)
	}
	if strings.Contains(string(body), "secret-host") || strings.Contains(string(body), "connectex") {
		t.Errorf("internal error text leaked: %s", body)
	}
}

func TestReadyzDatabaseDown(t *testing.T) {
	app := newApp(t, io.Discard, time.Second, down("database", errors.New("boom")), up("kafka"))
	resp, body := get(t, app, "/readyz")
	if resp.StatusCode != 503 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got struct {
		Error struct {
			Details struct {
				Checks checks `json:"checks"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &got)
	if got.Error.Details.Checks["database"].Status != "down" || got.Error.Details.Checks["kafka"].Status != "up" {
		t.Errorf("unexpected checks: %s", body)
	}
}

func TestReadyzSlowDependencyTimesOutInsteadOfHanging(t *testing.T) {
	hang := fakeProbe{name: "kafka", ping: func(ctx context.Context) error {
		<-ctx.Done() // honours cancellation like a real client
		return ctx.Err()
	}}
	app := newApp(t, io.Discard, 150*time.Millisecond, up("database"), hang)

	start := time.Now()
	resp, body := get(t, app, "/readyz")
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("readyz took %v; the per-check timeout is not enforced", elapsed)
	}
	if resp.StatusCode != 503 || !strings.Contains(string(body), `"reason":"timeout"`) {
		t.Errorf("want 503 with reason timeout, got %d %s", resp.StatusCode, body)
	}
}

func TestReadyzChecksRunConcurrently(t *testing.T) {
	var running, maxRunning int32
	slow := func(name string) fakeProbe {
		return fakeProbe{name: name, ping: func(ctx context.Context) error {
			n := atomic.AddInt32(&running, 1)
			for {
				m := atomic.LoadInt32(&maxRunning)
				if n <= m || atomic.CompareAndSwapInt32(&maxRunning, m, n) {
					break
				}
			}
			time.Sleep(100 * time.Millisecond)
			atomic.AddInt32(&running, -1)
			return nil
		}}
	}
	app := newApp(t, io.Discard, time.Second, slow("database"), slow("kafka"))
	get(t, app, "/readyz")
	if atomic.LoadInt32(&maxRunning) < 2 {
		t.Error("probes should run in parallel so one slow dependency does not delay the others")
	}
}

func TestStateTransitionsAreLoggedOnceWithRealError(t *testing.T) {
	var logs strings.Builder
	var broken atomic.Bool
	broken.Store(true)
	kafka := fakeProbe{name: "kafka", ping: func(context.Context) error {
		if broken.Load() {
			return errors.New("connection refused")
		}
		return nil
	}}
	app := newApp(t, &logs, time.Second, up("database"), kafka)

	for i := 0; i < 3; i++ {
		get(t, app, "/readyz")
	}
	if n := strings.Count(logs.String(), "dependency is down"); n != 1 {
		t.Errorf("down transition should be logged once, got %d:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "connection refused") {
		t.Error("real error should be in the server log")
	}

	broken.Store(false)
	get(t, app, "/readyz")
	if !strings.Contains(logs.String(), "dependency recovered") {
		t.Errorf("recovery not logged:\n%s", logs.String())
	}
}

func TestHealthEndpointsRejectUnsupportedMethods(t *testing.T) {
	app := newApp(t, io.Discard, time.Second, up("database"))
	req := httptest.NewRequest("DELETE", "/healthz", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 405 {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}
