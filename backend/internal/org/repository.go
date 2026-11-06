package org

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

type Repository interface {
	GetByID(ctx context.Context, id uuid.UUID) (*Organization, error)
	GetPlatformOrg(ctx context.Context) (*Organization, error)
	List(ctx context.Context, tc *tenant.Context, f OrgFilter) (OrgListResult, error)
	Create(ctx context.Context, tx pgx.Tx, o Organization) (*Organization, error)
	Update(ctx context.Context, tx pgx.Tx, id uuid.UUID, name string) error
	SetStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string) error
	RevokeSessionsForOrgTree(ctx context.Context, tx pgx.Tx, orgPath string, reason string) error
}

type pgRepository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

func (r *pgRepository) GetByID(ctx context.Context, id uuid.UUID) (*Organization, error) {
	query := `SELECT id, name, type, parent_id, path, status, created_at, updated_at FROM organizations WHERE id = $1`
	var o Organization
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&o.ID, &o.Name, &o.Type, &o.ParentID, &o.Path, &o.Status, &o.CreatedAt, &o.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func (r *pgRepository) GetPlatformOrg(ctx context.Context) (*Organization, error) {
	query := `SELECT id, name, type, parent_id, path, status, created_at, updated_at FROM organizations WHERE type = 'platform' LIMIT 1`
	var o Organization
	err := r.pool.QueryRow(ctx, query).Scan(
		&o.ID, &o.Name, &o.Type, &o.ParentID, &o.Path, &o.Status, &o.CreatedAt, &o.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func (r *pgRepository) List(ctx context.Context, tc *tenant.Context, f OrgFilter) (OrgListResult, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 100 {
		f.PageSize = 25
	}

	var where []string
	var args []any
	argIdx := 1

	// Mandatory Tenant Boundary:
	if !tc.IsPlatformAdmin {
		where = append(where, fmt.Sprintf("path LIKE $%d || '%%'", argIdx))
		args = append(args, tc.ActorOrgPath)
		argIdx++
	}

	if f.Type != "" {
		where = append(where, fmt.Sprintf("type = $%d", argIdx))
		args = append(args, f.Type)
		argIdx++
	}
	if f.Status != "" {
		where = append(where, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, f.Status)
		argIdx++
	}
	if f.Search != "" {
		where = append(where, fmt.Sprintf("name ILIKE $%d", argIdx))
		args = append(args, "%"+f.Search+"%")
		argIdx++
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	var total int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM organizations %s", whereClause)
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return OrgListResult{}, fmt.Errorf("count organizations: %w", err)
	}

	offset := (f.Page - 1) * f.PageSize
	query := fmt.Sprintf(`
		SELECT id, name, type, parent_id, path, status, created_at, updated_at
		FROM organizations
		%s
		ORDER BY path ASC, created_at ASC
		LIMIT $%d OFFSET $%d`, whereClause, argIdx, argIdx+1)

	args = append(args, f.PageSize, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return OrgListResult{}, fmt.Errorf("list organizations: %w", err)
	}
	defer rows.Close()

	var items []Organization
	for rows.Next() {
		var o Organization
		if err := rows.Scan(
			&o.ID, &o.Name, &o.Type, &o.ParentID, &o.Path, &o.Status, &o.CreatedAt, &o.UpdatedAt,
		); err != nil {
			return OrgListResult{}, err
		}
		items = append(items, o)
	}

	return OrgListResult{
		Items:      items,
		TotalCount: total,
		Page:       f.Page,
		PageSize:   f.PageSize,
	}, nil
}

func (r *pgRepository) Create(ctx context.Context, tx pgx.Tx, o Organization) (*Organization, error) {
	query := `
		INSERT INTO organizations (id, name, type, parent_id, path, status, created_at, updated_at)
		VALUES (COALESCE(NULLIF($1, '00000000-0000-0000-0000-000000000000'::uuid), gen_random_uuid()), $2, $3, $4, $5, $6, now(), now())
		RETURNING id, name, type, parent_id, path, status, created_at, updated_at`

	var created Organization
	var err error
	if tx != nil {
		err = tx.QueryRow(ctx, query, o.ID, o.Name, o.Type, o.ParentID, o.Path, o.Status).Scan(
			&created.ID, &created.Name, &created.Type, &created.ParentID, &created.Path, &created.Status, &created.CreatedAt, &created.UpdatedAt,
		)
	} else {
		err = r.pool.QueryRow(ctx, query, o.ID, o.Name, o.Type, o.ParentID, o.Path, o.Status).Scan(
			&created.ID, &created.Name, &created.Type, &created.ParentID, &created.Path, &created.Status, &created.CreatedAt, &created.UpdatedAt,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("insert organization: %w", err)
	}
	return &created, nil
}

func (r *pgRepository) Update(ctx context.Context, tx pgx.Tx, id uuid.UUID, name string) error {
	query := `UPDATE organizations SET name = $1, updated_at = now() WHERE id = $2`
	if tx != nil {
		_, err := tx.Exec(ctx, query, name, id)
		return err
	}
	_, err := r.pool.Exec(ctx, query, name, id)
	return err
}

func (r *pgRepository) SetStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string) error {
	query := `UPDATE organizations SET status = $1, updated_at = now() WHERE id = $2`
	if tx != nil {
		_, err := tx.Exec(ctx, query, status, id)
		return err
	}
	_, err := r.pool.Exec(ctx, query, status, id)
	return err
}

// RevokeSessionsForOrgTree invalidates all active sessions for any user belonging to this organization or any descendant.
func (r *pgRepository) RevokeSessionsForOrgTree(ctx context.Context, tx pgx.Tx, orgPath string, reason string) error {
	query := `
		UPDATE sessions
		SET revoked_at = now(), revoked_reason = $1
		WHERE revoked_at IS NULL
		  AND organization_id IN (SELECT id FROM organizations WHERE path LIKE $2 || '%')`

	if tx != nil {
		_, err := tx.Exec(ctx, query, reason, orgPath)
		return err
	}
	_, err := r.pool.Exec(ctx, query, reason, orgPath)
	return err
}
