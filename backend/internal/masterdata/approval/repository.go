package approval

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrChangeRequestNotFound = errors.New("change request not found")
	ErrInvalidStatus         = errors.New("change request is not in pending status")
)

type Repository interface {
	Create(ctx context.Context, tx pgx.Tx, cr ChangeRequest) (*ChangeRequest, error)
	GetByID(ctx context.Context, id uuid.UUID) (*ChangeRequest, error)
	List(ctx context.Context, filter ChangeRequestFilter) ([]ChangeRequest, int, error)
	Approve(ctx context.Context, tx pgx.Tx, id uuid.UUID, checkerID uuid.UUID) error
	Reject(ctx context.Context, tx pgx.Tx, id uuid.UUID, checkerID uuid.UUID, reason string) error
	Cancel(ctx context.Context, tx pgx.Tx, id uuid.UUID, makerID uuid.UUID) error
}

type pgRepository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

func (r *pgRepository) Create(ctx context.Context, tx pgx.Tx, cr ChangeRequest) (*ChangeRequest, error) {
	query := `
		INSERT INTO change_requests (
			id, entity_type, entity_id, operation, proposed_data, current_data,
			status, maker_user_id, made_at, entity_version, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, 'pending', $7, $8, $9, $10, $10
		) RETURNING id, entity_type, entity_id, operation, proposed_data, current_data,
		            status, maker_user_id, made_at, checker_user_id, checked_at,
		            reject_reason, entity_version, created_at, updated_at
	`
	id := cr.ID
	if id == uuid.Nil {
		id = uuid.New()
	}
	now := time.Now()

	var out ChangeRequest
	var execer interface {
		QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	} = r.pool
	if tx != nil {
		execer = tx
	}

	err := execer.QueryRow(ctx, query,
		id, cr.EntityType, cr.EntityID, cr.Operation, cr.ProposedData, cr.CurrentData,
		cr.MakerUserID, now, cr.EntityVersion, now,
	).Scan(
		&out.ID, &out.EntityType, &out.EntityID, &out.Operation, &out.ProposedData, &out.CurrentData,
		&out.Status, &out.MakerUserID, &out.MadeAt, &out.CheckerUserID, &out.CheckedAt,
		&out.RejectReason, &out.EntityVersion, &out.CreatedAt, &out.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create change request: %w", err)
	}
	return &out, nil
}

func (r *pgRepository) GetByID(ctx context.Context, id uuid.UUID) (*ChangeRequest, error) {
	query := `
		SELECT cr.id, cr.entity_type, cr.entity_id, cr.operation, cr.proposed_data, cr.current_data,
		       cr.status, cr.maker_user_id, COALESCE(mu.email, '') AS maker_email, cr.made_at,
		       cr.checker_user_id, COALESCE(cu.email, '') AS checker_email, cr.checked_at,
		       cr.reject_reason, cr.entity_version, cr.created_at, cr.updated_at
		FROM change_requests cr
		LEFT JOIN users mu ON cr.maker_user_id = mu.id
		LEFT JOIN users cu ON cr.checker_user_id = cu.id
		WHERE cr.id = $1
	`
	var out ChangeRequest
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&out.ID, &out.EntityType, &out.EntityID, &out.Operation, &out.ProposedData, &out.CurrentData,
		&out.Status, &out.MakerUserID, &out.MakerEmail, &out.MadeAt,
		&out.CheckerUserID, &out.CheckerEmail, &out.CheckedAt,
		&out.RejectReason, &out.EntityVersion, &out.CreatedAt, &out.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrChangeRequestNotFound
		}
		return nil, fmt.Errorf("get change request by id: %w", err)
	}
	return &out, nil
}

func (r *pgRepository) List(ctx context.Context, filter ChangeRequestFilter) ([]ChangeRequest, int, error) {
	var where []string
	var args []any
	argIdx := 1

	if filter.EntityType != "" {
		where = append(where, fmt.Sprintf("cr.entity_type = $%d", argIdx))
		args = append(args, filter.EntityType)
		argIdx++
	}
	if filter.Status != "" {
		where = append(where, fmt.Sprintf("cr.status = $%d", argIdx))
		args = append(args, filter.Status)
		argIdx++
	}
	if filter.MakerID != nil {
		where = append(where, fmt.Sprintf("cr.maker_user_id = $%d", argIdx))
		args = append(args, *filter.MakerID)
		argIdx++
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + where[0]
		for i := 1; i < len(where); i++ {
			whereClause += " AND " + where[i]
		}
	}

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM change_requests cr %s", whereClause)
	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count change requests: %w", err)
	}

	page := filter.Page
	if page < 1 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize < 1 || pageSize > 100 {
		pageSize = 25
	}
	offset := (page - 1) * pageSize

	query := fmt.Sprintf(`
		SELECT cr.id, cr.entity_type, cr.entity_id, cr.operation, cr.proposed_data, cr.current_data,
		       cr.status, cr.maker_user_id, COALESCE(mu.email, '') AS maker_email, cr.made_at,
		       cr.checker_user_id, COALESCE(cu.email, '') AS checker_email, cr.checked_at,
		       cr.reject_reason, cr.entity_version, cr.created_at, cr.updated_at
		FROM change_requests cr
		LEFT JOIN users mu ON cr.maker_user_id = mu.id
		LEFT JOIN users cu ON cr.checker_user_id = cu.id
		%s
		ORDER BY cr.created_at DESC
		LIMIT $%d OFFSET $%d
	`, whereClause, argIdx, argIdx+1)

	args = append(args, pageSize, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list change requests: %w", err)
	}
	defer rows.Close()

	var items []ChangeRequest
	for rows.Next() {
		var item ChangeRequest
		if err := rows.Scan(
			&item.ID, &item.EntityType, &item.EntityID, &item.Operation, &item.ProposedData, &item.CurrentData,
			&item.Status, &item.MakerUserID, &item.MakerEmail, &item.MadeAt,
			&item.CheckerUserID, &item.CheckerEmail, &item.CheckedAt,
			&item.RejectReason, &item.EntityVersion, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan change request: %w", err)
		}
		items = append(items, item)
	}

	return items, total, nil
}

func (r *pgRepository) Approve(ctx context.Context, tx pgx.Tx, id uuid.UUID, checkerID uuid.UUID) error {
	now := time.Now()
	res, err := tx.Exec(ctx, `
		UPDATE change_requests
		SET status = 'approved', checker_user_id = $1, checked_at = $2, updated_at = $2
		WHERE id = $3 AND status = 'pending'
	`, checkerID, now, id)
	if err != nil {
		return fmt.Errorf("approve change request: %w", err)
	}
	if res.RowsAffected() == 0 {
		return ErrInvalidStatus
	}
	return nil
}

func (r *pgRepository) Reject(ctx context.Context, tx pgx.Tx, id uuid.UUID, checkerID uuid.UUID, reason string) error {
	now := time.Now()
	res, err := tx.Exec(ctx, `
		UPDATE change_requests
		SET status = 'rejected', checker_user_id = $1, checked_at = $2, reject_reason = $3, updated_at = $2
		WHERE id = $4 AND status = 'pending'
	`, checkerID, now, reason, id)
	if err != nil {
		return fmt.Errorf("reject change request: %w", err)
	}
	if res.RowsAffected() == 0 {
		return ErrInvalidStatus
	}
	return nil
}

func (r *pgRepository) Cancel(ctx context.Context, tx pgx.Tx, id uuid.UUID, makerID uuid.UUID) error {
	now := time.Now()
	res, err := tx.Exec(ctx, `
		UPDATE change_requests
		SET status = 'cancelled', updated_at = $1
		WHERE id = $2 AND maker_user_id = $3 AND status = 'pending'
	`, now, id, makerID)
	if err != nil {
		return fmt.Errorf("cancel change request: %w", err)
	}
	if res.RowsAffected() == 0 {
		return ErrInvalidStatus
	}
	return nil
}
