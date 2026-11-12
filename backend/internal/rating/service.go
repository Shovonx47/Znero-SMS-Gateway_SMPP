package rating

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/number"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

type applierFunc func(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error

func (f applierFunc) ApplyChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	return f(ctx, tx, cr)
}

type WriteResult struct {
	Card           *RateCard               `json:"card,omitempty"`
	Rate           *Rate                   `json:"rate,omitempty"`
	CustomerAssign *CustomerRateAssignment `json:"customer_assignment,omitempty"`
	ResellerAssign *ResellerCostAssignment `json:"reseller_assignment,omitempty"`
	PendingChange  *approval.ChangeRequest `json:"pending_change,omitempty"`
}

type Service struct {
	repo        Repository
	auditSvc    *audit.Service
	approvalSvc *approval.Service
	numberSvc   *number.Service
	engine      Engine
}

func NewService(
	repo Repository,
	auditSvc *audit.Service,
	approvalSvc *approval.Service,
	numberSvc *number.Service,
	engine Engine,
) *Service {
	if engine == nil {
		engine = NewInMemoryEngine(repo)
	}
	svc := &Service{
		repo:        repo,
		auditSvc:    auditSvc,
		approvalSvc: approvalSvc,
		numberSvc:   numberSvc,
		engine:      engine,
	}

	if approvalSvc != nil {
		svc.RegisterWithApproval(approvalSvc)
	}

	// Warm up engine in background
	_ = engine.Reload(context.Background())

	return svc
}

func (s *Service) Engine() Engine {
	return s.engine
}

func (s *Service) RegisterWithApproval(approvalSvc *approval.Service) {
	approvalSvc.RegisterApplier("rate_card", applierFunc(s.applyRateCardChange))
	approvalSvc.RegisterApplier("rate", applierFunc(s.applyRateChange))
	approvalSvc.RegisterApplier("rate_assignment", applierFunc(s.applyAssignmentChange))

	approvalSvc.RegisterApprovePermission("rate_card", "rate.sellcard.approve,rate.buycard.approve")
	approvalSvc.RegisterApprovePermission("rate", "rate.sellrate.approve,rate.buyrate.approve")
	approvalSvc.RegisterApprovePermission("rate_assignment", "rate.assignment.approve")
}

func hasOverride(tc *tenant.Context, overridePerm string) bool {
	if tc == nil {
		return false
	}
	if !tc.IsPlatformOrg() {
		// Non-platform organizations (e.g. resellers managing downstream rates) write directly
		// within their tenant scope without going through the platform maker-checker queue.
		return true
	}
	return tc.HasPermission(overridePerm)
}

func (s *Service) reloadEngineAsync() {
	go func() {
		_ = s.engine.Reload(context.Background())
	}()
}

func (s *Service) ReloadEngine(ctx context.Context) error {
	return s.engine.Reload(ctx)
}

// -----------------------------------------------------------------------------
// RATE CARDS
// -----------------------------------------------------------------------------

func (s *Service) CreateCard(ctx context.Context, tc *tenant.Context, card *RateCard, reason string) (*WriteResult, error) {
	if tc == nil {
		return nil, ErrPermissionDenied
	}

	// 1. Kind & Tenancy checks
	if card.Kind == "provider_buy" {
		if !tc.IsPlatformOrg() || !tc.HasPermission("rate.buycard.create") {
			return nil, ErrPlatformOnlyResource
		}
		if card.ProviderID == nil {
			return nil, errors.New("provider_id is required for buy rate cards")
		}
		card.OwnerOrgID = tc.ActorOrgID
	} else if card.Kind == "customer_sell" {
		if tc.IsPlatformOrg() {
			if !tc.HasPermission("rate.sellcard.create") {
				return nil, ErrPermissionDenied
			}
			if card.OwnerOrgID == uuid.Nil {
				card.OwnerOrgID = tc.ActorOrgID
			}
		} else {
			// Reseller creating sell card for customer
			if !tc.HasPermission("rate.sellcard.create") {
				return nil, ErrPermissionDenied
			}
			card.OwnerOrgID = tc.ActorOrgID
			card.ProviderID = nil // Reseller cannot link provider
		}
	} else {
		return nil, errors.New("invalid rate card kind: must be provider_buy or customer_sell")
	}

	// 2. Currency validation
	validCurrency, err := s.repo.ValidateCurrencyExists(ctx, card.CurrencyCode)
	if err != nil || !validCurrency {
		return nil, ErrInvalidCurrency
	}

	overridePerm := "rate.sellcard.override"
	if card.Kind == "provider_buy" {
		overridePerm = "rate.buycard.override"
	}

	// 3. Maker-checker check
	if !hasOverride(tc, overridePerm) && s.approvalSvc != nil {
		cr, err := s.submitChange(ctx, tc, "rate_card", nil, nil, "create", nil, card, reason)
		if err != nil {
			return nil, err
		}
		return &WriteResult{PendingChange: cr}, nil
	}

	// 4. Direct write
	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	card.CreatedBy = &tc.ActorUserID
	card.UpdatedBy = &tc.ActorUserID
	created, err := s.repo.CreateCard(ctx, tx, card, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		Action:      fmt.Sprintf("rate_card.%s.create", card.Kind),
		Module:      "rate",
		EntityType:  "rate_card",
		EntityID:    created.ID.String(),
		AfterValue:  audit.RedactValue(created),
		Reason:      reason,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	s.reloadEngineAsync()
	return &WriteResult{Card: created}, nil
}

func (s *Service) UpdateCard(ctx context.Context, tc *tenant.Context, card *RateCard, reason string) (*WriteResult, error) {
	if tc == nil {
		return nil, ErrPermissionDenied
	}

	current, err := s.repo.GetCardByID(ctx, card.ID)
	if err != nil {
		return nil, err
	}

	if current.Kind == "provider_buy" && (!tc.IsPlatformOrg() || !tc.HasPermission("rate.buycard.update")) {
		return nil, ErrPlatformOnlyResource
	}
	if current.Kind == "customer_sell" {
		if !tc.IsPlatformOrg() {
			if current.OwnerOrgID != tc.ActorOrgID || !tc.HasPermission("rate.sellcard.update") {
				return nil, ErrCrossTenantAccess
			}
		} else if !tc.HasPermission("rate.sellcard.update") {
			return nil, ErrPermissionDenied
		}
	}

	overridePerm := "rate.sellcard.override"
	if current.Kind == "provider_buy" {
		overridePerm = "rate.buycard.override"
	}

	if !hasOverride(tc, overridePerm) && s.approvalSvc != nil {
		v := current.Version
		cr, err := s.submitChange(ctx, tc, "rate_card", &card.ID, &v, "update", current, card, reason)
		if err != nil {
			return nil, err
		}
		return &WriteResult{PendingChange: cr}, nil
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	card.UpdatedBy = &tc.ActorUserID
	updated, err := s.repo.UpdateCard(ctx, tx, card, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		Action:      fmt.Sprintf("rate_card.%s.update", current.Kind),
		Module:      "rate",
		EntityType:  "rate_card",
		EntityID:    updated.ID.String(),
		BeforeValue: audit.RedactValue(current),
		AfterValue:  audit.RedactValue(updated),
		Reason:      reason,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	s.reloadEngineAsync()
	return &WriteResult{Card: updated}, nil
}

func (s *Service) ArchiveCard(ctx context.Context, tc *tenant.Context, id uuid.UUID, reason string) (*WriteResult, error) {
	current, err := s.repo.GetCardByID(ctx, id)
	if err != nil {
		return nil, err
	}

	copyCard := *current
	copyCard.Archived = true
	copyCard.Status = "archived"
	return s.UpdateCard(ctx, tc, &copyCard, reason)
}

func (s *Service) GetCardByID(ctx context.Context, tc *tenant.Context, id uuid.UUID) (*RateCard, error) {
	card, err := s.repo.GetCardByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if card.Kind == "provider_buy" && (tc == nil || !tc.IsPlatformOrg()) {
		return nil, ErrPlatformOnlyResource
	}
	if card.Kind == "customer_sell" && tc != nil && !tc.IsPlatformOrg() {
		// Non-platform user can view if owned by their org, or if assigned to their customer/reseller org
		if card.OwnerOrgID != tc.ActorOrgID {
			// Check if assigned
			assign, _ := s.repo.GetActiveCustomerAssignmentAt(ctx, tc.ActorOrgID, time.Now().UTC())
			if assign == nil || assign.CardID != card.ID {
				resAssign, _ := s.repo.GetActiveResellerCostAssignmentAt(ctx, tc.ActorOrgID, time.Now().UTC())
				if resAssign == nil || resAssign.CardID != card.ID {
					return nil, ErrCrossTenantAccess
				}
			}
		}
	}

	return card, nil
}

func (s *Service) ListCards(ctx context.Context, tc *tenant.Context, filter RateCardListFilter) ([]RateCard, int, error) {
	if tc == nil {
		return nil, 0, ErrPermissionDenied
	}

	if !tc.IsPlatformOrg() {
		// Resellers and customers can never list buy cards
		if filter.Kind == "provider_buy" {
			return nil, 0, ErrPlatformOnlyResource
		}
		filter.Kind = "customer_sell"
		filter.OwnerOrgID = &tc.ActorOrgID
	}

	return s.repo.ListCards(ctx, filter)
}

func (s *Service) ListCardHistory(ctx context.Context, tc *tenant.Context, cardID uuid.UUID) ([]RateCardHistory, error) {
	card, err := s.GetCardByID(ctx, tc, cardID)
	if err != nil {
		return nil, err
	}
	_ = card
	return s.repo.ListCardHistory(ctx, cardID)
}

func (s *Service) RollbackCard(ctx context.Context, tc *tenant.Context, cardID uuid.UUID, targetVersion int, reason string) (*WriteResult, error) {
	current, err := s.repo.GetCardByID(ctx, cardID)
	if err != nil {
		return nil, err
	}

	hist, err := s.repo.GetCardHistoryVersion(ctx, cardID, targetVersion)
	if err != nil {
		return nil, err
	}

	var restored RateCard
	if err := json.Unmarshal(hist.Snapshot, &restored); err != nil {
		return nil, fmt.Errorf("decode snapshot: %w", err)
	}

	restored.Version = current.Version
	restored.UpdatedBy = &tc.ActorUserID

	overridePerm := "rate.sellcard.override"
	if current.Kind == "provider_buy" {
		overridePerm = "rate.buycard.override"
	}

	if !hasOverride(tc, overridePerm) && s.approvalSvc != nil {
		v := current.Version
		cr, err := s.submitChange(ctx, tc, "rate_card", &cardID, &v, "rollback", current, &restored, reason)
		if err != nil {
			return nil, err
		}
		return &WriteResult{PendingChange: cr}, nil
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	updated, err := s.repo.UpdateCard(ctx, tx, &restored, fmt.Sprintf("Rollback to version %d: %s", targetVersion, reason))
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		Action:      fmt.Sprintf("rate_card.%s.rollback", current.Kind),
		Module:      "rate",
		EntityType:  "rate_card",
		EntityID:    updated.ID.String(),
		BeforeValue: audit.RedactValue(current),
		AfterValue:  audit.RedactValue(updated),
		Reason:      fmt.Sprintf("Rollback to version %d: %s", targetVersion, reason),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	s.reloadEngineAsync()
	return &WriteResult{Card: updated}, nil
}

// -----------------------------------------------------------------------------
// RATES
// -----------------------------------------------------------------------------

func (s *Service) CreateRate(ctx context.Context, tc *tenant.Context, rate *Rate, reason string) (*WriteResult, error) {
	if tc == nil {
		return nil, ErrPermissionDenied
	}

	card, err := s.repo.GetCardByID(ctx, rate.CardID)
	if err != nil {
		return nil, err
	}
	if card.Archived {
		return nil, ErrRateCardArchived
	}

	// 1. Tenancy & permission checks
	if card.Kind == "provider_buy" {
		if !tc.IsPlatformOrg() || !tc.HasPermission("rate.buyrate.create") {
			return nil, ErrPlatformOnlyResource
		}
	} else {
		if !tc.IsPlatformOrg() {
			if card.OwnerOrgID != tc.ActorOrgID || !tc.HasPermission("rate.sellrate.create") {
				return nil, ErrCrossTenantAccess
			}
		} else if !tc.HasPermission("rate.sellrate.create") {
			return nil, ErrPermissionDenied
		}
	}

	// 2. Validate price & destination (and normalize timestamps to UTC)
	rate.Price = TruncateMoney(rate.Price)
	if rate.Price.LessThan(decimal.Zero) {
		return nil, ErrNegativePrice
	}
	rate.EffectiveFrom = rate.EffectiveFrom.UTC()
	if rate.EffectiveTo != nil {
		utc := rate.EffectiveTo.UTC()
		rate.EffectiveTo = &utc
	}
	if err := s.repo.ValidateCountryAndOperator(ctx, rate.CountryID, rate.OperatorID); err != nil {
		return nil, ErrUnknownCountryOrOperator
	}
	if rate.EffectiveTo != nil && !rate.EffectiveTo.After(rate.EffectiveFrom) {
		return nil, ErrInvalidDateRange
	}

	// 3. Margin & Reseller Cost Floor Protection for customer sell rates
	marginOverridden := false
	if card.Kind == "customer_sell" {
		marginRes, err := s.CheckMargin(ctx, card.ID, rate.CountryID, rate.OperatorID, rate.MessageType, rate.Price, card.CurrencyCode)
		if err == nil && marginRes.Violation {
			if marginRes.ResellerCost != nil && rate.Price.LessThan(*marginRes.ResellerCost) {
				// A reseller cannot sell below its own assigned cost without platform override
				if !tc.IsPlatformOrg() || !tc.HasPermission("rate.margin.override") || strings.TrimSpace(reason) == "" {
					return nil, fmt.Errorf("%w: %s", ErrResellerCostFloor, marginRes.Reason)
				}
			}
			if !tc.HasPermission("rate.margin.override") {
				return nil, fmt.Errorf("%w: %s", ErrMarginViolation, marginRes.Reason)
			}
			if strings.TrimSpace(reason) == "" {
				return nil, fmt.Errorf("%w: mandatory override reason is required", ErrMarginViolation)
			}
			deficit := ""
			if !marginRes.MarginAmount.IsZero() {
				deficit = fmt.Sprintf(" deficit=%s", marginRes.MarginAmount.Abs().String())
			}
			reason = fmt.Sprintf("[MARGIN OVERRIDE:%s] %s", deficit, strings.TrimSpace(reason))
			marginOverridden = true
		}
	}

	overridePerm := "rate.sellrate.override"
	if card.Kind == "provider_buy" {
		overridePerm = "rate.buyrate.override"
	}

	// 4. Maker-checker check
	if !hasOverride(tc, overridePerm) && s.approvalSvc != nil {
		cr, err := s.submitChange(ctx, tc, "rate", nil, nil, "create", nil, rate, reason)
		if err != nil {
			return nil, err
		}
		return &WriteResult{PendingChange: cr}, nil
	}

	// 5. Direct write: auto-close prior open-ended rate + insert in same transaction
	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Publishing a new rate: inside single transaction, set effective_to = new_rate.effective_from
	_, _ = s.repo.ClosePreviousOpenEndedRate(ctx, tx, rate.CardID, rate.CountryID, rate.OperatorID, rate.MessageType, rate.EffectiveFrom, &tc.ActorUserID)

	rate.CreatedBy = &tc.ActorUserID
	rate.UpdatedBy = &tc.ActorUserID
	created, err := s.repo.CreateRate(ctx, tx, rate, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      fmt.Sprintf("rate.%s.create", card.Kind),
		Module:      "rate",
		EntityType:  "rate",
		EntityID:    created.ID.String(),
		AfterValue:  audit.RedactValue(created),
		Reason:      reason,
	})
	if marginOverridden {
		_ = s.auditSvc.Record(ctx, tx, audit.Entry{
			ActorUserID: &tc.ActorUserID,
			ActorOrgID:  &tc.ActorOrgID,
			Action:      "rate.margin.override",
			Module:      "rate",
			EntityType:  "rate",
			EntityID:    created.ID.String(),
			AfterValue:  audit.RedactValue(created),
			Reason:      reason,
		})
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	s.reloadEngineAsync()
	s.stripProtectedFields(tc, created)
	return &WriteResult{Rate: created}, nil
}

func (s *Service) UpdateRate(ctx context.Context, tc *tenant.Context, rate *Rate, reason string) (*WriteResult, error) {
	if tc == nil {
		return nil, ErrPermissionDenied
	}

	current, err := s.repo.GetRateByID(ctx, rate.ID)
	if err != nil {
		return nil, err
	}

	card, err := s.repo.GetCardByID(ctx, current.CardID)
	if err != nil {
		return nil, err
	}
	if card.Archived {
		return nil, ErrRateCardArchived
	}

	if card.Kind == "provider_buy" {
		if !tc.IsPlatformOrg() || !tc.HasPermission("rate.buyrate.update") {
			return nil, ErrPlatformOnlyResource
		}
	} else {
		if !tc.IsPlatformOrg() {
			if card.OwnerOrgID != tc.ActorOrgID || !tc.HasPermission("rate.sellrate.update") {
				return nil, ErrCrossTenantAccess
			}
		} else if !tc.HasPermission("rate.sellrate.update") {
			return nil, ErrPermissionDenied
		}
	}

	rate.Price = TruncateMoney(rate.Price)
	if rate.Price.LessThan(decimal.Zero) {
		return nil, ErrNegativePrice
	}
	if !rate.EffectiveFrom.IsZero() {
		rate.EffectiveFrom = rate.EffectiveFrom.UTC()
	}
	if rate.EffectiveTo != nil {
		utc := rate.EffectiveTo.UTC()
		rate.EffectiveTo = &utc
	}

	marginOverridden := false
	if card.Kind == "customer_sell" {
		marginRes, err := s.CheckMargin(ctx, card.ID, current.CountryID, current.OperatorID, current.MessageType, rate.Price, card.CurrencyCode)
		if err == nil && marginRes.Violation {
			if marginRes.ResellerCost != nil && rate.Price.LessThan(*marginRes.ResellerCost) {
				if !tc.IsPlatformOrg() || !tc.HasPermission("rate.margin.override") || strings.TrimSpace(reason) == "" {
					return nil, fmt.Errorf("%w: %s", ErrResellerCostFloor, marginRes.Reason)
				}
			}
			if !tc.HasPermission("rate.margin.override") {
				return nil, fmt.Errorf("%w: %s", ErrMarginViolation, marginRes.Reason)
			}
			if strings.TrimSpace(reason) == "" {
				return nil, fmt.Errorf("%w: mandatory override reason is required", ErrMarginViolation)
			}
			deficit := ""
			if !marginRes.MarginAmount.IsZero() {
				deficit = fmt.Sprintf(" deficit=%s", marginRes.MarginAmount.Abs().String())
			}
			reason = fmt.Sprintf("[MARGIN OVERRIDE:%s] %s", deficit, strings.TrimSpace(reason))
			marginOverridden = true
		}
	}

	overridePerm := "rate.sellrate.override"
	if card.Kind == "provider_buy" {
		overridePerm = "rate.buyrate.override"
	}

	if !hasOverride(tc, overridePerm) && s.approvalSvc != nil {
		v := current.Version
		cr, err := s.submitChange(ctx, tc, "rate", &rate.ID, &v, "update", current, rate, reason)
		if err != nil {
			return nil, err
		}
		return &WriteResult{PendingChange: cr}, nil
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rate.UpdatedBy = &tc.ActorUserID
	updated, err := s.repo.UpdateRate(ctx, tx, rate, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      fmt.Sprintf("rate.%s.update", card.Kind),
		Module:      "rate",
		EntityType:  "rate",
		EntityID:    updated.ID.String(),
		BeforeValue: audit.RedactValue(current),
		AfterValue:  audit.RedactValue(updated),
		Reason:      reason,
	})
	if marginOverridden {
		_ = s.auditSvc.Record(ctx, tx, audit.Entry{
			ActorUserID: &tc.ActorUserID,
			ActorOrgID:  &tc.ActorOrgID,
			Action:      "rate.margin.override",
			Module:      "rate",
			EntityType:  "rate",
			EntityID:    updated.ID.String(),
			BeforeValue: audit.RedactValue(current),
			AfterValue:  audit.RedactValue(updated),
			Reason:      reason,
		})
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	s.reloadEngineAsync()
	s.stripProtectedFields(tc, updated)
	return &WriteResult{Rate: updated}, nil
}

func (s *Service) CloseRate(ctx context.Context, tc *tenant.Context, rateID uuid.UUID, effectiveTo time.Time, reason string) (*WriteResult, error) {
	current, err := s.repo.GetRateByID(ctx, rateID)
	if err != nil {
		return nil, err
	}
	if effectiveTo.IsZero() {
		effectiveTo = time.Now().UTC()
	}
	if !effectiveTo.After(current.EffectiveFrom) {
		return nil, errors.New("effective_to must be after effective_from")
	}

	copyRate := *current
	copyRate.EffectiveTo = &effectiveTo
	return s.UpdateRate(ctx, tc, &copyRate, reason)
}

func (s *Service) GetRateByID(ctx context.Context, tc *tenant.Context, id uuid.UUID) (*Rate, error) {
	rt, err := s.repo.GetRateByID(ctx, id)
	if err != nil {
		return nil, err
	}

	card, err := s.repo.GetCardByID(ctx, rt.CardID)
	if err != nil {
		return nil, err
	}

	if card.Kind == "provider_buy" && (tc == nil || !tc.IsPlatformOrg()) {
		return nil, ErrPlatformOnlyResource
	}
	if card.Kind == "customer_sell" && tc != nil && !tc.IsPlatformOrg() {
		if card.OwnerOrgID != tc.ActorOrgID {
			// Check if assigned as customer card or reseller cost card
			assign, _ := s.repo.GetActiveCustomerAssignmentAt(ctx, tc.ActorOrgID, time.Now().UTC())
			costAssign, _ := s.repo.GetActiveResellerCostAssignmentAt(ctx, tc.ActorOrgID, time.Now().UTC())
			hasCustAccess := assign != nil && assign.CardID == card.ID
			hasCostAccess := costAssign != nil && costAssign.CardID == card.ID
			if !hasCustAccess && !hasCostAccess {
				return nil, ErrCrossTenantAccess
			}
		}
	}

	s.stripProtectedFields(tc, rt)
	return rt, nil
}

func (s *Service) ListRates(ctx context.Context, tc *tenant.Context, filter RateListFilter) ([]Rate, int, error) {
	card, err := s.repo.GetCardByID(ctx, filter.CardID)
	if err != nil {
		return nil, 0, err
	}

	if card.Kind == "provider_buy" && (tc == nil || !tc.IsPlatformOrg()) {
		return nil, 0, ErrPlatformOnlyResource
	}
	if card.Kind == "customer_sell" && tc != nil && !tc.IsPlatformOrg() {
		if card.OwnerOrgID != tc.ActorOrgID {
			assign, _ := s.repo.GetActiveCustomerAssignmentAt(ctx, tc.ActorOrgID, time.Now().UTC())
			costAssign, _ := s.repo.GetActiveResellerCostAssignmentAt(ctx, tc.ActorOrgID, time.Now().UTC())
			hasCustAccess := assign != nil && assign.CardID == card.ID
			hasCostAccess := costAssign != nil && costAssign.CardID == card.ID
			if !hasCustAccess && !hasCostAccess {
				return nil, 0, ErrCrossTenantAccess
			}
		}
	}

	rates, total, err := s.repo.ListRates(ctx, filter)
	if err != nil {
		return nil, 0, err
	}

	for i := range rates {
		s.stripProtectedFields(tc, &rates[i])
	}
	return rates, total, nil
}

func (s *Service) ListRateHistory(ctx context.Context, tc *tenant.Context, rateID uuid.UUID) ([]RateHistory, error) {
	rt, err := s.GetRateByID(ctx, tc, rateID)
	if err != nil {
		return nil, err
	}
	_ = rt
	return s.repo.ListRateHistory(ctx, rateID)
}

func (s *Service) RollbackRate(ctx context.Context, tc *tenant.Context, rateID uuid.UUID, targetVersion int, reason string) (*WriteResult, error) {
	current, err := s.repo.GetRateByID(ctx, rateID)
	if err != nil {
		return nil, err
	}

	card, err := s.repo.GetCardByID(ctx, current.CardID)
	if err != nil {
		return nil, err
	}

	hist, err := s.repo.GetRateHistoryVersion(ctx, rateID, targetVersion)
	if err != nil {
		return nil, err
	}

	var restored Rate
	if err := json.Unmarshal(hist.Snapshot, &restored); err != nil {
		return nil, fmt.Errorf("decode snapshot: %w", err)
	}

	restored.Version = current.Version
	restored.UpdatedBy = &tc.ActorUserID

	overridePerm := "rate.sellrate.override"
	if card.Kind == "provider_buy" {
		overridePerm = "rate.buyrate.override"
	}

	if !hasOverride(tc, overridePerm) && s.approvalSvc != nil {
		v := current.Version
		cr, err := s.submitChange(ctx, tc, "rate", &rateID, &v, "rollback", current, &restored, reason)
		if err != nil {
			return nil, err
		}
		return &WriteResult{PendingChange: cr}, nil
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	updated, err := s.repo.UpdateRate(ctx, tx, &restored, fmt.Sprintf("Rollback to version %d: %s", targetVersion, reason))
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		Action:      fmt.Sprintf("rate.%s.rollback", card.Kind),
		Module:      "rate",
		EntityType:  "rate",
		EntityID:    updated.ID.String(),
		BeforeValue: audit.RedactValue(current),
		AfterValue:  audit.RedactValue(updated),
		Reason:      fmt.Sprintf("Rollback to version %d: %s", targetVersion, reason),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	s.reloadEngineAsync()
	s.stripProtectedFields(tc, updated)
	return &WriteResult{Rate: updated}, nil
}

func (s *Service) ListUpcomingRates(ctx context.Context, tc *tenant.Context, days int) ([]Rate, error) {
	rates, err := s.repo.ListUpcomingRates(ctx, days)
	if err != nil {
		return nil, err
	}

	var filtered []Rate
	for _, rt := range rates {
		// Non-platform users only see their own accessible sell rates
		if tc != nil && !tc.IsPlatformOrg() {
			card, err := s.repo.GetCardByID(ctx, rt.CardID)
			if err != nil || card.Kind == "provider_buy" {
				continue
			}
			if card.OwnerOrgID != tc.ActorOrgID {
				assign, _ := s.repo.GetActiveCustomerAssignmentAt(ctx, tc.ActorOrgID, time.Now().UTC())
				if assign == nil || assign.CardID != card.ID {
					continue
				}
			}
		}
		s.stripProtectedFields(tc, &rt)
		filtered = append(filtered, rt)
	}
	return filtered, nil
}

// -----------------------------------------------------------------------------
// ASSIGNMENTS
// -----------------------------------------------------------------------------

func (s *Service) CreateAssignment(ctx context.Context, tc *tenant.Context, targetOrgID, cardID uuid.UUID, from time.Time, to *time.Time, reason string) (*WriteResult, error) {
	if tc == nil || !tc.IsPlatformOrg() || !tc.HasPermission("rate.assignment.create") {
		return nil, ErrPermissionDenied
	}

	card, err := s.repo.GetCardByID(ctx, cardID)
	if err != nil {
		return nil, err
	}
	if card.Kind != "customer_sell" {
		return nil, errors.New("only customer sell cards may be assigned to customers or resellers")
	}

	if from.IsZero() {
		from = time.Now().UTC()
	}
	if to != nil && !to.After(from) {
		return nil, ErrInvalidDateRange
	}

	assign := &CustomerRateAssignment{
		CustomerOrgID: targetOrgID,
		CardID:        cardID,
		EffectiveFrom: from,
		EffectiveTo:   to,
		AssignedBy:    &tc.ActorUserID,
	}

	if !hasOverride(tc, "rate.assignment.override") && s.approvalSvc != nil {
		cr, err := s.submitChange(ctx, tc, "rate_assignment", nil, nil, "create", nil, assign, reason)
		if err != nil {
			return nil, err
		}
		return &WriteResult{PendingChange: cr}, nil
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	created, err := s.repo.CreateCustomerAssignment(ctx, tx, assign, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		Action:      "rate_assignment.customer.create",
		Module:      "rate",
		EntityType:  "rate_assignment",
		EntityID:    created.ID.String(),
		AfterValue:  audit.RedactValue(created),
		Reason:      reason,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	s.reloadEngineAsync()
	return &WriteResult{CustomerAssign: created}, nil
}

func (s *Service) CreateResellerCostAssignment(ctx context.Context, tc *tenant.Context, resellerOrgID, cardID uuid.UUID, from time.Time, to *time.Time, reason string) (*WriteResult, error) {
	if tc == nil || !tc.IsPlatformOrg() || !tc.HasPermission("rate.assignment.create") {
		return nil, ErrPermissionDenied
	}

	card, err := s.repo.GetCardByID(ctx, cardID)
	if err != nil {
		return nil, err
	}
	if card.Kind != "customer_sell" {
		return nil, errors.New("reseller cost cards must be customer_sell cards")
	}

	if from.IsZero() {
		from = time.Now().UTC()
	}
	if to != nil && !to.After(from) {
		return nil, ErrInvalidDateRange
	}

	assign := &ResellerCostAssignment{
		ResellerOrgID: resellerOrgID,
		CardID:        cardID,
		EffectiveFrom: from,
		EffectiveTo:   to,
		AssignedBy:    &tc.ActorUserID,
	}

	if !hasOverride(tc, "rate.assignment.override") && s.approvalSvc != nil {
		cr, err := s.submitChange(ctx, tc, "rate_assignment", nil, nil, "create_reseller_cost", nil, assign, reason)
		if err != nil {
			return nil, err
		}
		return &WriteResult{PendingChange: cr}, nil
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	created, err := s.repo.CreateResellerCostAssignment(ctx, tx, assign, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		Action:      "rate_assignment.reseller_cost.create",
		Module:      "rate",
		EntityType:  "reseller_cost_assignment",
		EntityID:    created.ID.String(),
		AfterValue:  audit.RedactValue(created),
		Reason:      reason,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	s.reloadEngineAsync()
	return &WriteResult{ResellerAssign: created}, nil
}

func (s *Service) DeactivateAssignment(ctx context.Context, tc *tenant.Context, id uuid.UUID, isCustomer bool, reason string) (*WriteResult, error) {
	if tc == nil || !tc.IsPlatformOrg() || !tc.HasPermission("rate.assignment.update") {
		return nil, ErrPermissionDenied
	}

	now := time.Now().UTC()

	if isCustomer {
		current, err := s.repo.GetCustomerAssignmentByID(ctx, id)
		if err != nil {
			return nil, err
		}
		copyA := *current
		copyA.Status = "inactive"
		copyA.EffectiveTo = &now

		if !hasOverride(tc, "rate.assignment.override") && s.approvalSvc != nil {
			v := current.Version
			cr, err := s.submitChange(ctx, tc, "rate_assignment", &id, &v, "deactivate", current, &copyA, reason)
			if err != nil {
				return nil, err
			}
			return &WriteResult{PendingChange: cr}, nil
		}

		tx, err := s.repo.BeginTx(ctx)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback(ctx)

		updated, err := s.repo.UpdateCustomerAssignment(ctx, tx, &copyA, reason)
		if err != nil {
			return nil, err
		}

		_ = s.auditSvc.Record(ctx, tx, audit.Entry{
			ActorUserID: &tc.ActorUserID,
			Action:      "rate_assignment.customer.deactivate",
			Module:      "rate",
			EntityType:  "rate_assignment",
			EntityID:    updated.ID.String(),
			BeforeValue: audit.RedactValue(current),
			AfterValue:  audit.RedactValue(updated),
			Reason:      reason,
		})

		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		s.reloadEngineAsync()
		return &WriteResult{CustomerAssign: updated}, nil
	}

	// Reseller assignment
	current, err := s.repo.GetResellerCostAssignmentByID(ctx, id)
	if err != nil {
		return nil, err
	}
	copyA := *current
	copyA.Status = "inactive"
	copyA.EffectiveTo = &now

	if !hasOverride(tc, "rate.assignment.override") && s.approvalSvc != nil {
		v := current.Version
		cr, err := s.submitChange(ctx, tc, "rate_assignment", &id, &v, "deactivate_reseller_cost", current, &copyA, reason)
		if err != nil {
			return nil, err
		}
		return &WriteResult{PendingChange: cr}, nil
	}

	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	updated, err := s.repo.UpdateResellerCostAssignment(ctx, tx, &copyA, reason)
	if err != nil {
		return nil, err
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		Action:      "rate_assignment.reseller_cost.deactivate",
		Module:      "rate",
		EntityType:  "reseller_cost_assignment",
		EntityID:    updated.ID.String(),
		BeforeValue: audit.RedactValue(current),
		AfterValue:  audit.RedactValue(updated),
		Reason:      reason,
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	s.reloadEngineAsync()
	return &WriteResult{ResellerAssign: updated}, nil
}

func (s *Service) ListCustomerAssignments(ctx context.Context, tc *tenant.Context, customerOrgID *uuid.UUID) ([]CustomerRateAssignment, error) {
	if tc == nil || tc.IsCustomerOrg() || !tc.HasPermission("rate.assignment.read") {
		return nil, ErrPermissionDenied
	}
	if !tc.IsPlatformOrg() {
		// Non-platform user can only see their own org's assignments
		customerOrgID = &tc.ActorOrgID
	}
	return s.repo.ListCustomerAssignments(ctx, customerOrgID)
}

func (s *Service) ListResellerCostAssignments(ctx context.Context, tc *tenant.Context, resellerOrgID *uuid.UUID) ([]ResellerCostAssignment, error) {
	if tc == nil || tc.IsCustomerOrg() || !tc.HasPermission("rate.assignment.read") {
		return nil, ErrPermissionDenied
	}
	if !tc.IsPlatformOrg() {
		// Reseller can only see their own cost card assignment
		resellerOrgID = &tc.ActorOrgID
	}
	return s.repo.ListResellerCostAssignments(ctx, resellerOrgID)
}

func (s *Service) GetAssignmentHistory(ctx context.Context, tc *tenant.Context, id uuid.UUID, isCustomer bool) (any, error) {
	if tc == nil || tc.IsCustomerOrg() || !tc.HasPermission("rate.assignment.read") {
		return nil, ErrPermissionDenied
	}
	if isCustomer {
		if !tc.IsPlatformOrg() {
			assign, err := s.repo.GetCustomerAssignmentByID(ctx, id)
			if err != nil {
				return nil, err
			}
			if assign.CustomerOrgID != tc.ActorOrgID {
				return nil, ErrCrossTenantAccess
			}
		}
		return s.repo.ListCustomerAssignmentHistory(ctx, id)
	}
	if !tc.IsPlatformOrg() {
		assign, err := s.repo.GetResellerCostAssignmentByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if assign.ResellerOrgID != tc.ActorOrgID {
			return nil, ErrCrossTenantAccess
		}
	}
	return s.repo.ListResellerCostAssignmentHistory(ctx, id)
}

func (s *Service) GetResellerAssignedCostCard(ctx context.Context, tc *tenant.Context, resellerOrgID uuid.UUID) (*RateCard, error) {
	if tc == nil {
		return nil, ErrPermissionDenied
	}
	if !tc.IsPlatformOrg() && tc.ActorOrgID != resellerOrgID {
		return nil, ErrCrossTenantAccess
	}

	assign, err := s.repo.GetActiveResellerCostAssignmentAt(ctx, resellerOrgID, time.Now().UTC())
	if err != nil || assign == nil {
		return nil, ErrAssignmentNotFound
	}

	card, err := s.repo.GetCardByID(ctx, assign.CardID)
	if err != nil {
		return nil, err
	}
	return card, nil
}

// -----------------------------------------------------------------------------
// MARGIN PROTECTION
// -----------------------------------------------------------------------------

func (s *Service) CheckMargin(
	ctx context.Context,
	cardID, countryID uuid.UUID,
	operatorID *uuid.UUID,
	msgType string,
	sellPrice decimal.Decimal,
	currency string,
) (MarginResult, error) {
	now := time.Now().UTC()
	sellPrice = TruncateMoney(sellPrice)
	if msgType == "" || msgType == "sms" {
		msgType = "default"
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))

	res := MarginResult{
		SellPrice: sellPrice,
		Currency:  currency,
		Status:    "ok",
	}

	// 1. If this card belongs to a reseller, verify against reseller's assigned cost card
	card, err := s.repo.GetCardByID(ctx, cardID)
	if err == nil && card != nil {
		if currency == "" {
			currency = strings.ToUpper(card.CurrencyCode)
			res.Currency = currency
		}
		resellerCost, costCurrency, err := s.repo.GetResellerCostRateWithCurrencyAt(ctx, card.OwnerOrgID, countryID, operatorID, msgType, now)
		if err == nil && costCurrency != "" && !strings.EqualFold(costCurrency, currency) {
			res.Comparable = false
			res.Status = "not_comparable"
			res.Reason = fmt.Sprintf("not comparable: reseller cost card currency (%s) differs from sell card currency (%s); currencies are never converted automatically", costCurrency, currency)
			return res, nil
		}
		if err == nil && resellerCost != nil {
			res.ResellerCost = resellerCost
			if sellPrice.LessThan(*resellerCost) {
				res.Comparable = true
				res.Status = "violation"
				res.Violation = true
				res.MarginAmount = sellPrice.Sub(*resellerCost)
				if sellPrice.GreaterThan(decimal.Zero) {
					res.MarginPercent = res.MarginAmount.Div(sellPrice).Mul(decimal.NewFromInt(100)).Truncate(2)
				}
				res.Reason = fmt.Sprintf("Selling price (%s %s) is below reseller assigned cost rate (%s %s)", currency, sellPrice.String(), currency, resellerCost.String())
				return res, nil
			}
		}
	}

	// 2. Inspect active coverage and lowest buy rate among providers
	lowestBuy, sameCcyProvs, activeCovProvs, otherCcyProvs, otherCurrencies, err := s.repo.InspectBuyCoverageAndRates(ctx, countryID, operatorID, msgType, now, currency)
	if err != nil {
		return MarginResult{}, fmt.Errorf("check margin lowest buy: %w", err)
	}
	res.ActiveProviders = activeCovProvs

	if lowestBuy == nil || sameCcyProvs == 0 {
		if otherCcyProvs > 0 {
			res.Comparable = false
			res.Status = "not_comparable"
			res.Reason = fmt.Sprintf("not comparable: active buy rates exist in [%s], cannot compare with %s without FX conversion", strings.Join(otherCurrencies, ", "), currency)
			return res, nil
		}
		if res.ResellerCost != nil {
			// Reseller cost floor was checked and satisfied
			res.Comparable = true
			res.Status = "ok"
			return res, nil
		}
		if activeCovProvs == 0 {
			res.Comparable = false
			res.Status = "no_active_coverage"
			res.Reason = "no active provider coverage for destination"
			return res, nil
		}
		res.Comparable = false
		res.Status = "no_active_buy_rate"
		res.Reason = "active provider coverage exists, but no active provider buy rate is loaded in matching currency"
		return res, nil
	}

	res.Comparable = true
	res.LowestBuyCost = *lowestBuy
	res.MarginAmount = sellPrice.Sub(*lowestBuy)
	if sellPrice.GreaterThan(decimal.Zero) {
		res.MarginPercent = res.MarginAmount.Div(sellPrice).Mul(decimal.NewFromInt(100)).Truncate(2)
	}

	if sellPrice.LessThan(*lowestBuy) {
		res.Status = "violation"
		res.Violation = true
		res.Reason = fmt.Sprintf("Selling price (%s %s) is below lowest provider buy cost (%s %s)", currency, sellPrice.String(), currency, lowestBuy.String())
		return res, nil
	}

	return res, nil
}

func (s *Service) GetMarginReport(ctx context.Context, tc *tenant.Context, filter MarginReportFilter) ([]MarginReportItem, int, error) {
	if tc == nil || !tc.IsPlatformOrg() || !tc.HasPermission("rate.margin.view") {
		return nil, 0, ErrPlatformOnlyResource
	}
	return s.repo.GenerateMarginReport(ctx, filter)
}

// -----------------------------------------------------------------------------
// RATE LOOKUP SERVICE
// -----------------------------------------------------------------------------

func (s *Service) LookupSellRate(ctx context.Context, customerOrgID uuid.UUID, countryID uuid.UUID, operatorID *uuid.UUID, msgType string, at time.Time) (*RateMatch, error) {
	return s.engine.LookupSellRate(ctx, customerOrgID, countryID, operatorID, msgType, at)
}

func (s *Service) LookupByPhoneNumber(ctx context.Context, customerOrgID uuid.UUID, e164 string, msgType string, at time.Time) (*RateMatch, error) {
	if s.numberSvc == nil {
		return nil, errors.New("number intelligence service is not configured")
	}

	// 1. Resolve number via Phase 3 number engine
	numRes := s.numberSvc.Lookup(ctx, number.LookupRequest{Number: e164})
	if !numRes.IsValid || numRes.Country == nil {
		return &RateMatch{Found: false, E164: e164}, nil
	}

	countryID := numRes.Country.ID
	var operatorID *uuid.UUID
	if numRes.Operator != nil {
		operatorID = numRes.Operator.ID
	}

	// 2. Call LookupSellRate
	match, err := s.engine.LookupSellRate(ctx, customerOrgID, countryID, operatorID, msgType, at)
	if err != nil {
		return nil, err
	}
	if match != nil {
		match.E164 = e164
		if match.Found {
			match.CountryISO2 = numRes.Country.ISO2
			match.CountryName = numRes.Country.Name
			if numRes.Operator != nil {
				match.OperatorName = numRes.Operator.Name
			}
		}
	}
	return match, nil
}

func (s *Service) LookupBuyRate(ctx context.Context, tc *tenant.Context, cardID uuid.UUID, countryID uuid.UUID, operatorID *uuid.UUID, msgType string, at time.Time) (*RateMatch, error) {
	if tc == nil || !tc.IsPlatformOrg() || !tc.HasPermission("rate.buyrate.read") {
		return nil, ErrPlatformOnlyResource
	}
	return s.engine.LookupBuyRate(ctx, cardID, countryID, operatorID, msgType, at)
}

func (s *Service) BulkLookup(ctx context.Context, tc *tenant.Context, items []BulkLookupItem) ([]BulkLookupResult, error) {
	if len(items) > 1000 {
		return nil, ErrBulkLookupLimitExceeded
	}

	results := make([]BulkLookupResult, len(items))
	for i, it := range items {
		// Non-platform user can only lookup for their own org
		if tc != nil && !tc.IsPlatformOrg() && it.CustomerOrgID != tc.ActorOrgID {
			results[i] = BulkLookupResult{Item: it, Error: "access denied for customer organization"}
			continue
		}

		at := time.Now().UTC()
		if it.At != nil && !it.At.IsZero() {
			at = *it.At
		}

		match, err := s.LookupByPhoneNumber(ctx, it.CustomerOrgID, it.Number, it.MessageType, at)
		if err != nil {
			results[i] = BulkLookupResult{Item: it, Error: err.Error()}
		} else {
			results[i] = BulkLookupResult{Item: it, Match: match}
		}
	}
	return results, nil
}

// -----------------------------------------------------------------------------
// FIELD-LEVEL SECURITY STRIPPER
// -----------------------------------------------------------------------------

func (s *Service) stripProtectedFields(tc *tenant.Context, r *Rate) {
	if r == nil {
		return
	}
	// Only platform users may see buy price or margin metrics
	if tc == nil || !tc.IsPlatformOrg() || !tc.HasPermission("rate.margin.view") {
		r.LowestBuyCost = nil
		r.MarginAmount = nil
		r.MarginPercent = nil
	}
}

// -----------------------------------------------------------------------------
// APPROVAL APPLIERS
// -----------------------------------------------------------------------------

func (s *Service) submitChange(
	ctx context.Context,
	tc *tenant.Context,
	entityType string,
	entityID *uuid.UUID,
	version *int,
	op string,
	current any,
	proposed any,
	reason string,
) (*approval.ChangeRequest, error) {
	var currentData, proposedData json.RawMessage
	if current != nil {
		b, err := json.Marshal(current)
		if err != nil {
			return nil, err
		}
		currentData = b
	}
	if proposed != nil {
		b, err := json.Marshal(proposed)
		if err != nil {
			return nil, err
		}
		proposedData = b
	}

	cr := approval.ChangeRequest{
		EntityType:    entityType,
		EntityID:      entityID,
		EntityVersion: version,
		Operation:     op,
		ProposedData:  proposedData,
		CurrentData:   currentData,
		MakerUserID:   tc.ActorUserID,
		MadeAt:        time.Now().UTC(),
		Status:        "pending",
		RejectReason:  reason,
	}

	return s.approvalSvc.SubmitChangeRequest(ctx, nil, cr)
}

func (s *Service) applyRateCardChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	switch cr.Operation {
	case "create":
		var card RateCard
		if err := json.Unmarshal(cr.ProposedData, &card); err != nil {
			return err
		}
		card.CreatedBy = &cr.MakerUserID
		card.UpdatedBy = &cr.MakerUserID
		_, err := s.repo.CreateCard(ctx, tx, &card, "approved: "+cr.Operation)
		return err

	case "update", "rollback":
		var card RateCard
		if err := json.Unmarshal(cr.ProposedData, &card); err != nil {
			return err
		}
		card.UpdatedBy = cr.CheckerUserID
		_, err := s.repo.UpdateCard(ctx, tx, &card, "approved: "+cr.Operation)
		return err

	default:
		return fmt.Errorf("unsupported rate card operation: %s", cr.Operation)
	}
}

func (s *Service) applyRateChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	switch cr.Operation {
	case "create":
		var rate Rate
		if err := json.Unmarshal(cr.ProposedData, &rate); err != nil {
			return err
		}
		// Close previous open-ended rate inside the same transaction
		_, _ = s.repo.ClosePreviousOpenEndedRate(ctx, tx, rate.CardID, rate.CountryID, rate.OperatorID, rate.MessageType, rate.EffectiveFrom, &cr.MakerUserID)
		rate.CreatedBy = &cr.MakerUserID
		rate.UpdatedBy = &cr.MakerUserID
		_, err := s.repo.CreateRate(ctx, tx, &rate, "approved: "+cr.Operation)
		return err

	case "update", "rollback":
		var rate Rate
		if err := json.Unmarshal(cr.ProposedData, &rate); err != nil {
			return err
		}
		rate.UpdatedBy = cr.CheckerUserID
		_, err := s.repo.UpdateRate(ctx, tx, &rate, "approved: "+cr.Operation)
		return err

	default:
		return fmt.Errorf("unsupported rate operation: %s", cr.Operation)
	}
}

func (s *Service) applyAssignmentChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	switch cr.Operation {
	case "create":
		var a CustomerRateAssignment
		if err := json.Unmarshal(cr.ProposedData, &a); err != nil {
			return err
		}
		a.AssignedBy = &cr.MakerUserID
		_, err := s.repo.CreateCustomerAssignment(ctx, tx, &a, "approved: "+cr.Operation)
		return err

	case "create_reseller_cost":
		var a ResellerCostAssignment
		if err := json.Unmarshal(cr.ProposedData, &a); err != nil {
			return err
		}
		a.AssignedBy = &cr.MakerUserID
		_, err := s.repo.CreateResellerCostAssignment(ctx, tx, &a, "approved: "+cr.Operation)
		return err

	case "deactivate":
		var a CustomerRateAssignment
		if err := json.Unmarshal(cr.ProposedData, &a); err != nil {
			return err
		}
		a.AssignedBy = cr.CheckerUserID
		_, err := s.repo.UpdateCustomerAssignment(ctx, tx, &a, "approved: "+cr.Operation)
		return err

	case "deactivate_reseller_cost":
		var a ResellerCostAssignment
		if err := json.Unmarshal(cr.ProposedData, &a); err != nil {
			return err
		}
		a.AssignedBy = cr.CheckerUserID
		_, err := s.repo.UpdateResellerCostAssignment(ctx, tx, &a, "approved: "+cr.Operation)
		return err

	default:
		return fmt.Errorf("unsupported rate assignment operation: %s", cr.Operation)
	}
}

func GetRateChangeThresholdPct() decimal.Decimal {
	val := strings.TrimSpace(os.Getenv("RATE_CHANGE_THRESHOLD_PCT"))
	if val != "" {
		if d, err := decimal.NewFromString(val); err == nil && d.GreaterThan(decimal.Zero) {
			return d
		}
	}
	return decimal.NewFromInt(20) // Default 20%
}
