package routing

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

var (
	ErrForbidden          = errors.New("access denied: routing is platform-only")
	ErrUnauthorized       = errors.New("unauthorized")
	ErrValidation         = errors.New("validation failed")
	ErrInvalidStatus      = errors.New("invalid route plan status for this operation")
	ErrMissingReason      = errors.New("reason is required")
	ErrInactiveConnection = errors.New("cannot activate plan: contains inactive connection")
	ErrWeightSumMismatch  = errors.New("cannot activate plan: weighted entries must sum to 100")
)

type Service struct {
	repo     Repository
	approval *approval.Service
	audit    *audit.Service
	pool     *pgxpool.Pool
	engine   *Engine
}

func NewService(repo Repository, appSvc *approval.Service, auditSvc *audit.Service, pool *pgxpool.Pool, engine *Engine) *Service {
	s := &Service{
		repo:     repo,
		approval: appSvc,
		audit:    auditSvc,
		pool:     pool,
		engine:   engine,
	}

	if appSvc != nil {
		s.registerApprovals()
	}

	return s
}

func (s *Service) SetEngine(engine *Engine) {
	s.engine = engine
}

func (s *Service) registerApprovals() {
	s.approval.RegisterApplier("route_plan_activate", &routePlanActivateApplier{svc: s})
	s.approval.RegisterApplier("route_plan_deactivate", &routePlanDeactivateApplier{svc: s})
	s.approval.RegisterApplier("route_plan_entry_change", &routePlanEntryApplier{svc: s})
	s.approval.RegisterApplier("route_assignment_change", &routeAssignmentApplier{svc: s})
	s.approval.RegisterApplier("routing_rule_change", &routingRuleApplier{svc: s})
	s.approval.RegisterApplier("connection_reenable", &connectionReenableApplier{svc: s})

	s.approval.RegisterApprovePermission("route_plan_activate", "routing.plan.approve")
	s.approval.RegisterApprovePermission("route_plan_deactivate", "routing.plan.approve")
	s.approval.RegisterApprovePermission("route_plan_entry_change", "routing.entry.approve")
	s.approval.RegisterApprovePermission("route_assignment_change", "routing.assignment.approve")
	s.approval.RegisterApprovePermission("routing_rule_change", "routing.rule.approve")
	s.approval.RegisterApprovePermission("connection_reenable", "routing.killswitch,routing.plan.approve")
}

func (s *Service) enforcePlatform(tc *tenant.Context, requiredPerm string) error {
	if tc == nil || !tc.IsPlatformOrg() {
		return ErrForbidden
	}
	if requiredPerm != "" && !tc.HasPermission(requiredPerm) {
		return ErrUnauthorized
	}
	return nil
}

// -----------------------------------------------------------------------------
// Route Plans
// -----------------------------------------------------------------------------

func (s *Service) CreatePlan(ctx context.Context, tc *tenant.Context, plan *RoutePlan) (*RoutePlan, error) {
	if err := s.enforcePlatform(tc, "routing.plan.create"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(plan.Name) == "" {
		return nil, fmt.Errorf("%w: plan name is required", ErrValidation)
	}
	if strings.TrimSpace(plan.Code) == "" {
		return nil, fmt.Errorf("%w: plan code is required", ErrValidation)
	}
	if plan.Strategy != StrategyPriority && plan.Strategy != StrategyLeastCost && plan.Strategy != StrategyWeighted {
		return nil, fmt.Errorf("%w: invalid strategy %s", ErrValidation, plan.Strategy)
	}
	plan.Status = PlanDraft
	plan.CreatedBy = tc.ActorUserID

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	created, err := s.repo.CreatePlan(ctx, tx, plan)
	if err != nil {
		return nil, err
	}

	if err := s.repo.RecordPlanHistory(ctx, tx, created.ID, tc.ActorUserID, "create", created); err != nil {
		return nil, err
	}

	_ = s.audit.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "routing.plan.create",
		Module:      "routing",
		EntityType:  "route_plan",
		EntityID:    created.ID.String(),
		AfterValue:  audit.RedactValue(created),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	go TryReload(context.Background(), s.pool)
	return created, nil
}

func (s *Service) UpdatePlan(ctx context.Context, tc *tenant.Context, plan *RoutePlan) (*RoutePlan, error) {
	if err := s.enforcePlatform(tc, "routing.plan.edit"); err != nil {
		return nil, err
	}
	existing, err := s.repo.GetPlanByID(ctx, plan.ID)
	if err != nil {
		return nil, err
	}
	if existing.Status == PlanArchived {
		return nil, fmt.Errorf("%w: cannot edit archived route plan", ErrInvalidStatus)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	plan.Status = existing.Status
	updated, err := s.repo.UpdatePlan(ctx, tx, plan)
	if err != nil {
		return nil, err
	}

	if err := s.repo.RecordPlanHistory(ctx, tx, updated.ID, tc.ActorUserID, "update", updated); err != nil {
		return nil, err
	}

	_ = s.audit.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "routing.plan.update",
		Module:      "routing",
		EntityType:  "route_plan",
		EntityID:    updated.ID.String(),
		BeforeValue: audit.RedactValue(existing),
		AfterValue:  audit.RedactValue(updated),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	go TryReload(context.Background(), s.pool)
	return updated, nil
}

func (s *Service) GetPlan(ctx context.Context, tc *tenant.Context, id uuid.UUID) (*RoutePlan, error) {
	if err := s.enforcePlatform(tc, "routing.plan.view"); err != nil {
		return nil, err
	}
	return s.repo.GetPlanByID(ctx, id)
}

func (s *Service) ListPlans(ctx context.Context, tc *tenant.Context, status string, page, pageSize int) ([]RoutePlan, int, error) {
	if err := s.enforcePlatform(tc, "routing.plan.view"); err != nil {
		return nil, 0, err
	}
	return s.repo.ListPlans(ctx, status, page, pageSize)
}

func (s *Service) ValidatePlanForActivation(ctx context.Context, planID uuid.UUID) error {
	plan, err := s.repo.GetPlanByID(ctx, planID)
	if err != nil {
		return err
	}
	if len(plan.Entries) == 0 {
		return fmt.Errorf("%w: route plan has no entries", ErrValidation)
	}

	var activeEntries []RoutePlanEntry
	totalWeight := 0
	var validationErrors []string
	var hasInactiveConn bool
	var hasWeightMismatch bool
	seenScopes := make(map[string]bool)

	for _, e := range plan.Entries {
		if e.Status == "active" {
			activeEntries = append(activeEntries, e)
			totalWeight += e.Weight

			scopeKey := fmt.Sprintf("%s:%s:%v:%v:%s", e.ConnectionID, e.ScopeType, e.ScopeCountryID, e.ScopeOperatorID, e.ScopePrefix)
			if seenScopes[scopeKey] {
				validationErrors = append(validationErrors, fmt.Sprintf("overlapping duplicate entry for connection %s with scope %s", e.ConnectionID, e.ScopeType))
			}
			seenScopes[scopeKey] = true

			// Validate connection is active
			conn, err := s.repo.GetConnectionDetails(ctx, e.ConnectionID)
			if err != nil {
				validationErrors = append(validationErrors, fmt.Sprintf("connection %s lookup error: %v", e.ConnectionID, err))
			} else if conn.ConnectionStatus != "active" || conn.ProviderStatus != "active" {
				hasInactiveConn = true
				validationErrors = append(validationErrors, fmt.Sprintf("inactive connection %s (status=%s, provider_status=%s)", e.ConnectionID, conn.ConnectionStatus, conn.ProviderStatus))
			}
		}
	}

	if len(activeEntries) == 0 {
		validationErrors = append(validationErrors, "route plan has no active entries")
	}

	if plan.Strategy == StrategyWeighted && totalWeight != 100 {
		hasWeightMismatch = true
		validationErrors = append(validationErrors, fmt.Sprintf("weighted entries must sum to exactly 100, got %d", totalWeight))
	}

	if len(validationErrors) > 0 {
		var errChain error = ErrValidation
		if hasInactiveConn {
			errChain = fmt.Errorf("%w: %w", errChain, ErrInactiveConnection)
		}
		if hasWeightMismatch {
			errChain = fmt.Errorf("%w: %w", errChain, ErrWeightSumMismatch)
		}
		return fmt.Errorf("%w: plan activation blocked: %s", errChain, strings.Join(validationErrors, "; "))
	}

	return nil
}

func (s *Service) RequestPlanActivation(ctx context.Context, tc *tenant.Context, planID uuid.UUID) (*RoutePlan, *approval.ChangeRequest, error) {
	if err := s.enforcePlatform(tc, "routing.plan.activate"); err != nil {
		return nil, nil, err
	}
	plan, err := s.repo.GetPlanByID(ctx, planID)
	if err != nil {
		return nil, nil, err
	}
	if plan.Status != PlanDraft && plan.Status != PlanDeactivated {
		return nil, nil, fmt.Errorf("%w: plan must be draft or deactivated to activate", ErrInvalidStatus)
	}

	// Validate entries before submitting
	if err := s.ValidatePlanForActivation(ctx, planID); err != nil {
		return nil, nil, err
	}

	// Maker-checker check: if user has override or approve permission, apply directly
	if tc.HasPermission("routing.plan.approve") || tc.HasPermission("routing.plan.override") {
		activated, err := s.applyPlanStatusChange(ctx, tc.ActorUserID, tc.ActorOrgID, planID, PlanActive, plan.Version, "direct activation")
		return activated, nil, err
	}

	// Otherwise submit change request
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	proposedData, _ := json.Marshal(map[string]interface{}{
		"plan_id":  planID,
		"status":   PlanActive,
		"version":  plan.Version,
	})
	currentData, _ := json.Marshal(plan)

	cr, err := s.approval.SubmitChangeRequest(ctx, tx, approval.ChangeRequest{
		EntityType:    "route_plan_activate",
		EntityID:      &planID,
		Operation:     "activate",
		ProposedData:  proposedData,
		CurrentData:   currentData,
		MakerUserID:   tc.ActorUserID,
		EntityVersion: &plan.Version,
	})
	if err != nil {
		return nil, nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}

	return plan, cr, nil
}

func (s *Service) RequestPlanDeactivation(ctx context.Context, tc *tenant.Context, planID uuid.UUID) (*RoutePlan, *approval.ChangeRequest, error) {
	if err := s.enforcePlatform(tc, "routing.plan.deactivate"); err != nil {
		return nil, nil, err
	}
	plan, err := s.repo.GetPlanByID(ctx, planID)
	if err != nil {
		return nil, nil, err
	}
	if plan.Status != PlanActive {
		return nil, nil, fmt.Errorf("%w: plan must be active to deactivate", ErrInvalidStatus)
	}

	// Maker-checker check
	if tc.HasPermission("routing.plan.approve") || tc.HasPermission("routing.plan.override") {
		deactivated, err := s.applyPlanStatusChange(ctx, tc.ActorUserID, tc.ActorOrgID, planID, PlanDeactivated, plan.Version, "direct deactivation")
		return deactivated, nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	proposedData, _ := json.Marshal(map[string]interface{}{
		"plan_id":  planID,
		"status":   PlanDeactivated,
		"version":  plan.Version,
	})
	currentData, _ := json.Marshal(plan)

	cr, err := s.approval.SubmitChangeRequest(ctx, tx, approval.ChangeRequest{
		EntityType:    "route_plan_deactivate",
		EntityID:      &planID,
		Operation:     "deactivate",
		ProposedData:  proposedData,
		CurrentData:   currentData,
		MakerUserID:   tc.ActorUserID,
		EntityVersion: &plan.Version,
	})
	if err != nil {
		return nil, nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}

	return plan, cr, nil
}

func (s *Service) applyPlanStatusChange(ctx context.Context, userID, orgID uuid.UUID, planID uuid.UUID, newStatus PlanStatus, version int, reason string) (*RoutePlan, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	plan, err := s.repo.GetPlanByID(ctx, planID)
	if err != nil {
		return nil, err
	}

	plan.Status = newStatus
	plan.Version = version
	updated, err := s.repo.UpdatePlan(ctx, tx, plan)
	if err != nil {
		return nil, err
	}

	changeType := "activate"
	if newStatus == PlanDeactivated {
		changeType = "deactivate"
	}
	if err := s.repo.RecordPlanHistory(ctx, tx, updated.ID, userID, changeType, updated); err != nil {
		return nil, err
	}

	_ = s.audit.Record(ctx, tx, audit.Entry{
		ActorUserID: &userID,
		ActorOrgID:  &orgID,
		Action:      fmt.Sprintf("routing.plan.%s", changeType),
		Module:      "routing",
		EntityType:  "route_plan",
		EntityID:    updated.ID.String(),
		Reason:      reason,
		AfterValue:  audit.RedactValue(updated),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	go TryReload(context.Background(), s.pool)
	return updated, nil
}

func (s *Service) ArchivePlan(ctx context.Context, tc *tenant.Context, planID uuid.UUID) error {
	if err := s.enforcePlatform(tc, "routing.plan.archive"); err != nil {
		return err
	}
	plan, err := s.repo.GetPlanByID(ctx, planID)
	if err != nil {
		return err
	}
	if plan.Status == PlanActive {
		return fmt.Errorf("%w: cannot archive an active route plan (deactivate first)", ErrInvalidStatus)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	plan.Status = PlanArchived
	updated, err := s.repo.UpdatePlan(ctx, tx, plan)
	if err != nil {
		return err
	}

	if err := s.repo.RecordPlanHistory(ctx, tx, updated.ID, tc.ActorUserID, "archive", updated); err != nil {
		return err
	}

	_ = s.audit.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "routing.plan.archive",
		Module:      "routing",
		EntityType:  "route_plan",
		EntityID:    updated.ID.String(),
		AfterValue:  audit.RedactValue(updated),
	})

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	go TryReload(context.Background(), s.pool)
	return nil
}

func (s *Service) GetPlanHistory(ctx context.Context, tc *tenant.Context, planID uuid.UUID) ([]RoutePlanHistory, error) {
	if err := s.enforcePlatform(tc, "routing.plan.view"); err != nil {
		return nil, err
	}
	return s.repo.GetPlanHistory(ctx, planID)
}

func (s *Service) RollbackPlan(ctx context.Context, tc *tenant.Context, planID uuid.UUID, historyID uuid.UUID) (*RoutePlan, error) {
	if err := s.enforcePlatform(tc, "routing.plan.edit"); err != nil {
		return nil, err
	}
	histories, err := s.repo.GetPlanHistory(ctx, planID)
	if err != nil {
		return nil, err
	}
	var targetHistory *RoutePlanHistory
	for i := range histories {
		if histories[i].ID == historyID {
			targetHistory = &histories[i]
			break
		}
	}
	if targetHistory == nil {
		return nil, ErrNotFound
	}

	var snapshot RoutePlan
	if err := json.Unmarshal(targetHistory.Snapshot, &snapshot); err != nil {
		return nil, fmt.Errorf("unmarshal history snapshot: %w", err)
	}

	current, err := s.repo.GetPlanByID(ctx, planID)
	if err != nil {
		return nil, err
	}

	snapshot.ID = current.ID
	snapshot.Version = current.Version
	snapshot.Status = current.Status // Keep current status

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	updated, err := s.repo.UpdatePlan(ctx, tx, &snapshot)
	if err != nil {
		return nil, err
	}

	if err := s.repo.RecordPlanHistory(ctx, tx, updated.ID, tc.ActorUserID, "rollback", updated); err != nil {
		return nil, err
	}

	_ = s.audit.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "routing.plan.rollback",
		Module:      "routing",
		EntityType:  "route_plan",
		EntityID:    updated.ID.String(),
		BeforeValue: audit.RedactValue(current),
		AfterValue:  audit.RedactValue(updated),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	go TryReload(context.Background(), s.pool)
	return updated, nil
}

// -----------------------------------------------------------------------------
// Route Plan Entries
// -----------------------------------------------------------------------------

func (s *Service) CreateEntry(ctx context.Context, tc *tenant.Context, entry *RoutePlanEntry) (*RoutePlanEntry, *approval.ChangeRequest, error) {
	if err := s.enforcePlatform(tc, "routing.entry.create"); err != nil {
		return nil, nil, err
	}
	plan, err := s.repo.GetPlanByID(ctx, entry.PlanID)
	if err != nil {
		return nil, nil, err
	}
	if plan.Status == PlanArchived {
		return nil, nil, fmt.Errorf("%w: plan is archived", ErrInvalidStatus)
	}

	// Validate weight and priority
	if entry.Weight < 1 || entry.Weight > 100 {
		return nil, nil, fmt.Errorf("%w: weight must be between 1 and 100", ErrValidation)
	}
	if entry.Priority < 1 {
		entry.Priority = 1
	}
	if entry.ScopeType == "" {
		entry.ScopeType = "global"
	}
	if entry.Status == "" {
		entry.Status = "active"
	}
	if entry.DestinationNumberFormat == "" {
		entry.DestinationNumberFormat = "e164"
	}

	// If plan is active and user lacks override, submit change request
	if plan.Status == PlanActive && !tc.HasPermission("routing.entry.approve") && !tc.HasPermission("routing.entry.override") {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return nil, nil, err
		}
		defer tx.Rollback(ctx)

		proposedData, _ := json.Marshal(entry)
		cr, err := s.approval.SubmitChangeRequest(ctx, tx, approval.ChangeRequest{
			EntityType:   "route_plan_entry_change",
			EntityID:     &plan.ID,
			Operation:    "create",
			ProposedData: proposedData,
			MakerUserID:  tc.ActorUserID,
		})
		if err != nil {
			return nil, nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, nil, err
		}
		return entry, cr, nil
	}

	// Direct create
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	created, err := s.repo.CreateEntry(ctx, tx, entry)
	if err != nil {
		return nil, nil, err
	}

	if err := s.repo.RecordEntryHistory(ctx, tx, created.ID, tc.ActorUserID, "create", created); err != nil {
		return nil, nil, err
	}

	_ = s.audit.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "routing.entry.create",
		Module:      "routing",
		EntityType:  "route_plan_entry",
		EntityID:    created.ID.String(),
		AfterValue:  audit.RedactValue(created),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}

	go TryReload(context.Background(), s.pool)
	return created, nil, nil
}

func (s *Service) UpdateEntry(ctx context.Context, tc *tenant.Context, entry *RoutePlanEntry) (*RoutePlanEntry, *approval.ChangeRequest, error) {
	if err := s.enforcePlatform(tc, "routing.entry.edit"); err != nil {
		return nil, nil, err
	}
	existing, err := s.repo.GetEntryByID(ctx, entry.ID)
	if err != nil {
		return nil, nil, err
	}

	plan, err := s.repo.GetPlanByID(ctx, existing.PlanID)
	if err != nil {
		return nil, nil, err
	}
	if plan.Status == PlanArchived {
		return nil, nil, fmt.Errorf("%w: plan is archived", ErrInvalidStatus)
	}

	if entry.Weight < 1 || entry.Weight > 100 {
		return nil, nil, fmt.Errorf("%w: weight must be between 1 and 100", ErrValidation)
	}

	if plan.Status == PlanActive && !tc.HasPermission("routing.entry.approve") && !tc.HasPermission("routing.entry.override") {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return nil, nil, err
		}
		defer tx.Rollback(ctx)

		proposedData, _ := json.Marshal(entry)
		currentData, _ := json.Marshal(existing)
		cr, err := s.approval.SubmitChangeRequest(ctx, tx, approval.ChangeRequest{
			EntityType:    "route_plan_entry_change",
			EntityID:      &existing.ID,
			Operation:     "update",
			ProposedData:  proposedData,
			CurrentData:   currentData,
			MakerUserID:   tc.ActorUserID,
			EntityVersion: &existing.Version,
		})
		if err != nil {
			return nil, nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, nil, err
		}
		return entry, cr, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	updated, err := s.repo.UpdateEntry(ctx, tx, entry)
	if err != nil {
		return nil, nil, err
	}

	if err := s.repo.RecordEntryHistory(ctx, tx, updated.ID, tc.ActorUserID, "update", updated); err != nil {
		return nil, nil, err
	}

	_ = s.audit.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "routing.entry.update",
		Module:      "routing",
		EntityType:  "route_plan_entry",
		EntityID:    updated.ID.String(),
		BeforeValue: audit.RedactValue(existing),
		AfterValue:  audit.RedactValue(updated),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}

	go TryReload(context.Background(), s.pool)
	return updated, nil, nil
}

func (s *Service) DeleteEntry(ctx context.Context, tc *tenant.Context, entryID uuid.UUID) (*approval.ChangeRequest, error) {
	if err := s.enforcePlatform(tc, "routing.entry.edit"); err != nil {
		return nil, err
	}
	existing, err := s.repo.GetEntryByID(ctx, entryID)
	if err != nil {
		return nil, err
	}

	plan, err := s.repo.GetPlanByID(ctx, existing.PlanID)
	if err != nil {
		return nil, err
	}
	if plan.Status == PlanArchived {
		return nil, fmt.Errorf("%w: plan is archived", ErrInvalidStatus)
	}

	if plan.Status == PlanActive && !tc.HasPermission("routing.entry.approve") && !tc.HasPermission("routing.entry.override") {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback(ctx)

		currentData, _ := json.Marshal(existing)
		cr, err := s.approval.SubmitChangeRequest(ctx, tx, approval.ChangeRequest{
			EntityType:    "route_plan_entry_change",
			EntityID:      &existing.ID,
			Operation:     "delete",
			ProposedData:  []byte("{}"),
			CurrentData:   currentData,
			MakerUserID:   tc.ActorUserID,
			EntityVersion: &existing.Version,
		})
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return cr, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if err := s.repo.DeleteEntry(ctx, tx, entryID); err != nil {
		return nil, err
	}

	if err := s.repo.RecordEntryHistory(ctx, tx, entryID, tc.ActorUserID, "delete", existing); err != nil {
		return nil, err
	}

	_ = s.audit.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "routing.entry.delete",
		Module:      "routing",
		EntityType:  "route_plan_entry",
		EntityID:    entryID.String(),
		BeforeValue: audit.RedactValue(existing),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	go TryReload(context.Background(), s.pool)
	return nil, nil
}

func (s *Service) ListEntries(ctx context.Context, tc *tenant.Context, planID uuid.UUID) ([]RoutePlanEntry, error) {
	if err := s.enforcePlatform(tc, "routing.entry.view"); err != nil {
		return nil, err
	}
	return s.repo.ListEntriesByPlanID(ctx, planID)
}

func (s *Service) RollbackEntry(ctx context.Context, tc *tenant.Context, entryID uuid.UUID, historyID uuid.UUID) (*RoutePlanEntry, error) {
	if err := s.enforcePlatform(tc, "routing.entry.edit"); err != nil {
		return nil, err
	}
	histories, err := s.repo.GetEntryHistory(ctx, entryID)
	if err != nil {
		return nil, err
	}
	var targetHistory *RoutePlanEntryHistory
	for i := range histories {
		if histories[i].ID == historyID {
			targetHistory = &histories[i]
			break
		}
	}
	if targetHistory == nil {
		return nil, ErrNotFound
	}

	var snapshot RoutePlanEntry
	if err := json.Unmarshal(targetHistory.Snapshot, &snapshot); err != nil {
		return nil, fmt.Errorf("unmarshal history snapshot: %w", err)
	}

	current, err := s.repo.GetEntryByID(ctx, entryID)
	if err != nil {
		return nil, err
	}

	snapshot.ID = current.ID
	snapshot.Version = current.Version

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	updated, err := s.repo.UpdateEntry(ctx, tx, &snapshot)
	if err != nil {
		return nil, err
	}

	if err := s.repo.RecordEntryHistory(ctx, tx, updated.ID, tc.ActorUserID, "rollback", updated); err != nil {
		return nil, err
	}

	_ = s.audit.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "routing.entry.rollback",
		Module:      "routing",
		EntityType:  "route_plan_entry",
		EntityID:    updated.ID.String(),
		BeforeValue: audit.RedactValue(current),
		AfterValue:  audit.RedactValue(updated),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	go TryReload(context.Background(), s.pool)
	return updated, nil
}

// -----------------------------------------------------------------------------
// Customer Route Assignments
// -----------------------------------------------------------------------------

func (s *Service) CreateAssignment(ctx context.Context, tc *tenant.Context, a *CustomerRouteAssignment) (*CustomerRouteAssignment, *approval.ChangeRequest, error) {
	if err := s.enforcePlatform(tc, "routing.assignment.create"); err != nil {
		return nil, nil, err
	}
	if a.CustomerOrgID == uuid.Nil {
		return nil, nil, fmt.Errorf("%w: customer organization is required", ErrValidation)
	}
	if a.PlanID == uuid.Nil {
		return nil, nil, fmt.Errorf("%w: plan is required", ErrValidation)
	}
	if a.Status == "" {
		a.Status = "active"
	}
	if a.Priority < 1 {
		a.Priority = 1
	}
	a.CreatedBy = tc.ActorUserID

	// Maker-checker check
	if !tc.HasPermission("routing.assignment.approve") && !tc.HasPermission("routing.assignment.override") {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return nil, nil, err
		}
		defer tx.Rollback(ctx)

		proposedData, _ := json.Marshal(a)
		cr, err := s.approval.SubmitChangeRequest(ctx, tx, approval.ChangeRequest{
			EntityType:   "route_assignment_change",
			Operation:    "create",
			ProposedData: proposedData,
			MakerUserID:  tc.ActorUserID,
		})
		if err != nil {
			return nil, nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, nil, err
		}
		return a, cr, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	created, err := s.repo.CreateAssignment(ctx, tx, a)
	if err != nil {
		return nil, nil, err
	}

	if err := s.repo.RecordAssignmentHistory(ctx, tx, created.ID, tc.ActorUserID, "create", created); err != nil {
		return nil, nil, err
	}

	_ = s.audit.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "routing.assignment.create",
		Module:      "routing",
		EntityType:  "customer_route_assignment",
		EntityID:    created.ID.String(),
		AfterValue:  audit.RedactValue(created),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}

	go TryReload(context.Background(), s.pool)
	return created, nil, nil
}

func (s *Service) UpdateAssignment(ctx context.Context, tc *tenant.Context, a *CustomerRouteAssignment) (*CustomerRouteAssignment, *approval.ChangeRequest, error) {
	if err := s.enforcePlatform(tc, "routing.assignment.edit"); err != nil {
		return nil, nil, err
	}
	existing, err := s.repo.GetAssignmentByID(ctx, a.ID)
	if err != nil {
		return nil, nil, err
	}

	if !tc.HasPermission("routing.assignment.approve") && !tc.HasPermission("routing.assignment.override") {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return nil, nil, err
		}
		defer tx.Rollback(ctx)

		proposedData, _ := json.Marshal(a)
		currentData, _ := json.Marshal(existing)
		cr, err := s.approval.SubmitChangeRequest(ctx, tx, approval.ChangeRequest{
			EntityType:    "route_assignment_change",
			EntityID:      &existing.ID,
			Operation:     "update",
			ProposedData:  proposedData,
			CurrentData:   currentData,
			MakerUserID:   tc.ActorUserID,
			EntityVersion: &existing.Version,
		})
		if err != nil {
			return nil, nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, nil, err
		}
		return a, cr, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	updated, err := s.repo.UpdateAssignment(ctx, tx, a)
	if err != nil {
		return nil, nil, err
	}

	if err := s.repo.RecordAssignmentHistory(ctx, tx, updated.ID, tc.ActorUserID, "update", updated); err != nil {
		return nil, nil, err
	}

	_ = s.audit.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "routing.assignment.update",
		Module:      "routing",
		EntityType:  "customer_route_assignment",
		EntityID:    updated.ID.String(),
		BeforeValue: audit.RedactValue(existing),
		AfterValue:  audit.RedactValue(updated),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}

	go TryReload(context.Background(), s.pool)
	return updated, nil, nil
}

func (s *Service) DeleteAssignment(ctx context.Context, tc *tenant.Context, id uuid.UUID) (*approval.ChangeRequest, error) {
	if err := s.enforcePlatform(tc, "routing.assignment.edit"); err != nil {
		return nil, err
	}
	existing, err := s.repo.GetAssignmentByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if !tc.HasPermission("routing.assignment.approve") && !tc.HasPermission("routing.assignment.override") {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback(ctx)

		currentData, _ := json.Marshal(existing)
		cr, err := s.approval.SubmitChangeRequest(ctx, tx, approval.ChangeRequest{
			EntityType:    "route_assignment_change",
			EntityID:      &existing.ID,
			Operation:     "delete",
			ProposedData:  []byte("{}"),
			CurrentData:   currentData,
			MakerUserID:   tc.ActorUserID,
			EntityVersion: &existing.Version,
		})
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return cr, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if err := s.repo.DeleteAssignment(ctx, tx, id); err != nil {
		return nil, err
	}

	if err := s.repo.RecordAssignmentHistory(ctx, tx, id, tc.ActorUserID, "delete", existing); err != nil {
		return nil, err
	}

	_ = s.audit.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "routing.assignment.delete",
		Module:      "routing",
		EntityType:  "customer_route_assignment",
		EntityID:    id.String(),
		BeforeValue: audit.RedactValue(existing),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	go TryReload(context.Background(), s.pool)
	return nil, nil
}

func (s *Service) ListAssignments(ctx context.Context, tc *tenant.Context, customerOrgID *uuid.UUID, page, pageSize int) ([]CustomerRouteAssignment, int, error) {
	if err := s.enforcePlatform(tc, "routing.assignment.view"); err != nil {
		return nil, 0, err
	}
	return s.repo.ListAssignments(ctx, customerOrgID, page, pageSize)
}

// -----------------------------------------------------------------------------
// Routing Rules
// -----------------------------------------------------------------------------

func (s *Service) CreateRule(ctx context.Context, tc *tenant.Context, r *RoutingRule) (*RoutingRule, *approval.ChangeRequest, error) {
	if err := s.enforcePlatform(tc, "routing.rule.create"); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(r.Name) == "" {
		return nil, nil, fmt.Errorf("%w: rule name is required", ErrValidation)
	}
	if r.ActionType != "block" && r.ActionType != "force" && r.ActionType != "avoid" && r.ActionType != "prefer" {
		return nil, nil, fmt.Errorf("%w: invalid action type %s", ErrValidation, r.ActionType)
	}
	if r.Priority < 1 {
		r.Priority = 1
	}
	if r.Status == "" {
		r.Status = "active"
	}
	r.CreatedBy = tc.ActorUserID

	if !tc.HasPermission("routing.rule.approve") && !tc.HasPermission("routing.rule.override") {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return nil, nil, err
		}
		defer tx.Rollback(ctx)

		proposedData, _ := json.Marshal(r)
		cr, err := s.approval.SubmitChangeRequest(ctx, tx, approval.ChangeRequest{
			EntityType:   "routing_rule_change",
			Operation:    "create",
			ProposedData: proposedData,
			MakerUserID:  tc.ActorUserID,
		})
		if err != nil {
			return nil, nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, nil, err
		}
		return r, cr, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	created, err := s.repo.CreateRule(ctx, tx, r)
	if err != nil {
		return nil, nil, err
	}

	if err := s.repo.RecordRuleHistory(ctx, tx, created.ID, tc.ActorUserID, "create", created); err != nil {
		return nil, nil, err
	}

	_ = s.audit.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "routing.rule.create",
		Module:      "routing",
		EntityType:  "routing_rule",
		EntityID:    created.ID.String(),
		AfterValue:  audit.RedactValue(created),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}

	go TryReload(context.Background(), s.pool)
	return created, nil, nil
}

func (s *Service) UpdateRule(ctx context.Context, tc *tenant.Context, r *RoutingRule) (*RoutingRule, *approval.ChangeRequest, error) {
	if err := s.enforcePlatform(tc, "routing.rule.edit"); err != nil {
		return nil, nil, err
	}
	existing, err := s.repo.GetRuleByID(ctx, r.ID)
	if err != nil {
		return nil, nil, err
	}

	if !tc.HasPermission("routing.rule.approve") && !tc.HasPermission("routing.rule.override") {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return nil, nil, err
		}
		defer tx.Rollback(ctx)

		proposedData, _ := json.Marshal(r)
		currentData, _ := json.Marshal(existing)
		cr, err := s.approval.SubmitChangeRequest(ctx, tx, approval.ChangeRequest{
			EntityType:    "routing_rule_change",
			EntityID:      &existing.ID,
			Operation:     "update",
			ProposedData:  proposedData,
			CurrentData:   currentData,
			MakerUserID:   tc.ActorUserID,
			EntityVersion: &existing.Version,
		})
		if err != nil {
			return nil, nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, nil, err
		}
		return r, cr, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	updated, err := s.repo.UpdateRule(ctx, tx, r)
	if err != nil {
		return nil, nil, err
	}

	if err := s.repo.RecordRuleHistory(ctx, tx, updated.ID, tc.ActorUserID, "update", updated); err != nil {
		return nil, nil, err
	}

	_ = s.audit.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "routing.rule.update",
		Module:      "routing",
		EntityType:  "routing_rule",
		EntityID:    updated.ID.String(),
		BeforeValue: audit.RedactValue(existing),
		AfterValue:  audit.RedactValue(updated),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}

	go TryReload(context.Background(), s.pool)
	return updated, nil, nil
}

func (s *Service) DeleteRule(ctx context.Context, tc *tenant.Context, id uuid.UUID) (*approval.ChangeRequest, error) {
	if err := s.enforcePlatform(tc, "routing.rule.edit"); err != nil {
		return nil, err
	}
	existing, err := s.repo.GetRuleByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if !tc.HasPermission("routing.rule.approve") && !tc.HasPermission("routing.rule.override") {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback(ctx)

		currentData, _ := json.Marshal(existing)
		cr, err := s.approval.SubmitChangeRequest(ctx, tx, approval.ChangeRequest{
			EntityType:    "routing_rule_change",
			EntityID:      &existing.ID,
			Operation:     "delete",
			ProposedData:  []byte("{}"),
			CurrentData:   currentData,
			MakerUserID:   tc.ActorUserID,
			EntityVersion: &existing.Version,
		})
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return cr, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if err := s.repo.DeleteRule(ctx, tx, id); err != nil {
		return nil, err
	}

	if err := s.repo.RecordRuleHistory(ctx, tx, id, tc.ActorUserID, "delete", existing); err != nil {
		return nil, err
	}

	_ = s.audit.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "routing.rule.delete",
		Module:      "routing",
		EntityType:  "routing_rule",
		EntityID:    id.String(),
		BeforeValue: audit.RedactValue(existing),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	go TryReload(context.Background(), s.pool)
	return nil, nil
}

func (s *Service) ListRules(ctx context.Context, tc *tenant.Context, status string, page, pageSize int) ([]RoutingRule, int, error) {
	if err := s.enforcePlatform(tc, "routing.rule.view"); err != nil {
		return nil, 0, err
	}
	return s.repo.ListRules(ctx, status, page, pageSize)
}

// -----------------------------------------------------------------------------
// Kill Switch (Connection Overrides)
// -----------------------------------------------------------------------------

// DisableConnection applies instantly, audited, triggers reload.
func (s *Service) DisableConnection(ctx context.Context, tc *tenant.Context, connectionID uuid.UUID, reason string) (*ConnectionOverride, error) {
	if err := s.enforcePlatform(tc, "routing.killswitch"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("%w: reason is required when disabling connection via kill switch", ErrMissingReason)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	o := &ConnectionOverride{
		ConnectionID: connectionID,
		State:        "disabled",
		Reason:       reason,
		SetBy:        tc.ActorUserID,
	}

	created, err := s.repo.SetConnectionOverride(ctx, tx, o)
	if err != nil {
		return nil, err
	}

	_ = s.audit.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "routing.killswitch.disable",
		Module:      "routing",
		EntityType:  "provider_connection_override",
		EntityID:    connectionID.String(),
		Reason:      reason,
		AfterValue:  audit.RedactValue(created),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	TryReload(ctx, s.pool)
	return created, nil
}

// EnableConnection requires approval unless user has routing.plan.approve or routing.killswitch override.
func (s *Service) EnableConnection(ctx context.Context, tc *tenant.Context, connectionID uuid.UUID, reason string) (*approval.ChangeRequest, error) {
	if err := s.enforcePlatform(tc, "routing.killswitch"); err != nil {
		return nil, err
	}
	existing, err := s.repo.GetConnectionOverride(ctx, connectionID)
	if err != nil {
		return nil, err
	}

	// Always requires approval via connection_reenable approval type
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	proposedData, _ := json.Marshal(map[string]interface{}{
		"connection_id": connectionID,
		"reason":        reason,
	})
	currentData, _ := json.Marshal(existing)

	cr, err := s.approval.SubmitChangeRequest(ctx, tx, approval.ChangeRequest{
		EntityType:   "connection_reenable",
		EntityID:     &connectionID,
		Operation:    "enable",
		ProposedData: proposedData,
		CurrentData:  currentData,
		MakerUserID:  tc.ActorUserID,
	})
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return cr, nil
}

func (s *Service) ListOverrides(ctx context.Context, tc *tenant.Context) ([]ConnectionOverride, error) {
	if err := s.enforcePlatform(tc, "routing.killswitch"); err != nil {
		return nil, err
	}
	return s.repo.ListConnectionOverrides(ctx)
}

// -----------------------------------------------------------------------------
// Simulator & Status
// -----------------------------------------------------------------------------

func (s *Service) Simulate(ctx context.Context, tc *tenant.Context, req SimulateRequest) (*SimulateResult, error) {
	if err := s.enforcePlatform(tc, "routing.simulate"); err != nil {
		return nil, err
	}
	if req.DestinationE164 == "" && req.DestinationNumber != "" {
		req.DestinationE164 = req.DestinationNumber
	}
	if strings.TrimSpace(req.DestinationE164) == "" {
		return nil, fmt.Errorf("%w: destination_e164 is required", ErrValidation)
	}

	res, err := s.engine.Simulate(ctx, req)
	if err != nil {
		return nil, err
	}

	// Audit simulation call
	tx, err := s.pool.Begin(ctx)
	if err == nil {
		_ = s.audit.Record(ctx, tx, audit.Entry{
			ActorUserID: &tc.ActorUserID,
			ActorOrgID:  &tc.ActorOrgID,
			Action:      "routing.simulate",
			Module:      "routing",
			EntityType:  "simulate",
			EntityID:    req.DestinationE164,
			AfterValue: audit.RedactValue(map[string]interface{}{
				"destination": req.DestinationE164,
				"customer":    req.CustomerOrgID,
				"candidates":  len(res.Candidates),
				"plan_used":   res.PlanUsed,
			}),
		})
		_ = tx.Commit(ctx)
	}

	return res, nil
}

func (s *Service) SnapshotStatus(ctx context.Context, tc *tenant.Context) (map[string]interface{}, error) {
	if err := s.enforcePlatform(tc, "routing.snapshot.view"); err != nil {
		return nil, err
	}
	snap := CurrentSnapshot()
	if snap == nil {
		return map[string]interface{}{
			"status": "not_loaded",
		}, nil
	}

	return map[string]interface{}{
		"status":      "ready",
		"built_at":    snap.BuildAt,
		"age_seconds": time.Since(snap.BuildAt).Seconds(),
		"plans_count": len(snap.Plans),
		"rules_count": len(snap.Rules),
		"overrides":   len(snap.Overrides),
		"connections": len(snap.Connections),
	}, nil
}

func (s *Service) CoverageReport(ctx context.Context, tc *tenant.Context) (map[string]interface{}, error) {
	if err := s.enforcePlatform(tc, "routing.snapshot.view"); err != nil {
		return nil, err
	}
	snap := CurrentSnapshot()
	if snap == nil {
		return map[string]interface{}{"status": "not_loaded"}, nil
	}

	// 1. Query active countries
	countryRows, err := s.pool.Query(ctx, `
		SELECT id, iso2, name, COALESCE(calling_code, '')
		FROM countries
		WHERE status = 'active'
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("load countries: %w", err)
	}
	defer countryRows.Close()

	type CountryInfo struct {
		ID       uuid.UUID `json:"id"`
		ISO2     string    `json:"iso2"`
		Name     string    `json:"name"`
		DialCode string    `json:"dial_code"`
	}
	var allCountries []CountryInfo
	for countryRows.Next() {
		var c CountryInfo
		if err := countryRows.Scan(&c.ID, &c.ISO2, &c.Name, &c.DialCode); err == nil {
			allCountries = append(allCountries, c)
		}
	}

	// 2. Query active customers
	custRows, err := s.pool.Query(ctx, `
		SELECT id, name
		FROM organizations
		WHERE type = 'customer' AND status = 'active'
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("load customers: %w", err)
	}
	defer custRows.Close()

	type CustomerInfo struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	}
	var allCustomers []CustomerInfo
	for custRows.Next() {
		var c CustomerInfo
		if err := custRows.Scan(&c.ID, &c.Name); err == nil {
			allCustomers = append(allCustomers, c)
		}
	}

	// 3. Destinations with no route and single-provider destinations
	type DestinationCoverage struct {
		CountryID     uuid.UUID `json:"country_id"`
		CountryISO2   string    `json:"country_iso2"`
		CountryName   string    `json:"country_name"`
		DialCode      string    `json:"dial_code"`
		ProviderCount int       `json:"provider_count"`
	}

	var destinationsWithNoRoute []DestinationCoverage
	var singleProviderDestinations []DestinationCoverage

	for _, c := range allCountries {
		distinctProviders := make(map[uuid.UUID]bool)
		for _, conn := range snap.Connections {
			if conn.ConnectionStatus == "active" && conn.ProviderStatus == "active" && !conn.KillSwitched {
				status := snap.GetCoverageStatus(conn.ProviderID, c.ID, nil)
				if status == "active" {
					distinctProviders[conn.ProviderID] = true
				}
			}
		}
		count := len(distinctProviders)
		dc := DestinationCoverage{
			CountryID:     c.ID,
			CountryISO2:   c.ISO2,
			CountryName:   c.Name,
			DialCode:      c.DialCode,
			ProviderCount: count,
		}
		if count == 0 {
			destinationsWithNoRoute = append(destinationsWithNoRoute, dc)
		} else if count == 1 {
			singleProviderDestinations = append(singleProviderDestinations, dc)
		}
	}

	// 4. Customers without route assignment
	assignedCustomerIDs := make(map[uuid.UUID]bool)
	for _, a := range snap.Assignments {
		if a.Status == "active" {
			assignedCustomerIDs[a.CustomerOrgID] = true
		}
	}
	var customersWithoutRoute []CustomerInfo
	for _, cust := range allCustomers {
		if !assignedCustomerIDs[cust.ID] {
			customersWithoutRoute = append(customersWithoutRoute, cust)
		}
	}

	// 5. Entries missing buy rate
	type MissingBuyRateEntry struct {
		PlanID         uuid.UUID `json:"plan_id"`
		PlanName       string    `json:"plan_name"`
		EntryID        uuid.UUID `json:"entry_id"`
		ConnectionID   uuid.UUID `json:"connection_id"`
		ConnectionName string    `json:"connection_name"`
		ProviderName   string    `json:"provider_name"`
	}
	var entriesMissingBuyRate []MissingBuyRateEntry
	for _, p := range snap.Plans {
		if p.Status == PlanActive {
			for _, e := range p.Entries {
				if e.Status == "active" {
					conn, ok := snap.Connections[e.ConnectionID]
					if !ok || conn.BuyCardID == nil {
						entriesMissingBuyRate = append(entriesMissingBuyRate, MissingBuyRateEntry{
							PlanID:         p.ID,
							PlanName:       p.Name,
							EntryID:        e.ID,
							ConnectionID:   e.ConnectionID,
							ConnectionName: e.ConnectionName,
							ProviderName:   e.ProviderName,
						})
					}
				}
			}
		}
	}

	return map[string]interface{}{
		"built_at":                       snap.BuildAt,
		"destinations_with_no_route":     destinationsWithNoRoute,
		"no_route_count":                 len(destinationsWithNoRoute),
		"single_provider_destinations":   singleProviderDestinations,
		"single_provider_count":          len(singleProviderDestinations),
		"customers_without_route":        customersWithoutRoute,
		"customers_without_route_count":  len(customersWithoutRoute),
		"entries_missing_buy_rate":       entriesMissingBuyRate,
		"entries_missing_buy_rate_count": len(entriesMissingBuyRate),
	}, nil
}

// -----------------------------------------------------------------------------
// Approval ChangeAppliers
// -----------------------------------------------------------------------------

type routePlanActivateApplier struct {
	svc *Service
}

func (a *routePlanActivateApplier) ApplyChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	if cr.EntityID == nil {
		return errors.New("missing entity ID for route plan activation")
	}
	plan, err := a.svc.repo.GetPlanByID(ctx, *cr.EntityID)
	if err != nil {
		return err
	}

	// Validate again before activating
	if err := a.svc.ValidatePlanForActivation(ctx, plan.ID); err != nil {
		return err
	}

	plan.Status = PlanActive
	if cr.EntityVersion != nil {
		plan.Version = *cr.EntityVersion
	}
	updated, err := a.svc.repo.UpdatePlan(ctx, tx, plan)
	if err != nil {
		return err
	}

	checkerID := cr.MakerUserID
	if cr.CheckerUserID != nil {
		checkerID = *cr.CheckerUserID
	}
	if err := a.svc.repo.RecordPlanHistory(ctx, tx, updated.ID, checkerID, "activate", updated); err != nil {
		return err
	}

	go TryReload(context.Background(), a.svc.pool)
	return nil
}

type routePlanDeactivateApplier struct {
	svc *Service
}

func (a *routePlanDeactivateApplier) ApplyChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	if cr.EntityID == nil {
		return errors.New("missing entity ID for route plan deactivation")
	}
	plan, err := a.svc.repo.GetPlanByID(ctx, *cr.EntityID)
	if err != nil {
		return err
	}

	plan.Status = PlanDeactivated
	if cr.EntityVersion != nil {
		plan.Version = *cr.EntityVersion
	}
	updated, err := a.svc.repo.UpdatePlan(ctx, tx, plan)
	if err != nil {
		return err
	}

	checkerID := cr.MakerUserID
	if cr.CheckerUserID != nil {
		checkerID = *cr.CheckerUserID
	}
	if err := a.svc.repo.RecordPlanHistory(ctx, tx, updated.ID, checkerID, "deactivate", updated); err != nil {
		return err
	}

	go TryReload(context.Background(), a.svc.pool)
	return nil
}

type routePlanEntryApplier struct {
	svc *Service
}

func (a *routePlanEntryApplier) ApplyChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	checkerID := cr.MakerUserID
	if cr.CheckerUserID != nil {
		checkerID = *cr.CheckerUserID
	}

	switch cr.Operation {
	case "create":
		var entry RoutePlanEntry
		if err := json.Unmarshal(cr.ProposedData, &entry); err != nil {
			return err
		}
		created, err := a.svc.repo.CreateEntry(ctx, tx, &entry)
		if err != nil {
			return err
		}
		if err := a.svc.repo.RecordEntryHistory(ctx, tx, created.ID, checkerID, "create", created); err != nil {
			return err
		}

	case "update":
		var entry RoutePlanEntry
		if err := json.Unmarshal(cr.ProposedData, &entry); err != nil {
			return err
		}
		if cr.EntityVersion != nil {
			entry.Version = *cr.EntityVersion
		}
		updated, err := a.svc.repo.UpdateEntry(ctx, tx, &entry)
		if err != nil {
			return err
		}
		if err := a.svc.repo.RecordEntryHistory(ctx, tx, updated.ID, checkerID, "update", updated); err != nil {
			return err
		}

	case "delete":
		if cr.EntityID == nil {
			return errors.New("missing entity id for entry delete")
		}
		if err := a.svc.repo.DeleteEntry(ctx, tx, *cr.EntityID); err != nil {
			return err
		}
		if err := a.svc.repo.RecordEntryHistory(ctx, tx, *cr.EntityID, checkerID, "delete", map[string]string{"id": cr.EntityID.String()}); err != nil {
			return err
		}
	}

	go TryReload(context.Background(), a.svc.pool)
	return nil
}

type routeAssignmentApplier struct {
	svc *Service
}

func (a *routeAssignmentApplier) ApplyChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	checkerID := cr.MakerUserID
	if cr.CheckerUserID != nil {
		checkerID = *cr.CheckerUserID
	}

	switch cr.Operation {
	case "create":
		var assign CustomerRouteAssignment
		if err := json.Unmarshal(cr.ProposedData, &assign); err != nil {
			return err
		}
		created, err := a.svc.repo.CreateAssignment(ctx, tx, &assign)
		if err != nil {
			return err
		}
		if err := a.svc.repo.RecordAssignmentHistory(ctx, tx, created.ID, checkerID, "create", created); err != nil {
			return err
		}

	case "update":
		var assign CustomerRouteAssignment
		if err := json.Unmarshal(cr.ProposedData, &assign); err != nil {
			return err
		}
		if cr.EntityVersion != nil {
			assign.Version = *cr.EntityVersion
		}
		updated, err := a.svc.repo.UpdateAssignment(ctx, tx, &assign)
		if err != nil {
			return err
		}
		if err := a.svc.repo.RecordAssignmentHistory(ctx, tx, updated.ID, checkerID, "update", updated); err != nil {
			return err
		}

	case "delete":
		if cr.EntityID == nil {
			return errors.New("missing entity id for assignment delete")
		}
		if err := a.svc.repo.DeleteAssignment(ctx, tx, *cr.EntityID); err != nil {
			return err
		}
		if err := a.svc.repo.RecordAssignmentHistory(ctx, tx, *cr.EntityID, checkerID, "delete", map[string]string{"id": cr.EntityID.String()}); err != nil {
			return err
		}
	}

	go TryReload(context.Background(), a.svc.pool)
	return nil
}

type routingRuleApplier struct {
	svc *Service
}

func (a *routingRuleApplier) ApplyChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	checkerID := cr.MakerUserID
	if cr.CheckerUserID != nil {
		checkerID = *cr.CheckerUserID
	}

	switch cr.Operation {
	case "create":
		var rule RoutingRule
		if err := json.Unmarshal(cr.ProposedData, &rule); err != nil {
			return err
		}
		created, err := a.svc.repo.CreateRule(ctx, tx, &rule)
		if err != nil {
			return err
		}
		if err := a.svc.repo.RecordRuleHistory(ctx, tx, created.ID, checkerID, "create", created); err != nil {
			return err
		}

	case "update":
		var rule RoutingRule
		if err := json.Unmarshal(cr.ProposedData, &rule); err != nil {
			return err
		}
		if cr.EntityVersion != nil {
			rule.Version = *cr.EntityVersion
		}
		updated, err := a.svc.repo.UpdateRule(ctx, tx, &rule)
		if err != nil {
			return err
		}
		if err := a.svc.repo.RecordRuleHistory(ctx, tx, updated.ID, checkerID, "update", updated); err != nil {
			return err
		}

	case "delete":
		if cr.EntityID == nil {
			return errors.New("missing entity id for rule delete")
		}
		if err := a.svc.repo.DeleteRule(ctx, tx, *cr.EntityID); err != nil {
			return err
		}
		if err := a.svc.repo.RecordRuleHistory(ctx, tx, *cr.EntityID, checkerID, "delete", map[string]string{"id": cr.EntityID.String()}); err != nil {
			return err
		}
	}

	go TryReload(context.Background(), a.svc.pool)
	return nil
}

type connectionReenableApplier struct {
	svc *Service
}

func (a *connectionReenableApplier) ApplyChange(ctx context.Context, tx pgx.Tx, cr *approval.ChangeRequest) error {
	if cr.EntityID == nil {
		return errors.New("missing connection ID for reenable")
	}

	checkerID := cr.MakerUserID
	if cr.CheckerUserID != nil {
		checkerID = *cr.CheckerUserID
	}

	if err := a.svc.repo.RemoveConnectionOverride(ctx, tx, *cr.EntityID); err != nil {
		return err
	}

	_ = a.svc.audit.Record(ctx, tx, audit.Entry{
		ActorUserID: &checkerID,
		Action:      "routing.killswitch.enable",
		Module:      "routing",
		EntityType:  "provider_connection_override",
		EntityID:    cr.EntityID.String(),
		Reason:      "Re-enabled via approved change request",
	})

	slog.Info("connection re-enabled via approval", "connection_id", *cr.EntityID)
	go TryReload(context.Background(), a.svc.pool)
	return nil
}

// -----------------------------------------------------------------------------
// History and Rollback for Assignments and Rules
// -----------------------------------------------------------------------------

func (s *Service) GetAssignmentHistory(ctx context.Context, tc *tenant.Context, assignmentID uuid.UUID) ([]CustomerRouteAssignmentHistory, error) {
	if err := s.enforcePlatform(tc, "routing.assignment.view"); err != nil {
		return nil, err
	}
	return s.repo.GetAssignmentHistory(ctx, assignmentID)
}

func (s *Service) RollbackAssignment(ctx context.Context, tc *tenant.Context, assignmentID uuid.UUID, historyID uuid.UUID) (*CustomerRouteAssignment, error) {
	if err := s.enforcePlatform(tc, "routing.assignment.edit"); err != nil {
		return nil, err
	}
	historyList, err := s.repo.GetAssignmentHistory(ctx, assignmentID)
	if err != nil {
		return nil, err
	}

	var target *CustomerRouteAssignmentHistory
	for i := range historyList {
		if historyList[i].ID == historyID {
			target = &historyList[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("%w: history snapshot not found", ErrNotFound)
	}

	current, err := s.repo.GetAssignmentByID(ctx, assignmentID)
	if err != nil {
		return nil, err
	}

	var snapAssignment CustomerRouteAssignment
	if err := json.Unmarshal(target.Snapshot, &snapAssignment); err != nil {
		return nil, fmt.Errorf("unmarshal history snapshot: %w", err)
	}

	snapAssignment.ID = assignmentID
	snapAssignment.Version = current.Version

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	updated, err := s.repo.UpdateAssignment(ctx, tx, &snapAssignment)
	if err != nil {
		return nil, err
	}

	if err := s.repo.RecordAssignmentHistory(ctx, tx, updated.ID, tc.ActorUserID, "rollback", updated); err != nil {
		return nil, err
	}

	_ = s.audit.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "routing.assignment.rollback",
		Module:      "routing",
		EntityType:  "customer_route_assignment",
		EntityID:    updated.ID.String(),
		BeforeValue: audit.RedactValue(current),
		AfterValue:  audit.RedactValue(updated),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	go TryReload(context.Background(), s.pool)
	return updated, nil
}

func (s *Service) GetRuleHistory(ctx context.Context, tc *tenant.Context, ruleID uuid.UUID) ([]RoutingRuleHistory, error) {
	if err := s.enforcePlatform(tc, "routing.rule.view"); err != nil {
		return nil, err
	}
	return s.repo.GetRuleHistory(ctx, ruleID)
}

func (s *Service) RollbackRule(ctx context.Context, tc *tenant.Context, ruleID uuid.UUID, historyID uuid.UUID) (*RoutingRule, error) {
	if err := s.enforcePlatform(tc, "routing.rule.edit"); err != nil {
		return nil, err
	}
	historyList, err := s.repo.GetRuleHistory(ctx, ruleID)
	if err != nil {
		return nil, err
	}

	var target *RoutingRuleHistory
	for i := range historyList {
		if historyList[i].ID == historyID {
			target = &historyList[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("%w: history snapshot not found", ErrNotFound)
	}

	current, err := s.repo.GetRuleByID(ctx, ruleID)
	if err != nil {
		return nil, err
	}

	var snapRule RoutingRule
	if err := json.Unmarshal(target.Snapshot, &snapRule); err != nil {
		return nil, fmt.Errorf("unmarshal history snapshot: %w", err)
	}

	snapRule.ID = ruleID
	snapRule.Version = current.Version

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	updated, err := s.repo.UpdateRule(ctx, tx, &snapRule)
	if err != nil {
		return nil, err
	}

	if err := s.repo.RecordRuleHistory(ctx, tx, updated.ID, tc.ActorUserID, "rollback", updated); err != nil {
		return nil, err
	}

	_ = s.audit.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      "routing.rule.rollback",
		Module:      "routing",
		EntityType:  "routing_rule",
		EntityID:    updated.ID.String(),
		BeforeValue: audit.RedactValue(current),
		AfterValue:  audit.RedactValue(updated),
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	go TryReload(context.Background(), s.pool)
	return updated, nil
}

// -----------------------------------------------------------------------------
// CSV / JSON Exports (Formula-Injection Safe)
// -----------------------------------------------------------------------------

func sanitizeCSVCell(s string) string {
	if len(s) == 0 {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	default:
		return s
	}
}

func (s *Service) ExportPlansCSV(ctx context.Context, tc *tenant.Context) ([]byte, error) {
	if err := s.enforcePlatform(tc, "routing.plan.view"); err != nil {
		return nil, err
	}
	plans, _, err := s.repo.ListPlans(ctx, "", 1, 10000)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"ID", "Name", "Code", "Description", "Strategy", "Status", "Version", "Created At"})
	for _, p := range plans {
		_ = w.Write([]string{
			p.ID.String(),
			sanitizeCSVCell(p.Name),
			sanitizeCSVCell(p.Code),
			sanitizeCSVCell(p.Description),
			string(p.Strategy),
			string(p.Status),
			strconv.Itoa(p.Version),
			p.CreatedAt.Format(time.RFC3339),
		})
	}
	w.Flush()
	return buf.Bytes(), nil
}

func (s *Service) ExportAssignmentsCSV(ctx context.Context, tc *tenant.Context) ([]byte, error) {
	if err := s.enforcePlatform(tc, "routing.assignment.view"); err != nil {
		return nil, err
	}
	assignments, _, err := s.repo.ListAssignments(ctx, nil, 1, 10000)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"ID", "Customer Name", "Customer Org ID", "Plan Name", "Country Name", "Priority", "Status", "Version"})
	for _, a := range assignments {
		_ = w.Write([]string{
			a.ID.String(),
			sanitizeCSVCell(a.CustomerName),
			a.CustomerOrgID.String(),
			sanitizeCSVCell(a.PlanName),
			sanitizeCSVCell(a.CountryName),
			strconv.Itoa(a.Priority),
			a.Status,
			strconv.Itoa(a.Version),
		})
	}
	w.Flush()
	return buf.Bytes(), nil
}

func (s *Service) ExportRulesCSV(ctx context.Context, tc *tenant.Context) ([]byte, error) {
	if err := s.enforcePlatform(tc, "routing.rule.view"); err != nil {
		return nil, err
	}
	rules, _, err := s.repo.ListRules(ctx, "", 1, 10000)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"ID", "Name", "Priority", "Action Type", "Status", "Block Reason", "Version"})
	for _, r := range rules {
		_ = w.Write([]string{
			r.ID.String(),
			sanitizeCSVCell(r.Name),
			strconv.Itoa(r.Priority),
			r.ActionType,
			r.Status,
			sanitizeCSVCell(r.ActionBlockReason),
			strconv.Itoa(r.Version),
		})
	}
	w.Flush()
	return buf.Bytes(), nil
}
