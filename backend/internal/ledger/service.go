package ledger

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

type Service struct {
	repo        Repository
	orgRepo     org.Repository
	approvalSvc *approval.Service
	auditSvc    *audit.Service
	pool        *pgxpool.Pool
}

func NewService(
	repo Repository,
	orgRepo org.Repository,
	approvalSvc *approval.Service,
	auditSvc *audit.Service,
	pool *pgxpool.Pool,
) *Service {
	s := &Service{
		repo:        repo,
		orgRepo:     orgRepo,
		approvalSvc: approvalSvc,
		auditSvc:    auditSvc,
		pool:        pool,
	}

	// Register approval appliers and permissions
	if approvalSvc != nil {
		approvalSvc.RegisterApplier("wallet_topup", &topupApplier{s: s})
		approvalSvc.RegisterApprovePermission("wallet_topup", "wallet.approve")

		approvalSvc.RegisterApplier("wallet_adjustment", &adjustmentApplier{s: s})
		approvalSvc.RegisterApprovePermission("wallet_adjustment", "wallet.approve")

		approvalSvc.RegisterApplier("wallet_unfreeze", &unfreezeApplier{s: s})
		approvalSvc.RegisterApprovePermission("wallet_unfreeze", "wallet.approve")
	}

	return s
}

// -------------------------------------------------------------------------
// Helper: Tenancy & Permissions
// -------------------------------------------------------------------------

func (s *Service) authorizeWalletAccess(ctx context.Context, tc *tenant.Context, walletID uuid.UUID, perm string) (*Wallet, error) {
	if tc == nil {
		return nil, ErrPermissionDenied
	}
	if perm != "" && !tc.HasPermission(perm) {
		return nil, ErrPermissionDenied
	}

	w, err := s.repo.GetWalletByID(ctx, walletID)
	if err != nil {
		return nil, err
	}

	if tc.IsPlatformAdmin || tc.IsPlatformOrg() {
		return w, nil
	}

	// Verify organizational access via materialized path
	targetOrg, err := s.orgRepo.GetByID(ctx, w.OrganizationID)
	if err != nil {
		return nil, ErrCrossTenantAccess
	}
	if !tc.CanAccessOrg(targetOrg.Path) {
		return nil, ErrCrossTenantAccess
	}

	return w, nil
}

// -------------------------------------------------------------------------
// Wallets
// -------------------------------------------------------------------------

func (s *Service) EnsureWallet(ctx context.Context, tc *tenant.Context, orgID uuid.UUID, currency, accountType string) (*Wallet, error) {
	if tc == nil || (!tc.IsPlatformAdmin && !tc.IsPlatformOrg()) {
		return nil, ErrPermissionDenied
	}
	if orgID == uuid.Nil {
		return nil, fmt.Errorf("%w: organization_id is required", ErrValidation)
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = "USD"
	}
	if len(currency) != 3 {
		return nil, fmt.Errorf("%w: currency must be a 3-letter code", ErrValidation)
	}
	accountType = strings.ToLower(strings.TrimSpace(accountType))
	if accountType == "" {
		accountType = "prepaid"
	}
	if accountType != "prepaid" && accountType != "postpaid" {
		return nil, fmt.Errorf("%w: account_type must be prepaid or postpaid", ErrValidation)
	}
	if s.orgRepo != nil {
		if target, err := s.orgRepo.GetByID(ctx, orgID); err != nil || target == nil {
			return nil, ErrWalletNotFound
		}
	}
	return s.repo.EnsureWalletForOrg(ctx, nil, orgID, currency, accountType)
}

func (s *Service) GetWallet(ctx context.Context, tc *tenant.Context, walletID uuid.UUID) (*Wallet, error) {
	return s.authorizeWalletAccess(ctx, tc, walletID, "wallet.wallet.view")
}

func (s *Service) GetWalletByOrgID(ctx context.Context, tc *tenant.Context, orgID uuid.UUID) (*Wallet, error) {
	if tc == nil {
		return nil, ErrPermissionDenied
	}
	if !tc.HasPermission("wallet.wallet.view") {
		return nil, ErrPermissionDenied
	}

	if !tc.IsPlatformAdmin && !tc.IsPlatformOrg() {
		targetOrg, err := s.orgRepo.GetByID(ctx, orgID)
		if err != nil {
			return nil, ErrCrossTenantAccess
		}
		if !tc.CanAccessOrg(targetOrg.Path) {
			return nil, ErrCrossTenantAccess
		}
	}

	return s.repo.GetWalletByOrgID(ctx, orgID)
}

// GetWalletByOrgIDInternal retrieves a wallet for internal trusted backend services (e.g. messaging hold
// placement after message.send authorization has already been verified). Never expose to untrusted callers.
func (s *Service) GetWalletByOrgIDInternal(ctx context.Context, orgID uuid.UUID) (*Wallet, error) {
	return s.repo.GetWalletByOrgID(ctx, orgID)
}

func (s *Service) ListWallets(ctx context.Context, tc *tenant.Context, search, status string, page, pageSize int) ([]Wallet, int, error) {
	if tc == nil || !tc.HasPermission("wallet.wallet.view") {
		return nil, 0, ErrPermissionDenied
	}
	return s.repo.ListWallets(ctx, tc, search, status, page, pageSize)
}

// -------------------------------------------------------------------------
// Top-Ups (Maker-Checker & Override)
// -------------------------------------------------------------------------

func (s *Service) Topup(ctx context.Context, tc *tenant.Context, walletID uuid.UUID, req WalletTopupRequest, ip, userAgent string) (*LedgerTransaction, *approval.ChangeRequest, error) {
	if tc == nil || (!tc.IsPlatformAdmin && !tc.IsPlatformOrg()) {
		return nil, nil, ErrPermissionDenied
	}
	if !tc.HasPermission("wallet.topup.create") {
		return nil, nil, ErrPermissionDenied
	}

	req.Amount = req.Amount.Truncate(MoneyScale)
	if req.Amount.LessThanOrEqual(decimal.Zero) {
		return nil, nil, ErrNegativeAmount
	}
	if req.Amount.GreaterThan(MaxTransactionAmount) {
		return nil, nil, ErrAmountExceedsMaximum
	}
	if strings.TrimSpace(req.Reason) == "" {
		return nil, nil, ErrReasonRequired
	}

	w, err := s.repo.GetWalletByID(ctx, walletID)
	if err != nil {
		return nil, nil, err
	}
	if w.Status != "active" {
		if w.Status == "frozen" {
			return nil, nil, ErrWalletFrozen
		}
		return nil, nil, ErrWalletClosed
	}

	// If caller does not possess wallet.override, route to Maker-Checker queue
	if !tc.HasPermission("wallet.override") && s.approvalSvc != nil {
		proposedData, err := json.Marshal(map[string]any{
			"wallet_id":       walletID,
			"amount":          req.Amount.String(),
			"reason":          req.Reason,
			"reference":       req.Reference,
			"idempotency_key": req.IdempotencyKey,
			"actor_org_id":    tc.ActorOrgID,
			"ip_address":      ip,
			"user_agent":      userAgent,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("marshal topup change: %w", err)
		}

		cr, err := s.approvalSvc.SubmitChangeRequest(ctx, nil, approval.ChangeRequest{
			EntityType:    "wallet_topup",
			EntityID:      &walletID,
			Operation:     "topup",
			ProposedData:  proposedData,
			MakerUserID:   tc.ActorUserID,
			EntityVersion: &w.Version,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("submit topup request: %w", err)
		}

		_ = s.auditSvc.Record(ctx, nil, audit.Entry{
			ActorUserID: &tc.ActorUserID,
			ActorOrgID:  &tc.ActorOrgID,
			TargetOrgID: &w.OrganizationID,
			Action:      "wallet.topup.request",
			Module:      "wallet",
			EntityType:  "wallet",
			EntityID:    w.ID.String(),
			Reason:      req.Reason,
			IPAddress:   ip,
			UserAgent:   userAgent,
		})

		return nil, cr, nil
	}

	// Direct execution (Override permission held)
	tx, err := s.executeTopup(ctx, nil, tc.ActorUserID, tc.ActorOrgID, walletID, req, ip, userAgent)
	return tx, nil, err
}

func (s *Service) executeTopup(
	ctx context.Context,
	dbTx pgx.Tx,
	actorUserID, actorOrgID uuid.UUID,
	walletID uuid.UUID,
	req WalletTopupRequest,
	ip, userAgent string,
) (*LedgerTransaction, error) {
	runTx := func(tx pgx.Tx) (*LedgerTransaction, error) {
		// Row lock wallet
		w, err := s.repo.GetWalletByIDForUpdate(ctx, tx, walletID)
		if err != nil {
			return nil, err
		}
		if w.Status != "active" {
			if w.Status == "frozen" {
				return nil, ErrWalletFrozen
			}
			return nil, ErrWalletClosed
		}

		// Idempotency check
		if req.IdempotencyKey != "" {
			existing, err := s.repo.GetTransactionByIdempotency(ctx, tx, &w.OrganizationID, "topup", req.IdempotencyKey)
			if err != nil {
				return nil, err
			}
			if existing != nil {
				// Replay original result
				return existing, nil
			}
		}

		// System cash/customer funds account
		sysAccCode := "CUSTOMER_FUNDS"
		if w.Currency == "EUR" {
			sysAccCode = "CUSTOMER_FUNDS_EUR"
		}
		sysAcc, err := s.repo.GetSystemAccount(ctx, tx, sysAccCode)
		if err != nil {
			return nil, err
		}

		walletAcc, err := s.repo.GetAccountByWalletID(ctx, tx, w.ID)
		if err != nil {
			return nil, err
		}

		desc := fmt.Sprintf("Top-up of %s %s", req.Amount.StringFixed(MoneyScale), w.Currency)
		if req.Reference != "" {
			desc += fmt.Sprintf(" (Ref: %s)", req.Reference)
		}

		idemp := req.IdempotencyKey
		var idempPtr *string
		if idemp != "" {
			idempPtr = &idemp
		}
		reason := req.Reason
		var reasonPtr *string
		if reason != "" {
			reasonPtr = &reason
		}
		var refTypePtr *string
		if req.Reference != "" {
			rt := "external_ref"
			refTypePtr = &rt
		}

		var actorUserIDPtr *uuid.UUID
		if actorUserID != uuid.Nil {
			actorUserIDPtr = &actorUserID
		}
		var actorOrgIDPtr *uuid.UUID
		if actorOrgID != uuid.Nil {
			actorOrgIDPtr = &actorOrgID
		}

		lt, err := s.repo.CreateTransaction(ctx, tx, LedgerTransaction{
			Type:           "topup",
			ReferenceType:  refTypePtr,
			IdempotencyKey: idempPtr,
			Description:    desc,
			Reason:         reasonPtr,
			TenantOrgID:    &w.OrganizationID,
			ActorUserID:    actorUserIDPtr,
			ActorOrgID:     actorOrgIDPtr,
		})
		if err != nil {
			return nil, err
		}

		// Double-entry: Debit Customer Funds, Credit Wallet Account
		entries := []LedgerEntry{
			{
				TransactionID: lt.ID,
				AccountID:     sysAcc.ID,
				Direction:     "debit",
				Amount:        req.Amount,
				Currency:      w.Currency,
			},
			{
				TransactionID: lt.ID,
				AccountID:     walletAcc.ID,
				Direction:     "credit",
				Amount:        req.Amount,
				Currency:      w.Currency,
			},
		}
		if err := s.repo.CreateEntries(ctx, tx, entries); err != nil {
			return nil, err
		}

		// Update cached balance
		newBalance := w.CachedBalance.Add(req.Amount)
		if err := s.repo.UpdateWalletCachedBalance(ctx, tx, w.ID, newBalance, w.Version); err != nil {
			return nil, err
		}

		// Audit
		_ = s.auditSvc.Record(ctx, tx, audit.Entry{
			ActorUserID: &actorUserID,
			ActorOrgID:  &actorOrgID,
			TargetOrgID: &w.OrganizationID,
			Action:      "wallet.topup.execute",
			Module:      "wallet",
			EntityType:  "wallet",
			EntityID:    w.ID.String(),
			Reason:      req.Reason,
			IPAddress:   ip,
			UserAgent:   userAgent,
			AfterValue: audit.RedactValue(map[string]any{
				"amount":         req.Amount.String(),
				"new_balance":    newBalance.String(),
				"transaction_id": lt.ID.String(),
				"reference":      req.Reference,
			}),
		})

		lt.Entries = entries
		return lt, nil
	}

	if dbTx != nil {
		return runTx(dbTx)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	lt, err := runTx(tx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit topup: %w", err)
	}
	return lt, nil
}

// -------------------------------------------------------------------------
// Adjustments (Maker-Checker & Override)
// -------------------------------------------------------------------------

func (s *Service) Adjust(ctx context.Context, tc *tenant.Context, walletID uuid.UUID, req WalletAdjustmentRequest, ip, userAgent string) (*LedgerTransaction, *approval.ChangeRequest, error) {
	if tc == nil || (!tc.IsPlatformAdmin && !tc.IsPlatformOrg()) {
		return nil, nil, ErrPermissionDenied
	}
	if !tc.HasPermission("wallet.adjustment.create") {
		return nil, nil, ErrPermissionDenied
	}

	req.Amount = req.Amount.Truncate(MoneyScale)
	if req.Amount.LessThanOrEqual(decimal.Zero) {
		return nil, nil, ErrNegativeAmount
	}
	if req.Amount.GreaterThan(MaxTransactionAmount) {
		return nil, nil, ErrAmountExceedsMaximum
	}
	req.Direction = strings.ToLower(strings.TrimSpace(req.Direction))
	if req.Direction != "credit" && req.Direction != "debit" {
		return nil, nil, errors.New("direction must be 'credit' or 'debit'")
	}
	if strings.TrimSpace(req.Reason) == "" {
		return nil, nil, ErrReasonRequired
	}

	w, err := s.repo.GetWalletByID(ctx, walletID)
	if err != nil {
		return nil, nil, err
	}
	if w.Status == "closed" {
		return nil, nil, ErrWalletClosed
	}
	if w.Status == "frozen" && req.Direction == "debit" {
		return nil, nil, ErrWalletFrozen
	}

	if !tc.HasPermission("wallet.override") && s.approvalSvc != nil {
		proposedData, err := json.Marshal(map[string]any{
			"wallet_id":       walletID,
			"amount":          req.Amount.String(),
			"direction":       req.Direction,
			"reason":          req.Reason,
			"reference":       req.Reference,
			"idempotency_key": req.IdempotencyKey,
			"actor_org_id":    tc.ActorOrgID,
			"ip_address":      ip,
			"user_agent":      userAgent,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("marshal adjust change: %w", err)
		}

		cr, err := s.approvalSvc.SubmitChangeRequest(ctx, nil, approval.ChangeRequest{
			EntityType:    "wallet_adjustment",
			EntityID:      &walletID,
			Operation:     "adjustment",
			ProposedData:  proposedData,
			MakerUserID:   tc.ActorUserID,
			EntityVersion: &w.Version,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("submit adjustment request: %w", err)
		}

		_ = s.auditSvc.Record(ctx, nil, audit.Entry{
			ActorUserID: &tc.ActorUserID,
			ActorOrgID:  &tc.ActorOrgID,
			TargetOrgID: &w.OrganizationID,
			Action:      "wallet.adjustment.request",
			Module:      "wallet",
			EntityType:  "wallet",
			EntityID:    w.ID.String(),
			Reason:      req.Reason,
			IPAddress:   ip,
			UserAgent:   userAgent,
		})

		return nil, cr, nil
	}

	tx, err := s.executeAdjustment(ctx, nil, tc.ActorUserID, tc.ActorOrgID, walletID, req, ip, userAgent)
	return tx, nil, err
}

func (s *Service) executeAdjustment(
	ctx context.Context,
	dbTx pgx.Tx,
	actorUserID, actorOrgID uuid.UUID,
	walletID uuid.UUID,
	req WalletAdjustmentRequest,
	ip, userAgent string,
) (*LedgerTransaction, error) {
	runTx := func(tx pgx.Tx) (*LedgerTransaction, error) {
		w, err := s.repo.GetWalletByIDForUpdate(ctx, tx, walletID)
		if err != nil {
			return nil, err
		}
		if w.Status == "closed" {
			return nil, ErrWalletClosed
		}
		if w.Status == "frozen" && req.Direction == "debit" {
			return nil, ErrWalletFrozen
		}

		// Idempotency check
		if req.IdempotencyKey != "" {
			existing, err := s.repo.GetTransactionByIdempotency(ctx, tx, &w.OrganizationID, "adjustment", req.IdempotencyKey)
			if err != nil {
				return nil, err
			}
			if existing != nil {
				return existing, nil
			}
		}

		// Overspend check if debit
		if req.Direction == "debit" {
			if w.AvailableBalance.LessThan(req.Amount) {
				return nil, ErrInsufficientFunds
			}
		}

		sysAccCode := "ADJUSTMENTS"
		if w.Currency == "EUR" {
			sysAccCode = "ADJUSTMENTS_EUR"
		}
		sysAcc, err := s.repo.GetSystemAccount(ctx, tx, sysAccCode)
		if err != nil {
			return nil, err
		}

		walletAcc, err := s.repo.GetAccountByWalletID(ctx, tx, w.ID)
		if err != nil {
			return nil, err
		}

		desc := fmt.Sprintf("Adjustment (%s) of %s %s", req.Direction, req.Amount.StringFixed(MoneyScale), w.Currency)
		if req.Reference != "" {
			desc += fmt.Sprintf(" (Ref: %s)", req.Reference)
		}

		idemp := req.IdempotencyKey
		var idempPtr *string
		if idemp != "" {
			idempPtr = &idemp
		}
		reason := req.Reason

		var actorUserIDPtr *uuid.UUID
		if actorUserID != uuid.Nil {
			actorUserIDPtr = &actorUserID
		}
		var actorOrgIDPtr *uuid.UUID
		if actorOrgID != uuid.Nil {
			actorOrgIDPtr = &actorOrgID
		}

		lt, err := s.repo.CreateTransaction(ctx, tx, LedgerTransaction{
			Type:           "adjustment",
			IdempotencyKey: idempPtr,
			Description:    desc,
			Reason:         &reason,
			TenantOrgID:    &w.OrganizationID,
			ActorUserID:    actorUserIDPtr,
			ActorOrgID:     actorOrgIDPtr,
		})
		if err != nil {
			return nil, err
		}

		var entries []LedgerEntry
		var newBalance decimal.Decimal
		if req.Direction == "credit" {
			// Debit Adjustments, Credit Wallet Account
			entries = []LedgerEntry{
				{
					TransactionID: lt.ID,
					AccountID:     sysAcc.ID,
					Direction:     "debit",
					Amount:        req.Amount,
					Currency:      w.Currency,
				},
				{
					TransactionID: lt.ID,
					AccountID:     walletAcc.ID,
					Direction:     "credit",
					Amount:        req.Amount,
					Currency:      w.Currency,
				},
			}
			newBalance = w.CachedBalance.Add(req.Amount)
		} else {
			// Debit Wallet Account, Credit Adjustments
			entries = []LedgerEntry{
				{
					TransactionID: lt.ID,
					AccountID:     walletAcc.ID,
					Direction:     "debit",
					Amount:        req.Amount,
					Currency:      w.Currency,
				},
				{
					TransactionID: lt.ID,
					AccountID:     sysAcc.ID,
					Direction:     "credit",
					Amount:        req.Amount,
					Currency:      w.Currency,
				},
			}
			newBalance = w.CachedBalance.Sub(req.Amount)
		}

		if err := s.repo.CreateEntries(ctx, tx, entries); err != nil {
			return nil, err
		}

		if err := s.repo.UpdateWalletCachedBalance(ctx, tx, w.ID, newBalance, w.Version); err != nil {
			return nil, err
		}

		_ = s.auditSvc.Record(ctx, tx, audit.Entry{
			ActorUserID: &actorUserID,
			ActorOrgID:  &actorOrgID,
			TargetOrgID: &w.OrganizationID,
			Action:      "wallet.adjustment.execute",
			Module:      "wallet",
			EntityType:  "wallet",
			EntityID:    w.ID.String(),
			Reason:      req.Reason,
			IPAddress:   ip,
			UserAgent:   userAgent,
			AfterValue: audit.RedactValue(map[string]any{
				"direction":      req.Direction,
				"amount":         req.Amount.String(),
				"new_balance":    newBalance.String(),
				"transaction_id": lt.ID.String(),
				"reference":      req.Reference,
			}),
		})

		lt.Entries = entries
		return lt, nil
	}

	if dbTx != nil {
		return runTx(dbTx)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	lt, err := runTx(tx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit adjustment: %w", err)
	}
	return lt, nil
}

// -------------------------------------------------------------------------
// Freeze / Unfreeze
// -------------------------------------------------------------------------

func (s *Service) FreezeWallet(ctx context.Context, tc *tenant.Context, walletID uuid.UUID, reason, ip, userAgent string) error {
	if tc == nil || (!tc.IsPlatformAdmin && !tc.IsPlatformOrg()) {
		return ErrPermissionDenied
	}
	if !tc.HasPermission("wallet.freeze") {
		return ErrPermissionDenied
	}
	if strings.TrimSpace(reason) == "" {
		return ErrReasonRequired
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	w, err := s.repo.GetWalletByIDForUpdate(ctx, tx, walletID)
	if err != nil {
		return err
	}
	if w.Status == "frozen" {
		return nil // already frozen
	}
	if w.Status == "closed" {
		return ErrWalletClosed
	}

	if err := s.repo.UpdateWalletStatus(ctx, tx, w.ID, "frozen", w.Version); err != nil {
		return err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		TargetOrgID: &w.OrganizationID,
		Action:      "wallet.freeze",
		Module:      "wallet",
		EntityType:  "wallet",
		EntityID:    w.ID.String(),
		Reason:      reason,
		IPAddress:   ip,
		UserAgent:   userAgent,
	})

	return tx.Commit(ctx)
}

func (s *Service) UnfreezeWallet(ctx context.Context, tc *tenant.Context, walletID uuid.UUID, reason, ip, userAgent string) (*approval.ChangeRequest, error) {
	if tc == nil || (!tc.IsPlatformAdmin && !tc.IsPlatformOrg()) {
		return nil, ErrPermissionDenied
	}
	if !tc.HasPermission("wallet.freeze") {
		return nil, ErrPermissionDenied
	}
	if strings.TrimSpace(reason) == "" {
		return nil, ErrReasonRequired
	}

	w, err := s.repo.GetWalletByID(ctx, walletID)
	if err != nil {
		return nil, err
	}
	if w.Status != "frozen" {
		return nil, errors.New("wallet is not frozen")
	}

	// Unfreeze requires Maker-Checker unless wallet.override is held
	if !tc.HasPermission("wallet.override") && s.approvalSvc != nil {
		proposedData, err := json.Marshal(map[string]any{
			"wallet_id":    walletID,
			"reason":       reason,
			"actor_org_id": tc.ActorOrgID,
			"ip_address":   ip,
			"user_agent":   userAgent,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal unfreeze: %w", err)
		}

		cr, err := s.approvalSvc.SubmitChangeRequest(ctx, nil, approval.ChangeRequest{
			EntityType:    "wallet_unfreeze",
			EntityID:      &walletID,
			Operation:     "unfreeze",
			ProposedData:  proposedData,
			MakerUserID:   tc.ActorUserID,
			EntityVersion: &w.Version,
		})
		if err != nil {
			return nil, err
		}

		_ = s.auditSvc.Record(ctx, nil, audit.Entry{
			ActorUserID: &tc.ActorUserID,
			ActorOrgID:  &tc.ActorOrgID,
			TargetOrgID: &w.OrganizationID,
			Action:      "wallet.unfreeze.request",
			Module:      "wallet",
			EntityType:  "wallet",
			EntityID:    w.ID.String(),
			Reason:      reason,
			IPAddress:   ip,
			UserAgent:   userAgent,
		})

		return cr, nil
	}

	// Direct unfreeze
	err = s.executeUnfreeze(ctx, nil, tc.ActorUserID, tc.ActorOrgID, walletID, reason, ip, userAgent)
	return nil, err
}

func (s *Service) executeUnfreeze(
	ctx context.Context,
	dbTx pgx.Tx,
	actorUserID, actorOrgID uuid.UUID,
	walletID uuid.UUID,
	reason, ip, userAgent string,
) error {
	runTx := func(tx pgx.Tx) error {
		w, err := s.repo.GetWalletByIDForUpdate(ctx, tx, walletID)
		if err != nil {
			return err
		}
		if w.Status != "frozen" {
			return nil
		}

		if err := s.repo.UpdateWalletStatus(ctx, tx, w.ID, "active", w.Version); err != nil {
			return err
		}

		var actorUserIDPtr *uuid.UUID
		if actorUserID != uuid.Nil {
			actorUserIDPtr = &actorUserID
		}
		var actorOrgIDPtr *uuid.UUID
		if actorOrgID != uuid.Nil {
			actorOrgIDPtr = &actorOrgID
		}

		_ = s.auditSvc.Record(ctx, tx, audit.Entry{
			ActorUserID: actorUserIDPtr,
			ActorOrgID:  actorOrgIDPtr,
			TargetOrgID: &w.OrganizationID,
			Action:      "wallet.unfreeze.execute",
			Module:      "wallet",
			EntityType:  "wallet",
			EntityID:    w.ID.String(),
			Reason:      reason,
			IPAddress:   ip,
			UserAgent:   userAgent,
		})
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

// -------------------------------------------------------------------------
// Reseller-Funded Transfer (Atomic, Two-Leg, Tenant-Checked)
// -------------------------------------------------------------------------

func (s *Service) TransferResellerToCustomer(ctx context.Context, tc *tenant.Context, req WalletTransferRequest, ip, userAgent string) (*LedgerTransaction, error) {
	if tc == nil {
		return nil, ErrPermissionDenied
	}

	req.Amount = req.Amount.Truncate(MoneyScale)
	if req.Amount.LessThanOrEqual(decimal.Zero) {
		return nil, ErrNegativeAmount
	}

	srcWallet, err := s.repo.GetWalletByID(ctx, req.SourceWalletID)
	if err != nil {
		return nil, fmt.Errorf("source wallet: %w", err)
	}
	dstWallet, err := s.repo.GetWalletByID(ctx, req.DestinationWalletID)
	if err != nil {
		return nil, fmt.Errorf("destination wallet: %w", err)
	}

	// Currency match
	if srcWallet.Currency != dstWallet.Currency {
		return nil, fmt.Errorf("%w: source (%s) != dest (%s)", ErrCurrencyMismatch, srcWallet.Currency, dstWallet.Currency)
	}

	// Tenant verification:
	// If caller is reseller, caller must own srcWallet, and dstWallet must be a direct child customer!
	srcOrg, err := s.orgRepo.GetByID(ctx, srcWallet.OrganizationID)
	if err != nil {
		return nil, err
	}
	dstOrg, err := s.orgRepo.GetByID(ctx, dstWallet.OrganizationID)
	if err != nil {
		return nil, err
	}

	if !tc.IsPlatformAdmin && !tc.IsPlatformOrg() {
		if tc.ActorOrgID != srcOrg.ID {
			return nil, ErrCrossTenantAccess
		}
		if !strings.HasPrefix(dstOrg.Path, srcOrg.Path) || dstOrg.ID == srcOrg.ID {
			return nil, fmt.Errorf("%w: destination organization is not a child of your reseller organization", ErrCrossTenantAccess)
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Deadlock prevention: lock wallets in deterministic order by UUID string
	var firstID, secondID uuid.UUID
	if srcWallet.ID.String() < dstWallet.ID.String() {
		firstID, secondID = srcWallet.ID, dstWallet.ID
	} else {
		firstID, secondID = dstWallet.ID, srcWallet.ID
	}

	w1, err := s.repo.GetWalletByIDForUpdate(ctx, tx, firstID)
	if err != nil {
		return nil, err
	}
	w2, err := s.repo.GetWalletByIDForUpdate(ctx, tx, secondID)
	if err != nil {
		return nil, err
	}

	var lockedSrc, lockedDst *Wallet
	if w1.ID == srcWallet.ID {
		lockedSrc, lockedDst = w1, w2
	} else {
		lockedSrc, lockedDst = w2, w1
	}

	if lockedSrc.Status != "active" {
		return nil, fmt.Errorf("source wallet %w", ErrWalletFrozen)
	}
	if lockedDst.Status != "active" {
		return nil, fmt.Errorf("destination wallet %w", ErrWalletFrozen)
	}

	// Available balance check on source wallet
	if lockedSrc.AvailableBalance.LessThan(req.Amount) {
		return nil, fmt.Errorf("source wallet %w: available %s < transfer %s", ErrInsufficientFunds, lockedSrc.AvailableBalance.StringFixed(MoneyScale), req.Amount.StringFixed(MoneyScale))
	}

	// Idempotency
	if req.IdempotencyKey != "" {
		existing, err := s.repo.GetTransactionByIdempotency(ctx, tx, &lockedSrc.OrganizationID, "transfer", req.IdempotencyKey)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return existing, nil
		}
	}

	srcAcc, err := s.repo.GetAccountByWalletID(ctx, tx, lockedSrc.ID)
	if err != nil {
		return nil, err
	}
	dstAcc, err := s.repo.GetAccountByWalletID(ctx, tx, lockedDst.ID)
	if err != nil {
		return nil, err
	}

	desc := fmt.Sprintf("Transfer from %s to %s of %s %s", srcOrg.Name, dstOrg.Name, req.Amount.StringFixed(MoneyScale), srcWallet.Currency)
	idemp := req.IdempotencyKey
	var idempPtr *string
	if idemp != "" {
		idempPtr = &idemp
	}
	reason := req.Reason
	var reasonPtr *string
	if reason != "" {
		reasonPtr = &reason
	}

	lt, err := s.repo.CreateTransaction(ctx, tx, LedgerTransaction{
		Type:           "transfer",
		IdempotencyKey: idempPtr,
		Description:    desc,
		Reason:         reasonPtr,
		TenantOrgID:    &lockedSrc.OrganizationID,
		ActorUserID:    &tc.ActorUserID,
		ActorOrgID:     &tc.ActorOrgID,
	})
	if err != nil {
		return nil, err
	}

	// Double-entry: Debit Source Wallet Account, Credit Destination Wallet Account
	entries := []LedgerEntry{
		{
			TransactionID: lt.ID,
			AccountID:     srcAcc.ID,
			Direction:     "debit",
			Amount:        req.Amount,
			Currency:      srcWallet.Currency,
		},
		{
			TransactionID: lt.ID,
			AccountID:     dstAcc.ID,
			Direction:     "credit",
			Amount:        req.Amount,
			Currency:      dstWallet.Currency,
		},
	}
	if err := s.repo.CreateEntries(ctx, tx, entries); err != nil {
		return nil, err
	}

	// Update cached balances
	newSrcBal := lockedSrc.CachedBalance.Sub(req.Amount)
	newDstBal := lockedDst.CachedBalance.Add(req.Amount)

	if err := s.repo.UpdateWalletCachedBalance(ctx, tx, lockedSrc.ID, newSrcBal, lockedSrc.Version); err != nil {
		return nil, err
	}
	if err := s.repo.UpdateWalletCachedBalance(ctx, tx, lockedDst.ID, newDstBal, lockedDst.Version); err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		TargetOrgID: &lockedDst.OrganizationID,
		Action:      "wallet.transfer",
		Module:      "wallet",
		EntityType:  "wallet",
		EntityID:    lockedDst.ID.String(),
		Reason:      req.Reason,
		IPAddress:   ip,
		UserAgent:   userAgent,
		AfterValue: audit.RedactValue(map[string]any{
			"source_wallet_id": lockedSrc.ID.String(),
			"dest_wallet_id":   lockedDst.ID.String(),
			"amount":           req.Amount.String(),
			"transaction_id":   lt.ID.String(),
		}),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transfer: %w", err)
	}

	lt.Entries = entries
	return lt, nil
}

// -------------------------------------------------------------------------
// Reversals
// -------------------------------------------------------------------------

func (s *Service) ReverseTransaction(ctx context.Context, tc *tenant.Context, origTxID uuid.UUID, reason, ip, userAgent string) (*LedgerTransaction, error) {
	if tc == nil || (!tc.IsPlatformAdmin && !tc.IsPlatformOrg()) {
		return nil, ErrPermissionDenied
	}
	if !tc.HasPermission("wallet.adjustment.create") {
		return nil, ErrPermissionDenied
	}
	if strings.TrimSpace(reason) == "" {
		return nil, ErrReasonRequired
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	origTx, err := s.repo.GetTransactionByID(ctx, origTxID)
	if err != nil {
		return nil, err
	}
	if origTx.Type == "reversal" {
		return nil, fmt.Errorf("%w: cannot reverse a reversal transaction", ErrReversalNotAllowed)
	}

	// Check if already reversed
	var alreadyReversedCount int
	err = tx.QueryRow(ctx, "SELECT count(*) FROM ledger_transactions WHERE reversal_of_transaction_id = $1", origTx.ID).Scan(&alreadyReversedCount)
	if err != nil {
		return nil, err
	}
	if alreadyReversedCount > 0 {
		return nil, ErrTransactionAlreadyReversed
	}

	desc := fmt.Sprintf("Reversal of transaction %s (%s)", origTx.ID.String()[:8], origTx.Type)
	revTx, err := s.repo.CreateTransaction(ctx, tx, LedgerTransaction{
		Type:                    "reversal",
		ReversalOfTransactionID: &origTx.ID,
		Description:             desc,
		Reason:                  &reason,
		TenantOrgID:             origTx.TenantOrgID,
		ActorUserID:             &tc.ActorUserID,
		ActorOrgID:              &tc.ActorOrgID,
	})
	if err != nil {
		return nil, fmt.Errorf("create reversal tx: %w", err)
	}

	// Invert every original entry: debit -> credit, credit -> debit
	var revEntries []LedgerEntry
	walletNetChanges := make(map[uuid.UUID]decimal.Decimal)

	for _, e := range origTx.Entries {
		oppositeDir := "credit"
		if e.Direction == "credit" {
			oppositeDir = "debit"
		}

		revEntries = append(revEntries, LedgerEntry{
			TransactionID: revTx.ID,
			AccountID:     e.AccountID,
			Direction:     oppositeDir,
			Amount:        e.Amount,
			Currency:      e.Currency,
		})

		// Check if this account belongs to a wallet
		var walletID *uuid.UUID
		_ = tx.QueryRow(ctx, "SELECT wallet_id FROM ledger_accounts WHERE id = $1", e.AccountID).Scan(&walletID)
		if walletID != nil {
			curChange := walletNetChanges[*walletID]
			// In reversal: credit adds to wallet, debit subtracts from wallet
			if oppositeDir == "credit" {
				curChange = curChange.Add(e.Amount)
			} else {
				curChange = curChange.Sub(e.Amount)
			}
			walletNetChanges[*walletID] = curChange
		}
	}

	if err := s.repo.CreateEntries(ctx, tx, revEntries); err != nil {
		return nil, err
	}

	// Apply wallet cached balance updates and overspend checks
	for wID, netChange := range walletNetChanges {
		w, err := s.repo.GetWalletByIDForUpdate(ctx, tx, wID)
		if err != nil {
			return nil, err
		}
		newBal := w.CachedBalance.Add(netChange)

		// Overspend check if balance decreased
		if netChange.IsNegative() {
			decrease := netChange.Abs()
			if w.AvailableBalance.LessThan(decrease) {
				return nil, fmt.Errorf("%w: reversal would cause wallet %s to overspend", ErrInsufficientFunds, w.ID)
			}
		}

		if err := s.repo.UpdateWalletCachedBalance(ctx, tx, w.ID, newBal, w.Version); err != nil {
			return nil, err
		}
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		TargetOrgID: origTx.TenantOrgID,
		Action:      "ledger.transaction.reverse",
		Module:      "ledger",
		EntityType:  "ledger_transaction",
		EntityID:    revTx.ID.String(),
		Reason:      reason,
		IPAddress:   ip,
		UserAgent:   userAgent,
		AfterValue: audit.RedactValue(map[string]any{
			"original_transaction_id": origTx.ID.String(),
		}),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit reversal: %w", err)
	}

	revTx.Entries = revEntries
	return revTx, nil
}

// -------------------------------------------------------------------------
// Holds Lifecycle (Reserve, then Settle)
// -------------------------------------------------------------------------

func (s *Service) CheckAvailable(ctx context.Context, walletID uuid.UUID, amount decimal.Decimal) (bool, *Wallet, error) {
	amount = amount.Truncate(MoneyScale)
	if amount.LessThanOrEqual(decimal.Zero) {
		return false, nil, ErrNegativeAmount
	}

	w, err := s.repo.GetWalletByID(ctx, walletID)
	if err != nil {
		return false, nil, err
	}

	if w.Status != "active" {
		return false, w, nil
	}

	return w.AvailableBalance.GreaterThanOrEqual(amount), w, nil
}

func (s *Service) PlaceHold(
	ctx context.Context,
	walletID uuid.UUID,
	amount decimal.Decimal,
	currency string,
	refType *string,
	refID *uuid.UUID,
	idempKey *string,
	expiry time.Time,
) (*Hold, error) {
	return s.PlaceHoldTx(ctx, nil, walletID, amount, currency, refType, refID, idempKey, expiry)
}

// PlaceHoldTx places a balance hold using an optional caller-provided pgx.Tx.
// If dbTx is nil, a new transaction is begun and committed internally.
func (s *Service) PlaceHoldTx(
	ctx context.Context,
	dbTx pgx.Tx,
	walletID uuid.UUID,
	amount decimal.Decimal,
	currency string,
	refType *string,
	refID *uuid.UUID,
	idempKey *string,
	expiry time.Time,
) (*Hold, error) {
	amount = amount.Truncate(MoneyScale)
	if amount.LessThanOrEqual(decimal.Zero) {
		return nil, ErrNegativeAmount
	}

	tx := dbTx
	var err error
	if tx == nil {
		tx, err = s.pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback(ctx)
	}

	w, err := s.repo.GetWalletByIDForUpdate(ctx, tx, walletID)
	if err != nil {
		return nil, err
	}
	if w.Status != "active" {
		if w.Status == "frozen" {
			return nil, ErrWalletFrozen
		}
		return nil, ErrWalletClosed
	}

	if currency != "" && w.Currency != currency {
		return nil, ErrCurrencyMismatch
	}

	// Idempotency
	if idempKey != nil && *idempKey != "" {
		existing, err := s.repo.GetHoldByIdempotency(ctx, tx, walletID, *idempKey)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return existing, nil
		}
	}

	// Available balance check (takes active holds and credit limit into account)
	if w.AvailableBalance.LessThan(amount) {
		return nil, ErrInsufficientFunds
	}

	if expiry.IsZero() {
		expiry = time.Now().Add(15 * time.Minute)
	}

	hold, err := s.repo.CreateHold(ctx, tx, Hold{
		WalletID:       walletID,
		Amount:         amount,
		Currency:       w.Currency,
		ExpiresAt:      expiry,
		ReferenceType:  refType,
		ReferenceID:    refID,
		IdempotencyKey: idempKey,
	})
	if err != nil {
		return nil, err
	}

	if dbTx == nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit hold: %w", err)
		}
	}
	return hold, nil
}

// BatchHoldRequest describes one item in a single-wallet batch hold placement.
type BatchHoldRequest struct {
	Amount         decimal.Decimal
	Expiry         time.Time
	ReferenceType  *string
	ReferenceID    *uuid.UUID
	IdempotencyKey *string
}

// PlaceHoldsBatchTx validates and creates holds for multiple items on a single wallet in one
// FOR UPDATE lock and one batched SQL roundtrip inside caller tx dbTx.
// Returns per-index hold pointer and per-index validation/funds error alongside any fatal error.
func (s *Service) PlaceHoldsBatchTx(
	ctx context.Context,
	dbTx pgx.Tx,
	walletID uuid.UUID,
	currency string,
	reqs []BatchHoldRequest,
) ([]*Hold, []error, error) {
	if len(reqs) == 0 {
		return nil, nil, nil
	}
	tx := dbTx
	var err error
	if tx == nil {
		tx, err = s.pool.Begin(ctx)
		if err != nil {
			return nil, nil, err
		}
		defer tx.Rollback(ctx)
	}

	w, err := s.repo.GetWalletByIDForUpdate(ctx, tx, walletID)
	if err != nil {
		return nil, nil, err
	}
	if w.Status != "active" {
		if w.Status == "frozen" {
			return nil, nil, ErrWalletFrozen
		}
		return nil, nil, ErrWalletClosed
	}
	if currency != "" && w.Currency != currency {
		return nil, nil, ErrCurrencyMismatch
	}

	remaining := w.AvailableBalance
	now := time.Now()
	holds := make([]*Hold, len(reqs))
	itemErrs := make([]error, len(reqs))

	var toCreate []Hold
	var createIdx []int
	for i, req := range reqs {
		amt := req.Amount.Truncate(MoneyScale)
		if amt.LessThanOrEqual(decimal.Zero) {
			itemErrs[i] = ErrNegativeAmount
			continue
		}
		if remaining.LessThan(amt) {
			itemErrs[i] = ErrInsufficientFunds
			continue
		}
		remaining = remaining.Sub(amt)
		exp := req.Expiry
		if exp.IsZero() {
			exp = now.Add(15 * time.Minute)
		}
		toCreate = append(toCreate, Hold{
			WalletID:       walletID,
			Amount:         amt,
			Currency:       w.Currency,
			ExpiresAt:      exp,
			ReferenceType:  req.ReferenceType,
			ReferenceID:    req.ReferenceID,
			IdempotencyKey: req.IdempotencyKey,
		})
		createIdx = append(createIdx, i)
	}

	if len(toCreate) > 0 {
		created, err := s.repo.CreateHoldsBatch(ctx, tx, toCreate)
		if err != nil {
			return nil, nil, err
		}
		for j, origIdx := range createIdx {
			h := created[j]
			holds[origIdx] = &h
		}
	}

	if dbTx == nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, nil, fmt.Errorf("commit batch holds: %w", err)
		}
	}
	return holds, itemErrs, nil
}

func (s *Service) Capture(
	ctx context.Context,
	holdID uuid.UUID,
	captureAmount decimal.Decimal,
	idempKey *string,
) (*LedgerTransaction, error) {
	return s.CaptureTx(ctx, nil, holdID, captureAmount, idempKey)
}

// CaptureTx captures an active hold (full or partial) using an optional caller-provided pgx.Tx.
// If dbTx is nil, a new transaction is begun and committed internally.
// A capture replay with the same idempotency key returns the original LedgerTransaction.
// A replay with a different amount is rejected with ErrIdempotencyConflict.
func (s *Service) CaptureTx(
	ctx context.Context,
	dbTx pgx.Tx,
	holdID uuid.UUID,
	captureAmount decimal.Decimal,
	idempKey *string,
) (*LedgerTransaction, error) {
	captureAmount = captureAmount.Truncate(MoneyScale)
	if captureAmount.LessThanOrEqual(decimal.Zero) {
		return nil, ErrNegativeAmount
	}

	tx := dbTx
	var err error
	if tx == nil {
		tx, err = s.pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback(ctx)
	}

	hold, err := s.repo.GetHoldByIDForUpdate(ctx, tx, holdID)
	if err != nil {
		return nil, err
	}

	w, err := s.repo.GetWalletByIDForUpdate(ctx, tx, hold.WalletID)
	if err != nil {
		return nil, err
	}

	// Idempotency check before status validation: replay with same key returns original tx, differing amount rejected
	if idempKey != nil && *idempKey != "" {
		existing, err := s.repo.GetTransactionByIdempotency(ctx, tx, &w.OrganizationID, "hold_capture", *idempKey)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			var origAmount decimal.Decimal
			if len(existing.Entries) > 0 {
				origAmount = existing.Entries[0].Amount
			} else {
				origAmount = hold.CapturedAmount
			}
			if !origAmount.Equal(captureAmount) {
				return nil, fmt.Errorf("%w: original capture amount %s, replayed with %s", ErrIdempotencyConflict, origAmount.StringFixed(MoneyScale), captureAmount.StringFixed(MoneyScale))
			}
			return existing, nil
		}
	}

	if hold.Status != "active" {
		return nil, fmt.Errorf("%w: current status %s", ErrHoldNotActive, hold.Status)
	}
	if hold.ExpiresAt.Before(time.Now()) {
		_ = s.repo.UpdateHoldStatus(ctx, tx, hold.ID, "expired", decimal.Zero, hold.Version)
		return nil, ErrHoldExpired
	}
	if captureAmount.GreaterThan(hold.Amount) {
		return nil, ErrHoldCaptureExceeded
	}

	// Post charge
	revAccCode := "PLATFORM_REVENUE"
	if w.Currency == "EUR" {
		revAccCode = "PLATFORM_REVENUE_EUR"
	}
	revAcc, err := s.repo.GetSystemAccount(ctx, tx, revAccCode)
	if err != nil {
		return nil, err
	}
	walletAcc, err := s.repo.GetAccountByWalletID(ctx, tx, w.ID)
	if err != nil {
		return nil, err
	}

	desc := fmt.Sprintf("Hold capture of %s %s", captureAmount.StringFixed(MoneyScale), w.Currency)
	entries := []LedgerEntry{
		{
			AccountID: walletAcc.ID,
			Direction: "debit",
			Amount:    captureAmount,
			Currency:  w.Currency,
		},
		{
			AccountID: revAcc.ID,
			Direction: "credit",
			Amount:    captureAmount,
			Currency:  w.Currency,
		},
	}

	newBal := w.CachedBalance.Sub(captureAmount)
	lt, err := s.repo.FinalizeHoldCapture(ctx, tx, LedgerTransaction{
		Type:           "hold_capture",
		ReferenceType:  hold.ReferenceType,
		ReferenceID:    hold.ReferenceID,
		IdempotencyKey: idempKey,
		Description:    desc,
		TenantOrgID:    &w.OrganizationID,
	}, entries, w.ID, newBal, w.Version, hold.ID, captureAmount, hold.Version)
	if err != nil {
		return nil, err
	}

	if dbTx == nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit capture: %w", err)
		}
	}

	return lt, nil
}

func (s *Service) Release(ctx context.Context, holdID uuid.UUID) error {
	return s.ReleaseTx(ctx, nil, holdID)
}

// ReleaseTx releases an active hold using an optional caller-provided pgx.Tx.
// If dbTx is nil, a new transaction is begun and committed internally.
// Calling ReleaseTx on an already-released hold returns nil (idempotent release).
func (s *Service) ReleaseTx(ctx context.Context, dbTx pgx.Tx, holdID uuid.UUID) error {
	tx := dbTx
	var err error
	if tx == nil {
		tx, err = s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
	}

	hold, err := s.repo.GetHoldByIDForUpdate(ctx, tx, holdID)
	if err != nil {
		return err
	}
	if hold.Status == "released" {
		return nil
	}
	if hold.Status != "active" {
		return fmt.Errorf("%w: status is %s", ErrHoldNotActive, hold.Status)
	}

	if err := s.repo.UpdateHoldStatus(ctx, tx, hold.ID, "released", decimal.Zero, hold.Version); err != nil {
		return err
	}

	if dbTx == nil {
		return tx.Commit(ctx)
	}
	return nil
}

// RefundCaptureTx refunds a previously captured hold (used when billing_mode = on_provider_accept
// with failed_after_accept_policy = refund). It posts a balanced 'refund' transaction (debit
// revenue, credit wallet), updates cached_balance, and marks the hold status as 'refunded'.
// Calling RefundCaptureTx again for an already-refunded hold or matching idempotency key is idempotent.
func (s *Service) RefundCaptureTx(
	ctx context.Context,
	dbTx pgx.Tx,
	holdID uuid.UUID,
	refundAmount decimal.Decimal,
	idempKey *string,
	reason string,
) (*LedgerTransaction, error) {
	refundAmount = refundAmount.Truncate(MoneyScale)
	if refundAmount.LessThanOrEqual(decimal.Zero) {
		return nil, ErrNegativeAmount
	}

	tx := dbTx
	var err error
	if tx == nil {
		tx, err = s.pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback(ctx)
	}

	hold, err := s.repo.GetHoldByIDForUpdate(ctx, tx, holdID)
	if err != nil {
		return nil, err
	}
	w, err := s.repo.GetWalletByIDForUpdate(ctx, tx, hold.WalletID)
	if err != nil {
		return nil, err
	}

	if idempKey != nil && *idempKey != "" {
		existing, err := s.repo.GetTransactionByIdempotency(ctx, tx, &w.OrganizationID, "refund", *idempKey)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return existing, nil
		}
	}

	if hold.Status == "refunded" {
		return nil, nil
	}
	if hold.Status != "captured" {
		return nil, fmt.Errorf("%w: cannot refund hold with status %s (must be captured)", ErrHoldNotActive, hold.Status)
	}
	if refundAmount.GreaterThan(hold.CapturedAmount) {
		return nil, fmt.Errorf("%w: refund %s exceeds captured %s", ErrHoldCaptureExceeded, refundAmount.StringFixed(MoneyScale), hold.CapturedAmount.StringFixed(MoneyScale))
	}

	revAccCode := "PLATFORM_REVENUE"
	if w.Currency == "EUR" {
		revAccCode = "PLATFORM_REVENUE_EUR"
	}
	revAcc, err := s.repo.GetSystemAccount(ctx, tx, revAccCode)
	if err != nil {
		return nil, err
	}
	walletAcc, err := s.repo.GetAccountByWalletID(ctx, tx, w.ID)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(reason) == "" {
		reason = fmt.Sprintf("Refund captured hold of %s %s", refundAmount.StringFixed(MoneyScale), w.Currency)
	}
	lt, err := s.repo.CreateTransaction(ctx, tx, LedgerTransaction{
		Type:           "refund",
		ReferenceType:  hold.ReferenceType,
		ReferenceID:    hold.ReferenceID,
		IdempotencyKey: idempKey,
		Description:    reason,
		Reason:         &reason,
		TenantOrgID:    &w.OrganizationID,
	})
	if err != nil {
		return nil, err
	}

	entries := []LedgerEntry{
		{
			TransactionID: lt.ID,
			AccountID:     revAcc.ID,
			Direction:     "debit",
			Amount:        refundAmount,
			Currency:      w.Currency,
		},
		{
			TransactionID: lt.ID,
			AccountID:     walletAcc.ID,
			Direction:     "credit",
			Amount:        refundAmount,
			Currency:      w.Currency,
		},
	}
	if err := s.repo.CreateEntries(ctx, tx, entries); err != nil {
		return nil, err
	}

	newBal := w.CachedBalance.Add(refundAmount)
	if err := s.repo.UpdateWalletCachedBalance(ctx, tx, w.ID, newBal, w.Version); err != nil {
		return nil, err
	}

	if err := s.repo.UpdateHoldStatus(ctx, tx, hold.ID, "refunded", decimal.Zero, hold.Version); err != nil {
		return nil, err
	}

	if dbTx == nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit refund capture: %w", err)
		}
	}

	lt.Entries = entries
	return lt, nil
}

func (s *Service) ExpireOldHolds(ctx context.Context) (int64, error) {
	return s.repo.ExpireActiveHolds(ctx, time.Now())
}

func (s *Service) ListHolds(ctx context.Context, tc *tenant.Context, walletID uuid.UUID, status string) ([]Hold, error) {
	if _, err := s.authorizeWalletAccess(ctx, tc, walletID, "wallet.wallet.view"); err != nil {
		return nil, err
	}
	return s.repo.ListHoldsByWallet(ctx, walletID, status)
}

// PostCharge is the internal service API for Phase 8 direct usage charges.
func (s *Service) PostCharge(
	ctx context.Context,
	walletID uuid.UUID,
	amount decimal.Decimal,
	currency, description string,
	refType *string,
	refID *uuid.UUID,
	idempKey *string,
) (*LedgerTransaction, error) {
	return s.PostChargeTx(ctx, nil, walletID, amount, currency, description, refType, refID, idempKey)
}

// PostChargeTx posts a direct usage charge using an optional caller-provided pgx.Tx.
// If dbTx is nil, a new transaction is begun and committed internally.
func (s *Service) PostChargeTx(
	ctx context.Context,
	dbTx pgx.Tx,
	walletID uuid.UUID,
	amount decimal.Decimal,
	currency, description string,
	refType *string,
	refID *uuid.UUID,
	idempKey *string,
) (*LedgerTransaction, error) {
	amount = amount.Truncate(MoneyScale)
	if amount.LessThanOrEqual(decimal.Zero) {
		return nil, ErrNegativeAmount
	}

	tx := dbTx
	var err error
	if tx == nil {
		tx, err = s.pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback(ctx)
	}

	w, err := s.repo.GetWalletByIDForUpdate(ctx, tx, walletID)
	if err != nil {
		return nil, err
	}
	if w.Status != "active" {
		if w.Status == "frozen" {
			return nil, ErrWalletFrozen
		}
		return nil, ErrWalletClosed
	}

	if currency != "" && w.Currency != currency {
		return nil, ErrCurrencyMismatch
	}

	if idempKey != nil && *idempKey != "" {
		existing, err := s.repo.GetTransactionByIdempotency(ctx, tx, &w.OrganizationID, "charge", *idempKey)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return existing, nil
		}
	}

	if w.AvailableBalance.LessThan(amount) {
		return nil, ErrInsufficientFunds
	}

	revAccCode := "PLATFORM_REVENUE"
	if w.Currency == "EUR" {
		revAccCode = "PLATFORM_REVENUE_EUR"
	}
	revAcc, err := s.repo.GetSystemAccount(ctx, tx, revAccCode)
	if err != nil {
		return nil, err
	}
	walletAcc, err := s.repo.GetAccountByWalletID(ctx, tx, w.ID)
	if err != nil {
		return nil, err
	}

	if description == "" {
		description = fmt.Sprintf("Service charge of %s %s", amount.StringFixed(MoneyScale), w.Currency)
	}

	lt, err := s.repo.CreateTransaction(ctx, tx, LedgerTransaction{
		Type:           "charge",
		ReferenceType:  refType,
		ReferenceID:    refID,
		IdempotencyKey: idempKey,
		Description:    description,
		TenantOrgID:    &w.OrganizationID,
	})
	if err != nil {
		return nil, err
	}

	entries := []LedgerEntry{
		{
			TransactionID: lt.ID,
			AccountID:     walletAcc.ID,
			Direction:     "debit",
			Amount:        amount,
			Currency:      w.Currency,
		},
		{
			TransactionID: lt.ID,
			AccountID:     revAcc.ID,
			Direction:     "credit",
			Amount:        amount,
			Currency:      w.Currency,
		},
	}
	if err := s.repo.CreateEntries(ctx, tx, entries); err != nil {
		return nil, err
	}

	newBal := w.CachedBalance.Sub(amount)
	if err := s.repo.UpdateWalletCachedBalance(ctx, tx, w.ID, newBal, w.Version); err != nil {
		return nil, err
	}

	if dbTx == nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit charge: %w", err)
		}
	}

	lt.Entries = entries
	return lt, nil
}

// -------------------------------------------------------------------------
// Statements & Exports
// -------------------------------------------------------------------------

func (s *Service) GetStatement(ctx context.Context, tc *tenant.Context, filter StatementFilter) ([]StatementItem, int, error) {
	if _, err := s.authorizeWalletAccess(ctx, tc, filter.WalletID, "wallet.wallet.view"); err != nil {
		return nil, 0, err
	}
	return s.repo.GetWalletStatement(ctx, filter)
}

func (s *Service) ExportStatementCSV(ctx context.Context, tc *tenant.Context, filter StatementFilter) ([]byte, error) {
	filter.Page = 1
	filter.PageSize = 1000
	items, _, err := s.GetStatement(ctx, tc, filter)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)

	_ = w.Write([]string{"date", "transaction_type", "description", "debit", "credit", "net_change", "running_balance", "currency"})
	for _, it := range items {
		_ = w.Write([]string{
			it.CreatedAt.Format(time.RFC3339),
			sanitizeCSVCell(it.TransactionType),
			sanitizeCSVCell(it.Description),
			it.Debit.StringFixed(MoneyScale),
			it.Credit.StringFixed(MoneyScale),
			it.NetChange.StringFixed(MoneyScale),
			it.RunningBalance.StringFixed(MoneyScale),
			sanitizeCSVCell(it.Currency),
		})
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

// -------------------------------------------------------------------------
// Transactions Explorer
// -------------------------------------------------------------------------

func (s *Service) ListTransactions(ctx context.Context, tc *tenant.Context, filter TransactionFilter) ([]LedgerTransaction, int, error) {
	if tc == nil || !tc.HasPermission("ledger.view") {
		return nil, 0, ErrPermissionDenied
	}
	return s.repo.ListTransactions(ctx, tc, filter)
}

func (s *Service) GetTransaction(ctx context.Context, tc *tenant.Context, id uuid.UUID) (*LedgerTransaction, error) {
	if tc == nil || !tc.HasPermission("ledger.view") {
		return nil, ErrPermissionDenied
	}
	tx, err := s.repo.GetTransactionByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !tc.IsPlatformAdmin && !tc.IsPlatformOrg() {
		// Non-platform actor must belong to same org tree
		if tx.TenantOrgID == nil {
			return nil, ErrCrossTenantAccess
		}
		targetOrg, err := s.orgRepo.GetByID(ctx, *tx.TenantOrgID)
		if err != nil || !tc.CanAccessOrg(targetOrg.Path) {
			return nil, ErrCrossTenantAccess
		}
	}
	return tx, nil
}

func (s *Service) ExportTransactionsCSV(ctx context.Context, tc *tenant.Context, filter TransactionFilter) ([]byte, error) {
	filter.Page = 1
	filter.PageSize = 1000
	txs, _, err := s.ListTransactions(ctx, tc, filter)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)

	_ = w.Write([]string{"id", "date", "type", "description", "reason", "actor_user_id"})
	for _, t := range txs {
		reason := ""
		if t.Reason != nil {
			reason = *t.Reason
		}
		actor := ""
		if t.ActorUserID != nil {
			actor = t.ActorUserID.String()
		}
		_ = w.Write([]string{
			t.ID.String(),
			t.CreatedAt.Format(time.RFC3339),
			sanitizeCSVCell(t.Type),
			sanitizeCSVCell(t.Description),
			sanitizeCSVCell(reason),
			actor,
		})
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

// -------------------------------------------------------------------------
// Reconciliation Engine
// -------------------------------------------------------------------------

func (s *Service) ReconcileBalances(ctx context.Context, tc *tenant.Context, repair bool) ([]ReconciliationReport, error) {
	if tc == nil || (!tc.IsPlatformAdmin && !tc.IsPlatformOrg()) {
		return nil, ErrPermissionDenied
	}
	if !tc.HasPermission("ledger.reconcile") {
		return nil, ErrPermissionDenied
	}

	wallets, err := s.repo.ListAllWalletsForReconciliation(ctx)
	if err != nil {
		return nil, err
	}

	var reports []ReconciliationReport
	now := time.Now()

	for _, w := range wallets {
		computed, count, err := s.repo.ComputeWalletBalanceFromEntries(ctx, w.ID)
		if err != nil {
			return nil, err
		}

		diff := w.CachedBalance.Sub(computed)
		status := "matched"
		if !diff.IsZero() {
			status = "mismatch"
		}

		if repair && !diff.IsZero() && (tc.IsPlatformAdmin || tc.IsPlatformOrg()) {
			// Repair corrupted cached balance; leaves ledger entries untouched
			if err := s.repo.UpdateWalletCachedBalance(ctx, nil, w.ID, computed, w.Version); err != nil {
				return nil, fmt.Errorf("repair wallet %s: %w", w.ID, err)
			}
			_ = s.auditSvc.Record(ctx, nil, audit.Entry{
				ActorUserID: &tc.ActorUserID,
				ActorOrgID:  &tc.ActorOrgID,
				TargetOrgID: &w.OrganizationID,
				Action:      "ledger.wallet.reconcile_repair",
				Module:      "ledger",
				EntityType:  "wallet",
				EntityID:    w.ID.String(),
				Reason:      fmt.Sprintf("Repaired corrupted cached balance from %s to computed %s", w.CachedBalance.StringFixed(MoneyScale), computed.StringFixed(MoneyScale)),
				BeforeValue: audit.RedactValue(map[string]any{
					"cached_balance": w.CachedBalance.StringFixed(MoneyScale),
				}),
				AfterValue: audit.RedactValue(map[string]any{
					"repaired_balance": computed.StringFixed(MoneyScale),
				}),
			})
			diff = decimal.Zero
			status = "repaired"
			w.CachedBalance = computed
		}

		reports = append(reports, ReconciliationReport{
			WalletID:         w.ID,
			OrganizationID:   w.OrganizationID,
			OrganizationName: w.OrganizationName,
			Currency:         w.Currency,
			CachedBalance:    w.CachedBalance,
			ComputedBalance:  computed,
			Difference:       diff,
			Status:           status,
			EntriesCount:     count,
			CheckedAt:        now,
		})
	}

	return reports, nil
}

// -------------------------------------------------------------------------
// Approval Appliers
// -------------------------------------------------------------------------

type topupApplier struct {
	s *Service
}

func (a *topupApplier) ApplyChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	var payload struct {
		WalletID       uuid.UUID       `json:"wallet_id"`
		Amount         decimal.Decimal `json:"amount"`
		Reason         string          `json:"reason"`
		Reference      string          `json:"reference"`
		IdempotencyKey string          `json:"idempotency_key"`
		ActorOrgID     uuid.UUID       `json:"actor_org_id"`
		IPAddress      string          `json:"ip_address"`
		UserAgent      string          `json:"user_agent"`
	}
	if err := json.Unmarshal(cr.ProposedData, &payload); err != nil {
		return fmt.Errorf("unmarshal topup change: %w", err)
	}

	_, err := a.s.executeTopup(ctx, tx, cr.MakerUserID, payload.ActorOrgID, payload.WalletID, WalletTopupRequest{
		Amount:         payload.Amount,
		Reason:         payload.Reason,
		Reference:      payload.Reference,
		IdempotencyKey: payload.IdempotencyKey,
	}, payload.IPAddress, payload.UserAgent)
	return err
}

type adjustmentApplier struct {
	s *Service
}

func (a *adjustmentApplier) ApplyChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	var payload struct {
		WalletID       uuid.UUID       `json:"wallet_id"`
		Amount         decimal.Decimal `json:"amount"`
		Direction      string          `json:"direction"`
		Reason         string          `json:"reason"`
		Reference      string          `json:"reference"`
		IdempotencyKey string          `json:"idempotency_key"`
		ActorOrgID     uuid.UUID       `json:"actor_org_id"`
		IPAddress      string          `json:"ip_address"`
		UserAgent      string          `json:"user_agent"`
	}
	if err := json.Unmarshal(cr.ProposedData, &payload); err != nil {
		return fmt.Errorf("unmarshal adjustment change: %w", err)
	}

	_, err := a.s.executeAdjustment(ctx, tx, cr.MakerUserID, payload.ActorOrgID, payload.WalletID, WalletAdjustmentRequest{
		Amount:         payload.Amount,
		Direction:      payload.Direction,
		Reason:         payload.Reason,
		Reference:      payload.Reference,
		IdempotencyKey: payload.IdempotencyKey,
	}, payload.IPAddress, payload.UserAgent)
	return err
}

type unfreezeApplier struct {
	s *Service
}

func (a *unfreezeApplier) ApplyChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	var payload struct {
		WalletID   uuid.UUID `json:"wallet_id"`
		Reason     string    `json:"reason"`
		ActorOrgID uuid.UUID `json:"actor_org_id"`
		IPAddress  string    `json:"ip_address"`
		UserAgent  string    `json:"user_agent"`
	}
	if err := json.Unmarshal(cr.ProposedData, &payload); err != nil {
		return fmt.Errorf("unmarshal unfreeze change: %w", err)
	}

	return a.s.executeUnfreeze(ctx, tx, cr.MakerUserID, payload.ActorOrgID, payload.WalletID, payload.Reason, payload.IPAddress, payload.UserAgent)
}

func sanitizeCSVCell(s string) string {
	if s == "" {
		return ""
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}
