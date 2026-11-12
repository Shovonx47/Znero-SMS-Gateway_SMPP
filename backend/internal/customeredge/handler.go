package customeredge

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/provider"
	"github.com/znero-international/znero/backend/internal/rating"
	"github.com/znero-international/znero/backend/internal/secrets"
)

// EdgeHandler serves public customer-facing HTTP REST APIs (/api/v1/customer).
type EdgeHandler struct {
	auth          *EdgeAuthenticator
	limiter       *CustomerLimiter
	msgSvc        *messaging.Service
	repo          messaging.Repository
	ledgerSvc     *ledger.Service
	ratingSvc     *rating.Service
	keyring       *secrets.KeyRing
	pool          *pgxpool.Pool
	appEnv        string
	allowLoopback bool
}

// NewEdgeHandler constructs a new EdgeHandler.
func NewEdgeHandler(
	auth *EdgeAuthenticator,
	limiter *CustomerLimiter,
	msgSvc *messaging.Service,
	repo messaging.Repository,
	ledgerSvc *ledger.Service,
	ratingSvc *rating.Service,
	keyring *secrets.KeyRing,
	pool *pgxpool.Pool,
) *EdgeHandler {
	return &EdgeHandler{
		auth:      auth,
		limiter:   limiter,
		msgSvc:    msgSvc,
		repo:      repo,
		ledgerSvc: ledgerSvc,
		ratingSvc: ratingSvc,
		keyring:   keyring,
		pool:      pool,
	}
}

// SetAllowLoopback configures loopback webhook exceptions strictly outside production.
func (h *EdgeHandler) SetAllowLoopback(appEnv string, allow bool) {
	h.appEnv = strings.TrimSpace(appEnv)
	h.allowLoopback = allow && !strings.EqualFold(h.appEnv, "production")
}

func (h *EdgeHandler) validateWebhookURL(rawURL string) error {
	if strings.ContainsAny(rawURL, "\r\n\x00") {
		return fmt.Errorf("control characters are not allowed in webhook URL")
	}
	env := h.appEnv
	if env == "" {
		env = "production"
	}
	return provider.ValidateHTTPBaseURLWithOptions(rawURL, "http", env, "active", h.allowLoopback)
}

// RegisterRoutes mounts all customer edge endpoints onto the provided Fiber router.
func (h *EdgeHandler) RegisterRoutes(router fiber.Router) {
	// Public Edge Root with edge authentication middleware
	edge := router.Group("/customer", h.edgeAuthMiddleware)

	// SMS Submissions & Queries
	edge.Post("/sms", h.requireScope(ScopeSMSSend), h.submitSingle)
	edge.Post("/sms/bulk", h.requireScope(ScopeSMSSend), h.submitBulk)
	edge.Post("/sms/estimate", h.requireScope(ScopeSMSRead), h.estimateSMS)
	edge.Get("/sms/:id", h.requireScope(ScopeSMSRead), h.getMessage)
	edge.Get("/sms", h.requireScope(ScopeSMSRead), h.listMessages)

	// Account, Balance & Rates
	edge.Get("/balance", h.requireScope(ScopeAccountRead), h.getBalance)
	edge.Get("/rates", h.requireScope(ScopeAccountRead), h.listRates)
	edge.Get("/account", h.requireScope(ScopeAccountRead), h.getAccount)

	// Webhooks Management
	edge.Post("/webhooks", h.requireScope(ScopeAccountRead), h.createWebhook)
	edge.Get("/webhooks", h.requireScope(ScopeAccountRead), h.listWebhooks)
	edge.Get("/webhooks/:id", h.requireScope(ScopeAccountRead), h.getWebhook)
	edge.Put("/webhooks/:id", h.requireScope(ScopeAccountRead), h.updateWebhook)
	edge.Delete("/webhooks/:id", h.requireScope(ScopeAccountRead), h.deleteWebhook)
	edge.Post("/webhooks/:id/test", h.requireScope(ScopeAccountRead), h.testWebhook)
	edge.Get("/webhooks/:id/deliveries", h.requireScope(ScopeAccountRead), h.listWebhookDeliveries)

	// Inbound MO Messages
	edge.Get("/inbound", h.requireScope(ScopeInboundRead), h.listInboundMessages)
}

// Edge Authentication Middleware
func (h *EdgeHandler) edgeAuthMiddleware(c *fiber.Ctx) error {
	authHeader := c.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return c.Status(ErrUnauthorized.HTTPStatus).JSON(ErrUnauthorized)
	}

	token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
	if token == "" {
		return c.Status(ErrUnauthorized.HTTPStatus).JSON(ErrUnauthorized)
	}

	clientIP := h.auth.ExtractClientIP(c.IP(), c.Get("X-Forwarded-For"))
	principal, err := h.auth.AuthenticateAPIKey(c.Context(), token, clientIP)
	if err != nil {
		var edgeErr *PublicEdgeError
		if errors.As(err, &edgeErr) {
			return c.Status(edgeErr.HTTPStatus).JSON(edgeErr)
		}
		return c.Status(ErrUnauthorized.HTTPStatus).JSON(ErrUnauthorized)
	}

	c.Locals("edge_principal", principal)
	return c.Next()
}

// Scope Guard Helper
func (h *EdgeHandler) requireScope(scope string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		p, ok := c.Locals("edge_principal").(*Principal)
		if !ok || p == nil {
			return c.Status(ErrUnauthorized.HTTPStatus).JSON(ErrUnauthorized)
		}
		if !p.HasScope(scope) {
			return c.Status(ErrMissingScope.HTTPStatus).JSON(ErrMissingScope)
		}
		return c.Next()
	}
}

// DTO Requests & Responses
type CustomerSubmitRequest struct {
	Destination     string  `json:"destination"`
	SenderID        string  `json:"sender_id"`
	Text            string  `json:"text"`
	ClientReference *string `json:"client_reference,omitempty"`
	TrafficProfile  string  `json:"traffic_profile,omitempty"`
	ValiditySeconds *int    `json:"validity_seconds,omitempty"`
	DLRRequested    bool    `json:"dlr_requested,omitempty"`
}

type CustomerSubmitResponse struct {
	ID              string          `json:"id"`
	ClientReference *string         `json:"client_reference,omitempty"`
	Status          string          `json:"status"`
	Destination     string          `json:"destination"`
	SenderID        string          `json:"sender_id"`
	Parts           int             `json:"parts"`
	Price           decimal.Decimal `json:"price"`
	Currency        string          `json:"currency"`
	CreatedAt       string          `json:"created_at"`
}

type BulkSubmitRequest struct {
	Messages []CustomerSubmitRequest `json:"messages"`
}

type BulkItemResult struct {
	Index   int                     `json:"index"`
	Success bool                    `json:"success"`
	Data    *CustomerSubmitResponse `json:"data,omitempty"`
	Error   *PublicEdgeError        `json:"error,omitempty"`
}

type BulkSubmitResponse struct {
	Total    int              `json:"total"`
	Accepted int              `json:"accepted"`
	Failed   int              `json:"failed"`
	Items    []BulkItemResult `json:"items"`
}

type EstimateRequest struct {
	Destination    string `json:"destination"`
	SenderID       string `json:"sender_id"`
	Text           string `json:"text"`
	TrafficProfile string `json:"traffic_profile,omitempty"`
}

type EstimateResponse struct {
	Destination    string          `json:"destination"`
	Encoding       string          `json:"encoding"`
	Parts          int             `json:"parts"`
	CharacterCount int             `json:"character_count"`
	EstimatedPrice decimal.Decimal `json:"estimated_price"`
	Currency       string          `json:"currency"`
}

type BalanceResponse struct {
	Balance     decimal.Decimal `json:"balance"`
	ActiveHolds decimal.Decimal `json:"active_holds"`
	Available   decimal.Decimal `json:"available"`
	CreditLimit decimal.Decimal `json:"credit_limit"`
	Currency    string          `json:"currency"`
}

type BillingTermsSummary struct {
	BillingMode             string `json:"billing_mode"`
	DLRWaitHours            int    `json:"dlr_wait_hours"`
	NoDLRPolicy             string `json:"no_dlr_policy"`
	FailedAfterAcceptPolicy string `json:"failed_after_accept_policy"`
}

type AccountResponse struct {
	OrganizationID string              `json:"organization_id"`
	DailyUsage     int64               `json:"daily_usage"`
	DailyCap       int64               `json:"daily_cap"`
	MonthlyUsage   int64               `json:"monthly_usage"`
	MonthlyCap     int64               `json:"monthly_cap"`
	TPSLimit       int                 `json:"tps_limit"`
	BillingTerms   BillingTermsSummary `json:"billing_terms"`
}

// computeFingerprint hashes payload invariant fields for idempotency validation.
func computeFingerprint(dest, sender, text, profile string) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s|%s|%s|%s", strings.TrimSpace(dest), strings.TrimSpace(sender), text, profile)
	return hex.EncodeToString(h.Sum(nil))
}

// POST /api/v1/customer/sms
func (h *EdgeHandler) submitSingle(c *fiber.Ctx) error {
	principal := c.Locals("edge_principal").(*Principal)

	// Token bucket TPS check
	allowed, retryAfter := h.limiter.AllowN(principal.CustomerOrgID, principal.MaxTPS, 1)
	if !allowed {
		c.Set("Retry-After", strconv.Itoa(retryAfter))
		return c.Status(ErrRateLimitExceeded.HTTPStatus).JSON(ErrRateLimitExceeded)
	}

	var req CustomerSubmitRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(ErrValidation.HTTPStatus).JSON(&PublicEdgeError{
			HTTPStatus: http.StatusBadRequest,
			Code:       "invalid_json",
			Message:    "Unable to parse request body: " + err.Error(),
		})
	}

	if strings.TrimSpace(req.Destination) == "" || strings.TrimSpace(req.SenderID) == "" || req.Text == "" {
		return c.Status(ErrValidation.HTTPStatus).JSON(&PublicEdgeError{
			HTTPStatus: http.StatusBadRequest,
			Code:       "missing_required_fields",
			Message:    "destination, sender_id, and text are required fields",
		})
	}

	fingerprint := computeFingerprint(req.Destination, req.SenderID, req.Text, req.TrafficProfile)

	// Global Idempotency Check with fingerprint validation
	if req.ClientReference != nil && *req.ClientReference != "" {
		existingID, existingCreatedAt, existingFingerprint, idempErr := h.repo.CheckIdempotencyWithFingerprint(c.Context(), principal.CustomerOrgID, *req.ClientReference)
		if idempErr == nil && existingID != nil {
			if existingFingerprint != nil && *existingFingerprint != fingerprint {
				return c.Status(ErrReferenceConflict.HTTPStatus).JSON(ErrReferenceConflict)
			}
			// Safe idempotent replay: fetch existing message and return original response
			existingMsg, _, getErr := h.repo.GetMessage(c.Context(), *existingID, *existingCreatedAt)
			if getErr == nil && existingMsg != nil {
				return c.Status(http.StatusOK).JSON(CustomerSubmitResponse{
					ID:              existingMsg.ID.String(),
					ClientReference: existingMsg.ClientReference,
					Status:          MapInternalToCustomerStatus(existingMsg.Status).Status,
					Destination:     existingMsg.Destination,
					SenderID:        existingMsg.SenderID,
					Parts:           existingMsg.TotalParts,
					Price:           existingMsg.SellPrice,
					Currency:        existingMsg.SellCurrency,
					CreatedAt:       existingMsg.CreatedAt.UTC().Format(time.RFC3339),
				})
			}
		}
	}

	// Prepare messaging SubmitRequest
	subReq := messaging.SubmitRequest{
		Destination:        req.Destination,
		SenderID:           req.SenderID,
		Text:               req.Text,
		ClientReference:    req.ClientReference,
		TrafficProfile:     req.TrafficProfile,
		ValiditySeconds:    req.ValiditySeconds,
		IngressChannel:     "http",
		IngressAccountID:   principal.APIKeyID,
		DLRRequested:       req.DLRRequested,
		RequestFingerprint: fingerprint,
	}

	resp, err := h.msgSvc.Submit(c.Context(), principal.ToTenantContext(), subReq)
	if err != nil {
		mapped := MapSubmitError(err)
		return c.Status(mapped.HTTPStatus).JSON(mapped)
	}

	return c.Status(http.StatusCreated).JSON(CustomerSubmitResponse{
		ID:              resp.MessageID.String(),
		ClientReference: resp.ClientReference,
		Status:          MapInternalToCustomerStatus(resp.Status).Status,
		Destination:     resp.Destination,
		SenderID:        resp.SenderID,
		Parts:           resp.Parts,
		Price:           resp.Price,
		Currency:        resp.Currency,
		CreatedAt:       resp.CreatedAt.UTC().Format(time.RFC3339),
	})
}

// POST /api/v1/customer/sms/bulk
func (h *EdgeHandler) submitBulk(c *fiber.Ctx) error {
	principal := c.Locals("edge_principal").(*Principal)

	var req BulkSubmitRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(ErrValidation.HTTPStatus).JSON(&PublicEdgeError{
			HTTPStatus: http.StatusBadRequest,
			Code:       "invalid_json",
			Message:    "Unable to parse request body: " + err.Error(),
		})
	}

	n := len(req.Messages)
	if n == 0 || n > 1000 {
		return c.Status(ErrValidation.HTTPStatus).JSON(&PublicEdgeError{
			HTTPStatus: http.StatusBadRequest,
			Code:       "invalid_bulk_size",
			Message:    "Bulk submission must contain between 1 and 1000 messages",
		})
	}

	// Upfront Rate Limit token bucket check for all N items
	allowed, retryAfter := h.limiter.AllowN(principal.CustomerOrgID, principal.MaxTPS, n)
	if !allowed {
		c.Set("Retry-After", strconv.Itoa(retryAfter))
		return c.Status(ErrRateLimitExceeded.HTTPStatus).JSON(ErrRateLimitExceeded)
	}

	results := make([]BulkItemResult, n)
	acceptedCount := 0
	failedCount := 0

	var validIdxs []int
	var bulkReqs []messaging.SubmitRequest
	for i, m := range req.Messages {
		if strings.TrimSpace(m.Destination) == "" || strings.TrimSpace(m.SenderID) == "" || m.Text == "" {
			results[i] = BulkItemResult{
				Index:   i,
				Success: false,
				Error: &PublicEdgeError{
					HTTPStatus: http.StatusBadRequest,
					Code:       "missing_required_fields",
					Message:    "destination, sender_id, and text are required fields",
				},
			}
			failedCount++
			continue
		}

		validIdxs = append(validIdxs, i)
		bulkReqs = append(bulkReqs, messaging.SubmitRequest{
			Destination:        m.Destination,
			SenderID:           m.SenderID,
			Text:               m.Text,
			ClientReference:    m.ClientReference,
			TrafficProfile:     m.TrafficProfile,
			ValiditySeconds:    m.ValiditySeconds,
			IngressChannel:     "http",
			IngressAccountID:   principal.APIKeyID,
			DLRRequested:       m.DLRRequested,
			RequestFingerprint: computeFingerprint(m.Destination, m.SenderID, m.Text, m.TrafficProfile),
		})
	}

	if len(bulkReqs) > 0 {
		outcomes, err := h.msgSvc.SubmitBulk(c.Context(), principal.ToTenantContext(), bulkReqs)
		if err != nil {
			mapped := MapSubmitError(err)
			for _, origIdx := range validIdxs {
				results[origIdx] = BulkItemResult{Index: origIdx, Success: false, Error: mapped}
				failedCount++
			}
		} else {
			for k, origIdx := range validIdxs {
				out := outcomes[k]
				if out.Err != nil {
					results[origIdx] = BulkItemResult{
						Index:   origIdx,
						Success: false,
						Error:   MapSubmitError(out.Err),
					}
					failedCount++
				} else {
					resp := out.Response
					results[origIdx] = BulkItemResult{
						Index:   origIdx,
						Success: true,
						Data: &CustomerSubmitResponse{
							ID:              resp.MessageID.String(),
							ClientReference: resp.ClientReference,
							Status:          MapInternalToCustomerStatus(resp.Status).Status,
							Destination:     resp.Destination,
							SenderID:        resp.SenderID,
							Parts:           resp.Parts,
							Price:           resp.Price,
							Currency:        resp.Currency,
							CreatedAt:       resp.CreatedAt.UTC().Format(time.RFC3339),
						},
					}
					acceptedCount++
				}
			}
		}
	}

	statusCode := http.StatusOK
	if failedCount > 0 && acceptedCount > 0 {
		statusCode = http.StatusMultiStatus // 207
	} else if failedCount == n {
		statusCode = http.StatusUnprocessableEntity
	}

	return c.Status(statusCode).JSON(BulkSubmitResponse{
		Total:    n,
		Accepted: acceptedCount,
		Failed:   failedCount,
		Items:    results,
	})
}

// POST /api/v1/customer/sms/estimate
func (h *EdgeHandler) estimateSMS(c *fiber.Ctx) error {
	principal := c.Locals("edge_principal").(*Principal)

	var req EstimateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(ErrValidation.HTTPStatus).JSON(ErrValidation)
	}

	dest := strings.TrimSpace(req.Destination)
	if dest == "" || req.Text == "" {
		return c.Status(ErrValidation.HTTPStatus).JSON(&PublicEdgeError{
			HTTPStatus: http.StatusBadRequest,
			Code:       "missing_required_fields",
			Message:    "destination and text are required fields",
		})
	}

	// Calculate parts and encoding via standard SMS rules
	isUCS2 := false
	for _, r := range req.Text {
		if r > 127 { // Simple UCS-2 detection heuristic
			isUCS2 = true
			break
		}
	}

	encoding := "GSM-7"
	parts := 1
	charCount := len([]rune(req.Text))

	if isUCS2 {
		encoding = "UCS-2"
		if charCount > 70 {
			parts = (charCount + 66) / 67
		}
	} else {
		if charCount > 160 {
			parts = (charCount + 152) / 153
		}
	}

	// Query active sell rate for customer organization using Phase 5 rating engine
	var rate decimal.Decimal
	currency := "USD"
	msgType := "default"
	if req.TrafficProfile != "" {
		msgType = req.TrafficProfile
	}
	matched := false
	if h.ratingSvc != nil {
		if rm, err := h.ratingSvc.LookupByPhoneNumber(c.Context(), principal.CustomerOrgID, dest, msgType, time.Now().UTC()); err == nil && rm != nil && rm.Found {
			rate = rm.Price
			if rm.Currency != "" {
				currency = rm.Currency
			}
			matched = true
		}
	}
	if !matched {
		// Default estimation fallback if unrated
		rate = decimal.NewFromFloat(0.005)
	}

	totalPrice := rate.Mul(decimal.NewFromInt(int64(parts)))

	return c.Status(http.StatusOK).JSON(EstimateResponse{
		Destination:    dest,
		Encoding:       encoding,
		Parts:          parts,
		CharacterCount: charCount,
		EstimatedPrice: totalPrice,
		Currency:       currency,
	})
}

// GET /api/v1/customer/sms/:id
func (h *EdgeHandler) getMessage(c *fiber.Ctx) error {
	principal := c.Locals("edge_principal").(*Principal)

	idParam := c.Params("id")
	parsedID, err := ParseMessageID(idParam)
	if err != nil {
		return c.Status(ErrMessageNotFound.HTTPStatus).JSON(ErrMessageNotFound)
	}

	msg, _, err := h.repo.GetMessageByID(c.Context(), parsedID)
	if err != nil || msg == nil {
		return c.Status(ErrMessageNotFound.HTTPStatus).JSON(ErrMessageNotFound)
	}

	// Tenant boundary check: must belong to the caller organization
	if msg.CustomerOrgID != principal.CustomerOrgID {
		return c.Status(ErrMessageNotFound.HTTPStatus).JSON(ErrMessageNotFound)
	}

	bodyText := ""
	if principal.HasScope(ScopeSMSReadContent) && len(msg.EncryptedBody) > 0 && h.keyring != nil {
		if dec, decErr := h.keyring.Decrypt(string(msg.EncryptedBody)); decErr == nil {
			bodyText = dec
		}
	}

	var deliveredStr *string
	if msg.DeliveredAt != nil {
		d := msg.DeliveredAt.UTC().Format(time.RFC3339)
		deliveredStr = &d
	}

	return c.Status(http.StatusOK).JSON(fiber.Map{
		"id":               msg.ID.String(),
		"client_reference": msg.ClientReference,
		"status":           MapInternalToCustomerStatus(msg.Status).Status,
		"destination":      msg.Destination,
		"sender_id":        msg.SenderID,
		"parts":            msg.TotalParts,
		"price":            msg.SellPrice,
		"currency":         msg.SellCurrency,
		"text":             bodyText,
		"created_at":       msg.CreatedAt.UTC().Format(time.RFC3339),
		"delivered_at":     deliveredStr,
	})
}

// GET /api/v1/customer/sms
func (h *EdgeHandler) listMessages(c *fiber.Ctx) error {
	principal := c.Locals("edge_principal").(*Principal)

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("limit", "50"))
	if pageSize > 200 {
		pageSize = 200
	}

	status := c.Query("status")
	destination := c.Query("destination")
	senderID := c.Query("sender_id")

	now := time.Now().UTC()
	fromDate := now.Add(-24 * time.Hour) // Default last 24h
	toDate := now

	if fromQuery := c.Query("from_date"); fromQuery != "" {
		if parsed, err := time.Parse(time.RFC3339, fromQuery); err == nil {
			fromDate = parsed
		}
	}
	if toQuery := c.Query("to_date"); toQuery != "" {
		if parsed, err := time.Parse(time.RFC3339, toQuery); err == nil {
			toDate = parsed
		}
	}

	// Maximum 31 days query window
	if toDate.Sub(fromDate) > 31*24*time.Hour {
		return c.Status(ErrValidation.HTTPStatus).JSON(&PublicEdgeError{
			HTTPStatus: http.StatusBadRequest,
			Code:       "range_exceeded",
			Message:    "Date filter range cannot exceed 31 days",
		})
	}

	msgs, total, err := h.repo.ListMessagesAdvanced(
		c.Context(),
		&principal.CustomerOrgID,
		status,
		destination,
		senderID,
		nil,
		&fromDate,
		&toDate,
		page,
		pageSize,
	)
	if err != nil {
		return c.Status(http.StatusInternalServerError).JSON(ErrServiceUnavailable)
	}

	items := make([]fiber.Map, len(msgs))
	for i, m := range msgs {
		items[i] = fiber.Map{
			"id":               m.ID.String(),
			"client_reference": m.ClientReference,
			"status":           MapInternalToCustomerStatus(m.Status).Status,
			"destination":      m.Destination,
			"sender_id":        m.SenderID,
			"parts":            m.TotalParts,
			"price":            m.SellPrice,
			"currency":         m.SellCurrency,
			"created_at":       m.CreatedAt.UTC().Format(time.RFC3339),
		}
	}

	return c.Status(http.StatusOK).JSON(fiber.Map{
		"total": total,
		"page":  page,
		"limit": pageSize,
		"items": items,
	})
}

// GET /api/v1/customer/balance
func (h *EdgeHandler) getBalance(c *fiber.Ctx) error {
	principal := c.Locals("edge_principal").(*Principal)

	wallet, err := h.ledgerSvc.GetWalletByOrgID(c.Context(), principal.ToTenantContext(), principal.CustomerOrgID)
	if err != nil {
		return c.Status(http.StatusNotFound).JSON(&PublicEdgeError{
			HTTPStatus: http.StatusNotFound,
			Code:       "wallet_not_found",
			Message:    "Customer ledger wallet not found",
		})
	}

	return c.Status(http.StatusOK).JSON(BalanceResponse{
		Balance:     wallet.CachedBalance,
		ActiveHolds: wallet.ActiveHolds,
		Available:   wallet.AvailableBalance,
		CreditLimit: wallet.CreditLimit,
		Currency:    wallet.Currency,
	})
}

// GET /api/v1/customer/rates
func (h *EdgeHandler) listRates(c *fiber.Ctx) error {
	principal := c.Locals("edge_principal").(*Principal)

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("limit", "100"))
	if pageSize > 500 {
		pageSize = 500
	}
	offset := (page - 1) * pageSize

	q := `
		SELECT COALESCE(c.iso2, ''), COALESCE(c.name, ''), COALESCE(o.name, ''), r.message_type, r.price, rc.currency_code, r.effective_from
		FROM customer_rate_assignments cra
		JOIN rate_cards rc ON cra.card_id = rc.id
		JOIN rates r ON r.card_id = rc.id
		LEFT JOIN countries c ON r.country_id = c.id
		LEFT JOIN operators o ON r.operator_id = o.id
		WHERE cra.customer_org_id = $1
		  AND cra.status = 'active'
		  AND cra.effective_from <= now()
		  AND (cra.effective_to IS NULL OR cra.effective_to > now())
		  AND rc.status = 'active'
		  AND r.status = 'active'
		  AND r.effective_from <= now()
		  AND (r.effective_to IS NULL OR r.effective_to > now())
		ORDER BY COALESCE(c.iso2, '') ASC, COALESCE(o.name, '') ASC, r.message_type ASC
		LIMIT $2 OFFSET $3
	`
	rows, err := h.pool.Query(c.Context(), q, principal.CustomerOrgID, pageSize, offset)
	if err != nil {
		return c.Status(http.StatusInternalServerError).JSON(ErrServiceUnavailable)
	}
	defer rows.Close()

	type RateItem struct {
		CountryISO2   string          `json:"country_iso2"`
		CountryName   string          `json:"country_name"`
		OperatorName  string          `json:"operator_name,omitempty"`
		MessageType   string          `json:"message_type"`
		Rate          decimal.Decimal `json:"rate"`
		Currency      string          `json:"currency"`
		EffectiveFrom string          `json:"effective_from"`
	}

	var list []RateItem
	for rows.Next() {
		var it RateItem
		var effFrom time.Time
		if err := rows.Scan(&it.CountryISO2, &it.CountryName, &it.OperatorName, &it.MessageType, &it.Rate, &it.Currency, &effFrom); err == nil {
			it.EffectiveFrom = effFrom.UTC().Format(time.RFC3339)
			list = append(list, it)
		}
	}

	return c.Status(http.StatusOK).JSON(fiber.Map{
		"page":  page,
		"limit": pageSize,
		"items": list,
	})
}

// GET /api/v1/customer/account
func (h *EdgeHandler) getAccount(c *fiber.Ctx) error {
	principal := c.Locals("edge_principal").(*Principal)

	daily, monthly, dailyCap, monthlyCap, tps, err := h.repo.GetCustomerUsage(c.Context(), principal.CustomerOrgID)
	if err != nil {
		return c.Status(http.StatusInternalServerError).JSON(ErrServiceUnavailable)
	}

	terms, _ := h.repo.GetCustomerBillingTerms(c.Context(), principal.CustomerOrgID, messaging.DefaultCustomerBillingTerms())

	return c.Status(http.StatusOK).JSON(AccountResponse{
		OrganizationID: principal.CustomerOrgID.String(),
		DailyUsage:     daily,
		DailyCap:       dailyCap,
		MonthlyUsage:   monthly,
		MonthlyCap:     monthlyCap,
		TPSLimit:       tps,
		BillingTerms: BillingTermsSummary{
			BillingMode:             terms.BillingMode,
			DLRWaitHours:            terms.DLRWaitHours,
			NoDLRPolicy:             terms.NoDLRPolicy,
			FailedAfterAcceptPolicy: terms.FailedAfterAcceptPolicy,
		},
	})
}

// Customer Webhook Management DTOs & Handlers
type CreateWebhookRequest struct {
	URL    string   `json:"url"`
	Events []string `json:"events"`
}

type WebhookResponse struct {
	ID                  string   `json:"id"`
	URL                 string   `json:"url"`
	Events              []string `json:"events"`
	Status              string   `json:"status"`
	Secret              string   `json:"secret,omitempty"` // Returned ONLY on creation
	ConsecutiveFailures int      `json:"consecutive_failures"`
	CreatedAt           string   `json:"created_at"`
}

// POST /api/v1/customer/webhooks
func (h *EdgeHandler) createWebhook(c *fiber.Ctx) error {
	principal := c.Locals("edge_principal").(*Principal)

	var req CreateWebhookRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(ErrValidation.HTTPStatus).JSON(ErrValidation)
	}

	webhookURL := strings.TrimSpace(req.URL)
	if err := h.validateWebhookURL(webhookURL); err != nil {
		return c.Status(ErrValidation.HTTPStatus).JSON(&PublicEdgeError{
			HTTPStatus: http.StatusBadRequest,
			Code:       "invalid_webhook_url",
			Message:    "Webhook URL must be a valid external HTTPS endpoint and not target private, loopback, or metadata networks",
		})
	}
	url := webhookURL

	if len(req.Events) == 0 {
		req.Events = []string{"message.delivered", "message.failed"}
	}

	// Generate 32-byte cryptographically secure random signing secret
	secBytes := make([]byte, 32)
	_, _ = rand.Read(secBytes)
	plaintextSecret := hex.EncodeToString(secBytes)

	// Encrypt secret via secrets KeyRing for at-rest protection
	encryptedSec, err := h.keyring.Encrypt(plaintextSecret)
	if err != nil {
		return c.Status(http.StatusInternalServerError).JSON(ErrServiceUnavailable)
	}

	wh := &messaging.CustomerWebhook{
		ID:              uuid.New(),
		CustomerOrgID:   principal.CustomerOrgID,
		URL:             url,
		SecretEncrypted: encryptedSec,
		SecretKeyID:     h.keyring.ActiveKeyID(),
		Events:          req.Events,
		Status:          "active",
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}

	if err := h.repo.CreateWebhook(c.Context(), wh); err != nil {
		return c.Status(http.StatusInternalServerError).JSON(ErrServiceUnavailable)
	}

	return c.Status(http.StatusCreated).JSON(WebhookResponse{
		ID:        wh.ID.String(),
		URL:       wh.URL,
		Events:    wh.Events,
		Status:    wh.Status,
		Secret:    plaintextSecret, // Write-only: surfaced to caller exactly once
		CreatedAt: wh.CreatedAt.Format(time.RFC3339),
	})
}

// GET /api/v1/customer/webhooks
func (h *EdgeHandler) listWebhooks(c *fiber.Ctx) error {
	principal := c.Locals("edge_principal").(*Principal)

	list, err := h.repo.ListWebhooks(c.Context(), principal.CustomerOrgID)
	if err != nil {
		return c.Status(http.StatusInternalServerError).JSON(ErrServiceUnavailable)
	}

	res := make([]WebhookResponse, len(list))
	for i, w := range list {
		res[i] = WebhookResponse{
			ID:                  w.ID.String(),
			URL:                 w.URL,
			Events:              w.Events,
			Status:              w.Status,
			ConsecutiveFailures: w.ConsecutiveFailures,
			CreatedAt:           w.CreatedAt.Format(time.RFC3339),
		}
	}

	return c.Status(http.StatusOK).JSON(res)
}

// GET /api/v1/customer/webhooks/:id
func (h *EdgeHandler) getWebhook(c *fiber.Ctx) error {
	principal := c.Locals("edge_principal").(*Principal)
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "Webhook not found"})
	}

	w, err := h.repo.GetWebhook(c.Context(), id, principal.CustomerOrgID)
	if err != nil || w == nil {
		return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "Webhook not found"})
	}

	return c.Status(http.StatusOK).JSON(WebhookResponse{
		ID:                  w.ID.String(),
		URL:                 w.URL,
		Events:              w.Events,
		Status:              w.Status,
		ConsecutiveFailures: w.ConsecutiveFailures,
		CreatedAt:           w.CreatedAt.Format(time.RFC3339),
	})
}

// PUT /api/v1/customer/webhooks/:id
func (h *EdgeHandler) updateWebhook(c *fiber.Ctx) error {
	principal := c.Locals("edge_principal").(*Principal)
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "Webhook not found"})
	}

	w, err := h.repo.GetWebhook(c.Context(), id, principal.CustomerOrgID)
	if err != nil || w == nil {
		return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "Webhook not found"})
	}

	var req struct {
		URL    string   `json:"url"`
		Events []string `json:"events"`
		Status string   `json:"status"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(ErrValidation.HTTPStatus).JSON(ErrValidation)
	}

	if req.URL != "" {
		nextURL := strings.TrimSpace(req.URL)
		if err := h.validateWebhookURL(nextURL); err != nil {
			return c.Status(ErrValidation.HTTPStatus).JSON(&PublicEdgeError{
				HTTPStatus: http.StatusBadRequest,
				Code:       "invalid_webhook_url",
				Message:    "Webhook URL must be a valid external HTTPS endpoint and not target private, loopback, or metadata networks",
			})
		}
		w.URL = nextURL
	}
	if len(req.Events) > 0 {
		w.Events = req.Events
	}
	if req.Status != "" {
		st := strings.TrimSpace(req.Status)
		if st != "active" && st != "disabled" {
			return c.Status(ErrValidation.HTTPStatus).JSON(&PublicEdgeError{
				HTTPStatus: http.StatusBadRequest,
				Code:       "invalid_webhook_status",
				Message:    "Webhook status must be 'active' or 'disabled'",
			})
		}
		w.Status = st
	}

	if err := h.repo.UpdateWebhook(c.Context(), w); err != nil {
		return c.Status(http.StatusInternalServerError).JSON(ErrServiceUnavailable)
	}

	return c.Status(http.StatusOK).JSON(WebhookResponse{
		ID:                  w.ID.String(),
		URL:                 w.URL,
		Events:              w.Events,
		Status:              w.Status,
		ConsecutiveFailures: w.ConsecutiveFailures,
		CreatedAt:           w.CreatedAt.Format(time.RFC3339),
	})
}

// DELETE /api/v1/customer/webhooks/:id
func (h *EdgeHandler) deleteWebhook(c *fiber.Ctx) error {
	principal := c.Locals("edge_principal").(*Principal)
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "Webhook not found"})
	}

	w, err := h.repo.GetWebhook(c.Context(), id, principal.CustomerOrgID)
	if err != nil {
		return c.Status(http.StatusInternalServerError).JSON(ErrServiceUnavailable)
	}
	if w == nil {
		return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "Webhook not found"})
	}

	if err := h.repo.DeleteWebhook(c.Context(), id, principal.CustomerOrgID); err != nil {
		return c.Status(http.StatusInternalServerError).JSON(ErrServiceUnavailable)
	}

	return c.SendStatus(http.StatusNoContent)
}

// POST /api/v1/customer/webhooks/:id/test
func (h *EdgeHandler) testWebhook(c *fiber.Ctx) error {
	principal := c.Locals("edge_principal").(*Principal)
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "Webhook not found"})
	}

	w, err := h.repo.GetWebhook(c.Context(), id, principal.CustomerOrgID)
	if err != nil || w == nil {
		return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "Webhook not found"})
	}

	testPayload := fmt.Sprintf(`{"event":"test.ping","timestamp":"%s","customer_org_id":"%s"}`,
		time.Now().UTC().Format(time.RFC3339), principal.CustomerOrgID.String())

	insDeliv := `
		INSERT INTO customer_webhook_deliveries (
			id, webhook_id, customer_org_id, event_id, event_type, payload,
			status, attempts, max_attempts, next_attempt_at, created_at
		) VALUES ($1, $2, $3, $4, 'test.ping', $5, 'pending', 0, 3, now(), now())
	`
	delivID := uuid.New()
	_, err = h.pool.Exec(c.Context(), insDeliv, delivID, w.ID, principal.CustomerOrgID, uuid.New().String(), []byte(testPayload))
	if err != nil {
		return c.Status(http.StatusInternalServerError).JSON(ErrServiceUnavailable)
	}

	return c.Status(http.StatusAccepted).JSON(fiber.Map{
		"message":     "Test webhook delivery queued",
		"delivery_id": delivID.String(),
	})
}

// GET /api/v1/customer/webhooks/:id/deliveries
func (h *EdgeHandler) listWebhookDeliveries(c *fiber.Ctx) error {
	principal := c.Locals("edge_principal").(*Principal)
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "Webhook not found"})
	}

	w, err := h.repo.GetWebhook(c.Context(), id, principal.CustomerOrgID)
	if err != nil {
		return c.Status(http.StatusInternalServerError).JSON(ErrServiceUnavailable)
	}
	if w == nil {
		return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": "Webhook not found"})
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("limit", "20"))
	status := c.Query("status")

	deliveries, total, err := h.repo.ListWebhookDeliveries(c.Context(), principal.CustomerOrgID, &id, status, page, pageSize)
	if err != nil {
		return c.Status(http.StatusInternalServerError).JSON(ErrServiceUnavailable)
	}
	if deliveries == nil {
		deliveries = []messaging.CustomerWebhookDelivery{}
	}

	return c.Status(http.StatusOK).JSON(fiber.Map{
		"total": total,
		"page":  page,
		"items": deliveries,
	})
}

// GET /api/v1/customer/inbound
func (h *EdgeHandler) listInboundMessages(c *fiber.Ctx) error {
	principal := c.Locals("edge_principal").(*Principal)

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("limit", "50"))

	msgs, total, err := h.repo.ListInboundMessages(c.Context(), &principal.CustomerOrgID, page, pageSize)
	if err != nil {
		return c.Status(http.StatusInternalServerError).JSON(ErrServiceUnavailable)
	}

	canReadContent := principal.HasScope(ScopeSMSReadContent)
	type InboundView struct {
		ID         string `json:"id"`
		From       string `json:"from"`
		To         string `json:"to"`
		Text       string `json:"text,omitempty"`
		Encoding   string `json:"encoding"`
		Status     string `json:"status"`
		ReceivedAt string `json:"received_at"`
	}

	items := make([]InboundView, len(msgs))
	for i, m := range msgs {
		body := ""
		if canReadContent && len(m.EncryptedBody) > 0 && h.keyring != nil {
			if dec, err := h.keyring.Decrypt(string(m.EncryptedBody)); err == nil {
				body = dec
			}
		}
		items[i] = InboundView{
			ID:         m.ID.String(),
			From:       m.SourceE164,
			To:         m.DestinationE164,
			Text:       body,
			Encoding:   m.Encoding,
			Status:     m.Status,
			ReceivedAt: m.CreatedAt.UTC().Format(time.RFC3339),
		}
	}

	return c.Status(http.StatusOK).JSON(fiber.Map{
		"total": total,
		"page":  page,
		"items": items,
	})
}
