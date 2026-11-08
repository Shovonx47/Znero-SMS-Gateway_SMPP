package rbac

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

var (
	ErrRoleNotFound           = errors.New("role not found")
	ErrCannotModifySystemRole = errors.New("system roles cannot be modified or deleted")
	ErrRoleHasAssignedUsers   = errors.New("role cannot be deleted while assigned to users")
	ErrPrivilegeEscalation    = errors.New("privilege escalation: cannot grant permissions you do not possess")
	ErrInvalidRoleName        = errors.New("role name must be between 2 and 128 characters")
)

type Service struct {
	repo     Repository
	auditSvc *audit.Service
	pool     *pgxpool.Pool
}

func NewService(repo Repository, auditSvc *audit.Service, pool *pgxpool.Pool) *Service {
	return &Service{
		repo:     repo,
		auditSvc: auditSvc,
		pool:     pool,
	}
}

func (s *Service) ListPermissions(ctx context.Context) ([]Permission, error) {
	return s.repo.ListAllPermissions(ctx)
}

func (s *Service) ListRoles(ctx context.Context, tc *tenant.Context, orgID *uuid.UUID) ([]Role, error) {
	return s.repo.ListRoles(ctx, tc, orgID)
}

func (s *Service) GetRole(ctx context.Context, tc *tenant.Context, id uuid.UUID) (*Role, error) {
	role, err := s.repo.GetRole(ctx, id)
	if err != nil {
		return nil, ErrRoleNotFound
	}
	// Verify tenant scoping: non-platform user can only view roles in their organization tree
	if !tc.IsPlatformAdmin && role.OrganizationID != tc.ActorOrgID {
		// Scoping check
		roles, err := s.repo.ListRoles(ctx, tc, &role.OrganizationID)
		if err != nil || len(roles) == 0 {
			return nil, ErrRoleNotFound
		}
	}
	return role, nil
}

func (s *Service) CreateRole(ctx context.Context, tc *tenant.Context, req CreateRoleRequest) (*Role, error) {
	name := strings.TrimSpace(req.Name)
	if len(name) < 2 || len(name) > 128 {
		return nil, ErrInvalidRoleName
	}

	targetOrgID := tc.ActorOrgID
	if req.OrganizationID != nil && tc.IsPlatformAdmin {
		targetOrgID = *req.OrganizationID
	}

	// Prevent privilege escalation: caller cannot grant permissions they do not possess
	if !tc.IsPlatformAdmin {
		for _, p := range req.Permissions {
			if !tc.Permissions[p] {
				return nil, fmt.Errorf("%w: missing %s", ErrPrivilegeEscalation, p)
			}
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	created, err := s.repo.CreateRole(ctx, tx, Role{
		OrganizationID: targetOrgID,
		Name:           name,
		Description:    strings.TrimSpace(req.Description),
		IsSystem:       false,
	}, req.Permissions)
	if err != nil {
		return nil, err
	}

	// Audit in the same transaction
	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		TargetOrgID: &targetOrgID,
		Action:      "role.role.create",
		Module:      "role",
		EntityType:  "role",
		EntityID:    created.ID.String(),
		AfterValue:  audit.RedactValue(created),
		Reason:      fmt.Sprintf("Created custom role %q", created.Name),
	}); err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	return created, nil
}

func (s *Service) UpdateRole(ctx context.Context, tc *tenant.Context, id uuid.UUID, req UpdateRoleRequest) (*Role, error) {
	existing, err := s.GetRole(ctx, tc, id)
	if err != nil {
		return nil, err
	}
	if existing.IsSystem {
		return nil, ErrCannotModifySystemRole
	}

	name := strings.TrimSpace(req.Name)
	if len(name) < 2 || len(name) > 128 {
		return nil, ErrInvalidRoleName
	}

	// Privilege escalation check
	if !tc.IsPlatformAdmin {
		for _, p := range req.Permissions {
			if !tc.Permissions[p] {
				return nil, fmt.Errorf("%w: missing %s", ErrPrivilegeEscalation, p)
			}
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.repo.UpdateRole(ctx, tx, id, name, strings.TrimSpace(req.Description), req.Permissions); err != nil {
		return nil, err
	}

	updated, err := s.repo.GetRole(ctx, id)
	if err != nil {
		return nil, err
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		TargetOrgID: &existing.OrganizationID,
		Action:      "role.role.edit",
		Module:      "role",
		EntityType:  "role",
		EntityID:    id.String(),
		BeforeValue: audit.RedactValue(existing),
		AfterValue:  audit.RedactValue(updated),
		Reason:      fmt.Sprintf("Updated role %q", name),
	}); err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	return updated, nil
}

func (s *Service) CloneRole(ctx context.Context, tc *tenant.Context, sourceID uuid.UUID, req CloneRoleRequest) (*Role, error) {
	source, err := s.GetRole(ctx, tc, sourceID)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(req.Name)
	if len(name) < 2 || len(name) > 128 {
		name = source.Name + " (Copy)"
	}

	return s.CreateRole(ctx, tc, CreateRoleRequest{
		OrganizationID: &source.OrganizationID,
		Name:           name,
		Description:    source.Description,
		Permissions:    source.Permissions,
	})
}

func (s *Service) DeleteRole(ctx context.Context, tc *tenant.Context, id uuid.UUID) error {
	existing, err := s.GetRole(ctx, tc, id)
	if err != nil {
		return err
	}
	if existing.IsSystem {
		return ErrCannotModifySystemRole
	}

	userCount, err := s.repo.CountUsersWithRole(ctx, id)
	if err != nil {
		return fmt.Errorf("check assigned users: %w", err)
	}
	if userCount > 0 {
		return ErrRoleHasAssignedUsers
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.repo.DeleteRole(ctx, tx, id); err != nil {
		return err
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		TargetOrgID: &existing.OrganizationID,
		Action:      "role.role.delete",
		Module:      "role",
		EntityType:  "role",
		EntityID:    id.String(),
		BeforeValue: audit.RedactValue(existing),
		Reason:      fmt.Sprintf("Deleted role %q", existing.Name),
	}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	return tx.Commit(ctx)
}
