package logx_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/znero-international/znero/backend/pkg/logx"
)

func TestRequestIDContextRoundTrip(t *testing.T) {
	if got := logx.RequestID(context.Background()); got != "" {
		t.Errorf("empty context should have no id, got %q", got)
	}
	ctx := logx.WithRequestID(context.Background(), "abc-123")
	if got := logx.RequestID(ctx); got != "abc-123" {
		t.Errorf("RequestID = %q", got)
	}
}

func TestLoggerAddsRequestIDOnlyWhenPresent(t *testing.T) {
	var buf bytes.Buffer
	l, err := logx.New(&buf, "info", "json")
	if err != nil {
		t.Fatal(err)
	}
	l.InfoContext(logx.WithRequestID(context.Background(), "req-1"), "with id")
	l.Info("without id")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lines))
	}
	var first, second map[string]any
	_ = json.Unmarshal([]byte(lines[0]), &first)
	_ = json.Unmarshal([]byte(lines[1]), &second)
	if first["request_id"] != "req-1" {
		t.Errorf("request_id missing: %v", first)
	}
	if _, ok := second["request_id"]; ok {
		t.Errorf("request_id should be absent outside a request: %v", second)
	}
}

func TestRequestIDSurvivesWithAttrsAndGroups(t *testing.T) {
	var buf bytes.Buffer
	l, _ := logx.New(&buf, "info", "json")
	l.With("component", "x").WithGroup("g").InfoContext(logx.WithRequestID(context.Background(), "req-2"), "hello", slog.String("k", "v"))
	if !strings.Contains(buf.String(), "req-2") {
		t.Errorf("request id lost through With/WithGroup: %s", buf.String())
	}
}

func TestLevelsAndFormats(t *testing.T) {
	var buf bytes.Buffer
	l, _ := logx.New(&buf, "warn", "text")
	l.Info("hidden")
	l.Warn("shown")
	if strings.Contains(buf.String(), "hidden") || !strings.Contains(buf.String(), "shown") {
		t.Errorf("level filtering wrong: %s", buf.String())
	}
	if _, err := logx.New(&buf, "loud", "json"); err == nil {
		t.Error("unknown level must be rejected")
	}
	if _, err := logx.New(&buf, "info", "xml"); err == nil {
		t.Error("unknown format must be rejected")
	}
}
