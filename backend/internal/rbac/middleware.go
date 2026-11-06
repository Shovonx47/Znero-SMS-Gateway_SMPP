package rbac

import (
	"fmt"

	"github.com/gofiber/fiber/v2"
	"github.com/znero-international/znero/backend/pkg/httpx"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

// RequirePermission constructs a strict, default-deny authorization guard.
// If permissionCode is empty, the route has no permission declared and is ALWAYS denied.
func RequirePermission(permissionCode string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if permissionCode == "" {
			return httpx.Forbidden("Access denied: no permission policy declared for route (default deny)")
		}

		tc, err := tenant.FromContext(c.UserContext())
		if err != nil {
			return httpx.Unauthorized("Authentication required")
		}

		if !tc.HasPermission(permissionCode) {
			return httpx.Forbidden(fmt.Sprintf("Access denied: missing required permission %q", permissionCode))
		}

		return c.Next()
	}
}

// RequireAnyPermission allows access if the user holds at least one of the specified permissions.
func RequireAnyPermission(permissionCodes ...string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if len(permissionCodes) == 0 {
			return httpx.Forbidden("Access denied: no permission policy declared for route (default deny)")
		}

		tc, err := tenant.FromContext(c.UserContext())
		if err != nil {
			return httpx.Unauthorized("Authentication required")
		}

		if tc.IsPlatformAdmin {
			return c.Next()
		}

		for _, code := range permissionCodes {
			if tc.HasPermission(code) {
				return c.Next()
			}
		}

		return httpx.Forbidden("Access denied: missing required permissions")
	}
}
