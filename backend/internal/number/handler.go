package number

import (
	"errors"
	"strconv"
	"strings"

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
	group := router.Group("/number", authMiddleware)
	group.Get("/lookup", rbac.RequirePermission("number.lookup.use"), h.lookup)
	group.Post("/lookup", rbac.RequirePermission("number.lookup.use"), h.lookup)
	group.Post("/lookup/bulk", rbac.RequirePermission("number.lookup.use"), h.bulkLookup)

	// Direct routes for /lookup and /lookup/bulk
	router.Get("/lookup", authMiddleware, rbac.RequirePermission("number.lookup.use"), h.lookup)
	router.Post("/lookup", authMiddleware, rbac.RequirePermission("number.lookup.use"), h.lookup)
	router.Post("/lookup/bulk", authMiddleware, rbac.RequirePermission("number.lookup.use"), h.bulkLookup)

	// Transform Rules on both /number/rules and /transform-rules
	for _, p := range []string{"/number/rules", "/transform-rules"} {
		rg := router.Group(p, authMiddleware)
		rg.Get("", rbac.RequirePermission("number.rule.view"), h.listRules)
		rg.Get("/:id", rbac.RequirePermission("number.rule.view"), h.getRule)
		rg.Post("", rbac.RequirePermission("number.rule.create"), h.createRule)
		rg.Put("/:id", rbac.RequirePermission("number.rule.edit"), h.updateRule)
		rg.Delete("/:id", rbac.RequirePermission("number.rule.delete"), h.deleteRule)
		rg.Post("/preview", rbac.RequirePermission("number.rule.view"), h.previewRule)
		rg.Post("/:id/preview", rbac.RequirePermission("number.rule.view"), h.previewRule)
		rg.Post("/test-chain", rbac.RequirePermission("number.rule.view"), h.testChain)
	}
}

func (h *Handler) lookup(c *fiber.Ctx) error {
	var req LookupRequest
	if c.Method() == fiber.MethodGet {
		req.Number = c.Query("number")
		req.Direction = c.Query("direction")
	} else {
		_ = c.BodyParser(&req)
		if req.Number == "" {
			req.Number = c.Query("number")
		}
		if req.Direction == "" {
			req.Direction = c.Query("direction")
		}
	}
	if strings.TrimSpace(req.Number) == "" {
		return httpx.BadRequest("number field is required")
	}

	result := h.svc.Lookup(c.UserContext(), req)
	return httpx.OK(c, result)
}

func (h *Handler) bulkLookup(c *fiber.Ctx) error {
	var req BulkLookupRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid request body")
	}
	if len(req.Numbers) == 0 {
		return httpx.BadRequest("numbers array is required and must not be empty")
	}

	result, err := h.svc.BulkLookup(c.UserContext(), req)
	if err != nil {
		if errors.Is(err, ErrTooManyBulkNumbers) {
			return httpx.BadRequest(err.Error())
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, result)
}

func (h *Handler) listRules(c *fiber.Ctx) error {
	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "25"))

	var countryID *uuid.UUID
	if cidStr := c.Query("country_id"); cidStr != "" {
		if cid, err := uuid.Parse(cidStr); err == nil {
			countryID = &cid
		}
	}

	filter := RuleFilter{
		Scope:     c.Query("scope"),
		Status:    c.Query("status"),
		CountryID: countryID,
		Page:      page,
		PageSize:  pageSize,
	}

	res, err := h.svc.ListRules(c.UserContext(), filter)
	if err != nil {
		return httpx.Internal(err)
	}
	return httpx.OK(c, res)
}

func (h *Handler) getRule(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid rule ID")
	}

	rule, err := h.svc.GetRule(c.UserContext(), id)
	if err != nil {
		if errors.Is(err, ErrRuleNotFound) {
			return httpx.NotFound("Transformation rule not found")
		}
		return httpx.Internal(err)
	}
	return httpx.OK(c, rule)
}

func (h *Handler) createRule(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	var req CreateTransformRuleRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid request body")
	}

	created, err := h.svc.CreateRule(c.UserContext(), tc, req)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyRuleManage) {
			return httpx.Forbidden(err.Error())
		}
		return httpx.BadRequest(err.Error())
	}

	return httpx.OK(c, created)
}

func (h *Handler) updateRule(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid rule ID")
	}

	var req UpdateTransformRuleRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid request body")
	}

	updated, err := h.svc.UpdateRule(c.UserContext(), tc, id, req)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyRuleManage) {
			return httpx.Forbidden(err.Error())
		}
		if errors.Is(err, ErrRuleNotFound) {
			return httpx.NotFound("Transformation rule not found")
		}
		return httpx.BadRequest(err.Error())
	}

	return httpx.OK(c, updated)
}

func (h *Handler) deleteRule(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid rule ID")
	}

	reason := c.Query("reason", "Deactivated by administrator")
	if err := h.svc.DeleteRule(c.UserContext(), tc, id, reason); err != nil {
		if errors.Is(err, ErrPlatformOnlyRuleManage) {
			return httpx.Forbidden(err.Error())
		}
		if errors.Is(err, ErrRuleNotFound) {
			return httpx.NotFound("Transformation rule not found")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{"message": "Transformation rule deactivated successfully"})
}

func (h *Handler) previewRule(c *fiber.Ctx) error {
	var req RulePreviewRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid request body")
	}

	res := h.svc.PreviewRule(req)
	return httpx.OK(c, res)
}

func (h *Handler) testChain(c *fiber.Ctx) error {
	var req ChainTestRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid request body")
	}
	if strings.TrimSpace(req.Number) == "" {
		return httpx.BadRequest("number is required")
	}

	res, err := h.svc.TestChain(c.UserContext(), req)
	if err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, res)
}
