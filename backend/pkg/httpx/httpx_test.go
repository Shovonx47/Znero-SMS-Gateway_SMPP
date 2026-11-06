package httpx_test

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/znero-international/znero/backend/pkg/httpx"
	"github.com/znero-international/znero/backend/pkg/logx"
)

func testApp(t *testing.T, logOut io.Writer, mutate func(*fiber.App)) *fiber.App {
	t.Helper()
	logger, err := logx.New(logOut, "debug", "json")
	if err != nil {
		t.Fatal(err)
	}
	app := httpx.NewApp(httpx.AppConfig{
		Name:           "test",
		BodyLimitBytes: 1024,
		AllowedMethods: []string{"GET", "HEAD", "POST", "OPTIONS"},
		QuietPaths:     []string{"/quiet"},
		RateLimitMax:   0,
	}, logger)
	if mutate != nil {
		mutate(app)
	}
	return app
}

func do(t *testing.T, app *fiber.App, method, path string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, body
}

func TestSuccessFormat(t *testing.T) {
	app := testApp(t, io.Discard, func(a *fiber.App) {
		a.Get("/ok", func(c *fiber.Ctx) error { return httpx.OK(c, map[string]string{"hello": "world"}) })
	})
	resp, body := do(t, app, "GET", "/ok", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got struct {
		Success bool              `json:"success"`
		Data    map[string]string `json:"data"`
		Meta    struct {
			RequestID string `json:"request_id"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, body)
	}
	if !got.Success || got.Data["hello"] != "world" {
		t.Errorf("unexpected body: %s", body)
	}
	if got.Meta.RequestID == "" || got.Meta.RequestID != resp.Header.Get("X-Request-ID") {
		t.Errorf("meta.request_id %q must equal X-Request-ID header %q", got.Meta.RequestID, resp.Header.Get("X-Request-ID"))
	}
}

type errBody struct {
	Success bool `json:"success"`
	Error   struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Details json.RawMessage `json:"details"`
	} `json:"error"`
	Meta struct {
		RequestID string `json:"request_id"`
	} `json:"meta"`
}

func decodeErr(t *testing.T, body []byte) errBody {
	t.Helper()
	var e errBody
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("not JSON: %v: %s", err, body)
	}
	return e
}

func TestErrorFormat(t *testing.T) {
	secret := errors.New("pq: password authentication failed for user znero_app at 10.1.2.3")
	app := testApp(t, io.Discard, func(a *fiber.App) {
		a.Get("/validation", func(c *fiber.Ctx) error {
			return httpx.ValidationFailed("Invalid input.", map[string]string{"field": "too short"})
		})
		a.Get("/forbidden", func(c *fiber.Ctx) error { return httpx.Forbidden("No access.") })
		a.Get("/internal", func(c *fiber.Ctx) error { return httpx.Internal(secret) })
		a.Get("/plain", func(c *fiber.Ctx) error { return secret })
		a.Get("/wrapped", func(c *fiber.Ctx) error { return errors.Join(errors.New("ctx"), httpx.NotFound("Nope.")) })
	})

	tests := []struct {
		path       string
		wantStatus int
		wantCode   string
	}{
		{"/validation", 422, "validation_failed"},
		{"/forbidden", 403, "forbidden"},
		{"/internal", 500, "internal_error"},
		{"/plain", 500, "internal_error"},
		{"/wrapped", 404, "not_found"},
		{"/does-not-exist", 404, "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			resp, body := do(t, app, "GET", tt.path, nil)
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", resp.StatusCode, tt.wantStatus, body)
			}
			e := decodeErr(t, body)
			if e.Success || e.Error.Code != tt.wantCode || e.Error.Message == "" {
				t.Errorf("unexpected error body: %s", body)
			}
			if e.Meta.RequestID == "" {
				t.Error("error response must carry meta.request_id")
			}
			if strings.Contains(string(body), "password") || strings.Contains(string(body), "10.1.2.3") {
				t.Errorf("internal detail leaked to client: %s", body)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("Content-Type = %q", ct)
			}
		})
	}
}

func TestValidationDetailsAreReturned(t *testing.T) {
	app := testApp(t, io.Discard, func(a *fiber.App) {
		a.Get("/v", func(c *fiber.Ctx) error {
			return httpx.ValidationFailed("Invalid input.", map[string]string{"field": "too short"})
		})
	})
	_, body := do(t, app, "GET", "/v", nil)
	e := decodeErr(t, body)
	if !strings.Contains(string(e.Error.Details), "too short") {
		t.Errorf("details missing: %s", body)
	}
}

func TestPanicIsRecoveredAsStandardError(t *testing.T) {
	var logs strings.Builder
	app := testApp(t, &logs, func(a *fiber.App) {
		a.Get("/boom", func(c *fiber.Ctx) error { panic("kaboom with secret-token") })
	})
	resp, body := do(t, app, "GET", "/boom", nil)
	if resp.StatusCode != 500 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if e := decodeErr(t, body); e.Error.Code != "internal_error" {
		t.Errorf("code = %s", e.Error.Code)
	}
	if strings.Contains(string(body), "secret-token") {
		t.Error("panic value leaked to client")
	}
	if !strings.Contains(logs.String(), "panic recovered") {
		t.Error("panic should be logged server-side")
	}
}

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestRequestID(t *testing.T) {
	app := testApp(t, io.Discard, func(a *fiber.App) {
		a.Get("/x", func(c *fiber.Ctx) error { return httpx.OK(c, nil) })
	})

	t.Run("generated when absent", func(t *testing.T) {
		resp, _ := do(t, app, "GET", "/x", nil)
		if id := resp.Header.Get("X-Request-ID"); !hex32.MatchString(id) {
			t.Errorf("generated id = %q", id)
		}
	})
	t.Run("two requests get different ids", func(t *testing.T) {
		a, _ := do(t, app, "GET", "/x", nil)
		b, _ := do(t, app, "GET", "/x", nil)
		if a.Header.Get("X-Request-ID") == b.Header.Get("X-Request-ID") {
			t.Error("ids must be unique")
		}
	})
	t.Run("valid inbound id is kept", func(t *testing.T) {
		resp, _ := do(t, app, "GET", "/x", map[string]string{"X-Request-ID": "trace-abc.123_XYZ"})
		if got := resp.Header.Get("X-Request-ID"); got != "trace-abc.123_XYZ" {
			t.Errorf("id = %q", got)
		}
	})
	t.Run("malicious inbound id is replaced", func(t *testing.T) {
		for _, bad := range []string{"short", "has spaces in it!!", strings.Repeat("a", 65), "id\"};{injected:1}"} {
			resp, _ := do(t, app, "GET", "/x", map[string]string{"X-Request-ID": bad})
			if got := resp.Header.Get("X-Request-ID"); got == bad || !hex32.MatchString(got) {
				t.Errorf("bad id %q was not replaced (got %q)", bad, got)
			}
		}
	})
}

func TestRequestIDInEveryLogLine(t *testing.T) {
	var logs strings.Builder
	logger, _ := logx.New(&logs, "debug", "json")
	app := httpx.NewApp(httpx.AppConfig{
		AllowedMethods: []string{"GET"}, BodyLimitBytes: 1024, QuietPaths: []string{"/quiet"},
	}, logger)
	app.Get("/work", func(c *fiber.Ctx) error {
		logger.InfoContext(c.UserContext(), "inside handler", slog.String("k", "v"))
		return httpx.OK(c, nil)
	})
	app.Get("/quiet", func(c *fiber.Ctx) error { return httpx.OK(c, nil) })

	resp, _ := do(t, app, "GET", "/work", nil)
	do(t, app, "GET", "/quiet", nil)
	id := resp.Header.Get("X-Request-ID")

	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected handler + 2 access log lines, got %d: %s", len(lines), logs.String())
	}
	for _, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		if rec["request_id"] == nil || rec["request_id"] == "" {
			t.Errorf("log line without request_id: %s", line)
		}
	}
	if !strings.Contains(lines[0], id) || !strings.Contains(lines[0], "inside handler") {
		t.Errorf("handler log line should carry the response's request id %s: %s", id, lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], `"level":"DEBUG"`) {
		t.Errorf("quiet path should log at debug: %s", lines[len(lines)-1])
	}
}

func TestSecurityHeaders(t *testing.T) {
	app := testApp(t, io.Discard, func(a *fiber.App) {
		a.Get("/x", func(c *fiber.Ctx) error { return httpx.OK(c, nil) })
	})
	resp, _ := do(t, app, "GET", "/x", nil)
	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "no-referrer",
		"Cache-Control":           "no-store",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
	}
	for k, v := range want {
		if got := resp.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if resp.Header.Get("Permissions-Policy") == "" {
		t.Error("Permissions-Policy missing")
	}
	// Security headers must also be present on error responses.
	resp, _ = do(t, app, "GET", "/missing", nil)
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("security headers missing on 404")
	}
}

func TestMethodAllowListIsDefaultDeny(t *testing.T) {
	app := testApp(t, io.Discard, func(a *fiber.App) {
		a.All("/x", func(c *fiber.Ctx) error { return httpx.OK(c, nil) })
	})
	for _, m := range []string{"PUT", "DELETE", "PATCH", "TRACE"} {
		resp, body := do(t, app, m, "/x", nil)
		if resp.StatusCode != 405 || decodeErr(t, body).Error.Code != "method_not_allowed" {
			t.Errorf("%s: status %d body %s", m, resp.StatusCode, body)
		}
		if resp.Header.Get("Allow") == "" {
			t.Errorf("%s: Allow header missing", m)
		}
	}
	if resp, _ := do(t, app, "GET", "/x", nil); resp.StatusCode != 200 {
		t.Errorf("GET should be allowed, got %d", resp.StatusCode)
	}
}

func TestRateLimiterReturnsStandardError(t *testing.T) {
	logger, _ := logx.New(io.Discard, "error", "json")
	app := httpx.NewApp(httpx.AppConfig{
		AllowedMethods: []string{"GET"}, BodyLimitBytes: 1024,
		RateLimitMax: 2, RateLimitWindow: 60e9, QuietPaths: []string{"/healthz"},
	}, logger)
	app.Get("/x", func(c *fiber.Ctx) error { return httpx.OK(c, nil) })
	app.Get("/healthz", func(c *fiber.Ctx) error { return httpx.OK(c, nil) })

	for i := 0; i < 2; i++ {
		if resp, _ := do(t, app, "GET", "/x", nil); resp.StatusCode != 200 {
			t.Fatalf("request %d: status %d", i, resp.StatusCode)
		}
	}
	resp, body := do(t, app, "GET", "/x", nil)
	if resp.StatusCode != 429 || decodeErr(t, body).Error.Code != "rate_limited" {
		t.Fatalf("expected 429 rate_limited, got %d %s", resp.StatusCode, body)
	}
	for i := 0; i < 5; i++ {
		if resp, _ := do(t, app, "GET", "/healthz", nil); resp.StatusCode != 200 {
			t.Fatalf("health probes must be exempt from rate limiting, got %d", resp.StatusCode)
		}
	}
}

func TestCORSAllowListOnly(t *testing.T) {
	logger, _ := logx.New(io.Discard, "error", "json")
	build := func(origins []string) *fiber.App {
		app := httpx.NewApp(httpx.AppConfig{
			AllowedMethods: []string{"GET", "POST", "OPTIONS"}, BodyLimitBytes: 1024, CORSOrigins: origins,
		}, logger)
		app.Get("/x", func(c *fiber.Ctx) error { return httpx.OK(c, nil) })
		return app
	}

	resp, _ := do(t, build(nil), "GET", "/x", map[string]string{"Origin": "http://evil.example"})
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("no allow-list configured, but ACAO = %q (must never default to *)", got)
	}

	app := build([]string{"http://localhost:5173"})
	resp, _ = do(t, app, "GET", "/x", map[string]string{"Origin": "http://localhost:5173"})
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Errorf("allowed origin: ACAO = %q", got)
	}
	resp, _ = do(t, app, "GET", "/x", map[string]string{"Origin": "http://evil.example"})
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("disallowed origin got ACAO = %q", got)
	}
}

func TestCredentialRateLimiter(t *testing.T) {
	app := testApp(t, io.Discard, func(a *fiber.App) {
		limiter := httpx.CredentialRateLimiter(3, time.Minute)
		a.Post("/secret-endpoint", limiter, func(c *fiber.Ctx) error {
			return httpx.OK(c, map[string]string{"status": "ok"})
		})
	})

	for i := 1; i <= 3; i++ {
		resp, body := do(t, app, "POST", "/secret-endpoint", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d expected 200, got %d (%s)", i, resp.StatusCode, string(body))
		}
	}

	// 4th request must be rate limited (429)
	resp, body := do(t, app, "POST", "/secret-endpoint", nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("request 4 expected 429, got %d (%s)", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "Too many credential operations") {
		t.Fatalf("expected rate limit error message, got %s", string(body))
	}
}

func TestMoneyRateLimiter_Returns429WhenExceeded(t *testing.T) {
	app := testApp(t, io.Discard, func(a *fiber.App) {
		limiter := httpx.MoneyRateLimiter(5, time.Minute)
		a.Post("/api/v1/wallets/test/topup", limiter, func(c *fiber.Ctx) error {
			return httpx.OK(c, map[string]string{"status": "ok"})
		})
	})

	for i := 1; i <= 5; i++ {
		resp, body := do(t, app, "POST", "/api/v1/wallets/test/topup", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d expected 200, got %d (%s)", i, resp.StatusCode, string(body))
		}
	}

	// 6th request must exceed the rate limit and return HTTP 429
	resp, body := do(t, app, "POST", "/api/v1/wallets/test/topup", nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("request 6 expected 429 Too Many Requests, got %d (%s)", resp.StatusCode, string(body))
	}
	appErr := decodeErr(t, body)
	if appErr.Error.Code != httpx.CodeRateLimited {
		t.Fatalf("expected error code %q, got %q", httpx.CodeRateLimited, appErr.Error.Code)
	}
	if !strings.Contains(appErr.Error.Message, "Too many financial operations") {
		t.Fatalf("expected financial rate limit message, got %s", appErr.Error.Message)
	}
}
