package search_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/internal/search"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/pkg/httpx"
	"github.com/znero-international/znero/backend/pkg/tenant"
	"github.com/znero-international/znero/backend/pkg/tokens"
)

func TestGlobalSearch_TenantIsolation(t *testing.T) {
	dbURL := testkit.RequireIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	var platID uuid.UUID
	err = pool.QueryRow(ctx, `SELECT id FROM organizations WHERE type = 'platform' LIMIT 1`).Scan(&platID)
	if err != nil {
		t.Fatalf("fetch platform org: %v", err)
	}

	orgAID := uuid.New()
	userAID := uuid.New()
	orgBID := uuid.New()
	userBID := uuid.New()

	pathA := "/" + platID.String() + "/" + orgAID.String() + "/"
	pathB := "/" + platID.String() + "/" + orgBID.String() + "/"

	_, err = pool.Exec(ctx, `
		INSERT INTO organizations (id, name, type, parent_id, path, status)
		VALUES ($1, 'Search Test Org A', 'customer', $2, $3, 'active'),
		       ($4, 'Search Test Org B', 'customer', $2, $5, 'active')
		ON CONFLICT DO NOTHING;
	`, orgAID, platID, pathA, orgBID, pathB)
	if err != nil {
		t.Fatalf("seed orgs: %v", err)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO users (id, organization_id, email, password_hash, status)
		VALUES ($1, $2, 'search_user_a_' || substr(md5(random()::text), 1, 6) || '@example.com', 'hash', 'active'),
		       ($3, $4, 'search_user_b_' || substr(md5(random()::text), 1, 6) || '@example.com', 'hash', 'active')
		ON CONFLICT DO NOTHING;
	`, userAID, orgAID, userBID, orgBID)
	if err != nil {
		t.Fatalf("seed users: %v", err)
	}

	// Seed Message for Org A
	msgAID := uuid.New()
	recipientA := "+12025550199"
	_, err = pool.Exec(ctx, `
		INSERT INTO messages (
			id, created_at, customer_org_id, original_destination, destination,
			sender_id, encrypted_body, key_id, status, sell_price, sell_currency,
			estimated_buy_cost, actual_buy_cost, buy_currency, expires_at
		) VALUES (
			$1, now(), $2, $3, $3,
			'INFO', 'testbody'::bytea, 'k1', 'delivered', 0.05, 'USD',
			0.02, 0.02, 'USD', now() + interval '24 hours'
		)
		ON CONFLICT DO NOTHING;
	`, msgAID, orgAID, recipientA)
	if err != nil {
		t.Fatalf("seed msg A: %v", err)
	}

	// Seed Provider (Platform only!)
	provID := uuid.New()
	provCode := "SEARCH_PROV_" + testkit.RandHex(4)
	provName := "Alpha Global Carrier " + testkit.RandHex(2)
	_, err = pool.Exec(ctx, `
		INSERT INTO providers (id, name, code, provider_type, status)
		VALUES ($1, $2, $3, 'http', 'active')
		ON CONFLICT DO NOTHING;
	`, provID, provName, provCode)
	if err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	// Seed Incident (Platform only!)
	incID := uuid.New()
	incTitle := "Core Routing Degradation " + testkit.RandHex(3)
	_, err = pool.Exec(ctx, `
		INSERT INTO incidents (id, title, severity, status, created_at)
		VALUES ($1, $2, 'major', 'open', now())
		ON CONFLICT DO NOTHING;
	`, incID, incTitle)
	if err != nil {
		t.Fatalf("seed incident: %v", err)
	}

	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		_, _ = pool.Exec(cctx, `DELETE FROM messages WHERE id = $1`, msgAID)
		_, _ = pool.Exec(cctx, `DELETE FROM providers WHERE id = $1`, provID)
		_, _ = pool.Exec(cctx, `DELETE FROM incidents WHERE id = $1`, incID)
	})

	repo := search.NewRepository(pool, nil)
	svc := search.NewService(repo, nil)

	tcPlatform := &tenant.Context{
		ActorUserID:     userAID,
		ActorOrgID:      platID,
		ActorOrgType:    "platform",
		IsPlatformAdmin: true,
	}

	tcCustA := &tenant.Context{
		ActorUserID:     userAID,
		ActorOrgID:      orgAID,
		ActorOrgType:    "customer",
		IsPlatformAdmin: false,
	}

	tcCustB := &tenant.Context{
		ActorUserID:     userBID,
		ActorOrgID:      orgBID,
		ActorOrgType:    "customer",
		IsPlatformAdmin: false,
	}

	// 1. Search for Provider Name
	resPlat, err := svc.Search(ctx, tcPlatform, provCode)
	if err != nil {
		t.Fatalf("platform search: %v", err)
	}
	if len(resPlat.Results) == 0 {
		t.Errorf("expected platform admin to find provider %s", provCode)
	}

	resCustA, err := svc.Search(ctx, tcCustA, provCode)
	if err != nil {
		t.Fatalf("customer search: %v", err)
	}
	if len(resCustA.Results) != 0 {
		t.Errorf("TENANT LEAK: customer discovered platform provider %s! Got: %+v", provCode, resCustA.Results)
	}

	// 2. Search for Message Recipient
	resMsgA, err := svc.Search(ctx, tcCustA, recipientA)
	if err != nil {
		t.Fatalf("cust A msg search: %v", err)
	}
	if len(resMsgA.Results) == 0 {
		t.Errorf("expected Customer A to find their own message %s", recipientA)
	}

	resMsgB, err := svc.Search(ctx, tcCustB, recipientA)
	if err != nil {
		t.Fatalf("cust B msg search: %v", err)
	}
	if len(resMsgB.Results) != 0 {
		t.Errorf("TENANT LEAK: Customer B saw Customer A's message! Got: %+v", resMsgB.Results)
	}

	// 3. Search for Incident
	resIncPlat, _ := svc.Search(ctx, tcPlatform, "Routing Degradation")
	if len(resIncPlat.Results) == 0 {
		t.Errorf("expected platform admin to find incident")
	}

	resIncCust, _ := svc.Search(ctx, tcCustA, "Routing Degradation")
	if len(resIncCust.Results) != 0 {
		t.Errorf("TENANT LEAK: Customer discovered platform incident! Got: %+v", resIncCust.Results)
	}
}

func TestSavedFilters_CRUDAndDefaults(t *testing.T) {
	dbURL := testkit.RequireIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	var realUserID uuid.UUID
	err = pool.QueryRow(ctx, `SELECT id FROM users LIMIT 1`).Scan(&realUserID)
	if err != nil {
		t.Fatalf("fetch seeded user: %v", err)
	}

	repo := search.NewRepository(pool, nil)
	svc := search.NewService(repo, nil)

	tc := &tenant.Context{
		ActorUserID:  realUserID,
		ActorOrgType: "customer",
	}

	// 1. Create Filter 1
	f1, err := svc.CreateSavedFilter(ctx, tc, search.CreateSavedFilterRequest{
		ViewName:  "messages",
		Name:      "Failed Messages in US",
		Query:     map[string]interface{}{"status": "failed", "country": "US"},
		IsDefault: false,
	})
	if err != nil {
		t.Fatalf("create f1: %v", err)
	}

	// 2. Create Filter 2 (Default)
	f2, err := svc.CreateSavedFilter(ctx, tc, search.CreateSavedFilterRequest{
		ViewName:  "messages",
		Name:      "Pending Queue",
		Query:     map[string]interface{}{"status": "pending"},
		IsDefault: true,
	})
	if err != nil {
		t.Fatalf("create f2: %v", err)
	}

	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		_ = svc.DeleteSavedFilter(cctx, tc, f1.ID)
		_ = svc.DeleteSavedFilter(cctx, tc, f2.ID)
	})

	// 3. List saved filters for view "messages"
	list, err := svc.ListSavedFilters(ctx, tc, "messages")
	if err != nil {
		t.Fatalf("list filters: %v", err)
	}
	if len(list) < 2 {
		t.Errorf("expected at least 2 filters, got %d", len(list))
	}
	// First should be the default one
	if !list[0].IsDefault {
		t.Errorf("expected first filter to be default")
	}

	// 4. Set f1 as default
	err = svc.SetDefaultFilter(ctx, tc, f1.ID)
	if err != nil {
		t.Fatalf("set default f1: %v", err)
	}

	listUpdated, _ := svc.ListSavedFilters(ctx, tc, "messages")
	for _, f := range listUpdated {
		if f.ID == f1.ID && !f.IsDefault {
			t.Errorf("expected f1 to be default")
		}
		if f.ID == f2.ID && f.IsDefault {
			t.Errorf("expected f2 to no longer be default")
		}
	}

	// 5. Delete f2
	err = svc.DeleteSavedFilter(ctx, tc, f2.ID)
	if err != nil {
		t.Fatalf("delete f2: %v", err)
	}
}

func TestUserPreferences_LocalesAndThemes(t *testing.T) {
	dbURL := testkit.RequireIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	var realUserID uuid.UUID
	err = pool.QueryRow(ctx, `SELECT id FROM users LIMIT 1`).Scan(&realUserID)
	if err != nil {
		t.Fatalf("fetch seeded user: %v", err)
	}

	repo := search.NewRepository(pool, nil)
	svc := search.NewService(repo, nil)

	tc := &tenant.Context{
		ActorUserID:  realUserID,
		ActorOrgType: "customer",
	}

	// 1. Initial preferences
	initPref, err := svc.GetUserPreferences(ctx, tc)
	if err != nil {
		t.Fatalf("get init pref: %v", err)
	}
	if initPref.Theme == "" || initPref.Locale == "" {
		t.Errorf("expected default theme and locale")
	}

	// 2. Update to Bengali (bn) locale and dark theme
	theme := "dark"
	locale := "bn"
	tz := "Asia/Dhaka"
	layout := map[string]interface{}{"widgets": []string{"volume", "success_rate"}}

	updated, err := svc.UpdateUserPreferences(ctx, tc, search.UpdatePreferenceRequest{
		Theme:           &theme,
		Locale:          &locale,
		Timezone:        &tz,
		DashboardLayout: &layout,
	})
	if err != nil {
		t.Fatalf("update pref: %v", err)
	}
	if updated.Theme != "dark" || updated.Locale != "bn" || updated.Timezone != "Asia/Dhaka" {
		t.Errorf("updated preference mismatch: %+v", updated)
	}

	// 3. Validation: invalid locale (e.g. "fr") must be rejected
	invalidLocale := "fr"
	_, err = svc.UpdateUserPreferences(ctx, tc, search.UpdatePreferenceRequest{
		Locale: &invalidLocale,
	})
	if err == nil {
		t.Errorf("expected error updating to unsupported locale 'fr', got nil")
	}

	// 4. Validation: invalid theme (e.g. "neon") must be rejected
	invalidTheme := "neon"
	_, err = svc.UpdateUserPreferences(ctx, tc, search.UpdatePreferenceRequest{
		Theme: &invalidTheme,
	})
	if err == nil {
		t.Errorf("expected error updating to unsupported theme 'neon', got nil")
	}
}

func TestSearchAndFilters_HTTPRoutes(t *testing.T) {
	dbURL := testkit.RequireIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	tokenMgr, err := tokens.NewTokenManager(testkit.RandHex(32), 15*time.Minute)
	if err != nil {
		t.Fatalf("token manager: %v", err)
	}

	repo := search.NewRepository(pool, nil)
	svc := search.NewService(repo, nil)
	handler := search.NewHandler(svc)

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		ErrorHandler:          httpx.ErrorHandler(slog.Default()),
	})

	authMiddleware := func(c *fiber.Ctx) error {
		authHdr := c.Get("Authorization")
		if authHdr == "" {
			return httpx.Unauthorized("Missing authorization header")
		}
		var tokenStr string
		if len(authHdr) > 7 && authHdr[:7] == "Bearer " {
			tokenStr = authHdr[7:]
		} else {
			tokenStr = authHdr
		}
		claims, err := tokenMgr.ValidateAccessToken(tokenStr)
		if err != nil {
			return httpx.Unauthorized("Invalid token")
		}
		perms := make(map[string]bool, len(claims.Permissions))
		for _, p := range claims.Permissions {
			perms[p] = true
		}
		tc := &tenant.Context{
			ActorUserID:     claims.UserID,
			ActorOrgID:      claims.OrgID,
			ActorOrgType:    claims.OrgType,
			ActorOrgPath:    claims.OrgPath,
			IsPlatformAdmin: claims.IsPlatformAdmin,
			Permissions:     perms,
		}
		c.SetUserContext(tenant.WithContext(c.UserContext(), tc))
		return c.Next()
	}

	handler.Register(app.Group("/api/v1"), authMiddleware)

	var realUserID uuid.UUID
	_ = pool.QueryRow(ctx, `SELECT id FROM users LIMIT 1`).Scan(&realUserID)
	token, _ := tokenMgr.GenerateAccessToken(realUserID, uuid.New(), uuid.New(), "customer", "/c/", false, []string{"analytics.dashboard.view"})

	// 1. GET /api/v1/search?q=test -> 200 OK
	reqSearch := httptest.NewRequest("GET", "/api/v1/search?q=test", nil)
	reqSearch.Header.Set("Authorization", "Bearer "+token)
	respSearch, _ := app.Test(reqSearch)
	if respSearch.StatusCode != http.StatusOK {
		t.Errorf("search failed with status %d", respSearch.StatusCode)
	}

	// 2. GET /api/v1/preferences -> 200 OK
	reqPref := httptest.NewRequest("GET", "/api/v1/preferences", nil)
	reqPref.Header.Set("Authorization", "Bearer "+token)
	respPref, _ := app.Test(reqPref)
	if respPref.StatusCode != http.StatusOK {
		t.Errorf("get preferences failed with status %d", respPref.StatusCode)
	}

	// 3. PUT /api/v1/preferences -> 200 OK
	prefBody := []byte(`{"theme": "light", "locale": "en"}`)
	reqPutPref := httptest.NewRequest("PUT", "/api/v1/preferences", bytes.NewReader(prefBody))
	reqPutPref.Header.Set("Authorization", "Bearer "+token)
	reqPutPref.Header.Set("Content-Type", "application/json")
	respPutPref, _ := app.Test(reqPutPref)
	if respPutPref.StatusCode != http.StatusOK {
		t.Errorf("update preferences failed with status %d", respPutPref.StatusCode)
	}

	// 4. POST /api/v1/saved-filters -> 200 OK
	filterBody := []byte(`{"view_name": "delivery_reports", "name": "HTTP Test Filter", "query": {"country": "US"}}`)
	reqFilter := httptest.NewRequest("POST", "/api/v1/saved-filters", bytes.NewReader(filterBody))
	reqFilter.Header.Set("Authorization", "Bearer "+token)
	reqFilter.Header.Set("Content-Type", "application/json")
	respFilter, _ := app.Test(reqFilter)
	if respFilter.StatusCode != http.StatusOK {
		t.Errorf("create saved filter failed with status %d", respFilter.StatusCode)
	}
}
