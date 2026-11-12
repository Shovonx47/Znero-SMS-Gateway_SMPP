package metrics

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestRegistry_MetricsOutputAndThreadSafety(t *testing.T) {
	reg := NewRegistry("api")
	cv := reg.Counter("znero_messages_submitted_total", "Total messages submitted")
	gv := reg.Gauge("znero_outbox_pending_count", "Pending outbox messages")

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ch := "http"
			if n%2 == 0 {
				ch = "smpp"
			}
			cv.Inc(map[string]string{"channel": ch, "status": "accepted"})
			gv.Set(int64(n), map[string]string{"queue": "primary"})
		}(i)
	}
	wg.Wait()

	var buf bytes.Buffer
	if err := reg.WritePrometheus(&buf); err != nil {
		t.Fatalf("WritePrometheus failed: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "znero_process_up{role=\"api\"} 1") {
		t.Errorf("expected znero_process_up with role api, got:\n%s", out)
	}
	if !strings.Contains(out, "znero_messages_submitted_total{channel=\"http\",status=\"accepted\"} 25") {
		t.Errorf("expected 25 http submitted messages, got:\n%s", out)
	}
	if !strings.Contains(out, "znero_messages_submitted_total{channel=\"smpp\",status=\"accepted\"} 25") {
		t.Errorf("expected 25 smpp submitted messages, got:\n%s", out)
	}
	if !strings.Contains(out, "znero_outbox_pending_count{queue=\"primary\"}") {
		t.Errorf("expected outbox pending gauge, got:\n%s", out)
	}
}

func TestRegistry_LoopbackAndTokenProtection(t *testing.T) {
	reg := NewRegistry("api")
	handler := reg.HTTPHandler()

	// 1. Loopback request without token succeeds
	reqLoopback := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	reqLoopback.RemoteAddr = "127.0.0.1:45678"
	recLoopback := httptest.NewRecorder()
	handler.ServeHTTP(recLoopback, reqLoopback)
	if recLoopback.Code != http.StatusOK {
		t.Errorf("expected 200 OK for loopback, got %d", recLoopback.Code)
	}

	// 2. Remote request without METRICS_TOKEN is forbidden
	reqRemote := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	reqRemote.RemoteAddr = "192.168.1.100:45678"
	recRemote := httptest.NewRecorder()
	handler.ServeHTTP(recRemote, reqRemote)
	if recRemote.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for remote request without env token, got %d", recRemote.Code)
	}
}
