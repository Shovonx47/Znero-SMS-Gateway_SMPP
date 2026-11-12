package customeredge_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/znero-international/znero/backend/internal/customeredge"
	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/masterdata/prefix"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/number"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/internal/rating"
	"github.com/znero-international/znero/backend/internal/routing"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/internal/testkit"
)

type edgeRater struct {
	sellPrice decimal.Decimal
	buyPrice  decimal.Decimal
	currency  string
}

func (r *edgeRater) LookupBuyRate(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ *uuid.UUID, _ time.Time) (*routing.RateLookupResult, error) {
	return &routing.RateLookupResult{Found: true, Price: r.buyPrice, Currency: r.currency}, nil
}
func (r *edgeRater) LookupSellRate(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ *uuid.UUID, _ time.Time) (*routing.RateLookupResult, error) {
	return &routing.RateLookupResult{Found: true, Price: r.sellPrice, Currency: r.currency}, nil
}

type edgeNumberer struct{ countryID uuid.UUID }

func (n *edgeNumberer) Lookup(_ string) (*routing.NumberInfo, error) {
	return &routing.NumberInfo{CountryID: n.countryID}, nil
}

func hashAPIKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func seedEdgeCustomer(
	t *testing.T,
	e *testkit.Env,
	ledgerSvc *ledger.Service,
	countryID uuid.UUID,
	tps int,
) (custOrgID uuid.UUID, rawFullKey string, rawSendOnlyKey string) {
	t.Helper()
	ctx := context.Background()

	custOrgID, _ = e.ChildOrg(e.PlatformOrgID, e.PlatformOrgPath, "edge-cust-"+testkit.RandHex(4), "customer")

	if _, err := e.Pool.Exec(ctx, `
		INSERT INTO customer_profiles (
			organization_id, account_type, billing_currency, default_max_tps,
			billing_mode, dlr_wait_hours, no_dlr_policy, failed_after_accept_policy, version
		) VALUES ($1, 'prepaid', 'USD', $2, 'on_provider_accept', 18, 'immediate_release', 'refund', 1)`,
		custOrgID, tps,
	); err != nil {
		t.Fatalf("insert customer_profile: %v", err)
	}

	w, err := ledgerSvc.EnsureWallet(ctx, e.Override, custOrgID, "USD", "prepaid")
	if err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	if _, _, err := ledgerSvc.Topup(ctx, e.Override, w.ID, ledger.WalletTopupRequest{
		Amount:         decimal.NewFromInt(200),
		Reason:         "edge seed topup",
		IdempotencyKey: "edge-topup-" + custOrgID.String(),
	}, "127.0.0.1", "test"); err != nil {
		t.Fatalf("topup wallet: %v", err)
	}

	// API Key with full scopes
	rawFullKey = "znk_live_" + testkit.RandHex(24)
	if _, err := e.Pool.Exec(ctx, `
		INSERT INTO customer_api_keys (organization_id, label, key_prefix, key_hash, scopes, status)
		VALUES ($1, 'full-key', $2, $3, ARRAY['sms:send','sms:read','account:read','inbound:read'], 'active')`,
		custOrgID, rawFullKey[:17], hashAPIKey(rawFullKey),
	); err != nil {
		t.Fatalf("insert full api key: %v", err)
	}

	// API Key with sms:send ONLY (distinct prefix!)
	rawSendOnlyKey = "znk_live_" + testkit.RandHex(24)
	if _, err := e.Pool.Exec(ctx, `
		INSERT INTO customer_api_keys (organization_id, label, key_prefix, key_hash, scopes, status)
		VALUES ($1, 'send-only-key', $2, $3, ARRAY['sms:send'], 'active')`,
		custOrgID, rawSendOnlyKey[:17], hashAPIKey(rawSendOnlyKey),
	); err != nil {
		t.Fatalf("insert send-only api key: %v", err)
	}

	// Seed a real Phase 5 rate card & assignment for /rates and /sms/estimate
	var cardID, rateID uuid.UUID
	if err := e.Pool.QueryRow(ctx, `
		INSERT INTO rate_cards (name, kind, owner_org_id, currency_code, status, version)
		VALUES ($1, 'customer_sell', $2, 'USD', 'active', 1) RETURNING id`,
		"Edge Sell Card "+testkit.RandHex(4), e.PlatformOrgID,
	).Scan(&cardID); err != nil {
		t.Fatalf("insert rate card: %v", err)
	}
	if err := e.Pool.QueryRow(ctx, `
		INSERT INTO rates (card_id, country_id, message_type, price, effective_from, status, version)
		VALUES ($1, $2, 'transactional', 0.045000, now() - interval '1 hour', 'active', 1) RETURNING id`,
		cardID, countryID,
	).Scan(&rateID); err != nil {
		t.Fatalf("insert rate: %v", err)
	}
	if _, err := e.Pool.Exec(ctx, `
		INSERT INTO customer_rate_assignments (customer_org_id, card_id, effective_from, status, version)
		VALUES ($1, $2, now() - interval '1 hour', 'active', 1)`,
		custOrgID, cardID,
	); err != nil {
		t.Fatalf("insert customer_rate_assignment: %v", err)
	}

	return custOrgID, rawFullKey, rawSendOnlyKey
}

func TestCustomerEdge_Auth_Lockout_Scopes_BillingTerms_And_Send(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()

	lc, logger := testkit.NewLogCapture()
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

	ledgerSvc := ledger.NewService(ledger.NewRepository(e.Pool), org.NewRepository(e.Pool), e.Approval, e.Audit, e.Pool)
	numSvc := number.NewService(number.NewRepository(e.Pool), prefix.NewRepository(e.Pool), e.Prefix, e.Approval, e.Audit, e.Pool, logger)
	ratingSvc := rating.NewService(rating.NewRepository(e.Pool), e.Audit, e.Approval, numSvc, nil)

	// Setup Provider + Connection + Coverage + Routing Plan for custOrgID
	var provID, connID uuid.UUID
	_ = e.Pool.QueryRow(ctx, `
		INSERT INTO providers (code, name, provider_type, status, version)
		VALUES ($1, 'Edge Test Prov', 'smpp', 'active', 1) RETURNING id`,
		"EDG_"+testkit.RandHex(4),
	).Scan(&provID)
	_ = e.Pool.QueryRow(ctx, `
		INSERT INTO provider_connections (provider_id, name, protocol, status, version, max_tps)
		VALUES ($1, $2, 'smpp', 'active', 1, 100) RETURNING id`,
		provID, "Edge-Conn-"+testkit.RandHex(4),
	).Scan(&connID)
	_, _ = e.Pool.Exec(ctx, `
		INSERT INTO provider_coverage (provider_id, country_id, status, version)
		VALUES ($1, $2, 'active', 1)
		ON CONFLICT (provider_id, country_id, COALESCE(operator_id, '00000000-0000-0000-0000-000000000000'::uuid)) DO UPDATE SET status = 'active'`,
		provID, countryID,
	)

	rater := &edgeRater{sellPrice: decimal.RequireFromString("0.045000"), buyPrice: decimal.RequireFromString("0.015000"), currency: "USD"}
	routingEngine := routing.NewEngine(e.Pool, rater, &edgeNumberer{countryID: countryID}, nil)
	routingSvc := routing.NewService(routing.NewRepository(e.Pool), e.Approval, e.Audit, e.Pool, routingEngine)
	plan, err := routingSvc.CreatePlan(ctx, e.Override, &routing.RoutePlan{
		Name:     "Edge Plan " + testkit.RandHex(4),
		Code:     "EP_" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	_, _, _ = routingSvc.CreateEntry(ctx, e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: connID, ScopeType: "global", Priority: 1, Weight: 100, Status: "active",
	})
	_, _, _ = routingSvc.RequestPlanActivation(ctx, e.Override, plan.ID)

	custOrgID, fullKey, sendOnlyKey := seedEdgeCustomer(t, e, ledgerSvc, countryID, 3)
	_, _, _ = routingSvc.CreateAssignment(ctx, e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: custOrgID, PlanID: plan.ID, Priority: 1, Status: "active",
	})
	routing.TryReload(ctx, e.Pool)

	msgRepo := messaging.NewRepository(e.Pool)
	msgSvc, err := messaging.NewService(e.Pool, msgRepo, messaging.DefaultConfig(), routingEngine, ledgerSvc, kr, e.Audit, messaging.NewFakeSender(), logger)
	if err != nil {
		t.Fatalf("messaging service: %v", err)
	}

	edgeAuth := customeredge.NewEdgeAuthenticator(e.Pool, nil, logger)
	edgeLimiter := customeredge.NewCustomerLimiter()
	handler := customeredge.NewEdgeHandler(edgeAuth, edgeLimiter, msgSvc, msgRepo, ledgerSvc, ratingSvc, kr, e.Pool)

	app := fiber.New()
	v1 := app.Group("/api/v1")
	handler.RegisterRoutes(v1)

	// 1. Unauthenticated -> 401
	unauthResp, err := app.Test(httptest.NewRequest("GET", "/api/v1/customer/balance", nil), -1)
	if err != nil || unauthResp.StatusCode != 401 {
		t.Fatalf("expected 401 unauthenticated, got %d err=%v", unauthResp.StatusCode, err)
	}

	// 2. Brute-force composite lockout: 5 failures on attacker prefix from IP 10.9.9.1 locks attacker prefix,
	//    while legitimate customer key from SAME IP 10.9.9.1 is NOT locked out!
	badKey := "znk_live_attacker0_" + testkit.RandHex(12)
	for i := 0; i < 6; i++ {
		if _, err := edgeAuth.AuthenticateAPIKey(ctx, badKey, "10.9.9.1"); err == nil {
			t.Fatalf("expected bad key auth failure")
		}
	}
	if _, err := edgeAuth.AuthenticateAPIKey(ctx, fullKey, "10.9.9.1"); err != nil {
		t.Fatalf("SECURITY/DOS BUG: legitimate key locked out by attacker IP collision: %v", err)
	}

	// 3. Scope check: sms:send key can POST /sms, but MUST get 403 on /balance, /account, /rates
	for _, path := range []string{"/api/v1/customer/balance", "/api/v1/customer/account", "/api/v1/customer/rates"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+sendOnlyKey)
		resp, _ := app.Test(req, -1)
		if resp.StatusCode != 403 {
			t.Fatalf("send-only key on %s expected 403 Forbidden, got %d", path, resp.StatusCode)
		}
	}

	// 4. Full-scope key checks: GET /balance, GET /account (with read-only billing_terms!), GET /rates, POST /sms/estimate
	reqBal := httptest.NewRequest("GET", "/api/v1/customer/balance", nil)
	reqBal.Header.Set("Authorization", "Bearer "+fullKey)
	respBal, _ := app.Test(reqBal, -1)
	balBytes, _ := io.ReadAll(respBal.Body)
	if respBal.StatusCode != 200 || !strings.Contains(string(balBytes), `"200`) {
		t.Fatalf("GET /balance failed: status=%d body=%s", respBal.StatusCode, string(balBytes))
	}

	reqAcct := httptest.NewRequest("GET", "/api/v1/customer/account", nil)
	reqAcct.Header.Set("Authorization", "Bearer "+fullKey)
	respAcct, _ := app.Test(reqAcct, -1)
	acctBytes, _ := io.ReadAll(respAcct.Body)
	if respAcct.StatusCode != 200 {
		t.Fatalf("GET /account failed: status=%d body=%s", respAcct.StatusCode, string(acctBytes))
	}
	var acctJSON struct {
		BillingTerms struct {
			BillingMode             string `json:"billing_mode"`
			DLRWaitHours            int    `json:"dlr_wait_hours"`
			NoDLRPolicy             string `json:"no_dlr_policy"`
			FailedAfterAcceptPolicy string `json:"failed_after_accept_policy"`
		} `json:"billing_terms"`
	}
	_ = json.Unmarshal(acctBytes, &acctJSON)
	if acctJSON.BillingTerms.BillingMode != "on_provider_accept" || acctJSON.BillingTerms.DLRWaitHours != 18 ||
		acctJSON.BillingTerms.NoDLRPolicy != "immediate_release" || acctJSON.BillingTerms.FailedAfterAcceptPolicy != "refund" {
		t.Fatalf("GET /account billing terms mismatch: %+v", acctJSON.BillingTerms)
	}
	// Customer edge must NEVER leak buy cost, margin, or provider internals!
	for _, forbidden := range []string{"buy_cost", "buy_price", "provider_id", "margin", "Edge Test Prov"} {
		if strings.Contains(string(acctBytes), forbidden) {
			t.Fatalf("SECURITY: forbidden internal field %q leaked in GET /account", forbidden)
		}
	}

	reqRates := httptest.NewRequest("GET", "/api/v1/customer/rates", nil)
	reqRates.Header.Set("Authorization", "Bearer "+fullKey)
	respRates, _ := app.Test(reqRates, -1)
	ratesBytes, _ := io.ReadAll(respRates.Body)
	if respRates.StatusCode != 200 || !strings.Contains(string(ratesBytes), "0.045") {
		t.Fatalf("GET /rates failed: status=%d body=%s", respRates.StatusCode, string(ratesBytes))
	}

	reqEst := httptest.NewRequest("POST", "/api/v1/customer/sms/estimate", strings.NewReader(`{"destination":"+12025550199","sender_id":"ZNERO","text":"Hello customer API test"}`))
	reqEst.Header.Set("Content-Type", "application/json")
	reqEst.Header.Set("Authorization", "Bearer "+fullKey)
	respEst, _ := app.Test(reqEst, -1)
	estBytes, _ := io.ReadAll(respEst.Body)
	if respEst.StatusCode != 200 {
		t.Fatalf("POST /sms/estimate failed: status=%d body=%s", respEst.StatusCode, string(estBytes))
	}

	// 5. Submit SMS + Idempotency + Rate Limiting (customer TPS=3 -> 4th immediate send returns 429)
	secretBodyText := "OTP_EDGE_SECRET_8821"
	submitBody := `{"destination":"+12025550188","sender_id":"ZNERO","text":"` + secretBodyText + `","client_reference":"edge-ref-1"}`
	reqSub := httptest.NewRequest("POST", "/api/v1/customer/sms", strings.NewReader(submitBody))
	reqSub.Header.Set("Content-Type", "application/json")
	reqSub.Header.Set("Authorization", "Bearer "+sendOnlyKey)
	respSub, _ := app.Test(reqSub, -1)
	subBytes, _ := io.ReadAll(respSub.Body)
	if respSub.StatusCode != 201 && respSub.StatusCode != 202 && respSub.StatusCode != 200 {
		t.Fatalf("POST /sms failed: status=%d body=%s", respSub.StatusCode, string(subBytes))
	}

	// Burst rate limit test: burn remaining TPS tokens until 429 appears
	got429 := false
	for i := 0; i < 10; i++ {
		r := httptest.NewRequest("POST", "/api/v1/customer/sms", strings.NewReader(`{"destination":"+12025550188","sender_id":"ZNERO","text":"hi"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+fullKey)
		rsp, _ := app.Test(r, -1)
		if rsp.StatusCode == 429 {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatalf("expected 429 Too Many Requests when exceeding customer TPS=3")
	}

	// 6. Log leak verification
	lc.ScanForLeaks(t)
	logs := lc.Output()
	if strings.Contains(logs, fullKey) || strings.Contains(logs, sendOnlyKey) ||
		strings.Contains(logs, secretBodyText) || strings.Contains(logs, "+12025550188") {
		t.Fatalf("SECURITY: API key, SMS text, or full phone number leaked into logs!")
	}
}
