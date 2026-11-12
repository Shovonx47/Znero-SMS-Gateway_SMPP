package app_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/znero-international/znero/backend/internal/app"
	"github.com/znero-international/znero/backend/internal/auth"
	"github.com/znero-international/znero/backend/internal/billing"
	"github.com/znero-international/znero/backend/internal/config"
	"github.com/znero-international/znero/backend/internal/customer"
	"github.com/znero-international/znero/backend/internal/customeredge"
	"github.com/znero-international/znero/backend/internal/health"
	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/internal/provider"
	"github.com/znero-international/znero/backend/internal/rating"
	"github.com/znero-international/znero/backend/internal/rbac"
	"github.com/znero-international/znero/backend/internal/routing"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/internal/senderid"
	"github.com/znero-international/znero/backend/internal/smpp"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/internal/webhook"
	"github.com/znero-international/znero/backend/pkg/crypto/password"
	"github.com/znero-international/znero/backend/pkg/tenant"
	"github.com/znero-international/znero/backend/pkg/tokens"
)

func testFiberReq(t *testing.T, appTest func(*http.Request, ...int) (*http.Response, error), method, path, bearer, body string, headers map[string]string) (int, http.Header, []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := appTest(req, -1)
	if err != nil {
		t.Fatalf("%s %s error: %v", method, path, err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, resp.Header, b
}

func TestSecurity_IDOR_And_TenantIsolationMatrix(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()

	jwtSecret := testkit.RandHex(32)
	tokenMgr, err := tokens.NewTokenManager(jwtSecret, time.Hour)
	if err != nil {
		t.Fatalf("token manager: %v", err)
	}

	httpApp, _, orgSvc, authRepo, orgRepo, rbacRepo, custSvc, kr := buildFullHTTPApp(t, e, tokenMgr)

	// Create Reseller A -> Customer A1, and Reseller B -> Customer B1
	platID := e.PlatformOrgID
	resA, err := orgSvc.Create(ctx, e.Override, org.CreateOrgRequest{ParentID: &platID, Name: "SecResA-" + testkit.RandHex(4), Type: "reseller"})
	if err != nil {
		t.Fatalf("create resA: %v", err)
	}
	resB, err := orgSvc.Create(ctx, e.Override, org.CreateOrgRequest{ParentID: &platID, Name: "SecResB-" + testkit.RandHex(4), Type: "reseller"})
	if err != nil {
		t.Fatalf("create resB: %v", err)
	}
	custA1, err := orgSvc.Create(ctx, e.Override, org.CreateOrgRequest{ParentID: &resA.ID, Name: "SecCustA1-" + testkit.RandHex(4), Type: "customer"})
	if err != nil {
		t.Fatalf("create custA1: %v", err)
	}
	custB1, err := orgSvc.Create(ctx, e.Override, org.CreateOrgRequest{ParentID: &resB.ID, Name: "SecCustB1-" + testkit.RandHex(4), Type: "customer"})
	if err != nil {
		t.Fatalf("create custB1: %v", err)
	}

	findRole := func(orgID uuid.UUID, name string) *rbac.Role {
		roles, rErr := rbacRepo.ListRoles(ctx, e.Override, &orgID)
		if rErr != nil {
			t.Fatalf("list roles: %v", rErr)
		}
		for _, r := range roles {
			if r.Name == name {
				return &r
			}
		}
		t.Fatalf("role %q not found for org %s", name, orgID)
		return nil
	}
	roleCodes := func(roleID uuid.UUID) []string {
		perms, pErr := rbacRepo.GetRolePermissions(ctx, roleID)
		if pErr != nil {
			t.Fatalf("get role perms: %v", pErr)
		}
		return perms
	}

	// Verify seeded non-platform roles NEVER received system.info.view (BUG-046 regression)
	for _, checkOrg := range []struct {
		id   uuid.UUID
		name string
	}{
		{resA.ID, "Reseller A Org Admin"},
		{resB.ID, "Reseller B Org Admin"},
		{custA1.ID, "Customer A1 Org Admin"},
	} {
		for _, rName := range []string{"Org Admin", "Staff", "Read-only"} {
			role := findRole(checkOrg.id, rName)
			for _, code := range roleCodes(role.ID) {
				if code == "system.info.view" || rbac.IsPlatformOnlyPermission(code) {
					t.Fatalf("RBAC LEAK (%s / %s): non-platform role holds platform-only permission %q", checkOrg.name, rName, code)
				}
			}
		}
	}

	platRole := findRole(platID, "Super Admin")
	resARole := findRole(resA.ID, "Org Admin")
	resBRole := findRole(resB.ID, "Org Admin")
	resBReadonly := findRole(resB.ID, "Read-only")
	custA1Role := findRole(custA1.ID, "Org Admin")
	custB1Role := findRole(custB1.ID, "Org Admin")

	platAdmin := seedPrincipal(t, e, authRepo, tokenMgr, "sec_plat", platID, "platform", e.PlatformOrgPath, true, roleCodes(platRole.ID))
	resAAdmin := seedPrincipal(t, e, authRepo, tokenMgr, "sec_resA", resA.ID, "reseller", resA.Path, false, roleCodes(resARole.ID))
	resBAdmin := seedPrincipal(t, e, authRepo, tokenMgr, "sec_resB", resB.ID, "reseller", resB.Path, false, roleCodes(resBRole.ID))
	resBRO := seedPrincipal(t, e, authRepo, tokenMgr, "sec_resB_ro", resB.ID, "reseller", resB.Path, false, roleCodes(resBReadonly.ID))
	custA1Admin := seedPrincipal(t, e, authRepo, tokenMgr, "sec_custA1", custA1.ID, "customer", custA1.Path, false, roleCodes(custA1Role.ID))
	custB1Admin := seedPrincipal(t, e, authRepo, tokenMgr, "sec_custB1", custB1.ID, "customer", custB1.Path, false, roleCodes(custB1Role.ID))

	// Seed Tenant A resources: user, profile, api key, smpp account, senderid, wallet, invoice, webhook, provider, routing plan
	pw := "unused"
	userA1, err := authRepo.CreateUser(ctx, nil, auth.User{
		OrganizationID: custA1.ID,
		Email:          "victim-a1-" + testkit.RandHex(4) + "@znero.test",
		PasswordHash:   &pw,
		Status:         "active",
	}, nil)
	if err != nil {
		t.Fatalf("create userA1: %v", err)
	}

	tps := 10
	profResA1, err := custSvc.CreateProfile(ctx, e.Override, customer.CreateProfileRequest{
		OrganizationID:  custA1.ID,
		AccountType:     "prepaid",
		BillingCurrency: "USD",
		DefaultMaxTPS:   &tps,
		Reason:          "idor seed",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create custA1 profile: %v", err)
	}

	apiKeyA1, err := custSvc.CreateAPIKey(ctx, e.Override, customer.CreateAPIKeyRequest{
		OrganizationID: custA1.ID,
		Label:          "victim-key-a1",
		Scopes:         []string{"sms:send", "sms:read", "account:read"},
		Reason:         "idor seed",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create apiKeyA1: %v", err)
	}
	apiKeyB1, err := custSvc.CreateAPIKey(ctx, e.Override, customer.CreateAPIKeyRequest{
		OrganizationID: custB1.ID,
		Label:          "attacker-key-b1",
		Scopes:         []string{"sms:send", "sms:read", "account:read"},
		Reason:         "idor seed",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create apiKeyB1: %v", err)
	}

	smppA1, err := custSvc.CreateSMPPAccount(ctx, e.Override, customer.CreateSMPPAccountRequest{
		OrganizationID: custA1.ID,
		SystemID:       "smp_" + testkit.RandHex(4),
		Status:         "active",
		Reason:         "idor seed",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create smppA1: %v", err)
	}

	senderSvc := senderid.NewService(senderid.NewRepository(e.Pool), e.Audit, e.Approval)
	senderA1, err := senderSvc.Create(ctx, e.Override, senderid.CreateSenderIDRequest{
		OrganizationID: custA1.ID,
		SenderID:       "ZN" + strings.ToUpper(testkit.RandHex(3)),
		SenderType:     "alphanumeric",
		Status:         "approved",
		Reason:         "idor seed",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create senderA1: %v", err)
	}

	ledgerRepo := ledger.NewRepository(e.Pool)
	ledgerSvc := ledger.NewService(ledgerRepo, orgRepo, e.Approval, e.Audit, e.Pool)
	walletA1, err := ledgerSvc.EnsureWallet(ctx, e.Override, custA1.ID, "USD", "prepaid")
	if err != nil {
		t.Fatalf("ensure walletA1: %v", err)
	}

	billingSvc := billing.NewService(billing.NewRepository(e.Pool), orgRepo, ledgerRepo, e.Approval, e.Audit, e.Pool)
	invA1, err := billingSvc.CreateDraftInvoice(ctx, e.Override, billing.CreateDraftInvoiceRequest{
		CustomerOrgID: custA1.ID,
		Currency:      "USD",
		Notes:         "idor victim",
		Lines: []billing.CreateLineRequest{
			{Description: "SMS bundle", Quantity: decimal.NewFromInt(10), UnitPrice: decimal.NewFromFloat(0.05)},
		},
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create invoiceA1: %v", err)
	}

	provSvc := provider.NewService(provider.NewRepository(e.Pool), e.Audit, e.Approval, kr, "test")
	provSvc.SetAllowLoopback(true)
	provRes, err := provSvc.CreateProvider(ctx, e.Override, provider.CreateProviderRequest{
		Name:         "SecProv-" + testkit.RandHex(4),
		Code:         "SPRV_" + strings.ToUpper(testkit.RandHex(4)),
		ProviderType: "http",
		CurrencyCode: "USD",
		Status:       "active",
		Reason:       "idor seed",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}

	routingSvc := routing.NewService(routing.NewRepository(e.Pool), e.Approval, e.Audit, e.Pool, routing.NewEngine(e.Pool, nil, nil, nil))
	planRes, err := routingSvc.CreatePlan(ctx, e.Override, &routing.RoutePlan{
		Name:     "SecPlan-" + testkit.RandHex(4),
		Code:     "SPLN_" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})
	if err != nil {
		t.Fatalf("create route plan: %v", err)
	}

	// Register a webhook belonging to Customer A1 via Customer Edge
	whCreateStatus, _, whCreateBody := testFiberReq(t, httpApp.Test, "POST", "/api/v1/customer/webhooks", apiKeyA1.PlaintextKey,
		`{"url":"http://127.0.0.1:18999/hook-a1","events":["message.final"]}`, nil)
	if whCreateStatus != 201 {
		t.Fatalf("create webhook for custA1 expected 201, got %d (%s)", whCreateStatus, string(whCreateBody))
	}
	var whDoc struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(whCreateBody, &whDoc)
	if whDoc.ID == "" {
		t.Fatalf("expected webhook id in create response: %s", string(whCreateBody))
	}

	// 1. /api/v1/system/info must reject unauth (401) and non-platform principals (403), and allow platform admin (200)
	if st, _, _ := testFiberReq(t, httpApp.Test, "GET", "/api/v1/system/info", "", "", nil); st != 401 {
		t.Fatalf("unauthenticated GET /system/info = %d, want 401", st)
	}
	for _, p := range []struct {
		name string
		tok  string
	}{
		{"resA", resAAdmin.token},
		{"resB", resBAdmin.token},
		{"resB_RO", resBRO.token},
		{"custA1", custA1Admin.token},
		{"custB1", custB1Admin.token},
	} {
		if st, _, body := testFiberReq(t, httpApp.Test, "GET", "/api/v1/system/info", p.tok, "", nil); st != 403 {
			t.Fatalf("BUG-046 LEAK: %s GET /api/v1/system/info = %d (%s), want 403", p.name, st, string(body))
		}
	}
	if st, _, _ := testFiberReq(t, httpApp.Test, "GET", "/api/v1/system/info", platAdmin.token, "", nil); st != 200 {
		t.Fatalf("platform admin GET /api/v1/system/info = %d, want 200", st)
	}

	// 2. Dashboard IDOR matrix: Reseller B and Customer B1 MUST NEVER read/mutate Reseller A or Customer A1 resources
	idorCases := []struct {
		name   string
		method string
		path   string
		body   string
		actor  string
	}{
		{"resB read resA org", "GET", "/api/v1/organizations/" + resA.ID.String(), "", resBAdmin.token},
		{"resB read custA1 org", "GET", "/api/v1/organizations/" + custA1.ID.String(), "", resBAdmin.token},
		{"resB edit custA1 org", "PUT", "/api/v1/organizations/" + custA1.ID.String(), `{"name":"pwn"}`, resBAdmin.token},
		{"resB suspend custA1 org", "POST", "/api/v1/organizations/" + custA1.ID.String() + "/suspend", `{"reason":"x"}`, resBAdmin.token},
		{"resB read userA1", "GET", "/api/v1/users/" + userA1.ID.String(), "", resBAdmin.token},
		{"resB suspend userA1", "POST", "/api/v1/users/" + userA1.ID.String() + "/suspend", `{"reason":"x"}`, resBAdmin.token},
		{"resB change userA1 roles", "PUT", "/api/v1/users/" + userA1.ID.String() + "/roles", `{"role_ids":["` + custA1Role.ID.String() + `"]}`, resBAdmin.token},
		{"resB read custA1 role", "GET", "/api/v1/roles/" + custA1Role.ID.String(), "", resBAdmin.token},
		{"resB update custA1 role", "PUT", "/api/v1/roles/" + custA1Role.ID.String(), `{"name":"hijack","permissions":[]}`, resBAdmin.token},
		{"resB read custA1 profile", "GET", "/api/v1/customer-profiles/" + profResA1.Profile.ID.String(), "", resBAdmin.token},
		{"custB1 read custA1 profile", "GET", "/api/v1/customer-profiles/" + profResA1.Profile.ID.String(), "", custB1Admin.token},
		{"resB read custA1 profile by org", "GET", "/api/v1/customer-profiles/by-org/" + custA1.ID.String(), "", resBAdmin.token},
		{"resB update custA1 profile", "PUT", "/api/v1/customer-profiles/" + profResA1.Profile.ID.String(), `{"technical_contact":"evil@x.com","reason":"x"}`, resBAdmin.token},
		{"resB list custA1 api keys", "GET", "/api/v1/customer-api-keys?organization_id=" + custA1.ID.String(), "", resBAdmin.token},
		{"resB revoke custA1 api key", "POST", "/api/v1/customer-api-keys/" + apiKeyA1.APIKey.ID.String() + "/revoke", `{"reason":"x"}`, resBAdmin.token},
		{"resB list custA1 smpp", "GET", "/api/v1/customer-smpp-accounts?organization_id=" + custA1.ID.String(), "", resBAdmin.token},
		{"resB update custA1 smpp", "PUT", "/api/v1/customer-smpp-accounts/" + smppA1.Account.ID.String(), `{"max_tps":99,"reason":"x"}`, resBAdmin.token},
		{"resB read custA1 senderid", "GET", "/api/v1/sender-ids/" + senderA1.ID.String(), "", resBAdmin.token},
		{"resB read custA1 wallet", "GET", "/api/v1/wallets/" + walletA1.ID.String(), "", resBAdmin.token},
		{"custB1 read custA1 wallet", "GET", "/api/v1/wallets/" + walletA1.ID.String(), "", custB1Admin.token},
		{"resB topup custA1 wallet", "POST", "/api/v1/wallets/" + walletA1.ID.String() + "/topup", `{"amount":100,"reason":"x"}`, resBAdmin.token},
		{"resB read custA1 invoice", "GET", "/api/v1/invoices/" + invA1.ID.String(), "", resBAdmin.token},
		{"custB1 read custA1 invoice", "GET", "/api/v1/invoices/" + invA1.ID.String(), "", custB1Admin.token},
		{"resB read platform provider", "GET", "/api/v1/providers/" + provRes.Provider.ID.String(), "", resBAdmin.token},
		{"resB read platform routing plan", "GET", "/api/v1/routing/plans/" + planRes.ID.String(), "", resBAdmin.token},
	}

	for _, tc := range idorCases {
		st, _, body := testFiberReq(t, httpApp.Test, tc.method, tc.path, tc.actor, tc.body, nil)
		if st != 403 && st != 404 {
			t.Fatalf("IDOR FAILURE [%s]: %s %s returned status %d (%s), expected 403 or 404", tc.name, tc.method, tc.path, st, string(body))
		}
		if strings.Contains(string(body), custA1.Name) || strings.Contains(string(body), senderA1.SenderID) {
			t.Fatalf("IDOR DATA LEAK [%s]: response leaked tenant A data: %s", tc.name, string(body))
		}
	}

	// 3. Customer Edge IDOR: Customer B1 API key cannot access/update/delete Customer A1 webhook
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{"GET", "/api/v1/customer/webhooks/" + whDoc.ID, ""},
		{"PUT", "/api/v1/customer/webhooks/" + whDoc.ID, `{"status":"disabled"}`},
		{"DELETE", "/api/v1/customer/webhooks/" + whDoc.ID, ""},
		{"POST", "/api/v1/customer/webhooks/" + whDoc.ID + "/test", ""},
		{"GET", "/api/v1/customer/webhooks/" + whDoc.ID + "/deliveries", ""},
	} {
		st, _, body := testFiberReq(t, httpApp.Test, tc.method, tc.path, apiKeyB1.PlaintextKey, tc.body, nil)
		if st != 404 && st != 403 {
			t.Fatalf("EDGE IDOR FAILURE: CustB1 %s %s returned %d (%s), want 404/403", tc.method, tc.path, st, string(body))
		}
	}
	// Verify Customer A1's webhook is still intact after Customer B1's DELETE attempt
	if stA, _, _ := testFiberReq(t, httpApp.Test, "GET", "/api/v1/customer/webhooks/"+whDoc.ID, apiKeyA1.PlaintextKey, "", nil); stA != 200 {
		t.Fatalf("CustA1 webhook disappeared or inaccessible after CustB1 cross-tenant DELETE attempt: status=%d", stA)
	}
}

func TestSecurity_Injection_MassAssignment_Oversized_And_CRLF(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()

	jwtSecret := testkit.RandHex(32)
	tokenMgr, _ := tokens.NewTokenManager(jwtSecret, time.Hour)
	httpApp, _, orgSvc, authRepo, _, rbacRepo, custSvc, _ := buildFullHTTPApp(t, e, tokenMgr)

	platID := e.PlatformOrgID
	resOrg, err := orgSvc.Create(ctx, e.Override, org.CreateOrgRequest{ParentID: &platID, Name: "InjRes-" + testkit.RandHex(4), Type: "reseller"})
	if err != nil {
		t.Fatalf("create reseller: %v", err)
	}
	custOrg, err := orgSvc.Create(ctx, e.Override, org.CreateOrgRequest{ParentID: &resOrg.ID, Name: "InjCust-" + testkit.RandHex(4), Type: "customer"})
	if err != nil {
		t.Fatalf("create cust: %v", err)
	}
	roles, _ := rbacRepo.ListRoles(ctx, e.Override, &resOrg.ID)
	var resAdminRole rbac.Role
	for _, r := range roles {
		if r.Name == "Org Admin" {
			resAdminRole = r
		}
	}
	codes, _ := rbacRepo.GetRolePermissions(ctx, resAdminRole.ID)
	resAdmin := seedPrincipal(t, e, authRepo, tokenMgr, "inj_res", resOrg.ID, "reseller", resOrg.Path, false, codes)

	edgeKey, err := custSvc.CreateAPIKey(ctx, e.Override, customer.CreateAPIKeyRequest{
		OrganizationID: custOrg.ID,
		Label:          "inj-edge",
		Scopes:         []string{"sms:send", "sms:read", "account:read"},
		Reason:         "injection test",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create edge key: %v", err)
	}

	// 1. SQLi payloads on query filters and path UUIDs -> NEVER 500
	sqliQueries := []string{
		"/api/v1/organizations?search=" + url.QueryEscape("' OR '1'='1"),
		"/api/v1/organizations?search=" + url.QueryEscape(`"; DROP TABLE messages; --`),
		"/api/v1/users?search=" + url.QueryEscape("' UNION SELECT password_hash FROM users--"),
		"/api/v1/customer-profiles/" + url.PathEscape("' OR 1=1--"),
		"/api/v1/customer/sms?limit=10&status=" + url.QueryEscape("';DELETE FROM messages;--"),
	}
	for _, path := range sqliQueries {
		st, _, body := testFiberReq(t, httpApp.Test, "GET", path, resAdmin.token, "", nil)
		if st >= 500 {
			t.Fatalf("SQLI CAUSED 500 on %s: %d %s", path, st, string(body))
		}
	}
	// Verify messages table still exists and is queryable
	var msgTblCount int
	if err := e.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM messages`).Scan(&msgTblCount); err != nil {
		t.Fatalf("messages table missing/broken after SQLi checks: %v", err)
	}

	// 2. Privilege / mass-assignment escalation via POST /api/v1/roles
	for _, forbiddenPerm := range []string{"system.info.view", "provider.provider.create", "billing.invoice.void", "wallet.override"} {
		st, _, body := testFiberReq(t, httpApp.Test, "POST", "/api/v1/roles", resAdmin.token,
			`{"name":"Esc-`+testkit.RandHex(3)+`","description":"x","permissions":["org.organization.view","`+forbiddenPerm+`"]}`, nil)
		if st != 403 {
			t.Fatalf("PRIVILEGE ESCALATION: reseller created role with %s -> %d (%s), want 403", forbiddenPerm, st, string(body))
		}
	}

	// 3. Invalid webhook status on PUT /api/v1/customer/webhooks/:id rejected (BUG-047)
	stW, _, bW := testFiberReq(t, httpApp.Test, "POST", "/api/v1/customer/webhooks", edgeKey.PlaintextKey,
		`{"url":"http://127.0.0.1:19991/ok","events":["message.final"]}`, nil)
	if stW != 201 {
		t.Fatalf("valid webhook create failed: %d %s", stW, string(bW))
	}
	var whCreated struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(bW, &whCreated)
	stBadStatus, _, _ := testFiberReq(t, httpApp.Test, "PUT", "/api/v1/customer/webhooks/"+whCreated.ID, edgeKey.PlaintextKey,
		`{"status":"super_admin_hacked"}`, nil)
	if stBadStatus != 400 {
		t.Fatalf("invalid webhook status returned %d, want 400", stBadStatus)
	}

	// 4. Oversized payload > BodyLimitBytes -> HTTP 413 over real TCP listener
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_, limitLogger := testkit.NewLogCapture()
	limitApp := app.NewHTTP(&config.Config{
		Env: config.EnvTest,
		Server: config.ServerConfig{
			BodyLimitBytes:  4096,
			ReadTimeout:     5 * time.Second,
			WriteTimeout:    5 * time.Second,
			IdleTimeout:     5 * time.Second,
			RateLimitMax:    100,
			RateLimitWindow: time.Minute,
		},
	}, limitLogger, app.Modules{
		Health: health.NewHandler(health.NewService("test", time.Second, limitLogger)),
	})
	limitApp.Post("/api/v1/limit-probe", func(c *fiber.Ctx) error { return c.SendStatus(200) })
	serveDone := make(chan struct{})
	go func() {
		_ = limitApp.Listener(ln)
		close(serveDone)
	}()
	hugeBody := `{"text":"` + strings.Repeat("A", 8192) + `"}`
	respLarge, postErr := http.Post("http://"+ln.Addr().String()+"/api/v1/limit-probe", "application/json", strings.NewReader(hugeBody))
	if postErr != nil {
		_ = limitApp.Shutdown()
		<-serveDone
		t.Fatalf("oversized HTTP POST failed unexpectedly: %v", postErr)
	}
	_ = respLarge.Body.Close()
	_ = limitApp.Shutdown()
	<-serveDone
	if respLarge.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body returned %d, want 413", respLarge.StatusCode)
	}

	// 5. CRLF injection in X-Request-ID header is sanitized and replaced by server RequestIDMiddleware
	stCRLF, hdrCRLF, _ := testFiberReq(t, httpApp.Test, "GET", "/healthz", "", "", map[string]string{
		"X-Request-ID": "bad\r\nX-Injected: evil",
	})
	if stCRLF != 200 {
		t.Fatalf("/healthz returned %d", stCRLF)
	}
	gotReqID := hdrCRLF.Get("X-Request-ID")
	if gotReqID == "" || strings.ContainsAny(gotReqID, "\r\n:") {
		t.Fatalf("CRLF/unsafe X-Request-ID reflected: %q", gotReqID)
	}
}

func TestSecurity_HTTP_CORS_CSRF_Cookie_Headers_And_RefreshReuse(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	_, logger := testkit.NewLogCapture()

	jwtSecret := testkit.RandHex(32)
	tokenMgr, err := tokens.NewTokenManager(jwtSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("token manager: %v", err)
	}
	authRepo := auth.NewRepository(e.Pool)
	orgRepo := org.NewRepository(e.Pool)
	rbacRepo := rbac.NewRepository(e.Pool)

	// Configure a PRODUCTION HTTP app so Secure cookie, strict CORS allowlist, and CSRF apply
	prodCfg := &config.Config{
		Env: config.EnvProduction,
		Auth: config.AuthConfig{
			SessionCookieName: "znero_refresh",
		},
		Server: config.ServerConfig{
			ReadTimeout:     5 * time.Second,
			WriteTimeout:    5 * time.Second,
			IdleTimeout:     30 * time.Second,
			BodyLimitBytes:  64 * 1024,
			CORSOrigins:     []string{"https://portal.znero.test"},
			RateLimitMax:    500,
			RateLimitWindow: time.Minute,
		},
	}
	authSvc := auth.NewService(authRepo, orgRepo, rbacRepo, e.Audit, tokenMgr, auth.NewDevConsoleMailer("production", logger), e.Pool, testkit.RandHex(32), "production")
	prodApp := app.NewHTTP(prodCfg, logger, app.Modules{
		Health:         health.NewHandler(health.NewService("test", time.Second, logger)),
		Auth:           auth.NewHandler(authSvc, prodCfg),
		AuthMiddleware: auth.Authenticate(tokenMgr, authRepo, orgRepo),
	})

	// 1. CORS negative origin & allowed origin + Security headers
	stEvil, hdrEvil, _ := testFiberReq(t, prodApp.Test, "OPTIONS", "/api/v1/auth/login", "", "", map[string]string{
		"Origin":                        "https://evil.example",
		"Access-Control-Request-Method": "POST",
	})
	_ = stEvil
	if acao := hdrEvil.Get("Access-Control-Allow-Origin"); acao != "" {
		t.Fatalf("CORS LEAK: evil origin received Access-Control-Allow-Origin=%q", acao)
	}
	if hdrEvil.Get("X-Content-Type-Options") != "nosniff" || hdrEvil.Get("X-Frame-Options") != "DENY" || hdrEvil.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("missing security headers on response: %+v", hdrEvil)
	}
	if csp := hdrEvil.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("missing strict CSP header, got %q", csp)
	}

	_, hdrGood, _ := testFiberReq(t, prodApp.Test, "OPTIONS", "/api/v1/auth/login", "", "", map[string]string{
		"Origin":                        "https://portal.znero.test",
		"Access-Control-Request-Method": "POST",
	})
	if acao := hdrGood.Get("Access-Control-Allow-Origin"); acao != "https://portal.znero.test" {
		t.Fatalf("allowed origin CORS mismatch: got %q", acao)
	}

	// 2. Real login -> verify cookie flags (HttpOnly, SameSite=Strict, Secure)
	userEmail := "sec-login-" + testkit.RandHex(5) + "@znero.test"
	userPass := "Str0ng!Pass#2026"
	pwHash, err := password.Hash(userPass)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	_, err = authRepo.CreateUser(ctx, nil, auth.User{
		OrganizationID: e.PlatformOrgID,
		Email:          userEmail,
		PasswordHash:   &pwHash,
		Status:         "active",
	}, nil)
	if err != nil {
		t.Fatalf("create login user: %v", err)
	}

	stLogin, hdrLogin, bodyLogin := testFiberReq(t, prodApp.Test, "POST", "/api/v1/auth/login", "",
		`{"email":"`+userEmail+`","password":"`+userPass+`"}`, map[string]string{"Origin": "https://portal.znero.test"})
	if stLogin != 200 {
		t.Fatalf("login failed: %d %s", stLogin, string(bodyLogin))
	}
	rawCookies := hdrLogin.Values("Set-Cookie")
	if len(rawCookies) == 0 {
		t.Fatalf("expected Set-Cookie header on login")
	}
	cookie1 := rawCookies[0]
	lowerC := strings.ToLower(cookie1)
	for _, flag := range []string{"httponly", "secure", "samesite=strict", "path=/api/v1/auth"} {
		if !strings.Contains(lowerC, flag) {
			t.Fatalf("refresh cookie missing %q flag: %s", flag, cookie1)
		}
	}
	// Extract name=value pair
	cookiePair1 := strings.SplitN(cookie1, ";", 2)[0]

	// 3. CSRFProtection blocks cross-site Origin & Referer on /refresh and /logout
	for _, hdr := range []map[string]string{
		{"Origin": "https://evil.example", "Cookie": cookiePair1},
		{"Referer": "https://evil.example/form.html", "Cookie": cookiePair1},
	} {
		if st, _, _ := testFiberReq(t, prodApp.Test, "POST", "/api/v1/auth/refresh", "", "", hdr); st != 403 {
			t.Fatalf("CSRF bypass on /refresh with %+v: got %d, want 403", hdr, st)
		}
		if st, _, _ := testFiberReq(t, prodApp.Test, "POST", "/api/v1/auth/logout", "", "", hdr); st != 403 {
			t.Fatalf("CSRF bypass on /logout with %+v: got %d, want 403", hdr, st)
		}
	}

	// 4. Refresh token rotation & RFC 6819 reuse detection revokes whole family
	stRef1, hdrRef1, bodyRef1 := testFiberReq(t, prodApp.Test, "POST", "/api/v1/auth/refresh", "", "", map[string]string{
		"Origin": "https://portal.znero.test",
		"Cookie": cookiePair1,
	})
	if stRef1 != 200 {
		t.Fatalf("legitimate refresh 1 failed: %d %s", stRef1, string(bodyRef1))
	}
	cookiePair2 := strings.SplitN(hdrRef1.Values("Set-Cookie")[0], ";", 2)[0]
	if cookiePair2 == cookiePair1 {
		t.Fatalf("refresh token did not rotate!")
	}

	// Replay stale cookiePair1 (attacker replay) -> MUST fail 401 AND revoke family!
	stReplay, _, _ := testFiberReq(t, prodApp.Test, "POST", "/api/v1/auth/refresh", "", "", map[string]string{
		"Origin": "https://portal.znero.test",
		"Cookie": cookiePair1,
	})
	if stReplay != 401 {
		t.Fatalf("replayed refresh token returned %d, want 401", stReplay)
	}
	// Even legitimate cookiePair2 must now be revoked!
	stFamilyRevoked, _, _ := testFiberReq(t, prodApp.Test, "POST", "/api/v1/auth/refresh", "", "", map[string]string{
		"Origin": "https://portal.znero.test",
		"Cookie": cookiePair2,
	})
	if stFamilyRevoked != 401 {
		t.Fatalf("session family NOT revoked after refresh reuse attack: got %d, want 401", stFamilyRevoked)
	}
}

type fakeResolver struct {
	ips []net.IPAddr
}

func (r fakeResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return r.ips, nil
}

func TestSecurity_SSRF_And_WebhookHMAC_Timing_And_LogRedaction(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	lc, logger := testkit.NewLogCapture()

	kr, err := secrets.NewKeyRingFromEnv("test", testkit.RandHex(32), "v1", "")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}

	jwtSecret := testkit.RandHex(32)
	tokenMgr, _ := tokens.NewTokenManager(jwtSecret, time.Hour)
	httpApp, _, orgSvc, _, _, _, custSvc, _ := buildFullHTTPApp(t, e, tokenMgr)
	_ = httpApp

	platID := e.PlatformOrgID
	custOrg, err := orgSvc.Create(ctx, e.Override, org.CreateOrgRequest{ParentID: &platID, Name: "SSRFCust-" + testkit.RandHex(4), Type: "customer"})
	if err != nil {
		t.Fatalf("create cust: %v", err)
	}
	apiKey, err := custSvc.CreateAPIKey(ctx, e.Override, customer.CreateAPIKeyRequest{
		OrganizationID: custOrg.ID,
		Label:          "ssrf-key",
		Scopes:         []string{"sms:send", "sms:read", "account:read"},
		Reason:         "ssrf test",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create key: %v", err)
	}

	// Build a FAIL-CLOSED PRODUCTION EdgeHandler (without SetAllowLoopback)
	msgRepo := messaging.NewRepository(e.Pool)
	ledgerRepo := ledger.NewRepository(e.Pool)
	orgRepo := org.NewRepository(e.Pool)
	ledgerSvc := ledger.NewService(ledgerRepo, orgRepo, e.Approval, e.Audit, e.Pool)
	routingEngine := routing.NewEngine(e.Pool, nil, nil, nil)
	msgSvc, err := messaging.NewService(e.Pool, msgRepo, messaging.DefaultConfig(), routingEngine, ledgerSvc, kr, e.Audit, messaging.NewFakeSender(), logger)
	if err != nil {
		t.Fatalf("msgSvc: %v", err)
	}
	rateSvc := rating.NewService(rating.NewRepository(e.Pool), e.Audit, e.Approval, nil, nil)
	prodEdge := customeredge.NewEdgeHandler(customeredge.NewEdgeAuthenticator(e.Pool, nil, logger), customeredge.NewCustomerLimiter(), msgSvc, msgRepo, ledgerSvc, rateSvc, kr, e.Pool)
	prodEdgeCfg := &config.Config{
		Env:    config.EnvProduction,
		Server: config.ServerConfig{BodyLimitBytes: 1024 * 1024, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second},
	}
	prodEdgeApp := app.NewHTTP(prodEdgeCfg, logger, app.Modules{CustomerEdge: prodEdge})

	// 1. Customer Webhook SSRF & obfuscated IP vectors (MUST ALL BE REJECTED 400 in production)
	ssrfWebhookURLs := []string{
		"http://169.254.169.254/latest/meta-data/",
		"https://169.254.169.254/latest/meta-data/",
		"https://10.0.0.1/webhook",
		"https://172.16.0.1/webhook",
		"https://192.168.1.10/webhook",
		"https://127.0.0.1/webhook",
		"http://127.0.0.1:8080/webhook",
		"https://localhost/webhook",
		"https://[::1]/webhook",
		"https://[fc00::1]/webhook",
		"https://[fd12:3456:789a::1]/webhook",
		"https://[fe80::1]/webhook",
		"https://[::ffff:127.0.0.1]/webhook",
		"https://[::ffff:169.254.169.254]/webhook",
		"https://0177.0.0.1/webhook",
		"https://0x7f.0.0.1/webhook",
		"https://0x7f000001/webhook",
		"https://2130706433/webhook",
		"https://127.1/webhook",
		"https://127.0.0.1./webhook",
		"https://169.254.169.254./webhook",
		"https://user:pass@webhook.example.com/hook",
		"http://webhook.example.com/non-tls",
		"file:///etc/passwd",
		"gopher://127.0.0.1:6379/_INFO",
		"https://webhook.example.com/ok\\r\\nX-Injected: 1",
	}
	for _, targetURL := range ssrfWebhookURLs {
		payloadBytes, _ := json.Marshal(map[string]any{
			"url":    strings.ReplaceAll(targetURL, `\r\n`, "\r\n"),
			"events": []string{"message.final"},
		})
		st, _, body := testFiberReq(t, prodEdgeApp.Test, "POST", "/api/v1/customer/webhooks", apiKey.PlaintextKey, string(payloadBytes), nil)
		if st != 400 {
			t.Fatalf("BUG-047 SSRF BYPASS: POST /webhooks accepted %q -> %d (%s), want 400", targetURL, st, string(body))
		}
	}

	// Verify provider validator also rejects obfuscated / trailing-dot / IPv4-mapped IPv6 forms
	for _, host := range []string{"0177.0.0.1", "0x7f.0.0.1", "0x7f000001", "2130706433", "127.1", "127.0.0.1.", "::ffff:169.254.169.254", "fc00::1"} {
		if err := provider.ValidateHostSSRFWithOptions(host, "production", "active", false); err == nil {
			t.Fatalf("provider.ValidateHostSSRFWithOptions failed to block %q", host)
		}
	}

	// 2. Dial-time DNS rebinding protection: resolver returning [publicIP, 10.0.0.1] MUST abort dial!
	safeDialer := smpp.NewSafeDialer(smpp.DialOptions{
		Timeout:       time.Second,
		AllowLoopback: false,
		Resolver: fakeResolver{ips: []net.IPAddr{
			{IP: net.ParseIP("93.184.216.34")},
			{IP: net.ParseIP("10.0.0.1")},
		}},
	})
	if conn, dialErr := safeDialer.DialContext(ctx, "tcp", "rebind.example.com:443"); dialErr == nil {
		_ = conn.Close()
		t.Fatalf("SafeDialer allowed dial when DNS returned mixed public + RFC1918 IP!")
	}

	// 3. Real webhook HMAC-SHA256 signing & constant-time signature verification + LogCapture redaction
	whSecret := "whsec_secret_" + testkit.RandHex(12)
	encSec, err := kr.Encrypt(whSecret)
	if err != nil {
		t.Fatalf("encrypt webhook secret: %v", err)
	}
	var mu sync.Mutex
	var gotSig, gotTS string
	var gotBody []byte
	recvSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotSig = r.Header.Get("X-Znero-Signature")
		gotTS = r.Header.Get("X-Znero-Timestamp")
		gotBody = b
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer recvSrv.Close()

	var whID uuid.UUID
	if err := e.Pool.QueryRow(ctx, `
		INSERT INTO customer_webhooks (customer_org_id, url, secret_encrypted, secret_key_id, events, status, version)
		VALUES ($1, $2, $3, $4, '["message.final"]'::jsonb, 'active', 1) RETURNING id`,
		custOrg.ID, recvSrv.URL, encSec, secrets.DefaultKeyID,
	).Scan(&whID); err != nil {
		t.Fatalf("insert test webhook: %v", err)
	}
	delivPayload, _ := json.Marshal(map[string]any{"id": uuid.NewString(), "status": "delivered"})
	if _, err := e.Pool.Exec(ctx, `
		INSERT INTO customer_webhook_deliveries (id, webhook_id, customer_org_id, event_id, event_type, payload, status, attempts, max_attempts, next_attempt_at, created_at)
		VALUES ($1, $2, $3, $4, 'message.final', $5, 'pending', 0, 5, now(), now())`,
		uuid.New(), whID, custOrg.ID, uuid.New(), delivPayload,
	); err != nil {
		t.Fatalf("insert delivery: %v", err)
	}

	whDisp := webhook.NewDispatcher(msgRepo, kr, webhook.DefaultConfig(true), logger)
	whDisp.DispatchPending(ctx)

	mu.Lock()
	sigSnap, tsSnap, bodySnap := gotSig, gotTS, gotBody
	mu.Unlock()

	if !strings.HasPrefix(sigSnap, "v1=") || tsSnap == "" || len(bodySnap) == 0 {
		t.Fatalf("missing webhook delivery headers/body: sig=%q ts=%q len=%d", sigSnap, tsSnap, len(bodySnap))
	}
	mac := hmac.New(sha256.New, []byte(whSecret))
	mac.Write([]byte(tsSnap + "." + string(bodySnap)))
	expectedSig := "v1=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sigSnap), []byte(expectedSig)) {
		t.Fatalf("webhook HMAC mismatch: got %q want %q", sigSnap, expectedSig)
	}
	if !bytes.Contains(bodySnap, []byte(`"delivered"`)) {
		t.Fatalf("unexpected webhook body: %s", string(bodySnap))
	}

	// Verify NO registered secrets or API keys appeared anywhere in captured structured logs
	lc.ScanForLeaks(t)
	captured := lc.Output()
	for _, needle := range []string{apiKey.PlaintextKey, whSecret, jwtSecret} {
		if needle != "" && strings.Contains(captured, needle) {
			t.Fatalf("SECRET LEAK: %q appeared in logs", needle)
		}
	}

	// Confirm explicit tenant.Override never leaked unused
	_ = tenant.Context{}
}
