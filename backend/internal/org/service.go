package org

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/rbac"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

var (
	ErrOrgNotFound        = errors.New("organization not found")
	ErrInvalidOrgType     = errors.New("invalid organization type (allowed: reseller, customer)")
	ErrInvalidOrgName     = errors.New("organization name must be between 2 and 255 characters")
	ErrCannotCreateChild  = errors.New("customer organizations cannot have child organizations")
	ErrParentNotPermitted = errors.New("cannot create child organization under specified parent")
	ErrCannotModifyRoot   = errors.New("platform root organization cannot be suspended or deleted")
)

type Service struct {
	repo     Repository
	rbacRepo rbac.Repository
	auditSvc *audit.Service
	pool     *pgxpool.Pool
}

func NewService(repo Repository, rbacRepo rbac.Repository, auditSvc *audit.Service, pool *pgxpool.Pool) *Service {
	return &Service{
		repo:     repo,
		rbacRepo: rbacRepo,
		auditSvc: auditSvc,
		pool:     pool,
	}
}

func (s *Service) Get(ctx context.Context, tc *tenant.Context, id uuid.UUID) (*Organization, error) {
	org, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, ErrOrgNotFound
	}
	if !tc.CanAccessOrg(org.Path) {
		return nil, ErrOrgNotFound
	}
	return org, nil
}

func (s *Service) List(ctx context.Context, tc *tenant.Context, filter OrgFilter) (OrgListResult, error) {
	return s.repo.List(ctx, tc, filter)
}

func (s *Service) Create(ctx context.Context, tc *tenant.Context, req CreateOrgRequest) (*Organization, error) {
	name := strings.TrimSpace(req.Name)
	if len(name) < 2 || len(name) > 255 {
		return nil, ErrInvalidOrgName
	}

	reqType := strings.ToLower(strings.TrimSpace(req.Type))
	if reqType != "reseller" && reqType != "customer" {
		return nil, ErrInvalidOrgType
	}

	// Customer orgs cannot create children
	if tc.ActorOrgType == "customer" {
		return nil, ErrCannotCreateChild
	}

	// Reseller can only create customer orgs
	if tc.ActorOrgType == "reseller" && reqType != "customer" {
		return nil, errors.New("resellers can only create customer organizations")
	}

	parentID := tc.ActorOrgID
	if req.ParentID != nil && tc.IsPlatformAdmin {
		parentID = *req.ParentID
	}

	parentOrg, err := s.repo.GetByID(ctx, parentID)
	if err != nil || !tc.CanAccessOrg(parentOrg.Path) {
		return nil, ErrParentNotPermitted
	}
	if parentOrg.Type == "customer" {
		return nil, ErrCannotCreateChild
	}

	newID := uuid.New()
	newPath := fmt.Sprintf("%s%s/", parentOrg.Path, newID.String())

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	created, err := s.repo.Create(ctx, tx, Organization{
		ID:       newID,
		Name:     name,
		Type:     reqType,
		ParentID: &parentID,
		Path:     newPath,
		Status:   "active",
	})
	if err != nil {
		return nil, fmt.Errorf("create org: %w", err)
	}

	// Seed system roles for the newly created organization
	if err := s.rbacRepo.SeedSystemRoles(ctx, tx, created.ID, created.Type); err != nil {
		return nil, fmt.Errorf("seed system roles: %w", err)
	}

	// Audit log entry in the same transaction
	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		TargetOrgID: &created.ID,
		Action:      "org.organization.create",
		Module:      "org",
		EntityType:  "organization",
		EntityID:    created.ID.String(),
		AfterValue:  audit.RedactValue(created),
		Reason:      fmt.Sprintf("Created %s organization %q", created.Type, created.Name),
	}); err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	return created, nil
}

func (s *Service) Update(ctx context.Context, tc *tenant.Context, id uuid.UUID, req UpdateOrgRequest) (*Organization, error) {
	org, err := s.Get(ctx, tc, id)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(req.Name)
	if len(name) < 2 || len(name) > 255 {
		return nil, ErrInvalidOrgName
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := s.repo.Update(ctx, tx, id, name); err != nil {
		return nil, fmt.Errorf("update org: %w", err)
	}

	updated, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		TargetOrgID: &id,
		Action:      "org.organization.edit",
		Module:      "org",
		EntityType:  "organization",
		EntityID:    id.String(),
		BeforeValue: audit.RedactValue(org),
		AfterValue:  audit.RedactValue(updated),
		Reason:      fmt.Sprintf("Renamed organization to %q", name),
	}); err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	return updated, nil
}

func (s *Service) Suspend(ctx context.Context, tc *tenant.Context, id uuid.UUID, reason string) error {
	org, err := s.Get(ctx, tc, id)
	if err != nil {
		return err
	}
	if org.Type == "platform" {
		return ErrCannotModifyRoot
	}
	if org.ID == tc.ActorOrgID {
		return errors.New("cannot suspend your own organization")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := s.repo.SetStatus(ctx, tx, id, "suspended"); err != nil {
		return fmt.Errorf("set status: %w", err)
	}

	// Immediately revoke all active sessions for this organization and all its descendants!
	revokeReason := fmt.Sprintf("Organization %s suspended: %s", org.Name, reason)
	if err := s.repo.RevokeSessionsForOrgTree(ctx, tx, org.Path, revokeReason); err != nil {
		return fmt.Errorf("revoke tree sessions: %w", err)
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		TargetOrgID: &id,
		Action:      "org.organization.suspend",
		Module:      "org",
		EntityType:  "organization",
		EntityID:    id.String(),
		BeforeValue: audit.RedactValue(org),
		Reason:      revokeReason,
	}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	return tx.Commit(ctx)
}

func (s *Service) Activate(ctx context.Context, tc *tenant.Context, id uuid.UUID) error {
	org, err := s.Get(ctx, tc, id)
	if err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := s.repo.SetStatus(ctx, tx, id, "active"); err != nil {
		return fmt.Errorf("set status: %w", err)
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		TargetOrgID: &id,
		Action:      "org.organization.activate",
		Module:      "org",
		EntityType:  "organization",
		EntityID:    id.String(),
		BeforeValue: audit.RedactValue(org),
		Reason:      fmt.Sprintf("Reactivated organization %q", org.Name),
	}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	return tx.Commit(ctx)
}
