package rbac

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

type Repository interface {
	GetRole(ctx context.Context, id uuid.UUID) (*Role, error)
	ListRoles(ctx context.Context, tc *tenant.Context, orgID *uuid.UUID) ([]Role, error)
	CreateRole(ctx context.Context, tx pgx.Tx, r Role, permissions []string) (*Role, error)
	UpdateRole(ctx context.Context, tx pgx.Tx, id uuid.UUID, name, description string, permissions []string) error
	DeleteRole(ctx context.Context, tx pgx.Tx, id uuid.UUID) error
	GetRolePermissions(ctx context.Context, roleID uuid.UUID) ([]string, error)
	GetUserPermissions(ctx context.Context, userID uuid.UUID) ([]string, error)
	ListAllPermissions(ctx context.Context) ([]Permission, error)
	SeedSystemRoles(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, orgType string) error
	CountUsersWithRole(ctx context.Context, roleID uuid.UUID) (int, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

func (r *pgRepository) GetRole(ctx context.Context, id uuid.UUID) (*Role, error) {
	query := `
		SELECT r.id, r.organization_id, r.name, r.description, r.is_system, r.created_at, r.updated_at,
		       COALESCE(array_agg(rp.permission_code) FILTER (WHERE rp.permission_code IS NOT NULL), '{}') AS permissions
		FROM roles r
		LEFT JOIN role_permissions rp ON r.id = rp.role_id
		WHERE r.id = $1
		GROUP BY r.id`

	var role Role
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&role.ID, &role.OrganizationID, &role.Name, &role.Description,
		&role.IsSystem, &role.CreatedAt, &role.UpdatedAt, &role.Permissions,
	)
	if err != nil {
		return nil, err
	}
	return &role, nil
}

func (r *pgRepository) ListRoles(ctx context.Context, tc *tenant.Context, orgID *uuid.UUID) ([]Role, error) {
	var where []string
	var args []any
	argIdx := 1

	if !tc.IsPlatformAdmin {
		where = append(where, fmt.Sprintf(`r.organization_id IN (
			SELECT id FROM organizations WHERE path LIKE $%d || '%%'
		)`, argIdx))
		args = append(args, tc.ActorOrgPath)
		argIdx++
	}

	if orgID != nil {
		where = append(where, fmt.Sprintf("r.organization_id = $%d", argIdx))
		args = append(args, *orgID)
		argIdx++
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + where[0]
		for i := 1; i < len(where); i++ {
			whereClause += " AND " + where[i]
		}
	}

	query := fmt.Sprintf(`
		SELECT r.id, r.organization_id, r.name, r.description, r.is_system, r.created_at, r.updated_at,
		       COALESCE(array_agg(rp.permission_code) FILTER (WHERE rp.permission_code IS NOT NULL), '{}') AS permissions
		FROM roles r
		LEFT JOIN role_permissions rp ON r.id = rp.role_id
		%s
		GROUP BY r.id
		ORDER BY r.is_system DESC, r.name ASC`, whereClause)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	defer rows.Close()

	var roles []Role
	for rows.Next() {
		var role Role
		if err := rows.Scan(
			&role.ID, &role.OrganizationID, &role.Name, &role.Description,
			&role.IsSystem, &role.CreatedAt, &role.UpdatedAt, &role.Permissions,
		); err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, nil
}

func (r *pgRepository) CreateRole(ctx context.Context, tx pgx.Tx, role Role, permissions []string) (*Role, error) {
	query := `
		INSERT INTO roles (id, organization_id, name, description, is_system, created_at, updated_at)
		VALUES (COALESCE(NULLIF($1, '00000000-0000-0000-0000-000000000000'::uuid), gen_random_uuid()), $2, $3, $4, $5, now(), now())
		RETURNING id, organization_id, name, description, is_system, created_at, updated_at`

	var created Role
	err := tx.QueryRow(ctx, query, role.ID, role.OrganizationID, role.Name, role.Description, role.IsSystem).Scan(
		&created.ID, &created.OrganizationID, &created.Name, &created.Description,
		&created.IsSystem, &created.CreatedAt, &created.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert role: %w", err)
	}

	for _, perm := range permissions {
		_, err := tx.Exec(ctx, `INSERT INTO role_permissions (role_id, permission_code) VALUES ($1, $2) ON CONFLICT DO NOTHING`, created.ID, perm)
		if err != nil {
			return nil, fmt.Errorf("insert role permission %q: %w", perm, err)
		}
	}
	created.Permissions = permissions
	return &created, nil
}

func (r *pgRepository) UpdateRole(ctx context.Context, tx pgx.Tx, id uuid.UUID, name, description string, permissions []string) error {
	query := `UPDATE roles SET name = $1, description = $2, updated_at = now() WHERE id = $3 AND is_system = false`
	tag, err := tx.Exec(ctx, query, name, description, id)
	if err != nil {
		return fmt.Errorf("update role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("role not found or is a protected system role")
	}

	// Replace permissions
	if _, err := tx.Exec(ctx, `DELETE FROM role_permissions WHERE role_id = $1`, id); err != nil {
		return fmt.Errorf("delete old permissions: %w", err)
	}
	for _, perm := range permissions {
		_, err := tx.Exec(ctx, `INSERT INTO role_permissions (role_id, permission_code) VALUES ($1, $2)`, id, perm)
		if err != nil {
			return fmt.Errorf("insert role permission %q: %w", perm, err)
		}
	}
	return nil
}

func (r *pgRepository) DeleteRole(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	query := `DELETE FROM roles WHERE id = $1 AND is_system = false`
	tag, err := tx.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("delete role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("role not found or is a protected system role")
	}
	return nil
}

func (r *pgRepository) CountUsersWithRole(ctx context.Context, roleID uuid.UUID) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_roles WHERE role_id = $1`, roleID).Scan(&count)
	return count, err
}

func (r *pgRepository) GetRolePermissions(ctx context.Context, roleID uuid.UUID) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT permission_code FROM role_permissions WHERE role_id = $1`, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var perms []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		perms = append(perms, p)
	}
	return perms, nil
}

func (r *pgRepository) GetUserPermissions(ctx context.Context, userID uuid.UUID) ([]string, error) {
	query := `
		SELECT DISTINCT rp.permission_code
		FROM user_roles ur
		JOIN role_permissions rp ON ur.role_id = rp.role_id
		WHERE ur.user_id = $1`

	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var perms []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		perms = append(perms, p)
	}
	return perms, nil
}

func (r *pgRepository) ListAllPermissions(ctx context.Context) ([]Permission, error) {
	rows, err := r.pool.Query(ctx, `SELECT code, module, resource, action, description, created_at FROM permissions ORDER BY module, resource, action`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var perms []Permission
	for rows.Next() {
		var p Permission
		if err := rows.Scan(&p.Code, &p.Module, &p.Resource, &p.Action, &p.Description, &p.CreatedAt); err != nil {
			return nil, err
		}
		perms = append(perms, p)
	}
	return perms, nil
}

// SeedSystemRoles creates standard system templates for an organization:
// - Super Admin (Platform only): ALL permissions
// - Org Admin: All non-platform admin permissions
// - Staff: View org, view/invite users, view audit
// - Read-only: View only
func (r *pgRepository) SeedSystemRoles(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, orgType string) error {
	allPerms, err := r.ListAllPermissions(ctx)
	if err != nil {
		return err
	}

	var allCodes []string
	var orgAdminCodes []string
	var staffCodes []string
	var readOnlyCodes []string

	existing := make(map[string]bool, len(allPerms))
	for _, p := range allPerms {
		existing[p.Code] = true
		// Platform-only permissions (master data writes, providers, imports, rules) never go into
		// reseller/customer role templates; only the platform organization receives them.
		if orgType != "platform" && IsPlatformOnlyPermission(p.Code) {
			continue
		}
		if orgType == "customer" && IsForbiddenForCustomerOrg(p.Code) {
			continue
		}
		allCodes = append(allCodes, p.Code)

		// Org Admin gets org/user/role/audit/session management
		if p.Code != "system.info.view" {
			orgAdminCodes = append(orgAdminCodes, p.Code)
		}

		if p.Action == "view" || p.Action == "invite" {
			staffCodes = append(staffCodes, p.Code)
		}

		if p.Action == "view" {
			readOnlyCodes = append(readOnlyCodes, p.Code)
		}
	}

	// If platform org, create "Super Admin" role
	if orgType == "platform" {
		_, err := r.CreateRole(ctx, tx, Role{
			OrganizationID: orgID,
			Name:           "Super Admin",
			Description:    "Full platform administrative access to all organizations, users, and resources",
			IsSystem:       true,
		}, allCodes)
		if err != nil {
			return err
		}
	}

	// Platform maker/checker templates for master data and routing (no override permission on purpose).
	if orgType == "platform" {
		for _, t := range []struct {
			name, desc string
			codes      []string
		}{
			{"Master Data Editor", "Maker: edits master data; every change is routed to a pending change request", platformMasterDataEditorCodes},
			{"Master Data Approver", "Checker: reviews and approves or rejects master data change requests", platformMasterDataApproverCodes},
			{"Routing Maker", "Maker: creates and edits route plans, entries, customer assignments, and routing rules; changes require approval", platformRoutingMakerCodes},
			{"Routing Checker", "Checker: reviews and approves or rejects routing change requests and manages emergency kill switch", platformRoutingCheckerCodes},
		} {
			var codes []string
			for _, code := range t.codes {
				if existing[code] {
					codes = append(codes, code)
				}
			}
			if _, err := r.CreateRole(ctx, tx, Role{OrganizationID: orgID, Name: t.name, Description: t.desc, IsSystem: true}, codes); err != nil {
				return err
			}
		}
	}

	// Standard Org Admin
	_, err = r.CreateRole(ctx, tx, Role{
		OrganizationID: orgID,
		Name:           "Org Admin",
		Description:    "Full administrative access within this organization",
		IsSystem:       true,
	}, orgAdminCodes)
	if err != nil {
		return err
	}

	// Staff
	_, err = r.CreateRole(ctx, tx, Role{
		OrganizationID: orgID,
		Name:           "Staff",
		Description:    "Standard staff operations: view resources and invite users",
		IsSystem:       true,
	}, staffCodes)
	if err != nil {
		return err
	}

	// Read-only
	_, err = r.CreateRole(ctx, tx, Role{
		OrganizationID: orgID,
		Name:           "Read-only",
		Description:    "Read-only access to organization resources",
		IsSystem:       true,
	}, readOnlyCodes)
	if err != nil {
		return err
	}

	return nil
}
