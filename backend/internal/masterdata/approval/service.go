package approval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

var (
	ErrMakerCannotApproveOwnChange = errors.New("maker cannot approve their own change request")
	ErrApplierNotRegistered        = errors.New("no change applier registered for entity type")
	ErrEmptyRejectReason           = errors.New("rejection reason is required")
	ErrNotAuthorizedToApprove      = errors.New("not authorized to review this type of change request")
)

type ChangeApplier interface {
	ApplyChange(ctx context.Context, tx pgx.Tx, cr *ChangeRequest) error
}

type Service struct {
	repo     Repository
	auditSvc *audit.Service
	pool     *pgxpool.Pool
	mu       sync.RWMutex
	appliers map[string]ChangeApplier
	// approvePerms maps an entity type to the permission a checker must hold to review it.
	approvePerms map[string]string
}

func NewService(repo Repository, auditSvc *audit.Service, pool *pgxpool.Pool) *Service {
	return &Service{
		repo:     repo,
		auditSvc: auditSvc,
		pool:     pool,
		appliers: make(map[string]ChangeApplier),

		approvePerms: make(map[string]string),
	}
}

// RegisterApprovePermission declares which permission is required to approve or reject change
// requests of the given entity type. Entity types without a registered permission cannot be
// reviewed by anyone (default deny), so every new entity type must register one.
func (s *Service) RegisterApprovePermission(entityType, permission string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approvePerms[entityType] = permission
}

// authorizeReview enforces who may act as checker for cr: a platform-organization user holding
// the entity type's approve permission. The maker rule is enforced by the callers.
func (s *Service) authorizeReview(tc *tenant.Context, cr *ChangeRequest) error {
	s.mu.RLock()
	perm, ok := s.approvePerms[cr.EntityType]
	s.mu.RUnlock()
	if !ok || !tc.IsPlatformOrg() {
		return ErrNotAuthorizedToApprove
	}
	for _, p := range strings.Split(perm, ",") {
		p = strings.TrimSpace(p)
		if tc.HasPermission(p) {
			return nil
		}
	}
	return ErrNotAuthorizedToApprove
}

func (s *Service) RegisterApplier(entityType string, applier ChangeApplier) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appliers[entityType] = applier
}

func (s *Service) SubmitChangeRequest(ctx context.Context, tx pgx.Tx, cr ChangeRequest) (*ChangeRequest, error) {
	created, err := s.repo.Create(ctx, tx, cr)
	if err != nil {
		return nil, err
	}

	entityIDStr := ""
	if created.EntityID != nil {
		entityIDStr = created.EntityID.String()
	}

	_ = s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &created.MakerUserID,
		Action:      fmt.Sprintf("%s.change_request.submit", created.EntityType),
		Module:      created.EntityType,
		EntityType:  "change_request",
		EntityID:    created.ID.String(),
		AfterValue:  audit.RedactValue(created),
		Reason:      fmt.Sprintf("Submitted change request (%s %s %s)", created.Operation, created.EntityType, entityIDStr),
	})

	return created, nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*ChangeRequest, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *Service) List(ctx context.Context, filter ChangeRequestFilter) (ListResult, error) {
	items, total, err := s.repo.List(ctx, filter)
	if err != nil {
		return ListResult{}, err
	}
	return ListResult{
		Items:      items,
		TotalCount: total,
		Page:       filter.Page,
		PageSize:   filter.PageSize,
	}, nil
}

func (s *Service) Approve(ctx context.Context, tc *tenant.Context, id uuid.UUID, reason string) error {
	cr, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}

	if err := s.authorizeReview(tc, cr); err != nil {
		return err
	}

	// Rule: Maker cannot approve their own change
	if cr.MakerUserID == tc.ActorUserID {
		return ErrMakerCannotApproveOwnChange
	}

	if cr.Status != "pending" {
		return ErrInvalidStatus
	}

	// The checker is the actor recorded in the entity's version history for the applied change.
	checker := tc.ActorUserID
	cr.CheckerUserID = &checker

	s.mu.RLock()
	applier, ok := s.appliers[cr.EntityType]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrApplierNotRegistered, cr.EntityType)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Apply change to the underlying entity
	if err := applier.ApplyChange(ctx, tx, cr); err != nil {
		return fmt.Errorf("apply change: %w", err)
	}

	// Update change request to approved
	if err := s.repo.Approve(ctx, tx, id, tc.ActorUserID); err != nil {
		return err
	}

	entityIDStr := ""
	if cr.EntityID != nil {
		entityIDStr = cr.EntityID.String()
	}

	auditReason := reason
	if auditReason == "" {
		auditReason = fmt.Sprintf("Approved %s change request for %s %s", cr.Operation, cr.EntityType, entityIDStr)
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      fmt.Sprintf("%s.change_request.approve", cr.EntityType),
		Module:      cr.EntityType,
		EntityType:  "change_request",
		EntityID:    cr.ID.String(),
		Reason:      auditReason,
	}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	return tx.Commit(ctx)
}

func (s *Service) Reject(ctx context.Context, tc *tenant.Context, id uuid.UUID, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ErrEmptyRejectReason
	}

	cr, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}

	if err := s.authorizeReview(tc, cr); err != nil {
		return err
	}
	if cr.MakerUserID == tc.ActorUserID {
		return ErrMakerCannotApproveOwnChange
	}

	if cr.Status != "pending" {
		return ErrInvalidStatus
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := s.repo.Reject(ctx, tx, id, tc.ActorUserID, reason); err != nil {
		return err
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      fmt.Sprintf("%s.change_request.reject", cr.EntityType),
		Module:      cr.EntityType,
		EntityType:  "change_request",
		EntityID:    cr.ID.String(),
		Reason:      fmt.Sprintf("Rejected change request: %s", reason),
	}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	return tx.Commit(ctx)
}

func (s *Service) Cancel(ctx context.Context, tc *tenant.Context, id uuid.UUID) error {
	cr, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}

	if cr.MakerUserID != tc.ActorUserID && !tc.IsPlatformAdmin {
		return errors.New("only the maker or a platform admin can cancel a pending change request")
	}

	if cr.Status != "pending" {
		return ErrInvalidStatus
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := s.repo.Cancel(ctx, tx, id, cr.MakerUserID); err != nil {
		return err
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		Action:      fmt.Sprintf("%s.change_request.cancel", cr.EntityType),
		Module:      cr.EntityType,
		EntityType:  "change_request",
		EntityID:    cr.ID.String(),
		Reason:      "Cancelled change request",
	}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	return tx.Commit(ctx)
}
