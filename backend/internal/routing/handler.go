package routing

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

func requirePlatformOrg(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}
	if !tc.IsPlatformOrg() {
		return httpx.Forbidden("routing is platform-only")
	}
	return c.Next()
}

func (h *Handler) Register(router fiber.Router, authMiddleware fiber.Handler) {
	routing := router.Group("/routing", authMiddleware, requirePlatformOrg)
	mutateLimiter := httpx.RateLimiter(60, time.Minute, "Routing mutation rate limit exceeded. Please retry later.")

	// Route Plans
	plans := routing.Group("/plans")
	plans.Get("/export", rbac.RequirePermission("routing.plan.view"), h.ExportPlans)
	plans.Get("/", rbac.RequirePermission("routing.plan.view"), h.ListPlans)
	plans.Post("/", mutateLimiter, rbac.RequirePermission("routing.plan.create"), h.CreatePlan)
	plans.Get("/:id", rbac.RequirePermission("routing.plan.view"), h.GetPlan)
	plans.Put("/:id", mutateLimiter, rbac.RequirePermission("routing.plan.edit"), h.UpdatePlan)
	plans.Post("/:id/activate", mutateLimiter, rbac.RequirePermission("routing.plan.activate"), h.ActivatePlan)
	plans.Post("/:id/deactivate", mutateLimiter, rbac.RequirePermission("routing.plan.deactivate"), h.DeactivatePlan)
	plans.Post("/:id/archive", mutateLimiter, rbac.RequirePermission("routing.plan.archive"), h.ArchivePlan)
	plans.Get("/:id/history", rbac.RequirePermission("routing.plan.view"), h.GetPlanHistory)
	plans.Post("/:id/rollback", mutateLimiter, rbac.RequirePermission("routing.plan.edit"), h.RollbackPlan)

	// Route Plan Entries
	plans.Get("/:id/entries/export", rbac.RequirePermission("routing.entry.view"), h.ExportEntries)
	plans.Get("/:id/entries", rbac.RequirePermission("routing.entry.view"), h.ListEntries)
	plans.Post("/:id/entries", mutateLimiter, rbac.RequirePermission("routing.entry.create"), h.CreateEntry)
	plans.Put("/:id/entries/:eid", mutateLimiter, rbac.RequirePermission("routing.entry.edit"), h.UpdateEntry)
	plans.Delete("/:id/entries/:eid", mutateLimiter, rbac.RequirePermission("routing.entry.edit"), h.DeleteEntry)
	plans.Get("/:id/entries/:eid/history", rbac.RequirePermission("routing.entry.view"), h.GetEntryHistory)
	plans.Post("/:id/entries/:eid/rollback", mutateLimiter, rbac.RequirePermission("routing.entry.edit"), h.RollbackEntry)

	// Customer Route Assignments
	assignments := routing.Group("/assignments")
	assignments.Get("/export", rbac.RequirePermission("routing.assignment.view"), h.ExportAssignments)
	assignments.Get("/", rbac.RequirePermission("routing.assignment.view"), h.ListAssignments)
	assignments.Post("/", mutateLimiter, rbac.RequirePermission("routing.assignment.create"), h.CreateAssignment)
	assignments.Get("/:id/history", rbac.RequirePermission("routing.assignment.view"), h.GetAssignmentHistory)
	assignments.Post("/:id/rollback", mutateLimiter, rbac.RequirePermission("routing.assignment.edit"), h.RollbackAssignment)
	assignments.Put("/:id", mutateLimiter, rbac.RequirePermission("routing.assignment.edit"), h.UpdateAssignment)
	assignments.Delete("/:id", mutateLimiter, rbac.RequirePermission("routing.assignment.edit"), h.DeleteAssignment)

	// Routing Rules
	rules := routing.Group("/rules")
	rules.Get("/export", rbac.RequirePermission("routing.rule.view"), h.ExportRules)
	rules.Get("/", rbac.RequirePermission("routing.rule.view"), h.ListRules)
	rules.Post("/", mutateLimiter, rbac.RequirePermission("routing.rule.create"), h.CreateRule)
	rules.Get("/:id/history", rbac.RequirePermission("routing.rule.view"), h.GetRuleHistory)
	rules.Post("/:id/rollback", mutateLimiter, rbac.RequirePermission("routing.rule.edit"), h.RollbackRule)
	rules.Put("/:id", mutateLimiter, rbac.RequirePermission("routing.rule.edit"), h.UpdateRule)
	rules.Delete("/:id", mutateLimiter, rbac.RequirePermission("routing.rule.edit"), h.DeleteRule)

	// Simulator & Kill Switch & Status
	routing.Post("/simulate", mutateLimiter, rbac.RequirePermission("routing.simulate"), h.Simulate)
	routing.Get("/connections/overrides", rbac.RequirePermission("routing.killswitch"), h.ListOverrides)
	routing.Post("/connections/:id/disable", mutateLimiter, rbac.RequirePermission("routing.killswitch"), h.DisableConnection)
	routing.Post("/connections/:id/enable", mutateLimiter, rbac.RequirePermission("routing.killswitch"), h.EnableConnection)
	routing.Post("/kill-switch/disable", mutateLimiter, rbac.RequirePermission("routing.killswitch"), h.DisableConnection)
	routing.Post("/kill-switch/reenable", mutateLimiter, rbac.RequirePermission("routing.killswitch"), h.EnableConnection)

	routing.Get("/snapshot-status", rbac.RequirePermission("routing.snapshot.view"), h.SnapshotStatus)
	routing.Get("/snapshot/status", rbac.RequirePermission("routing.snapshot.view"), h.SnapshotStatus)
	routing.Post("/snapshot/reload", mutateLimiter, rbac.RequirePermission("routing.plan.edit"), h.ReloadSnapshot)
	routing.Get("/coverage-report", rbac.RequirePermission("routing.snapshot.view"), h.CoverageReport)
	routing.Get("/coverage-report/export", rbac.RequirePermission("routing.snapshot.view"), h.ExportCoverageReport)
}

func (h *Handler) mapError(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return httpx.NotFound(err.Error())
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrUnauthorized):
		return httpx.Forbidden(err.Error())
	case errors.Is(err, ErrConflict):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrValidation), errors.Is(err, ErrInvalidStatus),
		errors.Is(err, ErrMissingReason), errors.Is(err, ErrInactiveConnection),
		errors.Is(err, ErrWeightSumMismatch), errors.Is(err, ErrVersionMismatch):
		return httpx.BadRequest(err.Error())
	default:
		return httpx.Internal(err)
	}
}

// -----------------------------------------------------------------------------
// Route Plans Handlers
// -----------------------------------------------------------------------------

func (h *Handler) ListPlans(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "20"))
	status := c.Query("status")

	plans, total, err := h.svc.ListPlans(c.UserContext(), tc, status, page, pageSize)
	if err != nil {
		return h.mapError(err)
	}

	return httpx.OK(c, fiber.Map{
		"items":       plans,
		"total_count": total,
		"page":        page,
		"page_size":   pageSize,
	})
}

func (h *Handler) CreatePlan(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	var req RoutePlan
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("invalid request payload")
	}

	created, err := h.svc.CreatePlan(c.UserContext(), tc, &req)
	if err != nil {
		return h.mapError(err)
	}
	return httpx.JSON(c, fiber.StatusCreated, created)
}

func (h *Handler) GetPlan(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid plan ID")
	}

	plan, err := h.svc.GetPlan(c.UserContext(), tc, id)
	if err != nil {
		return h.mapError(err)
	}
	return httpx.OK(c, plan)
}

func (h *Handler) UpdatePlan(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid plan ID")
	}

	var req RoutePlan
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("invalid request payload")
	}
	req.ID = id

	updated, err := h.svc.UpdatePlan(c.UserContext(), tc, &req)
	if err != nil {
		return h.mapError(err)
	}
	return httpx.OK(c, updated)
}

func (h *Handler) ActivatePlan(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid plan ID")
	}

	plan, cr, err := h.svc.RequestPlanActivation(c.UserContext(), tc, id)
	if err != nil {
		return h.mapError(err)
	}

	if cr != nil {
		return httpx.OK(c, fiber.Map{
			"status":         "pending_approval",
			"change_request": cr,
			"plan":           plan,
		})
	}
	return httpx.OK(c, fiber.Map{
		"status": "active",
		"plan":   plan,
	})
}

func (h *Handler) DeactivatePlan(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid plan ID")
	}

	plan, cr, err := h.svc.RequestPlanDeactivation(c.UserContext(), tc, id)
	if err != nil {
		return h.mapError(err)
	}

	if cr != nil {
		return httpx.OK(c, fiber.Map{
			"status":         "pending_approval",
			"change_request": cr,
			"plan":           plan,
		})
	}
	return httpx.OK(c, fiber.Map{
		"status": "deactivated",
		"plan":   plan,
	})
}

func (h *Handler) ArchivePlan(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid plan ID")
	}

	if err := h.svc.ArchivePlan(c.UserContext(), tc, id); err != nil {
		return h.mapError(err)
	}
	return httpx.OK(c, fiber.Map{"archived": true})
}

func (h *Handler) GetPlanHistory(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid plan ID")
	}

	history, err := h.svc.GetPlanHistory(c.UserContext(), tc, id)
	if err != nil {
		return h.mapError(err)
	}
	return httpx.OK(c, history)
}

func (h *Handler) RollbackPlan(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid plan ID")
	}

	var req struct {
		HistoryID uuid.UUID `json:"history_id"`
	}
	if err := c.BodyParser(&req); err != nil || req.HistoryID == uuid.Nil {
		return httpx.BadRequest("history_id is required")
	}

	rolledBack, err := h.svc.RollbackPlan(c.UserContext(), tc, id, req.HistoryID)
	if err != nil {
		return h.mapError(err)
	}
	return httpx.OK(c, rolledBack)
}

// -----------------------------------------------------------------------------
// Route Plan Entries Handlers
// -----------------------------------------------------------------------------

func (h *Handler) ListEntries(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid plan ID")
	}

	entries, err := h.svc.ListEntries(c.UserContext(), tc, id)
	if err != nil {
		return h.mapError(err)
	}
	return httpx.OK(c, entries)
}

func (h *Handler) CreateEntry(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	planID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid plan ID")
	}

	var entry RoutePlanEntry
	if err := c.BodyParser(&entry); err != nil {
		return httpx.BadRequest("invalid request payload")
	}
	entry.PlanID = planID

	created, cr, err := h.svc.CreateEntry(c.UserContext(), tc, &entry)
	if err != nil {
		return h.mapError(err)
	}
	if cr != nil {
		return httpx.OK(c, fiber.Map{
			"status":         "pending_approval",
			"change_request": cr,
		})
	}
	return httpx.JSON(c, fiber.StatusCreated, created)
}

func (h *Handler) UpdateEntry(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	planID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid plan ID")
	}
	eid, err := uuid.Parse(c.Params("eid"))
	if err != nil {
		return httpx.BadRequest("invalid entry ID")
	}

	var entry RoutePlanEntry
	if err := c.BodyParser(&entry); err != nil {
		return httpx.BadRequest("invalid request payload")
	}
	entry.ID = eid
	entry.PlanID = planID

	updated, cr, err := h.svc.UpdateEntry(c.UserContext(), tc, &entry)
	if err != nil {
		return h.mapError(err)
	}
	if cr != nil {
		return httpx.OK(c, fiber.Map{
			"status":         "pending_approval",
			"change_request": cr,
		})
	}
	return httpx.OK(c, updated)
}

func (h *Handler) DeleteEntry(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	eid, err := uuid.Parse(c.Params("eid"))
	if err != nil {
		return httpx.BadRequest("invalid entry ID")
	}

	cr, err := h.svc.DeleteEntry(c.UserContext(), tc, eid)
	if err != nil {
		return h.mapError(err)
	}
	if cr != nil {
		return httpx.OK(c, fiber.Map{
			"status":         "pending_approval",
			"change_request": cr,
		})
	}
	return httpx.OK(c, fiber.Map{"deleted": true})
}

func (h *Handler) GetEntryHistory(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	eid, err := uuid.Parse(c.Params("eid"))
	if err != nil {
		return httpx.BadRequest("invalid entry ID")
	}

	history, err := h.svc.GetEntryHistory(c.UserContext(), tc, eid)
	if err != nil {
		return h.mapError(err)
	}
	return httpx.OK(c, history)
}

func (h *Handler) RollbackEntry(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	eid, err := uuid.Parse(c.Params("eid"))
	if err != nil {
		return httpx.BadRequest("invalid entry ID")
	}

	var req struct {
		HistoryID uuid.UUID `json:"history_id"`
	}
	if err := c.BodyParser(&req); err != nil || req.HistoryID == uuid.Nil {
		return httpx.BadRequest("history_id is required")
	}

	rolledBack, err := h.svc.RollbackEntry(c.UserContext(), tc, eid, req.HistoryID)
	if err != nil {
		return h.mapError(err)
	}
	return httpx.OK(c, rolledBack)
}

// -----------------------------------------------------------------------------
// Customer Route Assignments Handlers
// -----------------------------------------------------------------------------

func (h *Handler) ListAssignments(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "20"))
	var custID *uuid.UUID
	if cidStr := c.Query("customer_org_id"); cidStr != "" {
		if id, err := uuid.Parse(cidStr); err == nil {
			custID = &id
		}
	}

	items, total, err := h.svc.ListAssignments(c.UserContext(), tc, custID, page, pageSize)
	if err != nil {
		return h.mapError(err)
	}

	return httpx.OK(c, fiber.Map{
		"items":       items,
		"total_count": total,
		"page":        page,
		"page_size":   pageSize,
	})
}

func (h *Handler) CreateAssignment(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	var a CustomerRouteAssignment
	if err := c.BodyParser(&a); err != nil {
		return httpx.BadRequest("invalid request payload")
	}

	created, cr, err := h.svc.CreateAssignment(c.UserContext(), tc, &a)
	if err != nil {
		return h.mapError(err)
	}
	if cr != nil {
		return httpx.OK(c, fiber.Map{
			"status":         "pending_approval",
			"change_request": cr,
		})
	}
	return httpx.JSON(c, fiber.StatusCreated, created)
}

func (h *Handler) UpdateAssignment(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid assignment ID")
	}

	var a CustomerRouteAssignment
	if err := c.BodyParser(&a); err != nil {
		return httpx.BadRequest("invalid request payload")
	}
	a.ID = id

	updated, cr, err := h.svc.UpdateAssignment(c.UserContext(), tc, &a)
	if err != nil {
		return h.mapError(err)
	}
	if cr != nil {
		return httpx.OK(c, fiber.Map{
			"status":         "pending_approval",
			"change_request": cr,
		})
	}
	return httpx.OK(c, updated)
}

func (h *Handler) DeleteAssignment(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid assignment ID")
	}

	cr, err := h.svc.DeleteAssignment(c.UserContext(), tc, id)
	if err != nil {
		return h.mapError(err)
	}
	if cr != nil {
		return httpx.OK(c, fiber.Map{
			"status":         "pending_approval",
			"change_request": cr,
		})
	}
	return httpx.OK(c, fiber.Map{"deleted": true})
}

// -----------------------------------------------------------------------------
// Routing Rules Handlers
// -----------------------------------------------------------------------------

func (h *Handler) ListRules(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "20"))
	status := c.Query("status")

	rules, total, err := h.svc.ListRules(c.UserContext(), tc, status, page, pageSize)
	if err != nil {
		return h.mapError(err)
	}

	return httpx.OK(c, fiber.Map{
		"items":       rules,
		"total_count": total,
		"page":        page,
		"page_size":   pageSize,
	})
}

func (h *Handler) CreateRule(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	var r RoutingRule
	if err := c.BodyParser(&r); err != nil {
		return httpx.BadRequest("invalid request payload")
	}

	created, cr, err := h.svc.CreateRule(c.UserContext(), tc, &r)
	if err != nil {
		return h.mapError(err)
	}
	if cr != nil {
		return httpx.OK(c, fiber.Map{
			"status":         "pending_approval",
			"change_request": cr,
		})
	}
	return httpx.JSON(c, fiber.StatusCreated, created)
}

func (h *Handler) UpdateRule(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid rule ID")
	}

	var r RoutingRule
	if err := c.BodyParser(&r); err != nil {
		return httpx.BadRequest("invalid request payload")
	}
	r.ID = id

	updated, cr, err := h.svc.UpdateRule(c.UserContext(), tc, &r)
	if err != nil {
		return h.mapError(err)
	}
	if cr != nil {
		return httpx.OK(c, fiber.Map{
			"status":         "pending_approval",
			"change_request": cr,
		})
	}
	return httpx.OK(c, updated)
}

func (h *Handler) DeleteRule(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid rule ID")
	}

	cr, err := h.svc.DeleteRule(c.UserContext(), tc, id)
	if err != nil {
		return h.mapError(err)
	}
	if cr != nil {
		return httpx.OK(c, fiber.Map{
			"status":         "pending_approval",
			"change_request": cr,
		})
	}
	return httpx.OK(c, fiber.Map{"deleted": true})
}

// -----------------------------------------------------------------------------
// Simulator, Kill Switch & Snapshot Status Handlers
// -----------------------------------------------------------------------------

func (h *Handler) Simulate(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	var req SimulateRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("invalid request payload")
	}

	result, err := h.svc.Simulate(c.UserContext(), tc, req)
	if err != nil {
		return h.mapError(err)
	}
	return httpx.OK(c, result)
}

func (h *Handler) ListOverrides(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	overrides, err := h.svc.ListOverrides(c.UserContext(), tc)
	if err != nil {
		return h.mapError(err)
	}
	return httpx.OK(c, overrides)
}

func (h *Handler) DisableConnection(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	var req struct {
		ConnectionID uuid.UUID `json:"connection_id"`
		Reason       string    `json:"reason"`
	}
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("invalid payload")
	}

	id := req.ConnectionID
	if idParam := c.Params("id"); idParam != "" {
		parsed, err := uuid.Parse(idParam)
		if err != nil {
			return httpx.BadRequest("invalid connection ID")
		}
		id = parsed
	}
	if id == uuid.Nil {
		return httpx.BadRequest("connection ID is required")
	}

	override, err := h.svc.DisableConnection(c.UserContext(), tc, id, req.Reason)
	if err != nil {
		return h.mapError(err)
	}
	return httpx.OK(c, override)
}

func (h *Handler) EnableConnection(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	var req struct {
		ConnectionID uuid.UUID `json:"connection_id"`
		Reason       string    `json:"reason"`
	}
	_ = c.BodyParser(&req)

	id := req.ConnectionID
	if idParam := c.Params("id"); idParam != "" {
		parsed, err := uuid.Parse(idParam)
		if err != nil {
			return httpx.BadRequest("invalid connection ID")
		}
		id = parsed
	}
	if id == uuid.Nil {
		return httpx.BadRequest("connection ID is required")
	}

	cr, err := h.svc.EnableConnection(c.UserContext(), tc, id, req.Reason)
	if err != nil {
		return h.mapError(err)
	}
	return httpx.OK(c, fiber.Map{
		"status":         "pending_approval",
		"change_request": cr,
	})
}

func (h *Handler) SnapshotStatus(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	status, err := h.svc.SnapshotStatus(c.UserContext(), tc)
	if err != nil {
		return h.mapError(err)
	}
	return httpx.OK(c, status)
}

func (h *Handler) CoverageReport(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	report, err := h.svc.CoverageReport(c.UserContext(), tc)
	if err != nil {
		return h.mapError(err)
	}
	return httpx.OK(c, report)
}

func (h *Handler) ExportPlans(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	format := c.Query("format", "csv")
	if format == "json" {
		plans, _, err := h.svc.ListPlans(c.UserContext(), tc, "", 1, 10000)
		if err != nil {
			return h.mapError(err)
		}
		return httpx.OK(c, plans)
	}

	data, err := h.svc.ExportPlansCSV(c.UserContext(), tc)
	if err != nil {
		return h.mapError(err)
	}
	c.Set("Content-Type", "text/csv")
	c.Set("Content-Disposition", "attachment; filename=\"route-plans.csv\"")
	return c.Send(data)
}

func (h *Handler) ExportEntries(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	planID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid plan ID")
	}

	format := c.Query("format", "csv")
	if format == "json" {
		entries, err := h.svc.ListEntries(c.UserContext(), tc, planID)
		if err != nil {
			return h.mapError(err)
		}
		return httpx.OK(c, entries)
	}

	data, err := h.svc.ExportEntriesCSV(c.UserContext(), tc, planID)
	if err != nil {
		return h.mapError(err)
	}
	c.Set("Content-Type", "text/csv")
	c.Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"plan-%s-entries.csv\"", planID))
	return c.Send(data)
}

func (h *Handler) ExportAssignments(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	format := c.Query("format", "csv")
	if format == "json" {
		assignments, _, err := h.svc.ListAssignments(c.UserContext(), tc, nil, 1, 10000)
		if err != nil {
			return h.mapError(err)
		}
		return httpx.OK(c, assignments)
	}

	data, err := h.svc.ExportAssignmentsCSV(c.UserContext(), tc)
	if err != nil {
		return h.mapError(err)
	}
	c.Set("Content-Type", "text/csv")
	c.Set("Content-Disposition", "attachment; filename=\"customer-assignments.csv\"")
	return c.Send(data)
}

func (h *Handler) ExportRules(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	format := c.Query("format", "csv")
	if format == "json" {
		rules, _, err := h.svc.ListRules(c.UserContext(), tc, "", 1, 10000)
		if err != nil {
			return h.mapError(err)
		}
		return httpx.OK(c, rules)
	}

	data, err := h.svc.ExportRulesCSV(c.UserContext(), tc)
	if err != nil {
		return h.mapError(err)
	}
	c.Set("Content-Type", "text/csv")
	c.Set("Content-Disposition", "attachment; filename=\"routing-rules.csv\"")
	return c.Send(data)
}

func (h *Handler) ExportCoverageReport(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	report, err := h.svc.CoverageReport(c.UserContext(), tc)
	if err != nil {
		return h.mapError(err)
	}
	return httpx.OK(c, report)
}

func (h *Handler) GetAssignmentHistory(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid assignment ID")
	}

	history, err := h.svc.GetAssignmentHistory(c.UserContext(), tc, id)
	if err != nil {
		return h.mapError(err)
	}
	return httpx.OK(c, history)
}

func (h *Handler) RollbackAssignment(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid assignment ID")
	}

	var req struct {
		HistoryID uuid.UUID `json:"history_id"`
	}
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("invalid request payload")
	}

	rolledBack, err := h.svc.RollbackAssignment(c.UserContext(), tc, id, req.HistoryID)
	if err != nil {
		return h.mapError(err)
	}
	return httpx.OK(c, rolledBack)
}

func (h *Handler) GetRuleHistory(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid rule ID")
	}

	history, err := h.svc.GetRuleHistory(c.UserContext(), tc, id)
	if err != nil {
		return h.mapError(err)
	}
	return httpx.OK(c, history)
}

func (h *Handler) RollbackRule(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid rule ID")
	}

	var req struct {
		HistoryID uuid.UUID `json:"history_id"`
	}
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("invalid request payload")
	}

	rolledBack, err := h.svc.RollbackRule(c.UserContext(), tc, id, req.HistoryID)
	if err != nil {
		return h.mapError(err)
	}
	return httpx.OK(c, rolledBack)
}

func (h *Handler) ReloadSnapshot(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}
	if !tc.IsPlatformOrg() {
		return httpx.Forbidden("platform-only")
	}
	snap, err := Reload(c.UserContext(), h.svc.pool)
	if err != nil {
		return h.mapError(err)
	}
	return httpx.OK(c, fiber.Map{
		"status":   "reloaded",
		"built_at": snap.BuildAt,
		"plans":    len(snap.Plans),
		"rules":    len(snap.Rules),
	})
}
