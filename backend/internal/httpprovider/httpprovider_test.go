package httpprovider_test

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/znero-international/znero/backend/internal/httpprovider"
	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/internal/provider"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/internal/smpp"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/pkg/httpx"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

func TestHTTPProvider_TemplateEscapingAndAuthModes(t *testing.T) {
	ctx := context.Background()
	msgID := uuid.New()
	trickyText := `Hello "quoted" \ & foo=bar&evil=1` + "\nline2"
	attempt := messaging.SendAttempt{
		MessageID:    msgID,
		ConnectionID: uuid.New(),
		Destination:  "+12025550199",
		SenderID:     `ZN"ERO`,
		Text:         trickyText,
		Encoding:     "GSM7",
		PartNumber:   1,
		TotalParts:   1,
	}

	// 1. JSON escaping + bearer auth
	cfg := provider.HTTPConnectorConfig{
		Method:       "POST",
		BodyFormat:   "json",
		BodyTemplate: `{"to":"{{destination_no_plus}}","from":"{{sender}}","msg":"{{text}}","id":"{{message_id}}","part":{{part_number}}}`,
	}
	req, dbg, err := httpprovider.RenderRequest(ctx, cfg, "https://sms.example.com/send", "bearer", "my-secret-token-xyz", attempt)
	if err != nil {
		t.Fatalf("RenderRequest json failed: %v", err)
	}
	if req.Header.Get("Authorization") != "Bearer my-secret-token-xyz" {
		t.Fatalf("expected Bearer auth header, got %q", req.Header.Get("Authorization"))
	}
	rawJSON, _ := io.ReadAll(req.Body)
	if !json.Valid(rawJSON) {
		t.Fatalf("rendered JSON invalid: %s", string(rawJSON))
	}
	var parsed map[string]any
	if err := json.Unmarshal(rawJSON, &parsed); err != nil {
		t.Fatalf("unmarshal rendered JSON: %v", err)
	}
	if parsed["to"] != "12025550199" || parsed["msg"] != trickyText || parsed["from"] != `ZN"ERO` {
		t.Fatalf("JSON placeholder injection or corruption: %+v", parsed)
	}
	_ = dbg

	// 2. Form escaping + api_key_query + redaction
	cfgForm := provider.HTTPConnectorConfig{
		Method:         "POST",
		BodyFormat:     "form",
		AuthQueryParam: "token",
		BodyTemplate:   `to={{destination}}&text={{text}}`,
	}
	reqForm, dbgForm, err := httpprovider.RenderRequest(ctx, cfgForm, "https://sms.example.com/form", "api_key_query", "super-secret-query-999", attempt)
	if err != nil {
		t.Fatalf("RenderRequest form failed: %v", err)
	}
	if !strings.Contains(reqForm.URL.RawQuery, "token=super-secret-query-999") {
		t.Fatalf("expected secret query param in actual URL, got %s", reqForm.URL.RawQuery)
	}
	if strings.Contains(dbgForm.RedactedURL, "super-secret-query-999") {
		t.Fatalf("SECURITY: secret leaked in RedactedURL: %s", dbgForm.RedactedURL)
	}
	rawForm, _ := io.ReadAll(reqForm.Body)
	if strings.Contains(string(rawForm), "&evil=1") {
		t.Fatalf("SECURITY: URL form parameter injection occurred: %s", string(rawForm))
	}

	// 3. Basic auth & api_key_header
	reqBasic, _, err := httpprovider.RenderRequest(ctx, cfg, "https://sms.example.com/send", "basic", "user1:pass1", attempt)
	if err != nil || !strings.HasPrefix(reqBasic.Header.Get("Authorization"), "Basic ") {
		t.Fatalf("expected Basic auth, got %q err=%v", reqBasic.Header.Get("Authorization"), err)
	}
	reqKeyHdr, _, err := httpprovider.RenderRequest(ctx, provider.HTTPConnectorConfig{AuthHeaderName: "X-Custom-Key"}, "https://sms.example.com/send", "api_key_header", "key-val-123", attempt)
	if err != nil || reqKeyHdr.Header.Get("X-Custom-Key") != "key-val-123" {
		t.Fatalf("expected X-Custom-Key header, got %q err=%v", reqKeyHdr.Header.Get("X-Custom-Key"), err)
	}
}

func TestHTTPProvider_ResponseClassification_SSRF_Redirect_SizeCap_AndNoResend(t *testing.T) {
	ctx := context.Background()

	// 1. Response classifier unit coverage (accept, throttle, permanent, 5xx ambiguous, condition check)
	cfg := provider.HTTPConnectorConfig{
		ProviderMsgIDPath:   "data.id",
		ErrorCodePath:       "err.code",
		PermanentErrorCodes: []string{"INVALID_NUMBER"},
		ThrottleErrorCodes:  []string{"QUOTA_EXCEEDED"},
	}
	acc := httpprovider.ParseSendResponse(cfg, 200, []byte(`{"data":{"id":"msg-42"}}`))
	if acc.Status != messaging.SendStatusAccepted || acc.ProviderMessageID != "msg-42" {
		t.Fatalf("expected accepted + msg-42, got %+v", acc)
	}
	thr := httpprovider.ParseSendResponse(cfg, 429, []byte(`{"error":"THROTTLED"}`))
	if thr.Status != messaging.SendStatusRetryableReject {
		t.Fatalf("expected retryable throttle on 429, got %+v", thr)
	}
	thrBody := httpprovider.ParseSendResponse(cfg, 200, []byte(`{"err":{"code":"QUOTA_EXCEEDED"}}`))
	if thrBody.Status != messaging.SendStatusRetryableReject {
		t.Fatalf("expected throttle on body code QUOTA_EXCEEDED, got %+v", thrBody)
	}
	perm := httpprovider.ParseSendResponse(cfg, 400, []byte(`{"err":{"code":"INVALID_NUMBER"}}`))
	if perm.Status != messaging.SendStatusFatalReject {
		t.Fatalf("expected fatal permanent reject on 400, got %+v", perm)
	}
	ambig := httpprovider.ParseSendResponse(cfg, 502, []byte(`{"error":"BAD_GW"}`))
	if ambig.Status != messaging.SendStatusUnknown {
		t.Fatalf("expected AMBIGUOUS SendStatusUnknown on 502, got %+v", ambig)
	}

	// 2. Dial-time SSRF protection in production mode (AllowLoopback=false) blocks 127.0.0.1
	e := testkit.New(t)

	lc, logger := testkit.NewLogCapture()
	fake := testkit.NewFakeHTTPProvider(t, testkit.HTTPModeAccept)

	kr, err := secrets.NewKeyRingFromEnv("development", testkit.RandHex(32), "v1", "")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	prodClient := httpprovider.NewClient(httpprovider.ClientConfig{
		Pool:          e.Pool,
		Decrypter:     kr,
		AllowLoopback: false, // Production SSRF strictness
		Logger:        logger,
	})

	provID := insertTestProvider(t, e)
	secPlain := "HTTP_SECRET_" + testkit.RandHex(8)
	encSec, _ := kr.Encrypt(secPlain)
	connID := insertTestHTTPConn(t, e, provID, fake.URL, "bearer", encSec, "none", "", provider.HTTPConnectorConfig{})

	ssrfRes, _ := prodClient.Send(ctx, messaging.SendAttempt{
		MessageID: uuid.New(), ConnectionID: connID, Destination: "+12025550101", SenderID: "ZNERO", Text: "SecretBodyTest",
	})
	if ssrfRes.Status != messaging.SendStatusFatalReject || fake.RequestCount() != 0 {
		t.Fatalf("SSRF failed to block 127.0.0.1 at dial time: status=%s reqCount=%d err=%s", ssrfRes.Status, fake.RequestCount(), ssrfRes.ErrorMessage)
	}

	// 3. No-redirect enforcement (302 -> FatalReject, never follows redirect target)
	var targetHit atomic.Bool
	redirTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit.Store(true)
		w.WriteHeader(200)
	}))
	defer redirTarget.Close()
	redirSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirTarget.URL, http.StatusFound)
	}))
	defer redirSrv.Close()

	devClient := httpprovider.NewClient(httpprovider.ClientConfig{
		Pool:          e.Pool,
		Decrypter:     kr,
		AllowLoopback: true,
		Logger:        logger,
	})
	redirConnID := insertTestHTTPConn(t, e, provID, redirSrv.URL, "none", "", "none", "", provider.HTTPConnectorConfig{})
	redirRes, _ := devClient.Send(ctx, messaging.SendAttempt{
		MessageID: uuid.New(), ConnectionID: redirConnID, Destination: "+12025550102", SenderID: "ZNERO", Text: "hi",
	})
	if redirRes.Status != messaging.SendStatusFatalReject || targetHit.Load() {
		t.Fatalf("redirect must not be followed: status=%s targetHit=%v", redirRes.Status, targetHit.Load())
	}

	// 4. Timeout after request sent -> AMBIGUOUS (SendStatusUnknown) and NEVER retried by HTTP client
	var slowHits atomic.Int32
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slowHits.Add(1)
		time.Sleep(400 * time.Millisecond)
		w.WriteHeader(200)
	}))
	defer slowSrv.Close()

	slowConnID := insertTestHTTPConn(t, e, provID, slowSrv.URL, "none", "", "none", "", provider.HTTPConnectorConfig{
		RequestTimeoutMs: 120,
	})
	slowRes, _ := devClient.Send(ctx, messaging.SendAttempt{
		MessageID: uuid.New(), ConnectionID: slowConnID, Destination: "+12025550103", SenderID: "ZNERO", Text: "hi",
	})
	if slowRes.Status != messaging.SendStatusUnknown || slowHits.Load() != 1 {
		t.Fatalf("timeout after write must be SendStatusUnknown with 1 attempt (never auto-resend), got status=%s hits=%d", slowRes.Status, slowHits.Load())
	}

	// 5. Response size cap (MaxResponseBytes=1024 prevents oversized payload)
	bigSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"message_id":"big-ok","pad":"` + strings.Repeat("x", 20000) + `"}`))
	}))
	defer bigSrv.Close()
	bigConnID := insertTestHTTPConn(t, e, provID, bigSrv.URL, "none", "", "none", "", provider.HTTPConnectorConfig{
		MaxResponseBytes:   1024,
		ProviderMsgIDRegex: `"message_id":"([^"]+)"`,
	})
	bigRes, _ := devClient.Send(ctx, messaging.SendAttempt{
		MessageID: uuid.New(), ConnectionID: bigConnID, Destination: "+12025550104", SenderID: "ZNERO", Text: "hi",
	})
	if bigRes.Status != messaging.SendStatusAccepted || bigRes.ProviderMessageID != "big-ok" {
		t.Fatalf("expected response size cap + regex ID extraction to succeed safely, got %+v", bigRes)
	}

	// 6. Log leak check: never logs secrets or body or full phone numbers
	lc.ScanForLeaks(t)
	if strings.Contains(lc.Output(), secPlain) || strings.Contains(lc.Output(), "SecretBodyTest") || strings.Contains(lc.Output(), "+12025550101") {
		t.Fatalf("SECURITY: credential, body, or full destination leaked into logs!")
	}
}

func TestHTTPProvider_DLRCallback_HMAC_Unmatched_And_Polling_And_CompositeSender(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()

	kr, err := secrets.NewKeyRingFromEnv("development", testkit.RandHex(32), "v1", "")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	lc, logger := testkit.NewLogCapture()

	// Seed platform/customer orgs, wallet, provider, HTTP + SMPP connections
	msgRepo := messaging.NewRepository(e.Pool)
	ledgerSvc := ledger.NewService(ledger.NewRepository(e.Pool), org.NewRepository(e.Pool), e.Approval, e.Audit, e.Pool)
	custOrgID := seedCustomerWithWallet(t, e, ledgerSvc, "on_delivery", 24, "retain_until_resolution", "keep_charge")
	provID := insertTestProvider(t, e)
	fakeHTTP := testkit.NewFakeHTTPProvider(t, testkit.HTTPModeAccept)
	smppMock := testkit.NewSMPPMockServer(t, testkit.SMPPModeAccept, 0)

	dlrSecret := "cb_hmac_" + testkit.RandHex(12)
	encDLRSec, _ := kr.Encrypt(dlrSecret)
	cbConnID := insertTestHTTPConn(t, e, provID, fakeHTTP.URL, "bearer", "", "callback", encDLRSec, provider.HTTPConnectorConfig{
		CallbackSignatureHeader: "X-Provider-Signature",
		CallbackSignatureAlgo:   "hmac_sha256",
		CallbackMsgIDPath:       "msg_id",
		CallbackStatusPath:      "delivery_status",
		CallbackErrorCodePath:   "err",
		CallbackStatusMap:       map[string]string{"handset_ok": "DELIVRD"},
	})

	// Setup SMPP mock connection in DB to test CompositeSender routing BOTH SMPP and HTTP seamlessly!
	smppConnID := insertTestSMPPConn(t, e, provID, smppMock.Addr)
	smppMgr := smpp.NewConnectionManager(smpp.ManagerConfig{DB: e.Pool, AllowLoopback: true, Logger: logger})
	defer smppMgr.Close()
	smppClient := smpp.NewClient(smpp.ClientConfig{Manager: smppMgr, DB: e.Pool, Logger: logger})
	httpClient := httpprovider.NewClient(httpprovider.ClientConfig{Pool: e.Pool, Decrypter: kr, AllowLoopback: true, Logger: logger})

	composite := messaging.NewCompositeSender(e.Pool, map[string]messaging.Sender{
		"smpp": smppClient,
		"http": httpClient,
	}, 0)

	// Send via CompositeSender to HTTP connection AND to SMPP connection
	httpAttemptRes, err := composite.Send(ctx, messaging.SendAttempt{
		MessageID: uuid.New(), ConnectionID: cbConnID, Destination: "+12025550201", SenderID: "ZNERO", Text: "via http",
	})
	if err != nil || httpAttemptRes.Status != messaging.SendStatusAccepted || httpAttemptRes.ProviderMessageID == "" {
		t.Fatalf("CompositeSender HTTP failed: res=%+v err=%v", httpAttemptRes, err)
	}
	smppAttemptRes, err := composite.Send(ctx, messaging.SendAttempt{
		MessageID: uuid.New(), ConnectionID: smppConnID, Destination: "+12025550202", SenderID: "ZNERO", Text: "via smpp",
	})
	if err != nil || smppAttemptRes.Status != messaging.SendStatusAccepted || smppAttemptRes.ProviderMessageID == "" {
		t.Fatalf("CompositeSender SMPP failed: res=%+v err=%v", smppAttemptRes, err)
	}

	// Verify Callback endpoint (bad HMAC -> 401; unmatched msg_id -> stored in unmatched_dlrs; matched -> DELIVRD & wallet captured!)
	dlrProc := messaging.NewDLRProcessor(msgRepo, ledgerSvc, "on_delivery", logger)

	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler(logger)})
	v1 := app.Group("/api/v1")
	httpprovider.NewCallbackHandler(httpClient, dlrProc, logger).Register(v1)

	cbURL := provider.ProviderDLRCallbackPath(cbConnID)
	payload := []byte(`{"msg_id":"unknown-http-999","delivery_status":"handset_ok","err":"000"}`)

	// Bad signature rejected 401
	badReq := httptest.NewRequest("POST", cbURL, strings.NewReader(string(payload)))
	badReq.Header.Set("Content-Type", "application/json")
	badReq.Header.Set("X-Provider-Signature", "sha256=deadbeef")
	badResp, err := app.Test(badReq, -1)
	if err != nil || badResp.StatusCode != 401 {
		t.Fatalf("expected 401 on invalid callback HMAC, got status=%d err=%v", badResp.StatusCode, err)
	}

	// Valid HMAC + unmatched message -> 200 and recorded in unmatched_dlrs
	goodReq := httptest.NewRequest("POST", cbURL, strings.NewReader(string(payload)))
	goodReq.Header.Set("Content-Type", "application/json")
	goodReq.Header.Set("X-Provider-Signature", "sha256="+signHMAC(dlrSecret, payload))
	goodResp, err := app.Test(goodReq, -1)
	goodBodyBytes, _ := io.ReadAll(goodResp.Body)
	_ = goodResp.Body.Close()
	if err != nil || goodResp.StatusCode != 200 {
		t.Fatalf("expected 200 on valid callback HMAC, got status=%d body=%s err=%v", goodResp.StatusCode, string(goodBodyBytes), err)
	}
	var unmatchedCount int
	qErr := e.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM unmatched_dlrs WHERE connection_id = $1 AND provider_message_id = 'unknown-http-999'`, cbConnID).Scan(&unmatchedCount)
	if unmatchedCount != 1 {
		t.Fatalf("expected unmatched HTTP DLR recorded in unmatched_dlrs, got count=%d qErr=%v resp=%s", unmatchedCount, qErr, string(goodBodyBytes))
	}

	// Now create a submitted message with active hold on cbConnID & test matched callback settles DELIVERED + captures hold
	msgID, createdAt, holdID := seedSubmittedMessageWithHold(t, e, msgRepo, ledgerSvc, custOrgID, cbConnID, httpAttemptRes.ProviderMessageID, decimal.RequireFromString("0.05"))
	matchPayload, _ := json.Marshal(map[string]string{
		"msg_id":          httpAttemptRes.ProviderMessageID,
		"delivery_status": "handset_ok",
		"err":             "000",
	})
	matchReq := httptest.NewRequest("POST", cbURL, strings.NewReader(string(matchPayload)))
	matchReq.Header.Set("Content-Type", "application/json")
	matchReq.Header.Set("X-Provider-Signature", "sha256="+signHMAC(dlrSecret, matchPayload))
	matchResp, err := app.Test(matchReq, -1)
	if err != nil || matchResp.StatusCode != 200 {
		t.Fatalf("expected 200 on matched callback, got %d err=%v", matchResp.StatusCode, err)
	}
	assertMsgStatusAndHold(t, e, msgID, createdAt, holdID, messaging.StatusDelivered, "captured")

	// 2. Test Polling DLR mode!
	pollStatusMap := map[string]string{"1": "queued", "2": "delivered"}
	pollSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id":     id,
			"state":  "2",
			"reason": "000",
		})
	}))
	defer pollSrv.Close()

	pollConnID := insertTestHTTPConn(t, e, provID, fakeHTTP.URL, "none", "", "polling", "", provider.HTTPConnectorConfig{
		PollMethod:        "GET",
		PollURLTemplate:   pollSrv.URL + "/status?id={{provider_message_id}}",
		PollIntervalSec:   60,
		PollStatusPath:    "state",
		PollErrorCodePath: "reason",
		PollStatusMap:     pollStatusMap,
	})
	pollProvMsgID := "poll-" + uuid.New().String()[:8]
	pollMsgID, pollCreatedAt, pollHoldID := seedSubmittedMessageWithHold(t, e, msgRepo, ledgerSvc, custOrgID, pollConnID, pollProvMsgID, decimal.RequireFromString("0.04"))

	poller := httpprovider.NewDLRPoller(e.Pool, httpClient, dlrProc, httpprovider.PollerConfig{}, logger)
	if err := poller.PollOnce(ctx, true); err != nil {
		t.Fatalf("PollOnce failed: %v", err)
	}
	assertMsgStatusAndHold(t, e, pollMsgID, pollCreatedAt, pollHoldID, messaging.StatusDelivered, "captured")

	// 3. Test provider.Service.TestConnectionSend against simulated FakeHTTPProvider
	provRepo := provider.NewRepository(e.Pool)
	provSvc := provider.NewService(provRepo, e.Audit, e.Approval, kr, "development")
	provSvc.SetHTTPTestSender(httpClient)
	overrideTC := &tenant.Context{
		ActorUserID:  e.Override.ActorUserID,
		ActorOrgID:   e.Override.ActorOrgID,
		ActorOrgType: "platform",
		Permissions: map[string]bool{
			"provider.connection.edit": true,
		},
	}
	// Create actor user in users table? TestConnectionSend doesn't insert to audit/users, just queries connection!
	simRes, err := provSvc.TestConnectionSend(ctx, overrideTC, cbConnID, provider.TestSendRequest{
		Destination:  "+12025550999",
		SenderID:     "ZNERO",
		Text:         "UI test",
		SimulateMode: "throttle",
	})
	if err != nil || simRes.Status != string(messaging.SendStatusRetryableReject) || !simRes.Simulated {
		t.Fatalf("TestConnectionSend throttle simulation unexpected: res=%+v err=%v", simRes, err)
	}

	lc.ScanForLeaks(t)
}

func signHMAC(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	_, _ = m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func insertTestProvider(t *testing.T, e *testkit.Env) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := e.Pool.QueryRow(context.Background(), `
		INSERT INTO providers (code, name, provider_type, status, version)
		VALUES ($1, $2, 'http', 'active', 1)
		RETURNING id`,
		"HT_"+testkit.RandHex(4), "HTTP Prov "+testkit.RandHex(3),
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert test provider: %v", err)
	}
	return id
}

func insertTestHTTPConn(t *testing.T, e *testkit.Env, provID uuid.UUID, baseURL, authType, credEnc, dlrMode, dlrSecretEnc string, cfg provider.HTTPConnectorConfig) uuid.UUID {
	t.Helper()
	cfgJSON, _ := json.Marshal(cfg)
	var id uuid.UUID
	err := e.Pool.QueryRow(context.Background(), `
		INSERT INTO provider_connections (
			provider_id, name, protocol, status, version, max_tps,
			http_base_url, http_auth_type, http_credentials_encrypted,
			http_dlr_mode, http_dlr_secret_encrypted, http_config
		) VALUES ($1, $2, 'http', 'active', 1, 100, $3, $4, $5, $6, $7, $8)
		RETURNING id`,
		provID, "HTTP-Conn-"+testkit.RandHex(4), baseURL, authType, credEnc, dlrMode, dlrSecretEnc, cfgJSON,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert http conn: %v", err)
	}
	return id
}

func insertTestSMPPConn(t *testing.T, e *testkit.Env, provID uuid.UUID, addr string) uuid.UUID {
	t.Helper()
	parts := strings.Split(addr, ":")
	host, port := parts[0], parts[1]
	var id uuid.UUID
	err := e.Pool.QueryRow(context.Background(), `
		INSERT INTO provider_connections (
			provider_id, name, protocol, status, version, max_tps,
			smpp_host, smpp_port, smpp_system_id, smpp_bind_type, smpp_tls
		) VALUES ($1, $2, 'smpp', 'active', 1, 100, $3, $4::int, 'test', 'transceiver', false)
		RETURNING id`,
		provID, "SMPP-Conn-"+testkit.RandHex(4), host, port,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert smpp conn: %v", err)
	}
	return id
}

func seedCustomerWithWallet(t *testing.T, e *testkit.Env, ledgerSvc *ledger.Service, mode string, waitH int, noDlr, failPolicy string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	custID := uuid.New()
	err := e.Pool.QueryRow(ctx, `
		INSERT INTO organizations (id, name, type, parent_id, status, path)
		VALUES ($1, $2, 'customer', $3, 'active', $4)
		RETURNING id`,
		custID, "HTTP-Cust-"+testkit.RandHex(4), e.PlatformOrgID, e.PlatformOrgPath+"."+custID.String(),
	).Scan(&custID)
	if err != nil {
		t.Fatalf("insert cust org: %v", err)
	}
	_, err = e.Pool.Exec(ctx, `
		INSERT INTO customer_profiles (
			organization_id, account_type, billing_currency, default_max_tps,
			billing_mode, dlr_wait_hours, no_dlr_policy, failed_after_accept_policy, version
		) VALUES ($1, 'prepaid', 'USD', 50, $2, $3, $4, $5, 1)`,
		custID, mode, waitH, noDlr, failPolicy,
	)
	if err != nil {
		t.Fatalf("insert customer profile: %v", err)
	}
	w, err := ledgerSvc.EnsureWallet(ctx, e.Override, custID, "USD", "prepaid")
	if err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	if _, _, err := ledgerSvc.Topup(ctx, e.Override, w.ID, ledger.WalletTopupRequest{
		Amount:         decimal.NewFromInt(100),
		Reason:         "http test seed",
		IdempotencyKey: "seed-" + custID.String(),
	}, "127.0.0.1", "test"); err != nil {
		t.Fatalf("topup wallet: %v", err)
	}
	return custID
}

func seedSubmittedMessageWithHold(
	t *testing.T,
	e *testkit.Env,
	repo messaging.Repository,
	ledgerSvc *ledger.Service,
	custOrgID, connID uuid.UUID,
	provMsgID string,
	price decimal.Decimal,
) (uuid.UUID, time.Time, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	w, err := ledgerSvc.GetWalletByOrgIDInternal(ctx, custOrgID)
	if err != nil {
		t.Fatalf("get wallet: %v", err)
	}
	msgID := uuid.New()
	now := time.Now().UTC()
	refType := "sms"
	h, err := ledgerSvc.PlaceHold(ctx, w.ID, price, "USD", &refType, &msgID, nil, now.Add(48*time.Hour))
	if err != nil {
		t.Fatalf("place hold: %v", err)
	}

	tx, err := e.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	msg := &messaging.Message{
		ID:                              msgID,
		CreatedAt:                       now,
		CustomerOrgID:                   custOrgID,
		OriginalDestination:             "+12025550199",
		Destination:                     "+12025550199",
		SenderID:                        "ZNERO",
		SenderIDType:                    "alphanumeric",
		MessageType:                     "transactional",
		Encoding:                        "GSM7",
		EncryptedBody:                   []byte("encrypted-test-payload"),
		KeyID:                           "v1",
		BodyLength:                      5,
		TotalParts:                      1,
		Status:                          messaging.StatusSubmitted,
		HoldID:                          &h.ID,
		SellPrice:                       price,
		SellCurrency:                    "USD",
		IngressChannel:                  "api",
		ProviderConnectionID:            &connID,
		ProviderMessageID:               &provMsgID,
		SubmittedAt:                     &now,
		ExpiresAt:                       now.Add(48 * time.Hour),
		BillingModeSnapshot:             "on_delivery",
		DLRWaitHoursSnapshot:            24,
		NoDLRPolicySnapshot:             "retain_until_resolution",
		FailedAfterAcceptPolicySnapshot: "keep_charge",
	}
	if err := repo.InsertMessageAtomic(ctx, tx, msg, nil, nil, nil); err != nil {
		t.Fatalf("insert msg: %v", err)
	}
	if err := repo.RecordProviderLookup(ctx, tx, connID, provMsgID, msgID, msg.CreatedAt); err != nil {
		t.Fatalf("record provider lookup: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit msg: %v", err)
	}
	return msgID, msg.CreatedAt, h.ID
}

func assertMsgStatusAndHold(t *testing.T, e *testkit.Env, msgID uuid.UUID, createdAt time.Time, holdID uuid.UUID, wantMsgStatus, wantHoldStatus string) {
	t.Helper()
	var gotMsg, gotHold string
	_ = e.Pool.QueryRow(context.Background(), `SELECT status FROM messages WHERE id = $1 AND created_at = $2`, msgID, createdAt).Scan(&gotMsg)
	_ = e.Pool.QueryRow(context.Background(), `SELECT status FROM holds WHERE id = $1`, holdID).Scan(&gotHold)
	if gotMsg != wantMsgStatus || gotHold != wantHoldStatus {
		t.Fatalf("msg=%s (want %s), hold=%s (want %s)", gotMsg, wantMsgStatus, gotHold, wantHoldStatus)
	}
}
