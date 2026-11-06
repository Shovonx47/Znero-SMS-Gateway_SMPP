package auth

import (
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/pkg/httpx"
	"github.com/znero-international/znero/backend/pkg/tenant"
	"github.com/znero-international/znero/backend/pkg/tokens"
)

// Authenticate verifies the Bearer JWT token from the Authorization header,
// enforces instant revocation (user/org status checks), and constructs the tenant.Context.
func Authenticate(tokenMgr *tokens.TokenManager, authRepo Repository, orgRepo org.Repository) fiber.Handler {
	return func(c *fiber.Ctx) error {
		authHeader := c.Get("Authorization")
		if authHeader == "" {
			return httpx.Unauthorized("Missing Authorization header")
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return httpx.Unauthorized("Invalid Authorization header format; expected 'Bearer <token>'")
		}

		tokenStr := strings.TrimSpace(parts[1])
		claims, err := tokenMgr.ValidateAccessToken(tokenStr)
		if err != nil {
			return httpx.Unauthorized("Invalid or expired access token")
		}

		// Instant revocation defense: verify session is active and not revoked
		sess, err := authRepo.GetSessionByID(c.UserContext(), claims.SessionID)
		if err != nil || sess == nil || sess.RevokedAt != nil || sess.ExpiresAt.Before(time.Now()) {
			return httpx.Unauthorized("Session is revoked or expired")
		}

		// Instant revocation defense: verify user is still active in database
		u, err := authRepo.GetUserByID(c.UserContext(), claims.UserID)
		if err != nil || u == nil || u.Status != "active" {
			return httpx.Unauthorized("Account is suspended or invalid")
		}

		// Verify organization is active
		orgRecord, err := orgRepo.GetByID(c.UserContext(), claims.OrgID)
		if err != nil || orgRecord == nil || orgRecord.Status != "active" {
			return httpx.Unauthorized("Organization is suspended or invalid")
		}

		// Construct tenant context derived strictly on the server
		permMap := make(map[string]bool, len(claims.Permissions))
		for _, p := range claims.Permissions {
			permMap[p] = true
		}

		tc := &tenant.Context{
			ActorUserID:     claims.UserID,
			ActorOrgID:      claims.OrgID,
			ActorOrgType:    claims.OrgType,
			ActorOrgPath:    claims.OrgPath,
			IsPlatformAdmin: claims.IsPlatformAdmin,
			Permissions:     permMap,
		}

		// Store in Fiber locals and context
		c.Locals("tenant_context", tc)
		c.Locals("session_id", claims.SessionID)
		c.SetUserContext(tenant.WithContext(c.UserContext(), tc))

		return c.Next()
	}
}

// GetTenantContext retrieves the tenant.Context stored by Authenticate middleware.
func GetTenantContext(c *fiber.Ctx) (*tenant.Context, error) {
	tc, ok := c.Locals("tenant_context").(*tenant.Context)
	if !ok || tc == nil {
		return tenant.FromContext(c.UserContext())
	}
	return tc, nil
}

// GetSessionID retrieves the current session UUID stored by Authenticate middleware.
func GetSessionID(c *fiber.Ctx) uuid.UUID {
	id, ok := c.Locals("session_id").(uuid.UUID)
	if !ok {
		return uuid.Nil
	}
	return id
}

// CSRFProtection verifies that requests to cookie-authenticated endpoints originate
// from trusted origins.
func CSRFProtection(allowedOrigins []string) fiber.Handler {
	allowedMap := make(map[string]bool)
	for _, origin := range allowedOrigins {
		allowedMap[strings.TrimRight(origin, "/")] = true
	}

	return func(c *fiber.Ctx) error {
		// Safe methods do not mutate state
		method := c.Method()
		if method == fiber.MethodGet || method == fiber.MethodHead || method == fiber.MethodOptions {
			return c.Next()
		}

		origin := c.Get("Origin")
		if origin == "" {
			referer := c.Get("Referer")
			if referer != "" {
				if u, err := url.Parse(referer); err == nil {
					origin = u.Scheme + "://" + u.Host
				}
			}
		}

		if origin != "" {
			cleaned := strings.TrimRight(origin, "/")
			if !allowedMap[cleaned] {
				return httpx.Forbidden("Cross-site request forgery protection: origin not permitted")
			}
		}

		return c.Next()
	}
}
