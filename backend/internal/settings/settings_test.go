package settings_test

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
	"github.com/znero-international/znero/backend/internal/settings"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/pkg/httpx"
	"github.com/znero-international/znero/backend/pkg/tenant"
	"github.com/znero-international/znero/backend/pkg/tokens"
)

func TestSettings_SchemaValidation(t *testing.T) {
	// Integer tests
	if _, err := settings.ValidateAndCoerce("not_a_number", settings.TypeInteger); err == nil {
		t.Errorf("expected error parsing string as integer")
	}
	if _, err := settings.ValidateAndCoerce(12.34, settings.TypeInteger); err == nil {
		t.Errorf("expected error for non-whole float as integer")
	}
	valInt, err := settings.ValidateAndCoerce(100.0, settings.TypeInteger)
	if err != nil || valInt.(int64) != 100 {
		t.Errorf("expected 100, got %v (err: %v)", valInt, err)
	}

	// Boolean tests
	if _, err := settings.ValidateAndCoerce("true", settings.TypeBoolean); err == nil {
		t.Errorf("expected error for string 'true' as boolean")
	}
	valBool, err := settings.ValidateAndCoerce(true, settings.TypeBoolean)
	if err != nil || valBool.(bool) != true {
		t.Errorf("expected true, got %v (err: %v)", valBool, err)
	}

	// JSON tests
	if _, err := settings.ValidateAndCoerce("not_json_obj", settings.TypeJSON); err == nil {
		t.Errorf("expected error for raw string as json")
	}
	obj := map[string]interface{}{"retry_count": 3}
	valJSON, err := settings.ValidateAndCoerce(obj, settings.TypeJSON)
	if err != nil || valJSON == nil {
		t.Errorf("expected json map, got %v (err: %v)", valJSON, err)
	}
}

func TestSettings_CRUDAndMasking(t *testing.T) {
	dbURL := testkit.RequireIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	repo := settings.NewRepository(pool, nil)
	svc := settings.NewService(repo, nil, nil)

	var realUserID uuid.UUID
	err = pool.QueryRow(ctx, `SELECT id FROM users LIMIT 1`).Scan(&realUserID)
	if err != nil {
		t.Fatalf("fetch seeded user: %v", err)
	}

	tcPlat := &tenant.Context{
		ActorUserID:     realUserID,
		ActorOrgType:    "platform",
		IsPlatformAdmin: true,
	}

	// 1. Create a secret setting
	secretKey := "security.test_api_key_" + testkit.RandHex(4)
	secretValue := "super_secret_token_xyz_999"

	err = repo.UpsertSetting(ctx, &settings.Setting{
		Key:         secretKey,
		Value:       secretValue,
		ValueType:   settings.TypeSecret,
		Category:    settings.CategorySecurity,
		Description: "Sensitive test API key",
		IsSecret:    true,
	})
	if err != nil {
		t.Fatalf("upsert secret setting: %v", err)
	}

	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		_, _ = pool.Exec(cctx, `DELETE FROM settings_registry WHERE key = $1`, secretKey)
	})

	// 2. Fetch single setting via service -> must be masked
	s, err := svc.GetSetting(ctx, tcPlat, secretKey)
	if err != nil {
		t.Fatalf("get setting: %v", err)
	}
	if s.Value != "********" {
		t.Errorf("SECURITY LEAK: secret setting was not masked! Got: %v", s.Value)
	}

	// 3. List all settings -> secret setting must be masked
	all, err := svc.ListSettings(ctx, tcPlat, "")
	if err != nil {
		t.Fatalf("list settings: %v", err)
	}
	for _, item := range all {
		if item.Key == secretKey && item.Value != "********" {
			t.Errorf("SECURITY LEAK: secret in list settings was not masked! Got: %v", item.Value)
		}
	}

	// 4. Update non-secret setting
	updated, err := svc.UpdateSetting(ctx, tcPlat, "system.maintenance_mode", true)
	if err != nil {
		t.Fatalf("update maintenance mode: %v", err)
	}
	if updated.Value.(bool) != true {
		t.Errorf("expected updated value true, got %v", updated.Value)
	}

	// Check typed bool getter
	if !svc.GetTypedBool(ctx, "system.maintenance_mode", false) {
		t.Errorf("expected GetTypedBool to return true")
	}

	// 5. Reset setting to factory default
	err = svc.ResetSetting(ctx, tcPlat, "system.maintenance_mode")
	if err != nil {
		t.Fatalf("reset setting: %v", err)
	}
	if svc.GetTypedBool(ctx, "system.maintenance_mode", true) != false {
		t.Errorf("expected GetTypedBool to return false after reset")
	}
}

func TestSettings_HTTPRoutesAndRBAC(t *testing.T) {
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

	repo := settings.NewRepository(pool, nil)
	svc := settings.NewService(repo, nil, nil)
	handler := settings.NewHandler(svc)

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

	// 1. Customer Token (Holds setting.view only)
	custUser := uuid.New()
	custOrg := uuid.New()
	custToken, _ := tokenMgr.GenerateAccessToken(custUser, custOrg, custOrg, "customer", "/c/", false, []string{"setting.view"})

	// GET /api/v1/settings -> 200 OK
	reqListCust := httptest.NewRequest("GET", "/api/v1/settings", nil)
	reqListCust.Header.Set("Authorization", "Bearer "+custToken)
	respListCust, _ := app.Test(reqListCust)
	if respListCust.StatusCode != http.StatusOK {
		t.Errorf("customer failed to view settings! Status: %d", respListCust.StatusCode)
	}

	// Attempt PUT /api/v1/settings/system.maintenance_mode with customer token -> 403 Forbidden
	bodyMod := []byte(`{"value": true}`)
	reqPutForbidden := httptest.NewRequest("PUT", "/api/v1/settings/system.maintenance_mode", bytes.NewReader(bodyMod))
	reqPutForbidden.Header.Set("Authorization", "Bearer "+custToken)
	reqPutForbidden.Header.Set("Content-Type", "application/json")
	respPutForbidden, _ := app.Test(reqPutForbidden)
	if respPutForbidden.StatusCode != http.StatusForbidden {
		t.Errorf("TENANT LEAK: Customer was not denied PUT setting! Got status: %d (want 403)", respPutForbidden.StatusCode)
	}

	// 2. Platform Admin Token (Holds setting.view and setting.manage)
	var platUser uuid.UUID
	_ = pool.QueryRow(ctx, `SELECT id FROM users LIMIT 1`).Scan(&platUser)
	platOrg := uuid.New()
	platToken, _ := tokenMgr.GenerateAccessToken(platUser, platOrg, platOrg, "platform", "/p/", true, []string{"setting.view", "setting.manage"})

	// Type Mismatch: sending string "not_a_boolean" to boolean setting -> 400 Bad Request
	bodyBadType := []byte(`{"value": "not_a_boolean"}`)
	reqBadType := httptest.NewRequest("PUT", "/api/v1/settings/system.maintenance_mode", bytes.NewReader(bodyBadType))
	reqBadType.Header.Set("Authorization", "Bearer "+platToken)
	reqBadType.Header.Set("Content-Type", "application/json")
	respBadType, _ := app.Test(reqBadType)
	if respBadType.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request on type mismatch, got %d", respBadType.StatusCode)
	}

	// Valid update: integer setting
	bodyValidInt := []byte(`{"value": 150}`)
	reqValidInt := httptest.NewRequest("PUT", "/api/v1/settings/messaging.default_max_tps", bytes.NewReader(bodyValidInt))
	reqValidInt.Header.Set("Authorization", "Bearer "+platToken)
	reqValidInt.Header.Set("Content-Type", "application/json")
	respValidInt, _ := app.Test(reqValidInt)
	if respValidInt.StatusCode != http.StatusOK {
		t.Errorf("platform admin failed valid integer update! Status: %d", respValidInt.StatusCode)
	}

	// Reset setting
	reqReset := httptest.NewRequest("POST", "/api/v1/settings/messaging.default_max_tps/reset", nil)
	reqReset.Header.Set("Authorization", "Bearer "+platToken)
	respReset, _ := app.Test(reqReset)
	if respReset.StatusCode != http.StatusOK {
		t.Errorf("platform admin failed reset setting! Status: %d", respReset.StatusCode)
	}
}
