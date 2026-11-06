package operator

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
	group := router.Group("/operators", authMiddleware)
	group.Get("", rbac.RequirePermission("operator.operator.view"), h.list)
	group.Get("/export", rbac.RequirePermission("operator.operator.view"), h.export)
	group.Get("/:id", rbac.RequirePermission("operator.operator.view"), h.get)
	group.Get("/:id/history", rbac.RequirePermission("operator.operator.view"), h.history)
	group.Post("", rbac.RequirePermission("operator.operator.create"), h.create)
	group.Put("/:id", rbac.RequirePermission("operator.operator.edit"), h.update)
	group.Post("/:id/deactivate", rbac.RequirePermission("operator.operator.deactivate"), h.deactivate)
	group.Post("/:id/activate", rbac.RequirePermission("operator.operator.deactivate"), h.activate)
	group.Post("/:id/archive", rbac.RequirePermission("operator.operator.archive"), h.archive)
	group.Post("/:id/rollback", rbac.RequirePermission("operator.operator.edit"), h.rollback)
}

func (h *Handler) list(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "25"))

	var countryID *uuid.UUID
	if cidStr := c.Query("country_id"); cidStr != "" {
		if cid, err := uuid.Parse(cidStr); err == nil {
			countryID = &cid
		}
	}

	filter := Filter{
		Search:             c.Query("search"),
		CountryID:          countryID,
		Status:             c.Query("status"),
		VerificationStatus: c.Query("verification_status"),
		MCC:                c.Query("mcc"),
		MNC:                c.Query("mnc"),
		Technology:         c.Query("technology"),
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
		return httpx.BadRequest("Invalid operator ID")
	}

	res, err := h.svc.Get(c.UserContext(), tc, id)
	if err != nil {
		if errors.Is(err, ErrOperatorNotFound) {
			return httpx.NotFound("Operator not found")
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
		return httpx.BadRequest("Invalid operator ID")
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

	var req CreateOperatorRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}

	res, err := h.svc.Create(c.UserContext(), tc, req)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyMasterData) {
			return httpx.Forbidden(err.Error())
		}
		if errors.Is(err, ErrEmptyOperatorName) || errors.Is(err, ErrInvalidMCCMNC) || errors.Is(err, ErrInvalidCountryID) {
			return httpx.BadRequest(err.Error())
		}
		if errors.Is(err, ErrDuplicateMCCMNC) {
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
		return httpx.BadRequest("Invalid operator ID")
	}

	var req UpdateOperatorRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}

	res, err := h.svc.Update(c.UserContext(), tc, id, req)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyMasterData) {
			return httpx.Forbidden(err.Error())
		}
		if errors.Is(err, ErrOperatorNotFound) {
			return httpx.NotFound("Operator not found")
		}
		if errors.Is(err, ErrEmptyOperatorName) {
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
		return httpx.BadRequest("Invalid operator ID")
	}

	var req ActionReasonRequest
	_ = c.BodyParser(&req)

	res, err := h.svc.SetStatus(c.UserContext(), tc, id, "inactive", req.Reason)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyMasterData) {
			return httpx.Forbidden(err.Error())
		}
		if errors.Is(err, ErrOperatorNotFound) {
			return httpx.NotFound("Operator not found")
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
		return httpx.BadRequest("Invalid operator ID")
	}

	var req ActionReasonRequest
	_ = c.BodyParser(&req)

	res, err := h.svc.SetStatus(c.UserContext(), tc, id, "active", req.Reason)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyMasterData) {
			return httpx.Forbidden(err.Error())
		}
		if errors.Is(err, ErrOperatorNotFound) {
			return httpx.NotFound("Operator not found")
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
		return httpx.BadRequest("Invalid operator ID")
	}

	var req ActionReasonRequest
	_ = c.BodyParser(&req)

	res, err := h.svc.SetStatus(c.UserContext(), tc, id, "archived", req.Reason)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyMasterData) {
			return httpx.Forbidden(err.Error())
		}
		if errors.Is(err, ErrOperatorNotFound) {
			return httpx.NotFound("Operator not found")
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
		return httpx.BadRequest("Invalid operator ID")
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
		if errors.Is(err, ErrOperatorNotFound) {
			return httpx.NotFound("Operator not found")
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

	var countryID *uuid.UUID
	if cidStr := c.Query("country_id"); cidStr != "" {
		if cid, err := uuid.Parse(cidStr); err == nil {
			countryID = &cid
		}
	}

	filter := Filter{
		Search:             c.Query("search"),
		CountryID:          countryID,
		Status:             c.Query("status"),
		VerificationStatus: c.Query("verification_status"),
		MCC:                c.Query("mcc"),
		MNC:                c.Query("mnc"),
		Technology:         c.Query("technology"),
	}

	data, contentType, err := h.svc.Export(c.UserContext(), tc, filter, format)
	if err != nil {
		return httpx.Internal(err)
	}

	filename := fmt.Sprintf("operators_export_%d.%s", time.Now().Unix(), format)
	c.Set("Content-Type", contentType)
	c.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	return c.Send(data)
}
