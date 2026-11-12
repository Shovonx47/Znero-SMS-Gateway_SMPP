package notification

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
	group := router.Group("/notifications", authMiddleware)

	group.Get("/", rbac.RequirePermission("notification.view"), h.listNotifications)
	group.Get("/unread-count", rbac.RequirePermission("notification.view"), h.getUnreadCount)
	group.Put("/:id/read", rbac.RequirePermission("notification.manage"), h.markAsRead)
	group.Post("/read-all", rbac.RequirePermission("notification.manage"), h.markAllAsRead)
	group.Delete("/:id", rbac.RequirePermission("notification.manage"), h.deleteNotification)
	group.Get("/preferences", rbac.RequirePermission("notification.view"), h.getPreferences)
	group.Put("/preferences", rbac.RequirePermission("notification.manage"), h.updatePreferences)
	group.Post("/test", rbac.RequirePermission("notification.manage"), h.testNotification)
}

func (h *Handler) listNotifications(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	filter := ListFilter{
		Category: c.Query("category"),
		Severity: c.Query("severity"),
	}

	if isReadStr := c.Query("is_read"); isReadStr != "" {
		if b, err := strconv.ParseBool(isReadStr); err == nil {
			filter.IsRead = &b
		}
	}

	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			filter.Limit = n
		}
	}
	if o := c.Query("offset"); o != "" {
		if n, err := strconv.Atoi(o); err == nil && n >= 0 {
			filter.Offset = n
		}
	}

	items, total, err := h.svc.List(c.UserContext(), tc, filter)
	if err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{
		"items": items,
		"total": total,
	})
}

func (h *Handler) getUnreadCount(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	count, err := h.svc.GetUnreadCount(c.UserContext(), tc)
	if err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{
		"unread_count": count,
	})
}

func (h *Handler) markAsRead(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid notification ID")
	}

	if err := h.svc.MarkAsRead(c.UserContext(), tc, id); err != nil {
		if errors.Is(err, ErrNotificationNotFound) {
			return httpx.NotFound("Notification not found")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{
		"status": "marked_as_read",
	})
}

func (h *Handler) markAllAsRead(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	if err := h.svc.MarkAllAsRead(c.UserContext(), tc); err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{
		"status": "all_marked_as_read",
	})
}

func (h *Handler) deleteNotification(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid notification ID")
	}

	if err := h.svc.Delete(c.UserContext(), tc, id); err != nil {
		if errors.Is(err, ErrNotificationNotFound) {
			return httpx.NotFound("Notification not found")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{
		"status": "deleted",
	})
}

func (h *Handler) getPreferences(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	prefs, err := h.svc.GetPreferences(c.UserContext(), tc)
	if err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, prefs)
}

func (h *Handler) updatePreferences(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	var req UpdatePreferencesRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid request body")
	}

	updated, err := h.svc.UpdatePreferences(c.UserContext(), tc, req)
	if err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, updated)
}

func (h *Handler) testNotification(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	if err := h.svc.TestNotification(c.UserContext(), tc); err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{
		"status":  "test_notification_dispatched",
		"message": "Test notification was sent across enabled channels",
	})
}
