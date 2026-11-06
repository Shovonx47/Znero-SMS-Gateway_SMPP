package org

import (
	"errors"
	"strconv"

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
	group := router.Group("/organizations", authMiddleware)
	group.Get("", rbac.RequirePermission("org.organization.view"), h.list)
	group.Get("/", rbac.RequirePermission("org.organization.view"), h.list)
	group.Post("", rbac.RequirePermission("org.organization.create"), h.create)
	group.Post("/", rbac.RequirePermission("org.organization.create"), h.create)
	group.Get("/:id", rbac.RequirePermission("org.organization.view"), h.get)
	group.Put("/:id", rbac.RequirePermission("org.organization.edit"), h.update)
	group.Post("/:id/suspend", rbac.RequirePermission("org.organization.suspend"), h.suspend)
	group.Post("/:id/activate", rbac.RequirePermission("org.organization.activate"), h.activate)
}

func (h *Handler) list(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "25"))

	filter := OrgFilter{
		Search:   c.Query("search"),
		Type:     c.Query("type"),
		Status:   c.Query("status"),
		Page:     page,
		PageSize: pageSize,
	}

	res, err := h.svc.List(c.UserContext(), tc, filter)
	if err != nil {
		return httpx.Internal(err)
	}
	return httpx.OK(c, res)
}

func (h *Handler) get(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid organization ID")
	}

	org, err := h.svc.Get(c.UserContext(), tc, id)
	if err != nil {
		if errors.Is(err, ErrOrgNotFound) {
			return httpx.NotFound("Organization not found")
		}
		return httpx.Internal(err)
	}
	return httpx.OK(c, org)
}

func (h *Handler) create(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	var req CreateOrgRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid request body")
	}

	created, err := h.svc.Create(c.UserContext(), tc, req)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidOrgName), errors.Is(err, ErrInvalidOrgType):
			return httpx.ValidationFailed("Validation failed", err.Error())
		case errors.Is(err, ErrCannotCreateChild), errors.Is(err, ErrParentNotPermitted):
			return httpx.Forbidden(err.Error())
		default:
			return httpx.Internal(err)
		}
	}

	return httpx.JSON(c, fiber.StatusCreated, created)
}

func (h *Handler) update(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid organization ID")
	}

	var req UpdateOrgRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid request body")
	}

	updated, err := h.svc.Update(c.UserContext(), tc, id, req)
	if err != nil {
		switch {
		case errors.Is(err, ErrOrgNotFound):
			return httpx.NotFound("Organization not found")
		case errors.Is(err, ErrInvalidOrgName):
			return httpx.ValidationFailed("Validation failed", err.Error())
		default:
			return httpx.Internal(err)
		}
	}

	return httpx.OK(c, updated)
}

func (h *Handler) suspend(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid organization ID")
	}

	var req struct {
		Reason string `json:"reason"`
	}
	_ = c.BodyParser(&req)

	if err := h.svc.Suspend(c.UserContext(), tc, id, req.Reason); err != nil {
		switch {
		case errors.Is(err, ErrOrgNotFound):
			return httpx.NotFound("Organization not found")
		case errors.Is(err, ErrCannotModifyRoot):
			return httpx.Forbidden(err.Error())
		default:
			return httpx.Internal(err)
		}
	}

	return httpx.OK(c, map[string]string{"message": "Organization suspended successfully"})
}

func (h *Handler) activate(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid organization ID")
	}

	if err := h.svc.Activate(c.UserContext(), tc, id); err != nil {
		if errors.Is(err, ErrOrgNotFound) {
			return httpx.NotFound("Organization not found")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, map[string]string{"message": "Organization reactivated successfully"})
}
