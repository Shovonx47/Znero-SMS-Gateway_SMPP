package rbac

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/pkg/httpx"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

func TestRequirePermission(t *testing.T) {
	app := fiber.New(fiber.Config{
		ErrorHandler: httpx.ErrorHandler(nil),
	})

	// 1. Undeclared permission guard: MUST deny everything
	app.Get("/test/undeclared", RequirePermission(""), func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	// 2. Resource route with permission
	app.Get("/test/guarded", RequirePermission("user.account.create"), func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	// Test 1: Route with empty permission code is always forbidden (default deny)
	req := httptest.NewRequest(http.MethodGet, "/test/undeclared", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("empty permission route returned status %d, want 403", resp.StatusCode)
	}

	// Test 2: Unauthenticated request to guarded route -> 401 Unauthorized
	req = httptest.NewRequest(http.MethodGet, "/test/guarded", nil)
	resp, err = app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated request returned %d, want 401", resp.StatusCode)
	}

	// Test 3: Authenticated user without required permission -> 403 Forbidden
	appWithAuth := fiber.New(fiber.Config{
		ErrorHandler: httpx.ErrorHandler(nil),
	})
	appWithAuth.Use(func(c *fiber.Ctx) error {
		tc := &tenant.Context{
			ActorUserID:     uuid.New(),
			ActorOrgID:      uuid.New(),
			IsPlatformAdmin: false,
			Permissions: map[string]bool{
				"other.permission.view": true,
			},
		}
		c.SetUserContext(tenant.WithContext(c.UserContext(), tc))
		return c.Next()
	})
	appWithAuth.Get("/test/guarded", RequirePermission("user.account.create"), func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	req = httptest.NewRequest(http.MethodGet, "/test/guarded", nil)
	resp, err = appWithAuth.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("user without permission returned %d, want 403", resp.StatusCode)
	}

	// Test 4: Authenticated user with permission -> 200 OK
	appPermitted := fiber.New()
	appPermitted.Use(func(c *fiber.Ctx) error {
		tc := &tenant.Context{
			ActorUserID:     uuid.New(),
			ActorOrgID:      uuid.New(),
			IsPlatformAdmin: false,
			Permissions: map[string]bool{
				"user.account.create": true,
			},
		}
		c.SetUserContext(tenant.WithContext(c.UserContext(), tc))
		return c.Next()
	})
	appPermitted.Get("/test/guarded", RequirePermission("user.account.create"), func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	req = httptest.NewRequest(http.MethodGet, "/test/guarded", nil)
	resp, err = appPermitted.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("user with permission returned %d, want 200", resp.StatusCode)
	}

	// Test 5: Platform Admin has wildcard bypass -> 200 OK
	appAdmin := fiber.New()
	appAdmin.Use(func(c *fiber.Ctx) error {
		tc := &tenant.Context{
			ActorUserID:     uuid.New(),
			ActorOrgID:      uuid.New(),
			IsPlatformAdmin: true,
			Permissions:     map[string]bool{},
		}
		c.SetUserContext(tenant.WithContext(c.UserContext(), tc))
		return c.Next()
	})
	appAdmin.Get("/test/guarded", RequirePermission("user.account.create"), func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	req = httptest.NewRequest(http.MethodGet, "/test/guarded", nil)
	resp, err = appAdmin.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("platform admin returned %d, want 200", resp.StatusCode)
	}
}

func TestPrivilegeEscalationGuard(t *testing.T) {
	svc := NewService(nil, nil, nil)

	tc := &tenant.Context{
		ActorUserID:     uuid.New(),
		ActorOrgID:      uuid.New(),
		IsPlatformAdmin: false,
		Permissions: map[string]bool{
			"user.account.view": true,
		},
	}

	// Non-platform user attempting to grant "user.account.delete" which they lack
	req := CreateRoleRequest{
		Name:        "Attempted Escalation Role",
		Description: "Should fail",
		Permissions: []string{"user.account.view", "user.account.delete"},
	}

	_, err := svc.CreateRole(context.Background(), tc, req)
	if err == nil {
		t.Fatalf("expected privilege escalation error, got nil")
	}
}
