package alerting_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/internal/alerting"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/pkg/httpx"
	"github.com/znero-international/znero/backend/pkg/tenant"
	"github.com/znero-international/znero/backend/pkg/tokens"
)

func TestAlertRuleLifecycleAndEvaluation(t *testing.T) {
	dbURL := testkit.RequireIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	repo := alerting.NewRepository(pool, nil)
	metricMock := alerting.NewMockMetricSource()
	evaluator := alerting.NewEvaluator(metricMock)
	svc := alerting.NewService(repo, evaluator, nil, nil)

	tc := &tenant.Context{
		ActorUserID:     uuid.New(),
		ActorOrgType:    "platform",
		IsPlatformAdmin: true,
	}

	// 1. Create alert rule: high failure rate > 10%
	rule, err := svc.CreateRule(ctx, tc, alerting.CreateRuleRequest{
		Name:            "Test High Failure Rate",
		ConditionType:   alerting.ConditionHighFailureRate,
		Threshold:       10.0,
		DurationSeconds: 60,
		Severity:        alerting.SeverityCritical,
		Scope:           "platform",
		Enabled:         true,
	})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}

	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		_, _ = pool.Exec(cctx, `DELETE FROM alerts WHERE rule_id = $1`, rule.ID)
		_ = svc.DeleteRule(cctx, tc, rule.ID)
	})

	// 2. Normal conditions (failure rate = 3.2%)
	metricMock.SetMetric(alerting.ConditionHighFailureRate, 3.2)
	metricMock.SetMetric(alerting.ConditionCarrierConnDown, 1.0)
	alerts, err := svc.EvaluateRules(ctx)
	if err != nil {
		t.Fatalf("evaluate rules normal: %v", err)
	}
	for _, a := range alerts {
		if a.RuleID != nil && *a.RuleID == rule.ID {
			t.Errorf("did not expect alert for rule %s under normal conditions", rule.ID)
		}
	}

	// 3. Spike above threshold (failure rate = 18.5% > 10.0%)
	metricMock.SetMetric(alerting.ConditionHighFailureRate, 18.5)
	metricMock.SetMetric(alerting.ConditionCarrierConnDown, 1.0) // Healthy
	alerts, err = svc.EvaluateRules(ctx)
	if err != nil {
		t.Fatalf("evaluate rules breached: %v", err)
	}

	var firingAlert *alerting.Alert
	for _, a := range alerts {
		if a.RuleID != nil && *a.RuleID == rule.ID {
			firingAlert = a
			break
		}
	}
	if firingAlert == nil {
		t.Fatalf("expected firing alert for rule %s", rule.ID)
	}
	if firingAlert.Status != alerting.AlertStatusFiring {
		t.Errorf("expected status firing, got %s", firingAlert.Status)
	}
	if firingAlert.Severity != alerting.SeverityCritical {
		t.Errorf("expected severity critical, got %s", firingAlert.Severity)
	}

	// 4. Repeated evaluation while still breaching should NOT duplicate the firing alert
	alertsRepeat, err := svc.EvaluateRules(ctx)
	if err != nil {
		t.Fatalf("evaluate rules repeat: %v", err)
	}
	for _, a := range alertsRepeat {
		if a.RuleID != nil && *a.RuleID == rule.ID {
			t.Errorf("expected no duplicate alert for rule %s on repeat evaluation", rule.ID)
		}
	}

	// 5. Silence alert for 1 hour
	err = svc.SilenceAlert(ctx, tc, firingAlert.ID, 1*time.Hour, "Under investigation by NOC team")
	if err != nil {
		t.Fatalf("silence alert: %v", err)
	}

	activeAlerts, err := svc.ListAlerts(ctx, tc, alerting.AlertStatusSilenced)
	if err != nil {
		t.Fatalf("list silenced alerts: %v", err)
	}
	if len(activeAlerts) == 0 {
		t.Fatalf("expected silenced alert to be present")
	}

	// 6. Recovery: failure rate drops back to 1.5% (< 10.0%)
	metricMock.SetMetric(alerting.ConditionHighFailureRate, 1.5)
	alertsResolved, err := svc.EvaluateRules(ctx)
	if err != nil {
		t.Fatalf("evaluate rules resolved: %v", err)
	}

	var resolvedAlert *alerting.Alert
	for _, a := range alertsResolved {
		if a.RuleID != nil && *a.RuleID == rule.ID {
			resolvedAlert = a
			break
		}
	}
	if resolvedAlert == nil {
		t.Fatalf("expected resolved alert for rule %s", rule.ID)
	}
	if resolvedAlert.Status != alerting.AlertStatusResolved {
		t.Errorf("expected status resolved, got %s", resolvedAlert.Status)
	}
	if resolvedAlert.ResolvedAt == nil {
		t.Errorf("expected non-nil ResolvedAt timestamp")
	}
}

func TestIncidentStateMachineAndTimeline(t *testing.T) {
	dbURL := testkit.RequireIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	repo := alerting.NewRepository(pool, nil)
	svc := alerting.NewService(repo, nil, nil, nil)

	var leadUserID *uuid.UUID
	var seededUID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM users LIMIT 1`).Scan(&seededUID); err == nil {
		leadUserID = &seededUID
	}

	actorID := uuid.Nil
	if leadUserID != nil {
		actorID = *leadUserID
	}

	tc := &tenant.Context{
		ActorUserID:     actorID,
		ActorOrgType:    "platform",
		IsPlatformAdmin: true,
	}

	// 1. Declare incident
	inc, err := svc.DeclareIncident(ctx, tc, alerting.DeclareIncidentRequest{
		Title:       "Upstream Provider Outage",
		Severity:    alerting.SeverityMajor,
		LeadUserID:  leadUserID,
		InitialNote: "Carrier A reports fiber cut outside Frankfurt.",
	})
	if err != nil {
		t.Fatalf("declare incident: %v", err)
	}
	if inc.Status != alerting.IncidentStatusOpen {
		t.Fatalf("expected status open, got %s", inc.Status)
	}

	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		_, _ = pool.Exec(cctx, `DELETE FROM incident_timeline WHERE incident_id = $1`, inc.ID)
		_, _ = pool.Exec(cctx, `DELETE FROM incidents WHERE id = $1`, inc.ID)
	})

	// 2. Invalid Transition: open -> resolved directly must fail
	err = svc.TransitionIncident(ctx, tc, inc.ID, alerting.IncidentStatusResolved, "Skipping triage", nil)
	if err == nil {
		t.Errorf("expected error transitioning directly from open to resolved, got nil")
	}

	// 3. Valid Lifecycle: open -> investigating -> identified -> monitoring -> resolved
	err = svc.TransitionIncident(ctx, tc, inc.ID, alerting.IncidentStatusInvestigating, "Assigned to telecom ops team", nil)
	if err != nil {
		t.Fatalf("transition investigating: %v", err)
	}

	err = svc.TransitionIncident(ctx, tc, inc.ID, alerting.IncidentStatusIdentified, "Fiber cut located near hub 4", nil)
	if err != nil {
		t.Fatalf("transition identified: %v", err)
	}

	err = svc.TransitionIncident(ctx, tc, inc.ID, alerting.IncidentStatusMonitoring, "Traffic rerouted via backup path", nil)
	if err != nil {
		t.Fatalf("transition monitoring: %v", err)
	}

	rootCause := "Physical fiber cut on primary transit route."
	err = svc.TransitionIncident(ctx, tc, inc.ID, alerting.IncidentStatusResolved, "All links restored and latency nominal", &rootCause)
	if err != nil {
		t.Fatalf("transition resolved: %v", err)
	}

	// 4. Verify detail and timeline
	detail, err := svc.GetIncident(ctx, tc, inc.ID)
	if err != nil {
		t.Fatalf("get incident detail: %v", err)
	}
	if detail.Status != alerting.IncidentStatusResolved {
		t.Errorf("expected final status resolved, got %s", detail.Status)
	}
	if detail.ResolvedAt == nil {
		t.Errorf("expected non-nil ResolvedAt timestamp")
	}
	if detail.RootCause == nil || *detail.RootCause != rootCause {
		t.Errorf("expected root cause %q, got %v", rootCause, detail.RootCause)
	}

	// Check timeline entries count (state changes + initial note)
	if len(detail.Timeline) < 5 {
		t.Errorf("expected at least 5 timeline entries, got %d", len(detail.Timeline))
	}
}

func TestAlertingAndIncidents_RBACAndTenantIsolation(t *testing.T) {
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

	repo := alerting.NewRepository(pool, nil)
	evaluator := alerting.NewEvaluator(alerting.NewMockMetricSource())
	svc := alerting.NewService(repo, evaluator, nil, nil)
	handler := alerting.NewHandler(svc)

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		ErrorHandler:          httpx.ErrorHandler(nil),
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

	// 1. Customer Token (Must receive 403 Forbidden for all alert and incident endpoints)
	custUser := uuid.New()
	custOrg := uuid.New()
	custToken, _ := tokenMgr.GenerateAccessToken(custUser, custOrg, custOrg, "customer", "/c/", false, []string{"analytics.dashboard.view"})

	reqAlertsCust := httptest.NewRequest("GET", "/api/v1/alerts", nil)
	reqAlertsCust.Header.Set("Authorization", "Bearer "+custToken)
	respAlertsCust, _ := app.Test(reqAlertsCust)
	if respAlertsCust.StatusCode != http.StatusForbidden {
		t.Errorf("TENANT LEAK: Customer accessed platform alerts! Status: %d (want 403)", respAlertsCust.StatusCode)
	}

	reqIncCust := httptest.NewRequest("GET", "/api/v1/incidents", nil)
	reqIncCust.Header.Set("Authorization", "Bearer "+custToken)
	respIncCust, _ := app.Test(reqIncCust)
	if respIncCust.StatusCode != http.StatusForbidden {
		t.Errorf("TENANT LEAK: Customer accessed platform incidents! Status: %d (want 403)", respIncCust.StatusCode)
	}

	// 2. Platform Admin Token (Must receive 200 OK)
	platUser := uuid.New()
	platOrg := uuid.New()
	platToken, _ := tokenMgr.GenerateAccessToken(platUser, platOrg, platOrg, "platform", "/p/", true, []string{"alert.view", "alert.manage", "incident.view", "incident.manage"})

	reqAlertsPlat := httptest.NewRequest("GET", "/api/v1/alerts", nil)
	reqAlertsPlat.Header.Set("Authorization", "Bearer "+platToken)
	respAlertsPlat, _ := app.Test(reqAlertsPlat)
	if respAlertsPlat.StatusCode != http.StatusOK {
		t.Errorf("Platform admin failed to access alerts! Status: %d (want 200)", respAlertsPlat.StatusCode)
	}

	// Declare incident over HTTP as platform admin
	incPayload := []byte(`{"title": "HTTP Platform Test Incident", "severity": "minor"}`)
	reqDeclInc := httptest.NewRequest("POST", "/api/v1/incidents", bytes.NewReader(incPayload))
	reqDeclInc.Header.Set("Authorization", "Bearer "+platToken)
	reqDeclInc.Header.Set("Content-Type", "application/json")
	respDeclInc, _ := app.Test(reqDeclInc)
	if respDeclInc.StatusCode != http.StatusCreated {
		t.Errorf("Platform admin failed to declare incident! Status: %d (want 201)", respDeclInc.StatusCode)
	}

	var incData struct {
		Data struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
	}
	_ = json.NewDecoder(respDeclInc.Body).Decode(&incData)

	t.Cleanup(func() {
		if incData.Data.ID != uuid.Nil {
			cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer ccancel()
			_, _ = pool.Exec(cctx, `DELETE FROM incident_timeline WHERE incident_id = $1`, incData.Data.ID)
			_, _ = pool.Exec(cctx, `DELETE FROM incidents WHERE id = $1`, incData.Data.ID)
		}
	})
}
