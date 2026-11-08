package messaging

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/rbac"
	"github.com/znero-international/znero/backend/pkg/httpx"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

// SessionStats represents operational metrics for an SMPP provider connection.
type SessionStats struct {
	ConnectionID   uuid.UUID `json:"connection_id"`
	Bound          bool      `json:"bound"`
	InFlight       int       `json:"in_flight"`
	CurrentTPS     float64   `json:"current_tps"`
	SuccessCount   uint64    `json:"success_count"`
	FailureCount   uint64    `json:"failure_count"`
	LastErrorCode  string    `json:"last_error_code"`
	ReconnectCount uint64    `json:"reconnect_count"`
}

// SessionStatsProvider supplies live connection session metrics to the handler.
type SessionStatsProvider interface {
	GetAllSessionStats(ctx context.Context) ([]SessionStats, error)
}

// Handler serves HTTP endpoints for messaging, messages explorer, and platform operational monitoring.
type Handler struct {
	svc          *Service
	reconcileSvc *ReconciliationService
	sessionProv  SessionStatsProvider
	repo         Repository
}

// NewHandler creates a new Handler instance.
func NewHandler(
	svc *Service,
	reconcileSvc *ReconciliationService,
	sessionProv SessionStatsProvider,
	repo Repository,
) *Handler {
	return &Handler{
		svc:          svc,
		reconcileSvc: reconcileSvc,
		sessionProv:  sessionProv,
		repo:         repo,
	}
}

// RegisterRoutes mounts all messaging routes onto the Fiber router.
func (h *Handler) RegisterRoutes(router fiber.Router, authMiddleware fiber.Handler) {
	// Protected messaging group
	g := router.Group("/messages", authMiddleware)
	g.Get("", rbac.RequirePermission("message.view"), h.listMessages)
	g.Get("/export", rbac.RequirePermission("message.export"), h.exportMessages)
	g.Get("/:id", rbac.RequirePermission("message.view"), h.getMessageDetail)

	// Platform Operational & Management Endpoints
	mgmt := router.Group("/messaging", authMiddleware)
	mgmt.Post("/test-send", rbac.RequirePermission("message.platform.manage"), h.testSend)
	mgmt.Get("/smpp/sessions", rbac.RequirePermission("message.platform.view"), h.getSMPPSessions)
	mgmt.Get("/queue/status", rbac.RequirePermission("message.queue.view"), h.getQueueStatus)
	mgmt.Get("/dlrs/unmatched", rbac.RequirePermission("message.platform.view"), h.getUnmatchedDLRs)
	mgmt.Get("/settlement/exceptions", rbac.RequirePermission("message.reconcile"), h.listSettlementExceptions)
	mgmt.Post("/settlement/exceptions/:id/resolve", rbac.RequirePermission("message.reconcile"), h.resolveSettlementException)
	mgmt.Post("/reconciliation/run", rbac.RequirePermission("message.reconcile"), h.runReconciliation)
	mgmt.Get("/reconciliation/history", rbac.RequirePermission("message.reconcile"), h.listReconciliationRuns)

	// DLR Mappings CRUD
	mgmt.Get("/dlr-mappings", rbac.RequirePermission("message.dlr_mapping.manage"), h.listDLRMappings)
	mgmt.Post("/dlr-mappings", rbac.RequirePermission("message.dlr_mapping.manage"), h.createDLRMapping)
	mgmt.Put("/dlr-mappings/:id", rbac.RequirePermission("message.dlr_mapping.manage"), h.updateDLRMapping)
	mgmt.Delete("/dlr-mappings/:id", rbac.RequirePermission("message.dlr_mapping.manage"), h.deleteDLRMapping)
}

func (h *Handler) listMessages(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil || tc == nil {
		return httpx.Unauthorized("authentication required")
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("per_page", "20"))
	if pageSize > 100 {
		pageSize = 100
	}

	filter := MessageFilter{
		Status:      c.Query("status"),
		Destination: c.Query("destination"),
		SenderID:    c.Query("sender_id"),
		Page:        page,
		PageSize:    pageSize,
	}

	if custStr := c.Query("customer_org_id"); custStr != "" {
		if u, err := uuid.Parse(custStr); err == nil {
			filter.CustomerOrgID = &u
		}
	}
	if connStr := c.Query("provider_connection_id"); connStr != "" && tc.IsPlatformOrg() {
		if u, err := uuid.Parse(connStr); err == nil {
			filter.ConnectionID = &u
		}
	}
	if df := c.Query("date_from"); df != "" {
		if t, err := time.Parse(time.RFC3339, df); err == nil {
			filter.DateFrom = &t
		}
	}
	if dt := c.Query("date_to"); dt != "" {
		if t, err := time.Parse(time.RFC3339, dt); err == nil {
			filter.DateTo = &t
		}
	}

	views, total, err := h.svc.ListMessagesAdvanced(c.UserContext(), filter, tc)
	if err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{
		"items":    views,
		"total":    total,
		"page":     page,
		"per_page": pageSize,
	})
}

func (h *Handler) getMessageDetail(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil || tc == nil {
		return httpx.Unauthorized("authentication required")
	}

	idStr := c.Params("id")
	msgID, err := uuid.Parse(idStr)
	if err != nil {
		return httpx.BadRequest("invalid message ID format")
	}

	detail, err := h.svc.GetMessageDetail(c.UserContext(), msgID, tc)
	if err != nil {
		if strings.Contains(err.Error(), "forbidden") {
			return httpx.Forbidden(err.Error())
		}
		if strings.Contains(err.Error(), "not found") {
			return httpx.NotFound(err.Error())
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, detail)
}

func (h *Handler) exportMessages(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil || tc == nil {
		return httpx.Unauthorized("authentication required")
	}

	format := strings.ToLower(c.Query("format", "csv"))
	if format != "csv" && format != "json" {
		format = "csv"
	}

	filter := MessageFilter{
		Status:      c.Query("status"),
		Destination: c.Query("destination"),
		SenderID:    c.Query("sender_id"),
	}
	if custStr := c.Query("customer_org_id"); custStr != "" {
		if u, err := uuid.Parse(custStr); err == nil {
			filter.CustomerOrgID = &u
		}
	}
	if connStr := c.Query("provider_connection_id"); connStr != "" && tc.IsPlatformOrg() {
		if u, err := uuid.Parse(connStr); err == nil {
			filter.ConnectionID = &u
		}
	}

	data, contentType, err := h.svc.ExportMessages(c.UserContext(), filter, format, tc)
	if err != nil {
		return httpx.Internal(err)
	}

	filename := "messages_export_" + time.Now().Format("20060102150405") + "." + format
	c.Set("Content-Type", contentType)
	c.Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	return c.Send(data)
}

func (h *Handler) testSend(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil || tc == nil || !tc.IsPlatformOrg() {
		return httpx.Forbidden("only platform administrators can dispatch test messages")
	}

	var req TestSendRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("invalid request body: " + err.Error())
	}

	if req.CustomerOrgID == uuid.Nil {
		return httpx.BadRequest("customer_org_id is required")
	}
	if req.Destination == "" {
		return httpx.BadRequest("destination number is required")
	}
	if req.SenderID == "" {
		return httpx.BadRequest("sender_id is required")
	}
	if req.Text == "" {
		return httpx.BadRequest("message text is required")
	}

	submitReq := SubmitRequest{
		Destination:     req.Destination,
		SenderID:        req.SenderID,
		Text:            req.Text,
		TrafficProfile:  "transactional",
		ValiditySeconds: req.ValiditySeconds,
	}

	custTC := &tenant.Context{
		ActorUserID:     tc.ActorUserID,
		ActorOrgID:      req.CustomerOrgID,
		ActorOrgType:    "customer",
		IsPlatformAdmin: false,
		Permissions:     tc.Permissions,
	}

	resp, err := h.svc.Submit(c.UserContext(), custTC, submitReq)
	if err != nil {
		return httpx.BadRequest("test send failed: " + err.Error())
	}

	return httpx.OK(c, resp)
}

func (h *Handler) getSMPPSessions(c *fiber.Ctx) error {
	if h.sessionProv == nil {
		return httpx.OK(c, fiber.Map{"sessions": []SessionStats{}})
	}

	stats, err := h.sessionProv.GetAllSessionStats(c.UserContext())
	if err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{"sessions": stats})
}

func (h *Handler) getQueueStatus(c *fiber.Ctx) error {
	stats, err := h.svc.GetQueueStatus(c.UserContext())
	if err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, stats)
}

func (h *Handler) getUnmatchedDLRs(c *fiber.Ctx) error {
	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("per_page", "50"))
	statusFilter := c.Query("match_status", "unmatched")

	list, total, err := h.repo.GetUnmatchedDLRs(c.UserContext(), page, pageSize, statusFilter)
	if err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{
		"items":    list,
		"total":    total,
		"page":     page,
		"per_page": pageSize,
	})
}

func (h *Handler) listSettlementExceptions(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil || tc == nil {
		return httpx.Unauthorized("authentication required")
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("per_page", "50"))

	var resolved *bool
	if rStr := c.Query("resolved"); rStr != "" {
		b := rStr == "true" || rStr == "1"
		resolved = &b
	}

	list, total, err := h.svc.ListSettlementExceptions(c.UserContext(), resolved, page, pageSize, tc)
	if err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{
		"items":    list,
		"total":    total,
		"page":     page,
		"per_page": pageSize,
	})
}

func (h *Handler) resolveSettlementException(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil || tc == nil || !tc.IsPlatformOrg() {
		return httpx.Forbidden("only platform administrators can resolve settlement exceptions")
	}

	idStr := c.Params("id")
	excID, err := uuid.Parse(idStr)
	if err != nil {
		return httpx.BadRequest("invalid exception ID")
	}

	if err := h.svc.ResolveSettlementException(c.UserContext(), excID, tc); err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{"success": true})
}

func (h *Handler) runReconciliation(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil || tc == nil || !tc.IsPlatformOrg() {
		return httpx.Forbidden("only platform administrators can trigger reconciliation runs")
	}

	if h.reconcileSvc == nil {
		return httpx.Internal(nil)
	}

	report, err := h.reconcileSvc.RunReconciliation(c.UserContext(), &tc.ActorUserID)
	if err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, report)
}

func (h *Handler) listReconciliationRuns(c *fiber.Ctx) error {
	limit, _ := strconv.Atoi(c.Query("limit", "20"))
	runs, err := h.repo.ListReconciliationRuns(c.UserContext(), limit)
	if err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{"items": runs})
}

func (h *Handler) listDLRMappings(c *fiber.Ctx) error {
	var connID *uuid.UUID
	if cStr := c.Query("connection_id"); cStr != "" {
		if u, err := uuid.Parse(cStr); err == nil {
			connID = &u
		}
	}

	mappings, err := h.svc.ListDLRMappings(c.UserContext(), connID)
	if err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{"items": mappings})
}

func (h *Handler) createDLRMapping(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil || tc == nil || !tc.IsPlatformOrg() {
		return httpx.Forbidden("only platform administrators can create DLR mappings")
	}

	var m DLRMapping
	if err := c.BodyParser(&m); err != nil {
		return httpx.BadRequest("invalid body: " + err.Error())
	}

	if m.ConnectionID == uuid.Nil {
		return httpx.BadRequest("connection_id is required")
	}
	if m.UpstreamStat == "" {
		return httpx.BadRequest("upstream_stat is required")
	}
	if m.MappedStatus == "" {
		return httpx.BadRequest("mapped_status is required")
	}

	if err := h.svc.CreateDLRMapping(c.UserContext(), &m, tc); err != nil {
		return httpx.BadRequest(err.Error())
	}

	return httpx.JSON(c, 201, m)
}

func (h *Handler) updateDLRMapping(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil || tc == nil || !tc.IsPlatformOrg() {
		return httpx.Forbidden("only platform administrators can update DLR mappings")
	}

	idStr := c.Params("id")
	mappingID, err := uuid.Parse(idStr)
	if err != nil {
		return httpx.BadRequest("invalid mapping ID")
	}

	var m DLRMapping
	if err := c.BodyParser(&m); err != nil {
		return httpx.BadRequest("invalid body: " + err.Error())
	}
	m.ID = mappingID

	if err := h.svc.UpdateDLRMapping(c.UserContext(), &m, tc); err != nil {
		return httpx.BadRequest(err.Error())
	}

	return httpx.OK(c, m)
}

func (h *Handler) deleteDLRMapping(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil || tc == nil || !tc.IsPlatformOrg() {
		return httpx.Forbidden("only platform administrators can delete DLR mappings")
	}

	idStr := c.Params("id")
	mappingID, err := uuid.Parse(idStr)
	if err != nil {
		return httpx.BadRequest("invalid mapping ID")
	}

	if err := h.svc.DeleteDLRMapping(c.UserContext(), mappingID, tc); err != nil {
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{"success": true})
}
