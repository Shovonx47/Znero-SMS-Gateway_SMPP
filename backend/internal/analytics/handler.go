package analytics

import (
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/rbac"
	"github.com/znero-international/znero/backend/pkg/httpx"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

type Handler struct {
	svc *Service
	hub *LiveHub
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) SetLiveHub(hub *LiveHub) {
	h.hub = hub
}

func (h *Handler) Register(router fiber.Router, authMiddleware fiber.Handler) {
	group := router.Group("/analytics", authMiddleware)

	group.Get("/dashboard/executive", rbac.RequirePermission("analytics.dashboard.view"), h.getExecutiveDashboard)
	group.Get("/dashboard/noc", rbac.RequirePermission("analytics.noc.view"), h.getNOCDashboard)
	group.Get("/dashboard/reseller", rbac.RequirePermission("analytics.dashboard.view"), h.getResellerDashboard)
	group.Get("/dashboard/customer", rbac.RequirePermission("analytics.dashboard.view"), h.getCustomerDashboard)
	group.Get("/noc/live", rbac.RequirePermission("analytics.noc.view"), h.getLiveNOC)
	group.Get("/live/ws", h.handleWebSocket)
	group.Get("/reports/delivery", rbac.RequirePermission("analytics.report.view"), h.getDeliveryReport)
	group.Get("/reports/financial", rbac.RequirePermission("analytics.report.view"), h.getFinancialReport)
	group.Get("/widgets", rbac.RequirePermission("analytics.dashboard.view"), h.getWidgets)
	group.Get("/preferences/layout", rbac.RequirePermission("analytics.dashboard.view"), h.getDashboardLayout)
	group.Put("/preferences/layout", rbac.RequirePermission("analytics.dashboard.view"), h.saveDashboardLayout)
	group.Get("/heartbeats", rbac.RequirePermission("system.info.view"), h.listHeartbeats)
	group.Post("/rebuild", rbac.RequirePermission("setting.manage"), h.rebuildRollups)

	// Global /ws/live unversioned alias
	router.Get("/ws/live", h.handleWebSocket)
}

func parseFilter(c *fiber.Ctx) DashboardFilter {
	var filter DashboardFilter

	if s := c.Query("start"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			filter.StartTime = t
		}
	}
	if e := c.Query("end"); e != "" {
		if t, err := time.Parse(time.RFC3339, e); err == nil {
			filter.EndTime = t
		}
	}
	if cust := c.Query("customer_id"); cust != "" {
		if id, err := uuid.Parse(cust); err == nil {
			filter.CustomerID = &id
		}
	}
	filter.Timezone = c.Query("tz", "UTC")
	filter.Granularity = c.Query("granularity", "")
	filter.Currency = c.Query("currency", "")
	filter.CountryISO = c.Query("country_iso", "")

	return filter
}

func mapAnalyticsError(err error) error {
	switch {
	case errors.Is(err, ErrUnauthorizedScope):
		return httpx.Forbidden("Access denied: requested analytics scope exceeds tenant boundary")
	case errors.Is(err, ErrPlatformOnly):
		return httpx.Forbidden("Access denied: this resource is restricted to platform administrators")
	default:
		return httpx.Internal(err)
	}
}

func (h *Handler) handleWebSocket(c *fiber.Ctx) error {
	if h.hub == nil {
		return c.Status(fiber.StatusServiceUnavailable).SendString("Live hub not active")
	}
	return h.hub.HandleWebSocket(c)
}

func (h *Handler) getExecutiveDashboard(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	filter := parseFilter(c)
	data, err := h.svc.GetExecutiveDashboard(c.UserContext(), filter, tc)
	if err != nil {
		return mapAnalyticsError(err)
	}
	return httpx.OK(c, data)
}

func (h *Handler) getNOCDashboard(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	filter := parseFilter(c)
	data, err := h.svc.GetNOCDashboard(c.UserContext(), filter, tc)
	if err != nil {
		return mapAnalyticsError(err)
	}
	return httpx.OK(c, data)
}

func (h *Handler) getLiveNOC(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	data, err := h.svc.GetLiveNOC(c.UserContext(), tc)
	if err != nil {
		return mapAnalyticsError(err)
	}
	return httpx.OK(c, data)
}

func (h *Handler) getResellerDashboard(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	filter := parseFilter(c)
	data, err := h.svc.GetResellerDashboard(c.UserContext(), filter, tc)
	if err != nil {
		return mapAnalyticsError(err)
	}
	return httpx.OK(c, data)
}

func (h *Handler) getCustomerDashboard(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	filter := parseFilter(c)
	data, err := h.svc.GetCustomerDashboard(c.UserContext(), filter, tc)
	if err != nil {
		return mapAnalyticsError(err)
	}
	return httpx.OK(c, data)
}

func (h *Handler) listHeartbeats(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	hbs, err := h.svc.ListHeartbeats(c.UserContext(), tc)
	if err != nil {
		return mapAnalyticsError(err)
	}
	return httpx.OK(c, fiber.Map{"items": hbs})
}

type RebuildRequest struct {
	FromTime time.Time `json:"from_time"`
}

func (h *Handler) rebuildRollups(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	var req RebuildRequest
	if err := c.BodyParser(&req); err != nil || req.FromTime.IsZero() {
		return httpx.BadRequest("Valid 'from_time' timestamp is required")
	}

	if err := h.svc.RebuildRollups(c.UserContext(), req.FromTime, tc); err != nil {
		return mapAnalyticsError(err)
	}
	return httpx.OK(c, fiber.Map{"status": "rebuild_scheduled", "from_time": req.FromTime})
}

func (h *Handler) getDeliveryReport(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	filter := parseFilter(c)
	data, err := h.svc.GetDeliveryReport(c.UserContext(), filter, tc)
	if err != nil {
		return mapAnalyticsError(err)
	}
	return httpx.OK(c, data)
}

func (h *Handler) getFinancialReport(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	filter := parseFilter(c)
	data, err := h.svc.GetFinancialReport(c.UserContext(), filter, tc)
	if err != nil {
		return mapAnalyticsError(err)
	}
	return httpx.OK(c, data)
}

func (h *Handler) getWidgets(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	widgets := h.svc.GetAvailableWidgets(c.UserContext(), tc)
	return httpx.OK(c, fiber.Map{"items": widgets})
}

func (h *Handler) getDashboardLayout(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	pref, err := h.svc.GetUserPreferences(c.UserContext(), tc)
	if err != nil {
		return mapAnalyticsError(err)
	}
	return httpx.OK(c, fiber.Map{"layout": pref.DashboardLayout})
}

type SaveLayoutRequest struct {
	Layout map[string]interface{} `json:"layout"`
}

func (h *Handler) saveDashboardLayout(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	body := c.Body()
	if len(body) == 0 {
		return httpx.BadRequest("Layout body is required")
	}

	if err := h.svc.SaveDashboardLayout(c.UserContext(), tc, body); err != nil {
		return mapAnalyticsError(err)
	}
	return httpx.OK(c, fiber.Map{"status": "layout_saved"})
}
