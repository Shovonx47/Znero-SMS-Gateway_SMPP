package audit

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

type Repository interface {
	Insert(ctx context.Context, tx pgx.Tx, e Entry) error
	List(ctx context.Context, tc *tenant.Context, f Filter) (ListResult, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

func (r *pgRepository) Insert(ctx context.Context, tx pgx.Tx, e Entry) error {
	query := `
		INSERT INTO audit_logs (
			id, timestamp, actor_user_id, actor_org_id, target_org_id,
			action, module, entity_type, entity_id, before_value, after_value,
			ip_address, user_agent, request_id, reason
		) VALUES (
			COALESCE(NULLIF($1, '00000000-0000-0000-0000-000000000000'::uuid), gen_random_uuid()),
			COALESCE(NULLIF($2, '0001-01-01T00:00:00Z'::timestamptz), now()),
			$3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
		)`

	var err error
	if tx != nil {
		_, err = tx.Exec(ctx, query,
			e.ID, e.Timestamp, e.ActorUserID, e.ActorOrgID, e.TargetOrgID,
			e.Action, e.Module, e.EntityType, e.EntityID, e.BeforeValue, e.AfterValue,
			e.IPAddress, e.UserAgent, e.RequestID, e.Reason,
		)
	} else {
		_, err = r.pool.Exec(ctx, query,
			e.ID, e.Timestamp, e.ActorUserID, e.ActorOrgID, e.TargetOrgID,
			e.Action, e.Module, e.EntityType, e.EntityID, e.BeforeValue, e.AfterValue,
			e.IPAddress, e.UserAgent, e.RequestID, e.Reason,
		)
	}

	return err
}

func (r *pgRepository) List(ctx context.Context, tc *tenant.Context, f Filter) (ListResult, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 100 {
		f.PageSize = 25
	}

	var where []string
	var args []any
	argIdx := 1

	// Mandatory Tenant Isolation:
	if !tc.IsPlatformAdmin {
		where = append(where, fmt.Sprintf(`(
			target_org_id IN (SELECT id FROM organizations WHERE path LIKE $%d || '%%')
			OR actor_org_id = $%d
		)`, argIdx, argIdx+1))
		args = append(args, tc.ActorOrgPath, tc.ActorOrgID)
		argIdx += 2
	}

	if f.StartDate != nil {
		where = append(where, fmt.Sprintf("timestamp >= $%d", argIdx))
		args = append(args, *f.StartDate)
		argIdx++
	}
	if f.EndDate != nil {
		where = append(where, fmt.Sprintf("timestamp <= $%d", argIdx))
		args = append(args, *f.EndDate)
		argIdx++
	}
	if f.ActorID != nil {
		where = append(where, fmt.Sprintf("actor_user_id = $%d", argIdx))
		args = append(args, *f.ActorID)
		argIdx++
	}
	if f.TargetOrg != nil {
		where = append(where, fmt.Sprintf("target_org_id = $%d", argIdx))
		args = append(args, *f.TargetOrg)
		argIdx++
	}
	if f.Module != "" {
		where = append(where, fmt.Sprintf("module = $%d", argIdx))
		args = append(args, f.Module)
		argIdx++
	}
	if f.Action != "" {
		where = append(where, fmt.Sprintf("action = $%d", argIdx))
		args = append(args, f.Action)
		argIdx++
	}
	if f.EntityType != "" {
		where = append(where, fmt.Sprintf("entity_type = $%d", argIdx))
		args = append(args, f.EntityType)
		argIdx++
	}
	if f.Search != "" {
		where = append(where, fmt.Sprintf("(action ILIKE $%d OR entity_id ILIKE $%d OR reason ILIKE $%d)", argIdx, argIdx, argIdx))
		args = append(args, "%"+f.Search+"%")
		argIdx++
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM audit_logs %s", whereClause)
	var totalCount int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&totalCount); err != nil {
		return ListResult{}, fmt.Errorf("count audit logs: %w", err)
	}

	offset := (f.Page - 1) * f.PageSize
	query := fmt.Sprintf(`
		SELECT id, timestamp, actor_user_id, actor_org_id, target_org_id,
		       action, module, entity_type, entity_id, before_value, after_value,
		       ip_address, user_agent, request_id, reason
		FROM audit_logs
		%s
		ORDER BY timestamp DESC
		LIMIT $%d OFFSET $%d`, whereClause, argIdx, argIdx+1)

	args = append(args, f.PageSize, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return ListResult{}, fmt.Errorf("query audit logs: %w", err)
	}
	defer rows.Close()

	items := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(
			&e.ID, &e.Timestamp, &e.ActorUserID, &e.ActorOrgID, &e.TargetOrgID,
			&e.Action, &e.Module, &e.EntityType, &e.EntityID, &e.BeforeValue, &e.AfterValue,
			&e.IPAddress, &e.UserAgent, &e.RequestID, &e.Reason,
		); err != nil {
			return ListResult{}, fmt.Errorf("scan audit log: %w", err)
		}
		items = append(items, e)
	}

	return ListResult{
		Items:      items,
		TotalCount: totalCount,
		Page:       f.Page,
		PageSize:   f.PageSize,
	}, nil
}
