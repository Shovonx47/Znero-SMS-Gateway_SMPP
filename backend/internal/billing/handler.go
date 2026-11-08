package billing

import (
	"errors"
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
	moneyLimiter := httpx.MoneyRateLimiter(60, time.Minute)

	// Invoices routes
	invoices := router.Group("/invoices", authMiddleware)
	invoices.Get("/", rbac.RequirePermission("billing.invoice.view"), h.ListInvoices)
	invoices.Post("/", rbac.RequirePermission("billing.invoice.create"), moneyLimiter, h.CreateDraft)

	invoice := invoices.Group("/:id")
	invoice.Get("/", rbac.RequirePermission("billing.invoice.view"), h.GetInvoice)
	invoice.Put("/", rbac.RequirePermission("billing.invoice.create"), h.UpdateDraft)
	invoice.Post("/issue", rbac.RequirePermission("billing.invoice.issue"), moneyLimiter, h.IssueInvoice)
	invoice.Post("/void", rbac.RequirePermission("billing.invoice.void"), moneyLimiter, h.VoidInvoice)
	invoice.Post("/credit-note", rbac.RequirePermission("billing.invoice.creditnote"), moneyLimiter, h.CreateCreditNote)
	invoice.Post("/payments", rbac.RequirePermission("billing.payment.create"), moneyLimiter, h.RecordPaymentForInvoice)
	invoice.Get("/printable", rbac.RequirePermission("billing.invoice.view"), h.GetPrintable)

	// Payments routes
	payments := router.Group("/payments", authMiddleware)
	payments.Get("/", rbac.RequirePermission("billing.payment.view"), h.ListPayments)
	payments.Post("/", rbac.RequirePermission("billing.payment.create"), moneyLimiter, h.RecordPayment)
	payments.Get("/:id", rbac.RequirePermission("billing.payment.view"), h.GetPayment)
}

func (h *Handler) mapError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, ErrInvoiceNotFound), errors.Is(err, ErrPaymentNotFound):
		return httpx.NotFound(err.Error())
	case errors.Is(err, ErrCrossTenantAccess), errors.Is(err, ErrPermissionDenied):
		return httpx.Forbidden(err.Error())
	case errors.Is(err, ErrInvoiceNotDraft), errors.Is(err, ErrInvoiceAlreadyIssued),
		errors.Is(err, ErrInvoiceAlreadyPaid), errors.Is(err, ErrInvoiceAlreadyVoid),
		errors.Is(err, ErrInvoiceCannotVoid), errors.Is(err, ErrPaymentExceedsTotal),
		errors.Is(err, ErrInvoiceCannotPay), errors.Is(err, ErrAmountExceedsMaximum),
		errors.Is(err, ErrLineItemsRequired), errors.Is(err, ErrNegativeAmount),
		errors.Is(err, ErrVoidReasonRequired), errors.Is(err, ErrCreditNoteReasonRequired):
		return httpx.BadRequest(err.Error())
	default:
		return httpx.Internal(err)
	}
}

func (h *Handler) ListInvoices(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "20"))

	var custIDPtr *uuid.UUID
	if cidStr := c.Query("customer_org_id"); cidStr != "" {
		if cid, err := uuid.Parse(cidStr); err == nil {
			custIDPtr = &cid
		}
	}

	var statusPtr *string
	if s := c.Query("status"); s != "" {
		statusPtr = &s
	}

	var currPtr *string
	if cr := c.Query("currency"); cr != "" {
		currPtr = &cr
	}

	var startPtr, endPtr *time.Time
	if s := c.Query("start_date"); s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			startPtr = &t
		}
	}
	if e := c.Query("end_date"); e != "" {
		if t, err := time.Parse("2006-01-02", e); err == nil {
			endPtr = &t
		}
	}

	filter := InvoiceFilter{
		CustomerOrgID: custIDPtr,
		Status:        statusPtr,
		Currency:      currPtr,
		StartDate:     startPtr,
		EndDate:       endPtr,
		Search:        c.Query("search"),
		Page:          page,
		PageSize:      pageSize,
	}

	invoices, total, err := h.svc.ListInvoices(c.UserContext(), tc, filter)
	if err != nil {
		return h.mapError(c, err)
	}

	return c.JSON(fiber.Map{
		"data": fiber.Map{
			"items":     invoices,
			"total":     total,
			"page":      page,
			"page_size": pageSize,
		},
	})
}

func (h *Handler) CreateDraft(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	var req CreateDraftInvoiceRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("invalid request body")
	}

	inv, err := h.svc.CreateDraftInvoice(c.UserContext(), tc, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return h.mapError(c, err)
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"message": "Draft invoice created successfully",
		"data":    inv,
	})
}

func (h *Handler) GetInvoice(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid invoice id")
	}

	inv, err := h.svc.GetInvoice(c.UserContext(), tc, id)
	if err != nil {
		return h.mapError(c, err)
	}

	return c.JSON(fiber.Map{"data": inv})
}

func (h *Handler) UpdateDraft(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid invoice id")
	}

	var req UpdateDraftInvoiceRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("invalid request body")
	}

	inv, err := h.svc.UpdateDraftInvoice(c.UserContext(), tc, id, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return h.mapError(c, err)
	}

	return c.JSON(fiber.Map{
		"message": "Draft invoice updated successfully",
		"data":    inv,
	})
}

func (h *Handler) IssueInvoice(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid invoice id")
	}

	inv, err := h.svc.IssueInvoice(c.UserContext(), tc, id, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return h.mapError(c, err)
	}

	return c.JSON(fiber.Map{
		"message": "Invoice issued successfully",
		"data":    inv,
	})
}

func (h *Handler) VoidInvoice(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid invoice id")
	}

	var req struct {
		Reason string `json:"reason"`
	}
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("invalid request body")
	}

	cr, err := h.svc.VoidInvoice(c.UserContext(), tc, id, req.Reason, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return h.mapError(c, err)
	}

	if cr != nil {
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
			"message": "Void invoice change request submitted for checker approval",
			"data": fiber.Map{
				"approval_request_id": cr.ID,
				"status":              cr.Status,
			},
		})
	}

	return c.JSON(fiber.Map{
		"message": "Invoice voided successfully",
	})
}

func (h *Handler) CreateCreditNote(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid invoice id")
	}

	var req struct {
		Reason string `json:"reason"`
	}
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("invalid request body")
	}

	cn, err := h.svc.CreateCreditNote(c.UserContext(), tc, id, req.Reason, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return h.mapError(c, err)
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"message": "Credit note issued successfully",
		"data":    cn,
	})
}

func (h *Handler) GetPrintable(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid invoice id")
	}

	htmlDoc, err := h.svc.GetPrintableInvoiceHTML(c.UserContext(), tc, id)
	if err != nil {
		return h.mapError(c, err)
	}

	c.Set("Content-Type", "text/html; charset=utf-8")
	return c.SendString(htmlDoc)
}

func (h *Handler) RecordPayment(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	var req struct {
		InvoiceID     uuid.UUID            `json:"invoice_id"`
		PaymentRecord RecordPaymentRequest `json:"payment"`
	}
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("invalid request body")
	}

	payment, err := h.svc.RecordPayment(c.UserContext(), tc, req.InvoiceID, req.PaymentRecord, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return h.mapError(c, err)
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"message": "Payment recorded successfully",
		"data":    payment,
	})
}

func (h *Handler) RecordPaymentForInvoice(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	invID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid invoice id")
	}

	var req RecordPaymentRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("invalid request body")
	}

	payment, err := h.svc.RecordPayment(c.UserContext(), tc, invID, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return h.mapError(c, err)
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"message": "Payment recorded successfully",
		"data":    payment,
	})
}

func (h *Handler) ListPayments(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "20"))

	var invIDPtr *uuid.UUID
	if iStr := c.Query("invoice_id"); iStr != "" {
		if id, err := uuid.Parse(iStr); err == nil {
			invIDPtr = &id
		}
	}

	var custIDPtr *uuid.UUID
	if cStr := c.Query("customer_org_id"); cStr != "" {
		if id, err := uuid.Parse(cStr); err == nil {
			custIDPtr = &id
		}
	}

	payments, total, err := h.svc.ListPayments(c.UserContext(), tc, invIDPtr, custIDPtr, page, pageSize)
	if err != nil {
		return h.mapError(c, err)
	}

	return c.JSON(fiber.Map{
		"data": fiber.Map{
			"items":     payments,
			"total":     total,
			"page":      page,
			"page_size": pageSize,
		},
	})
}

func (h *Handler) GetPayment(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid payment id")
	}

	p, err := h.svc.GetPayment(c.UserContext(), tc, id)
	if err != nil {
		return h.mapError(c, err)
	}

	return c.JSON(fiber.Map{"data": p})
}
