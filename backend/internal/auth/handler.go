package auth

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/config"
	"github.com/znero-international/znero/backend/internal/rbac"
	"github.com/znero-international/znero/backend/pkg/httpx"
)

type Handler struct {
	svc        *Service
	cfg        *config.Config
	cookieName string
	secure     bool
}

func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{
		svc:        svc,
		cfg:        cfg,
		cookieName: cfg.Auth.SessionCookieName,
		secure:     cfg.Env != config.EnvDevelopment,
	}
}

// Register mounts both public and protected auth routes on the Fiber router.
func (h *Handler) Register(router fiber.Router, authMiddleware fiber.Handler) {
	authGroup := router.Group("/auth")

	// Public routes
	authGroup.Post("/login", h.Login)
	authGroup.Post("/mfa/verify", h.VerifyMFA)
	authGroup.Post("/refresh", CSRFProtection(h.cfg.Server.CORSOrigins), h.RefreshToken)
	authGroup.Post("/logout", CSRFProtection(h.cfg.Server.CORSOrigins), h.Logout)
	authGroup.Post("/password/reset-request", h.RequestPasswordReset)
	authGroup.Post("/password/reset-confirm", h.ConfirmPasswordReset)
	authGroup.Post("/invitation/accept", h.AcceptInvitation)

	// Protected routes (Self & Session)
	authProtected := authGroup.Group("", authMiddleware)
	authProtected.Get("/me", h.Me)
	authProtected.Post("/mfa/setup", h.SetupMFA)
	authProtected.Post("/mfa/confirm", h.ConfirmMFA)
	authProtected.Post("/logout-all", h.LogoutAll)
	authProtected.Get("/sessions", h.ListSessions)
	authProtected.Delete("/sessions/:id", h.RevokeSession)
	authProtected.Post("/password/change", h.ChangePassword)
	authProtected.Get("/login-history", rbac.RequirePermission("audit.log.view"), h.ListLoginHistory)

	// Protected User Management routes
	usersGroup := router.Group("/users", authMiddleware)
	usersGroup.Get("", rbac.RequirePermission("user.account.view"), h.ListUsers)
	usersGroup.Get("/:id", rbac.RequirePermission("user.account.view"), h.GetUser)
	usersGroup.Post("/invite", rbac.RequirePermission("user.account.create"), h.InviteUser)
	usersGroup.Post("/:id/suspend", rbac.RequirePermission("user.account.suspend"), h.SuspendUser)
	usersGroup.Post("/:id/activate", rbac.RequirePermission("user.account.edit"), h.ActivateUser)
	usersGroup.Put("/:id/roles", rbac.RequirePermission("role.role.assign"), h.UpdateUserRoles)
}

func (h *Handler) setRefreshCookie(c *fiber.Ctx, token string) {
	c.Cookie(&fiber.Cookie{
		Name:     h.cookieName,
		Value:    token,
		Path:     "/api/v1/auth",
		HTTPOnly: true,
		Secure:   h.secure,
		SameSite: "Strict",
		Expires:  time.Now().Add(7 * 24 * time.Hour),
		MaxAge:   7 * 24 * 3600,
	})
}

func (h *Handler) clearRefreshCookie(c *fiber.Ctx) {
	c.Cookie(&fiber.Cookie{
		Name:     h.cookieName,
		Value:    "",
		Path:     "/api/v1/auth",
		HTTPOnly: true,
		Secure:   h.secure,
		SameSite: "Strict",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}

func (h *Handler) Login(c *fiber.Ctx) error {
	var req LoginRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}
	if req.Email == "" || req.Password == "" {
		return httpx.BadRequest("Email and password are required")
	}

	ip := c.IP()
	userAgent := c.Get("User-Agent")
	reqID := httpx.RequestID(c)

	resp, rawRefresh, err := h.svc.Login(c.UserContext(), req.Email, req.Password, ip, userAgent, reqID)
	if err != nil {
		switch err {
		case ErrAccountLocked:
			return httpx.NewError(fiber.StatusTooManyRequests, "account_locked", err.Error())
		case ErrAccountSuspended, ErrOrgSuspended:
			return httpx.Forbidden(err.Error())
		default:
			return httpx.Unauthorized(ErrInvalidCredentials.Error())
		}
	}

	if rawRefresh != "" {
		h.setRefreshCookie(c, rawRefresh)
	}

	return httpx.OK(c, resp)
}

func (h *Handler) VerifyMFA(c *fiber.Ctx) error {
	var req MFAVerifyRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}
	if req.ChallengeToken == "" || req.Code == "" {
		return httpx.BadRequest("Challenge token and code are required")
	}

	ip := c.IP()
	userAgent := c.Get("User-Agent")
	reqID := httpx.RequestID(c)

	resp, rawRefresh, err := h.svc.VerifyMFA(c.UserContext(), req.ChallengeToken, req.Code, ip, userAgent, reqID)
	if err != nil {
		switch err {
		case ErrInvalidChallenge, ErrInvalidMFACode:
			return httpx.Unauthorized(err.Error())
		case ErrAccountSuspended, ErrOrgSuspended:
			return httpx.Forbidden(err.Error())
		default:
			return httpx.BadRequest(err.Error())
		}
	}

	h.setRefreshCookie(c, rawRefresh)
	return httpx.OK(c, resp)
}

func (h *Handler) RefreshToken(c *fiber.Ctx) error {
	cookieVal := c.Cookies(h.cookieName)
	if cookieVal == "" {
		return httpx.Unauthorized("Missing session refresh cookie")
	}

	ip := c.IP()
	userAgent := c.Get("User-Agent")

	resp, newRawRefresh, err := h.svc.RefreshToken(c.UserContext(), cookieVal, ip, userAgent)
	if err != nil {
		h.clearRefreshCookie(c)
		return httpx.Unauthorized(err.Error())
	}

	h.setRefreshCookie(c, newRawRefresh)
	return httpx.OK(c, resp)
}

func (h *Handler) Logout(c *fiber.Ctx) error {
	cookieVal := c.Cookies(h.cookieName)
	if cookieVal != "" {
		_ = h.svc.Logout(c.UserContext(), cookieVal)
	}
	h.clearRefreshCookie(c)
	return httpx.OK(c, fiber.Map{"message": "Logged out successfully"})
}

func (h *Handler) Me(c *fiber.Ctx) error {
	tc, err := GetTenantContext(c)
	if err != nil {
		return httpx.Unauthorized("Unauthenticated")
	}
	u, err := h.svc.GetUser(c.UserContext(), tc, tc.ActorUserID)
	if err != nil {
		return httpx.Unauthorized("User not found")
	}
	return httpx.OK(c, fiber.Map{
		"user":           u,
		"tenant_context": tc,
	})
}

func (h *Handler) SetupMFA(c *fiber.Ctx) error {
	tc, err := GetTenantContext(c)
	if err != nil {
		return httpx.Unauthorized("Unauthenticated")
	}

	res, err := h.svc.SetupMFA(c.UserContext(), tc)
	if err != nil {
		return httpx.BadRequest(err.Error())
	}
	return httpx.OK(c, res)
}

func (h *Handler) ConfirmMFA(c *fiber.Ctx) error {
	tc, err := GetTenantContext(c)
	if err != nil {
		return httpx.Unauthorized("Unauthenticated")
	}

	var req MFAConfirmRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}

	if err := h.svc.ConfirmMFA(c.UserContext(), tc, req); err != nil {
		return httpx.BadRequest(err.Error())
	}
	return httpx.OK(c, fiber.Map{"message": "MFA enrollment confirmed successfully"})
}

func (h *Handler) LogoutAll(c *fiber.Ctx) error {
	tc, err := GetTenantContext(c)
	if err != nil {
		return httpx.Unauthorized("Unauthenticated")
	}

	if err := h.svc.LogoutAll(c.UserContext(), tc.ActorUserID); err != nil {
		return httpx.BadRequest(err.Error())
	}
	h.clearRefreshCookie(c)
	return httpx.OK(c, fiber.Map{"message": "All sessions revoked"})
}

func (h *Handler) ListSessions(c *fiber.Ctx) error {
	tc, err := GetTenantContext(c)
	if err != nil {
		return httpx.Unauthorized("Unauthenticated")
	}
	currentSessionID := GetSessionID(c)

	sessions, err := h.svc.ListSessions(c.UserContext(), tc.ActorUserID, currentSessionID)
	if err != nil {
		return httpx.BadRequest(err.Error())
	}
	return httpx.OK(c, sessions)
}

func (h *Handler) RevokeSession(c *fiber.Ctx) error {
	tc, err := GetTenantContext(c)
	if err != nil {
		return httpx.Unauthorized("Unauthenticated")
	}

	idParam := c.Params("id")
	sessionID, err := uuid.Parse(idParam)
	if err != nil {
		return httpx.BadRequest("Invalid session ID")
	}

	if err := h.svc.RevokeSession(c.UserContext(), tc.ActorUserID, sessionID); err != nil {
		return httpx.BadRequest(err.Error())
	}
	return httpx.OK(c, fiber.Map{"message": "Session revoked"})
}

func (h *Handler) ChangePassword(c *fiber.Ctx) error {
	tc, err := GetTenantContext(c)
	if err != nil {
		return httpx.Unauthorized("Unauthenticated")
	}

	var req ChangePasswordRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}

	if err := h.svc.ChangePassword(c.UserContext(), tc, req.CurrentPassword, req.NewPassword); err != nil {
		return httpx.BadRequest(err.Error())
	}
	return httpx.OK(c, fiber.Map{"message": "Password changed successfully"})
}

func (h *Handler) RequestPasswordReset(c *fiber.Ctx) error {
	var req ResetPasswordRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}

	ip := c.IP()
	userAgent := c.Get("User-Agent")

	_ = h.svc.RequestPasswordReset(c.UserContext(), req.Email, ip, userAgent)
	return httpx.OK(c, fiber.Map{
		"message": "If an active account exists for that email, a password reset link has been dispatched.",
	})
}

func (h *Handler) ConfirmPasswordReset(c *fiber.Ctx) error {
	var req ConfirmResetRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}
	if req.Token == "" || req.NewPassword == "" {
		return httpx.BadRequest("Token and new password are required")
	}

	if err := h.svc.ConfirmPasswordReset(c.UserContext(), req.Token, req.NewPassword); err != nil {
		return httpx.BadRequest(err.Error())
	}
	return httpx.OK(c, fiber.Map{"message": "Password has been reset successfully. Please log in with your new password."})
}

func (h *Handler) AcceptInvitation(c *fiber.Ctx) error {
	var req AcceptInvitationRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}
	if req.Token == "" || req.Password == "" {
		return httpx.BadRequest("Token and password are required")
	}

	if err := h.svc.AcceptInvitation(c.UserContext(), req.Token, req.Password); err != nil {
		return httpx.BadRequest(err.Error())
	}
	return httpx.OK(c, fiber.Map{"message": "Invitation accepted successfully. You can now log in."})
}

func (h *Handler) ListUsers(c *fiber.Ctx) error {
	tc, err := GetTenantContext(c)
	if err != nil {
		return httpx.Unauthorized("Unauthenticated")
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "20"))
	search := c.Query("search", "")
	status := c.Query("status", "")

	var orgID *uuid.UUID
	if orgStr := c.Query("org_id"); orgStr != "" {
		if parsed, err := uuid.Parse(orgStr); err == nil {
			orgID = &parsed
		}
	}

	users, total, err := h.svc.ListUsers(c.UserContext(), tc, orgID, search, status, page, pageSize)
	if err != nil {
		return httpx.BadRequest(err.Error())
	}

	return httpx.OK(c, fiber.Map{
		"items":     users,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *Handler) GetUser(c *fiber.Ctx) error {
	tc, err := GetTenantContext(c)
	if err != nil {
		return httpx.Unauthorized("Unauthenticated")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid user ID")
	}

	u, err := h.svc.GetUser(c.UserContext(), tc, id)
	if err != nil {
		return httpx.NotFound("User not found")
	}
	return httpx.OK(c, u)
}

func (h *Handler) InviteUser(c *fiber.Ctx) error {
	tc, err := GetTenantContext(c)
	if err != nil {
		return httpx.Unauthorized("Unauthenticated")
	}

	var req InviteUserRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}

	newUser, err := h.svc.InviteUser(c.UserContext(), tc, req)
	if err != nil {
		return httpx.BadRequest(err.Error())
	}
	return httpx.JSON(c, fiber.StatusCreated, newUser)
}

func (h *Handler) SuspendUser(c *fiber.Ctx) error {
	tc, err := GetTenantContext(c)
	if err != nil {
		return httpx.Unauthorized("Unauthenticated")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid user ID")
	}

	var body struct {
		Reason string `json:"reason"`
	}
	_ = c.BodyParser(&body)

	if err := h.svc.SuspendUser(c.UserContext(), tc, id, body.Reason); err != nil {
		if err.Error() == "user not found" {
			return httpx.NotFound("User not found")
		}
		return httpx.BadRequest(err.Error())
	}
	return httpx.OK(c, fiber.Map{"message": "User suspended successfully"})
}

func (h *Handler) ActivateUser(c *fiber.Ctx) error {
	tc, err := GetTenantContext(c)
	if err != nil {
		return httpx.Unauthorized("Unauthenticated")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid user ID")
	}

	if err := h.svc.ActivateUser(c.UserContext(), tc, id); err != nil {
		if err.Error() == "user not found" {
			return httpx.NotFound("User not found")
		}
		return httpx.BadRequest(err.Error())
	}
	return httpx.OK(c, fiber.Map{"message": "User activated successfully"})
}

func (h *Handler) UpdateUserRoles(c *fiber.Ctx) error {
	tc, err := GetTenantContext(c)
	if err != nil {
		return httpx.Unauthorized("Unauthenticated")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid user ID")
	}

	var body struct {
		RoleIDs []uuid.UUID `json:"role_ids"`
	}
	if err := c.BodyParser(&body); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}

	if err := h.svc.UpdateUserRoles(c.UserContext(), tc, id, body.RoleIDs); err != nil {
		if err.Error() == "user not found" {
			return httpx.NotFound("User not found")
		}
		return httpx.BadRequest(err.Error())
	}
	return httpx.OK(c, fiber.Map{"message": "User roles updated successfully"})
}

func (h *Handler) ListLoginHistory(c *fiber.Ctx) error {
	tc, err := GetTenantContext(c)
	if err != nil {
		return httpx.Unauthorized("Unauthenticated")
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "50"))

	var userID *uuid.UUID
	if userStr := c.Query("user_id"); userStr != "" {
		if parsed, err := uuid.Parse(userStr); err == nil {
			userID = &parsed
		}
	}

	items, total, err := h.svc.ListLoginHistory(c.UserContext(), tc, userID, page, pageSize)
	if err != nil {
		return httpx.BadRequest(err.Error())
	}

	return httpx.OK(c, fiber.Map{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}
