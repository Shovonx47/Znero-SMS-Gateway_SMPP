package audit

import (
	"strconv"
	"time"

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

func (h *Handler) Register(router fiber.Router, authMiddleware, permMiddleware fiber.Handler) {
	group := router.Group("/audit")
	if authMiddleware != nil {
		group.Use(authMiddleware)
	}
	if permMiddleware != nil {
		group.Use(permMiddleware)
	}
	group.Get("/logs", h.list)
}

func (h *Handler) list(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "25"))

	filter := Filter{
		Module:     c.Query("module"),
		Action:     c.Query("action"),
		EntityType: c.Query("entity_type"),
		Search:     c.Query("search"),
		Page:       page,
		PageSize:   pageSize,
	}

	if start := c.Query("start_date"); start != "" {
		if t, err := time.Parse(time.RFC3339, start); err == nil {
			filter.StartDate = &t
		}
	}
	if end := c.Query("end_date"); end != "" {
		if t, err := time.Parse(time.RFC3339, end); err == nil {
			filter.EndDate = &t
		}
	}
	if actor := c.Query("actor_id"); actor != "" {
		if uid, err := uuid.Parse(actor); err == nil {
			filter.ActorID = &uid
		}
	}
	if target := c.Query("target_org_id"); target != "" {
		if uid, err := uuid.Parse(target); err == nil {
			filter.TargetOrg = &uid
		}
	}

	result, err := h.svc.List(c.UserContext(), tc, filter)
	if err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, result)
}
