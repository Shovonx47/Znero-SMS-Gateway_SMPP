// Command loadgen runs mixed HTTP + SMPP load benchmarks and bounded continuous soak tests
// against live Znero OS processes, reporting p50/p95/p99 latencies, throughput, pprof metrics,
// memory/goroutine leak trends, DB pool stats, SQL ledger balance, and reconciliation status.
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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/znero-international/znero/backend/internal/smpp/pdu"
)

type loadCustomer struct {
	OrgID    string `json:"org_id"`
	WalletID string `json:"wallet_id"`
	APIKey   string `json:"api_key"`
	SMPPSys  string `json:"smpp_sys"`
	SMPPPass string `json:"smpp_pass"`
	Route    string `json:"route"` // smpp | http
}

type loadState struct {
	CountryID    string         `json:"country_id"`
	SuperToken   string         `json:"super_token"`
	ResellerID   string         `json:"reseller_id"`
	SMPPProvID   string         `json:"smpp_prov_id"`
	SMPPConnID   string         `json:"smpp_conn_id"`
	HTTPProvID   string         `json:"http_prov_id"`
	HTTPConnID   string         `json:"http_conn_id"`
	SMPPPlanID   string         `json:"smpp_plan_id"`
	HTTPPlanID   string         `json:"http_plan_id"`
	SellCardID   string         `json:"sell_card_id"`
	Customers    []loadCustomer `json:"customers"`
	BurstMetrics map[string]any `json:"burst_metrics,omitempty"`
	SoakSamples  []soakSample   `json:"soak_samples,omitempty"`
}

type soakSample struct {
	ElapsedSec   int            `json:"elapsed_sec"`
	SentTotal    int64          `json:"sent_total"`
	ErrorsTotal  int64          `json:"errors_total"`
	OutboxPend   int            `json:"outbox_pending"`
	DBActiveConn int            `json:"db_active_conns"`
	Goroutines   map[string]int `json:"goroutines"`
	HeapAllocMB  map[string]int `json:"heap_alloc_mb"`
	ProcessRSSMB map[string]int `json:"process_rss_mb"`
}

type apiClient struct {
	baseURL string
	http    *http.Client
}

func (c *apiClient) doJSON(method, path, token string, body any) (int, map[string]any, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, rdr)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var parsed map[string]any
	_ = json.Unmarshal(raw, &parsed)
	return resp.StatusCode, parsed, raw, nil
}

func (c *apiClient) mustJSON(method, path, token string, body any, wantCodes ...int) map[string]any {
	code, doc, raw, err := c.doJSON(method, path, token, body)
	if err != nil {
		log.Fatalf("%s %s transport error: %v", method, path, err)
	}
	for _, w := range wantCodes {
		if code == w {
			if d, ok := doc["data"].(map[string]any); ok {
				return d
			}
			return doc
		}
	}
	log.Fatalf("%s %s status=%d (wanted %v) body=%s", method, path, code, wantCodes, string(raw))
	return nil
}

func strField(m map[string]any, keys ...string) string {
	cur := m
	for i, k := range keys {
		if cur == nil {
			return ""
		}
		if i == len(keys)-1 {
			if v, ok := cur[k].(string); ok {
				return v
			}
			return ""
		}
		next, _ := cur[k].(map[string]any)
		cur = next
	}
	return ""
}

func loginSuper(c *apiClient) string {
	doc := c.mustJSON(http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"email":    "dev-superadmin@dev.znero.test",
		"password": "DevSeed1234!#",
	}, http.StatusOK)
	tok := strField(doc, "access_token")
	if tok == "" {
		log.Fatalf("superadmin login returned empty access_token")
	}
	return tok
}

func runSetup(c *apiClient, smppHost string, smppPort int, smppSys, smppPass, httpURL, httpTok, httpCbSec, webhookURL, stateFile string) {
	superTok := loginSuper(c)
	suffix := uuid.NewString()[:6]

	// Pick active US country via search endpoint
	countriesDoc := c.mustJSON(http.MethodGet, "/api/v1/countries?search=United+States&page=1&page_size=100", superTok, nil, http.StatusOK)
	var countryID string
	if items, ok := countriesDoc["items"].([]any); ok {
		for _, it := range items {
			m, _ := it.(map[string]any)
			if iso, _ := m["iso2"].(string); iso == "US" {
				countryID, _ = m["id"].(string)
				break
			}
		}
		if countryID == "" && len(items) > 0 {
			m, _ := items[0].(map[string]any)
			countryID, _ = m["id"].(string)
		}
	}
	if countryID == "" {
		iso := "L" + strings.ToUpper(suffix[:1])
		num := fmt.Sprintf("%03d", 800+time.Now().Unix()%190)
		cd := c.mustJSON(http.MethodPost, "/api/v1/countries", superTok, map[string]any{
			"name": "LoadCountry " + suffix, "iso2": iso, "iso3": iso + "X", "numeric_code": num,
			"country_calling_code": "1", "default_currency": "USD", "status": "active", "reason": "loadgen",
		}, http.StatusCreated, http.StatusOK)
		countryID = strField(cd, "id")
	}

	// Provision high-capacity SMPP carrier & HTTP carrier
	pSMPP := c.mustJSON(http.MethodPost, "/api/v1/providers", superTok, map[string]any{
		"name": "Load SMPP " + suffix, "code": "LD_SMPP_" + strings.ToUpper(suffix),
		"provider_type": "smpp", "country_id": countryID, "currency_code": "USD", "status": "active", "reason": "loadgen",
	}, http.StatusOK)
	provSMPPID := strField(pSMPP, "id")

	connSMPP := c.mustJSON(http.MethodPost, "/api/v1/providers/"+provSMPPID+"/connections", superTok, map[string]any{
		"name": "Load-SMPP-Conn-" + suffix, "protocol": "smpp", "status": "active",
		"max_tps": 50000, "smpp_host": smppHost, "smpp_port": smppPort,
		"smpp_system_id": smppSys, "smpp_password": smppPass, "smpp_tls": false,
		"smpp_bind_type": "transceiver", "smpp_window_size": 256, "smpp_enquire_link_sec": 30,
		"reason": "loadgen high window",
	}, http.StatusOK)
	connSMPPID := strField(connSMPP, "id")

	c.mustJSON(http.MethodPost, "/api/v1/provider-coverage", superTok, map[string]any{
		"provider_id": provSMPPID, "country_id": countryID, "status": "active", "reason": "loadgen",
	}, http.StatusOK)

	smppBuyCard := c.mustJSON(http.MethodPost, "/api/v1/rates/cards", superTok, map[string]any{
		"name": "Load SMPP Buy " + suffix, "kind": "provider_buy", "provider_id": provSMPPID,
		"currency_code": "USD", "status": "active", "reason": "loadgen",
	}, http.StatusCreated)
	smppBuyCardID := strField(smppBuyCard, "id")
	for _, mt := range []string{"default", "transactional"} {
		c.mustJSON(http.MethodPost, "/api/v1/rates/cards/"+smppBuyCardID+"/rates", superTok, map[string]any{
			"country_id": countryID, "message_type": mt, "price": "0.0200",
			"effective_from": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), "reason": "loadgen",
		}, http.StatusCreated)
	}

	pHTTP := c.mustJSON(http.MethodPost, "/api/v1/providers", superTok, map[string]any{
		"name": "Load HTTP " + suffix, "code": "LD_HTTP_" + strings.ToUpper(suffix),
		"provider_type": "http", "country_id": countryID, "currency_code": "USD", "status": "active", "reason": "loadgen",
	}, http.StatusOK)
	provHTTPID := strField(pHTTP, "id")

	connHTTP := c.mustJSON(http.MethodPost, "/api/v1/providers/"+provHTTPID+"/connections", superTok, map[string]any{
		"name": "Load-HTTP-Conn-" + suffix, "protocol": "http", "status": "active",
		"max_tps": 50000, "http_base_url": httpURL + "/send",
		"http_auth_type": "bearer", "http_credentials": httpTok,
		"http_dlr_mode": "callback", "http_dlr_secret": httpCbSec,
		"http_config": map[string]any{
			"method":                    "POST",
			"body_format":               "json",
			"body_template":             `{"to":"{{destination}}","from":"{{sender}}","text":"{{text}}","ref":"{{message_id}}"}`,
			"success_http_min":          200,
			"success_http_max":          299,
			"provider_msg_id_path":      "data.id",
			"success_json_path":         "status",
			"success_equals":            "ok",
			"request_timeout_ms":        5000,
			"callback_msg_id_path":      "provider_message_id",
			"callback_status_path":      "status",
			"callback_error_code_path":  "error_code",
			"callback_signature_header": "X-Provider-Signature",
			"callback_signature_algo":   "hmac_sha256",
		},
		"reason": "loadgen high concurrency",
	}, http.StatusOK)
	connHTTPID := strField(connHTTP, "id")

	c.mustJSON(http.MethodPost, "/api/v1/provider-coverage", superTok, map[string]any{
		"provider_id": provHTTPID, "country_id": countryID, "status": "active", "reason": "loadgen",
	}, http.StatusOK)

	httpBuyCard := c.mustJSON(http.MethodPost, "/api/v1/rates/cards", superTok, map[string]any{
		"name": "Load HTTP Buy " + suffix, "kind": "provider_buy", "provider_id": provHTTPID,
		"currency_code": "USD", "status": "active", "reason": "loadgen",
	}, http.StatusCreated)
	httpBuyCardID := strField(httpBuyCard, "id")
	for _, mt := range []string{"default", "transactional"} {
		c.mustJSON(http.MethodPost, "/api/v1/rates/cards/"+httpBuyCardID+"/rates", superTok, map[string]any{
			"country_id": countryID, "message_type": mt, "price": "0.0200",
			"effective_from": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), "reason": "loadgen",
		}, http.StatusCreated)
	}

	// Set e2ehttpprov callback target + auto_callback=true
	cbTarget := c.baseURL + "/api/v1/provider-callbacks/dlr/" + connHTTPID
	if r, e := c.http.Post(httpURL+"/ctrl/mode", "application/json", bytes.NewReader([]byte(fmt.Sprintf(`{"mode":"ok","auto_callback":true,"callback_url":%q,"callback_secret":%q}`, cbTarget, httpCbSec)))); e == nil {
		_ = r.Body.Close()
	}

	createPlan := func(label, connID string) string {
		pl := c.mustJSON(http.MethodPost, "/api/v1/routing/plans", superTok, map[string]any{
			"name": "Load Plan " + label + " " + suffix, "code": "LD_" + label + "_" + strings.ToUpper(suffix), "strategy": "priority",
		}, http.StatusCreated)
		pid := strField(pl, "id")
		c.mustJSON(http.MethodPost, "/api/v1/routing/plans/"+pid+"/entries", superTok, map[string]any{
			"connection_id": connID, "scope_type": "global", "priority": 1, "weight": 100, "status": "active",
		}, http.StatusCreated)
		c.mustJSON(http.MethodPost, "/api/v1/routing/plans/"+pid+"/activate", superTok, map[string]any{"reason": "loadgen"}, http.StatusOK)
		return pid
	}
	smppPlanID := createPlan("SMPP", connSMPPID)
	httpPlanID := createPlan("HTTP", connHTTPID)

	resDoc := c.mustJSON(http.MethodPost, "/api/v1/organizations", superTok, map[string]any{
		"name": "LoadReseller_" + suffix, "type": "reseller",
	}, http.StatusCreated, http.StatusOK)
	resellerID := strField(resDoc, "id")

	scDoc := c.mustJSON(http.MethodPost, "/api/v1/rates/cards", superTok, map[string]any{
		"name": "Load Sell " + suffix, "kind": "customer_sell", "owner_org_id": resellerID,
		"currency_code": "USD", "status": "active", "reason": "loadgen",
	}, http.StatusCreated)
	sellCardID := strField(scDoc, "id")
	for _, mt := range []string{"default", "transactional"} {
		c.mustJSON(http.MethodPost, "/api/v1/rates/cards/"+sellCardID+"/rates", superTok, map[string]any{
			"country_id": countryID, "message_type": mt, "price": "0.0400",
			"effective_from": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), "reason": "loadgen",
		}, http.StatusCreated)
	}

	var customers []loadCustomer
	for i := 0; i < 6; i++ {
		routeKind := "smpp"
		planID := smppPlanID
		if i >= 3 {
			routeKind = "http"
			planID = httpPlanID
		}
		bMode := "on_delivery"
		if i%2 == 1 {
			bMode = "on_provider_accept"
		}
		name := fmt.Sprintf("LoadCust_%d_%s_%s", i, routeKind, suffix)
		orgDoc := c.mustJSON(http.MethodPost, "/api/v1/organizations", superTok, map[string]any{
			"name": name, "type": "customer", "parent_id": resellerID,
		}, http.StatusCreated, http.StatusOK)
		orgID := strField(orgDoc, "id")

		c.mustJSON(http.MethodPost, "/api/v1/customer-profiles", superTok, map[string]any{
			"organization_id": orgID, "account_type": "prepaid", "billing_currency": "USD",
			"default_max_tps": 50000, "daily_message_cap": 1000000, "monthly_message_cap": 5000000,
			"billing_mode": bMode, "dlr_wait_hours": 24,
			"no_dlr_policy": "retain_until_resolution", "failed_after_accept_policy": "keep_charge",
			"status": "active", "reason": "loadgen",
		}, http.StatusCreated, http.StatusOK)

		c.mustJSON(http.MethodPost, "/api/v1/sender-ids", superTok, map[string]any{
			"organization_id": orgID, "sender_id": "ZNERO", "sender_type": "alphanumeric",
			"status": "approved", "reason": "loadgen",
		}, http.StatusCreated, http.StatusOK)

		c.mustJSON(http.MethodPost, "/api/v1/rates/assignments", superTok, map[string]any{
			"target_org_id": orgID, "card_id": sellCardID,
			"effective_from": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), "reason": "loadgen",
		}, http.StatusCreated, http.StatusOK)

		wDoc := c.mustJSON(http.MethodPost, "/api/v1/wallets", superTok, map[string]any{
			"organization_id": orgID, "currency": "USD", "account_type": "prepaid",
		}, http.StatusOK, http.StatusCreated)
		walletID := strField(wDoc, "id")
		c.mustJSON(http.MethodPost, "/api/v1/wallets/"+walletID+"/topup", superTok, map[string]any{
			"amount": "50000.0000", "idempotency_key": "load-top-" + suffix + "-" + strconv.Itoa(i), "reason": "loadgen funds",
		}, http.StatusOK)

		akDoc := c.mustJSON(http.MethodPost, "/api/v1/customer-api-keys", superTok, map[string]any{
			"organization_id": orgID, "label": "load-key", "scopes": []string{"sms:send", "sms:read", "account:read"}, "reason": "loadgen",
		}, http.StatusCreated, http.StatusOK)
		apiKey := strField(akDoc, "plaintext_key")

		sysID := fmt.Sprintf("ld_%s_%d", suffix, i)
		smDoc := c.mustJSON(http.MethodPost, "/api/v1/customer-smpp-accounts", superTok, map[string]any{
			"organization_id": orgID, "system_id": sysID, "allowed_bind_types": []string{"transceiver", "transmitter", "receiver"},
			"max_binds": 10, "max_tps": 50000, "status": "active", "reason": "loadgen",
		}, http.StatusCreated, http.StatusOK)
		smPass := strField(smDoc, "plaintext_password")

		c.mustJSON(http.MethodPost, "/api/v1/routing/assignments", superTok, map[string]any{
			"customer_org_id": orgID, "plan_id": planID, "priority": 1, "status": "active",
		}, http.StatusCreated)

		if webhookURL != "" {
			c.mustJSON(http.MethodPost, "/api/v1/customer/webhooks", apiKey, map[string]any{
				"url":    fmt.Sprintf("%s/%d", strings.TrimRight(webhookURL, "/"), i),
				"events": []string{"message.delivered", "message.failed", "message.undelivered", "message.expired"},
			}, http.StatusCreated, http.StatusOK)
		}

		customers = append(customers, loadCustomer{
			OrgID: orgID, WalletID: walletID, APIKey: apiKey, SMPPSys: sysID, SMPPPass: smPass, Route: routeKind,
		})
	}

	c.mustJSON(http.MethodPost, "/api/v1/routing/snapshot/reload", superTok, map[string]any{}, http.StatusOK)
	time.Sleep(6 * time.Second)

	st := loadState{
		CountryID: countryID, SuperToken: superTok, ResellerID: resellerID,
		SMPPProvID: provSMPPID, SMPPConnID: connSMPPID, HTTPProvID: provHTTPID, HTTPConnID: connHTTPID,
		SMPPPlanID: smppPlanID, HTTPPlanID: httpPlanID, SellCardID: sellCardID, Customers: customers,
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	if err := os.WriteFile(stateFile, b, 0644); err != nil {
		log.Fatalf("write state: %v", err)
	}
	log.Printf("[SETUP] Provisioned %d load customers (3 SMPP + 3 HTTP routes, max_tps=50000)", len(customers))
}

func percentiles(latNs []int64) (p50, p95, p99 float64) {
	if len(latNs) == 0 {
		return 0, 0, 0
	}
	cp := make([]int64, len(latNs))
	copy(cp, latNs)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	pct := func(q float64) float64 {
		idx := int(float64(len(cp)-1) * q)
		return float64(cp[idx]) / 1e6 // ms
	}
	return pct(0.50), pct(0.95), pct(0.99)
}

func sendSingleHTTP(hc *http.Client, baseURL, apiKey, tag string) error {
	payload := fmt.Sprintf(`{"destination":"+12025550101","sender_id":"ZNERO","text":"%s","dlr_requested":true}`, tag)
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
		return fmt.Errorf("http single status=%d", resp.StatusCode)
	}
	return nil
}

func sendBulkHTTP(hc *http.Client, baseURL, apiKey string, count int, tag string) error {
	var msgs []map[string]any
	for i := 0; i < count; i++ {
		msgs = append(msgs, map[string]any{
			"destination":   fmt.Sprintf("+1202555%04d", 1000+(i%8000)),
			"sender_id":     "ZNERO",
			"text":          fmt.Sprintf("%s-%d", tag, i),
			"dlr_requested": true,
		})
	}
	b, _ := json.Marshal(map[string]any{"messages": msgs})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/customer/sms/bulk", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http bulk status=%d body=%s", resp.StatusCode, string(raw))
	}
	return nil
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
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
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

func submitOneSMPP(conn net.Conn, seq uint32, dest, txt string) error {
	sub := pdu.NewSubmitSM(seq)
	sub.DestAddr = strings.TrimPrefix(dest, "+")
	sub.SourceAddr = "ZNERO"
	sub.DataCoding = pdu.DataCodingSMSCDefault
	sub.RegisteredDelivery = 1
	sub.ShortMessage = []byte(txt)
	if err := pdu.WritePDU(conn, sub); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		p, err := pdu.ReadPDU(conn)
		if err != nil {
			return err
		}
		if d, isDLR := p.(*pdu.DeliverSM); isDLR {
			_ = pdu.WritePDU(conn, pdu.NewDeliverSMResp(pdu.StatusROK, d.Header.SequenceNumber))
			continue
		}
		_ = conn.SetReadDeadline(time.Time{})
		sr, ok := p.(*pdu.SubmitSMResp)
		if !ok {
			continue
		}
		if sr.Header.CommandStatus != pdu.StatusROK {
			return fmt.Errorf("submit_sm_resp status=0x%x", sr.Header.CommandStatus)
		}
		return nil
	}
}

func runBurst(st *loadState, stateFile, baseURL, smppAddr, dbURL, pprofDir, pprofPhase, webhookListen string, waitWebhooks bool, drainTimeout time.Duration, httpSingleN, httpBulkBatches, bulkSize, smppN int) {
	var whRxCount atomic.Int64
	var whSrv *http.Server
	if webhookListen != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 256*1024))
			whRxCount.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		})
		whSrv = &http.Server{Addr: webhookListen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := whSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("[WEBHOOK SINK ERR] %v", err)
			}
		}()
		defer func() {
			c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = whSrv.Shutdown(c)
		}()
	}

	hc := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        128,
			MaxIdleConnsPerHost: 128,
			IdleConnTimeout:     60 * time.Second,
		},
	}

	totalExpected := httpSingleN + (httpBulkBatches * bulkSize) + smppN
	log.Printf("[BURST] Starting mixed load: http_single=%d + http_bulk=%dx%d (%d) + smpp=%d => %d TOTAL messages",
		httpSingleN, httpBulkBatches, bulkSize, httpBulkBatches*bulkSize, smppN, totalExpected)

	var (
		mu             sync.Mutex
		latSingle      = make([]int64, 0, httpSingleN)
		latBulk        = make([]int64, 0, httpBulkBatches)
		latSMPP        = make([]int64, 0, smppN)
		errSingle      atomic.Int64
		errBulk        atomic.Int64
		errSMPP        atomic.Int64
		submittedCount atomic.Int64
	)

	// Start pprof capture (either during burst or during drain)
	capturePprof := func() {
		if pprofDir == "" {
			return
		}
		_ = os.MkdirAll(pprofDir, 0755)
		targets := map[string]string{
			"api":        "127.0.0.1:6060",
			"smpp":       "127.0.0.1:6061",
			"dispatcher": "127.0.0.1:6062",
			"webhook":    "127.0.0.1:6064",
		}
		var capWg sync.WaitGroup
		for name, addr := range targets {
			capWg.Add(1)
			go func(n, a string) {
				defer capWg.Done()
				pClient := &http.Client{Timeout: 20 * time.Second}
				if r, err := pClient.Get("http://" + a + "/debug/pprof/profile?seconds=8"); err == nil {
					b, _ := io.ReadAll(r.Body)
					_ = r.Body.Close()
					_ = os.WriteFile(filepath.Join(pprofDir, n+"_cpu.pb.gz"), b, 0644)
				}
				if r, err := pClient.Get("http://" + a + "/debug/pprof/heap"); err == nil {
					b, _ := io.ReadAll(r.Body)
					_ = r.Body.Close()
					_ = os.WriteFile(filepath.Join(pprofDir, n+"_heap.pb.gz"), b, 0644)
				}
			}(name, addr)
		}
		capWg.Wait()
		log.Printf("[PPROF phase=%s] Captured CPU (8s) + Heap profiles into %s", pprofPhase, pprofDir)
	}
	var pprofWg sync.WaitGroup
	if pprofDir != "" && pprofPhase != "drain" {
		pprofWg.Add(1)
		go func() {
			defer pprofWg.Done()
			time.Sleep(4 * time.Second)
			capturePprof()
		}()
	}

	burstStart := time.Now()
	var wg sync.WaitGroup

	// 1. Single HTTP workers (24 workers)
	const singleWorkers = 24
	var singleIdx atomic.Int64
	for w := 0; w < singleWorkers; w++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			localLat := make([]int64, 0, httpSingleN/singleWorkers+16)
			for {
				i := int(singleIdx.Add(1) - 1)
				if i >= httpSingleN {
					break
				}
				cust := st.Customers[i%len(st.Customers)]
				t0 := time.Now()
				if err := sendSingleHTTP(hc, baseURL, cust.APIKey, fmt.Sprintf("LD1-%d", i)); err != nil {
					if errSingle.Add(1) <= 5 {
						log.Printf("[BURST single err] %v", err)
					}
				} else {
					localLat = append(localLat, time.Since(t0).Nanoseconds())
					submittedCount.Add(1)
				}
			}
			mu.Lock()
			latSingle = append(latSingle, localLat...)
			mu.Unlock()
		}(w)
	}

	// 2. Bulk HTTP workers (12 workers)
	const bulkWorkers = 12
	var bulkIdx atomic.Int64
	for w := 0; w < bulkWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			localLat := make([]int64, 0, httpBulkBatches/bulkWorkers+8)
			for {
				i := int(bulkIdx.Add(1) - 1)
				if i >= httpBulkBatches {
					break
				}
				cust := st.Customers[i%len(st.Customers)]
				t0 := time.Now()
				if err := sendBulkHTTP(hc, baseURL, cust.APIKey, bulkSize, fmt.Sprintf("LDB-%d", i)); err != nil {
					if errBulk.Add(1) <= 5 {
						log.Printf("[BURST bulk err] %v", err)
					}
				} else {
					localLat = append(localLat, time.Since(t0).Nanoseconds())
					submittedCount.Add(int64(bulkSize))
				}
			}
			mu.Lock()
			latBulk = append(latBulk, localLat...)
			mu.Unlock()
		}()
	}

	smppDoneCh := make(chan struct{})
	// 3. SMPP ESME workers (12 persistent TCP connections, 2 per customer across 6 customers)
	const smppWorkers = 12
	var smppIdx atomic.Int64
	for w := 0; w < smppWorkers; w++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			cust := st.Customers[wid%len(st.Customers)]
			conn, err := dialBindESME(smppAddr, cust.SMPPSys, cust.SMPPPass)
			if err != nil {
				log.Printf("smpp worker %d bind error: %v", wid, err)
				errSMPP.Add(1)
				return
			}
			defer func() {
				if waitWebhooks && wid < len(st.Customers) {
					// Keep 1 transceiver per customer open reading deliver_sm DLRs until full drain stops
					go func(c net.Conn) {
						for {
							select {
							case <-smppDoneCh:
								_ = pdu.WritePDU(c, pdu.NewUnbind(999999))
								_ = c.Close()
								return
							default:
								_ = c.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
								p, rErr := pdu.ReadPDU(c)
								if rErr == nil {
									if d, isDLR := p.(*pdu.DeliverSM); isDLR {
										_ = pdu.WritePDU(c, pdu.NewDeliverSMResp(pdu.StatusROK, d.Header.SequenceNumber))
									}
								}
							}
						}
					}(conn)
					return
				}
				_ = pdu.WritePDU(conn, pdu.NewUnbind(999999))
				_ = conn.Close()
			}()
			localLat := make([]int64, 0, smppN/smppWorkers+16)
			var seq uint32 = 10
			for {
				i := int(smppIdx.Add(1) - 1)
				if i >= smppN {
					break
				}
				seq++
				t0 := time.Now()
				if err := submitOneSMPP(conn, seq, "+12025550202", fmt.Sprintf("LDS-%d", i)); err != nil {
					errSMPP.Add(1)
				} else {
					localLat = append(localLat, time.Since(t0).Nanoseconds())
					submittedCount.Add(1)
				}
			}
			mu.Lock()
			latSMPP = append(latSMPP, localLat...)
			mu.Unlock()
		}(w)
	}

	progStop := make(chan struct{})
	go func() {
		tk := time.NewTicker(15 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-progStop:
				return
			case <-tk.C:
				log.Printf("[BURST submit progress] submitted=%d/%d elapsed=%.1fs errs=(%d,%d,%d)",
					submittedCount.Load(), totalExpected, time.Since(burstStart).Seconds(),
					errSingle.Load(), errBulk.Load(), errSMPP.Load())
			}
		}
	}()

	wg.Wait()
	close(progStop)
	submitDur := time.Since(burstStart)
	pprofWg.Wait()

	s50, s95, s99 := percentiles(latSingle)
	b50, b95, b99 := percentiles(latBulk)
	m50, m95, m99 := percentiles(latSMPP)
	submitRate := float64(submittedCount.Load()) / submitDur.Seconds()

	log.Printf("[BURST SUBMIT DONE] submitted=%d/%d in %.2fs (%.1f msg/s) errs=(single=%d,bulk=%d,smpp=%d)",
		submittedCount.Load(), totalExpected, submitDur.Seconds(), submitRate, errSingle.Load(), errBulk.Load(), errSMPP.Load())
	log.Printf("  Latencies (ms): http_single p50=%.2f p95=%.2f p99=%.2f | http_bulk(%d) p50=%.2f p95=%.2f p99=%.2f | smpp p50=%.2f p95=%.2f p99=%.2f",
		s50, s95, s99, bulkSize, b50, b95, b99, m50, m95, m99)

	// Wait for outbox + messages pipeline to finish draining all 100k messages
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("pgxpool: %v", err)
	}
	defer pool.Close()

	if pprofDir != "" && pprofPhase == "drain" {
		pprofWg.Add(1)
		go func() {
			defer pprofWg.Done()
			time.Sleep(1 * time.Second)
			capturePprof()
		}()
	}

	orgIDs := make([]uuid.UUID, 0, len(st.Customers))
	for _, c := range st.Customers {
		id, _ := uuid.Parse(c.OrgID)
		orgIDs = append(orgIDs, id)
	}

	if drainTimeout <= 0 {
		drainTimeout = 10 * time.Minute
	}
	drainDL := time.Now().Add(drainTimeout)
	drainStart := time.Now()
	var nonTerminal, whPend, smppDLRPend int
	lastPrint := time.Now()
	for time.Now().Before(drainDL) {
		_ = pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM messages
			WHERE customer_org_id = ANY($1)
			  AND status IN ('accepted','submitting','submitted','unknown')`,
			orgIDs).Scan(&nonTerminal)
		if waitWebhooks {
			_ = pool.QueryRow(ctx, `
				SELECT COUNT(*) FROM customer_webhook_deliveries
				WHERE customer_org_id = ANY($1) AND status IN ('pending','retry')`,
				orgIDs).Scan(&whPend)
			_ = pool.QueryRow(ctx, `
				SELECT COUNT(*) FROM smpp_dlr_queue q
				JOIN customer_smpp_accounts a ON a.id = q.account_id
				WHERE a.organization_id = ANY($1) AND q.status = 'pending'`,
				orgIDs).Scan(&smppDLRPend)
		}
		if nonTerminal == 0 && whPend == 0 && smppDLRPend == 0 {
			break
		}
		if time.Since(lastPrint) >= 10*time.Second {
			log.Printf("[DRAIN progress] non_terminal=%d wh_pend=%d smpp_dlr_pend=%d wh_rx=%d elapsed=%.1fs",
				nonTerminal, whPend, smppDLRPend, whRxCount.Load(), time.Since(burstStart).Seconds())
			lastPrint = time.Now()
		}
		time.Sleep(500 * time.Millisecond)
	}
	close(smppDoneCh)
	pprofWg.Wait()
	drainDur := time.Since(drainStart)
	totalE2EDur := time.Since(burstStart)
	e2eRate := float64(submittedCount.Load()) / totalE2EDur.Seconds()
	log.Printf("[DRAIN DONE] non_terminal=%d wh_pend=%d smpp_dlr_pend=%d wh_rx=%d drain_wait_sec=%.2fs total_e2e_sec=%.2fs e2e_throughput=%.1f msg/s",
		nonTerminal, whPend, smppDLRPend, whRxCount.Load(), drainDur.Seconds(), totalE2EDur.Seconds(), e2eRate)

	st.BurstMetrics = map[string]any{
		"total_target":       totalExpected,
		"submitted_total":    submittedCount.Load(),
		"submit_seconds":     submitDur.Seconds(),
		"submit_msg_per_sec": submitRate,
		"drain_wait_seconds": drainDur.Seconds(),
		"e2e_drain_seconds":  totalE2EDur.Seconds(),
		"e2e_msg_per_sec":    e2eRate,
		"non_terminal_left":  nonTerminal,
		"wh_pending_left":    whPend,
		"smpp_dlr_pend_left": smppDLRPend,
		"webhooks_received":  whRxCount.Load(),
		"err_http_single":    errSingle.Load(),
		"err_http_bulk":      errBulk.Load(),
		"err_smpp":           errSMPP.Load(),
		"http_single_ms":     map[string]float64{"p50": s50, "p95": s95, "p99": s99},
		"http_bulk_ms":       map[string]float64{"p50": b50, "p95": b95, "p99": b99},
		"smpp_submit_ms":     map[string]float64{"p50": m50, "p95": m95, "p99": m99},
	}
	bOut, _ := json.MarshalIndent(st, "", "  ")
	_ = os.WriteFile(stateFile, bOut, 0644)
	if errSingle.Load()+errBulk.Load()+errSMPP.Load() > 0 || nonTerminal > 0 || whPend > 0 || smppDLRPend > 0 {
		log.Fatalf("burst load completed with errors or remaining queue: non_terminal=%d wh_pend=%d smpp_dlr_pend=%d", nonTerminal, whPend, smppDLRPend)
	}
}

var (
	reGoroutineHdr = regexp.MustCompile(`goroutine profile: total (\d+)`)
	reHeapAlloc    = regexp.MustCompile(`#\s+HeapAlloc\s*=\s*(\d+)`)
)

func fetchPprofStats(addr string) (goroutines int, heapMB int) {
	hc := &http.Client{Timeout: 2 * time.Second}
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

func fetchZneroRSS() map[string]int {
	res := make(map[string]int)
	// Query process RSS cleanly via powershell on Windows
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		`Get-Process znero -ErrorAction SilentlyContinue | ForEach-Object { "$($_.Id):$([math]::Round($_.WorkingSet64/1MB))" }`)
	out, err := cmd.Output()
	if err != nil {
		return res
	}
	for i, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			v, _ := strconv.Atoi(parts[1])
			res[fmt.Sprintf("proc_%d_pid_%s", i, parts[0])] = v
		}
	}
	return res
}

func runSoak(st *loadState, stateFile, baseURL, smppAddr, dbURL string, duration time.Duration) {
	log.Printf("[SOAK] Starting continuous mixed soak for %s (~25 msg/s mixed HTTP + SMPP, sampling every 60s)...", duration)
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		log.Fatalf("pgxpool: %v", err)
	}
	defer pool.Close()

	hc := &http.Client{Timeout: 10 * time.Second}
	var sent, errs atomic.Int64

	// Connect one persistent SMPP client per soak loop
	smppCust := st.Customers[0]
	esme, err := dialBindESME(smppAddr, smppCust.SMPPSys, smppCust.SMPPPass)
	if err != nil {
		log.Fatalf("soak smpp bind: %v", err)
	}
	defer func() {
		_ = pdu.WritePDU(esme, pdu.NewUnbind(888888))
		_ = esme.Close()
	}()

	var wg sync.WaitGroup
	// Producer goroutine 1: HTTP single (~15 msg/s)
	wg.Add(1)
	go func() {
		defer wg.Done()
		tk := time.NewTicker(66 * time.Millisecond)
		defer tk.Stop()
		i := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				i++
				cust := st.Customers[i%len(st.Customers)]
				if err := sendSingleHTTP(hc, baseURL, cust.APIKey, fmt.Sprintf("SOAK-H-%d", i)); err != nil {
					errs.Add(1)
				} else {
					sent.Add(1)
				}
			}
		}
	}()

	// Producer goroutine 2: SMPP (~10 msg/s)
	wg.Add(1)
	go func() {
		defer wg.Done()
		tk := time.NewTicker(100 * time.Millisecond)
		defer tk.Stop()
		var seq uint32 = 100
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				seq++
				if err := submitOneSMPP(esme, seq, "+12025550303", fmt.Sprintf("SOAK-S-%d", seq)); err != nil {
					errs.Add(1)
				} else {
					sent.Add(1)
				}
			}
		}
	}()

	// Telemetry sampler every 60s (including t=0 baseline + final sample after ctx.Done)
	var samples []soakSample
	start := time.Now()
	recordSample := func() {
		gMap := make(map[string]int)
		hMap := make(map[string]int)
		for name, addr := range map[string]string{"api": "127.0.0.1:6060", "smpp": "127.0.0.1:6061", "dispatcher": "127.0.0.1:6062"} {
			g, h := fetchPprofStats(addr)
			gMap[name] = g
			hMap[name] = h
		}
		var outboxPend, dbConns int
		_ = pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM message_outbox WHERE status IN ('pending','leased')`).Scan(&outboxPend)
		_ = pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM pg_stat_activity WHERE datname = 'znero_test'`).Scan(&dbConns)
		samp := soakSample{
			ElapsedSec:   int(time.Since(start).Seconds()),
			SentTotal:    sent.Load(),
			ErrorsTotal:  errs.Load(),
			OutboxPend:   outboxPend,
			DBActiveConn: dbConns,
			Goroutines:   gMap,
			HeapAllocMB:  hMap,
			ProcessRSSMB: fetchZneroRSS(),
		}
		samples = append(samples, samp)
		log.Printf("[SOAK t=%ds] sent=%d errs=%d outbox=%d db_conns=%d goroutines=%v heap_mb=%v rss_mb=%v",
			samp.ElapsedSec, samp.SentTotal, samp.ErrorsTotal, samp.OutboxPend, samp.DBActiveConn, samp.Goroutines, samp.HeapAllocMB, samp.ProcessRSSMB)
	}

	recordSample()
	sampTicker := time.NewTicker(60 * time.Second)
	defer sampTicker.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-sampTicker.C:
			recordSample()
		}
	}
	wg.Wait()
	// Allow trailing soak messages to drain cleanly (up to 15s) before final sample
	time.Sleep(5 * time.Second)
	recordSample()

	st.SoakSamples = samples
	bOut, _ := json.MarshalIndent(st, "", "  ")
	_ = os.WriteFile(stateFile, bOut, 0644)
	if errs.Load() > 0 {
		log.Fatalf("soak finished with %d errors", errs.Load())
	}
}

func runVerify(c *apiClient, st *loadState, dbURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("pgxpool: %v", err)
	}
	defer pool.Close()

	for _, cust := range st.Customers {
		oid, _ := uuid.Parse(cust.OrgID)
		rows, qErr := pool.Query(ctx, `
			SELECT lt.id,
			       COALESCE(SUM(CASE WHEN le.direction = 'debit' THEN le.amount ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN le.direction = 'credit' THEN le.amount ELSE 0 END), 0)
			FROM ledger_transactions lt
			JOIN ledger_entries le ON le.transaction_id = lt.id
			WHERE lt.tenant_org_id = $1
			GROUP BY lt.id`, oid)
		if qErr != nil {
			log.Fatalf("ledger query: %v", qErr)
		}
		for rows.Next() {
			var txID uuid.UUID
			var deb, cred decimal.Decimal
			_ = rows.Scan(&txID, &deb, &cred)
			if !deb.Equal(cred) {
				log.Fatalf("UNBALANCED LEDGER org=%s tx=%s deb=%s cred=%s", oid, txID, deb, cred)
			}
		}
		rows.Close()

		var cached, held, avail decimal.Decimal
		if err := pool.QueryRow(ctx, `
			SELECT w.cached_balance,
			       COALESCE((SELECT SUM(h.amount) FROM holds h WHERE h.wallet_id = w.id AND h.status = 'active'), 0),
			       w.cached_balance - COALESCE((SELECT SUM(h.amount) FROM holds h WHERE h.wallet_id = w.id AND h.status = 'active'), 0)
			FROM wallets w WHERE w.organization_id = $1`, oid).Scan(&cached, &held, &avail); err != nil {
			log.Fatalf("wallet check: %v", err)
		}
		if !avail.Add(held).Equal(cached) {
			log.Fatalf("WALLET INVARIANT BROKEN org=%s avail=%s held=%s cached=%s", oid, avail, held, cached)
		}
	}

	recDoc := c.mustJSON(http.MethodPost, "/api/v1/messaging/reconciliation/run", st.SuperToken, map[string]any{}, http.StatusOK)
	disc, _ := recDoc["discrepancy_count"].(float64)
	lOk, _ := recDoc["ledger_matches"].(bool)
	checked, _ := recDoc["messages_checked"].(float64)
	if int(disc) != 0 || !lOk {
		log.Fatalf("RECONCILIATION FAILED AFTER LOAD/SOAK: %+v", recDoc)
	}
	log.Printf("[VERIFY PASS] Reconciliation clean after burst+soak: messages_checked=%.0f discrepancy_count=0 ledger_matches=true", checked)
}

func main() {
	log.SetOutput(os.Stdout)
	mode := flag.String("mode", "setup", "setup | burst | soak | verify")
	apiURL := flag.String("api-url", "http://127.0.0.1:8080", "API URL")
	smppAddr := flag.String("smpp-addr", "127.0.0.1:2776", "Customer SMPP server address")
	provSMPPHost := flag.String("prov-smpp-host", "127.0.0.1", "Mock SMSC host")
	provSMPPPort := flag.Int("prov-smpp-port", 19100, "Mock SMSC port")
	provSMPPSys := flag.String("prov-smpp-sys", "E2E_EXT_SMSC", "Mock SMSC system_id")
	provSMPPPass := flag.String("prov-smpp-pass", "extSmscPass123!", "Mock SMSC password")
	provHTTPURL := flag.String("prov-http-url", "http://127.0.0.1:19102", "Fake HTTP provider URL")
	provHTTPTok := flag.String("prov-http-token", "extHttpTok123!", "Fake HTTP provider token")
	provHTTPCb := flag.String("prov-http-cb-secret", "extHttpCbSec123!", "Fake HTTP provider callback secret")
	dbURL := flag.String("db-url", os.Getenv("TEST_DATABASE_URL"), "PostgreSQL URL")
	stateFile := flag.String("state-file", ".tmp/p7_state.json", "State file")
	pprofDir := flag.String("pprof-dir", ".tmp/pprof", "Directory to save pprof captures during burst")
	pprofPhase := flag.String("pprof-phase", "burst", "Capture pprof during 'burst' or 'drain'")
	webhookURL := flag.String("webhook-url", "", "Optional customer webhook URL prefix during setup")
	webhookListen := flag.String("webhook-listen", "", "Optional loopback listen addr to receive webhooks during burst")
	waitWebhooks := flag.Bool("wait-webhooks", false, "Wait for webhook deliveries & smpp_dlr_queue to drain cleanly")
	drainTimeout := flag.Duration("drain-timeout", 10*time.Minute, "Max wait for full drain")
	httpSingle := flag.Int("http-single", 20000, "Count of single HTTP /customer/sms")
	httpBulkBatches := flag.Int("http-bulk-batches", 600, "Count of bulk /customer/sms/bulk requests")
	bulkSize := flag.Int("bulk-size", 100, "Messages per bulk request")
	smppN := flag.Int("smpp-count", 20000, "Count of SMPP submit_sm messages")
	soakDur := flag.Duration("duration", 10*time.Minute, "Soak test duration")
	flag.Parse()

	client := &apiClient{
		baseURL: strings.TrimRight(*apiURL, "/"),
		http:    &http.Client{Timeout: 20 * time.Second},
	}

	loadSt := func() *loadState {
		raw, err := os.ReadFile(*stateFile)
		if err != nil {
			log.Fatalf("read state: %v", err)
		}
		var st loadState
		if err := json.Unmarshal(raw, &st); err != nil {
			log.Fatalf("parse state: %v", err)
		}
		return &st
	}

	switch *mode {
	case "setup":
		runSetup(client, *provSMPPHost, *provSMPPPort, *provSMPPSys, *provSMPPPass, *provHTTPURL, *provHTTPTok, *provHTTPCb, *webhookURL, *stateFile)
	case "burst":
		runBurst(loadSt(), *stateFile, client.baseURL, *smppAddr, *dbURL, *pprofDir, *pprofPhase, *webhookListen, *waitWebhooks, *drainTimeout, *httpSingle, *httpBulkBatches, *bulkSize, *smppN)
	case "soak":
		runSoak(loadSt(), *stateFile, client.baseURL, *smppAddr, *dbURL, *soakDur)
	case "verify":
		runVerify(client, loadSt(), *dbURL)
	default:
		log.Fatalf("unknown -mode=%q", *mode)
	}
}
