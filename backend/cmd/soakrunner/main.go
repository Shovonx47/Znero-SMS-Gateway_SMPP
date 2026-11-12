// Command soakrunner orchestrates the Part 1 isolated >=120-minute background soak against copied bin\soak\ processes.
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/znero-international/znero/backend/internal/smpp/mocksmsc"
	"github.com/znero-international/znero/backend/internal/smpp/pdu"
)

type soakCustomer struct {
	OrgID      string `json:"org_id"`
	WalletID   string `json:"wallet_id"`
	APIKey     string `json:"api_key"`
	SMPPSys    string `json:"smpp_sys"`
	SMPPPass   string `json:"smpp_pass"`
	Route      string `json:"route"`
	Billing    string `json:"billing_mode"`
	NoDLR      string `json:"no_dlr_policy"`
	FailAfter  string `json:"failed_after_accept_policy"`
	WebhookID  string `json:"webhook_id"`
	WebhookSec string `json:"webhook_secret"`
}

type soakSample struct {
	Timestamp             string         `json:"timestamp"`
	ElapsedMin            float64        `json:"elapsed_min"`
	HTTPSubmitted         int64          `json:"http_submitted"`
	HTTPErrors            int64          `json:"http_errors"`
	SMPPSubmitted         int64          `json:"smpp_submitted"`
	SMPPErrors            int64          `json:"smpp_errors"`
	SMPPRebinds           int64          `json:"smpp_rebinds"`
	CarrierHTTPReqs       int64          `json:"carrier_http_reqs"`
	CarrierSMPPReqs       int64          `json:"carrier_smpp_reqs"`
	WebhookCallbacks      int64          `json:"webhook_callbacks"`
	DBActiveConns         int            `json:"db_active_conns"`
	DBTotalConns          int            `json:"db_total_conns"`
	OutboxPending         int            `json:"outbox_pending"`
	OutboxProcessing      int            `json:"outbox_processing"`
	OutboxFailed          int            `json:"outbox_failed"`
	OutboxOldestAgeSec    float64        `json:"outbox_oldest_age_sec"`
	SMPPDLRQueuePend      int            `json:"smpp_dlr_queue_pending"`
	WebhookDeliveriesPend int            `json:"webhook_deliveries_pending"`
	SettlementExceptions  int            `json:"settlement_exceptions"`
	MsgStatusCounts       map[string]int `json:"msg_status_counts"`
	Goroutines            map[string]int `json:"goroutines"`
	HeapAllocMB           map[string]int `json:"heap_alloc_mb"`
	ProcessRSSMB          map[string]int `json:"process_rss_mb"`
	UnbalancedTxs         int            `json:"unbalanced_txs"`
	NegPrepaidWallets     int            `json:"neg_prepaid_wallets"`
	DeliveredActiveHolds  int            `json:"delivered_active_holds"`
	RejectedCapturedHolds int            `json:"rejected_captured_holds"`
	InvariantOK           bool           `json:"invariant_ok"`
	EarlyP50Ms            float64        `json:"early_p50_ms"`
	EarlyP95Ms            float64        `json:"early_p95_ms"`
	RecentWindowP50Ms     float64        `json:"recent_window_p50_ms"`
	RecentWindowP95Ms     float64        `json:"recent_window_p95_ms"`
}

type apiClient struct {
	baseURL string
	http    *http.Client
}

func (c *apiClient) doJSON(method, path, token string, body any) (int, map[string]any, error) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out, nil
}

func (c *apiClient) mustJSON(method, path, token string, body any, want ...int) map[string]any {
	st, out, err := c.doJSON(method, path, token, body)
	if err != nil {
		log.Fatalf("%s %s err: %v", method, path, err)
	}
	for _, w := range want {
		if st == w {
			if data, ok := out["data"].(map[string]any); ok {
				return data
			}
			return out
		}
	}
	b, _ := json.Marshal(out)
	log.Fatalf("%s %s status=%d (want %v) body=%s", method, path, st, want, string(b))
	return nil
}

func strField(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

// startSoakHTTPCarrier launches an in-process HTTP carrier simulator returning randomized outcomes:
// ~65% DELIVRD (50..1800ms), ~15% UNDELIV (100..1500ms), ~10% no-receipt, ~5% 503 reject, ~5% slow hang (3..7s).
func startSoakHTTPCarrier(addr string, authTok string, cbSecret string, cbURLFn func() string, reqCounter *atomic.Int64) *http.Server {
	var rngMu sync.Mutex
	rng := rand.New(rand.NewSource(2026111299))
	nextRoll := func() int {
		rngMu.Lock()
		v := rng.Intn(100)
		rngMu.Unlock()
		return v
	}
	nextDelay := func(minMs, spanMs int) time.Duration {
		rngMu.Lock()
		v := minMs + rng.Intn(spanMs)
		rngMu.Unlock()
		return time.Duration(v) * time.Millisecond
	}

	var idSeq atomic.Int64
	var records sync.Map // provID -> map[string]any for /poll

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/send", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+authTok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.ReadAll(io.LimitReader(r.Body, 64*1024))
		reqCounter.Add(1)
		roll := nextRoll()
		if roll < 5 {
			// Transient upstream 503 failure
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"error","error":"upstream_busy"}`))
			return
		}
		if roll < 10 {
			// Occasional slow provider hang (exceeds 5s request_timeout_ms half the time, slow 3.5s the other)
			if roll%2 == 0 {
				time.Sleep(6200 * time.Millisecond)
			} else {
				time.Sleep(3200 * time.Millisecond)
			}
		}

		provID := fmt.Sprintf("SKHTTP-%d-%d", time.Now().UnixNano(), idSeq.Add(1))
		cbTarget := cbURLFn()
		switch {
		case roll >= 10 && roll < 20:
			// ~10% no-receipt case (never fires callback; tests dlr_wait_hours & reaper!)
			records.Store(provID, map[string]any{"provider_message_id": provID, "status": "PENDING", "error_code": ""})
		case roll >= 20 && roll < 35:
			// ~15% UNDELIV failure after randomized delay
			records.Store(provID, map[string]any{"provider_message_id": provID, "status": "UNDELIV", "error_code": "027"})
			delay := nextDelay(100, 1400)
			go fireHTTPCallback(cbTarget, cbSecret, provID, "UNDELIV", "027", delay)
		default:
			// ~65% DELIVRD after randomized delay
			records.Store(provID, map[string]any{"provider_message_id": provID, "status": "DELIVRD", "error_code": "000"})
			delay := nextDelay(60, 1800)
			go fireHTTPCallback(cbTarget, cbSecret, provID, "DELIVRD", "000", delay)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprintf(w, `{"status":"ok","data":{"id":%q}}`, provID)
	})
	mux.HandleFunc("/poll", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		val, ok := records.Load(id)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(val)
	})

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[SOAK HTTP CARRIER ERR] %v", err)
		}
	}()
	return srv
}

var cbHTTPClient = &http.Client{Timeout: 8 * time.Second}

func fireHTTPCallback(urlStr, secret, provID, status, errCode string, delay time.Duration) {
	if urlStr == "" {
		return
	}
	time.Sleep(delay)
	payload, _ := json.Marshal(map[string]string{
		"provider_message_id": provID,
		"status":              status,
		"error_code":          errCode,
	})
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	sig := hex.EncodeToString(mac.Sum(nil))
	req, err := http.NewRequest(http.MethodPost, urlStr, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Provider-Signature", sig)
	resp, err := cbHTTPClient.Do(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

func startWebhookReceiver(addr string, count *atomic.Int64) *http.Server {
	var rngMu sync.Mutex
	rng := rand.New(rand.NewSource(2026111277))
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 256*1024))
		count.Add(1)
		rngMu.Lock()
		roll := rng.Intn(100)
		rngMu.Unlock()
		if roll < 15 {
			// 15% webhook failure -> triggers webhook retries/backoff
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"simulated_webhook_500"}`))
			return
		}
		if roll < 20 {
			// 5% slow webhook failure
			time.Sleep(1500 * time.Millisecond)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[SOAK WEBHOOK RX ERR] %v", err)
		}
	}()
	return srv
}

var (
	reGoroutineHdr = regexp.MustCompile(`goroutine profile: total (\d+)`)
	reHeapAlloc    = regexp.MustCompile(`#\s+HeapAlloc\s*=\s*(\d+)`)
)

func fetchPprofStats(addr string) (goroutines int, heapMB int) {
	hc := &http.Client{Timeout: 3 * time.Second}
	if r, err := hc.Get("http://" + addr + "/debug/pprof/goroutine?debug=1"); err == nil {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		_ = r.Body.Close()
		if m := reGoroutineHdr.FindSubmatch(b); len(m) == 2 {
			goroutines, _ = strconv.Atoi(string(m[1]))
		}
	}
	if r, err := hc.Get("http://" + addr + "/debug/pprof/heap?debug=1"); err == nil {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 256*1024))
		_ = r.Body.Close()
		if m := reHeapAlloc.FindSubmatch(b); len(m) == 2 {
			v, _ := strconv.ParseUint(string(m[1]), 10, 64)
			heapMB = int(v / (1024 * 1024))
		}
	}
	return goroutines, heapMB
}

func fetchPIDsRSS(pidsFile string) map[string]int {
	res := make(map[string]int)
	raw, err := os.ReadFile(pidsFile)
	if err != nil {
		return res
	}
	var pm map[string]int
	if err := json.Unmarshal(raw, &pm); err != nil || len(pm) == 0 {
		return res
	}
	var ids []string
	rev := make(map[int]string)
	for role, pid := range pm {
		if pid > 0 {
			ids = append(ids, strconv.Itoa(pid))
			rev[pid] = role
		}
	}
	if len(ids) == 0 {
		return res
	}
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		fmt.Sprintf(`Get-Process -Id %s -ErrorAction SilentlyContinue | ForEach-Object { "$($_.Id):$([math]::Round($_.WorkingSet64/1MB))" }`, strings.Join(ids, ",")))
	out, err := cmd.Output()
	if err != nil {
		return res
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			pid, _ := strconv.Atoi(parts[0])
			mb, _ := strconv.Atoi(parts[1])
			if role, ok := rev[pid]; ok {
				res[role] = mb
			}
		}
	}
	return res
}

func pct5095(vals []int64) (float64, float64) {
	if len(vals) == 0 {
		return 0, 0
	}
	cp := make([]int64, len(vals))
	copy(cp, vals)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	p50 := float64(cp[int(float64(len(cp)-1)*0.50)]) / 1e6
	p95 := float64(cp[int(float64(len(cp)-1)*0.95)]) / 1e6
	return p50, p95
}

func dialBindESME(smppAddr, sysID, pass string) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", smppAddr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	if err := pdu.WritePDU(conn, pdu.NewBind(pdu.CmdBindTransceiver, 1, sysID, pass, "SMPP")); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := pdu.ReadPDU(conn)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	br, ok := resp.(*pdu.BindResp)
	if !ok || br.Header.CommandStatus != pdu.StatusROK {
		_ = conn.Close()
		return nil, fmt.Errorf("bind failed: %+v", resp)
	}
	return conn, nil
}

func sendHTTP(hc *http.Client, baseURL, apiKey, tag string) error {
	payload := fmt.Sprintf(`{"destination":"+12025550101","sender_id":"ZNERO","text":%q,"dlr_requested":true}`, tag)
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/customer/sms", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 && resp.StatusCode != 202 {
		return fmt.Errorf("status=%d", resp.StatusCode)
	}
	return nil
}

func main() {
	log.SetOutput(os.Stdout)
	apiURL := flag.String("api-url", "http://127.0.0.1:18180", "Soak API URL")
	smppAddr := flag.String("smpp-addr", "127.0.0.1:12776", "Soak customer SMPP server addr")
	provSMPPAddr := flag.String("prov-smpp-addr", "127.0.0.1:19200", "Soak mock SMSC addr")
	provHTTPAddr := flag.String("prov-http-addr", "127.0.0.1:19202", "Soak HTTP carrier mock addr")
	webhookAddr := flag.String("webhook-addr", "127.0.0.1:19203", "Soak webhook receiver addr")
	dbURL := flag.String("db-url", "", "Soak PostgreSQL URL (znero_soak)")
	pidsFile := flag.String("pids-file", ".run/soak/pids.json", "Soak PIDs JSON file")
	outJSONL := flag.String("telemetry-file", "docs/soak-telemetry.jsonl", "5-minute sample JSONL output")
	summaryOut := flag.String("summary-file", ".tmp/soak_summary.json", "Final soak summary JSON output")
	duration := flag.Duration("duration", 125*time.Minute, "Soak duration (>=120m)")
	sampleInterval := flag.Duration("sample-interval", 5*time.Minute, "Metrics & ledger invariant sample interval")
	targetTPS := flag.Int("tps", 50, "Total soak target TPS (split 50/50 HTTP and SMPP)")
	flag.Parse()

	if *dbURL == "" || !strings.Contains(*dbURL, "/znero_soak") {
		log.Fatalf("SAFETY GUARD: -db-url must target znero_soak")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start Soak SMPP Carrier Mock with rolling chaos
	smppSys := "SOAK_SMSC"
	smppPass := "soakPass123"
	smsc, err := mocksmsc.NewServer(mocksmsc.ServerConfig{
		Addr:              *provSMPPAddr,
		SystemID:          smppSys,
		Password:          smppPass,
		DLRMode:           mocksmsc.DLRModeImmediate,
		DLRDelay:          700 * time.Millisecond,
		ReplayUnackedDLRs: true,
	})
	if err != nil {
		log.Fatalf("mocksmsc new: %v", err)
	}
	if err := smsc.Start(); err != nil {
		log.Fatalf("mocksmsc start: %v", err)
	}
	defer smsc.Close()

	var carrierHTTPReqs, webhookCallbacks atomic.Int64
	var cbTarget atomic.Value
	cbTarget.Store("")
	httpCarrier := startSoakHTTPCarrier(*provHTTPAddr, "soakHttpTok123!", "soakHttpCbSec123!", func() string {
		s, _ := cbTarget.Load().(string)
		return s
	}, &carrierHTTPReqs)
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = httpCarrier.Shutdown(c)
	}()

	whRx := startWebhookReceiver(*webhookAddr, &webhookCallbacks)
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = whRx.Shutdown(c)
	}()

	// Wait for soak API readyz before setup
	api := &apiClient{baseURL: strings.TrimRight(*apiURL, "/"), http: &http.Client{Timeout: 20 * time.Second}}
	log.Printf("[SOAK] Waiting for soak API at %s...", api.baseURL)
	for i := 0; i < 60; i++ {
		if r, e := api.http.Get(api.baseURL + "/readyz"); e == nil {
			_ = r.Body.Close()
			if r.StatusCode == 200 {
				break
			}
		}
		if i == 59 {
			log.Fatalf("soak API failed to report ready")
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Provision soak providers, routes & 6 customers covering all billing modes & both upstreams
	login := api.mustJSON(http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"email": "dev-superadmin@dev.znero.test", "password": "DevSeed1234!#",
	}, http.StatusOK)
	superTok := strField(login, "access_token")

	countries := api.mustJSON(http.MethodGet, "/api/v1/countries?search=United+States&page=1&page_size=100", superTok, nil, http.StatusOK)
	items, _ := countries["items"].([]any)
	var countryID string
	for _, it := range items {
		m, _ := it.(map[string]any)
		if strings.EqualFold(strField(m, "iso2"), "US") {
			countryID = strField(m, "id")
			break
		}
	}
	if countryID == "" {
		log.Fatalf("US country not found")
	}

	suffix := fmt.Sprintf("sk%d", time.Now().Unix()%100000)
	smppHost, smppPortStr, _ := net.SplitHostPort(*provSMPPAddr)
	smppPort, _ := strconv.Atoi(smppPortStr)

	pSMPP := api.mustJSON(http.MethodPost, "/api/v1/providers", superTok, map[string]any{
		"name": "Soak SMPP " + suffix, "code": "SK_SMPP_" + strings.ToUpper(suffix),
		"provider_type": "smpp", "country_id": countryID, "currency_code": "USD", "status": "active", "reason": "soak",
	}, http.StatusOK)
	provSMPPID := strField(pSMPP, "id")
	connSMPP := api.mustJSON(http.MethodPost, "/api/v1/providers/"+provSMPPID+"/connections", superTok, map[string]any{
		"name": "Soak-SMPP-Conn", "protocol": "smpp", "status": "active", "max_tps": 5000,
		"smpp_host": smppHost, "smpp_port": smppPort, "smpp_system_id": smppSys, "smpp_password": smppPass,
		"smpp_tls": false, "smpp_bind_type": "transceiver", "smpp_window_size": 128, "smpp_enquire_link_sec": 30,
		"reason": "soak",
	}, http.StatusOK)
	connSMPPID := strField(connSMPP, "id")
	api.mustJSON(http.MethodPost, "/api/v1/provider-coverage", superTok, map[string]any{
		"provider_id": provSMPPID, "country_id": countryID, "status": "active", "reason": "soak",
	}, http.StatusOK)
	smppBuy := api.mustJSON(http.MethodPost, "/api/v1/rates/cards", superTok, map[string]any{
		"name": "Soak SMPP Buy " + suffix, "kind": "provider_buy", "provider_id": provSMPPID,
		"currency_code": "USD", "status": "active", "reason": "soak",
	}, http.StatusCreated)
	for _, mt := range []string{"default", "transactional"} {
		api.mustJSON(http.MethodPost, "/api/v1/rates/cards/"+strField(smppBuy, "id")+"/rates", superTok, map[string]any{
			"country_id": countryID, "message_type": mt, "price": "0.0200",
			"effective_from": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), "reason": "soak",
		}, http.StatusCreated)
	}

	pHTTP := api.mustJSON(http.MethodPost, "/api/v1/providers", superTok, map[string]any{
		"name": "Soak HTTP " + suffix, "code": "SK_HTTP_" + strings.ToUpper(suffix),
		"provider_type": "http", "country_id": countryID, "currency_code": "USD", "status": "active", "reason": "soak",
	}, http.StatusOK)
	provHTTPID := strField(pHTTP, "id")
	connHTTP := api.mustJSON(http.MethodPost, "/api/v1/providers/"+provHTTPID+"/connections", superTok, map[string]any{
		"name": "Soak-HTTP-Conn", "protocol": "http", "status": "active", "max_tps": 5000,
		"http_base_url": "http://" + *provHTTPAddr + "/send", "http_auth_type": "bearer", "http_credentials": "soakHttpTok123!",
		"http_dlr_mode": "callback", "http_dlr_secret": "soakHttpCbSec123!",
		"http_config": map[string]any{
			"method": "POST", "body_format": "json",
			"body_template":    `{"to":"{{destination}}","from":"{{sender}}","text":"{{text}}","ref":"{{message_id}}"}`,
			"success_http_min": 200, "success_http_max": 299,
			"provider_msg_id_path": "data.id", "success_json_path": "status", "success_equals": "ok",
			"request_timeout_ms": 4500, "callback_msg_id_path": "provider_message_id",
			"callback_status_path": "status", "callback_error_code_path": "error_code",
			"callback_signature_header": "X-Provider-Signature", "callback_signature_algo": "hmac_sha256",
		},
		"reason": "soak",
	}, http.StatusOK)
	connHTTPID := strField(connHTTP, "id")
	cbTarget.Store(api.baseURL + "/api/v1/provider-callbacks/dlr/" + connHTTPID)

	api.mustJSON(http.MethodPost, "/api/v1/provider-coverage", superTok, map[string]any{
		"provider_id": provHTTPID, "country_id": countryID, "status": "active", "reason": "soak",
	}, http.StatusOK)
	httpBuy := api.mustJSON(http.MethodPost, "/api/v1/rates/cards", superTok, map[string]any{
		"name": "Soak HTTP Buy " + suffix, "kind": "provider_buy", "provider_id": provHTTPID,
		"currency_code": "USD", "status": "active", "reason": "soak",
	}, http.StatusCreated)
	for _, mt := range []string{"default", "transactional"} {
		api.mustJSON(http.MethodPost, "/api/v1/rates/cards/"+strField(httpBuy, "id")+"/rates", superTok, map[string]any{
			"country_id": countryID, "message_type": mt, "price": "0.0200",
			"effective_from": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), "reason": "soak",
		}, http.StatusCreated)
	}

	makePlan := func(label, connID string) string {
		pl := api.mustJSON(http.MethodPost, "/api/v1/routing/plans", superTok, map[string]any{
			"name": "Soak Plan " + label + " " + suffix, "code": "SKP_" + label + "_" + suffix, "strategy": "priority",
		}, http.StatusCreated)
		pid := strField(pl, "id")
		api.mustJSON(http.MethodPost, "/api/v1/routing/plans/"+pid+"/entries", superTok, map[string]any{
			"connection_id": connID, "scope_type": "global", "priority": 1, "weight": 100, "status": "active",
		}, http.StatusCreated)
		api.mustJSON(http.MethodPost, "/api/v1/routing/plans/"+pid+"/activate", superTok, map[string]any{"reason": "soak"}, http.StatusOK)
		return pid
	}
	smppPlanID := makePlan("SMPP", connSMPPID)
	httpPlanID := makePlan("HTTP", connHTTPID)

	resDoc := api.mustJSON(http.MethodPost, "/api/v1/organizations", superTok, map[string]any{
		"name": "SoakReseller_" + suffix, "type": "reseller",
	}, http.StatusCreated, http.StatusOK)
	resellerID := strField(resDoc, "id")
	sellCard := api.mustJSON(http.MethodPost, "/api/v1/rates/cards", superTok, map[string]any{
		"name": "Soak Sell " + suffix, "kind": "customer_sell", "owner_org_id": resellerID,
		"currency_code": "USD", "status": "active", "reason": "soak",
	}, http.StatusCreated)
	sellCardID := strField(sellCard, "id")
	for _, mt := range []string{"default", "transactional"} {
		api.mustJSON(http.MethodPost, "/api/v1/rates/cards/"+sellCardID+"/rates", superTok, map[string]any{
			"country_id": countryID, "message_type": mt, "price": "0.0400",
			"effective_from": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), "reason": "soak",
		}, http.StatusCreated)
	}

	profiles := []struct {
		route, billing, nodlr, failAfter string
		waitH                            int
	}{
		{"http", "on_delivery", "retain_until_resolution", "keep_charge", 24},
		{"smpp", "on_delivery", "immediate_release", "keep_charge", 1},
		{"http", "on_delivery", "capture_at_ttl", "keep_charge", 1},
		{"smpp", "on_provider_accept", "retain_until_resolution", "keep_charge", 24},
		{"http", "on_provider_accept", "retain_until_resolution", "refund", 24},
		{"smpp", "on_provider_accept", "immediate_release", "refund", 1},
	}

	var customers []soakCustomer
	for i, pf := range profiles {
		planID := httpPlanID
		if pf.route == "smpp" {
			planID = smppPlanID
		}
		orgDoc := api.mustJSON(http.MethodPost, "/api/v1/organizations", superTok, map[string]any{
			"name": fmt.Sprintf("SoakCust_%d_%s_%s", i, pf.route, suffix), "type": "customer", "parent_id": resellerID,
		}, http.StatusCreated, http.StatusOK)
		orgID := strField(orgDoc, "id")
		api.mustJSON(http.MethodPost, "/api/v1/customer-profiles", superTok, map[string]any{
			"organization_id": orgID, "account_type": "prepaid", "billing_currency": "USD",
			"default_max_tps": 50000, "daily_message_cap": 10000000, "monthly_message_cap": 50000000,
			"billing_mode": pf.billing, "dlr_wait_hours": pf.waitH,
			"no_dlr_policy": pf.nodlr, "failed_after_accept_policy": pf.failAfter,
			"status": "active", "reason": "soak",
		}, http.StatusCreated, http.StatusOK)
		api.mustJSON(http.MethodPost, "/api/v1/sender-ids", superTok, map[string]any{
			"organization_id": orgID, "sender_id": "ZNERO", "sender_type": "alphanumeric", "status": "approved", "reason": "soak",
		}, http.StatusCreated, http.StatusOK)
		api.mustJSON(http.MethodPost, "/api/v1/rates/assignments", superTok, map[string]any{
			"target_org_id": orgID, "card_id": sellCardID,
			"effective_from": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), "reason": "soak",
		}, http.StatusCreated, http.StatusOK)
		wDoc := api.mustJSON(http.MethodPost, "/api/v1/wallets", superTok, map[string]any{
			"organization_id": orgID, "currency": "USD", "account_type": "prepaid",
		}, http.StatusOK, http.StatusCreated)
		wID := strField(wDoc, "id")
		api.mustJSON(http.MethodPost, "/api/v1/wallets/"+wID+"/topup", superTok, map[string]any{
			"amount": "100000.0000", "idempotency_key": "sk-top-" + suffix + "-" + strconv.Itoa(i), "reason": "soak funding",
		}, http.StatusOK)
		akDoc := api.mustJSON(http.MethodPost, "/api/v1/customer-api-keys", superTok, map[string]any{
			"organization_id": orgID, "label": "soak-key", "scopes": []string{"sms:send", "sms:read", "account:read"}, "reason": "soak",
		}, http.StatusCreated, http.StatusOK)
		apiKey := strField(akDoc, "plaintext_key")
		sysID := fmt.Sprintf("sk_%s_%d", suffix, i)
		smDoc := api.mustJSON(http.MethodPost, "/api/v1/customer-smpp-accounts", superTok, map[string]any{
			"organization_id": orgID, "system_id": sysID, "allowed_bind_types": []string{"transceiver", "transmitter", "receiver"},
			"max_binds": 10, "max_tps": 50000, "status": "active", "reason": "soak",
		}, http.StatusCreated, http.StatusOK)
		smPass := strField(smDoc, "plaintext_password")
		api.mustJSON(http.MethodPost, "/api/v1/routing/assignments", superTok, map[string]any{
			"customer_org_id": orgID, "plan_id": planID, "priority": 1, "status": "active",
		}, http.StatusCreated)

		whDoc := api.mustJSON(http.MethodPost, "/api/v1/customer/webhooks", apiKey, map[string]any{
			"url":    fmt.Sprintf("http://%s/hook/%d", *webhookAddr, i),
			"events": []string{"message.delivered", "message.failed", "message.undelivered", "message.expired", "message.submitted"},
		}, http.StatusCreated, http.StatusOK)

		customers = append(customers, soakCustomer{
			OrgID: orgID, WalletID: wID, APIKey: apiKey, SMPPSys: sysID, SMPPPass: smPass,
			Route: pf.route, Billing: pf.billing, NoDLR: pf.nodlr, FailAfter: pf.failAfter,
			WebhookID: strField(whDoc, "id"), WebhookSec: strField(whDoc, "secret"),
		})
	}
	api.mustJSON(http.MethodPost, "/api/v1/routing/snapshot/reload", superTok, map[string]any{}, http.StatusOK)
	time.Sleep(6 * time.Second)
	log.Printf("[SOAK SETUP] Provisioned 6 soak customers across SMPP+HTTP carriers and all billing policies; starting %s soak @ %d TPS...", *duration, *targetTPS)

	pool, err := pgxpool.New(ctx, *dbURL)
	if err != nil {
		log.Fatalf("pgxpool: %v", err)
	}
	defer pool.Close()

	soakCtx, cancelSoak := context.WithTimeout(ctx, *duration)
	defer cancelSoak()
	soakStart := time.Now()

	// Dynamic SMPP carrier chaos loop (~10s ticks)
	go func() {
		r := rand.New(rand.NewSource(2026111255))
		tk := time.NewTicker(10 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-soakCtx.Done():
				return
			case <-tk.C:
				roll := r.Intn(100)
				smsc.SetInjectedError(0)
				switch {
				case roll < 5:
					// Occasional SMPP throttle burst
					smsc.SetInjectedError(0x58)
					smsc.SetDLRMode(mocksmsc.DLRModeImmediate)
				case roll < 15:
					// No-receipt window (tests Reaper!)
					smsc.SetDLRMode(mocksmsc.DLRModeLost)
				case roll < 30:
					// Failed DLR window
					smsc.SetDLRMode(mocksmsc.DLRModeUndeliverable)
				case roll < 50:
					// Delayed DLR window
					smsc.SetDLRMode(mocksmsc.DLRModeDelayed)
				default:
					smsc.SetDLRMode(mocksmsc.DLRModeImmediate)
				}
			}
		}
	}()

	var httpSent, httpErrs, smppSent, smppErrs, smppRebinds atomic.Int64
	var latMu sync.Mutex
	var earlyLatNs []int64
	var windowLatNs []int64

	recordLat := func(ns int64) {
		latMu.Lock()
		if time.Since(soakStart) <= 15*time.Minute && len(earlyLatNs) < 60000 {
			earlyLatNs = append(earlyLatNs, ns)
		}
		if len(windowLatNs) < 30000 {
			windowLatNs = append(windowLatNs, ns)
		}
		latMu.Unlock()
	}

	httpTPS := *targetTPS / 2
	smppTPS := *targetTPS - httpTPS
	if httpTPS < 1 {
		httpTPS = 1
	}
	if smppTPS < 1 {
		smppTPS = 1
	}

	var loadWg sync.WaitGroup
	// 2 HTTP generator workers splitting httpTPS
	hc := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{MaxIdleConns: 64, MaxIdleConnsPerHost: 64}}
	for w := 0; w < 2; w++ {
		loadWg.Add(1)
		go func(wid int) {
			defer loadWg.Done()
			perWorker := httpTPS / 2
			if perWorker < 1 {
				perWorker = 1
			}
			tk := time.NewTicker(time.Second / time.Duration(perWorker))
			defer tk.Stop()
			var idx int64
			for {
				select {
				case <-soakCtx.Done():
					return
				case <-tk.C:
					i := idx
					idx++
					cust := customers[(int(i)+wid)%len(customers)]
					t0 := time.Now()
					err := sendHTTP(hc, api.baseURL, cust.APIKey, fmt.Sprintf("SOAK-H-%d-%d", wid, i))
					recordLat(time.Since(t0).Nanoseconds())
					if err != nil {
						if httpErrs.Add(1) <= 10 {
							log.Printf("[SOAK HTTP ERR] %v", err)
						}
					} else {
						httpSent.Add(1)
					}
				}
			}
		}(w)
	}

	// 2 SMPP ESME generator workers splitting smppTPS
	for w := 0; w < 2; w++ {
		loadWg.Add(1)
		go func(wid int) {
			defer loadWg.Done()
			cust := customers[(wid*3+1)%len(customers)]
			perWorker := smppTPS / 2
			if perWorker < 1 {
				perWorker = 1
			}
			var conn net.Conn
			var seq uint32 = 100
			tk := time.NewTicker(time.Second / time.Duration(perWorker))
			defer tk.Stop()
			defer func() {
				if conn != nil {
					_ = pdu.WritePDU(conn, pdu.NewUnbind(999999))
					_ = conn.Close()
				}
			}()
			for {
				select {
				case <-soakCtx.Done():
					return
				case <-tk.C:
					if conn == nil {
						c, err := dialBindESME(*smppAddr, cust.SMPPSys, cust.SMPPPass)
						if err != nil {
							if smppErrs.Add(1) <= 10 {
								log.Printf("[SOAK SMPP BIND ERR] %v", err)
							}
							continue
						}
						conn = c
					}
					seq++
					t0 := time.Now()
					sub := pdu.NewSubmitSM(seq)
					sub.DestAddr = "12025550202"
					sub.SourceAddr = "ZNERO"
					sub.DataCoding = pdu.DataCodingSMSCDefault
					sub.RegisteredDelivery = 1
					sub.ShortMessage = []byte(fmt.Sprintf("SOAK-S-%d-%d", wid, seq))
					if err := pdu.WritePDU(conn, sub); err != nil {
						_ = conn.Close()
						conn = nil
						smppRebinds.Add(1)
						smppErrs.Add(1)
						continue
					}
					_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
					var submitErr error
					for {
						p, err := pdu.ReadPDU(conn)
						if err != nil {
							submitErr = err
							break
						}
						if d, ok := p.(*pdu.DeliverSM); ok {
							_ = pdu.WritePDU(conn, pdu.NewDeliverSMResp(pdu.StatusROK, d.Header.SequenceNumber))
							continue
						}
						if sr, ok := p.(*pdu.SubmitSMResp); ok {
							if sr.Header.CommandStatus != pdu.StatusROK {
								submitErr = fmt.Errorf("submit_sm_resp=0x%x", sr.Header.CommandStatus)
							}
							break
						}
					}
					_ = conn.SetReadDeadline(time.Time{})
					recordLat(time.Since(t0).Nanoseconds())
					if submitErr != nil {
						_ = conn.Close()
						conn = nil
						smppRebinds.Add(1)
						if smppErrs.Add(1) <= 10 {
							log.Printf("[SOAK SMPP SUBMIT ERR] %v", submitErr)
						}
					} else {
						smppSent.Add(1)
					}
				}
			}
		}(w)
	}

	_ = os.Remove(*outJSONL)
	pprofTargets := map[string]string{
		"api": "127.0.0.1:16060", "smpp": "127.0.0.1:16061", "dispatcher": "127.0.0.1:16062",
		"reaper": "127.0.0.1:16063", "webhook": "127.0.0.1:16064", "poller": "127.0.0.1:16065",
	}

	var samples []soakSample
	sampleAndVerify := func() bool {
		txCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		var activeConns, totalConns int
		_ = pool.QueryRow(txCtx, `SELECT COUNT(*) FILTER (WHERE state='active'), COUNT(*) FROM pg_stat_activity WHERE datname='znero_soak'`).Scan(&activeConns, &totalConns)

		tx, err := pool.BeginTx(txCtx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			log.Printf("[SOAK SAMPLE ERR] begin tx: %v", err)
			return true
		}
		defer tx.Rollback(txCtx)

		var obPend, obProc, obFail, dlrPend, whPend, settleEx int
		var obOldestSec float64
		_ = tx.QueryRow(txCtx, `SELECT COUNT(*) FILTER (WHERE status='pending'), COUNT(*) FILTER (WHERE status='processing'), COUNT(*) FILTER (WHERE status='failed'), COALESCE(EXTRACT(EPOCH FROM (NOW() - MIN(created_at) FILTER (WHERE status IN ('pending','processing')))), 0) FROM message_outbox`).Scan(&obPend, &obProc, &obFail, &obOldestSec)
		_ = tx.QueryRow(txCtx, `SELECT COUNT(*) FROM smpp_dlr_queue WHERE status='pending'`).Scan(&dlrPend)
		_ = tx.QueryRow(txCtx, `SELECT COUNT(*) FROM customer_webhook_deliveries WHERE status='pending'`).Scan(&whPend)
		_ = tx.QueryRow(txCtx, `SELECT COUNT(*) FROM settlement_exceptions`).Scan(&settleEx)

		msgCounts := make(map[string]int)
		if rows, qErr := tx.Query(txCtx, `SELECT status, COUNT(*) FROM messages GROUP BY status`); qErr == nil {
			for rows.Next() {
				var st string
				var cnt int
				_ = rows.Scan(&st, &cnt)
				msgCounts[st] = cnt
			}
			rows.Close()
		}

		// Atomic ledger & wallet invariants inside single REPEATABLE READ snapshot
		var unbalTxs, negWallets, delivActive, rejCap int
		_ = tx.QueryRow(txCtx, `SELECT COUNT(*) FROM (SELECT transaction_id FROM ledger_entries GROUP BY transaction_id HAVING COALESCE(SUM(CASE WHEN direction='debit' THEN amount ELSE 0 END),0) <> COALESCE(SUM(CASE WHEN direction='credit' THEN amount ELSE 0 END),0)) t`).Scan(&unbalTxs)
		_ = tx.QueryRow(txCtx, `SELECT COUNT(*) FROM wallets w WHERE w.account_type='prepaid' AND (w.cached_balance < 0 OR w.cached_balance - COALESCE((SELECT SUM(h.amount) FROM holds h WHERE h.wallet_id=w.id AND h.status='active'),0) < 0)`).Scan(&negWallets)
		_ = tx.QueryRow(txCtx, `SELECT COUNT(*) FROM messages m JOIN holds h ON h.id=m.hold_id WHERE m.status='delivered' AND h.status='active'`).Scan(&delivActive)
		_ = tx.QueryRow(txCtx, `SELECT COUNT(*) FROM messages m JOIN holds h ON h.id=m.hold_id WHERE m.status='rejected' AND h.status='captured'`).Scan(&rejCap)
		_ = tx.Commit(txCtx)

		gMap := make(map[string]int)
		hMap := make(map[string]int)
		for k, addr := range pprofTargets {
			g, h := fetchPprofStats(addr)
			gMap[k] = g
			hMap[k] = h
		}
		rssMap := fetchPIDsRSS(*pidsFile)

		latMu.Lock()
		ep50, ep95 := pct5095(earlyLatNs)
		wp50, wp95 := pct5095(windowLatNs)
		windowLatNs = windowLatNs[:0]
		latMu.Unlock()

		invOK := unbalTxs == 0 && negWallets == 0 && delivActive == 0 && rejCap == 0
		samp := soakSample{
			Timestamp:             time.Now().Format(time.RFC3339),
			ElapsedMin:            time.Since(soakStart).Minutes(),
			HTTPSubmitted:         httpSent.Load(),
			HTTPErrors:            httpErrs.Load(),
			SMPPSubmitted:         smppSent.Load(),
			SMPPErrors:            smppErrs.Load(),
			SMPPRebinds:           smppRebinds.Load(),
			CarrierHTTPReqs:       carrierHTTPReqs.Load(),
			CarrierSMPPReqs:       int64(len(smsc.GetSubmittedMessages())),
			WebhookCallbacks:      webhookCallbacks.Load(),
			DBActiveConns:         activeConns,
			DBTotalConns:          totalConns,
			OutboxPending:         obPend,
			OutboxProcessing:      obProc,
			OutboxFailed:          obFail,
			OutboxOldestAgeSec:    obOldestSec,
			SMPPDLRQueuePend:      dlrPend,
			WebhookDeliveriesPend: whPend,
			SettlementExceptions:  settleEx,
			MsgStatusCounts:       msgCounts,
			Goroutines:            gMap,
			HeapAllocMB:           hMap,
			ProcessRSSMB:          rssMap,
			UnbalancedTxs:         unbalTxs,
			NegPrepaidWallets:     negWallets,
			DeliveredActiveHolds:  delivActive,
			RejectedCapturedHolds: rejCap,
			InvariantOK:           invOK,
			EarlyP50Ms:            ep50,
			EarlyP95Ms:            ep95,
			RecentWindowP50Ms:     wp50,
			RecentWindowP95Ms:     wp95,
		}
		samples = append(samples, samp)
		line, _ := json.Marshal(samp)
		if f, oErr := os.OpenFile(*outJSONL, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); oErr == nil {
			_, _ = f.Write(append(line, '\n'))
			_ = f.Close()
		}
		log.Printf("[SOAK t=%.1fm] http=%d(err=%d) smpp=%d(err=%d,rebind=%d) ob=(%d/%d/%d age=%.1fs) wh_q=%d dlr_q=%d sett_ex=%d db=(%d/%d) g=%v heap=%v rss=%v p50=%.1fms p95=%.1fms invOK=%v",
			samp.ElapsedMin, samp.HTTPSubmitted, samp.HTTPErrors, samp.SMPPSubmitted, samp.SMPPErrors, samp.SMPPRebinds,
			obPend, obProc, obFail, obOldestSec, whPend, dlrPend, settleEx, activeConns, totalConns, gMap, hMap, rssMap, wp50, wp95, invOK)

		if !invOK {
			log.Printf("[SOAK INVARIANT VIOLATION] unbal=%d neg_wallets=%d deliv_active=%d rej_cap=%d", unbalTxs, negWallets, delivActive, rejCap)
		}
		return invOK
	}

	// Capture t=0 baseline + 5m samples
	time.Sleep(5 * time.Second)
	if !sampleAndVerify() {
		log.Fatalf("SOAK STOPPED: invariant failure at baseline")
	}

	sampTk := time.NewTicker(*sampleInterval)
	defer sampTk.Stop()
	invariantFailed := false
loop:
	for {
		select {
		case <-soakCtx.Done():
			break loop
		case <-sampTk.C:
			if !sampleAndVerify() {
				invariantFailed = true
				cancelSoak()
				break loop
			}
		}
	}
	loadWg.Wait()

	// Brief settle (up to 30s) & final reconciliation verification via API when generators stop
	time.Sleep(20 * time.Second)
	sampleAndVerify()

	fresh := api.mustJSON(http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"email": "dev-superadmin@dev.znero.test", "password": "DevSeed1234!#",
	}, http.StatusOK)
	superTok = strField(fresh, "access_token")
	rec := api.mustJSON(http.MethodPost, "/api/v1/messaging/reconciliation/run", superTok, map[string]any{}, http.StatusOK)

	sumDoc := map[string]any{
		"duration_requested": duration.String(),
		"elapsed_minutes":    time.Since(soakStart).Minutes(),
		"samples_count":      len(samples),
		"invariant_failed":   invariantFailed,
		"reconciliation":     rec,
		"final_sample":       samples[len(samples)-1],
		"decimal_sample_ref": decimal.Zero.String(),
	}
	sb, _ := json.MarshalIndent(sumDoc, "", "  ")
	_ = os.WriteFile(*summaryOut, sb, 0644)

	disc, _ := rec["discrepancy_count"].(float64)
	lOk, _ := rec["ledger_matches"].(bool)
	log.Printf("[SOAK END] elapsed=%.1fm samples=%d invFailed=%v reconcile(disc=%.0f, ledger_matches=%v)",
		time.Since(soakStart).Minutes(), len(samples), invariantFailed, disc, lOk)
	if invariantFailed || int(disc) != 0 || !lOk {
		os.Exit(1)
	}
}
