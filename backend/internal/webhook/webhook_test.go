package webhook_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/internal/webhook"
)

func seedCustomerOrgAndWebhook(t *testing.T, e *testkit.Env, kr *secrets.KeyRing, targetURL, whSecret string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	custID := uuid.New()
	if _, err := e.Pool.Exec(ctx, `
		INSERT INTO organizations (id, name, type, parent_id, status, path)
		VALUES ($1, $2, 'customer', $3, 'active', $4)`,
		custID, "WH-Cust-"+testkit.RandHex(4), e.PlatformOrgID, e.PlatformOrgPath+"."+custID.String(),
	); err != nil {
		t.Fatalf("insert customer org: %v", err)
	}
	encSec, err := kr.Encrypt(whSecret)
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	var whID uuid.UUID
	if err := e.Pool.QueryRow(ctx, `
		INSERT INTO customer_webhooks (customer_org_id, url, secret_encrypted, secret_key_id, events, status, version)
		VALUES ($1, $2, $3, $4, '["message.final","message.inbound"]'::jsonb, 'active', 1)
		RETURNING id`,
		custID, targetURL, encSec, secrets.DefaultKeyID,
	).Scan(&whID); err != nil {
		t.Fatalf("insert webhook: %v", err)
	}
	return custID, whID
}

func TestWebhookDispatcher_HMACHeaders_SSRF_Redirect_And_Retries(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	// Clear pending deliveries left from earlier suites so our batch is isolated
	_, _ = e.Pool.Exec(ctx, `DELETE FROM customer_webhook_deliveries`)

	kr, err := secrets.NewKeyRingFromEnv("development", testkit.RandHex(32), "v1", "")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	lc, logger := testkit.NewLogCapture()
	msgRepo := messaging.NewRepository(e.Pool)

	// 1. Real HTTPS-like local receiver verifying standard X-Znero-* HMAC headers
	whSecret := "whsec_test_" + testkit.RandHex(16)
	var mu sync.Mutex
	var gotHeaders http.Header
	var gotBody []byte
	var recvStatus atomic.Int32
	recvStatus.Store(200)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotHeaders = r.Header.Clone()
		gotBody = b
		mu.Unlock()
		w.WriteHeader(int(recvStatus.Load()))
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	custID, whID := seedCustomerOrgAndWebhook(t, e, kr, srv.URL, whSecret)
	delivID := uuid.New()
	eventID := uuid.New()
	payload, _ := json.Marshal(map[string]any{
		"message_id": uuid.NewString(),
		"status":     "delivered",
	})
	if _, err := e.Pool.Exec(ctx, `
		INSERT INTO customer_webhook_deliveries (
			id, webhook_id, customer_org_id, event_id, event_type, payload,
			status, attempts, max_attempts, next_attempt_at, created_at
		) VALUES ($1, $2, $3, $4, 'message.final', $5, 'pending', 0, 5, now(), now())`,
		delivID, whID, custID, eventID, payload,
	); err != nil {
		t.Fatalf("insert delivery: %v", err)
	}

	devDisp := webhook.NewDispatcher(msgRepo, kr, webhook.DefaultConfig(true), logger)
	devDisp.DispatchPending(ctx)

	mu.Lock()
	hdrs := gotHeaders
	bodyCopy := gotBody
	mu.Unlock()
	if hdrs == nil {
		t.Fatalf("expected webhook delivery to reach server")
	}
	gotEventID := hdrs.Get("X-Znero-Event-Id")
	gotTS := hdrs.Get("X-Znero-Timestamp")
	gotSig := hdrs.Get("X-Znero-Signature")
	if gotEventID != delivID.String() || gotTS == "" || !strings.HasPrefix(gotSig, "v1=") {
		t.Fatalf("missing required X-Znero headers: event_id=%q ts=%q sig=%q", gotEventID, gotTS, gotSig)
	}

	mac := hmac.New(sha256.New, []byte(whSecret))
	mac.Write([]byte(gotTS + "." + string(bodyCopy)))
	wantSig := "v1=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(gotSig), []byte(wantSig)) {
		t.Fatalf("HMAC mismatch: got=%s want=%s", gotSig, wantSig)
	}

	var status string
	_ = e.Pool.QueryRow(ctx, `SELECT status FROM customer_webhook_deliveries WHERE id = $1`, delivID).Scan(&status)
	if status != "success" {
		t.Fatalf("expected delivery status success, got %q", status)
	}

	// 2. Non-2xx (500) schedules retry with backoff & increments consecutive_failures
	recvStatus.Store(500)
	delivFailID := uuid.New()
	if _, err := e.Pool.Exec(ctx, `
		INSERT INTO customer_webhook_deliveries (
			id, webhook_id, customer_org_id, event_id, event_type, payload,
			status, attempts, max_attempts, next_attempt_at, created_at
		) VALUES ($1, $2, $3, $4, 'message.final', $5, 'pending', 0, 5, now(), now())`,
		delivFailID, whID, custID, uuid.New(), payload,
	); err != nil {
		t.Fatalf("insert fail delivery: %v", err)
	}
	devDisp.DispatchPending(ctx)
	var failStatus string
	var attempts int
	var nextAt time.Time
	_ = e.Pool.QueryRow(ctx, `SELECT status, attempts, next_attempt_at FROM customer_webhook_deliveries WHERE id = $1`, delivFailID).Scan(&failStatus, &attempts, &nextAt)
	if (failStatus != "retry" && failStatus != "pending") || attempts != 1 || !nextAt.After(time.Now().UTC().Add(-1*time.Second)) {
		t.Fatalf("expected retry/pending backoff after HTTP 500, got status=%s attempts=%d next=%s", failStatus, attempts, nextAt)
	}

	// 3. Redirect response must NOT be followed
	var redirTargetHit atomic.Bool
	redirTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirTargetHit.Store(true)
		w.WriteHeader(200)
	}))
	defer redirTarget.Close()
	redirSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirTarget.URL, http.StatusFound)
	}))
	defer redirSrv.Close()

	redirCustID, whRedirID := seedCustomerOrgAndWebhook(t, e, kr, redirSrv.URL, whSecret)
	redirDelivID := uuid.New()
	_, _ = e.Pool.Exec(ctx, `
		INSERT INTO customer_webhook_deliveries (
			id, webhook_id, customer_org_id, event_id, event_type, payload,
			status, attempts, max_attempts, next_attempt_at, created_at
		) VALUES ($1, $2, $3, $4, 'message.final', $5, 'pending', 0, 5, now(), now())`,
		redirDelivID, whRedirID, redirCustID, uuid.New(), payload,
	)
	devDisp.DispatchPending(ctx)
	if redirTargetHit.Load() {
		t.Fatalf("SECURITY: webhook dispatcher followed redirect!")
	}

	// 4. Production SSRF mode (AllowLoopback=false) blocks 127.0.0.1 at dial time
	prodDisp := webhook.NewDispatcher(msgRepo, kr, webhook.DefaultConfig(false), logger)
	ssrfCustID, whSSRFID := seedCustomerOrgAndWebhook(t, e, kr, srv.URL, whSecret)
	ssrfDelivID := uuid.New()
	_, _ = e.Pool.Exec(ctx, `
		INSERT INTO customer_webhook_deliveries (
			id, webhook_id, customer_org_id, event_id, event_type, payload,
			status, attempts, max_attempts, next_attempt_at, created_at
		) VALUES ($1, $2, $3, $4, 'message.final', $5, 'pending', 0, 5, now(), now())`,
		ssrfDelivID, whSSRFID, ssrfCustID, uuid.New(), payload,
	)
	prodDisp.DispatchPending(ctx)
	var errClass string
	_ = e.Pool.QueryRow(ctx, `SELECT error_class FROM customer_webhook_attempts WHERE delivery_id = $1 ORDER BY created_at DESC LIMIT 1`, ssrfDelivID).Scan(&errClass)
	if errClass != "ssrf_blocked" {
		t.Fatalf("expected SSRF dial rejection error_class=ssrf_blocked, got %q", errClass)
	}

	lc.ScanForLeaks(t)
	if strings.Contains(lc.Output(), whSecret) {
		t.Fatalf("SECURITY: webhook secret leaked into logs")
	}
}
