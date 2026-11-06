package provider

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
	credLimiter := httpx.CredentialRateLimiter(10, time.Minute)

	p := router.Group("/providers", authMiddleware)
	p.Get("", rbac.RequirePermission("provider.provider.view"), h.listProviders)
	p.Get("/export", rbac.RequirePermission("provider.provider.view"), h.exportProviders)
	p.Get("/:id", rbac.RequirePermission("provider.provider.view"), h.getProvider)
	p.Get("/:id/history", rbac.RequirePermission("provider.provider.view"), h.providerHistory)
	p.Post("", rbac.RequirePermission("provider.provider.create"), h.createProvider)
	p.Put("/:id", rbac.RequirePermission("provider.provider.edit"), h.updateProvider)
	p.Post("/:id/rollback", rbac.RequirePermission("provider.provider.edit"), h.rollbackProvider)

	// Connections under /providers/:id/connections and /providers/:id/connections/:connId
	p.Get("/:id/connections", rbac.RequirePermission("provider.connection.view"), h.listConnections)
	p.Get("/:id/connections/export", rbac.RequirePermission("provider.connection.view"), h.exportConnections)
	p.Post("/:id/connections", rbac.RequirePermission("provider.connection.create"), h.createConnection)
	p.Get("/:id/connections/:connId", rbac.RequirePermission("provider.connection.view"), h.getConnection)
	p.Put("/:id/connections/:connId", rbac.RequirePermission("provider.connection.edit"), h.updateConnection)
	p.Put("/:id/connections/:connId/secret", credLimiter, rbac.RequirePermission("provider.connection.secret.update"), h.updateConnectionSecret)
	p.Post("/:id/connections/:connId/status", rbac.RequirePermission("provider.connection.status"), h.changeConnectionStatus)
	p.Get("/:id/connections/:connId/history", rbac.RequirePermission("provider.connection.view"), h.connectionHistory)
	p.Post("/:id/connections/:connId/rollback", rbac.RequirePermission("provider.connection.edit"), h.rollbackConnection)

	// Coverage under /provider-coverage
	cov := router.Group("/provider-coverage", authMiddleware)
	cov.Get("", rbac.RequirePermission("provider.coverage.view"), h.listCoverage)
	cov.Get("/export", rbac.RequirePermission("provider.coverage.view"), h.exportCoverage)
	cov.Get("/:covId", rbac.RequirePermission("provider.coverage.view"), h.getCoverage)
	cov.Get("/:covId/history", rbac.RequirePermission("provider.coverage.view"), h.coverageHistory)
	cov.Post("", rbac.RequirePermission("provider.coverage.create"), h.createCoverage)
	cov.Put("/:covId", rbac.RequirePermission("provider.coverage.edit"), h.updateCoverage)
	cov.Post("/:covId/rollback", rbac.RequirePermission("provider.coverage.edit"), h.rollbackCoverage)
}

func mapErr(err error) error {
	switch {
	case errors.Is(err, ErrPlatformOnlyProvider), errors.Is(err, ErrPermissionDenied):
		return httpx.Forbidden(err.Error())
	case errors.Is(err, ErrProviderNotFound), errors.Is(err, ErrConnectionNotFound), errors.Is(err, ErrCoverageNotFound):
		return httpx.NotFound(err.Error())
	case errors.Is(err, ErrProviderCodeExists), errors.Is(err, ErrConnectionNameExists), errors.Is(err, ErrCoverageExists):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrValidation), errors.Is(err, ErrOperatorCountryMismatch):
		return httpx.BadRequest(err.Error())
	default:
		return httpx.Internal(err)
	}
}

func (h *Handler) listProviders(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	page, _ := strconv.Atoi(c.Query("page", "1"))
	limit, _ := strconv.Atoi(c.Query("page_size", c.Query("limit", "25")))
	var countryID *uuid.UUID
	if cid := c.Query("country_id"); cid != "" {
		if parsed, err := uuid.Parse(cid); err == nil {
			countryID = &parsed
		}
	}
	filter := ProviderListFilter{
		Search:       c.Query("search"),
		ProviderType: c.Query("provider_type"),
		CountryID:    countryID,
		Status:       c.Query("status"),
		SortBy:       c.Query("sort_by", "display_name"),
		SortDir:      c.Query("sort_dir", "asc"),
		Page:         page,
		Limit:        limit,
	}
	items, total, err := h.svc.ListProviders(c.UserContext(), tc, filter)
	if err != nil {
		return mapErr(err)
	}
	if items == nil {
		items = []Provider{}
	}
	return httpx.OK(c, fiber.Map{
		"items":       items,
		"total_count": total,
		"page":        page,
		"page_size":   limit,
	})
}

func (h *Handler) getProvider(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid provider ID")
	}
	p, err := h.svc.GetProvider(c.UserContext(), tc, id)
	if err != nil {
		return mapErr(err)
	}
	return httpx.OK(c, p)
}

func (h *Handler) createProvider(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	var req CreateProviderRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}
	res, err := h.svc.CreateProvider(c.UserContext(), tc, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	if res.PendingChange != nil {
		return httpx.OK(c, fiber.Map{"pending_approval": true, "change_request": res.PendingChange})
	}
	return httpx.OK(c, res.Provider)
}

func (h *Handler) updateProvider(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid provider ID")
	}
	var req UpdateProviderRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}
	res, err := h.svc.UpdateProvider(c.UserContext(), tc, id, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	if res.PendingChange != nil {
		return httpx.OK(c, fiber.Map{"pending_approval": true, "change_request": res.PendingChange})
	}
	return httpx.OK(c, res.Provider)
}

func (h *Handler) providerHistory(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid provider ID")
	}
	items, err := h.svc.ListProviderHistory(c.UserContext(), tc, id)
	if err != nil {
		return mapErr(err)
	}
	if items == nil {
		items = []ProviderHistory{}
	}
	return httpx.OK(c, items)
}

type rollbackBody struct {
	TargetVersion int    `json:"target_version"`
	Reason        string `json:"reason"`
}

func (h *Handler) rollbackProvider(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid provider ID")
	}
	var body rollbackBody
	if err := c.BodyParser(&body); err != nil || body.TargetVersion <= 0 {
		return httpx.BadRequest("target_version is required")
	}
	res, err := h.svc.RollbackProvider(c.UserContext(), tc, id, body.TargetVersion, body.Reason, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	if res.PendingChange != nil {
		return httpx.OK(c, fiber.Map{"pending_approval": true, "change_request": res.PendingChange})
	}
	return httpx.OK(c, res.Provider)
}

func (h *Handler) exportProviders(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	format := c.Query("format", "csv")
	filter := ProviderListFilter{
		Search:       c.Query("search"),
		ProviderType: c.Query("provider_type"),
		Status:       c.Query("status"),
	}
	data, contentType, err := h.svc.ExportProviders(c.UserContext(), tc, filter, format)
	if err != nil {
		return mapErr(err)
	}
	ext := "csv"
	if format == "json" {
		ext = "json"
	}
	c.Set("Content-Type", contentType)
	c.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="providers_%s.%s"`, time.Now().UTC().Format("20060102"), ext))
	return c.Send(data)
}

// Connections

func (h *Handler) listConnections(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	providerID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid provider ID")
	}
	items, err := h.svc.ListConnections(c.UserContext(), tc, providerID)
	if err != nil {
		return mapErr(err)
	}
	if items == nil {
		items = []ProviderConnection{}
	}
	return httpx.OK(c, items)
}

func (h *Handler) exportConnections(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	providerID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid provider ID")
	}
	format := c.Query("format", "csv")
	data, contentType, err := h.svc.ExportConnections(c.UserContext(), tc, providerID, format)
	if err != nil {
		return mapErr(err)
	}
	ext := "csv"
	if format == "json" {
		ext = "json"
	}
	c.Set("Content-Type", contentType)
	c.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="provider_%s_connections_%s.%s"`, providerID.String()[:8], time.Now().UTC().Format("20060102"), ext))
	return c.Send(data)
}

func (h *Handler) getConnection(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	connID, err := uuid.Parse(c.Params("connId"))
	if err != nil {
		return httpx.BadRequest("Invalid connection ID")
	}
	conn, err := h.svc.GetConnection(c.UserContext(), tc, connID)
	if err != nil {
		return mapErr(err)
	}
	return httpx.OK(c, conn)
}

func (h *Handler) createConnection(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	providerID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("Invalid provider ID")
	}
	var req CreateConnectionRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}
	res, err := h.svc.CreateConnection(c.UserContext(), tc, providerID, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	if res.PendingChange != nil {
		return httpx.OK(c, fiber.Map{"pending_approval": true, "change_request": res.PendingChange})
	}
	return httpx.OK(c, res.Connection)
}

func (h *Handler) updateConnection(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	connID, err := uuid.Parse(c.Params("connId"))
	if err != nil {
		return httpx.BadRequest("Invalid connection ID")
	}
	var req UpdateConnectionRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}
	res, err := h.svc.UpdateConnection(c.UserContext(), tc, connID, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	if res.PendingChange != nil {
		return httpx.OK(c, fiber.Map{"pending_approval": true, "change_request": res.PendingChange})
	}
	return httpx.OK(c, res.Connection)
}

func (h *Handler) updateConnectionSecret(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	connID, err := uuid.Parse(c.Params("connId"))
	if err != nil {
		return httpx.BadRequest("Invalid connection ID")
	}
	var req UpdateConnectionSecretRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}
	res, err := h.svc.UpdateConnectionSecret(c.UserContext(), tc, connID, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	if res.PendingChange != nil {
		return httpx.OK(c, fiber.Map{"pending_approval": true, "change_request": res.PendingChange})
	}
	return httpx.OK(c, res.Connection)
}

func (h *Handler) changeConnectionStatus(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	connID, err := uuid.Parse(c.Params("connId"))
	if err != nil {
		return httpx.BadRequest("Invalid connection ID")
	}
	var req UpdateConnectionStatusRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}
	res, err := h.svc.ChangeConnectionStatus(c.UserContext(), tc, connID, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	if res.PendingChange != nil {
		return httpx.OK(c, fiber.Map{"pending_approval": true, "change_request": res.PendingChange})
	}
	return httpx.OK(c, res.Connection)
}

func (h *Handler) connectionHistory(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	connID, err := uuid.Parse(c.Params("connId"))
	if err != nil {
		return httpx.BadRequest("Invalid connection ID")
	}
	items, err := h.svc.ListConnectionHistory(c.UserContext(), tc, connID)
	if err != nil {
		return mapErr(err)
	}
	if items == nil {
		items = []ProviderConnectionHistory{}
	}
	return httpx.OK(c, items)
}

func (h *Handler) rollbackConnection(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	connID, err := uuid.Parse(c.Params("connId"))
	if err != nil {
		return httpx.BadRequest("Invalid connection ID")
	}
	var body rollbackBody
	if err := c.BodyParser(&body); err != nil || body.TargetVersion <= 0 {
		return httpx.BadRequest("target_version is required")
	}
	res, err := h.svc.RollbackConnection(c.UserContext(), tc, connID, body.TargetVersion, body.Reason, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	if res.PendingChange != nil {
		return httpx.OK(c, fiber.Map{"pending_approval": true, "change_request": res.PendingChange})
	}
	return httpx.OK(c, res.Connection)
}

// Coverage

func (h *Handler) listCoverage(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	page, _ := strconv.Atoi(c.Query("page", "1"))
	limit, _ := strconv.Atoi(c.Query("page_size", c.Query("limit", "25")))
	filter := CoverageListFilter{
		Status: c.Query("status"),
		Search: c.Query("search"),
		Page:   page,
		Limit:  limit,
	}
	if v := c.Query("provider_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			filter.ProviderID = &id
		}
	}
	if v := c.Query("country_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			filter.CountryID = &id
		}
	}
	if v := c.Query("operator_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			filter.OperatorID = &id
		}
	}
	items, total, err := h.svc.ListCoverage(c.UserContext(), tc, filter)
	if err != nil {
		return mapErr(err)
	}
	if items == nil {
		items = []ProviderCoverage{}
	}
	return httpx.OK(c, fiber.Map{
		"items":       items,
		"total_count": total,
		"page":        page,
		"page_size":   limit,
	})
}

func (h *Handler) getCoverage(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	id, err := uuid.Parse(c.Params("covId"))
	if err != nil {
		return httpx.BadRequest("Invalid coverage ID")
	}
	cov, err := h.svc.GetCoverage(c.UserContext(), tc, id)
	if err != nil {
		return mapErr(err)
	}
	return httpx.OK(c, cov)
}

func (h *Handler) createCoverage(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	var req CreateCoverageRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}
	res, err := h.svc.CreateCoverage(c.UserContext(), tc, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	if res.PendingChange != nil {
		return httpx.OK(c, fiber.Map{"pending_approval": true, "change_request": res.PendingChange})
	}
	return httpx.OK(c, res.Coverage)
}

func (h *Handler) updateCoverage(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	id, err := uuid.Parse(c.Params("covId"))
	if err != nil {
		return httpx.BadRequest("Invalid coverage ID")
	}
	var req UpdateCoverageRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("Invalid JSON body")
	}
	res, err := h.svc.UpdateCoverage(c.UserContext(), tc, id, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	if res.PendingChange != nil {
		return httpx.OK(c, fiber.Map{"pending_approval": true, "change_request": res.PendingChange})
	}
	return httpx.OK(c, res.Coverage)
}

func (h *Handler) coverageHistory(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	id, err := uuid.Parse(c.Params("covId"))
	if err != nil {
		return httpx.BadRequest("Invalid coverage ID")
	}
	items, err := h.svc.ListCoverageHistory(c.UserContext(), tc, id)
	if err != nil {
		return mapErr(err)
	}
	if items == nil {
		items = []ProviderCoverageHistory{}
	}
	return httpx.OK(c, items)
}

func (h *Handler) rollbackCoverage(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	id, err := uuid.Parse(c.Params("covId"))
	if err != nil {
		return httpx.BadRequest("Invalid coverage ID")
	}
	var body rollbackBody
	if err := c.BodyParser(&body); err != nil || body.TargetVersion <= 0 {
		return httpx.BadRequest("target_version is required")
	}
	res, err := h.svc.RollbackCoverage(c.UserContext(), tc, id, body.TargetVersion, body.Reason, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return mapErr(err)
	}
	if res.PendingChange != nil {
		return httpx.OK(c, fiber.Map{"pending_approval": true, "change_request": res.PendingChange})
	}
	return httpx.OK(c, res.Coverage)
}

func (h *Handler) exportCoverage(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("Authentication required")
	}
	format := c.Query("format", "csv")
	filter := CoverageListFilter{
		Status: c.Query("status"),
		Search: c.Query("search"),
	}
	if v := c.Query("provider_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			filter.ProviderID = &id
		}
	}
	if v := c.Query("country_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			filter.CountryID = &id
		}
	}
	data, contentType, err := h.svc.ExportCoverage(c.UserContext(), tc, filter, format)
	if err != nil {
		return mapErr(err)
	}
	ext := "csv"
	if format == "json" {
		ext = "json"
	}
	c.Set("Content-Type", contentType)
	c.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="provider_coverage_%s.%s"`, time.Now().UTC().Format("20060102"), ext))
	return c.Send(data)
}
