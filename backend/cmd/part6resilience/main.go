// Command part6resilience exercises live OS-process resilience scenarios:
//   - db-proxy: runs a toggleable TCP forwarder in front of Postgres (:5432), launches a dedicated znero-api OS process,
//     verifies /healthz stays 200 and /readyz drops to 503 during DB outage and auto-recovers to 200 + serves traffic.
//   - chaos-run: drives live HTTP + SMPP traffic across Kafka outage/restart, flapping HTTP/SMPP carrier errors/garbage/timeouts,
//     customer TPS 429 backpressure, 0 duplicate carrier sends, SQL ledger balance, and 0 reconciliation discrepancies.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type blackboxState struct {
	CountryID     string `json:"country_id"`
	SuperToken    string `json:"super_token"`
	ResellerOrgID string `json:"reseller_org_id"`
	SellCardID    string `json:"sell_card_id"`
	Cust1OrgID    string `json:"cust1_org_id"`
	Cust1APIKey   string `json:"cust1_api_key"`
	Cust2OrgID    string `json:"cust2_org_id"`
	Cust2APIKey   string `json:"cust2_api_key"`
}

func httpReq(client *http.Client, method, url, bearer string, body any) (int, http.Header, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return 0, nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, resp.Header, raw, nil
}

// runDBProxyTest launches a dedicated znero.exe -role=api pointed at a TCP forwarder (:proxyPort -> 127.0.0.1:5432),
// severs forwarding briefly, verifies /healthz (200) vs /readyz (503 database down), restores forwarding, and verifies auto-recovery.
func runDBProxyTest(zneroExe, dbURL string) {
	parsedDB, err := url.Parse(dbURL)
	if err != nil || parsedDB.Host == "" {
		log.Fatalf("invalid db-url %q: %v", dbURL, err)
	}
	uHost := parsedDB.Host
	if strings.HasPrefix(uHost, "localhost:") {
		uHost = "127.0.0.1:" + strings.TrimPrefix(uHost, "localhost:")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("listen db tcp proxy: %v", err)
	}
	defer ln.Close()
	proxyAddr := ln.Addr().String()

	var blackout atomic.Bool
	var connsMu sync.Mutex
	activeConns := make(map[net.Conn]struct{})

	closeActive := func() {
		connsMu.Lock()
		for c := range activeConns {
			_ = c.Close()
			delete(activeConns, c)
		}
		connsMu.Unlock()
	}

	go func() {
		for {
			cli, aErr := ln.Accept()
			if aErr != nil {
				return
			}
			if blackout.Load() {
				_ = cli.Close()
				continue
			}
			srv, dErr := net.DialTimeout("tcp", uHost, 2*time.Second)
			if dErr != nil {
				_ = cli.Close()
				continue
			}
			connsMu.Lock()
			activeConns[cli] = struct{}{}
			activeConns[srv] = struct{}{}
			connsMu.Unlock()
			go func(a, b net.Conn) {
				defer func() {
					connsMu.Lock()
					delete(activeConns, a)
					delete(activeConns, b)
					connsMu.Unlock()
					_ = a.Close()
					_ = b.Close()
				}()
				_, _ = io.Copy(a, b)
			}(cli, srv)
			go func(a, b net.Conn) {
				_, _ = io.Copy(b, a)
				_ = b.Close()
			}(cli, srv)
		}
	}()

	// Pick free port for dedicated isolated znero-api instance
	apiLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("pick api port: %v", err)
	}
	_, apiPortStr, _ := net.SplitHostPort(apiLn.Addr().String())
	_ = apiLn.Close()

	parsedDB.Host = proxyAddr
	proxyDBURL := parsedDB.String()

	cmd := exec.Command(zneroExe, "-role=api")
	env := os.Environ()
	env = append(env,
		"APP_PORT="+apiPortStr,
		"APP_HOST=127.0.0.1",
		"APP_ENV=test",
		"ALLOW_LOOPBACK_IN_TEST=true",
		"DATABASE_URL="+proxyDBURL,
		"DB_NAME=znero_test",
		"DB_MAX_CONNS=5",
		"READY_CHECK_TIMEOUT=500ms",
		"CUSTOMER_SMPP_ADDR=127.0.0.1:0",
	)
	cmd.Env = env
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	if err := cmd.Start(); err != nil {
		log.Fatalf("start isolated znero-api: %v", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	}()

	baseURL := "http://127.0.0.1:" + apiPortStr
	hc := &http.Client{Timeout: 3 * time.Second}

	// 1. Wait until /readyz == 200
	deadline := time.Now().Add(20 * time.Second)
	readyOK := false
	for time.Now().Before(deadline) {
		st, _, _, rErr := httpReq(hc, http.MethodGet, baseURL+"/readyz", "", nil)
		if rErr == nil && st == 200 {
			readyOK = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !readyOK {
		log.Fatalf("isolated api did not become ready within 20s; logs:\n%s", outBuf.String())
	}
	log.Printf("[DB-PROXY 1/3] Isolated OS znero-api ready via proxy %s (/readyz=200)", proxyAddr)

	// 2. Inject brief DB blackout (2.5s) -> /healthz MUST remain 200, /readyz MUST return 503 reporting database down!
	blackout.Store(true)
	closeActive()
	time.Sleep(300 * time.Millisecond)

	hzSt, _, _, err := httpReq(hc, http.MethodGet, baseURL+"/healthz", "", nil)
	if err != nil || hzSt != 200 {
		log.Fatalf("expected /healthz=200 during DB blackout, got %d err=%v", hzSt, err)
	}
	rzSt, _, rzBody, err := httpReq(hc, http.MethodGet, baseURL+"/readyz", "", nil)
	if err != nil || rzSt != 503 {
		log.Fatalf("expected /readyz=503 during DB blackout, got %d err=%v body=%s", rzSt, err, string(rzBody))
	}
	if !strings.Contains(string(rzBody), "database") {
		log.Fatalf("expected /readyz 503 body to report database failure, got: %s", string(rzBody))
	}
	log.Printf("[DB-PROXY 2/3] During 2.5s DB outage: /healthz=%d (alive) /readyz=%d (database down)", hzSt, rzSt)

	// 3. Restore proxy forwarding -> /readyz MUST auto-recover to 200 WITHOUT process restart
	time.Sleep(2200 * time.Millisecond)
	blackout.Store(false)

	recDeadline := time.Now().Add(15 * time.Second)
	recovered := false
	for time.Now().Before(recDeadline) {
		st, _, _, rErr := httpReq(hc, http.MethodGet, baseURL+"/readyz", "", nil)
		if rErr == nil && st == 200 {
			recovered = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !recovered {
		log.Fatalf("isolated api failed to recover /readyz=200 after DB restored; logs:\n%s", outBuf.String())
	}
	log.Printf("[DB-PROXY 3/3] Auto-recovered after DB restoration: /readyz=200 (0 restarts required)")
}

func runRateLimit429Check(baseURL string, st *blackboxState) {
	hc := &http.Client{Timeout: 5 * time.Second}
	// Create a dedicated customer with default_max_tps = 2 to trigger deterministic 429 backpressure without saturating mock carriers
	suf := uuid.NewString()[:8]
	code, _, body, err := httpReq(hc, http.MethodPost, baseURL+"/api/v1/organizations", st.SuperToken, map[string]any{
		"name":      "Part6TPS_" + suf,
		"type":      "customer",
		"parent_id": st.ResellerOrgID,
	})
	if err != nil || (code != 200 && code != 201) {
		log.Fatalf("create 429 test org failed (%d): %v %s", code, err, string(body))
	}
	var doc map[string]any
	_ = json.Unmarshal(body, &doc)
	data := doc["data"].(map[string]any)
	orgID := data["id"].(string)

	cP, _, bP, err := httpReq(hc, http.MethodPost, baseURL+"/api/v1/customer-profiles", st.SuperToken, map[string]any{
		"organization_id":            orgID,
		"account_type":               "prepaid",
		"billing_currency":           "USD",
		"default_max_tps":            2,
		"billing_mode":               "on_delivery",
		"dlr_wait_hours":             24,
		"no_dlr_policy":              "retain_until_resolution",
		"failed_after_accept_policy": "keep_charge",
		"status":                     "active",
		"reason":                     "part6 429 test",
	})
	if err != nil || (cP != 200 && cP != 201) {
		log.Fatalf("create tps=2 profile failed (%d): %v %s", cP, err, string(bP))
	}
	// Issue API key
	cK, _, bK, err := httpReq(hc, http.MethodPost, baseURL+"/api/v1/customer-api-keys", st.SuperToken, map[string]any{
		"organization_id": orgID,
		"label":           "tps2-key",
		"scopes":          []string{"sms:send", "sms:read"},
		"reason":          "part6 429 test",
	})
	if err != nil || (cK != 200 && cK != 201) {
		log.Fatalf("create tps2 key failed (%d): %v %s", cK, err, string(bK))
	}
	var kDoc map[string]any
	_ = json.Unmarshal(bK, &kDoc)
	rawKey := kDoc["data"].(map[string]any)["plaintext_key"].(string)

	got429 := false
	gotRetryAfter := ""
	for i := 0; i < 12; i++ {
		sc, hdr, b, _ := httpReq(hc, http.MethodPost, baseURL+"/api/v1/customer/sms", rawKey, map[string]any{
			"destination": "+12025550999",
			"sender_id":   "ZNERO",
			"text":        "tps burst probe",
		})
		if sc >= 500 {
			log.Fatalf("5xx during rate-limit burst: %d %s", sc, string(b))
		}
		if sc == 429 {
			got429 = true
			gotRetryAfter = hdr.Get("Retry-After")
			break
		}
	}
	if !got429 || gotRetryAfter == "" {
		log.Fatalf("expected 429 Too Many Requests with Retry-After header from live znero-api, got429=%v retryAfter=%q", got429, gotRetryAfter)
	}
	log.Printf("[PASS] Customer Edge 429 backpressure enforced on live znero-api (Retry-After=%s)", gotRetryAfter)
}

func submitMsg(hc *http.Client, baseURL, apiKey, text string) (string, int) {
	sc, _, body, err := httpReq(hc, http.MethodPost, baseURL+"/api/v1/customer/sms", apiKey, map[string]any{
		"destination": "+12025550177",
		"sender_id":   "ZNERO",
		"text":        text,
	})
	if err != nil {
		return "", 0
	}
	var doc map[string]any
	_ = json.Unmarshal(body, &doc)
	id, _ := doc["id"].(string)
	return id, sc
}

func checkDuplicatesAndReconcile(baseURL, httpCtrl, smppCtrl, dbURL string, st *blackboxState, expectedHTTPTexts, expectedSMPPTexts []string) {
	hc := &http.Client{Timeout: 8 * time.Second}

	// Wait up to 35s for submitted messages to leave dispatching/queued/accepted
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("pgxpool: %v", err)
	}
	defer pool.Close()

	dl := time.Now().Add(35 * time.Second)
	for time.Now().Before(dl) {
		var pendingCnt int
		_ = pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM messages
			WHERE customer_org_id IN ($1, $2)
			  AND status IN ('queued','dispatching','retry_pending','provider_accepted','sent')`,
			st.Cust1OrgID, st.Cust2OrgID).Scan(&pendingCnt)
		if pendingCnt == 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Verify 0 duplicates per message text on HTTP mock & SMPP mock
	_, _, rawHTTP, err := httpReq(hc, http.MethodGet, httpCtrl+"/ctrl/requests", "", nil)
	if err != nil {
		log.Fatalf("fetch e2ehttpprov /ctrl/requests: %v", err)
	}
	var httpRecs []struct {
		ProviderMsgID string `json:"provider_message_id"`
		Text          string `json:"text"`
		Ref           string `json:"ref"`
	}
	_ = json.Unmarshal(rawHTTP, &httpRecs)
	httpByText := make(map[string]int)
	for _, r := range httpRecs {
		httpByText[r.Text]++
	}
	for _, txt := range expectedHTTPTexts {
		if httpByText[txt] > 1 {
			log.Fatalf("DUPLICATE HTTP SEND DETECTED for %q: count=%d", txt, httpByText[txt])
		}
		if httpByText[txt] == 0 {
			log.Fatalf("MISSING HTTP DELIVERY for %q: count=0", txt)
		}
	}

	_, _, rawSMPP, err := httpReq(hc, http.MethodGet, smppCtrl+"/requests", "", nil)
	if err != nil {
		log.Fatalf("fetch smppmock /requests: %v", err)
	}
	var smppRecs []struct {
		MessageID string `json:"message_id"`
		Text      string `json:"text"`
	}
	_ = json.Unmarshal(rawSMPP, &smppRecs)
	smppByText := make(map[string]int)
	for _, r := range smppRecs {
		smppByText[r.Text]++
	}
	for _, txt := range expectedSMPPTexts {
		if smppByText[txt] > 1 {
			log.Fatalf("DUPLICATE SMPP SEND DETECTED for %q: count=%d", txt, smppByText[txt])
		}
		if smppByText[txt] == 0 {
			log.Fatalf("MISSING SMPP DELIVERY for %q: count=0", txt)
		}
	}
	log.Printf("[PASS] 0 duplicate carrier sends verified across %d HTTP and %d SMPP resilient messages", len(expectedHTTPTexts), len(expectedSMPPTexts))

	// Direct SQL ledger double-entry & wallet invariant check
	for _, orgIDStr := range []string{st.Cust1OrgID, st.Cust2OrgID} {
		oid, _ := uuid.Parse(orgIDStr)
		rows, qErr := pool.Query(ctx, `
			SELECT lt.id,
			       COALESCE(SUM(CASE WHEN le.direction = 'debit' THEN le.amount ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN le.direction = 'credit' THEN le.amount ELSE 0 END), 0)
			FROM ledger_transactions lt
			JOIN ledger_entries le ON le.transaction_id = lt.id
			WHERE lt.tenant_org_id = $1
			GROUP BY lt.id`, oid)
		if qErr != nil {
			log.Fatalf("query ledger balance: %v", qErr)
		}
		for rows.Next() {
			var txID uuid.UUID
			var deb, cred decimal.Decimal
			_ = rows.Scan(&txID, &deb, &cred)
			if !deb.Equal(cred) {
				log.Fatalf("SQL LEDGER UNBALANCED tx=%s deb=%s cred=%s", txID, deb, cred)
			}
		}
		rows.Close()

		var cached, held, avail decimal.Decimal
		if err := pool.QueryRow(ctx, `
			SELECT w.cached_balance,
			       COALESCE((SELECT SUM(h.amount) FROM holds h WHERE h.wallet_id = w.id AND h.status = 'active'), 0),
			       w.cached_balance - COALESCE((SELECT SUM(h.amount) FROM holds h WHERE h.wallet_id = w.id AND h.status = 'active'), 0)
			FROM wallets w WHERE w.organization_id = $1`, oid).Scan(&cached, &held, &avail); err != nil {
			log.Fatalf("query wallet math: %v", err)
		}
		if !avail.Add(held).Equal(cached) {
			log.Fatalf("WALLET INVARIANT BROKEN: avail=%s held=%s cached=%s", avail, held, cached)
		}
	}

	// Reconciliation API check
	sc, _, rawRec, err := httpReq(hc, http.MethodPost, baseURL+"/api/v1/messaging/reconciliation/run", st.SuperToken, map[string]any{})
	if err != nil || sc != 200 {
		log.Fatalf("reconciliation run failed (%d): %v %s", sc, err, string(rawRec))
	}
	var recDoc map[string]any
	_ = json.Unmarshal(rawRec, &recDoc)
	recData, _ := recDoc["data"].(map[string]any)
	if recData == nil {
		recData = recDoc
	}
	disc, _ := recData["discrepancy_count"].(float64)
	lOk, _ := recData["ledger_matches"].(bool)
	if int(disc) != 0 || !lOk {
		log.Fatalf("RECONCILIATION FAILED AFTER CHAOS: %s", string(rawRec))
	}
	log.Printf("[PASS] Reconciliation clean after all chaos scenarios: discrepancy_count=0 ledger_matches=true")
}

func main() {
	log.SetOutput(os.Stdout)
	mode := flag.String("mode", "", "db-proxy | burst-429 | send-batch | set-http-mode | set-smpp-ctrl | verify-chaos | probe-url")
	zneroExe := flag.String("znero-exe", filepath.Join("bin", "znero.exe"), "Path to znero.exe")
	dbURL := flag.String("db-url", os.Getenv("TEST_DATABASE_URL"), "PostgreSQL URL")
	baseURL := flag.String("base-url", "http://127.0.0.1:8080", "API base URL")
	stateFile := flag.String("state-file", ".tmp/bb_state.json", "Blackbox state JSON path")
	httpCtrl := flag.String("http-ctrl", "http://127.0.0.1:19102", "e2ehttpprov control base URL")
	smppCtrl := flag.String("smpp-ctrl", "http://127.0.0.1:19101", "smppmock control base URL")
	channel := flag.String("channel", "http", "http | smpp")
	prefix := flag.String("prefix", "P6", "Message text prefix")
	count := flag.Int("count", 5, "Batch count")
	httpMode := flag.String("http-mode", "ok", "ok|502|429|html|binary|slow")
	autoCB := flag.Bool("auto-cb", true, "Enable auto_callback on e2ehttpprov")
	smppErr := flag.String("smpp-err", "0x00", "SMPP mock error code")
	smppLatency := flag.Int("smpp-latency-ms", 0, "SMPP mock latency ms")
	probeURL := flag.String("probe-url", "", "URL to HTTP GET")
	expectCode := flag.Int("expect-code", 200, "Expected HTTP status code")
	expectedFile := flag.String("expected-file", ".tmp/p6_expected.json", "File tracking sent non-chaos texts")
	flag.Parse()

	loadSt := func() *blackboxState {
		raw, err := os.ReadFile(*stateFile)
		if err != nil {
			log.Fatalf("read state file %s: %v", *stateFile, err)
		}
		var st blackboxState
		if err := json.Unmarshal(raw, &st); err != nil {
			log.Fatalf("parse state file: %v", err)
		}
		return &st
	}

	switch *mode {
	case "db-proxy":
		runDBProxyTest(*zneroExe, *dbURL)
	case "burst-429":
		runRateLimit429Check(*baseURL, loadSt())
	case "probe-url":
		hc := &http.Client{Timeout: 4 * time.Second}
		sc, _, b, err := httpReq(hc, http.MethodGet, *probeURL, "", nil)
		if err != nil || sc != *expectCode {
			log.Fatalf("probe %s expected %d, got %d err=%v body=%s", *probeURL, *expectCode, sc, err, string(b))
		}
		log.Printf("[PASS] Probe %s -> HTTP %d", *probeURL, sc)
	case "set-http-mode":
		hc := &http.Client{Timeout: 4 * time.Second}
		sc, _, b, err := httpReq(hc, http.MethodPost, *httpCtrl+"/ctrl/mode", "", map[string]any{
			"mode":          *httpMode,
			"auto_callback": *autoCB,
		})
		if err != nil || sc != 200 {
			log.Fatalf("set-http-mode %s failed (%d): %v %s", *httpMode, sc, err, string(b))
		}
	case "set-smpp-ctrl":
		hc := &http.Client{Timeout: 4 * time.Second}
		sc, _, b, err := httpReq(hc, http.MethodPost, *smppCtrl+"/ctrl", "", map[string]any{
			"error_code": *smppErr,
			"latency_ms": *smppLatency,
		})
		if err != nil || sc != 200 {
			log.Fatalf("set-smpp-ctrl failed (%d): %v %s", sc, err, string(b))
		}
	case "send-batch":
		st := loadSt()
		hc := &http.Client{Timeout: 5 * time.Second}
		key := st.Cust2APIKey
		if *channel == "smpp" {
			key = st.Cust1APIKey
		}
		var doc struct {
			HTTPTexts []string `json:"http_texts"`
			SMPPTexts []string `json:"smpp_texts"`
		}
		if b, err := os.ReadFile(*expectedFile); err == nil {
			_ = json.Unmarshal(b, &doc)
		}
		for i := 0; i < *count; i++ {
			txt := fmt.Sprintf("%s-%s-%d-%s", *prefix, *channel, i, uuid.NewString()[:8])
			id, sc := submitMsg(hc, *baseURL, key, txt)
			if sc >= 500 || sc == 0 {
				log.Fatalf("submission %s failed with %d", txt, sc)
			}
			if *httpMode == "ok" && *smppErr == "0x00" && id != "" && (sc == 200 || sc == 201 || sc == 202) {
				if *channel == "smpp" {
					doc.SMPPTexts = append(doc.SMPPTexts, txt)
				} else {
					doc.HTTPTexts = append(doc.HTTPTexts, txt)
				}
			}
		}
		bOut, _ := json.MarshalIndent(doc, "", "  ")
		_ = os.WriteFile(*expectedFile, bOut, 0644)
		log.Printf("[PASS] Sent %d %s msgs (prefix=%s)", *count, *channel, *prefix)
	case "verify-chaos":
		var doc struct {
			HTTPTexts []string `json:"http_texts"`
			SMPPTexts []string `json:"smpp_texts"`
		}
		if b, err := os.ReadFile(*expectedFile); err == nil {
			_ = json.Unmarshal(b, &doc)
		}
		checkDuplicatesAndReconcile(*baseURL, *httpCtrl, *smppCtrl, *dbURL, loadSt(), doc.HTTPTexts, doc.SMPPTexts)
	default:
		log.Fatalf("unknown -mode=%q", *mode)
	}
}
