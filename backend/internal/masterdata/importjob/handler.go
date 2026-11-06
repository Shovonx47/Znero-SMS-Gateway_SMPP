package importjob

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
	group := router.Group("/import/jobs", authMiddleware)
	group.Get("", rbac.RequirePermission("import.job.view"), h.list)
	group.Post("/upload", rbac.RequirePermission("import.job.manage"), h.upload)
	group.Get("/:id", rbac.RequirePermission("import.job.view"), h.get)
	group.Get("/:id/rows", rbac.RequirePermission("import.job.view"), h.rows)
	group.Get("/:id/errors", rbac.RequirePermission("import.job.view"), h.errors)
	group.Post("/:id/confirm", rbac.RequirePermission("import.job.manage"), h.confirm)
	group.Post("/:id/rollback", rbac.RequirePermission("import.job.manage"), h.rollback)

	// Also support /import/upload directly
	router.Post("/import/upload", authMiddleware, rbac.RequirePermission("import.job.manage"), h.upload)
}

func (h *Handler) list(c *fiber.Ctx) error {
	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "25"))

	filter := Filter{
		EntityType: c.Query("entity_type"),
		Status:     c.Query("status"),
		Page:       page,
		PageSize:   pageSize,
	}

	res, err := h.svc.ListJobs(c.UserContext(), filter)
	if err != nil {
		return httpx.Internal(err)
	}
	return httpx.OK(c, res)
}

func (h *Handler) get(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid job ID")
	}

	job, err := h.svc.GetJob(c.UserContext(), id)
	if err != nil {
		if errors.Is(err, ErrJobNotFound) {
			return httpx.NotFound("Import job not found")
		}
		return httpx.Internal(err)
	}
	return httpx.OK(c, job)
}

func (h *Handler) rows(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid job ID")
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "50"))
	statusFilter := c.Query("status")

	res, err := h.svc.ListJobRows(c.UserContext(), id, statusFilter, page, pageSize)
	if err != nil {
		return httpx.Internal(err)
	}
	return httpx.OK(c, res)
}

func (h *Handler) upload(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	entityType := c.FormValue("entity_type")
	if entityType == "" {
		return httpx.BadRequest("entity_type form field is required (country, operator, or prefix)")
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return httpx.BadRequest("file form field is required")
	}

	file, err := fileHeader.Open()
	if err != nil {
		return httpx.BadRequest("failed to open uploaded file")
	}
	defer file.Close()

	contentType := fileHeader.Header.Get("Content-Type")
	job, err := h.svc.UploadAndPreview(c.UserContext(), tc, entityType, fileHeader.Filename, fileHeader.Size, contentType, file)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyImport) {
			return httpx.Forbidden(err.Error())
		}
		if errors.Is(err, ErrFileTooLarge) || errors.Is(err, ErrUnsupportedType) || errors.Is(err, ErrEmptyFile) {
			return httpx.BadRequest(err.Error())
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, job)
}

func (h *Handler) confirm(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid job ID")
	}

	var req ConfirmRequest
	_ = c.BodyParser(&req)

	job, err := h.svc.ConfirmImport(c.UserContext(), tc, id, req.AllowPartial)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyImport) {
			return httpx.Forbidden(err.Error())
		}
		if errors.Is(err, ErrJobNotFound) {
			return httpx.NotFound("Import job not found")
		}
		if errors.Is(err, ErrPartialNotPermitted) || errors.Is(err, ErrNoValidRows) || errors.Is(err, ErrInvalidJobStatus) {
			return httpx.BadRequest(err.Error())
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, job)
}

func (h *Handler) rollback(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid job ID")
	}

	if err := h.svc.RollbackImport(c.UserContext(), tc, id); err != nil {
		if errors.Is(err, ErrPlatformOnlyImport) {
			return httpx.Forbidden(err.Error())
		}
		if errors.Is(err, ErrJobNotFound) {
			return httpx.NotFound("Import job not found")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{"message": "Import job rolled back successfully"})
}

func (h *Handler) errors(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid job ID")
	}

	data, err := h.svc.DownloadErrors(c.UserContext(), tc, id)
	if err != nil {
		return httpx.Internal(err)
	}

	filename := fmt.Sprintf("import_errors_%s_%d.csv", id.String()[:8], time.Now().Unix())
	c.Set("Content-Type", "text/csv")
	c.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	return c.Send(data)
}
