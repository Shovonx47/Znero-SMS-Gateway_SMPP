package rbac

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
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
	// Permissions catalog
	router.Get("/permissions", authMiddleware, RequirePermission("role.role.view"), h.listPermissions)

	// Roles endpoints
	roles := router.Group("/roles", authMiddleware)
	roles.Get("", RequirePermission("role.role.view"), h.listRoles)
	roles.Get("/", RequirePermission("role.role.view"), h.listRoles)
	roles.Get("/:id", RequirePermission("role.role.view"), h.getRole)
	roles.Post("", RequirePermission("role.role.create"), h.createRole)
	roles.Post("/", RequirePermission("role.role.create"), h.createRole)
	roles.Put("/:id", RequirePermission("role.role.edit"), h.updateRole)
	roles.Post("/:id/clone", RequirePermission("role.role.create"), h.cloneRole)
	roles.Delete("/:id", RequirePermission("role.role.delete"), h.deleteRole)
}

func (h *Handler) listPermissions(c *fiber.Ctx) error {
	perms, err := h.svc.ListPermissions(c.UserContext())
	if err != nil {
		return httpx.Internal(err)
	}
	return httpx.OK(c, perms)
}

func (h *Handler) listRoles(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	var orgID *uuid.UUID
	if q := c.Query("organization_id"); q != "" {
		if id, err := uuid.Parse(q); err == nil {
			orgID = &id
		}
	}

	roles, err := h.svc.ListRoles(c.UserContext(), tc, orgID)
	if err != nil {
		return httpx.Internal(err)
	}
	return httpx.OK(c, roles)
}

func (h *Handler) getRole(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid role ID")
	}

	role, err := h.svc.GetRole(c.UserContext(), tc, id)
	if err != nil {
		if errors.Is(err, ErrRoleNotFound) {
			return httpx.NotFound("Role not found")
		}
		return httpx.Internal(err)
	}
	return httpx.OK(c, role)
}

func (h *Handler) createRole(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	var req CreateRoleRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid request body")
	}

	role, err := h.svc.CreateRole(c.UserContext(), tc, req)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidRoleName):
			return httpx.ValidationFailed("Invalid role name", err.Error())
		case errors.Is(err, ErrPrivilegeEscalation):
			return httpx.Forbidden(err.Error())
		default:
			return httpx.Internal(err)
		}
	}

	return httpx.JSON(c, fiber.StatusCreated, role)
}

func (h *Handler) updateRole(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid role ID")
	}

	var req UpdateRoleRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid request body")
	}

	role, err := h.svc.UpdateRole(c.UserContext(), tc, id, req)
	if err != nil {
		switch {
		case errors.Is(err, ErrRoleNotFound):
			return httpx.NotFound("Role not found")
		case errors.Is(err, ErrCannotModifySystemRole):
			return httpx.Forbidden(err.Error())
		case errors.Is(err, ErrPrivilegeEscalation):
			return httpx.Forbidden(err.Error())
		case errors.Is(err, ErrInvalidRoleName):
			return httpx.ValidationFailed("Invalid role name", err.Error())
		default:
			return httpx.Internal(err)
		}
	}

	return httpx.OK(c, role)
}

func (h *Handler) cloneRole(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid role ID")
	}

	var req CloneRoleRequest
	_ = c.BodyParser(&req)

	cloned, err := h.svc.CloneRole(c.UserContext(), tc, id, req)
	if err != nil {
		if errors.Is(err, ErrRoleNotFound) {
			return httpx.NotFound("Source role not found")
		}
		return httpx.Internal(err)
	}

	return httpx.JSON(c, fiber.StatusCreated, cloned)
}

func (h *Handler) deleteRole(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid role ID")
	}

	if err := h.svc.DeleteRole(c.UserContext(), tc, id); err != nil {
		switch {
		case errors.Is(err, ErrRoleNotFound):
			return httpx.NotFound("Role not found")
		case errors.Is(err, ErrCannotModifySystemRole):
			return httpx.Forbidden(err.Error())
		case errors.Is(err, ErrRoleHasAssignedUsers):
			return httpx.NewError(fiber.StatusConflict, httpx.CodeConflict, err.Error())
		default:
			return httpx.Internal(err)
		}
	}

	return httpx.OK(c, map[string]string{"message": "Role deleted successfully"})
}
