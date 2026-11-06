package ledger

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
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

	// Wallets routes
	wallets := router.Group("/wallets", authMiddleware)
	wallets.Get("/", rbac.RequirePermission("wallet.wallet.view"), h.ListWallets)
	wallets.Get("/my", rbac.RequirePermission("wallet.wallet.view"), h.GetMyWallet)
	wallets.Post("/transfer", moneyLimiter, h.Transfer)
	wallets.Post("/holds/expire", rbac.RequirePermission("wallet.override"), h.ExpireHolds)

	wallet := wallets.Group("/:id")
	wallet.Get("/", rbac.RequirePermission("wallet.wallet.view"), h.GetWallet)
	wallet.Get("/balances", rbac.RequirePermission("wallet.wallet.view"), h.GetBalances)
	wallet.Post("/topup", rbac.RequirePermission("wallet.topup.create"), moneyLimiter, h.Topup)
	wallet.Post("/adjust", rbac.RequirePermission("wallet.adjustment.create"), moneyLimiter, h.Adjust)
	wallet.Post("/freeze", rbac.RequirePermission("wallet.freeze"), h.Freeze)
	wallet.Post("/unfreeze", rbac.RequirePermission("wallet.freeze"), h.Unfreeze)
	wallet.Get("/holds", rbac.RequirePermission("wallet.wallet.view"), h.ListHolds)
	wallet.Get("/statement", rbac.RequirePermission("wallet.wallet.view"), h.GetStatement)

	// Ledger routes
	ledger := router.Group("/ledger", authMiddleware)
	ledger.Get("/transactions", rbac.RequirePermission("ledger.view"), h.ListTransactions)
	ledger.Get("/transactions/:id", rbac.RequirePermission("ledger.view"), h.GetTransaction)
	ledger.Post("/transactions/:id/reverse", rbac.RequirePermission("wallet.adjustment.create"), moneyLimiter, h.ReverseTransaction)
	ledger.Get("/reconciliation", rbac.RequirePermission("ledger.reconcile"), h.GetReconciliation)
	ledger.Post("/reconciliation/run", rbac.RequirePermission("ledger.reconcile"), h.RunReconciliation)
}

func (h *Handler) mapError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, ErrWalletNotFound), errors.Is(err, ErrHoldNotFound), errors.Is(err, ErrTransactionNotFound):
		return httpx.NotFound(err.Error())
	case errors.Is(err, ErrCrossTenantAccess), errors.Is(err, ErrPermissionDenied), errors.Is(err, ErrMakerCannotApprove):
		return httpx.Forbidden(err.Error())
	case errors.Is(err, ErrIdempotencyConflict):
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"error": fiber.Map{
				"code":    "IDEMPOTENCY_CONFLICT",
				"message": err.Error(),
			},
		})
	case errors.Is(err, ErrInsufficientFunds), errors.Is(err, ErrWalletFrozen), errors.Is(err, ErrWalletClosed),
		errors.Is(err, ErrNegativeAmount), errors.Is(err, ErrAmountExceedsMaximum), errors.Is(err, ErrCurrencyMismatch), errors.Is(err, ErrUnbalancedTransaction),
		errors.Is(err, ErrHoldNotActive), errors.Is(err, ErrHoldExpired), errors.Is(err, ErrHoldCaptureExceeded),
		errors.Is(err, ErrTransactionAlreadyReversed), errors.Is(err, ErrReversalNotAllowed), errors.Is(err, ErrReasonRequired):
		return httpx.BadRequest(err.Error())
	default:
		return httpx.Internal(err)
	}
}

func (h *Handler) ListWallets(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	search := c.Query("search")
	status := c.Query("status")
	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "20"))

	items, total, err := h.svc.ListWallets(c.UserContext(), tc, search, status, page, pageSize)
	if err != nil {
		return h.mapError(c, err)
	}

	return c.JSON(fiber.Map{
		"data": fiber.Map{
			"items":     items,
			"total":     total,
			"page":      page,
			"page_size": pageSize,
		},
	})
}

func (h *Handler) GetWallet(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid wallet id")
	}

	w, err := h.svc.GetWallet(c.UserContext(), tc, id)
	if err != nil {
		return h.mapError(c, err)
	}

	return c.JSON(fiber.Map{"data": w})
}

func (h *Handler) GetMyWallet(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	w, err := h.svc.GetWalletByOrgID(c.UserContext(), tc, tc.ActorOrgID)
	if err != nil {
		return h.mapError(c, err)
	}

	return c.JSON(fiber.Map{"data": w})
}

func (h *Handler) GetBalances(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid wallet id")
	}

	w, err := h.svc.GetWallet(c.UserContext(), tc, id)
	if err != nil {
		return h.mapError(c, err)
	}

	return c.JSON(fiber.Map{
		"data": fiber.Map{
			"wallet_id":         w.ID,
			"currency":          w.Currency,
			"account_type":      w.AccountType,
			"status":            w.Status,
			"ledger_balance":    w.CachedBalance,
			"credit_limit":      w.CreditLimit,
			"active_holds":      w.ActiveHolds,
			"available_balance": w.AvailableBalance,
		},
	})
}

func (h *Handler) Topup(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid wallet id")
	}

	var req WalletTopupRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("invalid request body")
	}

	tx, cr, err := h.svc.Topup(c.UserContext(), tc, id, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return h.mapError(c, err)
	}

	if cr != nil {
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
			"message": "Top-up change request submitted for checker approval",
			"data": fiber.Map{
				"pending_approval":    true,
				"approval_request_id": cr.ID,
				"status":              cr.Status,
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"message": "Top-up completed successfully",
		"data":    tx,
	})
}

func (h *Handler) Adjust(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid wallet id")
	}

	var req WalletAdjustmentRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("invalid request body")
	}

	tx, cr, err := h.svc.Adjust(c.UserContext(), tc, id, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return h.mapError(c, err)
	}

	if cr != nil {
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
			"message": "Adjustment change request submitted for checker approval",
			"data": fiber.Map{
				"pending_approval":    true,
				"approval_request_id": cr.ID,
				"status":              cr.Status,
			},
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"message": "Adjustment completed successfully",
		"data":    tx,
	})
}

func (h *Handler) Freeze(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid wallet id")
	}

	var req FreezeWalletRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("invalid request body")
	}

	if err := h.svc.FreezeWallet(c.UserContext(), tc, id, req.Reason, c.IP(), c.Get("User-Agent")); err != nil {
		return h.mapError(c, err)
	}

	return c.JSON(fiber.Map{
		"message": "Wallet frozen successfully",
	})
}

func (h *Handler) Unfreeze(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid wallet id")
	}

	var req UnfreezeWalletRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("invalid request body")
	}

	cr, err := h.svc.UnfreezeWallet(c.UserContext(), tc, id, req.Reason, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return h.mapError(c, err)
	}

	if cr != nil {
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
			"message": "Unfreeze request submitted for checker approval",
			"data": fiber.Map{
				"pending_approval":    true,
				"approval_request_id": cr.ID,
				"status":              cr.Status,
			},
		})
	}

	return c.JSON(fiber.Map{
		"message": "Wallet unfrozen successfully",
	})
}

func (h *Handler) Transfer(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	var req WalletTransferRequest
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("invalid request body")
	}

	tx, err := h.svc.TransferResellerToCustomer(c.UserContext(), tc, req, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return h.mapError(c, err)
	}

	return c.JSON(fiber.Map{
		"message": "Reseller-funded transfer completed successfully",
		"data":    tx,
	})
}

func (h *Handler) ListHolds(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid wallet id")
	}

	status := c.Query("status")
	holds, err := h.svc.ListHolds(c.UserContext(), tc, id, status)
	if err != nil {
		return h.mapError(c, err)
	}

	return c.JSON(fiber.Map{"data": holds})
}

func (h *Handler) ExpireHolds(c *fiber.Ctx) error {
	count, err := h.svc.ExpireOldHolds(c.UserContext())
	if err != nil {
		return h.mapError(c, err)
	}

	return c.JSON(fiber.Map{
		"message":       "Expired active holds processed",
		"expired_count": count,
	})
}

func (h *Handler) GetStatement(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid wallet id")
	}

	format := strings.ToLower(c.Query("format", "json"))
	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "50"))

	var startPtr, endPtr *time.Time
	if s := c.Query("start_date"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			startPtr = &t
		}
	}
	if e := c.Query("end_date"); e != "" {
		if t, err := time.Parse(time.RFC3339, e); err == nil {
			endPtr = &t
		}
	}

	filter := StatementFilter{
		WalletID:  id,
		StartDate: startPtr,
		EndDate:   endPtr,
		Page:      page,
		PageSize:  pageSize,
	}

	if format == "csv" {
		data, err := h.svc.ExportStatementCSV(c.UserContext(), tc, filter)
		if err != nil {
			return h.mapError(c, err)
		}
		c.Set("Content-Type", "text/csv")
		c.Set("Content-Disposition", fmt.Sprintf("attachment; filename=statement_%s.csv", id.String()[:8]))
		return c.Send(data)
	}

	items, total, err := h.svc.GetStatement(c.UserContext(), tc, filter)
	if err != nil {
		return h.mapError(c, err)
	}

	return c.JSON(fiber.Map{
		"data": fiber.Map{
			"items":     items,
			"total":     total,
			"page":      page,
			"page_size": pageSize,
		},
	})
}

func (h *Handler) ListTransactions(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	format := strings.ToLower(c.Query("format", "json"))
	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "20"))

	var walletIDPtr *uuid.UUID
	if wIDStr := c.Query("wallet_id"); wIDStr != "" {
		if wid, err := uuid.Parse(wIDStr); err == nil {
			walletIDPtr = &wid
		}
	}

	var typePtr *string
	if tStr := c.Query("type"); tStr != "" {
		typePtr = &tStr
	}

	var startPtr, endPtr *time.Time
	if s := c.Query("start_date"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			startPtr = &t
		}
	}
	if e := c.Query("end_date"); e != "" {
		if t, err := time.Parse(time.RFC3339, e); err == nil {
			endPtr = &t
		}
	}

	var minAmtPtr, maxAmtPtr *decimal.Decimal
	if m := c.Query("min_amount"); m != "" {
		if d, err := decimal.NewFromString(m); err == nil {
			minAmtPtr = &d
		}
	}
	if m := c.Query("max_amount"); m != "" {
		if d, err := decimal.NewFromString(m); err == nil {
			maxAmtPtr = &d
		}
	}

	filter := TransactionFilter{
		WalletID:        walletIDPtr,
		TransactionType: typePtr,
		StartDate:       startPtr,
		EndDate:         endPtr,
		MinAmount:       minAmtPtr,
		MaxAmount:       maxAmtPtr,
		Search:          c.Query("search"),
		Page:            page,
		PageSize:        pageSize,
	}

	if format == "csv" {
		data, err := h.svc.ExportTransactionsCSV(c.UserContext(), tc, filter)
		if err != nil {
			return h.mapError(c, err)
		}
		c.Set("Content-Type", "text/csv")
		c.Set("Content-Disposition", "attachment; filename=transactions.csv")
		return c.Send(data)
	}

	txs, total, err := h.svc.ListTransactions(c.UserContext(), tc, filter)
	if err != nil {
		return h.mapError(c, err)
	}

	return c.JSON(fiber.Map{
		"data": fiber.Map{
			"items":     txs,
			"total":     total,
			"page":      page,
			"page_size": pageSize,
		},
	})
}

func (h *Handler) GetTransaction(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid transaction id")
	}

	tx, err := h.svc.GetTransaction(c.UserContext(), tc, id)
	if err != nil {
		return h.mapError(c, err)
	}

	return c.JSON(fiber.Map{"data": tx})
}

func (h *Handler) ReverseTransaction(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return httpx.BadRequest("invalid transaction id")
	}

	var req struct {
		Reason string `json:"reason"`
	}
	if err := c.BodyParser(&req); err != nil {
		return httpx.BadRequest("invalid request body")
	}

	revTx, err := h.svc.ReverseTransaction(c.UserContext(), tc, id, req.Reason, c.IP(), c.Get("User-Agent"))
	if err != nil {
		return h.mapError(c, err)
	}

	return c.JSON(fiber.Map{
		"message": "Transaction reversed successfully",
		"data":    revTx,
	})
}

func (h *Handler) GetReconciliation(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	reports, err := h.svc.ReconcileBalances(c.UserContext(), tc, false)
	if err != nil {
		return h.mapError(c, err)
	}

	return c.JSON(fiber.Map{"data": reports})
}

func (h *Handler) RunReconciliation(c *fiber.Ctx) error {
	tc, err := tenant.FromContext(c.UserContext())
	if err != nil {
		return httpx.Unauthorized("authentication required")
	}

	var req struct {
		Repair bool `json:"repair"`
	}
	_ = c.BodyParser(&req)

	reports, err := h.svc.ReconcileBalances(c.UserContext(), tc, req.Repair)
	if err != nil {
		return h.mapError(c, err)
	}

	return c.JSON(fiber.Map{
		"message": "Reconciliation check completed",
		"data":    reports,
	})
}
