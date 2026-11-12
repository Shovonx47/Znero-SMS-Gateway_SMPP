package export

import (
	"errors"
	"net/http"
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
	group := router.Group("/exports", authMiddleware)

	group.Post("/", rbac.RequirePermission("analytics.export.create"), h.createJob)
	group.Get("/", rbac.RequirePermission("analytics.export.view"), h.listJobs)
	group.Get("/:id", rbac.RequirePermission("analytics.export.view"), h.getJob)
	group.Get("/:id/download", rbac.RequirePermission("analytics.export.view"), h.downloadJob)
}

func (h *Handler) createJob(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	var req CreateJobRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid request body")
	}

	job, err := h.svc.SubmitJob(c.UserContext(), tc, req)
	if err != nil {
		if errors.Is(err, ErrUnsupportedFormat) || errors.Is(err, ErrUnsupportedType) {
			return httpx.BadRequest(err.Error())
		}
		return httpx.Internal(err)
	}

	return httpx.JSON(c, http.StatusAccepted, fiber.Map{
		"job": job,
	})
}

func (h *Handler) listJobs(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	limit := 20
	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	offset := 0
	if o := c.Query("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	jobs, total, err := h.svc.ListJobs(c.UserContext(), tc, limit, offset)
	if err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{
		"jobs":  jobs,
		"total": total,
	})
}

func (h *Handler) getJob(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid job ID")
	}

	job, err := h.svc.GetJob(c.UserContext(), tc, id)
	if err != nil {
		if errors.Is(err, ErrJobNotFound) {
			return httpx.NotFound("Export job not found")
		}
		if errors.Is(err, ErrUnauthorizedTenant) {
			return httpx.Forbidden("Access denied to export job")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{
		"job": job,
	})
}

func (h *Handler) downloadJob(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid job ID")
	}

	filePath, filename, contentType, err := h.svc.DownloadJob(c.UserContext(), tc, id)
	if err != nil {
		if errors.Is(err, ErrJobNotFound) {
			return httpx.NotFound("Export file not found")
		}
		if errors.Is(err, ErrUnauthorizedTenant) {
			return httpx.Forbidden("Access denied to export job")
		}
		if errors.Is(err, ErrJobNotReady) {
			return httpx.BadRequest("Export job is still processing")
		}
		if errors.Is(err, ErrJobExpired) {
			return httpx.BadRequest("Export file has expired")
		}
		return httpx.Internal(err)
	}

	c.Set("Content-Type", contentType)
	c.Attachment(filename)
	return c.SendFile(filePath)
}
