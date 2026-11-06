package senderid

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
	g := router.Group("/sender-ids", authMiddleware)
	g.Get("", rbac.RequirePermission("customer.senderid.view"), h.list)
	g.Get("/export", rbac.RequirePermission("customer.senderid.view"), h.export)
	g.Get("/:id", rbac.RequirePermission("customer.senderid.view"), h.get)
	g.Get("/:id/history", rbac.RequirePermission("customer.senderid.view"), h.history)
	g.Post("", rbac.RequirePermission("customer.senderid.create"), h.create)
	g.Put("/:id", rbac.RequirePermission("customer.senderid.edit"), h.update)
	g.Post("/:id/approve", rbac.RequirePermission("customer.senderid.approve"), h.approve)
	g.Post("/:id/reject", rbac.RequirePermission("customer.senderid.approve"), h.reject)
	g.Post("/:id/rollback", rbac.RequirePermission("customer.senderid.edit"), h.rollback)
}

func mapErr(err error) error {
	switch {
	case errors.Is(err, ErrPermissionDenied), errors.Is(err, ErrCrossTenantAccess), errors.Is(err, ErrMakerCannotApproveOwnSenderID):
		return httpx.Forbidden(err.Error())
	case errors.Is(err, ErrSenderIDNotFound):
		return httpx.NotFound(err.Error())
	case errors.Is(err, ErrSenderIDExists):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrValidation), errors.Is(err, ErrNotCustomerOrg), errors.Is(err, ErrRejectReasonRequired):
		return httpx.BadRequest(err.Error())
	default:
		return httpx.Internal(err)
	}
}

func (h *Handler) list(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	page, _ := strconv.Atoi(c.Query("page", "1"))
	limit, _ := strconv.Atoi(c.Query("page_size", c.Query("limit", "25")))
	filter := ListFilter{
		SenderType: c.Query("sender_type"),
		Status:     c.Query("status"),
		Search:     c.Query("search"),
		Page:       page,
		Limit:      limit,
	}
	if v := c.Query("organization_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			filter.OrganizationID = &id
		}
	}
	if v := c.Query("country_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			filter.CountryID = &id
		}
	}
	items, total, err := h.svc.List(c.UserContext(), tc, filter)
	if err != nil {
		return mapErr(err)
	}
	if items == nil {
		items = []SenderID{}
	}
	return httpx.OK(c, fiber.Map{
		"items":       items,
		"total_count": total,
		"page":        page,
		"page_size":   limit,
	})
}

func (h *Handler) get(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid sender ID")
	}
	res, err := h.svc.Get(c.UserContext(), tc, id)
	if err != nil {
		return mapErr(err)
	}
	return httpx.OK(c, res)
}

func (h *Handler) create(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	var req CreateSenderIDRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}
	res, err := h.svc.Create(c.UserContext(), tc, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
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
		return httpx.BadRequest("Invalid sender ID")
	}
	var req UpdateSenderIDRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}
	res, err := h.svc.Update(c.UserContext(), tc, id, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	return httpx.OK(c, res)
}

func (h *Handler) approve(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid sender ID")
	}
	var req ReviewSenderIDRequest
	_ = c.BodyParser(&req)
	res, err := h.svc.Approve(c.UserContext(), tc, id, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	return httpx.OK(c, res)
}

func (h *Handler) reject(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid sender ID")
	}
	var req ReviewSenderIDRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}
	res, err := h.svc.Reject(c.UserContext(), tc, id, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
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
		return httpx.BadRequest("Invalid sender ID")
	}
	items, err := h.svc.ListHistory(c.UserContext(), tc, id)
	if err != nil {
		return mapErr(err)
	}
	if items == nil {
		items = []SenderIDHistory{}
	}
	return httpx.OK(c, items)
}

type rollbackBody struct {
	TargetVersion int    `json:"target_version"`
	Reason        string `json:"reason"`
}

func (h *Handler) rollback(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid sender ID")
	}
	var body rollbackBody
	if err := c.BodyParser(&body); err != nil || body.TargetVersion <= 0 {
		return httpx.BadRequest("target_version is required")
	}
	res, err := h.svc.Rollback(c.UserContext(), tc, id, body.TargetVersion, body.Reason, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	return httpx.OK(c, res)
}

func (h *Handler) export(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	format := c.Query("format", "csv")
	filter := ListFilter{
		SenderType: c.Query("sender_type"),
		Status:     c.Query("status"),
		Search:     c.Query("search"),
	}
	if v := c.Query("organization_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			filter.OrganizationID = &id
		}
	}
	if v := c.Query("country_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			filter.CountryID = &id
		}
	}
	data, contentType, err := h.svc.Export(c.UserContext(), tc, filter, format)
	if err != nil {
		return mapErr(err)
	}
	ext := "csv"
	if format == "json" {
		ext = "json"
	}
	c.Set("Content-Type", contentType)
	c.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="sender_ids_%s.%s"`, time.Now().UTC().Format("20060102"), ext))
	return c.Send(data)
}
