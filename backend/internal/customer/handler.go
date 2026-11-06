package customer

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/znero-international/znero/backend/internal/rbac"
	"github.com/znero-international/znero/backend/pkg/httpx"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Register(router fiber.Router, authMiddleware fiber.Handler) {
	credLimiter := httpx.CredentialRateLimiter(10, time.Minute)

	prof := router.Group("/customer-profiles", authMiddleware)
	prof.Get("", rbac.RequirePermission("customer.profile.view"), h.listProfiles)
	prof.Get("/export", rbac.RequirePermission("customer.profile.view"), h.exportProfiles)
	prof.Get("/by-org/:orgId", rbac.RequirePermission("customer.profile.view"), h.getProfileByOrg)
	prof.Get("/:id", rbac.RequirePermission("customer.profile.view"), h.getProfile)
	prof.Get("/:id/history", rbac.RequirePermission("customer.profile.view"), h.profileHistory)
	prof.Post("", rbac.RequirePermission("customer.profile.create"), h.createProfile)
	prof.Put("/:id", rbac.RequirePermission("customer.profile.edit"), h.updateProfile)
	prof.Post("/:id/rollback", rbac.RequirePermission("customer.profile.edit"), h.rollbackProfile)

	ak := router.Group("/customer-api-keys", authMiddleware)
	ak.Get("", rbac.RequirePermission("customer.apikey.view"), h.listAPIKeys)
	ak.Get("/export", rbac.RequirePermission("customer.apikey.view"), h.exportAPIKeys)
	ak.Post("", credLimiter, rbac.RequirePermission("customer.apikey.create"), h.createAPIKey)
	ak.Post("/:id/revoke", rbac.RequirePermission("customer.apikey.revoke"), h.revokeAPIKey)
	ak.Post("/:id/rotate", credLimiter, rbac.RequirePermission("customer.apikey.rotate"), h.rotateAPIKey)

	smpp := router.Group("/customer-smpp-accounts", authMiddleware)
	smpp.Get("", rbac.RequirePermission("customer.smppaccount.view"), h.listSMPPAccounts)
	smpp.Get("/export", rbac.RequirePermission("customer.smppaccount.view"), h.exportSMPPAccounts)
	smpp.Post("", credLimiter, rbac.RequirePermission("customer.smppaccount.create"), h.createSMPPAccount)
	smpp.Put("/:id", rbac.RequirePermission("customer.smppaccount.edit"), h.updateSMPPAccount)
	smpp.Post("/:id/reset-password", credLimiter, rbac.RequirePermission("customer.smppaccount.edit"), h.resetSMPPPassword)
	smpp.Post("/:id/revoke", rbac.RequirePermission("customer.smppaccount.revoke"), h.revokeSMPPAccount)
}

func mapErr(err error) error {
	switch {
	case errors.Is(err, ErrPermissionDenied), errors.Is(err, ErrCrossTenantAccess), errors.Is(err, ErrPlatformOnlyField), errors.Is(err, ErrResellerCapExceeded):
		return httpx.Forbidden(err.Error())
	case errors.Is(err, ErrProfileNotFound), errors.Is(err, ErrAPIKeyNotFound), errors.Is(err, ErrSMPPAccountNotFound):
		return httpx.NotFound(err.Error())
	case errors.Is(err, ErrProfileAlreadyExists), errors.Is(err, ErrAPIKeyAlreadyRevoked), errors.Is(err, ErrSMPPSystemIDExists):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrValidation), errors.Is(err, ErrNotCustomerOrg):
		return httpx.BadRequest(err.Error())
	default:
		return httpx.Internal(err)
	}
}

func (h *Handler) listProfiles(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	page, _ := strconv.Atoi(c.Query("page", "1"))
	limit, _ := strconv.Atoi(c.Query("page_size", c.Query("limit", "25")))
	filter := ProfileListFilter{
		Search:      c.Query("search"),
		Status:      c.Query("status"),
		AccountType: c.Query("account_type"),
		Page:        page,
		Limit:       limit,
	}
	if v := c.Query("organization_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			filter.OrganizationID = &id
		}
	}
	items, total, err := h.svc.ListProfiles(c.UserContext(), tc, filter)
	if err != nil {
		return mapErr(err)
	}
	if items == nil {
		items = []CustomerProfile{}
	}
	return httpx.OK(c, fiber.Map{
		"items":       items,
		"total_count": total,
		"page":        page,
		"page_size":   limit,
	})
}

func (h *Handler) getProfile(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid profile ID")
	}
	p, err := h.svc.GetProfile(c.UserContext(), tc, id)
	if err != nil {
		return mapErr(err)
	}
	return httpx.OK(c, p)
}

func (h *Handler) getProfileByOrg(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	orgID, err := uuid.Parse(c.Params("orgId"))
	if err != nil {
		return httpx.BadRequest("Invalid organization ID")
	}
	p, err := h.svc.GetProfileByOrg(c.UserContext(), tc, orgID)
	if err != nil {
		return mapErr(err)
	}
	return httpx.OK(c, p)
}

func (h *Handler) createProfile(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	var req CreateProfileRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}
	res, err := h.svc.CreateProfile(c.UserContext(), tc, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	if res.PendingChange != nil {
		return httpx.OK(c, fiber.Map{"pending_approval": true, "change_request": res.PendingChange})
	}
	return httpx.OK(c, res.Profile)
}

func (h *Handler) updateProfile(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid profile ID")
	}
	var req UpdateProfileRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}
	res, err := h.svc.UpdateProfile(c.UserContext(), tc, id, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	if res.PendingChange != nil {
		return httpx.OK(c, fiber.Map{"pending_approval": true, "change_request": res.PendingChange})
	}
	return httpx.OK(c, res.Profile)
}

func (h *Handler) profileHistory(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid profile ID")
	}
	items, err := h.svc.ListProfileHistory(c.UserContext(), tc, id)
	if err != nil {
		return mapErr(err)
	}
	if items == nil {
		items = []CustomerProfileHistory{}
	}
	return httpx.OK(c, items)
}

type rollbackBody struct {
	TargetVersion int    `json:"target_version"`
	Reason        string `json:"reason"`
}

func (h *Handler) rollbackProfile(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid profile ID")
	}
	var body rollbackBody
	if err := c.BodyParser(&body); err != nil || body.TargetVersion <= 0 {
		return httpx.BadRequest("target_version is required")
	}
	res, err := h.svc.RollbackProfile(c.UserContext(), tc, id, body.TargetVersion, body.Reason, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	if res.PendingChange != nil {
		return httpx.OK(c, fiber.Map{"pending_approval": true, "change_request": res.PendingChange})
	}
	return httpx.OK(c, res.Profile)
}

// API Keys

func (h *Handler) listAPIKeys(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	orgID := tc.ActorOrgID
	if v := c.Query("organization_id"); v != "" {
		parsed, err := uuid.Parse(v)
		if err != nil {
			return httpx.BadRequest("Invalid organization_id")
		}
		orgID = parsed
	}
	items, err := h.svc.ListAPIKeys(c.UserContext(), tc, orgID)
	if err != nil {
		return mapErr(err)
	}
	if items == nil {
		items = []CustomerAPIKey{}
	}
	return httpx.OK(c, items)
}

func (h *Handler) createAPIKey(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	var req CreateAPIKeyRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}
	if req.OrganizationID == uuid.Nil {
		req.OrganizationID = tc.ActorOrgID
	}
	res, err := h.svc.CreateAPIKey(c.UserContext(), tc, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	return httpx.OK(c, res)
}

type reasonBody struct {
	Reason string `json:"reason"`
}

func (h *Handler) revokeAPIKey(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid API key ID")
	}
	var body reasonBody
	_ = c.BodyParser(&body)
	res, err := h.svc.RevokeAPIKey(c.UserContext(), tc, id, body.Reason, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	return httpx.OK(c, res)
}

func (h *Handler) rotateAPIKey(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid API key ID")
	}
	var body reasonBody
	_ = c.BodyParser(&body)
	res, err := h.svc.RotateAPIKey(c.UserContext(), tc, id, body.Reason, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	return httpx.OK(c, res)
}

// SMPP Accounts

func (h *Handler) listSMPPAccounts(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	orgID := tc.ActorOrgID
	if v := c.Query("organization_id"); v != "" {
		parsed, err := uuid.Parse(v)
		if err != nil {
			return httpx.BadRequest("Invalid organization_id")
		}
		orgID = parsed
	}
	items, err := h.svc.ListSMPPAccounts(c.UserContext(), tc, orgID)
	if err != nil {
		return mapErr(err)
	}
	if items == nil {
		items = []CustomerSMPPAccount{}
	}
	return httpx.OK(c, items)
}

func (h *Handler) createSMPPAccount(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	var req CreateSMPPAccountRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}
	res, err := h.svc.CreateSMPPAccount(c.UserContext(), tc, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	return httpx.OK(c, res)
}

func (h *Handler) updateSMPPAccount(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid SMPP account ID")
	}
	var req UpdateSMPPAccountRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}
	res, err := h.svc.UpdateSMPPAccount(c.UserContext(), tc, id, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	return httpx.OK(c, res)
}

func (h *Handler) resetSMPPPassword(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid SMPP account ID")
	}
	var body reasonBody
	_ = c.BodyParser(&body)
	res, err := h.svc.ResetSMPPPassword(c.UserContext(), tc, id, body.Reason, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	return httpx.OK(c, res)
}

func (h *Handler) revokeSMPPAccount(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid SMPP account ID")
	}
	var body reasonBody
	_ = c.BodyParser(&body)
	disabled := "disabled"
	res, err := h.svc.UpdateSMPPAccount(c.UserContext(), tc, id, UpdateSMPPAccountRequest{
		Status: &disabled,
		Reason: body.Reason,
	}, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	return httpx.OK(c, res)
}

// Exports

func (h *Handler) exportProfiles(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	filter := ProfileListFilter{
		Search:      c.Query("search"),
		Status:      c.Query("status"),
		AccountType: c.Query("account_type"),
	}
	format := c.Query("format", "csv")
	data, contentType, err := h.svc.ExportProfiles(c.UserContext(), tc, filter, format)
	if err != nil {
		return mapErr(err)
	}
	filename := fmt.Sprintf("customer_profiles_export_%d.%s", time.Now().Unix(), format)
	c.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Set("Content-Type", contentType)
	return c.Send(data)
}

func (h *Handler) exportAPIKeys(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	orgID, err := uuid.Parse(c.Query("organization_id"))
	if err != nil {
		return httpx.BadRequest("organization_id query parameter is required")
	}
	format := c.Query("format", "csv")
	data, contentType, err := h.svc.ExportAPIKeys(c.UserContext(), tc, orgID, format)
	if err != nil {
		return mapErr(err)
	}
	filename := fmt.Sprintf("customer_api_keys_%s_%d.%s", orgID.String()[:8], time.Now().Unix(), format)
	c.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Set("Content-Type", contentType)
	return c.Send(data)
}

func (h *Handler) exportSMPPAccounts(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	orgID, err := uuid.Parse(c.Query("organization_id"))
	if err != nil {
		return httpx.BadRequest("organization_id query parameter is required")
	}
	format := c.Query("format", "csv")
	data, contentType, err := h.svc.ExportSMPPAccounts(c.UserContext(), tc, orgID, format)
	if err != nil {
		return mapErr(err)
	}
	filename := fmt.Sprintf("customer_smpp_accounts_%s_%d.%s", orgID.String()[:8], time.Now().Unix(), format)
	c.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Set("Content-Type", contentType)
	return c.Send(data)
}
