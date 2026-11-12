package app_test

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
	"github.com/znero-international/znero/backend/internal/analytics"
	"github.com/znero-international/znero/backend/internal/app"
	"github.com/znero-international/znero/backend/internal/auth"
	"github.com/znero-international/znero/backend/internal/billing"
	"github.com/znero-international/znero/backend/internal/config"
	"github.com/znero-international/znero/backend/internal/customer"
	"github.com/znero-international/znero/backend/internal/customeredge"
	"github.com/znero-international/znero/backend/internal/health"
	"github.com/znero-international/znero/backend/internal/httpprovider"
	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/masterdata/importjob"
	"github.com/znero-international/znero/backend/internal/masterdata/operator"
	"github.com/znero-international/znero/backend/internal/masterdata/prefix"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/number"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/internal/provider"
	"github.com/znero-international/znero/backend/internal/rating"
	"github.com/znero-international/znero/backend/internal/rbac"
	"github.com/znero-international/znero/backend/internal/routing"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/internal/senderid"
	"github.com/znero-international/znero/backend/internal/system"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/pkg/tokens"
)

func buildFullHTTPApp(t *testing.T, e *testkit.Env, tokenMgr *tokens.TokenManager) (*fiber.App, *rbac.Service, *org.Service, auth.Repository, org.Repository, rbac.Repository, *customer.Service, *secrets.KeyRing) {
	t.Helper()
	_, logger := testkit.NewLogCapture()

	kr, err := secrets.NewKeyRingFromEnv("test", testkit.RandHex(32), "v1", "")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}

	authRepo := auth.NewRepository(e.Pool)
	orgRepo := org.NewRepository(e.Pool)
	rbacRepo := rbac.NewRepository(e.Pool)
	orgSvc := org.NewService(orgRepo, rbacRepo, e.Audit, e.Pool)
	rbacSvc := rbac.NewService(rbacRepo, e.Audit, e.Pool)

	mfaKey := testkit.RandHex(32)
	cfg := &config.Config{
		Env: "test",
		Server: config.ServerConfig{
			ReadTimeout:     10 * time.Second,
			WriteTimeout:    10 * time.Second,
			IdleTimeout:     10 * time.Second,
			BodyLimitBytes:  2 * 1024 * 1024,
			RateLimitMax:    5000,
			RateLimitWindow: time.Minute,
			CORSOrigins:     []string{"http://127.0.0.1:5173"},
		},
		ReadyCheckTimeout: 2 * time.Second,
	}

	authSvc := auth.NewService(authRepo, orgRepo, rbacRepo, e.Audit, tokenMgr, auth.NewDevConsoleMailer("test", logger), e.Pool, mfaKey, "test")
	numSvc := number.NewService(number.NewRepository(e.Pool), e.PrefixRepo, e.Prefix, e.Approval, e.Audit, e.Pool, logger)
	provSvc := provider.NewService(provider.NewRepository(e.Pool), e.Audit, e.Approval, kr, "test")
	custSvc := customer.NewService(customer.NewRepository(e.Pool), e.Audit, e.Approval)
	senderSvc := senderid.NewService(senderid.NewRepository(e.Pool), e.Audit, e.Approval)
	rateSvc := rating.NewService(rating.NewRepository(e.Pool), e.Audit, e.Approval, numSvc, nil)
	ledgerRepo := ledger.NewRepository(e.Pool)
	ledgerSvc := ledger.NewService(ledgerRepo, orgRepo, e.Approval, e.Audit, e.Pool)
	billingSvc := billing.NewService(billing.NewRepository(e.Pool), orgRepo, ledgerRepo, e.Approval, e.Audit, e.Pool)
	routingEngine := routing.NewEngine(e.Pool, nil, nil, nil)
	routingSvc := routing.NewService(routing.NewRepository(e.Pool), e.Approval, e.Audit, e.Pool, routingEngine)
	msgRepo := messaging.NewRepository(e.Pool)
	msgSvc, err := messaging.NewService(e.Pool, msgRepo, messaging.DefaultConfig(), routingEngine, ledgerSvc, kr, e.Audit, messaging.NewFakeSender(), logger)
	if err != nil {
		t.Fatalf("messaging service: %v", err)
	}
	reconcileSvc := messaging.NewReconciliationService(e.Pool, msgRepo, ledgerSvc, logger)
	dlrProc := messaging.NewDLRProcessor(msgRepo, ledgerSvc, "on_delivery", logger)
	httpClient := httpprovider.NewClient(httpprovider.ClientConfig{
		Pool:          e.Pool,
		Decrypter:     kr,
		AllowLoopback: true,
		Logger:        logger,
	})
	httpCallback := httpprovider.NewCallbackHandler(httpClient, dlrProc, logger)

	edgeAuth := customeredge.NewEdgeAuthenticator(e.Pool, nil, logger)
	edgeHandler := customeredge.NewEdgeHandler(edgeAuth, customeredge.NewCustomerLimiter(), msgSvc, msgRepo, ledgerSvc, rateSvc, kr, e.Pool)
	edgeHandler.SetAllowLoopback("test", true)

	mods := app.Modules{
		Health:               health.NewHandler(health.NewService("test", time.Second, logger)),
		System:               system.NewHandler(system.NewService(system.NewRepository(e.Pool), "test", "test")),
		Auth:                 auth.NewHandler(authSvc, cfg),
		Org:                  org.NewHandler(orgSvc),
		RBAC:                 rbac.NewHandler(rbacSvc),
		Country:              country.NewHandler(e.Country),
		Operator:             operator.NewHandler(e.Operator),
		Prefix:               prefix.NewHandler(e.Prefix),
		Approval:             approval.NewHandler(e.Approval),
		ImportJob:            importjob.NewHandler(e.Import),
		Number:               number.NewHandler(numSvc),
		Provider:             provider.NewHandler(provSvc),
		Customer:             customer.NewHandler(custSvc),
		SenderID:             senderid.NewHandler(senderSvc),
		Rating:               rating.NewHandler(rateSvc),
		Ledger:               ledger.NewHandler(ledgerSvc),
		Billing:              billing.NewHandler(billingSvc),
		Routing:              routing.NewHandler(routingSvc),
		Messaging:            messaging.NewHandler(msgSvc, reconcileSvc, nil, msgRepo),
		CustomerEdge:         edgeHandler,
		HTTPProviderCallback: httpCallback,
		Analytics:            analytics.NewHandler(analytics.NewService(analytics.NewRepository(e.Pool, logger), logger)),
		AuthMiddleware:       auth.Authenticate(tokenMgr, authRepo, orgRepo),
	}

	httpApp := app.NewHTTP(cfg, logger, mods)
	return httpApp, rbacSvc, orgSvc, authRepo, orgRepo, rbacRepo, custSvc, kr
}

type matrixPrincipal struct {
	name            string
	orgID           uuid.UUID
	orgType         string
	orgPath         string
	isPlatformAdmin bool
	token           string
}

func seedPrincipal(
	t *testing.T,
	e *testkit.Env,
	authRepo auth.Repository,
	tokenMgr *tokens.TokenManager,
	name string,
	orgID uuid.UUID,
	orgType, orgPath string,
	isPlatformAdmin bool,
	perms []string,
) matrixPrincipal {
	t.Helper()
	ctx := context.Background()

	pwHash := "unused-hash"
	u, err := authRepo.CreateUser(ctx, nil, auth.User{
		OrganizationID: orgID,
		Email:          "matrix-" + name + "-" + testkit.RandHex(4) + "@znero.test",
		PasswordHash:   &pwHash,
		Status:         "active",
	}, nil)
	if err != nil {
		t.Fatalf("create user %s: %v", name, err)
	}
	sessID := uuid.New()
	if err := authRepo.CreateSession(ctx, nil, auth.Session{
		ID:               sessID,
		UserID:           u.ID,
		OrganizationID:   orgID,
		RefreshTokenHash: testkit.RandHex(32),
		FamilyID:         uuid.New(),
		IPAddress:        "127.0.0.1",
		UserAgent:        "matrix-test",
		ExpiresAt:        time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create session %s: %v", name, err)
	}
	tok, err := tokenMgr.GenerateAccessToken(u.ID, orgID, sessID, orgType, orgPath, isPlatformAdmin, perms)
	if err != nil {
		t.Fatalf("generate token %s: %v", name, err)
	}
	return matrixPrincipal{
		name:            name,
		orgID:           orgID,
		orgType:         orgType,
		orgPath:         orgPath,
		isPlatformAdmin: isPlatformAdmin,
		token:           tok,
	}
}

func TestAuthzMatrix_AllRoutesAcrossFivePrincipals(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()

	jwtSecret := testkit.RandHex(32)
	tokenMgr, err := tokens.NewTokenManager(jwtSecret, time.Hour)
	if err != nil {
		t.Fatalf("token manager: %v", err)
	}

	httpApp, _, orgSvc, authRepo, _, rbacRepo, _, _ := buildFullHTTPApp(t, e, tokenMgr)

	// 1. Create a real reseller org & child customer org through org.Service (invokes SeedSystemRoles!)
	resellerName := "matrix-res-" + testkit.RandHex(4)
	platID := e.PlatformOrgID
	resellerOrg, err := orgSvc.Create(ctx, e.Override, org.CreateOrgRequest{
		ParentID: &platID,
		Name:     resellerName,
		Type:     "reseller",
	})
	if err != nil {
		t.Fatalf("create reseller org: %v", err)
	}

	custName := "matrix-cust-" + testkit.RandHex(4)
	resID := resellerOrg.ID
	customerOrg, err := orgSvc.Create(ctx, e.Override, org.CreateOrgRequest{
		ParentID: &resID,
		Name:     custName,
		Type:     "customer",
	})
	if err != nil {
		t.Fatalf("create customer org: %v", err)
	}

	// BUG-016 regression check: verify SeedSystemRoles did NOT grant platform-only Phase 8 permissions
	// to reseller or customer Org Admin roles in the real database.
	for _, checkOrg := range []struct {
		label string
		id    uuid.UUID
	}{{"reseller", resellerOrg.ID}, {"customer", customerOrg.ID}} {
		roles, rErr := rbacRepo.ListRoles(ctx, e.Override, &checkOrg.id)
		if rErr != nil {
			t.Fatalf("list roles %s: %v", checkOrg.label, rErr)
		}
		for _, r := range roles {
			rp, pErr := rbacRepo.GetRole(ctx, r.ID)
			if pErr != nil {
				t.Fatalf("get role %s/%s: %v", checkOrg.label, r.Name, pErr)
			}
			for _, permCode := range rp.Permissions {
				if rbac.IsPlatformOnlyPermission(permCode) {
					t.Fatalf("SECURITY BUG-016: non-platform %s role %q received platform-only permission %q", checkOrg.label, r.Name, permCode)
				}
				if checkOrg.label == "customer" && rbac.IsForbiddenForCustomerOrg(permCode) {
					t.Fatalf("SECURITY: customer role %q received forbidden permission %q", r.Name, permCode)
				}
			}
		}
	}

	// Build maximal realistic permission slices from DB permissions filtered via RBAC helpers
	allPerms, err := rbacRepo.ListAllPermissions(ctx)
	if err != nil {
		t.Fatalf("list all permissions: %v", err)
	}
	var allCodes, makerCodes, resellerCodes, customerCodes []string
	for _, p := range allPerms {
		allCodes = append(allCodes, p.Code)
		if !strings.HasSuffix(p.Code, ".approve") && !strings.HasSuffix(p.Code, ".override") && !strings.Contains(p.Code, "override") && p.Code != "billing.invoice.void" && p.Code != "routing.killswitch" {
			makerCodes = append(makerCodes, p.Code)
		}
		if !rbac.IsPlatformOnlyPermission(p.Code) {
			resellerCodes = append(resellerCodes, p.Code)
		}
		if !rbac.IsForbiddenForCustomerOrg(p.Code) {
			customerCodes = append(customerCodes, p.Code)
		}
	}

	principals := []matrixPrincipal{
		{name: "unauthenticated"},
		seedPrincipal(t, e, authRepo, tokenMgr, "platform_super_admin", e.PlatformOrgID, "platform", e.PlatformOrgPath, true, allCodes),
		seedPrincipal(t, e, authRepo, tokenMgr, "platform_maker", e.PlatformOrgID, "platform", e.PlatformOrgPath, false, makerCodes),
		seedPrincipal(t, e, authRepo, tokenMgr, "reseller_admin", resellerOrg.ID, "reseller", resellerOrg.Path, false, resellerCodes),
		seedPrincipal(t, e, authRepo, tokenMgr, "customer_admin", customerOrg.ID, "customer", customerOrg.Path, false, customerCodes),
	}

	// Seed a real customer profile under resellerOrg -> customerOrg for billing-terms boundary tests
	var custProfID uuid.UUID
	if err := e.Pool.QueryRow(ctx, `
		INSERT INTO customer_profiles (
			organization_id, account_type, billing_currency, default_max_tps,
			billing_mode, dlr_wait_hours, no_dlr_policy, failed_after_accept_policy, status, version
		) VALUES ($1, 'prepaid', 'USD', 20, 'on_delivery', 24, 'retain_until_resolution', 'keep_charge', 'active', 1)
		RETURNING id`,
		customerOrg.ID,
	).Scan(&custProfID); err != nil {
		t.Fatalf("insert customer profile: %v", err)
	}

	// Public routes explicitly allowed without Bearer JWT authentication
	publicUnauthAllowed := map[string]bool{
		"GET /healthz":                                      true,
		"GET /readyz":                                       true,
		"POST /api/v1/auth/login":                           true,
		"POST /api/v1/auth/mfa/verify":                      true,
		"POST /api/v1/auth/refresh":                         true,
		"POST /api/v1/auth/logout":                          true,
		"POST /api/v1/auth/password/reset-request":          true,
		"POST /api/v1/auth/password/reset-confirm":          true,
		"POST /api/v1/auth/invitation/accept":               true,
		"GET /api/v1/provider-callbacks/dlr/:connectionId":  true,
		"POST /api/v1/provider-callbacks/dlr/:connectionId": true,
	}

	platformOnlyPathPrefixes := []string{
		"/api/v1/system",
		"/api/v1/providers",
		"/api/v1/imports",
		"/api/v1/routing",
		"/api/v1/rates/buy",
		"/api/v1/rates/margin",
		"/api/v1/ledger/reconciliation",
		"/api/v1/messaging/test-send",
		"/api/v1/messaging/smpp",
		"/api/v1/messaging/queue",
		"/api/v1/messaging/dlrs",
		"/api/v1/messaging/settlement",
		"/api/v1/messaging/reconciliation",
		"/api/v1/messaging/dlr-mappings",
		"/api/v1/messaging/customer/sessions",
		"/api/v1/messaging/inbound/numbers",
		"/api/v1/messaging/webhooks",
	}

	isPlatformOnlyRoute := func(method, routePath string) bool {
		for _, pfx := range platformOnlyPathPrefixes {
			if strings.HasPrefix(routePath, pfx) {
				return true
			}
		}
		if strings.HasPrefix(routePath, "/api/v1/approvals") && (strings.HasSuffix(routePath, "/approve") || strings.HasSuffix(routePath, "/reject")) {
			return true
		}
		if method != "GET" && (strings.HasPrefix(routePath, "/api/v1/countries") ||
			strings.HasPrefix(routePath, "/api/v1/operators") ||
			strings.HasPrefix(routePath, "/api/v1/prefixes") ||
			strings.HasPrefix(routePath, "/api/v1/number-rules") ||
			strings.HasPrefix(routePath, "/api/v1/rates/assignments")) {
			return true
		}
		return false
	}

	customerForbiddenPrefixes := []string{
		"/api/v1/customer-profiles",
		"/api/v1/rates/sell-cards",
		"/api/v1/rates/import",
	}
	isCustomerForbidden := func(method, routePath string) bool {
		if isPlatformOnlyRoute(method, routePath) || strings.HasPrefix(routePath, "/api/v1/rates/assignments") {
			return true
		}
		if method != "GET" {
			for _, pfx := range customerForbiddenPrefixes {
				if strings.HasPrefix(routePath, pfx) {
					return true
				}
			}
			if routePath == "/api/v1/wallets/:id/topup" ||
				routePath == "/api/v1/wallets/:id/adjust" ||
				routePath == "/api/v1/wallets/:id/freeze" ||
				routePath == "/api/v1/wallets/:id/unfreeze" ||
				strings.HasPrefix(routePath, "/api/v1/invoices") ||
				strings.HasPrefix(routePath, "/api/v1/payments") {
				return true
			}
		}
		return false
	}

	// Walk every registered Fiber route dynamically across all 5 principals
	seen := make(map[string]bool)
	checkedCount := 0
	for _, r := range httpApp.GetRoutes(true) {
		if r.Method == "HEAD" || r.Method == "OPTIONS" {
			continue
		}
		key := r.Method + " " + r.Path
		if seen[key] {
			continue
		}
		seen[key] = true
		checkedCount++

		concretePath := replaceRouteParams(r.Path, custProfID.String())

		for _, p := range principals {
			if key == "POST /api/v1/auth/logout-all" && p.token != "" {
				continue // Do not revoke the test principal's own active session mid-sweep
			}
			req := httptest.NewRequest(r.Method, concretePath, strings.NewReader(`{}`))
			if r.Method == "POST" || r.Method == "PUT" || r.Method == "PATCH" {
				req.Header.Set("Content-Type", "application/json")
			}
			if p.token != "" {
				req.Header.Set("Authorization", "Bearer "+p.token)
			}
			resp, reqErr := httpApp.Test(req, -1)
			if reqErr != nil {
				t.Fatalf("route %s [%s] request error: %v", key, p.name, reqErr)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()

			if p.name == "unauthenticated" {
				if !publicUnauthAllowed[key] && resp.StatusCode != 401 {
					t.Fatalf("UNAUTHENTICATED BYPASS: route %s returned %d (want 401)", key, resp.StatusCode)
				}
				continue
			}

			// Customer Edge (/api/v1/customer/...) uses API keys, NOT dashboard JWT -> must return 401 with JWT
			if strings.HasPrefix(r.Path, "/api/v1/customer/") {
				if resp.StatusCode != 401 {
					t.Fatalf("EDGE AUTH BYPASS: dashboard JWT on %s [%s] returned %d (want 401)", key, p.name, resp.StatusCode)
				}
				continue
			}

			if p.name == "reseller_admin" && isPlatformOnlyRoute(r.Method, r.Path) {
				if resp.StatusCode != 403 {
					t.Fatalf("RESELLER PRIVILEGE LEAK: %s returned %d (want 403)", key, resp.StatusCode)
				}
			}
			if p.name == "customer_admin" && isCustomerForbidden(r.Method, r.Path) {
				if resp.StatusCode != 403 {
					t.Fatalf("CUSTOMER PRIVILEGE LEAK: %s returned %d (want 403)", key, resp.StatusCode)
				}
			}
			if p.name == "platform_maker" && (strings.HasSuffix(r.Path, "/approve") || strings.HasSuffix(r.Path, "/reject")) {
				if resp.StatusCode != 403 {
					t.Fatalf("MAKER SELF-APPROVE BYPASS: %s returned %d (want 403)", key, resp.StatusCode)
				}
			}
		}
	}
	if checkedCount < 80 {
		t.Fatalf("expected >= 80 registered routes in matrix crawl, got %d", checkedCount)
	}
	t.Logf("Audited %d unique HTTP routes across 5 principals cleanly.", checkedCount)

	// Explicit Step 2 Billing-Terms Enforcement Tests across principals:
	// A. Reseller cannot set or alter billing terms on POST or PUT /customer-profiles
	resellerTok := principals[3].token
	reqResCreate := httptest.NewRequest("POST", "/api/v1/customer-profiles", strings.NewReader(`{
		"organization_id": "`+customerOrg.ID.String()+`",
		"account_type": "prepaid",
		"billing_currency": "USD",
		"billing_mode": "on_provider_accept",
		"reason": "unauthorized billing terms"
	}`))
	reqResCreate.Header.Set("Content-Type", "application/json")
	reqResCreate.Header.Set("Authorization", "Bearer "+resellerTok)
	respResCreate, _ := httpApp.Test(reqResCreate, -1)
	if respResCreate.StatusCode != 403 {
		t.Fatalf("reseller POST /customer-profiles with billing_mode must return 403, got %d", respResCreate.StatusCode)
	}

	reqResPut := httptest.NewRequest("PUT", "/api/v1/customer-profiles/"+custProfID.String(), strings.NewReader(`{
		"billing_mode": "on_provider_accept",
		"dlr_wait_hours": 12,
		"reason": "unauthorized billing update"
	}`))
	reqResPut.Header.Set("Content-Type", "application/json")
	reqResPut.Header.Set("Authorization", "Bearer "+resellerTok)
	respResPut, _ := httpApp.Test(reqResPut, -1)
	if respResPut.StatusCode != 403 {
		t.Fatalf("reseller PUT /customer-profiles/:id billing terms must return 403, got %d", respResPut.StatusCode)
	}

	// B. Platform Maker editing billing_mode creates pending_approval (maker-checker) without live mutation
	makerTok := principals[2].token
	reqMakerPut := httptest.NewRequest("PUT", "/api/v1/customer-profiles/"+custProfID.String(), strings.NewReader(`{
		"billing_mode": "on_provider_accept",
		"dlr_wait_hours": 16,
		"no_dlr_policy": "capture_at_ttl",
		"failed_after_accept_policy": "refund",
		"reason": "agreed deal modification"
	}`))
	reqMakerPut.Header.Set("Content-Type", "application/json")
	reqMakerPut.Header.Set("Authorization", "Bearer "+makerTok)
	respMakerPut, _ := httpApp.Test(reqMakerPut, -1)
	bodyMaker, _ := io.ReadAll(respMakerPut.Body)
	if respMakerPut.StatusCode != 200 || !strings.Contains(string(bodyMaker), `"pending_approval":true`) {
		t.Fatalf("platform_maker billing terms edit must create pending_approval, got %d body=%s", respMakerPut.StatusCode, string(bodyMaker))
	}

	// Verify live customer_profiles row is UNCHANGED before approval
	var liveMode string
	_ = e.Pool.QueryRow(ctx, `SELECT billing_mode FROM customer_profiles WHERE id = $1`, custProfID).Scan(&liveMode)
	if liveMode != "on_delivery" {
		t.Fatalf("maker edit bypassed approval: live billing_mode changed to %q", liveMode)
	}

	// C. Customer Edge /api/v1/customer/account shows billing_terms read-only & NEVER leaks provider/cost/margin
	rawKey := "znk_live_" + testkit.RandHex(24)
	sum := sha256.Sum256([]byte(rawKey))
	if _, err := e.Pool.Exec(ctx, `
		INSERT INTO customer_api_keys (organization_id, label, key_prefix, key_hash, scopes, status)
		VALUES ($1, 'matrix-edge', $2, $3, ARRAY['account:read'], 'active')`,
		customerOrg.ID, rawKey[:17], hex.EncodeToString(sum[:]),
	); err != nil {
		t.Fatalf("insert edge key: %v", err)
	}
	reqEdge := httptest.NewRequest("GET", "/api/v1/customer/account", nil)
	reqEdge.Header.Set("Authorization", "Bearer "+rawKey)
	respEdge, _ := httpApp.Test(reqEdge, -1)
	edgeBody, _ := io.ReadAll(respEdge.Body)
	if respEdge.StatusCode != 200 {
		t.Fatalf("customer edge /account returned %d: %s", respEdge.StatusCode, string(edgeBody))
	}
	var acctDoc map[string]any
	_ = json.Unmarshal(edgeBody, &acctDoc)
	edgeLower := strings.ToLower(string(edgeBody))
	for _, forbiddenLeak := range []string{"provider", "buy_price", "estimated_buy_cost", "actual_buy_cost", "margin"} {
		if strings.Contains(edgeLower, forbiddenLeak) {
			t.Fatalf("DATA LEAK: customer edge /account leaked %q: %s", forbiddenLeak, string(edgeBody))
		}
	}
	if _, ok := acctDoc["billing_terms"]; !ok {
		t.Fatalf("customer edge /account missing read-only billing_terms: %s", string(edgeBody))
	}
}

func replaceRouteParams(routePath, profileUUID string) string {
	parts := strings.Split(routePath, "/")
	for i, seg := range parts {
		if strings.HasPrefix(seg, ":") {
			if seg == ":id" && i > 0 && parts[i-1] == "customer-profiles" {
				parts[i] = profileUUID
			} else {
				parts[i] = uuid.NewString()
			}
		}
	}
	return strings.Join(parts, "/")
}
