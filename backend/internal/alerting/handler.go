package alerting

import (
	"errors"
	"net/http"
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
	alertsGroup := router.Group("/alerts", authMiddleware)
	alertsGroup.Get("/rules", rbac.RequirePermission("alert.view"), h.listRules)
	alertsGroup.Post("/rules", rbac.RequirePermission("alert.manage"), h.createRule)
	alertsGroup.Put("/rules/:id", rbac.RequirePermission("alert.manage"), h.updateRule)
	alertsGroup.Delete("/rules/:id", rbac.RequirePermission("alert.manage"), h.deleteRule)
	alertsGroup.Get("/", rbac.RequirePermission("alert.view"), h.listAlerts)
	alertsGroup.Post("/:id/silence", rbac.RequirePermission("alert.manage"), h.silenceAlert)
	alertsGroup.Post("/evaluate", rbac.RequirePermission("alert.manage"), h.evaluateRules)

	incidentsGroup := router.Group("/incidents", authMiddleware)
	incidentsGroup.Get("/", rbac.RequirePermission("incident.view"), h.listIncidents)
	incidentsGroup.Post("/", rbac.RequirePermission("incident.manage"), h.declareIncident)
	incidentsGroup.Get("/:id", rbac.RequirePermission("incident.view"), h.getIncident)
	incidentsGroup.Put("/:id/status", rbac.RequirePermission("incident.manage"), h.transitionIncident)
	incidentsGroup.Post("/:id/timeline", rbac.RequirePermission("incident.manage"), h.addTimelineEntry)
}

func (h *Handler) listRules(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	rules, err := h.svc.ListRules(c.UserContext(), tc)
	if err != nil {
		if errors.Is(err, ErrUnauthorizedTenant) {
			return httpx.Forbidden("Access restricted to platform administrators")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{"rules": rules})
}

func (h *Handler) createRule(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	var req CreateRuleRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid request body")
	}
	if req.Name == "" || req.ConditionType == "" {
		return httpx.BadRequest("Name and condition_type are required")
	}

	rule, err := h.svc.CreateRule(c.UserContext(), tc, req)
	if err != nil {
		if errors.Is(err, ErrUnauthorizedTenant) {
			return httpx.Forbidden("Access restricted to platform administrators")
		}
		return httpx.Internal(err)
	}

	return httpx.JSON(c, http.StatusCreated, rule)
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

	var req CreateRuleRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid request body")
	}

	rule, err := h.svc.UpdateRule(c.UserContext(), tc, id, req)
	if err != nil {
		if errors.Is(err, ErrRuleNotFound) {
			return httpx.NotFound("Alert rule not found")
		}
		if errors.Is(err, ErrUnauthorizedTenant) {
			return httpx.Forbidden("Access restricted to platform administrators")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, rule)
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

	if err := h.svc.DeleteRule(c.UserContext(), tc, id); err != nil {
		if errors.Is(err, ErrRuleNotFound) {
			return httpx.NotFound("Alert rule not found")
		}
		if errors.Is(err, ErrUnauthorizedTenant) {
			return httpx.Forbidden("Access restricted to platform administrators")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{"status": "deleted"})
}

func (h *Handler) listAlerts(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	status := c.Query("status")
	alerts, err := h.svc.ListAlerts(c.UserContext(), tc, status)
	if err != nil {
		if errors.Is(err, ErrUnauthorizedTenant) {
			return httpx.Forbidden("Access restricted to platform administrators")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{"alerts": alerts})
}

func (h *Handler) silenceAlert(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid alert ID")
	}

	var req SilenceAlertRequest
	_ = c.BodyParser(&req)

	dur := time.Duration(req.DurationSeconds) * time.Second
	if dur <= 0 {
		dur = 1 * time.Hour
	}

	if err := h.svc.SilenceAlert(c.UserContext(), tc, id, dur, req.Reason); err != nil {
		if errors.Is(err, ErrAlertNotFound) {
			return httpx.NotFound("Firing alert not found")
		}
		if errors.Is(err, ErrUnauthorizedTenant) {
			return httpx.Forbidden("Access restricted to platform administrators")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{"status": "silenced"})
}

func (h *Handler) evaluateRules(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	if !tc.IsPlatformAdmin {
		return httpx.Forbidden("Access restricted to platform administrators")
	}

	alerts, err := h.svc.EvaluateRules(c.UserContext())
	if err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{
		"evaluated_alerts": alerts,
		"count":            len(alerts),
	})
}

// ==================== Incident Handlers ====================

func (h *Handler) listIncidents(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	status := c.Query("status")
	severity := c.Query("severity")
	limit, _ := strconv.Atoi(c.Query("limit", "50"))
	offset, _ := strconv.Atoi(c.Query("offset", "0"))

	items, total, err := h.svc.ListIncidents(c.UserContext(), tc, status, severity, limit, offset)
	if err != nil {
		if errors.Is(err, ErrUnauthorizedTenant) {
			return httpx.Forbidden("Access restricted to platform administrators")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{
		"items": items,
		"total": total,
	})
}

func (h *Handler) declareIncident(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	var req DeclareIncidentRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid request body")
	}
	if req.Title == "" {
		return httpx.BadRequest("Incident title is required")
	}

	inc, err := h.svc.DeclareIncident(c.UserContext(), tc, req)
	if err != nil {
		if errors.Is(err, ErrUnauthorizedTenant) {
			return httpx.Forbidden("Access restricted to platform administrators")
		}
		return httpx.Internal(err)
	}

	return httpx.JSON(c, http.StatusCreated, inc)
}

func (h *Handler) getIncident(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid incident ID")
	}

	detail, err := h.svc.GetIncident(c.UserContext(), tc, id)
	if err != nil {
		if errors.Is(err, ErrIncidentNotFound) {
			return httpx.NotFound("Incident not found")
		}
		if errors.Is(err, ErrUnauthorizedTenant) {
			return httpx.Forbidden("Access restricted to platform administrators")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, detail)
}

func (h *Handler) transitionIncident(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid incident ID")
	}

	var req TransitionIncidentRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid request body")
	}
	if req.Status == "" {
		return httpx.BadRequest("Status is required")
	}

	if err := h.svc.TransitionIncident(c.UserContext(), tc, id, req.Status, req.Note, req.RootCause); err != nil {
		if errors.Is(err, ErrIncidentNotFound) {
			return httpx.NotFound("Incident not found")
		}
		if errors.Is(err, ErrInvalidTransition) {
			return httpx.BadRequest(err.Error())
		}
		if errors.Is(err, ErrUnauthorizedTenant) {
			return httpx.Forbidden("Access restricted to platform administrators")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{"status": "updated"})
}

func (h *Handler) addTimelineEntry(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid incident ID")
	}

	var req AddTimelineRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid request body")
	}
	if req.Content == "" {
		return httpx.BadRequest("Timeline content is required")
	}

	if err := h.svc.AddTimelineEntry(c.UserContext(), tc, id, req.EntryType, req.Content); err != nil {
		if errors.Is(err, ErrIncidentNotFound) {
			return httpx.NotFound("Incident not found")
		}
		if errors.Is(err, ErrUnauthorizedTenant) {
			return httpx.Forbidden("Access restricted to platform administrators")
		}
		return httpx.Internal(err)
	}

	return httpx.JSON(c, http.StatusCreated, fiber.Map{"status": "timeline_entry_added"})
}
