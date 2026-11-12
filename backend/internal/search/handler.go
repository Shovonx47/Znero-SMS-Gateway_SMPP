package search

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
	// Search group
	searchGroup := router.Group("/search", authMiddleware)
	searchGroup.Get("/", h.search)

	// Saved Filters group
	filtersGroup := router.Group("/saved-filters", authMiddleware)
	filtersGroup.Get("/", h.listSavedFilters)
	filtersGroup.Post("/", h.createSavedFilter)
	filtersGroup.Delete("/:id", h.deleteSavedFilter)
	filtersGroup.Post("/:id/default", h.setDefaultFilter)

	// User Preferences group
	prefGroup := router.Group("/preferences", authMiddleware)
	prefGroup.Get("/", h.getUserPreferences)
	prefGroup.Put("/", h.updateUserPreferences)
}

func (h *Handler) search(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	q := c.Query("q")
	resp, err := h.svc.Search(c.UserContext(), tc, q)
	if err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, resp)
}

func (h *Handler) listSavedFilters(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	viewName := c.Query("view_name")
	filters, err := h.svc.ListSavedFilters(c.UserContext(), tc, viewName)
	if err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{"filters": filters})
}

func (h *Handler) createSavedFilter(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	var req CreateSavedFilterRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid request body")
	}
	if req.ViewName == "" || req.Name == "" {
		return httpx.BadRequest("view_name and name are required")
	}

	sf, err := h.svc.CreateSavedFilter(c.UserContext(), tc, req)
	if err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, sf)
}

func (h *Handler) deleteSavedFilter(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid filter ID")
	}

	if err := h.svc.DeleteSavedFilter(c.UserContext(), tc, id); err != nil {
		if errors.Is(err, ErrFilterNotFound) {
			return httpx.NotFound("Saved filter not found")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{"status": "deleted"})
}

func (h *Handler) setDefaultFilter(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid filter ID")
	}

	if err := h.svc.SetDefaultFilter(c.UserContext(), tc, id); err != nil {
		if errors.Is(err, ErrFilterNotFound) {
			return httpx.NotFound("Saved filter not found")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{"status": "default_updated"})
}

func (h *Handler) getUserPreferences(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	pref, err := h.svc.GetUserPreferences(c.UserContext(), tc)
	if err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, pref)
}

func (h *Handler) updateUserPreferences(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	var req UpdatePreferenceRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid request body")
	}

	pref, err := h.svc.UpdateUserPreferences(c.UserContext(), tc, req)
	if err != nil {
		if errors.Is(err, ErrInvalidLocale) || errors.Is(err, ErrInvalidTheme) {
			return httpx.BadRequest(err.Error())
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, pref)
}
