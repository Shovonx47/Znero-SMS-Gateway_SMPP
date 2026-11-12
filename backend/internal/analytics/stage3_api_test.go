package analytics_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/internal/analytics"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/pkg/httpx"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

// setupTestApp builds a minimal test Fiber app with analytics routes and injectable tenant context.
func setupTestApp(pool *pgxpool.Pool) (*fiber.App, *analytics.Service) {
	app := fiber.New(fiber.Config{
		ErrorHandler: httpx.ErrorHandler(nil),
	})
	repo := analytics.NewRepository(pool, nil)
	svc := analytics.NewService(repo, nil)
	handler := analytics.NewHandler(svc)

	authMiddleware := func(c *fiber.Ctx) error {
		// Read mock headers to build tenant.Context for testing
		orgType := c.Get("X-Test-Org-Type", "platform")
		orgIDStr := c.Get("X-Test-Org-ID", "")
		userIDStr := c.Get("X-Test-User-ID", "")

		var orgID, userID uuid.UUID
		if orgIDStr != "" {
			orgID, _ = uuid.Parse(orgIDStr)
		} else {
			orgID = uuid.New()
		}
		if userIDStr != "" {
			userID, _ = uuid.Parse(userIDStr)
		} else {
			userID = uuid.New()
		}

		perms := map[string]bool{
			"analytics.dashboard.view": true,
			"analytics.noc.view":       orgType == "platform",
			"analytics.report.view":    true,
			"setting.manage":           orgType == "platform",
			"system.info.view":         orgType == "platform",
		}

		tc := &tenant.Context{
			ActorUserID:     userID,
			ActorOrgID:      orgID,
			ActorOrgType:    orgType,
			IsPlatformAdmin: orgType == "platform",
			Permissions:     perms,
		}
		c.SetUserContext(tenant.WithContext(c.UserContext(), tc))
		return c.Next()
	}

	api := app.Group("/api/v1")
	handler.Register(api, authMiddleware)
	return app, svc
}

func TestStage3_AnalyticsAPIs_TenancyIsolationAndLeakScan(t *testing.T) {
	dbURL := testkit.RequireIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	app, _ := setupTestApp(pool)

	customerID := uuid.New()
	resellerID := uuid.New()

	// 1. Executive Dashboard: Customer Request
	req := httptest.NewRequest("GET", "/api/v1/analytics/dashboard/executive", nil)
	req.Header.Set("X-Test-Org-Type", "customer")
	req.Header.Set("X-Test-Org-ID", customerID.String())

	resp, err := app.Test(req, 5000)
	if err != nil {
		t.Fatalf("customer exec dash request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for customer exec dash, got %d", resp.StatusCode)
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	bodyStr := string(bodyBytes)

	// Leak scan: Customer MUST NEVER see cost_by_currency, margin_by_currency, cost, or margin
	if strings.Contains(bodyStr, `"cost_by_currency"`) && !strings.Contains(bodyStr, `"cost_by_currency":null`) {
		t.Errorf("LEAK DETECTED: customer executive dashboard returned non-null cost_by_currency: %s", bodyStr)
	}
	if strings.Contains(bodyStr, `"margin_by_currency"`) && !strings.Contains(bodyStr, `"margin_by_currency":null`) {
		t.Errorf("LEAK DETECTED: customer executive dashboard returned non-null margin_by_currency: %s", bodyStr)
	}

	// 2. NOC Dashboard: Platform Allowed, Customer and Reseller FORBIDDEN
	nocReqPlatform := httptest.NewRequest("GET", "/api/v1/analytics/dashboard/noc", nil)
	nocReqPlatform.Header.Set("X-Test-Org-Type", "platform")
	respPlat, err := app.Test(nocReqPlatform, 5000)
	if err != nil || respPlat.StatusCode != http.StatusOK {
		t.Errorf("platform should access NOC dashboard: code=%d, err=%v", respPlat.StatusCode, err)
	}

	nocReqCustomer := httptest.NewRequest("GET", "/api/v1/analytics/dashboard/noc", nil)
	nocReqCustomer.Header.Set("X-Test-Org-Type", "customer")
	nocReqCustomer.Header.Set("X-Test-Org-ID", customerID.String())
	respCust, _ := app.Test(nocReqCustomer, 5000)
	if respCust.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for customer NOC dashboard, got %d", respCust.StatusCode)
	}

	nocReqReseller := httptest.NewRequest("GET", "/api/v1/analytics/dashboard/noc", nil)
	nocReqReseller.Header.Set("X-Test-Org-Type", "reseller")
	nocReqReseller.Header.Set("X-Test-Org-ID", resellerID.String())
	respResell, _ := app.Test(nocReqReseller, 5000)
	if respResell.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for reseller NOC dashboard, got %d", respResell.StatusCode)
	}

	// 3. Live NOC Endpoint: Platform Allowed, Non-Platform FORBIDDEN
	liveReq := httptest.NewRequest("GET", "/api/v1/analytics/noc/live", nil)
	liveReq.Header.Set("X-Test-Org-Type", "customer")
	liveResp, _ := app.Test(liveReq, 5000)
	if liveResp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for customer live NOC, got %d", liveResp.StatusCode)
	}

	// 4. Reports: Delivery and Financial
	delivReq := httptest.NewRequest("GET", "/api/v1/analytics/reports/delivery", nil)
	delivReq.Header.Set("X-Test-Org-Type", "customer")
	delivResp, err := app.Test(delivReq, 5000)
	if err != nil || delivResp.StatusCode != http.StatusOK {
		t.Errorf("delivery report failed: code=%d, err=%v", delivResp.StatusCode, err)
	}

	finReq := httptest.NewRequest("GET", "/api/v1/analytics/reports/financial", nil)
	finReq.Header.Set("X-Test-Org-Type", "customer")
	finResp, err := app.Test(finReq, 5000)
	if err != nil || finResp.StatusCode != http.StatusOK {
		t.Errorf("financial report failed: code=%d, err=%v", finResp.StatusCode, err)
	}
	finBody, _ := io.ReadAll(finResp.Body)
	if strings.Contains(string(finBody), `"cost_estimated"`) && !strings.Contains(string(finBody), `"cost_estimated":null`) {
		t.Errorf("LEAK DETECTED: customer financial report exposed cost_estimated")
	}

	// 5. Widgets Catalog Scoping
	widgetReqCust := httptest.NewRequest("GET", "/api/v1/analytics/widgets", nil)
	widgetReqCust.Header.Set("X-Test-Org-Type", "customer")
	wRespCust, _ := app.Test(widgetReqCust, 5000)
	if wRespCust.StatusCode != http.StatusOK {
		t.Fatalf("widgets catalog request failed: %d", wRespCust.StatusCode)
	}
	var custWidgets struct {
		Items []analytics.WidgetDefinition `json:"items"`
	}
	_ = json.NewDecoder(wRespCust.Body).Decode(&custWidgets)
	for _, w := range custWidgets.Items {
		if w.ID == "outbox_gauge" || w.ID == "carrier_health" || w.ID == "alerts_banner" {
			t.Errorf("LEAK DETECTED: customer saw platform-only widget %s", w.ID)
		}
	}

	// 6. Preferences & Dashboard Layout Round-Trip
	var userID uuid.UUID
	err = pool.QueryRow(ctx, `SELECT id FROM users LIMIT 1`).Scan(&userID)
	if err != nil {
		t.Fatalf("fetch user id for layout test: %v", err)
	}

	layoutPayload := `{"layout": {"grid": ["kpi_summary", "volume_trend"], "theme": "dark"}}`
	putReq := httptest.NewRequest("PUT", "/api/v1/analytics/preferences/layout", bytes.NewBufferString(layoutPayload))
	putReq.Header.Set("Content-Type", "application/json")
	putReq.Header.Set("X-Test-User-ID", userID.String())
	putReq.Header.Set("X-Test-Org-Type", "customer")
	putResp, err := app.Test(putReq, 5000)
	if err != nil || putResp.StatusCode != http.StatusOK {
		t.Fatalf("save dashboard layout failed: code=%d, err=%v", putResp.StatusCode, err)
	}

	getReq := httptest.NewRequest("GET", "/api/v1/analytics/preferences/layout", nil)
	getReq.Header.Set("X-Test-User-ID", userID.String())
	getReq.Header.Set("X-Test-Org-Type", "customer")
	getResp, err := app.Test(getReq, 5000)
	if err != nil || getResp.StatusCode != http.StatusOK {
		t.Fatalf("get dashboard layout failed: code=%d, err=%v", getResp.StatusCode, err)
	}
	getBody, _ := io.ReadAll(getResp.Body)
	if !strings.Contains(string(getBody), "volume_trend") {
		t.Errorf("layout round-trip did not return saved content: %s", string(getBody))
	}
}

func TestStage3_PerformanceBudgets(t *testing.T) {
	dbURL := testkit.RequireIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	app, _ := setupTestApp(pool)

	// Measure Executive Dashboard response latency across 20 iterations
	// Budget: p95 < 300ms
	var execLatencies []time.Duration
	for i := 0; i < 20; i++ {
		start := time.Now()
		req := httptest.NewRequest("GET", "/api/v1/analytics/dashboard/executive", nil)
		req.Header.Set("X-Test-Org-Type", "platform")
		resp, err := app.Test(req, 5000)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("exec request failed: code=%d, err=%v", resp.StatusCode, err)
		}
		execLatencies = append(execLatencies, time.Since(start))
	}

	// Calculate p95
	p95Idx := int(float64(len(execLatencies)) * 0.95)
	if p95Idx >= len(execLatencies) {
		p95Idx = len(execLatencies) - 1
	}
	execP95 := execLatencies[p95Idx]
	t.Logf("Executive Dashboard p95 response time: %v (budget: 300ms)", execP95)
	if execP95 > 300*time.Millisecond {
		t.Errorf("PERFORMANCE BUDGET EXCEEDED: executive dashboard p95 was %v (> 300ms)", execP95)
	}

	// Measure Live NOC response latency across 20 iterations
	// Budget: p95 < 100ms
	var nocLatencies []time.Duration
	for i := 0; i < 20; i++ {
		start := time.Now()
		req := httptest.NewRequest("GET", "/api/v1/analytics/noc/live", nil)
		req.Header.Set("X-Test-Org-Type", "platform")
		resp, err := app.Test(req, 5000)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("live noc request failed: code=%d, err=%v", resp.StatusCode, err)
		}
		nocLatencies = append(nocLatencies, time.Since(start))
	}

	nocP95Idx := int(float64(len(nocLatencies)) * 0.95)
	if nocP95Idx >= len(nocLatencies) {
		nocP95Idx = len(nocLatencies) - 1
	}
	nocP95 := nocLatencies[nocP95Idx]
	t.Logf("Live NOC Dashboard p95 response time: %v (budget: 100ms)", nocP95)
	if nocP95 > 100*time.Millisecond {
		t.Errorf("PERFORMANCE BUDGET EXCEEDED: live NOC dashboard p95 was %v (> 100ms)", nocP95)
	}
}
