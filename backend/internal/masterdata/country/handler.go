package country

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
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
	group := router.Group("/countries", authMiddleware)
	group.Get("", rbac.RequirePermission("country.country.view"), h.list)
	group.Get("/export", rbac.RequirePermission("country.country.view"), h.export)
	group.Get("/:id", rbac.RequirePermission("country.country.view"), h.get)
	group.Get("/:id/history", rbac.RequirePermission("country.country.view"), h.history)
	group.Post("", rbac.RequirePermission("country.country.create"), h.create)
	group.Put("/:id", rbac.RequirePermission("country.country.edit"), h.update)
	group.Post("/:id/deactivate", rbac.RequirePermission("country.country.deactivate"), h.deactivate)
	group.Post("/:id/activate", rbac.RequirePermission("country.country.deactivate"), h.activate)
	group.Post("/:id/archive", rbac.RequirePermission("country.country.archive"), h.archive)
	group.Post("/:id/rollback", rbac.RequirePermission("country.country.edit"), h.rollback)
}

func (h *Handler) list(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "25"))

	filter := Filter{
		Search:             c.Query("search"),
		Status:             c.Query("status"),
		VerificationStatus: c.Query("verification_status"),
		Continent:          c.Query("continent"),
		CallingCode:        c.Query("calling_code"),
		Page:               page,
		PageSize:           pageSize,
		SortBy:             c.Query("sort_by", "name"),
		SortDir:            c.Query("sort_dir", "asc"),
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
		return httpx.BadRequest("Invalid country ID")
	}

	res, err := h.svc.Get(c.UserContext(), tc, id)
	if err != nil {
		if errors.Is(err, ErrCountryNotFound) {
			return httpx.NotFound("Country not found")
		}
		return httpx.Internal(err)
	}
	return httpx.OK(c, res)
}

func (h *Handler) history(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid country ID")
	}

	res, err := h.svc.GetHistory(c.UserContext(), tc, id)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyMasterData) {
			return httpx.Forbidden(err.Error())
		}
		return httpx.Internal(err)
	}
	return httpx.OK(c, res)
}

func (h *Handler) create(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	var req CreateCountryRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}

	res, err := h.svc.Create(c.UserContext(), tc, req)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyMasterData) {
			return httpx.Forbidden(err.Error())
		}
		if errors.Is(err, ErrInvalidCountryCodes) || errors.Is(err, ErrEmptyCountryName) || errors.Is(err, ErrEmptyCallingCode) {
			return httpx.BadRequest(err.Error())
		}
		if errors.Is(err, ErrDuplicateISO2) || errors.Is(err, ErrDuplicateISO3) || errors.Is(err, ErrDuplicateNum) {
			return httpx.Conflict(err.Error())
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, res)
}

func (h *Handler) update(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid country ID")
	}

	var req UpdateCountryRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}

	res, err := h.svc.Update(c.UserContext(), tc, id, req)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyMasterData) {
			return httpx.Forbidden(err.Error())
		}
		if errors.Is(err, ErrCountryNotFound) {
			return httpx.NotFound("Country not found")
		}
		if errors.Is(err, ErrEmptyCountryName) || errors.Is(err, ErrEmptyCallingCode) {
			return httpx.BadRequest(err.Error())
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, res)
}

func (h *Handler) deactivate(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid country ID")
	}

	var req ActionReasonRequest
	_ = c.BodyParser(&req)

	res, err := h.svc.SetStatus(c.UserContext(), tc, id, "inactive", req.Reason)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyMasterData) {
			return httpx.Forbidden(err.Error())
		}
		if errors.Is(err, ErrCountryNotFound) {
			return httpx.NotFound("Country not found")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, res)
}

func (h *Handler) activate(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid country ID")
	}

	var req ActionReasonRequest
	_ = c.BodyParser(&req)

	res, err := h.svc.SetStatus(c.UserContext(), tc, id, "active", req.Reason)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyMasterData) {
			return httpx.Forbidden(err.Error())
		}
		if errors.Is(err, ErrCountryNotFound) {
			return httpx.NotFound("Country not found")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, res)
}

func (h *Handler) archive(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid country ID")
	}

	var req ActionReasonRequest
	_ = c.BodyParser(&req)

	res, err := h.svc.SetStatus(c.UserContext(), tc, id, "archived", req.Reason)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyMasterData) {
			return httpx.Forbidden(err.Error())
		}
		if errors.Is(err, ErrCountryNotFound) {
			return httpx.NotFound("Country not found")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, res)
}

func (h *Handler) rollback(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid country ID")
	}

	var req RollbackRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}
	if req.Version <= 0 {
		return httpx.BadRequest("Target version must be a positive integer")
	}

	res, err := h.svc.Rollback(c.UserContext(), tc, id, req.Version, req.Reason)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyMasterData) {
			return httpx.Forbidden(err.Error())
		}
		if errors.Is(err, ErrCountryNotFound) {
			return httpx.NotFound("Country not found")
		}
		if errors.Is(err, ErrVersionNotFound) {
			return httpx.NotFound("Target version snapshot not found")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, res)
}

func (h *Handler) export(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	format := strings.ToLower(c.Query("format", "csv"))
	if format != "json" && format != "csv" {
		format = "csv"
	}

	filter := Filter{
		Search:             c.Query("search"),
		Status:             c.Query("status"),
		VerificationStatus: c.Query("verification_status"),
		Continent:          c.Query("continent"),
	}

	data, contentType, err := h.svc.Export(c.UserContext(), tc, filter, format)
	if err != nil {
		return httpx.Internal(err)
	}

	filename := fmt.Sprintf("countries_export_%d.%s", time.Now().Unix(), format)
	c.Set("Content-Type", contentType)
	c.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	return c.Send(data)
}
