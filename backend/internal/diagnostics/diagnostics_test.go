package diagnostics_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/znero-international/znero/backend/internal/diagnostics"
	"github.com/znero-international/znero/backend/pkg/httpx"
	"github.com/znero-international/znero/backend/pkg/kafkaclient"
	"github.com/znero-international/znero/backend/pkg/logx"
)

type fakePublisher struct {
	err    error
	topic  string
	key    []byte
	value  []byte
	called int
}

func (f *fakePublisher) Publish(_ context.Context, topic string, key, value []byte) error {
	f.called++
	f.topic, f.key, f.value = topic, key, value
	return f.err
}

func setup(t *testing.T, pub *fakePublisher, logs io.Writer) (*fiber.App, *diagnostics.Service) {
	t.Helper()
	logger, _ := logx.New(logs, "debug", "json")
	svc := diagnostics.NewService(pub, "znero.dev.test", time.Second, logger)
	app := httpx.NewApp(httpx.AppConfig{AllowedMethods: []string{"GET", "POST"}, BodyLimitBytes: 4096}, logger)
	diagnostics.NewHandler(svc).Register(app.Group("/api/v1"))
	return app, svc
}

func post(t *testing.T, app *fiber.App, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/dev/kafka/test-message", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, b
}

func TestPublishSuccess(t *testing.T) {
	pub := &fakePublisher{}
	app, _ := setup(t, pub, io.Discard)
	status, body := post(t, app, `{"message":"hello kafka"}`)
	if status != 202 {
		t.Fatalf("status %d: %s", status, body)
	}
	if pub.called != 1 || pub.topic != "znero.dev.test" {
		t.Fatalf("publisher not called correctly: %+v", pub)
	}
	var sent diagnostics.TestMessage
	if err := json.Unmarshal(pub.value, &sent); err != nil {
		t.Fatalf("published payload is not JSON: %v", err)
	}
	if sent.Message != "hello kafka" || sent.ID == "" || string(pub.key) != sent.ID || sent.SentAt.IsZero() {
		t.Errorf("unexpected payload: %+v key=%s", sent, pub.key)
	}
	if !strings.Contains(string(body), `"topic":"znero.dev.test"`) {
		t.Errorf("response should name the topic: %s", body)
	}
}

func TestPublishValidation(t *testing.T) {
	pub := &fakePublisher{}
	app, _ := setup(t, pub, io.Discard)
	tests := []struct {
		name, body string
		wantStatus int
		wantCode   string
	}{
		{"empty message", `{"message":""}`, 422, "validation_failed"},
		{"missing field", `{}`, 422, "validation_failed"},
		{"too long", `{"message":"` + strings.Repeat("a", 201) + `"}`, 422, "validation_failed"},
		{"control character", `{"message":"a\u0000b"}`, 422, "validation_failed"},
		{"newline", `{"message":"a\nb"}`, 422, "validation_failed"},
		{"not json", `hello`, 400, "bad_request"},
		{"unknown field", `{"message":"x","topic":"other"}`, 400, "bad_request"},
		{"trailing data", `{"message":"x"}{"message":"y"}`, 400, "bad_request"},
		{"empty body", ``, 400, "bad_request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := post(t, app, tt.body)
			if status != tt.wantStatus || !strings.Contains(string(body), `"code":"`+tt.wantCode+`"`) {
				t.Errorf("got %d %s, want %d %s", status, body, tt.wantStatus, tt.wantCode)
			}
		})
	}
	if pub.called != 0 {
		t.Errorf("invalid input must never reach Kafka (published %d times)", pub.called)
	}
}

func TestPublishAcceptsExactMaxLengthAndUnicode(t *testing.T) {
	pub := &fakePublisher{}
	app, _ := setup(t, pub, io.Discard)
	if status, body := post(t, app, `{"message":"`+strings.Repeat("é", 200)+`"}`); status != 202 {
		t.Errorf("200 characters (multi-byte) should be accepted: %d %s", status, body)
	}
}

func TestPublishKafkaDownReturns503WithoutLeakingDetails(t *testing.T) {
	pub := &fakePublisher{err: errors.New("dial tcp 10.0.0.5:9092: i/o timeout (internal-broker)")}
	var logs strings.Builder
	app, _ := setup(t, pub, &logs)
	status, body := post(t, app, `{"message":"hi"}`)
	if status != 503 || !strings.Contains(string(body), `"code":"service_unavailable"`) {
		t.Fatalf("want 503 service_unavailable, got %d %s", status, body)
	}
	if strings.Contains(string(body), "internal-broker") || strings.Contains(string(body), "10.0.0.5") {
		t.Errorf("internal detail leaked: %s", body)
	}
	if !strings.Contains(logs.String(), "internal-broker") {
		t.Error("the real error should be logged server-side")
	}
}

func TestLogMessageHandler(t *testing.T) {
	var logs strings.Builder
	_, svc := setup(t, &fakePublisher{}, &logs)
	err := svc.LogMessage(context.Background(), kafkaclient.Message{
		Topic: "znero.dev.test", Partition: 0, Offset: 7, Key: []byte("k1"),
		Value: []byte("line1\nFAKE LOG LINE level=ERROR"),
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("a payload containing a newline must stay in ONE log line (log injection), got %d lines:\n%s", len(lines), logs.String())
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["msg"] != "kafka message received" || rec["offset"] != float64(7) || rec["topic"] != "znero.dev.test" {
		t.Errorf("unexpected log record: %v", rec)
	}
}

func TestLogMessageTruncatesLargePayloads(t *testing.T) {
	var logs strings.Builder
	_, svc := setup(t, &fakePublisher{}, &logs)
	_ = svc.LogMessage(context.Background(), kafkaclient.Message{Topic: "t", Value: bytes.Repeat([]byte("x"), 5000)})
	if len(logs.String()) > 2000 {
		t.Errorf("log line too large (%d bytes); payload must be truncated", len(logs.String()))
	}
	if !strings.Contains(logs.String(), `"truncated":true`) {
		t.Error("truncation should be flagged")
	}
}
