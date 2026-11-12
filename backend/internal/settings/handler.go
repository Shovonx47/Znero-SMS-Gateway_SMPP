package settings

import (
	"errors"

	"github.com/gofiber/fiber/v2"
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
	group := router.Group("/settings", authMiddleware)

	group.Get("/", rbac.RequirePermission("setting.view"), h.listSettings)
	group.Get("/:key", rbac.RequirePermission("setting.view"), h.getSetting)
	group.Put("/:key", rbac.RequirePermission("setting.manage"), h.updateSetting)
	group.Post("/:key/reset", rbac.RequirePermission("setting.manage"), h.resetSetting)
}

func (h *Handler) listSettings(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	category := c.Query("category")
	items, err := h.svc.ListSettings(c.UserContext(), tc, category)
	if err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{
		"settings": items,
	})
}

func (h *Handler) getSetting(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	key := c.Params("key")
	item, err := h.svc.GetSetting(c.UserContext(), tc, key)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return httpx.NotFound("Setting not found")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, item)
}

func (h *Handler) updateSetting(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	key := c.Params("key")
	var req UpdateSettingRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid request body")
	}

	updated, err := h.svc.UpdateSetting(c.UserContext(), tc, key, req.Value)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return httpx.NotFound("Setting not found")
		}
		if errors.Is(err, ErrInvalidValue) {
			return httpx.BadRequest(err.Error())
		}
		if errors.Is(err, ErrUnauthorizedTenant) {
			return httpx.Forbidden("Access restricted to platform administrators")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, updated)
}

func (h *Handler) resetSetting(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	key := c.Params("key")
	if err := h.svc.ResetSetting(c.UserContext(), tc, key); err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return httpx.NotFound("Setting not found")
		}
		if errors.Is(err, ErrUnauthorizedTenant) {
			return httpx.Forbidden("Access restricted to platform administrators")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{"status": "reset_to_default"})
}
