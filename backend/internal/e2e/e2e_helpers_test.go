package e2e_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/znero-international/znero/backend/internal/customer"
	"github.com/znero-international/znero/backend/internal/customeredge"
	"github.com/znero-international/znero/backend/internal/httpprovider"
	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/number"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/internal/provider"
	"github.com/znero-international/znero/backend/internal/rating"
	"github.com/znero-international/znero/backend/internal/routing"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/internal/smpp"
	"github.com/znero-international/znero/backend/internal/smpp/mocksmsc"
	"github.com/znero-international/znero/backend/internal/smpp/pdu"
	"github.com/znero-international/znero/backend/internal/smppserver"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/internal/webhook"
	"github.com/znero-international/znero/backend/pkg/crypto/password"
	"github.com/znero-international/znero/backend/pkg/httpx"
)

type fixedRater struct {
	sellPrice decimal.Decimal
	buyPrice  decimal.Decimal
	currency  string
}

func (r *fixedRater) LookupBuyRate(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ *uuid.UUID, _ time.Time) (*routing.RateLookupResult, error) {
	return &routing.RateLookupResult{Found: true, Price: r.buyPrice, Currency: r.currency}, nil
}

func (r *fixedRater) LookupSellRate(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ *uuid.UUID, _ time.Time) (*routing.RateLookupResult, error) {
	return &routing.RateLookupResult{Found: true, Price: r.sellPrice, Currency: r.currency}, nil
}

type fixedNumberer struct{ countryID uuid.UUID }

func (n *fixedNumberer) Lookup(_ string) (*routing.NumberInfo, error) {
	return &routing.NumberInfo{CountryID: n.countryID}, nil
}

func eventually(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s after %s", desc, timeout)
}

func signHMACSHA256(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func hashAPIKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func countNonSingletonGoroutines() (int, string) {
	buf := make([]byte, 512*1024)
	n := runtime.Stack(buf, true)
	raw := string(buf[:n])
	blocks := strings.Split(raw, "\n\n")
	count := 0
	for _, b := range blocks {
		if strings.TrimSpace(b) == "" {
			continue
		}
		// Fiber/fasthttp initializes one process-wide updateServerDate clock singleton on first Fiber instantiation.
		if strings.Contains(b, "fasthttp.updateServerDate") {
			continue
		}
		count++
	}
	return count, raw
}

// watchGoroutineLeaks captures the goroutine count before any test resources allocate
// and must be registered BEFORE testkit.New(t) so LIFO cleanup runs it last.
func watchGoroutineLeaks(t *testing.T) {
	t.Helper()
	runtime.GC()
	base, _ := countNonSingletonGoroutines()
	t.Cleanup(func() {
		if t.Failed() {
			return
		}
		if tr, ok := http.DefaultTransport.(*http.Transport); ok {
			tr.CloseIdleConnections()
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			runtime.GC()
			cur, _ := countNonSingletonGoroutines()
			if cur <= base {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		cur, dump := countNonSingletonGoroutines()
		t.Fatalf("goroutine leak detected: baseline=%d current=%d\n%s", base, cur, dump)
	})
}

type e2eCustomer struct {
	orgID      uuid.UUID
	walletID   uuid.UUID
	apiKey     string
	whSecret   string
	smppSysID  string
	smppPass   string
	smppAcctID uuid.UUID
	planID     uuid.UUID
	topupUSD   decimal.Decimal
}

type webhookEvent struct {
	EventID string
	Status  string
	Payload map[string]any
}

type httpScriptAction string

const (
	httpActionOK           httpScriptAction = "ok"
	httpActionRetryable429 httpScriptAction = "retryable_429"
	httpActionFatal400     httpScriptAction = "fatal_400"
	httpActionAmbiguous502 httpScriptAction = "ambiguous_502"
	httpActionSlowTimeout  httpScriptAction = "slow_timeout"
	httpActionHTMLGarbage  httpScriptAction = "html_garbage"
	httpActionBinaryTrash  httpScriptAction = "binary_trash"
)

type e2eHarness struct {
	t          *testing.T
	e          *testkit.Env
	lc         *testkit.LogCapture
	kr         *secrets.KeyRing
	countryID  uuid.UUID
	clk        *testkit.TestClock
	ledgerSvc  *ledger.Service
	numSvc     *number.Service
	ratingSvc  *rating.Service
	custSvc    *customer.Service
	routingEng *routing.Engine
	routingSvc *routing.Service
	msgRepo    messaging.Repository
	dlrProc    *messaging.DLRProcessor
	smppMgr    *smpp.ConnectionManager
	smppSender *smpp.Client
	httpSender *httpprovider.Client
	compSender *messaging.CompositeSender
	msgSvc     *messaging.Service
	dispatcher *messaging.Dispatcher
	reaper     *messaging.Reaper
	poller     *httpprovider.DLRPoller
	whDisp     *webhook.Dispatcher
	smppSrv    *smppserver.Server
	smppDLR    *smppserver.DLRDispatcher
	fiberApp   *fiber.App

	smsc       *mocksmsc.Server
	smscBackup *mocksmsc.Server
	httpProv   *httptest.Server
	custWhSrv  *httptest.Server

	provSMPPID       uuid.UUID
	connSMPPID       uuid.UUID
	provSMPPBackupID uuid.UUID
	connSMPPBackupID uuid.UUID
	provHTTPID       uuid.UUID
	connHTTPID       uuid.UUID
	connHTTPCbID     uuid.UUID
	smppPlanID       uuid.UUID
	httpPlanID       uuid.UUID
	httpCbPlanID     uuid.UUID
	failoverPlanID   uuid.UUID

	provSMPPPass   string
	httpAuthToken  string
	httpCbSecret   string
	custWhSecret   string
	httpSeq        atomic.Int64
	httpSendCount  atomic.Int64
	httpNextAction atomic.Value
	pollMu         sync.Mutex
	pollStatusByID map[string]string
	whMu           sync.Mutex
	receivedWH     map[uuid.UUID][]webhookEvent
}

func newE2EHarness(t *testing.T, allowLoopback bool) *e2eHarness {
	t.Helper()
	watchGoroutineLeaks(t)
	e := testkit.New(t)
	ctx := context.Background()

	// Clean-slate reset of transient messaging tables and deactivate stale provider_connections/plans
	// left by other package tests so ReconciliationService and RoutingEngine see ONLY this test's state.
	for _, stmt := range []string{
		`TRUNCATE TABLE message_events, message_dlrs, settlement_exceptions, unmatched_dlrs, customer_webhook_attempts, customer_webhook_deliveries, smpp_dlr_queue, message_outbox, message_provider_lookups, message_idempotency_lookups, message_locator, message_attempts, message_parts, inbound_messages, messages, holds CASCADE`,
		`UPDATE provider_connections SET status = 'disabled' WHERE status = 'active'`,
		`UPDATE route_plans SET status = 'deactivated' WHERE status = 'active'`,
	} {
		if _, err := e.Pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("clean-slate %s: %v", stmt, err)
		}
	}

	lc, logger := testkit.NewLogCapture()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("CAPTURED LOGS:\n%s", lc.Output())
		}
	})
	kr, err := secrets.NewKeyRingFromEnv("development", testkit.RandHex(32), "v1", "")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}

	var countryID uuid.UUID
	if err := e.Pool.QueryRow(ctx, `SELECT id FROM countries LIMIT 1`).Scan(&countryID); err != nil {
		cRes, cErr := e.Country.Create(ctx, e.Override, e.CountryRequest())
		if cErr != nil {
			t.Fatalf("create country: %v", cErr)
		}
		countryID = cRes.(*country.Country).ID
	}

	h := &e2eHarness{
		t:              t,
		e:              e,
		lc:             lc,
		kr:             kr,
		countryID:      countryID,
		clk:            testkit.NewTestClock(time.Now().UTC()),
		pollStatusByID: make(map[string]string),
		receivedWH:     make(map[uuid.UUID][]webhookEvent),
	}
	h.httpNextAction.Store(httpScriptAction(httpActionOK))

	h.ledgerSvc = ledger.NewService(ledger.NewRepository(e.Pool), org.NewRepository(e.Pool), e.Approval, e.Audit, e.Pool)
	h.numSvc = number.NewService(number.NewRepository(e.Pool), e.PrefixRepo, e.Prefix, e.Approval, e.Audit, e.Pool, logger)
	h.ratingSvc = rating.NewService(rating.NewRepository(e.Pool), e.Audit, e.Approval, h.numSvc, nil)
	h.custSvc = customer.NewService(customer.NewRepository(e.Pool), e.Audit, e.Approval)
	h.msgRepo = messaging.NewRepository(e.Pool)

	// Start Primary + Backup MockSMSC servers
	h.provSMPPPass = "provSmpp_" + testkit.RandHex(8)
	var errS error
	h.smsc, errS = mocksmsc.NewServer(mocksmsc.ServerConfig{
		Addr:     "127.0.0.1:0",
		SystemID: "E2E_SMSC_" + testkit.RandHex(3),
		Password: h.provSMPPPass,
		DLRMode:  mocksmsc.DLRModeImmediate,
	})
	if errS != nil {
		t.Fatalf("mocksmsc primary: %v", errS)
	}
	if err := h.smsc.Start(); err != nil {
		t.Fatalf("mocksmsc primary start: %v", err)
	}
	t.Cleanup(func() { _ = h.smsc.Close() })

	h.smscBackup, errS = mocksmsc.NewServer(mocksmsc.ServerConfig{
		Addr:     "127.0.0.1:0",
		SystemID: "E2E_BKP_" + testkit.RandHex(3),
		Password: h.provSMPPPass,
		DLRMode:  mocksmsc.DLRModeImmediate,
	})
	if errS != nil {
		t.Fatalf("mocksmsc backup: %v", errS)
	}
	if err := h.smscBackup.Start(); err != nil {
		t.Fatalf("mocksmsc backup start: %v", err)
	}
	t.Cleanup(func() { _ = h.smscBackup.Close() })

	h.provSMPPID, h.connSMPPID = h.insertSMPPProvider(ctx, h.smsc, "E2E-SMPP-Primary")
	h.provSMPPBackupID, h.connSMPPBackupID = h.insertSMPPProvider(ctx, h.smscBackup, "E2E-SMPP-Backup")

	// Scriptable HTTP provider supporting OK / 429 / 400 / 502 / slow hang / HTML / binary garbage + /poll
	h.httpAuthToken = "provHttpTok_" + testkit.RandHex(12)
	h.httpCbSecret = "provCbSec_" + testkit.RandHex(12)
	h.httpProv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+h.httpAuthToken {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/send" {
			h.httpSendCount.Add(1)
			action, _ := h.httpNextAction.Load().(httpScriptAction)
			if action == "" {
				action = httpActionOK
			}
			// Auto-reset non-sticky failure scripts back to OK after triggering once if needed via explicit tests
			switch action {
			case httpActionRetryable429:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(429)
				_, _ = io.WriteString(w, `{"error":"rate_limited"}`)
				return
			case httpActionFatal400:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(400)
				_, _ = io.WriteString(w, `{"error":"invalid_destination"}`)
				return
			case httpActionAmbiguous502:
				w.WriteHeader(502)
				_, _ = io.WriteString(w, `{"error":"bad_gateway"}`)
				return
			case httpActionSlowTimeout:
				time.Sleep(1500 * time.Millisecond) // > RequestTimeoutMs (500ms)
				return
			case httpActionHTMLGarbage:
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(200)
				_, _ = io.WriteString(w, `<html><body><h1>502 Bad Gateway</h1></body></html>`)
				return
			case httpActionBinaryTrash:
				w.Header().Set("Content-Type", "application/octet-stream")
				w.WriteHeader(200)
				_, _ = w.Write([]byte{0xff, 0x00, 0xfe, 0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a})
				return
			default:
				seq := h.httpSeq.Add(1)
				pid := fmt.Sprintf("http-msg-%d", seq)
				h.pollMu.Lock()
				h.pollStatusByID[pid] = "DELIVRD"
				h.pollMu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(200)
				_, _ = fmt.Fprintf(w, `{"status":"ok","data":{"id":%q}}`, pid)
				return
			}
		}
		if r.URL.Path == "/poll" {
			pid := r.URL.Query().Get("id")
			h.pollMu.Lock()
			st := h.pollStatusByID[pid]
			h.pollMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"status":%q,"error_code":"000"}}`, pid, st)
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(func() { h.httpProv.Close() })

	httpConnCfg := provider.HTTPConnectorConfig{
		Method:                  "POST",
		BodyFormat:              "json",
		BodyTemplate:            `{"to":"{{destination}}","from":"{{sender}}","text":"{{text}}","ref":"{{message_id}}"}`,
		SuccessHTTPMin:          200,
		SuccessHTTPMax:          299,
		ProviderMsgIDPath:       "data.id",
		SuccessJSONPath:         "status",
		SuccessEquals:           "ok",
		RequestTimeoutMs:        500, // Fast timeout so slow_timeout chaos cell completes well inside test budget
		CallbackMsgIDPath:       "provider_message_id",
		CallbackStatusPath:      "status",
		CallbackErrorCodePath:   "error_code",
		CallbackSignatureHeader: "X-Provider-Signature",
		CallbackSignatureAlgo:   "hmac_sha256",
		PollURLTemplate:         h.httpProv.URL + "/poll?id={{provider_message_id}}",
		PollMethod:              "GET",
		PollStatusPath:          "data.status",
		PollErrorCodePath:       "data.error_code",
		PollIntervalSec:         1,
	}
	httpCfgJSON, _ := json.Marshal(httpConnCfg)
	encHTTPTok, _ := kr.Encrypt(h.httpAuthToken)
	encHTTPCbSec, _ := kr.Encrypt(h.httpCbSecret)

	if err := e.Pool.QueryRow(ctx, `
		INSERT INTO providers (code, name, provider_type, status, version)
		VALUES ($1, 'E2E HTTP Carrier', 'http', 'active', 1) RETURNING id`,
		"E2E_HTTP_"+testkit.RandHex(4),
	).Scan(&h.provHTTPID); err != nil {
		t.Fatalf("insert HTTP provider: %v", err)
	}
	if err := e.Pool.QueryRow(ctx, `
		INSERT INTO provider_connections (
			provider_id, name, protocol, status, version, max_tps,
			http_base_url, http_auth_type, http_credentials_encrypted,
			http_config, http_dlr_mode, http_dlr_secret_encrypted
		) VALUES ($1, $2, 'http', 'active', 1, 500, $3, 'bearer', $4, $5, 'polling', $6)
		RETURNING id`,
		h.provHTTPID, "E2E-HTTP-Conn-"+testkit.RandHex(3), h.httpProv.URL+"/send", encHTTPTok, httpCfgJSON, encHTTPCbSec,
	).Scan(&h.connHTTPID); err != nil {
		t.Fatalf("insert HTTP provider connection: %v", err)
	}
	if err := e.Pool.QueryRow(ctx, `
		INSERT INTO provider_connections (
			provider_id, name, protocol, status, version, max_tps,
			http_base_url, http_auth_type, http_credentials_encrypted,
			http_config, http_dlr_mode, http_dlr_secret_encrypted
		) VALUES ($1, $2, 'http', 'active', 1, 500, $3, 'bearer', $4, $5, 'callback', $6)
		RETURNING id`,
		h.provHTTPID, "E2E-HTTP-CbConn-"+testkit.RandHex(3), h.httpProv.URL+"/send", encHTTPTok, httpCfgJSON, encHTTPCbSec,
	).Scan(&h.connHTTPCbID); err != nil {
		t.Fatalf("insert HTTP callback connection: %v", err)
	}
	_, _ = e.Pool.Exec(ctx, `
		INSERT INTO provider_coverage (provider_id, country_id, status, version)
		VALUES ($1, $2, 'active', 1)
		ON CONFLICT (provider_id, country_id, COALESCE(operator_id, '00000000-0000-0000-0000-000000000000'::uuid)) DO UPDATE SET status = 'active'`,
		h.provHTTPID, countryID,
	)

	rater := &fixedRater{
		sellPrice: decimal.RequireFromString("0.050000"),
		buyPrice:  decimal.RequireFromString("0.020000"),
		currency:  "USD",
	}
	h.routingEng = routing.NewEngine(e.Pool, rater, &fixedNumberer{countryID: countryID}, nil)
	h.routingSvc = routing.NewService(routing.NewRepository(e.Pool), e.Approval, e.Audit, e.Pool, h.routingEng)

	h.smppPlanID = h.createPlanForConns([]uuid.UUID{h.connSMPPID}, "SMPP")
	h.httpPlanID = h.createPlanForConns([]uuid.UUID{h.connHTTPID}, "HTTP")
	h.httpCbPlanID = h.createPlanForConns([]uuid.UUID{h.connHTTPCbID}, "HTTP_CB")
	h.failoverPlanID = h.createPlanForConns([]uuid.UUID{h.connSMPPID, h.connSMPPBackupID}, "FAILOVER")

	// Customer Webhook Receiver (validates X-Znero-Event-Id, Timestamp, Signature HMAC)
	h.custWhSecret = "custWhSec_" + testkit.RandHex(12)
	h.custWhSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		evID := r.Header.Get("X-Znero-Event-Id")
		sig := r.Header.Get("X-Znero-Signature")
		ts := r.Header.Get("X-Znero-Timestamp")
		if evID == "" || ts == "" || sig != "v1="+signHMACSHA256(h.custWhSecret, []byte(ts+"."+string(body))) {
			w.WriteHeader(401)
			return
		}
		var doc map[string]any
		_ = json.Unmarshal(body, &doc)
		if idStr, ok := doc["message_id"].(string); ok {
			if mid, parseErr := uuid.Parse(idStr); parseErr == nil {
				st, _ := doc["status"].(string)
				h.whMu.Lock()
				h.receivedWH[mid] = append(h.receivedWH[mid], webhookEvent{
					EventID: evID,
					Status:  st,
					Payload: doc,
				})
				h.whMu.Unlock()
			}
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(func() { h.custWhSrv.Close() })

	// Messaging + DLR + Edge + SMPP Server wiring (closed BEFORE mock servers & e.Pool in LIFO order!)
	h.dlrProc = messaging.NewDLRProcessor(h.msgRepo, h.ledgerSvc, "on_delivery", logger)
	h.smppMgr = smpp.NewConnectionManager(smpp.ManagerConfig{
		DB:            e.Pool,
		Decrypter:     kr,
		AllowLoopback: allowLoopback,
		Logger:        logger,
		DLRHandler: func(ctx context.Context, connID uuid.UUID, deliver *pdu.DeliverSM) error {
			return h.dlrProc.ProcessDeliverSM(ctx, connID, deliver)
		},
	})
	t.Cleanup(func() { _ = h.smppMgr.Close() })

	h.smppSender = smpp.NewClient(smpp.ClientConfig{Manager: h.smppMgr, DB: e.Pool, Logger: logger})
	h.httpSender = httpprovider.NewClient(httpprovider.ClientConfig{
		Pool:          e.Pool,
		Decrypter:     kr,
		AllowLoopback: allowLoopback,
		Logger:        logger,
	})
	h.compSender = messaging.NewCompositeSender(e.Pool, map[string]messaging.Sender{
		"smpp": h.smppSender,
		"http": h.httpSender,
	}, time.Minute)

	h.msgSvc, err = messaging.NewService(e.Pool, h.msgRepo, messaging.DefaultConfig(), h.routingEng, h.ledgerSvc, kr, e.Audit, h.compSender, logger)
	if err != nil {
		t.Fatalf("messaging service: %v", err)
	}
	h.dispatcher = messaging.NewDispatcher(e.Pool, h.msgRepo, h.routingEng, h.ledgerSvc, h.compSender, logger)

	edgeAuth := customeredge.NewEdgeAuthenticator(e.Pool, nil, logger)
	edgeLimiter := customeredge.NewCustomerLimiter()
	edgeHandler := customeredge.NewEdgeHandler(edgeAuth, edgeLimiter, h.msgSvc, h.msgRepo, h.ledgerSvc, h.ratingSvc, kr, e.Pool)
	edgeHandler.SetAllowLoopback("test", allowLoopback)

	h.fiberApp = fiber.New(fiber.Config{
		ErrorHandler: httpx.ErrorHandler(logger),
	})
	v1 := h.fiberApp.Group("/api/v1")
	edgeHandler.RegisterRoutes(v1)
	httpprovider.NewCallbackHandler(h.httpSender, h.dlrProc, logger).Register(v1)
	t.Cleanup(func() { _ = h.fiberApp.Shutdown() })

	h.smppSrv = smppserver.NewServer(smppserver.ServerConfig{
		Addr:     "127.0.0.1:0",
		SystemID: "ZNERO_E2E_GW",
	}, e.Pool, h.msgSvc, h.msgRepo, edgeLimiter, logger)
	srvCtx, cancelSrv := context.WithCancel(ctx)
	if err := h.smppSrv.Start(srvCtx); err != nil {
		t.Fatalf("smppserver start: %v", err)
	}
	t.Cleanup(func() {
		h.smppSrv.Stop()
		cancelSrv()
	})

	h.smppDLR = smppserver.NewDLRDispatcher(smppserver.DLRDispatcherConfig{
		BatchSize:     50,
		LeaseDuration: 10 * time.Second,
	}, e.Pool, h.msgRepo, h.smppSrv.Tracker(), logger)
	h.whDisp = webhook.NewDispatcher(h.msgRepo, kr, webhook.DefaultConfig(allowLoopback), logger)

	h.reaper = messaging.NewReaper(e.Pool, h.msgRepo, h.ledgerSvc, h.dlrProc, messaging.ReaperConfig{
		MessageTTL:              12 * time.Hour,
		UnknownResolutionWindow: 12 * time.Hour,
		HoldExpiry:              72 * time.Hour,
		UnmatchedDLRRetryWindow: 5 * time.Minute,
		BatchLimit:              100,
		Clock:                   h.clk.Now,
	}, logger)
	h.poller = httpprovider.NewDLRPoller(e.Pool, h.httpSender, h.dlrProc, httpprovider.PollerConfig{
		BatchSize: 50, MaxWorkers: 4, Clock: h.clk.Now,
	}, logger)

	routing.TryReload(ctx, e.Pool)
	return h
}

func (h *e2eHarness) insertSMPPProvider(ctx context.Context, srv *mocksmsc.Server, label string) (uuid.UUID, uuid.UUID) {
	_, portStr, _ := net.SplitHostPort(srv.Addr().String())
	port, _ := strconv.Atoi(portStr)
	sysID, pass := srv.Credentials()
	encPass, err := h.kr.Encrypt(pass)
	if err != nil {
		h.t.Fatalf("encrypt smpp pass: %v", err)
	}
	var provID, connID uuid.UUID
	if err := h.e.Pool.QueryRow(ctx, `
		INSERT INTO providers (code, name, provider_type, status, version)
		VALUES ($1, $2, 'smpp', 'active', 1) RETURNING id`,
		"E2E_SMPP_"+testkit.RandHex(4), label,
	).Scan(&provID); err != nil {
		h.t.Fatalf("insert smpp prov: %v", err)
	}
	if err := h.e.Pool.QueryRow(ctx, `
		INSERT INTO provider_connections (
			provider_id, name, protocol, status, version, max_tps,
			smpp_host, smpp_port, smpp_system_id, smpp_password_encrypted,
			smpp_system_type, smpp_bind_type, smpp_tls, smpp_window_size, smpp_enquire_link_sec
		) VALUES ($1, $2, 'smpp', 'active', 1, 500, '127.0.0.1', $3, $4, $5, 'SMPP', 'transceiver', false, 16, 30)
		RETURNING id`,
		provID, label+"-"+testkit.RandHex(3), port, sysID, encPass,
	).Scan(&connID); err != nil {
		h.t.Fatalf("insert smpp conn: %v", err)
	}
	_, _ = h.e.Pool.Exec(ctx, `
		INSERT INTO provider_coverage (provider_id, country_id, status, version)
		VALUES ($1, $2, 'active', 1)
		ON CONFLICT (provider_id, country_id, COALESCE(operator_id, '00000000-0000-0000-0000-000000000000'::uuid)) DO UPDATE SET status = 'active'`,
		provID, h.countryID,
	)
	return provID, connID
}

func (h *e2eHarness) createPlanForConns(connIDs []uuid.UUID, label string) uuid.UUID {
	ctx := context.Background()
	p, err := h.routingSvc.CreatePlan(ctx, h.e.Override, &routing.RoutePlan{
		Name:     "E2E Plan " + label + " " + testkit.RandHex(3),
		Code:     "E2EP_" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})
	if err != nil {
		h.t.Fatalf("create plan %s: %v", label, err)
	}
	for idx, cid := range connIDs {
		_, _, err = h.routingSvc.CreateEntry(ctx, h.e.Override, &routing.RoutePlanEntry{
			PlanID: p.ID, ConnectionID: cid, ScopeType: "global", Priority: idx + 1, Weight: 100, Status: "active",
		})
		if err != nil {
			h.t.Fatalf("create entry %s[%d]: %v", label, idx, err)
		}
	}
	if _, _, err := h.routingSvc.RequestPlanActivation(ctx, h.e.Override, p.ID); err != nil {
		h.t.Fatalf("activate plan %s: %v", label, err)
	}
	return p.ID
}

func (h *e2eHarness) provisionCustomer(label, mode string, waitH int, noDlr, failPolicy string, planID uuid.UUID) *e2eCustomer {
	ctx := context.Background()
	custOrgID, _ := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "e2e-"+label+"-"+testkit.RandHex(3), "customer")
	if _, err := h.e.Pool.Exec(ctx, `
		INSERT INTO customer_profiles (
			organization_id, account_type, billing_currency, default_max_tps,
			billing_mode, dlr_wait_hours, no_dlr_policy, failed_after_accept_policy, status, version
		) VALUES ($1, 'prepaid', 'USD', 1000, $2, $3, $4, $5, 'active', 1)`,
		custOrgID, mode, waitH, noDlr, failPolicy,
	); err != nil {
		h.t.Fatalf("insert customer profile %s: %v", label, err)
	}
	w, err := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrgID, "USD", "prepaid")
	if err != nil {
		h.t.Fatalf("ensure wallet %s: %v", label, err)
	}
	topupAmt := decimal.NewFromInt(100)
	if _, _, err := h.ledgerSvc.Topup(ctx, h.e.Override, w.ID, ledger.WalletTopupRequest{
		Amount:         topupAmt,
		Reason:         "e2e initial topup",
		IdempotencyKey: "e2e-topup-" + custOrgID.String(),
	}, "127.0.0.1", "e2e"); err != nil {
		h.t.Fatalf("topup %s: %v", label, err)
	}

	var cardID uuid.UUID
	_ = h.e.Pool.QueryRow(ctx, `
		INSERT INTO rate_cards (name, kind, owner_org_id, currency_code, status, version)
		VALUES ($1, 'customer_sell', $2, 'USD', 'active', 1) RETURNING id`,
		"E2E Card "+label+" "+testkit.RandHex(3), h.e.PlatformOrgID,
	).Scan(&cardID)
	_, _ = h.e.Pool.Exec(ctx, `
		INSERT INTO rates (card_id, country_id, message_type, price, effective_from, status, version)
		VALUES ($1, $2, 'transactional', 0.050000, now() - interval '1 hour', 'active', 1)`,
		cardID, h.countryID,
	)
	_, _ = h.e.Pool.Exec(ctx, `
		INSERT INTO customer_rate_assignments (customer_org_id, card_id, effective_from, status, version)
		VALUES ($1, $2, now() - interval '1 hour', 'active', 1)`,
		custOrgID, cardID,
	)
	if _, _, err := h.routingSvc.CreateAssignment(ctx, h.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: custOrgID, PlanID: planID, Priority: 1, Status: "active",
	}); err != nil {
		h.t.Fatalf("assign plan %s: %v", label, err)
	}

	rawKey := "znk_live_" + testkit.RandHex(24)
	_, _ = h.e.Pool.Exec(ctx, `
		INSERT INTO customer_api_keys (organization_id, label, key_prefix, key_hash, scopes, status)
		VALUES ($1, 'e2e-key', $2, $3, ARRAY['sms:send','sms:read','sms:read_content','account:read','inbound:read'], 'active')`,
		custOrgID, rawKey[:17], hashAPIKey(rawKey),
	)

	encWh, _ := h.kr.Encrypt(h.custWhSecret)
	eventsJSON := `["message.delivered","message.undelivered","message.failed","message.expired","message.rejected"]`
	_, _ = h.e.Pool.Exec(ctx, `
		INSERT INTO customer_webhooks (customer_org_id, url, secret_encrypted, secret_key_id, events, status, version)
		VALUES ($1, $2, $3, $4, $5::jsonb, 'active', 1)`,
		custOrgID, h.custWhSrv.URL, encWh, secrets.DefaultKeyID, eventsJSON,
	)

	sysID := "e2esmp_" + testkit.RandHex(4)
	smppPw := "esmePw_" + testkit.RandHex(8)
	pwHash, _ := password.Hash(smppPw)
	var acctID uuid.UUID
	_ = h.e.Pool.QueryRow(ctx, `
		INSERT INTO customer_smpp_accounts (organization_id, system_id, password_hash, allowed_bind_types, max_binds, max_tps, status)
		VALUES ($1, $2, $3, ARRAY['transceiver'], 2, 500, 'active') RETURNING id`,
		custOrgID, sysID, pwHash,
	).Scan(&acctID)

	routing.TryReload(ctx, h.e.Pool)
	return &e2eCustomer{
		orgID:      custOrgID,
		walletID:   w.ID,
		apiKey:     rawKey,
		whSecret:   h.custWhSecret,
		smppSysID:  sysID,
		smppPass:   smppPw,
		smppAcctID: acctID,
		planID:     planID,
		topupUSD:   topupAmt,
	}
}

func (h *e2eHarness) sendHTTP(cust *e2eCustomer, dest, body string) uuid.UUID {
	h.t.Helper()
	b, _ := json.Marshal(map[string]any{
		"destination":      dest,
		"sender_id":        "ZNERO",
		"text":             body,
		"client_reference": "e2e-" + testkit.RandHex(6),
	})
	req := httptest.NewRequest("POST", "/api/v1/customer/sms", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cust.apiKey)
	resp, reqErr := h.fiberApp.Test(req, 10000)
	if reqErr != nil {
		h.t.Fatalf("HTTP send request err: %v", reqErr)
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 201 {
		h.t.Fatalf("HTTP customer send failed: status=%d body=%s", resp.StatusCode, string(respBytes))
	}
	var doc struct {
		ID uuid.UUID `json:"id"`
	}
	_ = json.Unmarshal(respBytes, &doc)
	return doc.ID
}

func (h *e2eHarness) dialESME(cust *e2eCustomer) net.Conn {
	h.t.Helper()
	conn, err := net.DialTimeout("tcp", h.smppSrv.Addr(), 3*time.Second)
	if err != nil {
		h.t.Fatalf("dial customer SMPP server: %v", err)
	}
	if err := pdu.WritePDU(conn, pdu.NewBind(pdu.CmdBindTransceiver, 1, cust.smppSysID, cust.smppPass, "SMPP")); err != nil {
		_ = conn.Close()
		h.t.Fatalf("esme write bind: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	respPDU, err := pdu.ReadPDU(conn)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.Close()
		h.t.Fatalf("esme read bind_resp: %v", err)
	}
	if br, ok := respPDU.(*pdu.BindResp); !ok || br.Header.CommandStatus != pdu.StatusROK {
		_ = conn.Close()
		h.t.Fatalf("esme bind rejected: %+v", respPDU)
	}
	return conn
}

func (h *e2eHarness) sendSMPP(conn net.Conn, seq uint32, dest, text string) uuid.UUID {
	h.t.Helper()
	sub := pdu.NewSubmitSM(seq)
	sub.SourceAddr = "ZNERO"
	sub.DestAddr = strings.TrimPrefix(dest, "+")
	sub.ShortMessage = []byte(text)
	sub.RegisteredDelivery = 1
	if err := pdu.WritePDU(conn, sub); err != nil {
		h.t.Fatalf("esme submit_sm: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	respP, err := pdu.ReadPDU(conn)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		h.t.Fatalf("esme submit_sm_resp: %v", err)
	}
	sr, ok := respP.(*pdu.SubmitSMResp)
	if !ok || sr.Header.CommandStatus != pdu.StatusROK {
		h.t.Fatalf("esme submit_sm rejected: %+v", respP)
	}
	msgID, parseErr := uuid.Parse(sr.MessageID)
	if parseErr != nil {
		h.t.Fatalf("expected UUID message_id from SMPP server, got %q: %v", sr.MessageID, parseErr)
	}
	return msgID
}

func (h *e2eHarness) deliverCustomerSMPPDLRAndWait(conn net.Conn, wantSubstr string) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.smppDLR.ProcessBatch(ctx)
	}()
	defer wg.Wait()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	dlrPDU, err := pdu.ReadPDU(conn)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		h.t.Fatalf("read customer deliver_sm: %v", err)
	}
	d, ok := dlrPDU.(*pdu.DeliverSM)
	if !ok || !strings.Contains(string(d.ShortMessage), wantSubstr) {
		h.t.Fatalf("unexpected customer DLR PDU (wanted %q): %+v", wantSubstr, dlrPDU)
	}
	if err := pdu.WritePDU(conn, pdu.NewDeliverSMResp(pdu.StatusROK, d.Header.SequenceNumber)); err != nil {
		h.t.Fatalf("write deliver_sm_resp: %v", err)
	}
}

func (h *e2eHarness) waitMessageStatus(msgID uuid.UUID, wantStatus, desc string) {
	h.t.Helper()
	eventually(h.t, 5*time.Second, desc, func() bool {
		ctx := context.Background()
		var st string
		_ = h.e.Pool.QueryRow(ctx, `SELECT status FROM messages WHERE id = $1`, msgID).Scan(&st)
		if st == wantStatus {
			return true
		}
		h.reaper.RunSweep(ctx)
		_ = h.e.Pool.QueryRow(ctx, `SELECT status FROM messages WHERE id = $1`, msgID).Scan(&st)
		return st == wantStatus
	})
}

func (h *e2eHarness) waitWebhook(msgID uuid.UUID, wantStatus string) {
	h.t.Helper()
	eventually(h.t, 4*time.Second, "webhook dispatch", func() bool {
		h.whDisp.DispatchPending(context.Background())
		h.whMu.Lock()
		defer h.whMu.Unlock()
		for _, ev := range h.receivedWH[msgID] {
			if ev.Status == wantStatus {
				return true
			}
		}
		return false
	})
}

func (h *e2eHarness) sendHTTPCallbackDLR(provMsgID, status, errCode string) {
	h.t.Helper()
	cbPayload, _ := json.Marshal(map[string]any{
		"provider_message_id": provMsgID,
		"status":              status,
		"error_code":          errCode,
	})
	cbReq := httptest.NewRequest("POST", "/api/v1/provider-callbacks/dlr/"+h.connHTTPCbID.String(), bytes.NewReader(cbPayload))
	cbReq.Header.Set("Content-Type", "application/json")
	cbReq.Header.Set("X-Provider-Signature", signHMACSHA256(h.httpCbSecret, cbPayload))
	cbResp, err := h.fiberApp.Test(cbReq, 5000)
	if err != nil {
		h.t.Fatalf("HTTP callback request failed: err=%v", err)
	}
	defer cbResp.Body.Close()
	if cbResp.StatusCode != 200 {
		cbBody, _ := io.ReadAll(cbResp.Body)
		h.t.Fatalf("HTTP callback failed: status=%d body=%s", cbResp.StatusCode, string(cbBody))
	}
}

func (h *e2eHarness) assertMsgState(msgID uuid.UUID, cust *e2eCustomer, wantStatus, wantHoldStatus, wantAvail, wantHeld string) {
	h.t.Helper()
	var st, hst string
	err := h.e.Pool.QueryRow(context.Background(), `
		SELECT m.status, h.status FROM messages m JOIN holds h ON h.id = m.hold_id WHERE m.id = $1`, msgID).Scan(&st, &hst)
	if err != nil {
		h.t.Fatalf("query msg %s state: %v", msgID, err)
	}
	if st != wantStatus || hst != wantHoldStatus {
		h.t.Fatalf("msg %s: got status=%q hold=%q, want status=%q hold=%q", msgID, st, hst, wantStatus, wantHoldStatus)
	}
	w, wErr := h.ledgerSvc.GetWallet(context.Background(), h.e.Override, cust.walletID)
	if wErr != nil {
		h.t.Fatalf("get wallet %s: %v", cust.walletID, wErr)
	}
	if w.AvailableBalance.StringFixed(2) != wantAvail || w.ActiveHolds.StringFixed(2) != wantHeld {
		h.t.Fatalf("wallet %s: got avail=%s held=%s, want avail=%s held=%s",
			cust.walletID, w.AvailableBalance.StringFixed(2), w.ActiveHolds.StringFixed(2), wantAvail, wantHeld)
	}
}

func (h *e2eHarness) assertEventSequence(msgID uuid.UUID, wantStatuses ...string) {
	h.t.Helper()
	rows, err := h.e.Pool.Query(context.Background(), `
		SELECT to_status FROM message_events WHERE message_id = $1 ORDER BY created_at ASC`, msgID)
	if err != nil {
		h.t.Fatalf("query message_events for %s: %v", msgID, err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		got = append(got, s)
	}
	idx := 0
	for _, s := range got {
		if idx < len(wantStatuses) && s == wantStatuses[idx] {
			idx++
		}
	}
	if idx != len(wantStatuses) {
		h.t.Fatalf("message_events sequence mismatch for %s: got=%v want_subseq=%v", msgID, got, wantStatuses)
	}
}

func (h *e2eHarness) assertLedgerBalancedForCustomer(cust *e2eCustomer) {
	h.t.Helper()
	ctx := context.Background()
	rows, err := h.e.Pool.Query(ctx, `
		SELECT lt.id,
		       COALESCE(SUM(CASE WHEN le.direction = 'debit' THEN le.amount ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN le.direction = 'credit' THEN le.amount ELSE 0 END), 0)
		FROM ledger_transactions lt
		JOIN ledger_entries le ON le.transaction_id = lt.id
		WHERE lt.tenant_org_id = $1
		GROUP BY lt.id`, cust.orgID)
	if err != nil {
		h.t.Fatalf("ledger tx check %s: %v", cust.orgID, err)
	}
	defer rows.Close()
	txCount := 0
	for rows.Next() {
		txCount++
		var id uuid.UUID
		var deb, cred decimal.Decimal
		if err := rows.Scan(&id, &deb, &cred); err != nil {
			h.t.Fatalf("scan ledger tx: %v", err)
		}
		if !deb.Equal(cred) {
			h.t.Fatalf("UNBALANCED LEDGER tx=%s deb=%s cred=%s", id, deb, cred)
		}
	}
	if txCount == 0 {
		h.t.Fatalf("expected ledger transactions for customer %s, got 0", cust.orgID)
	}
}

func (h *e2eHarness) assertCustomerAPISafe(cust *e2eCustomer, msgID uuid.UUID, wantTerms messaging.CustomerBillingTerms) {
	h.t.Helper()
	// 1. GET /api/v1/customer/account returns billing_terms accurately and zero provider/cost/margin data.
	accReq := httptest.NewRequest("GET", "/api/v1/customer/account", nil)
	accReq.Header.Set("Authorization", "Bearer "+cust.apiKey)
	accResp, err := h.fiberApp.Test(accReq, 5000)
	if err != nil || accResp.StatusCode != 200 {
		h.t.Fatalf("GET /customer/account failed: err=%v status=%v", err, accResp)
	}
	accBytes, _ := io.ReadAll(accResp.Body)
	_ = accResp.Body.Close()
	accLower := strings.ToLower(string(accBytes))
	for _, forbidden := range []string{"provider", "buy_price", "margin", "cost", "connection_id"} {
		if strings.Contains(accLower, forbidden) {
			h.t.Fatalf("LEAKAGE in /customer/account (%q): %s", forbidden, string(accBytes))
		}
	}
	var accDoc struct {
		BillingTerms messaging.CustomerBillingTerms `json:"billing_terms"`
	}
	if err := json.Unmarshal(accBytes, &accDoc); err != nil {
		h.t.Fatalf("decode /customer/account: %v", err)
	}
	if accDoc.BillingTerms.BillingMode != wantTerms.BillingMode ||
		accDoc.BillingTerms.NoDLRPolicy != wantTerms.NoDLRPolicy {
		h.t.Fatalf("billing_terms mismatch in /customer/account: got=%+v want=%+v", accDoc.BillingTerms, wantTerms)
	}

	// 2. GET /api/v1/customer/sms/:id returns zero provider/cost/margin data.
	msgReq := httptest.NewRequest("GET", "/api/v1/customer/sms/"+msgID.String(), nil)
	msgReq.Header.Set("Authorization", "Bearer "+cust.apiKey)
	msgResp, err := h.fiberApp.Test(msgReq, 5000)
	if err != nil || msgResp.StatusCode != 200 {
		h.t.Fatalf("GET /customer/sms/%s failed: err=%v status=%v", msgID, err, msgResp)
	}
	msgBytes, _ := io.ReadAll(msgResp.Body)
	_ = msgResp.Body.Close()
	msgLower := strings.ToLower(string(msgBytes))
	for _, forbidden := range []string{"provider", "buy_price", "margin", "cost", "connection_id"} {
		if strings.Contains(msgLower, forbidden) {
			h.t.Fatalf("LEAKAGE in /customer/sms/:id (%q): %s", forbidden, string(msgBytes))
		}
	}
}

func (h *e2eHarness) assertReconcileZero() {
	h.t.Helper()
	reconcileSvc := messaging.NewReconciliationService(h.e.Pool, h.msgRepo, h.ledgerSvc, nil)
	report, err := reconcileSvc.RunReconciliation(context.Background(), nil)
	if err != nil {
		h.t.Fatalf("RunReconciliation: %v", err)
	}
	if report.DiscrepancyCount != 0 || !report.LedgerMatches || report.UnmatchedDLRsCount != 0 {
		h.t.Fatalf("RECONCILIATION FAILED: discrepancies=%d ledger_matches=%v unmatched=%d details=%+v",
			report.DiscrepancyCount, report.LedgerMatches, report.UnmatchedDLRsCount, report.Discrepancies)
	}
	h.t.Logf("Reconciliation clean: checked=%d terminal=%d in_flight=%d discrepancies=0 ledger_matches=true unmatched=0",
		report.MessagesChecked, report.TerminalCount, report.InFlightCount)
}

func (h *e2eHarness) assertNoSecretLeaks(extraSecrets ...string) {
	h.t.Helper()
	h.lc.ScanForLeaks(h.t)
	logs := h.lc.Output()
	for _, sec := range append(extraSecrets, h.provSMPPPass, h.httpAuthToken, h.httpCbSecret, h.custWhSecret) {
		if sec != "" && strings.Contains(logs, sec) {
			h.t.Fatalf("SECURITY: secret %q leaked into logs", sec)
		}
	}
}
