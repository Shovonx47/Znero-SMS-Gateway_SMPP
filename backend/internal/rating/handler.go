package rating

import (
	"bytes"
	"encoding/csv"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/znero-international/znero/backend/pkg/tenant"
)

func getTC(c *fiber.Ctx) (*tenant.Context, error) {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil || tc == nil {
		return nil, fiber.NewError(fiber.StatusUnauthorized, "unauthorized")
	}
	return tc, nil
}

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Register(router fiber.Router, authMiddleware fiber.Handler) {
	var ratesGroup fiber.Router
	if authMiddleware != nil {
		ratesGroup = router.Group("/rates", authMiddleware)
	} else {
		ratesGroup = router.Group("/rates")
	}

	// Rate Cards
	ratesGroup.Get("/cards/export", h.ExportCards)
	ratesGroup.Get("/cards", h.ListCards)
	ratesGroup.Post("/cards", h.CreateCard)
	ratesGroup.Get("/cards/:id", h.GetCard)
	ratesGroup.Put("/cards/:id", h.UpdateCard)
	ratesGroup.Post("/cards/:id/archive", h.ArchiveCard)
	ratesGroup.Get("/cards/:id/history", h.GetCardHistory)
	ratesGroup.Post("/cards/:id/rollback", h.RollbackCard)

	// Rates
	ratesGroup.Get("", h.ListRates)
	ratesGroup.Get("/", h.ListRates)
	ratesGroup.Get("/cards/:id/rates/export", h.ExportRates)
	ratesGroup.Get("/cards/:id/rates", h.ListRates)
	ratesGroup.Post("/cards/:id/rates", h.CreateRate)
	ratesGroup.Get("/rates/:id", h.GetRate)
	ratesGroup.Put("/rates/:id", h.UpdateRate)
	ratesGroup.Post("/rates/:id/close", h.CloseRate)
	ratesGroup.Get("/rates/:id/history", h.GetRateHistory)
	ratesGroup.Post("/rates/:id/rollback", h.RollbackRate)

	// Assignments
	ratesGroup.Get("/assignments", h.ListAssignments)
	ratesGroup.Post("/assignments", h.CreateAssignment)
	ratesGroup.Post("/assignments/:id/deactivate", h.DeactivateAssignment)
	ratesGroup.Get("/assignments/:id/history", h.GetAssignmentHistory)

	// Reseller / Customer dedicated portal endpoints
	ratesGroup.Get("/my-cost", h.GetMyCostCard)
	ratesGroup.Get("/my-rates", h.GetMyRates)

	// Margin & Lookups
	ratesGroup.Get("/margin-check", h.CheckMargin)
	ratesGroup.Get("/margin-report", h.GetMarginReport)
	ratesGroup.Get("/lookup", h.LookupDestinationRate)
	ratesGroup.Get("/lookup-by-number", h.LookupByPhoneNumber)
	ratesGroup.Post("/bulk-lookup", h.BulkLookup)
	ratesGroup.Get("/upcoming", h.ListUpcomingRates)
}

func (h *Handler) RegisterRoutes(router fiber.Router) {
	h.Register(router, nil)
}

// -----------------------------------------------------------------------------
// RATE CARDS HANDLERS
// -----------------------------------------------------------------------------

func (h *Handler) ListCards(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}

	filter := RateCardListFilter{
		Kind:         c.Query("kind"),
		CurrencyCode: c.Query("currency"),
		Status:       c.Query("status"),
		Search:       c.Query("search"),
		SortBy:       c.Query("sort_by"),
		SortOrder:    c.Query("sort_order"),
	}
	if p, err := strconv.Atoi(c.Query("page", "1")); err == nil {
		filter.Page = p
	}
	if ps, err := strconv.Atoi(c.Query("page_size", "50")); err == nil {
		filter.PageSize = ps
	}
	if provStr := c.Query("provider_id"); provStr != "" {
		if pid, err := uuid.Parse(provStr); err == nil {
			filter.ProviderID = &pid
		}
	}
	if orgStr := c.Query("owner_org_id"); orgStr != "" {
		if oid, err := uuid.Parse(orgStr); err == nil {
			filter.OwnerOrgID = &oid
		}
	}

	cards, total, err := h.svc.ListCards(c.UserContext(), tc, filter)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyResource) {
			return fiber.NewError(fiber.StatusForbidden, err.Error())
		}
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}

	return c.JSON(fiber.Map{
		"items":       cards,
		"total_count": total,
		"page":        filter.Page,
		"page_size":   filter.PageSize,
	})
}

func (h *Handler) CreateCard(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}

	var req struct {
		Name         string     `json:"name"`
		Kind         string     `json:"kind"`
		OwnerOrgID   *uuid.UUID `json:"owner_org_id"`
		ProviderID   *uuid.UUID `json:"provider_id"`
		CurrencyCode string     `json:"currency_code"`
		Description  string     `json:"description"`
		Status       string     `json:"status"`
		Reason       string     `json:"reason"`
	}
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}
	if req.Name == "" || req.Kind == "" || req.CurrencyCode == "" {
		return fiber.NewError(fiber.StatusBadRequest, "name, kind, and currency_code are required")
	}

	card := &RateCard{
		Name:         req.Name,
		Kind:         req.Kind,
		ProviderID:   req.ProviderID,
		CurrencyCode: strings.ToUpper(req.CurrencyCode),
		Description:  req.Description,
		Status:       req.Status,
	}
	if req.OwnerOrgID != nil {
		card.OwnerOrgID = *req.OwnerOrgID
	}

	res, err := h.svc.CreateCard(c.UserContext(), tc, card, req.Reason)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyResource) || errors.Is(err, ErrPermissionDenied) {
			return fiber.NewError(fiber.StatusForbidden, err.Error())
		}
		if errors.Is(err, ErrInvalidCurrency) {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	if res.PendingChange != nil {
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
			"message":        "Rate card creation submitted for approval",
			"pending_change": res.PendingChange,
		})
	}
	return c.Status(fiber.StatusCreated).JSON(res.Card)
}

func (h *Handler) GetCard(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid id")
	}

	card, err := h.svc.GetCardByID(c.UserContext(), tc, id)
	if err != nil {
		if errors.Is(err, ErrRateCardNotFound) {
			return fiber.NewError(fiber.StatusNotFound, err.Error())
		}
		if errors.Is(err, ErrPlatformOnlyResource) || errors.Is(err, ErrCrossTenantAccess) {
			return fiber.NewError(fiber.StatusForbidden, err.Error())
		}
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	return c.JSON(card)
}

func (h *Handler) UpdateCard(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid id")
	}

	var req struct {
		Name         string `json:"name"`
		CurrencyCode string `json:"currency_code"`
		Status       string `json:"status"`
		Description  string `json:"description"`
		Archived     bool   `json:"archived"`
		Version      int    `json:"version"`
		Reason       string `json:"reason"`
	}
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	card := &RateCard{
		ID:           id,
		Name:         req.Name,
		CurrencyCode: strings.ToUpper(req.CurrencyCode),
		Status:       req.Status,
		Description:  req.Description,
		Archived:     req.Archived,
		Version:      req.Version,
	}

	res, err := h.svc.UpdateCard(c.UserContext(), tc, card, req.Reason)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyResource) || errors.Is(err, ErrCrossTenantAccess) || errors.Is(err, ErrPermissionDenied) {
			return fiber.NewError(fiber.StatusForbidden, err.Error())
		}
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	if res.PendingChange != nil {
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
			"message":        "Rate card update submitted for approval",
			"pending_change": res.PendingChange,
		})
	}
	return c.JSON(res.Card)
}

func (h *Handler) ArchiveCard(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid id")
	}

	var req struct {
		Reason string `json:"reason"`
	}
	_ = c.BodyParser(&req)

	res, err := h.svc.ArchiveCard(c.UserContext(), tc, id, req.Reason)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}
	if res.PendingChange != nil {
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
			"message":        "Rate card archiving submitted for approval",
			"pending_change": res.PendingChange,
		})
	}
	return c.JSON(res.Card)
}

func (h *Handler) GetCardHistory(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid id")
	}

	history, err := h.svc.ListCardHistory(c.UserContext(), tc, id)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(history)
}

func (h *Handler) RollbackCard(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid id")
	}

	var req struct {
		TargetVersion int    `json:"target_version"`
		Reason        string `json:"reason"`
	}
	if err := c.BodyParser(&req); err != nil || req.TargetVersion <= 0 {
		return fiber.NewError(fiber.StatusBadRequest, "valid target_version is required")
	}

	res, err := h.svc.RollbackCard(c.UserContext(), tc, id, req.TargetVersion, req.Reason)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}
	if res.PendingChange != nil {
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
			"message":        "Rate card rollback submitted for approval",
			"pending_change": res.PendingChange,
		})
	}
	return c.JSON(res.Card)
}

func (h *Handler) ExportCards(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	if tc == nil {
		return fiber.NewError(fiber.StatusUnauthorized, "unauthorized")
	}

	format := strings.ToLower(c.Query("format", "csv"))
	filter := RateCardListFilter{
		Kind:         c.Query("kind"),
		CurrencyCode: c.Query("currency"),
		Status:       c.Query("status"),
		Search:       c.Query("search"),
		Page:         1,
		PageSize:     5000,
	}

	cards, _, err := h.svc.ListCards(c.UserContext(), tc, filter)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	if format == "json" {
		c.Set("Content-Type", "application/json")
		c.Set("Content-Disposition", `attachment; filename="rate_cards_export.json"`)
		return c.JSON(cards)
	}

	// CSV Export with formula injection sanitization
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"ID", "Name", "Kind", "Currency", "Channel", "Status", "Description", "Archived", "Version"})
	for _, cd := range cards {
		_ = w.Write([]string{
			escapeCSV(cd.ID.String()),
			escapeCSV(cd.Name),
			escapeCSV(cd.Kind),
			escapeCSV(cd.CurrencyCode),
			escapeCSV(cd.Channel),
			escapeCSV(cd.Status),
			escapeCSV(cd.Description),
			strconv.FormatBool(cd.Archived),
			strconv.Itoa(cd.Version),
		})
	}
	w.Flush()

	c.Set("Content-Type", "text/csv")
	c.Set("Content-Disposition", `attachment; filename="rate_cards_export.csv"`)
	return c.Send(buf.Bytes())
}

// -----------------------------------------------------------------------------
// RATES HANDLERS
// -----------------------------------------------------------------------------

func (h *Handler) ListRates(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	cardIDStr := c.Params("id")
	if cardIDStr == "" {
		cardIDStr = c.Query("card_id")
	}
	cardID, err := uuid.Parse(cardIDStr)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid card id")
	}

	filter := RateListFilter{
		CardID:      cardID,
		MessageType: c.Query("message_type"),
		Status:      c.Query("status"),
		Search:      c.Query("search"),
	}
	if p, err := strconv.Atoi(c.Query("page", "1")); err == nil {
		filter.Page = p
	}
	if ps, err := strconv.Atoi(c.Query("page_size", "50")); err == nil {
		filter.PageSize = ps
	}
	if cStr := c.Query("country_id"); cStr != "" {
		if cid, err := uuid.Parse(cStr); err == nil {
			filter.CountryID = &cid
		}
	}
	if opStr := c.Query("operator_id"); opStr != "" {
		if opid, err := uuid.Parse(opStr); err == nil {
			filter.OperatorID = &opid
		}
	}
	if atStr := c.Query("effective_at"); atStr != "" {
		if t, err := time.Parse(time.RFC3339, atStr); err == nil {
			filter.EffectiveAt = &t
		}
	}

	rates, total, err := h.svc.ListRates(c.UserContext(), tc, filter)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyResource) || errors.Is(err, ErrCrossTenantAccess) {
			return fiber.NewError(fiber.StatusForbidden, err.Error())
		}
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}

	return c.JSON(fiber.Map{
		"items":       rates,
		"total_count": total,
		"page":        filter.Page,
		"page_size":   filter.PageSize,
	})
}

func (h *Handler) CreateRate(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	cardID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid card id")
	}

	var req struct {
		CountryID      uuid.UUID       `json:"country_id"`
		OperatorID     *uuid.UUID      `json:"operator_id"`
		MessageType    string          `json:"message_type"`
		Price          decimal.Decimal `json:"price"`
		MinBillingNote string          `json:"min_billing_note"`
		EffectiveFrom  time.Time       `json:"effective_from"`
		EffectiveTo    *time.Time      `json:"effective_to"`
		Reason         string          `json:"reason"`
	}
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}
	if req.CountryID == uuid.Nil {
		return fiber.NewError(fiber.StatusBadRequest, "country_id is required")
	}
	if req.EffectiveFrom.IsZero() {
		req.EffectiveFrom = time.Now().UTC()
	}

	rate := &Rate{
		CardID:         cardID,
		CountryID:      req.CountryID,
		OperatorID:     req.OperatorID,
		MessageType:    req.MessageType,
		Price:          req.Price,
		MinBillingNote: req.MinBillingNote,
		EffectiveFrom:  req.EffectiveFrom,
		EffectiveTo:    req.EffectiveTo,
		Source:         "manual",
		Status:         "active",
	}

	res, err := h.svc.CreateRate(c.UserContext(), tc, rate, req.Reason)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyResource) || errors.Is(err, ErrCrossTenantAccess) || errors.Is(err, ErrPermissionDenied) {
			return fiber.NewError(fiber.StatusForbidden, err.Error())
		}
		if errors.Is(err, ErrEffectiveDateOverlap) {
			return fiber.NewError(fiber.StatusConflict, err.Error())
		}
		if errors.Is(err, ErrMarginViolation) {
			return fiber.NewError(fiber.StatusUnprocessableEntity, err.Error())
		}
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	if res.PendingChange != nil {
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
			"message":        "Rate creation submitted for approval",
			"pending_change": res.PendingChange,
		})
	}
	return c.Status(fiber.StatusCreated).JSON(res.Rate)
}

func (h *Handler) GetRate(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid id")
	}

	rt, err := h.svc.GetRateByID(c.UserContext(), tc, id)
	if err != nil {
		if errors.Is(err, ErrRateNotFound) {
			return fiber.NewError(fiber.StatusNotFound, err.Error())
		}
		if errors.Is(err, ErrPlatformOnlyResource) || errors.Is(err, ErrCrossTenantAccess) {
			return fiber.NewError(fiber.StatusForbidden, err.Error())
		}
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	return c.JSON(rt)
}

func (h *Handler) UpdateRate(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid id")
	}

	var req struct {
		Price          decimal.Decimal `json:"price"`
		MinBillingNote string          `json:"min_billing_note"`
		EffectiveTo    *time.Time      `json:"effective_to"`
		Status         string          `json:"status"`
		Version        int             `json:"version"`
		Reason         string          `json:"reason"`
	}
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	rate := &Rate{
		ID:             id,
		Price:          req.Price,
		MinBillingNote: req.MinBillingNote,
		EffectiveTo:    req.EffectiveTo,
		Status:         req.Status,
		Version:        req.Version,
	}

	res, err := h.svc.UpdateRate(c.UserContext(), tc, rate, req.Reason)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyResource) || errors.Is(err, ErrCrossTenantAccess) || errors.Is(err, ErrPermissionDenied) {
			return fiber.NewError(fiber.StatusForbidden, err.Error())
		}
		if errors.Is(err, ErrEffectiveDateOverlap) {
			return fiber.NewError(fiber.StatusConflict, err.Error())
		}
		if errors.Is(err, ErrMarginViolation) {
			return fiber.NewError(fiber.StatusUnprocessableEntity, err.Error())
		}
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	if res.PendingChange != nil {
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
			"message":        "Rate update submitted for approval",
			"pending_change": res.PendingChange,
		})
	}
	return c.JSON(res.Rate)
}

func (h *Handler) CloseRate(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid id")
	}

	var req struct {
		EffectiveTo time.Time `json:"effective_to"`
		Reason      string    `json:"reason"`
	}
	_ = c.BodyParser(&req)

	res, err := h.svc.CloseRate(c.UserContext(), tc, id, req.EffectiveTo, req.Reason)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}
	if res.PendingChange != nil {
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
			"message":        "Rate close submitted for approval",
			"pending_change": res.PendingChange,
		})
	}
	return c.JSON(res.Rate)
}

func (h *Handler) GetRateHistory(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid id")
	}

	hist, err := h.svc.ListRateHistory(c.UserContext(), tc, id)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(hist)
}

func (h *Handler) RollbackRate(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid id")
	}

	var req struct {
		TargetVersion int    `json:"target_version"`
		Reason        string `json:"reason"`
	}
	if err := c.BodyParser(&req); err != nil || req.TargetVersion <= 0 {
		return fiber.NewError(fiber.StatusBadRequest, "valid target_version is required")
	}

	res, err := h.svc.RollbackRate(c.UserContext(), tc, id, req.TargetVersion, req.Reason)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}
	if res.PendingChange != nil {
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
			"message":        "Rate rollback submitted for approval",
			"pending_change": res.PendingChange,
		})
	}
	return c.JSON(res.Rate)
}

func (h *Handler) ExportRates(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	cardID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid card id")
	}

	format := strings.ToLower(c.Query("format", "csv"))
	filter := RateListFilter{
		CardID:      cardID,
		MessageType: c.Query("message_type"),
		Status:      c.Query("status"),
		Search:      c.Query("search"),
		Page:        1,
		PageSize:    10000,
	}

	rates, _, err := h.svc.ListRates(c.UserContext(), tc, filter)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	if format == "json" {
		c.Set("Content-Type", "application/json")
		c.Set("Content-Disposition", `attachment; filename="rates_export.json"`)
		return c.JSON(rates)
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"CountryISO2", "CountryName", "OperatorMCC", "OperatorMNC", "OperatorName", "MessageType", "Price", "EffectiveFrom", "EffectiveTo", "Status"})
	for _, rt := range rates {
		toStr := ""
		if rt.EffectiveTo != nil {
			toStr = rt.EffectiveTo.Format(time.RFC3339)
		}
		_ = w.Write([]string{
			escapeCSV(rt.CountryISO2),
			escapeCSV(rt.CountryName),
			escapeCSV(safeStr(rt.OperatorMCC)),
			escapeCSV(safeStr(rt.OperatorMNC)),
			escapeCSV(safeStr(rt.OperatorName)),
			escapeCSV(rt.MessageType),
			rt.Price.String(),
			rt.EffectiveFrom.Format(time.RFC3339),
			toStr,
			escapeCSV(rt.Status),
		})
	}
	w.Flush()

	c.Set("Content-Type", "text/csv")
	c.Set("Content-Disposition", `attachment; filename="rates_export.csv"`)
	return c.Send(buf.Bytes())
}

// -----------------------------------------------------------------------------
// ASSIGNMENT HANDLERS
// -----------------------------------------------------------------------------

func (h *Handler) ListAssignments(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	if tc == nil {
		return fiber.NewError(fiber.StatusUnauthorized, "unauthorized")
	}

	var targetOrgID *uuid.UUID
	if orgStr := c.Query("target_org_id"); orgStr != "" {
		if oid, err := uuid.Parse(orgStr); err == nil {
			targetOrgID = &oid
		}
	}

	if c.Query("is_reseller") == "true" {
		assignments, err := h.svc.ListResellerCostAssignments(c.UserContext(), tc, targetOrgID)
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.JSON(assignments)
	}

	assignments, err := h.svc.ListCustomerAssignments(c.UserContext(), tc, targetOrgID)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(assignments)
}

func (h *Handler) CreateAssignment(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	if tc == nil {
		return fiber.NewError(fiber.StatusUnauthorized, "unauthorized")
	}

	var req struct {
		TargetOrgID   uuid.UUID  `json:"target_org_id"`
		CardID        uuid.UUID  `json:"card_id"`
		EffectiveFrom time.Time  `json:"effective_from"`
		EffectiveTo   *time.Time `json:"effective_to"`
		IsReseller    bool       `json:"is_reseller"`
		Reason        string     `json:"reason"`
	}
	if err := c.BodyParser(&req); err != nil || req.TargetOrgID == uuid.Nil || req.CardID == uuid.Nil {
		return fiber.NewError(fiber.StatusBadRequest, "valid target_org_id and card_id are required")
	}

	if req.IsReseller {
		res, err := h.svc.CreateResellerCostAssignment(c.UserContext(), tc, req.TargetOrgID, req.CardID, req.EffectiveFrom, req.EffectiveTo, req.Reason)
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		if res.PendingChange != nil {
			return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
				"message":        "Reseller cost assignment submitted for approval",
				"pending_change": res.PendingChange,
			})
		}
		return c.Status(fiber.StatusCreated).JSON(res.ResellerAssign)
	}

	res, err := h.svc.CreateAssignment(c.UserContext(), tc, req.TargetOrgID, req.CardID, req.EffectiveFrom, req.EffectiveTo, req.Reason)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}
	if res.PendingChange != nil {
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
			"message":        "Customer assignment submitted for approval",
			"pending_change": res.PendingChange,
		})
	}
	return c.Status(fiber.StatusCreated).JSON(res.CustomerAssign)
}

func (h *Handler) DeactivateAssignment(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid id")
	}

	var req struct {
		IsCustomer bool   `json:"is_customer"`
		Reason     string `json:"reason"`
	}
	_ = c.BodyParser(&req)

	res, err := h.svc.DeactivateAssignment(c.UserContext(), tc, id, req.IsCustomer, req.Reason)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}
	if res.PendingChange != nil {
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
			"message":        "Assignment deactivation submitted for approval",
			"pending_change": res.PendingChange,
		})
	}
	return c.JSON(fiber.Map{"status": "deactivated"})
}

func (h *Handler) GetAssignmentHistory(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	_ = tc
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid id")
	}

	isCustomer := c.Query("type") != "reseller"
	if isCustomer {
		hist, err := h.svc.repo.ListCustomerAssignmentHistory(c.UserContext(), id)
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
		return c.JSON(hist)
	}
	hist, err := h.svc.repo.ListResellerCostAssignmentHistory(c.UserContext(), id)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(hist)
}

func (h *Handler) GetMyCostCard(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	if tc == nil {
		return fiber.NewError(fiber.StatusUnauthorized, "unauthorized")
	}

	card, err := h.svc.GetResellerAssignedCostCard(c.UserContext(), tc, tc.ActorOrgID)
	if err != nil {
		if errors.Is(err, ErrAssignmentNotFound) {
			return fiber.NewError(fiber.StatusNotFound, "no active cost card assigned to your organization")
		}
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	filter := RateListFilter{
		CardID:   card.ID,
		Status:   "active",
		Search:   c.Query("search"),
		Page:     1,
		PageSize: 500,
	}
	rates, _, _ := h.svc.ListRates(c.UserContext(), tc, filter)

	return c.JSON(fiber.Map{
		"card":  card,
		"rates": rates,
	})
}

func (h *Handler) GetMyRates(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	if tc == nil {
		return fiber.NewError(fiber.StatusUnauthorized, "unauthorized")
	}

	// Find customer active assignment
	assign, err := h.svc.repo.GetActiveCustomerAssignmentAt(c.UserContext(), tc.ActorOrgID, time.Now().UTC())
	if err != nil || assign == nil {
		return fiber.NewError(fiber.StatusNotFound, "no active sell rate card assigned to your organization")
	}

	card, _ := h.svc.repo.GetCardByID(c.UserContext(), assign.CardID)

	filter := RateListFilter{
		CardID:   assign.CardID,
		Status:   "active",
		Search:   c.Query("search"),
		Page:     1,
		PageSize: 500,
	}
	rates, total, err := h.svc.ListRates(c.UserContext(), tc, filter)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	return c.JSON(fiber.Map{
		"card":        card,
		"card_id":     assign.CardID,
		"card_name":   assign.CardName,
		"rates":       rates,
		"items":       rates,
		"total_count": total,
	})
}

// -----------------------------------------------------------------------------
// MARGIN & LOOKUP HANDLERS
// -----------------------------------------------------------------------------

func (h *Handler) CheckMargin(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	if tc == nil {
		return fiber.NewError(fiber.StatusUnauthorized, "unauthorized")
	}

	cardID, err := uuid.Parse(c.Query("card_id"))
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "card_id is required")
	}
	countryID, err := uuid.Parse(c.Query("country_id"))
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "country_id is required")
	}
	var opID *uuid.UUID
	if opStr := c.Query("operator_id"); opStr != "" {
		if oid, err := uuid.Parse(opStr); err == nil {
			opID = &oid
		}
	}
	msgType := c.Query("message_type", "default")
	priceStr := c.Query("sell_price")
	if priceStr == "" {
		priceStr = c.Query("price")
	}
	priceDec, err := decimal.NewFromString(priceStr)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "valid price is required")
	}
	currency := c.Query("currency", "USD")

	res, err := h.svc.CheckMargin(c.UserContext(), cardID, countryID, opID, msgType, priceDec, currency)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	return c.JSON(res)
}

func (h *Handler) GetMarginReport(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	if tc == nil {
		return fiber.NewError(fiber.StatusUnauthorized, "unauthorized")
	}

	filter := MarginReportFilter{
		OnlyDeficit: c.Query("deficit") == "true",
	}
	if p, err := strconv.Atoi(c.Query("page", "1")); err == nil {
		filter.Page = p
	}
	if ps, err := strconv.Atoi(c.Query("page_size", "50")); err == nil {
		filter.PageSize = ps
	}
	if cardStr := c.Query("card_id"); cardStr != "" {
		if cid, err := uuid.Parse(cardStr); err == nil {
			filter.CardID = &cid
		}
	}
	if cStr := c.Query("country_id"); cStr != "" {
		if cid, err := uuid.Parse(cStr); err == nil {
			filter.CountryID = &cid
		}
	}

	items, total, err := h.svc.GetMarginReport(c.UserContext(), tc, filter)
	if err != nil {
		if errors.Is(err, ErrPlatformOnlyResource) {
			return fiber.NewError(fiber.StatusForbidden, err.Error())
		}
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}

	if c.Query("format") == "csv" {
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		_ = w.Write([]string{"CardName", "CountryISO2", "CountryName", "OperatorName", "MessageType", "Currency", "SellPrice", "LowestBuyCost", "MarginAmount", "MarginPercent", "Violation"})
		for _, it := range items {
			_ = w.Write([]string{
				escapeCSV(it.CardName),
				escapeCSV(it.CountryISO2),
				escapeCSV(it.CountryName),
				escapeCSV(safeStr(it.OperatorName)),
				escapeCSV(it.MessageType),
				escapeCSV(it.Currency),
				it.SellPrice.String(),
				it.LowestBuyCost.String(),
				it.MarginAmount.String(),
				it.MarginPercent.String() + "%",
				strconv.FormatBool(it.Violation),
			})
		}
		w.Flush()
		c.Set("Content-Type", "text/csv")
		c.Set("Content-Disposition", `attachment; filename="margin_report.csv"`)
		return c.Send(buf.Bytes())
	}

	return c.JSON(fiber.Map{
		"items":       items,
		"total_count": total,
		"page":        filter.Page,
		"page_size":   filter.PageSize,
	})
}

func (h *Handler) LookupDestinationRate(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	if tc == nil {
		return fiber.NewError(fiber.StatusUnauthorized, "unauthorized")
	}

	customerOrgID := tc.ActorOrgID
	if tc.IsPlatformOrg() && c.Query("customer") != "" {
		if cid, err := uuid.Parse(c.Query("customer")); err == nil {
			customerOrgID = cid
		}
	}

	countryID, err := uuid.Parse(c.Query("country"))
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "country is required and must be a valid UUID")
	}

	var operatorID *uuid.UUID
	if opStr := c.Query("operator"); opStr != "" {
		if opid, err := uuid.Parse(opStr); err == nil {
			operatorID = &opid
		}
	}

	msgType := c.Query("msg_type", "default")
	at := time.Now().UTC()
	if atStr := c.Query("at"); atStr != "" {
		if t, err := time.Parse(time.RFC3339, atStr); err == nil {
			at = t
		}
	}

	match, err := h.svc.LookupSellRate(c.UserContext(), customerOrgID, countryID, operatorID, msgType, at)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	return c.JSON(match)
}

func (h *Handler) LookupByPhoneNumber(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	if tc == nil {
		return fiber.NewError(fiber.StatusUnauthorized, "unauthorized")
	}

	customerOrgID := tc.ActorOrgID
	if tc.IsPlatformOrg() && c.Query("customer") != "" {
		if cid, err := uuid.Parse(c.Query("customer")); err == nil {
			customerOrgID = cid
		}
	}

	number := c.Query("number")
	if number == "" {
		return fiber.NewError(fiber.StatusBadRequest, "number query parameter is required")
	}

	msgType := c.Query("msg_type", "default")
	at := time.Now().UTC()
	if atStr := c.Query("at"); atStr != "" {
		if t, err := time.Parse(time.RFC3339, atStr); err == nil {
			at = t
		}
	}

	match, err := h.svc.LookupByPhoneNumber(c.UserContext(), customerOrgID, number, msgType, at)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	return c.JSON(match)
}

func (h *Handler) BulkLookup(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	if tc == nil {
		return fiber.NewError(fiber.StatusUnauthorized, "unauthorized")
	}

	var items []BulkLookupItem
	if err := c.BodyParser(&items); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "request body must be an array of lookup items")
	}
	if len(items) > 1000 {
		return fiber.NewError(fiber.StatusBadRequest, "bulk lookup exceeds maximum of 1000 numbers")
	}

	// Fill customer org ID for non-platform users
	if !tc.IsPlatformOrg() {
		for i := range items {
			items[i].CustomerOrgID = tc.ActorOrgID
		}
	}

	results, err := h.svc.BulkLookup(c.UserContext(), tc, items)
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(results)
}

func (h *Handler) ListUpcomingRates(c *fiber.Ctx) error {
	tc, err := getTC(c)
	if err != nil {
		return err
	}
	days := 30
	if d, err := strconv.Atoi(c.Query("days", "30")); err == nil && d > 0 {
		days = d
	}

	rates, err := h.svc.ListUpcomingRates(c.UserContext(), tc, days)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	return c.JSON(rates)
}

func escapeCSV(s string) string {
	if s == "" {
		return ""
	}
	// Formula injection protection: prefix with single quote if starts with dangerous character
	if s[0] == '=' || s[0] == '+' || s[0] == '-' || s[0] == '@' {
		return "'" + s
	}
	return s
}
