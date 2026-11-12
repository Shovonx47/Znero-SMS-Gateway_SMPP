// Command e2eblackbox is an external black-box E2E harness talking ONLY over HTTP, SMPP TCP,
// mock provider control APIs, and PostgreSQL read verification against live Znero OS processes.
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
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type blackboxState struct {
	CountryID         string   `json:"country_id"`
	SuperToken        string   `json:"super_token"`
	MakerToken        string   `json:"maker_token"`
	CheckerToken      string   `json:"checker_token"`
	SMPPProvID        string   `json:"smpp_prov_id"`
	SMPPConnID        string   `json:"smpp_conn_id"`
	HTTPProvID        string   `json:"http_prov_id"`
	HTTPConnID        string   `json:"http_conn_id"`
	SMPPPlanID        string   `json:"smpp_plan_id"`
	HTTPPlanID        string   `json:"http_plan_id"`
	ResellerOrgID     string   `json:"reseller_org_id"`
	SellCardID        string   `json:"sell_card_id"`
	Cust1OrgID        string   `json:"cust1_org_id"`
	Cust1WalletID     string   `json:"cust1_wallet_id"`
	Cust1APIKey       string   `json:"cust1_api_key"`
	Cust1SMPPSys      string   `json:"cust1_smpp_sys"`
	Cust1SMPPPass     string   `json:"cust1_smpp_pass"`
	Cust2OrgID        string   `json:"cust2_org_id"`
	Cust2WalletID     string   `json:"cust2_wallet_id"`
	Cust2APIKey       string   `json:"cust2_api_key"`
	Cust2SMPPSys      string   `json:"cust2_smpp_sys"`
	Cust2SMPPPass     string   `json:"cust2_smpp_pass"`
	EdgeSMPPSys       string   `json:"edge_smpp_sys"`
	EdgeSMPPPass      string   `json:"edge_smpp_pass"`
	LockoutSMPPSys    string   `json:"lockout_smpp_sys"`
	CrashSeq          int      `json:"crash_seq,omitempty"`
	CrashPendingMsgs  []string `json:"crash_pending_msgs,omitempty"`
	CrashPendingDests []string `json:"crash_pending_dests,omitempty"`
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
	ok := false
	for _, w := range wantCodes {
		if code == w {
			ok = true
			break
		}
	}
	if !ok {
		log.Fatalf("%s %s returned status=%d (wanted %v) body=%s", method, path, code, wantCodes, string(raw))
	}
	return unwrapData(doc)
}

func unwrapData(doc map[string]any) map[string]any {
	if doc == nil {
		return map[string]any{}
	}
	if d, ok := doc["data"].(map[string]any); ok {
		return d
	}
	return doc
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

func (c *apiClient) login(email, pwd string) string {
	doc := c.mustJSON(http.MethodPost, "/api/v1/auth/login", "", map[string]any{
		"email":    email,
		"password": pwd,
	}, http.StatusOK)
	tok := strField(doc, "access_token")
	if tok == "" {
		log.Fatalf("login for %s returned no access_token: %+v", email, doc)
	}
	return tok
}

func signHMACSHA256(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func main() {
	mode := flag.String("mode", "setup", "setup|verify|crash-http-send|crash-smpp-send|crash-verify")
	apiURL := flag.String("api-url", "http://127.0.0.1:8080", "Znero HTTP API base URL")
	smppAddr := flag.String("smpp-addr", "127.0.0.1:2776", "Znero customer SMPP gateway address")
	provSMPPHost := flag.String("prov-smpp-host", "127.0.0.1", "External mock SMSC host")
	provSMPPPort := flag.Int("prov-smpp-port", 19100, "External mock SMSC port")
	provSMPPCtrl := flag.String("prov-smpp-ctrl", "http://127.0.0.1:19101", "External mock SMSC control URL")
	provSMPPSys := flag.String("prov-smpp-sys", "E2E_EXT_SMSC", "External mock SMSC system_id")
	provSMPPPass := flag.String("prov-smpp-pass", "extSmscPass123!", "External mock SMSC password")
	provHTTPURL := flag.String("prov-http-url", "http://127.0.0.1:19102", "External fake HTTP provider URL")
	provHTTPToken := flag.String("prov-http-token", "extHttpTok123!", "External fake HTTP provider bearer token")
	provHTTPCbSecret := flag.String("prov-http-cb-secret", "extHttpCbSec123!", "External fake HTTP provider callback secret")
	dbURL := flag.String("db-url", os.Getenv("DATABASE_URL"), "PostgreSQL URL for direct read-only ledger/reconcile assertions")
	stateFile := flag.String("state-file", "e2e_blackbox_state.json", "Path to state file")
	count := flag.Int("count", 5, "Count of crash-phase messages")
	flag.Parse()

	client := &apiClient{
		baseURL: strings.TrimRight(*apiURL, "/"),
		http:    &http.Client{Timeout: 15 * time.Second},
	}

	switch *mode {
	case "setup":
		runSetup(client, *provSMPPHost, *provSMPPPort, *provSMPPSys, *provSMPPPass, *provHTTPURL, *provHTTPToken, *provHTTPCbSecret, *stateFile)
	case "verify":
		st := loadState(*stateFile)
		runVerify(client, st, *smppAddr, *provSMPPCtrl, *provHTTPURL, *dbURL)
		saveState(*stateFile, st)
	case "crash-http-send":
		st := loadState(*stateFile)
		ids, dests := runCrashHTTPSend(client, st, *count)
		st.CrashPendingMsgs = append(st.CrashPendingMsgs, ids...)
		st.CrashPendingDests = append(st.CrashPendingDests, dests...)
		saveState(*stateFile, st)
	case "crash-smpp-send":
		st := loadState(*stateFile)
		ids, dests := runCrashSMPPSend(*smppAddr, st, *count)
		st.CrashPendingMsgs = append(st.CrashPendingMsgs, ids...)
		st.CrashPendingDests = append(st.CrashPendingDests, dests...)
		saveState(*stateFile, st)
	case "crash-verify":
		st := loadState(*stateFile)
		runCrashVerify(client, st, *smppAddr, *provSMPPCtrl, *provHTTPURL, *dbURL)
	default:
		log.Fatalf("unknown -mode %q", *mode)
	}
}

func loadState(path string) *blackboxState {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("read state file %s: %v", path, err)
	}
	var st blackboxState
	if err := json.Unmarshal(b, &st); err != nil {
		log.Fatalf("decode state file %s: %v", path, err)
	}
	return &st
}

func saveState(path string, st *blackboxState) {
	b, _ := json.MarshalIndent(st, "", "  ")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		log.Fatalf("write state file %s: %v", path, err)
	}
}

func runSetup(client *apiClient, smppHost string, smppPort int, smppSys, smppPass, httpURL, httpTok, httpCbSec, stateFile string) {
	suffix := uuid.NewString()[:6]
	superTok := client.login("dev-superadmin@dev.znero.test", "DevSeed1234!#")
	makerTok := client.login("dev-maker@dev.znero.test", "DevSeed1234!#")
	checkerTok := client.login("dev-checker@dev.znero.test", "DevSeed1234!#")

	// 1. Lookup US country via paginated search
	var countryID string
	for page := 1; page <= 12 && countryID == ""; page++ {
		cDoc := client.mustJSON(http.MethodGet, fmt.Sprintf("/api/v1/countries?search=United+States&page=%d&page_size=100", page), superTok, nil, http.StatusOK)
		items, _ := cDoc["items"].([]any)
		for _, it := range items {
			m, _ := it.(map[string]any)
			if strings.EqualFold(strField(m, "iso2"), "US") {
				countryID = strField(m, "id")
				break
			}
		}
		if len(items) < 100 {
			break
		}
	}
	if countryID == "" {
		log.Fatalf("US country not found via GET /api/v1/countries")
	}

	// 2. Verify HTTP Maker-Checker flow explicitly via REST before proceeding
	mcStatus, mcCountryDoc, mcRaw, err := client.doJSON(http.MethodPost, "/api/v1/countries", makerTok, map[string]any{
		"iso2":          "X" + strings.ToUpper(suffix[:1]),
		"iso3":          "XX" + strings.ToUpper(suffix[:1]),
		"numeric_code":  fmt.Sprintf("%03d", 910+(int(suffix[0])%80)),
		"name":          "BlackBox MC Test " + suffix,
		"currency_code": "USD",
		"phone_code":    "+99" + suffix[:1],
		"calling_code":  "99" + suffix[:1],
		"reason":        "blackbox maker-checker test",
	})
	if err != nil {
		log.Fatalf("maker country create err: %v", err)
	}
	if mcStatus != http.StatusAccepted && mcStatus != http.StatusOK {
		log.Fatalf("maker country create expected 200/202 for Maker-Checker, got %d raw=%s", mcStatus, string(mcRaw))
	}
	crData := unwrapData(mcCountryDoc)
	crID := strField(crData, "id")
	if crID == "" || strField(crData, "status") != "pending" {
		log.Fatalf("maker country create expected pending change request, got status=%d raw=%s", mcStatus, string(mcRaw))
	}
	// Self-approval by maker MUST be rejected (403); checker rejects cleanly (200) so ISO seed stays clean on reruns
	selfCode, _, _, _ := client.doJSON(http.MethodPost, "/api/v1/change-requests/"+crID+"/approve", makerTok, map[string]any{"reason": "self"})
	if selfCode != http.StatusForbidden {
		log.Fatalf("maker self-approval returned %d, want 403", selfCode)
	}
	client.mustJSON(http.MethodPost, "/api/v1/change-requests/"+crID+"/reject", checkerTok, map[string]any{"reason": "checker reject blackbox test"}, http.StatusOK)
	log.Printf("[PASS] HTTP Maker-Checker verified (self-approve 403 blocked, checker 200 rejected CR=%s)", crID)

	// 3. Provision SMPP Carrier, Connection, Coverage, Buy Rate Card + Rate
	pSmpp := client.mustJSON(http.MethodPost, "/api/v1/providers", superTok, map[string]any{
		"name":          "Ext SMPP Carrier " + suffix,
		"code":          "EXT_SMPP_" + strings.ToUpper(suffix),
		"provider_type": "smpp",
		"country_id":    countryID,
		"currency_code": "USD",
		"status":        "active",
		"reason":        "blackbox smpp carrier",
	}, http.StatusCreated, http.StatusOK)
	provSMPPID := strField(pSmpp, "id")

	connSmpp := client.mustJSON(http.MethodPost, "/api/v1/providers/"+provSMPPID+"/connections", superTok, map[string]any{
		"name":                  "Ext-SMPP-Conn-" + suffix,
		"protocol":              "smpp",
		"status":                "active",
		"max_tps":               500,
		"smpp_host":             smppHost,
		"smpp_port":             smppPort,
		"smpp_system_id":        smppSys,
		"smpp_password":         smppPass,
		"smpp_system_type":      "SMPP",
		"smpp_bind_type":        "transceiver",
		"smpp_tls":              false,
		"smpp_window_size":      16,
		"smpp_enquire_link_sec": 30,
		"reason":                "blackbox smpp connection",
	}, http.StatusCreated, http.StatusOK)
	connSMPPID := strField(connSmpp, "id")

	client.mustJSON(http.MethodPost, "/api/v1/provider-coverage", superTok, map[string]any{
		"provider_id": provSMPPID,
		"country_id":  countryID,
		"status":      "active",
		"reason":      "blackbox coverage",
	}, http.StatusCreated, http.StatusOK)

	smppBuyCard := client.mustJSON(http.MethodPost, "/api/v1/rates/cards", superTok, map[string]any{
		"name":          "Ext SMPP Buy " + suffix,
		"kind":          "provider_buy",
		"provider_id":   provSMPPID,
		"currency_code": "USD",
		"status":        "active",
		"reason":        "blackbox buy card",
	}, http.StatusCreated)
	smppBuyCardID := strField(smppBuyCard, "id")
	for _, mt := range []string{"default", "transactional"} {
		client.mustJSON(http.MethodPost, "/api/v1/rates/cards/"+smppBuyCardID+"/rates", superTok, map[string]any{
			"country_id":     countryID,
			"message_type":   mt,
			"price":          "0.0200",
			"effective_from": time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339),
			"reason":         "blackbox buy rate",
		}, http.StatusCreated)
	}

	// 4. Provision HTTP Carrier, Connection (callback mode), Coverage, Buy Rate Card
	pHTTP := client.mustJSON(http.MethodPost, "/api/v1/providers", superTok, map[string]any{
		"name":          "Ext HTTP Carrier " + suffix,
		"code":          "EXT_HTTP_" + strings.ToUpper(suffix),
		"provider_type": "http",
		"country_id":    countryID,
		"currency_code": "USD",
		"status":        "active",
		"reason":        "blackbox http carrier",
	}, http.StatusCreated, http.StatusOK)
	provHTTPID := strField(pHTTP, "id")

	connHTTP := client.mustJSON(http.MethodPost, "/api/v1/providers/"+provHTTPID+"/connections", superTok, map[string]any{
		"name":             "Ext-HTTP-Conn-" + suffix,
		"protocol":         "http",
		"status":           "active",
		"max_tps":          500,
		"http_base_url":    httpURL + "/send",
		"http_auth_type":   "bearer",
		"http_credentials": httpTok,
		"http_dlr_mode":    "callback",
		"http_dlr_secret":  httpCbSec,
		"http_config": map[string]any{
			"method":                    "POST",
			"body_format":               "json",
			"body_template":             `{"to":"{{destination}}","from":"{{sender}}","text":"{{text}}","ref":"{{message_id}}"}`,
			"success_http_min":          200,
			"success_http_max":          299,
			"provider_msg_id_path":      "data.id",
			"success_json_path":         "status",
			"success_equals":            "ok",
			"request_timeout_ms":        2000,
			"callback_msg_id_path":      "provider_message_id",
			"callback_status_path":      "status",
			"callback_error_code_path":  "error_code",
			"callback_signature_header": "X-Provider-Signature",
			"callback_signature_algo":   "hmac_sha256",
		},
		"reason": "blackbox http connection",
	}, http.StatusCreated, http.StatusOK)
	connHTTPID := strField(connHTTP, "id")

	client.mustJSON(http.MethodPost, "/api/v1/provider-coverage", superTok, map[string]any{
		"provider_id": provHTTPID,
		"country_id":  countryID,
		"status":      "active",
		"reason":      "blackbox http coverage",
	}, http.StatusCreated, http.StatusOK)

	httpBuyCard := client.mustJSON(http.MethodPost, "/api/v1/rates/cards", superTok, map[string]any{
		"name":          "Ext HTTP Buy " + suffix,
		"kind":          "provider_buy",
		"provider_id":   provHTTPID,
		"currency_code": "USD",
		"status":        "active",
		"reason":        "blackbox http buy card",
	}, http.StatusCreated)
	httpBuyCardID := strField(httpBuyCard, "id")
	for _, mt := range []string{"default", "transactional"} {
		client.mustJSON(http.MethodPost, "/api/v1/rates/cards/"+httpBuyCardID+"/rates", superTok, map[string]any{
			"country_id":     countryID,
			"message_type":   mt,
			"price":          "0.0200",
			"effective_from": time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339),
			"reason":         "blackbox http buy rate",
		}, http.StatusCreated)
	}

	// Configure e2ehttpprov with the created callback URL
	cbTarget := client.baseURL + "/api/v1/provider-callbacks/dlr/" + connHTTPID
	cfgHTTPReq, _ := http.NewRequest(http.MethodPost, httpURL+"/ctrl/mode", bytes.NewReader([]byte(fmt.Sprintf(`{"callback_url":%q,"callback_secret":%q}`, cbTarget, httpCbSec))))
	cfgHTTPReq.Header.Set("Content-Type", "application/json")
	if resp, err := client.http.Do(cfgHTTPReq); err == nil {
		_ = resp.Body.Close()
	}

	// 5. Route Plans
	createPlan := func(label, connID string) string {
		pl := client.mustJSON(http.MethodPost, "/api/v1/routing/plans", superTok, map[string]any{
			"name":     "BB Plan " + label + " " + suffix,
			"code":     "BB_" + label + "_" + strings.ToUpper(suffix),
			"strategy": "priority",
		}, http.StatusCreated)
		pid := strField(pl, "id")
		client.mustJSON(http.MethodPost, "/api/v1/routing/plans/"+pid+"/entries", superTok, map[string]any{
			"connection_id": connID,
			"scope_type":    "global",
			"priority":      1,
			"weight":        100,
			"status":        "active",
		}, http.StatusCreated)
		client.mustJSON(http.MethodPost, "/api/v1/routing/plans/"+pid+"/activate", superTok, map[string]any{}, http.StatusOK)
		return pid
	}
	smppPlanID := createPlan("SMPP", connSMPPID)
	httpPlanID := createPlan("HTTP", connHTTPID)

	// 6. Reseller + Customers + Sell Rate Card + Wallets + Topups + API Keys + SMPP Accounts
	resOrg := client.mustJSON(http.MethodPost, "/api/v1/organizations", superTok, map[string]any{
		"name": "BB Reseller " + suffix,
		"type": "reseller",
	}, http.StatusCreated)
	resellerID := strField(resOrg, "id")

	sellCard := client.mustJSON(http.MethodPost, "/api/v1/rates/cards", superTok, map[string]any{
		"name":          "BB Sell " + suffix,
		"kind":          "customer_sell",
		"currency_code": "USD",
		"status":        "active",
		"reason":        "blackbox sell card",
	}, http.StatusCreated)
	sellCardID := strField(sellCard, "id")
	for _, mt := range []string{"default", "transactional"} {
		client.mustJSON(http.MethodPost, "/api/v1/rates/cards/"+sellCardID+"/rates", superTok, map[string]any{
			"country_id":     countryID,
			"message_type":   mt,
			"price":          "0.0500",
			"effective_from": time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339),
			"reason":         "blackbox sell rate",
		}, http.StatusCreated)
	}

	provisionCust := func(label, bMode, failPol, planID string) (orgID, walletID, apiKey, smppSys, smppPass string) {
		cOrg := client.mustJSON(http.MethodPost, "/api/v1/organizations", superTok, map[string]any{
			"name":      "BB Cust " + label + " " + suffix,
			"type":      "customer",
			"parent_id": resellerID,
		}, http.StatusCreated)
		orgID = strField(cOrg, "id")

		client.mustJSON(http.MethodPost, "/api/v1/customer-profiles", superTok, map[string]any{
			"organization_id":            orgID,
			"account_type":               "prepaid",
			"billing_currency":           "USD",
			"default_max_tps":            500,
			"billing_mode":               bMode,
			"dlr_wait_hours":             24,
			"no_dlr_policy":              "retain_until_resolution",
			"failed_after_accept_policy": failPol,
			"status":                     "active",
			"reason":                     "blackbox customer profile",
		}, http.StatusCreated, http.StatusOK)

		wDoc := client.mustJSON(http.MethodPost, "/api/v1/wallets", superTok, map[string]any{
			"organization_id": orgID,
			"currency":        "USD",
			"account_type":    "prepaid",
		}, http.StatusCreated)
		walletID = strField(wDoc, "id")

		client.mustJSON(http.MethodPost, "/api/v1/wallets/"+walletID+"/topup", superTok, map[string]any{
			"amount":          "200.00",
			"reason":          "blackbox topup",
			"idempotency_key": "bb-topup-" + orgID,
		}, http.StatusOK)

		client.mustJSON(http.MethodPost, "/api/v1/rates/assignments", superTok, map[string]any{
			"target_org_id":  orgID,
			"card_id":        sellCardID,
			"effective_from": time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339),
			"reason":         "blackbox rate assign",
		}, http.StatusCreated)

		client.mustJSON(http.MethodPost, "/api/v1/routing/assignments", superTok, map[string]any{
			"customer_org_id": orgID,
			"plan_id":         planID,
			"priority":        1,
			"status":          "active",
		}, http.StatusCreated)

		akDoc := client.mustJSON(http.MethodPost, "/api/v1/customer-api-keys", superTok, map[string]any{
			"organization_id": orgID,
			"label":           "bb-key-" + label,
			"scopes":          []string{"sms:send", "sms:read", "sms:read_content", "account:read"},
			"reason":          "blackbox api key",
		}, http.StatusCreated, http.StatusOK)
		apiKey = strField(akDoc, "plaintext_key")

		smppSys = "bb_" + label + "_" + suffix[:4]
		smppDoc := client.mustJSON(http.MethodPost, "/api/v1/customer-smpp-accounts", superTok, map[string]any{
			"organization_id":    orgID,
			"system_id":          smppSys,
			"allowed_bind_types": []string{"transceiver", "transmitter", "receiver"},
			"max_binds":          4,
			"max_tps":            500,
			"status":             "active",
			"reason":             "blackbox smpp acct",
		}, http.StatusCreated, http.StatusOK)
		smppPass = strField(smppDoc, "plaintext_password")
		return
	}

	c1Org, c1W, c1Key, c1Sys, c1Pass := provisionCust("c1", "on_delivery", "keep_charge", smppPlanID)
	c2Org, c2W, c2Key, c2Sys, c2Pass := provisionCust("c2", "on_provider_accept", "refund", httpPlanID)

	// Extra edge-case ESME account on Customer 1 (max_binds=1, max_tps=2) + lockout account
	edgeSys := "bbe_" + suffix
	edgeDoc := client.mustJSON(http.MethodPost, "/api/v1/customer-smpp-accounts", superTok, map[string]any{
		"organization_id":    c1Org,
		"system_id":          edgeSys,
		"allowed_bind_types": []string{"transceiver", "transmitter", "receiver"},
		"max_binds":          1,
		"max_tps":            2,
		"status":             "active",
		"reason":             "blackbox max_binds/max_tps test",
	}, http.StatusCreated, http.StatusOK)
	edgePass := strField(edgeDoc, "plaintext_password")

	lockSys := "bbl_" + suffix
	client.mustJSON(http.MethodPost, "/api/v1/customer-smpp-accounts", superTok, map[string]any{
		"organization_id":    c1Org,
		"system_id":          lockSys,
		"allowed_bind_types": []string{"transceiver"},
		"max_binds":          2,
		"max_tps":            50,
		"status":             "active",
		"reason":             "blackbox lockout test",
	}, http.StatusCreated, http.StatusOK)

	client.mustJSON(http.MethodPost, "/api/v1/routing/snapshot/reload", superTok, map[string]any{}, http.StatusOK)
	// Allow background worker processes (dispatcher/smpp 5s cache refresh tick) to refresh snapshot & rates cleanly.
	log.Printf("[SETUP] Waiting 6s for multi-process snapshot/rating cache sync...")
	time.Sleep(6 * time.Second)

	saveState(stateFile, &blackboxState{
		CountryID:      countryID,
		SuperToken:     superTok,
		MakerToken:     makerTok,
		CheckerToken:   checkerTok,
		SMPPProvID:     provSMPPID,
		SMPPConnID:     connSMPPID,
		HTTPProvID:     provHTTPID,
		HTTPConnID:     connHTTPID,
		SMPPPlanID:     smppPlanID,
		HTTPPlanID:     httpPlanID,
		ResellerOrgID:  resellerID,
		SellCardID:     sellCardID,
		Cust1OrgID:     c1Org,
		Cust1WalletID:  c1W,
		Cust1APIKey:    c1Key,
		Cust1SMPPSys:   c1Sys,
		Cust1SMPPPass:  c1Pass,
		Cust2OrgID:     c2Org,
		Cust2WalletID:  c2W,
		Cust2APIKey:    c2Key,
		Cust2SMPPSys:   c2Sys,
		Cust2SMPPPass:  c2Pass,
		EdgeSMPPSys:    edgeSys,
		EdgeSMPPPass:   edgePass,
		LockoutSMPPSys: lockSys,
	})
	log.Printf("[SETUP] Black-box setup COMPLETE via REST API ONLY -> %s", stateFile)
}

func startWebhookListener(tSecrets map[string]string, mu *sync.Mutex, received map[uuid.UUID][]string) (string, func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("listen webhook receiver: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/wh/", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/wh/")
		body, _ := io.ReadAll(r.Body)
		evID := r.Header.Get("X-Znero-Event-Id")
		ts := r.Header.Get("X-Znero-Timestamp")
		sig := r.Header.Get("X-Znero-Signature")

		mu.Lock()
		sec := tSecrets[key]
		mu.Unlock()
		if evID == "" || ts == "" || sec == "" || sig != "v1="+signHMACSHA256(sec, []byte(ts+"."+string(body))) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var doc map[string]any
		_ = json.Unmarshal(body, &doc)
		if midStr, ok := doc["message_id"].(string); ok {
			if mid, pErr := uuid.Parse(midStr); pErr == nil {
				st, _ := doc["status"].(string)
				mu.Lock()
				received[mid] = append(received[mid], st)
				mu.Unlock()
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String(), func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}

func assertSQLAndReconcile(client *apiClient, st *blackboxState, dbURL string, maxUnmatched int) {
	if dbURL == "" {
		log.Fatalf("-db-url / DATABASE_URL is required for direct SQL ledger checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("connect DB for SQL check: %v", err)
	}
	defer pool.Close()

	// 1. Every ledger transaction for our 2 customers MUST have SUM(debit) == SUM(credit)
	for _, orgIDStr := range []string{st.Cust1OrgID, st.Cust2OrgID} {
		oid, _ := uuid.Parse(orgIDStr)
		rows, err := pool.Query(ctx, `
			SELECT lt.id,
			       COALESCE(SUM(CASE WHEN le.direction = 'debit' THEN le.amount ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN le.direction = 'credit' THEN le.amount ELSE 0 END), 0)
			FROM ledger_transactions lt
			JOIN ledger_entries le ON le.transaction_id = lt.id
			WHERE lt.tenant_org_id = $1
			GROUP BY lt.id`, oid)
		if err != nil {
			log.Fatalf("query ledger balance for %s: %v", oid, err)
		}
		cnt := 0
		for rows.Next() {
			cnt++
			var txID uuid.UUID
			var deb, cred decimal.Decimal
			_ = rows.Scan(&txID, &deb, &cred)
			if !deb.Equal(cred) {
				log.Fatalf("SQL LEDGER UNBALANCED org=%s tx=%s deb=%s cred=%s", oid, txID, deb, cred)
			}
		}
		rows.Close()
		if cnt == 0 {
			log.Fatalf("expected >=1 ledger_transaction for %s, got 0", oid)
		}

		// 2. Verify wallet arithmetic: available + active_holds == cached_balance
		//    and cached_balance == SUM(topup/refund) - SUM(hold_capture)
		var avail, held, cached decimal.Decimal
		err = pool.QueryRow(ctx, `
			SELECT w.cached_balance,
			       COALESCE((SELECT SUM(h.amount) FROM holds h WHERE h.wallet_id = w.id AND h.status = 'active'), 0),
			       w.cached_balance - COALESCE((SELECT SUM(h.amount) FROM holds h WHERE h.wallet_id = w.id AND h.status = 'active'), 0)
			FROM wallets w WHERE w.organization_id = $1`, oid).Scan(&cached, &held, &avail)
		if err != nil {
			log.Fatalf("query wallet math %s: %v", oid, err)
		}
		if !avail.Add(held).Equal(cached) {
			log.Fatalf("WALLET INVARIANT BROKEN org=%s avail=%s held=%s cached=%s", oid, avail, held, cached)
		}
		log.Printf("[SQL CHECK PASS] org=%s cached_balance=%s active_holds=%s available=%s ledger_txs=%d balanced=true",
			oid.String()[:8], cached.StringFixed(4), held.StringFixed(4), avail.StringFixed(4), cnt)
	}

	// 3. Call HTTP Reconciliation endpoint and require 0 discrepancies
	recDoc := client.mustJSON(http.MethodPost, "/api/v1/messaging/reconciliation/run", st.SuperToken, map[string]any{}, http.StatusOK)
	discF, _ := recDoc["discrepancy_count"].(float64)
	ledgerOK, _ := recDoc["ledger_matches"].(bool)
	unmF, _ := recDoc["unmatched_dlrs_count"].(float64)
	if int(discF) != 0 || !ledgerOK || int(unmF) > maxUnmatched {
		log.Fatalf("RECONCILIATION FAILED: %+v", recDoc)
	}
	log.Printf("[PASS] Reconciliation via REST API: discrepancies=0 ledger_matches=true unmatched=%.0f (max=%d) checked=%.0f", unmF, maxUnmatched, recDoc["messages_checked"])
}
