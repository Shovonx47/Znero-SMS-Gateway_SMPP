package app_test

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/znero-international/znero/backend/internal/app"
	"github.com/znero-international/znero/backend/internal/config"
	"github.com/znero-international/znero/backend/internal/diagnostics"
	"github.com/znero-international/znero/backend/internal/health"
	"github.com/znero-international/znero/backend/internal/system"
	"github.com/znero-international/znero/backend/pkg/logx"
)

type okRepo struct{}

func (okRepo) MigrationState(context.Context) (system.MigrationState, error) {
	return system.MigrationState{Version: 1}, nil
}
func (okRepo) SchemaInfo(context.Context) ([]system.SchemaEntry, error) {
	return []system.SchemaEntry{}, nil
}

type okProbe struct{}

func (okProbe) Name() string               { return "database" }
func (okProbe) Ping(context.Context) error { return nil }

type nopPublisher struct{}

func (nopPublisher) Publish(context.Context, string, []byte, []byte) error { return nil }

func build(t *testing.T, withDiagnostics bool) *fiber.App {
	t.Helper()
	cfg := &config.Config{Server: config.ServerConfig{
		BodyLimitBytes: 4096, RateLimitMax: 0, RateLimitWindow: time.Minute,
	}}
	logger, _ := logx.New(io.Discard, "error", "json")
	mods := app.Modules{
		Health: health.NewHandler(health.NewService("t", time.Second, logger, okProbe{})),
		System: system.NewHandler(system.NewService(okRepo{}, "t", "test")),
	}
	if withDiagnostics {
		mods.Diagnostics = diagnostics.NewHandler(diagnostics.NewService(nopPublisher{}, "topic", time.Second, logger))
	}
	return app.NewHTTP(cfg, logger, mods)
}

func status(t *testing.T, a *fiber.App, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, string(b)
}

func TestRoutesAreVersionedAndHealthIsUnversioned(t *testing.T) {
	a := build(t, false)
	for path, want := range map[string]int{
		"/healthz":            200,
		"/readyz":             200,
		"/api/v1/system/info": 200,
		"/system/info":        404, // business endpoints must live under /api/v1
		"/api/v2/system/info": 404,
	} {
		if got, body := status(t, a, "GET", path, ""); got != want {
			t.Errorf("GET %s = %d, want %d (%s)", path, got, want, body)
		}
	}
}

func TestDevDiagnosticsRouteIsDefaultDeny(t *testing.T) {
	const path = "/api/v1/dev/kafka/test-message"
	if got, _ := status(t, build(t, false), "POST", path, `{"message":"x"}`); got != 404 {
		t.Errorf("without the diagnostics module the route must not exist, got %d", got)
	}
	if got, body := status(t, build(t, true), "POST", path, `{"message":"x"}`); got != 202 {
		t.Errorf("with the module enabled the route should work, got %d %s", got, body)
	}
}

func TestOnlyAllowListedMethodsReachRoutes(t *testing.T) {
	a := build(t, true)
	// Methods not in allowedMethods (e.g. TRACE) get 405 from the method filter.
	for _, m := range []string{"TRACE", "CONNECT"} {
		if got, _ := status(t, a, m, "/api/v1/system/info", ""); got != 405 {
			t.Errorf("%s = %d, want 405", m, got)
		}
	}
	// PUT/PATCH/DELETE are allowed by the server filter, but /system/info only registers GET,
	// so Fiber responds with 405 for unhandled methods on that route.
	for _, m := range []string{"PUT", "PATCH", "DELETE"} {
		if got, _ := status(t, a, m, "/api/v1/system/info", ""); got != 405 {
			t.Errorf("%s = %d, want 405", m, got)
		}
	}
}
