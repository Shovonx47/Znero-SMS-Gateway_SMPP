package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

type Service struct {
	repo        Repository
	orgRepo     org.Repository
	ledgerRepo  ledger.Repository
	approvalSvc *approval.Service
	auditSvc    *audit.Service
	pool        *pgxpool.Pool
}

func NewService(
	repo Repository,
	orgRepo org.Repository,
	ledgerRepo ledger.Repository,
	approvalSvc *approval.Service,
	auditSvc *audit.Service,
	pool *pgxpool.Pool,
) *Service {
	s := &Service{
		repo:        repo,
		orgRepo:     orgRepo,
		ledgerRepo:  ledgerRepo,
		approvalSvc: approvalSvc,
		auditSvc:    auditSvc,
		pool:        pool,
	}

	if approvalSvc != nil {
		approvalSvc.RegisterApplier("invoice_void", &voidInvoiceApplier{s: s})
		approvalSvc.RegisterApprovePermission("invoice_void", "billing.invoice.void")
	}

	return s
}

// -------------------------------------------------------------------------
// Helper: Tenancy & Permissions
// -------------------------------------------------------------------------

func (s *Service) authorizeInvoiceAccess(ctx context.Context, tc *tenant.Context, invoiceID uuid.UUID, perm string) (*Invoice, error) {
	if tc == nil {
		return nil, ErrPermissionDenied
	}
	if perm != "" && !tc.HasPermission(perm) {
		return nil, ErrPermissionDenied
	}

	inv, err := s.repo.GetInvoiceByID(ctx, invoiceID)
	if err != nil {
		return nil, err
	}

	if tc.IsPlatformAdmin || tc.IsPlatformOrg() {
		return inv, nil
	}

	// Verify organizational access via materialized path
	targetOrg, err := s.orgRepo.GetByID(ctx, inv.CustomerOrgID)
	if err != nil {
		return nil, ErrCrossTenantAccess
	}
	if !tc.CanAccessOrg(targetOrg.Path) {
		return nil, ErrCrossTenantAccess
	}

	return inv, nil
}

// -------------------------------------------------------------------------
// Invoices CRUD & Workflow
// -------------------------------------------------------------------------

func (s *Service) CreateDraftInvoice(ctx context.Context, tc *tenant.Context, req CreateDraftInvoiceRequest, ip, userAgent string) (*Invoice, error) {
	if tc == nil || (!tc.IsPlatformAdmin && !tc.IsPlatformOrg()) {
		return nil, ErrPermissionDenied
	}
	if !tc.HasPermission("billing.invoice.create") {
		return nil, ErrPermissionDenied
	}

	if len(req.Lines) == 0 {
		return nil, ErrLineItemsRequired
	}
	if req.Currency == "" {
		req.Currency = "USD"
	}
	if req.TaxLabel == "" {
		req.TaxLabel = "Tax"
	}
	req.TaxAmount = req.TaxAmount.Truncate(ledger.MoneyScale)

	// Calculate subtotal and total
	subtotal := decimal.Zero
	var lines []InvoiceLine
	for idx, l := range req.Lines {
		qty := l.Quantity.Truncate(ledger.MoneyScale)
		if qty.LessThanOrEqual(decimal.Zero) {
			return nil, fmt.Errorf("line %d: quantity must be > 0", idx+1)
		}
		price := l.UnitPrice.Truncate(ledger.MoneyScale)
		if price.LessThan(decimal.Zero) {
			return nil, fmt.Errorf("line %d: unit price cannot be negative", idx+1)
		}
		lineAmt := qty.Mul(price).Truncate(ledger.MoneyScale)
		if lineAmt.GreaterThan(MaxTransactionAmount) {
			return nil, fmt.Errorf("line %d: %w", idx+1, ErrAmountExceedsMaximum)
		}
		subtotal = subtotal.Add(lineAmt)

		lines = append(lines, InvoiceLine{
			LineNumber:     idx + 1,
			Description:    strings.TrimSpace(l.Description),
			Quantity:       qty,
			UnitPrice:      price,
			Amount:         lineAmt,
			LedgerChargeID: l.LedgerChargeID,
		})
	}

	total := subtotal.Add(req.TaxAmount)
	maxTotal := MaxTransactionAmount.Mul(decimal.NewFromInt(10))
	if total.GreaterThan(maxTotal) {
		return nil, ErrAmountExceedsMaximum
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	inv, err := s.repo.CreateInvoice(ctx, tx, Invoice{
		CustomerOrgID:   req.CustomerOrgID,
		BillingPeriodID: req.BillingPeriodID,
		Currency:        req.Currency,
		Status:          "draft",
		DueDate:         req.DueDate,
		Subtotal:        subtotal,
		TaxLabel:        req.TaxLabel,
		TaxAmount:       req.TaxAmount,
		Total:           total,
		Notes:           req.Notes,
		CreatedBy:       &tc.ActorUserID,
	}, lines)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		TargetOrgID: &inv.CustomerOrgID,
		Action:      "billing.invoice.create_draft",
		Module:      "billing",
		EntityType:  "invoice",
		EntityID:    inv.ID.String(),
		Reason:      "Created draft invoice",
		IPAddress:   ip,
		UserAgent:   userAgent,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create draft: %w", err)
	}
	return inv, nil
}

func (s *Service) UpdateDraftInvoice(ctx context.Context, tc *tenant.Context, id uuid.UUID, req UpdateDraftInvoiceRequest, ip, userAgent string) (*Invoice, error) {
	if tc == nil || (!tc.IsPlatformAdmin && !tc.IsPlatformOrg()) {
		return nil, ErrPermissionDenied
	}
	if !tc.HasPermission("billing.invoice.create") {
		return nil, ErrPermissionDenied
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	inv, err := s.repo.GetInvoiceByIDForUpdate(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if inv.Status != "draft" {
		return nil, ErrInvoiceNotDraft
	}

	if req.Currency != nil && *req.Currency != "" {
		inv.Currency = *req.Currency
	}
	if req.DueDate != nil {
		inv.DueDate = req.DueDate
	}
	if req.TaxLabel != nil {
		inv.TaxLabel = *req.TaxLabel
	}
	if req.TaxAmount != nil {
		inv.TaxAmount = req.TaxAmount.Truncate(ledger.MoneyScale)
	}
	if req.Notes != nil {
		inv.Notes = *req.Notes
	}

	var lines []InvoiceLine
	if req.Lines != nil {
		if len(*req.Lines) == 0 {
			return nil, ErrLineItemsRequired
		}
		subtotal := decimal.Zero
		for idx, l := range *req.Lines {
			qty := l.Quantity.Truncate(ledger.MoneyScale)
			price := l.UnitPrice.Truncate(ledger.MoneyScale)
			lineAmt := qty.Mul(price).Truncate(ledger.MoneyScale)
			subtotal = subtotal.Add(lineAmt)

			lines = append(lines, InvoiceLine{
				LineNumber:     idx + 1,
				Description:    strings.TrimSpace(l.Description),
				Quantity:       qty,
				UnitPrice:      price,
				Amount:         lineAmt,
				LedgerChargeID: l.LedgerChargeID,
			})
		}
		inv.Subtotal = subtotal
	} else {
		lines = inv.Lines
	}
	inv.Total = inv.Subtotal.Add(inv.TaxAmount)

	updated, err := s.repo.UpdateDraftInvoice(ctx, tx, *inv, lines)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		TargetOrgID: &inv.CustomerOrgID,
		Action:      "billing.invoice.update_draft",
		Module:      "billing",
		EntityType:  "invoice",
		EntityID:    inv.ID.String(),
		Reason:      "Updated draft invoice",
		IPAddress:   ip,
		UserAgent:   userAgent,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update draft: %w", err)
	}
	return updated, nil
}

func (s *Service) GetInvoice(ctx context.Context, tc *tenant.Context, id uuid.UUID) (*Invoice, error) {
	return s.authorizeInvoiceAccess(ctx, tc, id, "billing.invoice.view")
}

func (s *Service) ListInvoices(ctx context.Context, tc *tenant.Context, filter InvoiceFilter) ([]Invoice, int, error) {
	if tc == nil || !tc.HasPermission("billing.invoice.view") {
		return nil, 0, ErrPermissionDenied
	}
	return s.repo.ListInvoices(ctx, tc, filter)
}

func (s *Service) IssueInvoice(ctx context.Context, tc *tenant.Context, id uuid.UUID, ip, userAgent string) (*Invoice, error) {
	if tc == nil || (!tc.IsPlatformAdmin && !tc.IsPlatformOrg()) {
		return nil, ErrPermissionDenied
	}
	if !tc.HasPermission("billing.invoice.issue") {
		return nil, ErrPermissionDenied
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	inv, err := s.repo.GetInvoiceByIDForUpdate(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if inv.Status != "draft" {
		return nil, fmt.Errorf("%w: status is %s", ErrInvoiceAlreadyIssued, inv.Status)
	}
	if len(inv.Lines) == 0 {
		return nil, ErrLineItemsRequired
	}

	now := time.Now()
	year := now.Year()
	invNum, err := s.repo.GetNextInvoiceNumber(ctx, tx, tc.ActorOrgID, "INV", year)
	if err != nil {
		return nil, fmt.Errorf("assign invoice number: %w", err)
	}

	dueDate := now.AddDate(0, 0, 30) // default 30 days payment terms
	if inv.DueDate != nil {
		dueDate = *inv.DueDate
	}

	if err := s.repo.IssueInvoice(ctx, tx, inv.ID, invNum, now, dueDate, inv.Version); err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		TargetOrgID: &inv.CustomerOrgID,
		Action:      "billing.invoice.issue",
		Module:      "billing",
		EntityType:  "invoice",
		EntityID:    inv.ID.String(),
		Reason:      fmt.Sprintf("Issued invoice %s", invNum),
		IPAddress:   ip,
		UserAgent:   userAgent,
		AfterValue: audit.RedactValue(map[string]any{
			"invoice_number": invNum,
			"total":          inv.Total.String(),
		}),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit issue invoice: %w", err)
	}

	return s.repo.GetInvoiceByID(ctx, inv.ID)
}

func (s *Service) VoidInvoice(ctx context.Context, tc *tenant.Context, id uuid.UUID, reason, ip, userAgent string) (*approval.ChangeRequest, error) {
	if tc == nil || (!tc.IsPlatformAdmin && !tc.IsPlatformOrg()) {
		return nil, ErrPermissionDenied
	}
	if !tc.HasPermission("billing.invoice.void") {
		return nil, ErrPermissionDenied
	}
	if strings.TrimSpace(reason) == "" {
		return nil, ErrVoidReasonRequired
	}

	inv, err := s.repo.GetInvoiceByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if inv.Status == "void" {
		return nil, ErrInvoiceAlreadyVoid
	}
	if inv.Status != "issued" {
		return nil, ErrInvoiceCannotVoid
	}
	if !inv.AmountPaid.IsZero() {
		return nil, errors.New("cannot void invoice with recorded payments; issue a credit note instead")
	}

	// Void requires Maker-Checker unless wallet.override or platform admin
	if !tc.HasPermission("wallet.override") && s.approvalSvc != nil {
		proposedData, err := json.Marshal(map[string]any{
			"invoice_id": id,
			"reason":     reason,
			"ip_address": ip,
			"user_agent": userAgent,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal void request: %w", err)
		}

		cr, err := s.approvalSvc.SubmitChangeRequest(ctx, nil, approval.ChangeRequest{
			EntityType:    "invoice_void",
			EntityID:      &id,
			Operation:     "void",
			ProposedData:  proposedData,
			MakerUserID:   tc.ActorUserID,
			EntityVersion: &inv.Version,
		})
		if err != nil {
			return nil, err
		}

		_ = s.auditSvc.Record(ctx, nil, audit.Entry{
			ActorUserID: &tc.ActorUserID,
			ActorOrgID:  &tc.ActorOrgID,
			TargetOrgID: &inv.CustomerOrgID,
			Action:      "billing.invoice.void_request",
			Module:      "billing",
			EntityType:  "invoice",
			EntityID:    inv.ID.String(),
			Reason:      reason,
			IPAddress:   ip,
			UserAgent:   userAgent,
		})

		return cr, nil
	}

	// Direct void
	err = s.executeVoid(ctx, nil, tc.ActorUserID, tc.ActorOrgID, id, reason, ip, userAgent)
	return nil, err
}

func (s *Service) executeVoid(
	ctx context.Context,
	dbTx pgx.Tx,
	actorUserID, actorOrgID uuid.UUID,
	id uuid.UUID,
	reason, ip, userAgent string,
) error {
	runTx := func(tx pgx.Tx) error {
		inv, err := s.repo.GetInvoiceByIDForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if inv.Status == "void" {
			return nil
		}
		if inv.Status != "issued" {
			return ErrInvoiceCannotVoid
		}

		if err := s.repo.UpdateInvoiceStatusAndPaid(ctx, tx, inv.ID, "void", &reason, inv.AmountPaid, inv.Version); err != nil {
			return err
		}

		var actorOrg *uuid.UUID
		if actorOrgID != uuid.Nil {
			actorOrg = &actorOrgID
		}
		var actorUser *uuid.UUID
		if actorUserID != uuid.Nil {
			actorUser = &actorUserID
		}

		if err := s.auditSvc.Record(ctx, tx, audit.Entry{
			ActorUserID: actorUser,
			ActorOrgID:  actorOrg,
			TargetOrgID: &inv.CustomerOrgID,
			Action:      "billing.invoice.void",
			Module:      "billing",
			EntityType:  "invoice",
			EntityID:    inv.ID.String(),
			Reason:      reason,
			IPAddress:   ip,
			UserAgent:   userAgent,
		}); err != nil {
			return fmt.Errorf("record void audit: %w", err)
		}
		return nil
	}

	if dbTx != nil {
		return runTx(dbTx)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := runTx(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) CreateCreditNote(ctx context.Context, tc *tenant.Context, origInvoiceID uuid.UUID, reason string, ip, userAgent string) (*Invoice, error) {
	if tc == nil || (!tc.IsPlatformAdmin && !tc.IsPlatformOrg()) {
		return nil, ErrPermissionDenied
	}
	if !tc.HasPermission("billing.invoice.creditnote") {
		return nil, ErrPermissionDenied
	}
	if strings.TrimSpace(reason) == "" {
		return nil, ErrCreditNoteReasonRequired
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	orig, err := s.repo.GetInvoiceByIDForUpdate(ctx, tx, origInvoiceID)
	if err != nil {
		return nil, err
	}
	if orig.Status != "issued" && orig.Status != "paid" {
		return nil, errors.New("credit notes can only be issued against issued or paid invoices")
	}

	now := time.Now()
	cnNum, err := s.repo.GetNextInvoiceNumber(ctx, tx, tc.ActorOrgID, "CN", now.Year())
	if err != nil {
		return nil, err
	}

	creditLines := []InvoiceLine{
		{
			LineNumber:  1,
			Description: fmt.Sprintf("Credit Note for %s: %s", *orig.InvoiceNumber, reason),
			Quantity:    decimal.NewFromInt(1),
			UnitPrice:   orig.Subtotal,
			Amount:      orig.Subtotal,
		},
	}

	cn, err := s.repo.CreateInvoice(ctx, tx, Invoice{
		CustomerOrgID:          orig.CustomerOrgID,
		BillingPeriodID:        orig.BillingPeriodID,
		Currency:               orig.Currency,
		Status:                 "issued", // Credit notes are immediately issued documents
		InvoiceNumber:          &cnNum,
		IssueDate:              &now,
		Subtotal:               orig.Subtotal,
		TaxLabel:               orig.TaxLabel,
		TaxAmount:              orig.TaxAmount,
		Total:                  orig.Total,
		Notes:                  fmt.Sprintf("Credit note referencing %s. Reason: %s", *orig.InvoiceNumber, reason),
		CreditNoteForInvoiceID: &orig.ID,
		CreatedBy:              &tc.ActorUserID,
	}, creditLines)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		TargetOrgID: &orig.CustomerOrgID,
		Action:      "billing.invoice.credit_note",
		Module:      "billing",
		EntityType:  "invoice",
		EntityID:    cn.ID.String(),
		Reason:      reason,
		IPAddress:   ip,
		UserAgent:   userAgent,
		AfterValue: audit.RedactValue(map[string]any{
			"credit_note_number":      cnNum,
			"original_invoice_number": *orig.InvoiceNumber,
		}),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit credit note: %w", err)
	}

	return cn, nil
}

// -------------------------------------------------------------------------
// Payments & Ledger Treatment
// -------------------------------------------------------------------------

func (s *Service) RecordPayment(ctx context.Context, tc *tenant.Context, invoiceID uuid.UUID, req RecordPaymentRequest, ip, userAgent string) (*Payment, error) {
	if tc == nil || (!tc.IsPlatformAdmin && !tc.IsPlatformOrg()) {
		return nil, ErrPermissionDenied
	}
	if !tc.HasPermission("billing.payment.create") {
		return nil, ErrPermissionDenied
	}

	req.Amount = req.Amount.Truncate(ledger.MoneyScale)
	if req.Amount.LessThanOrEqual(decimal.Zero) {
		return nil, ErrNegativeAmount
	}
	if req.Amount.GreaterThan(MaxTransactionAmount) {
		return nil, ErrAmountExceedsMaximum
	}
	if strings.TrimSpace(req.PaymentMethod) == "" {
		req.PaymentMethod = "Manual Payment"
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	inv, err := s.repo.GetInvoiceByIDForUpdate(ctx, tx, invoiceID)
	if err != nil {
		return nil, err
	}
	if inv.Status == "void" {
		return nil, fmt.Errorf("%w: cannot record payment against void invoice", ErrInvoiceCannotPay)
	}
	if inv.Status != "issued" {
		return nil, fmt.Errorf("%w: status is %s", ErrInvoiceCannotPay, inv.Status)
	}

	// Idempotency check: if key provided and already processed, return original payment
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if idempotencyKey != "" {
		existing, err := s.repo.GetPaymentByIdempotency(ctx, tx, inv.CustomerOrgID, idempotencyKey)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			if !existing.Amount.Equal(req.Amount) || existing.InvoiceID != invoiceID {
				return nil, errors.New("idempotency key reused with differing parameters")
			}
			return existing, nil
		}
	}

	outstanding := inv.Total.Sub(inv.AmountPaid)
	if req.Amount.GreaterThan(outstanding) {
		return nil, fmt.Errorf("%w: payment %s > outstanding %s", ErrPaymentExceedsTotal, req.Amount.StringFixed(ledger.MoneyScale), outstanding.StringFixed(ledger.MoneyScale))
	}

	wallet, err := s.ledgerRepo.GetWalletByOrgID(ctx, inv.CustomerOrgID)
	if err != nil {
		// Ensure wallet exists if not created yet
		wallet, err = s.ledgerRepo.EnsureWalletForOrg(ctx, tx, inv.CustomerOrgID, inv.Currency, "prepaid")
		if err != nil {
			return nil, fmt.Errorf("ensure wallet for payment: %w", err)
		}
	}

	lockedWallet, err := s.ledgerRepo.GetWalletByIDForUpdate(ctx, tx, wallet.ID)
	if err != nil {
		return nil, err
	}

	sysAccCode := "CUSTOMER_FUNDS"
	if inv.Currency == "EUR" {
		sysAccCode = "CUSTOMER_FUNDS_EUR"
	}
	sysAcc, err := s.ledgerRepo.GetSystemAccount(ctx, tx, sysAccCode)
	if err != nil {
		return nil, err
	}
	walletAcc, err := s.ledgerRepo.GetAccountByWalletID(ctx, tx, lockedWallet.ID)
	if err != nil {
		return nil, err
	}

	desc := fmt.Sprintf("Payment for Invoice %s (%s)", *inv.InvoiceNumber, req.PaymentMethod)
	refType := "invoice"
	reason := fmt.Sprintf("Payment against invoice %s", *inv.InvoiceNumber)

	lt, err := s.ledgerRepo.CreateTransaction(ctx, tx, ledger.LedgerTransaction{
		Type:          "topup",
		ReferenceType: &refType,
		ReferenceID:   &inv.ID,
		Description:   desc,
		Reason:        &reason,
		TenantOrgID:   &inv.CustomerOrgID,
		ActorUserID:   &tc.ActorUserID,
		ActorOrgID:    &tc.ActorOrgID,
	})
	if err != nil {
		return nil, err
	}

	// Accounting Treatment of Payment:
	// A payment against an invoice reduces the outstanding accounts receivable / settles charges.
	// In our double-entry ledger:
	// - For prepaid customers: paying an invoice credits the wallet, replenishing spendable balance.
	// - For postpaid customers: paying an invoice settles accumulated debt. A postpaid wallet operates
	//   on credit limit and its balance must never be artificially inflated into positive spendable credit
	//   by manual invoice lines that had no matching ledger usage charge!
	//   Therefore: for postpaid wallets, wallet credit is capped at what is actually owed on that wallet
	//   (owed debt = abs(cached_balance) when negative; 0 when non-negative).
	//   Any excess payment settling manual invoice lines credits PLATFORM_REVENUE directly.
	walletCredit := req.Amount
	revenueCredit := decimal.Zero

	if lockedWallet.AccountType == "postpaid" {
		owedOnWallet := decimal.Zero
		if lockedWallet.CachedBalance.IsNegative() {
			owedOnWallet = lockedWallet.CachedBalance.Abs()
		}
		if walletCredit.GreaterThan(owedOnWallet) {
			walletCredit = owedOnWallet
			revenueCredit = req.Amount.Sub(walletCredit)
		}
	}

	entries := []ledger.LedgerEntry{
		{
			TransactionID: lt.ID,
			AccountID:     sysAcc.ID,
			Direction:     "debit",
			Amount:        req.Amount,
			Currency:      inv.Currency,
		},
	}

	if walletCredit.GreaterThan(decimal.Zero) {
		entries = append(entries, ledger.LedgerEntry{
			TransactionID: lt.ID,
			AccountID:     walletAcc.ID,
			Direction:     "credit",
			Amount:        walletCredit,
			Currency:      inv.Currency,
		})
	}

	if revenueCredit.GreaterThan(decimal.Zero) {
		revAccCode := "PLATFORM_REVENUE"
		if inv.Currency == "EUR" {
			revAccCode = "PLATFORM_REVENUE_EUR"
		}
		revAcc, err := s.ledgerRepo.GetSystemAccount(ctx, tx, revAccCode)
		if err != nil {
			return nil, fmt.Errorf("get platform revenue account for payment: %w", err)
		}
		entries = append(entries, ledger.LedgerEntry{
			TransactionID: lt.ID,
			AccountID:     revAcc.ID,
			Direction:     "credit",
			Amount:        revenueCredit,
			Currency:      inv.Currency,
		})
	}

	if err := s.ledgerRepo.CreateEntries(ctx, tx, entries); err != nil {
		return nil, err
	}

	newWalletBal := lockedWallet.CachedBalance.Add(walletCredit)
	if err := s.ledgerRepo.UpdateWalletCachedBalance(ctx, tx, lockedWallet.ID, newWalletBal, lockedWallet.Version); err != nil {
		return nil, err
	}

	// Update invoice amount_paid and status
	newAmountPaid := inv.AmountPaid.Add(req.Amount)
	newStatus := "issued"
	if newAmountPaid.Equal(inv.Total) {
		newStatus = "paid"
	}
	if err := s.repo.UpdateInvoiceStatusAndPaid(ctx, tx, inv.ID, newStatus, nil, newAmountPaid, inv.Version); err != nil {
		return nil, err
	}

	payDate := time.Now()
	if req.PaymentDate != nil {
		payDate = *req.PaymentDate
	}

	var idempKeyPtr *string
	if idempotencyKey != "" {
		idempKeyPtr = &idempotencyKey
	}

	payment, err := s.repo.CreatePayment(ctx, tx, Payment{
		InvoiceID:           inv.ID,
		CustomerOrgID:       inv.CustomerOrgID,
		Amount:              req.Amount,
		Currency:            inv.Currency,
		PaymentMethod:       req.PaymentMethod,
		Reference:           req.Reference,
		PaymentDate:         payDate,
		LedgerTransactionID: &lt.ID,
		Notes:               req.Notes,
		IdempotencyKey:      idempKeyPtr,
		CreatedBy:           &tc.ActorUserID,
	})
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		TargetOrgID: &inv.CustomerOrgID,
		Action:      "billing.payment.create",
		Module:      "billing",
		EntityType:  "payment",
		EntityID:    payment.ID.String(),
		Reason:      fmt.Sprintf("Recorded payment of %s %s against invoice %s", req.Amount.String(), inv.Currency, *inv.InvoiceNumber),
		IPAddress:   ip,
		UserAgent:   userAgent,
		AfterValue: audit.RedactValue(map[string]any{
			"payment_id":      payment.ID.String(),
			"invoice_id":      inv.ID.String(),
			"invoice_number":  *inv.InvoiceNumber,
			"amount":          req.Amount.String(),
			"new_amount_paid": newAmountPaid.String(),
			"new_status":      newStatus,
			"transaction_id":  lt.ID.String(),
		}),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit payment: %w", err)
	}

	payment.InvoiceNumber = *inv.InvoiceNumber
	return payment, nil
}

func (s *Service) ListPayments(ctx context.Context, tc *tenant.Context, invoiceID, customerOrgID *uuid.UUID, page, pageSize int) ([]Payment, int, error) {
	if tc == nil || !tc.HasPermission("billing.payment.view") {
		return nil, 0, ErrPermissionDenied
	}
	return s.repo.ListPayments(ctx, tc, invoiceID, customerOrgID, page, pageSize)
}

func (s *Service) GetPayment(ctx context.Context, tc *tenant.Context, id uuid.UUID) (*Payment, error) {
	if tc == nil || !tc.HasPermission("billing.payment.view") {
		return nil, ErrPermissionDenied
	}
	p, err := s.repo.GetPaymentByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !tc.IsPlatformAdmin && !tc.IsPlatformOrg() {
		targetOrg, err := s.orgRepo.GetByID(ctx, p.CustomerOrgID)
		if err != nil || !tc.CanAccessOrg(targetOrg.Path) {
			return nil, ErrCrossTenantAccess
		}
	}
	return p, nil
}

// -------------------------------------------------------------------------
// Printable HTML Invoice (No Secrets, No Provider Cost/Margin)
// -------------------------------------------------------------------------

func (s *Service) GetPrintableInvoiceHTML(ctx context.Context, tc *tenant.Context, id uuid.UUID) (string, error) {
	inv, err := s.authorizeInvoiceAccess(ctx, tc, id, "billing.invoice.view")
	if err != nil {
		return "", err
	}

	invNum := "DRAFT"
	if inv.InvoiceNumber != nil {
		invNum = *inv.InvoiceNumber
	}
	issueStr := "N/A"
	if inv.IssueDate != nil {
		issueStr = inv.IssueDate.Format("2006-01-02")
	}
	dueStr := "N/A"
	if inv.DueDate != nil {
		dueStr = inv.DueDate.Format("2006-01-02")
	}

	var linesHTML strings.Builder
	for _, l := range inv.Lines {
		linesHTML.WriteString(fmt.Sprintf(`
			<tr>
				<td style="padding: 8px; border-bottom: 1px solid #e5e7eb;">%d</td>
				<td style="padding: 8px; border-bottom: 1px solid #e5e7eb;">%s</td>
				<td style="padding: 8px; border-bottom: 1px solid #e5e7eb; text-align: right;">%s</td>
				<td style="padding: 8px; border-bottom: 1px solid #e5e7eb; text-align: right;">%s</td>
				<td style="padding: 8px; border-bottom: 1px solid #e5e7eb; text-align: right;">%s</td>
			</tr>
		`, l.LineNumber, html.EscapeString(l.Description), l.Quantity.StringFixed(ledger.MoneyScale), l.UnitPrice.StringFixed(ledger.MoneyScale), l.Amount.StringFixed(ledger.MoneyScale)))
	}

	htmlContent := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
	<meta charset="utf-8">
	<title>Invoice %s</title>
	<style>
		body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; color: #1f2937; margin: 40px; }
		.header { display: flex; justify-content: space-between; align-items: flex-start; margin-bottom: 30px; border-bottom: 2px solid #3b82f6; padding-bottom: 20px; }
		.logo { font-size: 24px; font-weight: bold; color: #1e3a8a; }
		.invoice-meta { text-align: right; }
		.badge { display: inline-block; padding: 4px 8px; border-radius: 4px; font-size: 12px; font-weight: bold; text-transform: uppercase; }
		.status-draft { background: #f3f4f6; color: #4b5563; }
		.status-issued { background: #dbeafe; color: #1e40af; }
		.status-paid { background: #dcfce7; color: #166534; }
		.status-void { background: #fee2e2; color: #991b1b; }
		.details-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 20px; margin-bottom: 30px; }
		table { width: 100%%; border-collapse: collapse; margin-bottom: 30px; }
		th { background: #f8fafc; padding: 10px 8px; text-align: left; font-size: 13px; text-transform: uppercase; color: #64748b; border-bottom: 2px solid #cbd5e1; }
		.totals { margin-left: auto; width: 300px; }
		.totals-row { display: flex; justify-content: space-between; padding: 6px 0; }
		.totals-total { border-top: 2px solid #1e3a8a; font-weight: bold; font-size: 16px; margin-top: 6px; padding-top: 6px; }
		@media print {
			body { margin: 0; }
			.no-print { display: none; }
		}
	</style>
</head>
<body>
	<div class="no-print" style="margin-bottom: 20px;">
		<button onclick="window.print()" style="background: #2563eb; color: white; border: none; padding: 8px 16px; border-radius: 4px; cursor: pointer; font-weight: bold;">Print / Save as PDF</button>
	</div>
	<div class="header">
		<div>
			<div class="logo">ZNERO INTERNATIONAL</div>
			<div style="color: #6b7280; font-size: 14px; margin-top: 4px;">Global Enterprise Messaging</div>
		</div>
		<div class="invoice-meta">
			<div style="font-size: 20px; font-weight: bold;">INVOICE %s</div>
			<div style="margin-top: 4px;"><span class="badge status-%s">%s</span></div>
		</div>
	</div>

	<div class="details-grid">
		<div>
			<div style="font-weight: bold; color: #6b7280; font-size: 12px; text-transform: uppercase;">Billed To:</div>
			<div style="font-size: 16px; font-weight: bold; margin-top: 4px;">%s</div>
		</div>
		<div style="text-align: right;">
			<div><strong>Issue Date:</strong> %s</div>
			<div><strong>Due Date:</strong> %s</div>
			<div><strong>Currency:</strong> %s</div>
		</div>
	</div>

	<table>
		<thead>
			<tr>
				<th style="width: 40px;">#</th>
				<th>Description</th>
				<th style="text-align: right; width: 100px;">Quantity</th>
				<th style="text-align: right; width: 120px;">Unit Price</th>
				<th style="text-align: right; width: 120px;">Amount</th>
			</tr>
		</thead>
		<tbody>
			%s
		</tbody>
	</table>

	<div class="totals">
		<div class="totals-row">
			<div>Subtotal:</div>
			<div>%s %s</div>
		</div>
		<div class="totals-row">
			<div>%s:</div>
			<div>%s %s</div>
		</div>
		<div class="totals-row totals-total">
			<div>Total:</div>
			<div>%s %s</div>
		</div>
		<div class="totals-row" style="color: #166534; margin-top: 4px;">
			<div>Amount Paid:</div>
			<div>%s %s</div>
		</div>
		<div class="totals-row" style="font-weight: bold; color: #b91c1c; border-top: 1px dashed #cbd5e1; margin-top: 4px; padding-top: 4px;">
			<div>Balance Due:</div>
			<div>%s %s</div>
		</div>
	</div>

	%s
</body>
</html>`,
		html.EscapeString(invNum),
		html.EscapeString(invNum),
		html.EscapeString(inv.Status),
		strings.ToUpper(html.EscapeString(inv.Status)),
		html.EscapeString(inv.CustomerOrgName),
		issueStr,
		dueStr,
		html.EscapeString(inv.Currency),
		linesHTML.String(),
		inv.Subtotal.StringFixed(ledger.MoneyScale), html.EscapeString(inv.Currency),
		html.EscapeString(inv.TaxLabel), inv.TaxAmount.StringFixed(ledger.MoneyScale), html.EscapeString(inv.Currency),
		inv.Total.StringFixed(ledger.MoneyScale), html.EscapeString(inv.Currency),
		inv.AmountPaid.StringFixed(ledger.MoneyScale), html.EscapeString(inv.Currency),
		inv.OutstandingBalance.StringFixed(ledger.MoneyScale), html.EscapeString(inv.Currency),
		formatNotes(inv.Notes),
	)

	return htmlContent, nil
}

func formatNotes(notes string) string {
	if strings.TrimSpace(notes) == "" {
		return ""
	}
	return fmt.Sprintf(`<div style="margin-top: 40px; padding: 12px; background: #f8fafc; border-left: 4px solid #94a3b8; font-size: 13px;"><strong>Notes:</strong> %s</div>`, html.EscapeString(notes))
}

// -------------------------------------------------------------------------
// Void Approval Applier
// -------------------------------------------------------------------------

type voidInvoiceApplier struct {
	s *Service
}

func (a *voidInvoiceApplier) ApplyChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	var payload struct {
		InvoiceID uuid.UUID `json:"invoice_id"`
		Reason    string    `json:"reason"`
		IPAddress string    `json:"ip_address"`
		UserAgent string    `json:"user_agent"`
	}
	if err := json.Unmarshal(cr.ProposedData, &payload); err != nil {
		return fmt.Errorf("unmarshal void request: %w", err)
	}

	var checkerID uuid.UUID
	if cr.CheckerUserID != nil {
		checkerID = *cr.CheckerUserID
	} else {
		checkerID = cr.MakerUserID
	}

	return a.s.executeVoid(ctx, tx, checkerID, uuid.Nil, payload.InvoiceID, payload.Reason, payload.IPAddress, payload.UserAgent)
}
