package prefix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrPrefixNotFound  = errors.New("number prefix not found")
	ErrVersionNotFound = errors.New("target version snapshot not found in history")
)

type Repository interface {
	Create(ctx context.Context, tx pgx.Tx, np NumberPrefix, actorID *uuid.UUID, reason string) (*NumberPrefix, error)
	GetByID(ctx context.Context, id uuid.UUID) (*NumberPrefix, error)
	List(ctx context.Context, f Filter) ([]NumberPrefix, int, error)
	ListAllActive(ctx context.Context) ([]NumberPrefix, error)
	Update(ctx context.Context, tx pgx.Tx, np NumberPrefix, actorID *uuid.UUID, reason string) (*NumberPrefix, error)
	SetStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string, actorID *uuid.UUID, reason string) (*NumberPrefix, error)
	GetHistory(ctx context.Context, prefixID uuid.UUID) ([]HistoryEntry, error)
	Rollback(ctx context.Context, tx pgx.Tx, id uuid.UUID, targetVersion int, actorID *uuid.UUID, reason string) (*NumberPrefix, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

func (r *pgRepository) Create(ctx context.Context, tx pgx.Tx, np NumberPrefix, actorID *uuid.UUID, reason string) (*NumberPrefix, error) {
	id := np.ID
	if id == uuid.Nil {
		id = uuid.New()
	}
	now := time.Now()
	version := 1
	status := np.Status
	if status == "" {
		status = "active"
	}
	numType := np.NumberType
	if numType == "" {
		numType = "mobile"
	}
	priority := np.Priority
	if priority <= 0 {
		priority = 100
	}

	query := `
		INSERT INTO number_prefixes (
			id, prefix, country_id, operator_id, number_type, priority,
			effective_from, effective_to, data_source, status,
			version, created_at, updated_at, created_by, updated_by
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $12, $13, $13
		) RETURNING id, prefix, country_id, operator_id, number_type, priority,
		            effective_from, effective_to, data_source, status,
		            version, created_at, updated_at, created_by, updated_by
	`

	var execer interface {
		QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	} = r.pool
	if tx != nil {
		execer = tx
	}

	var out NumberPrefix
	err := execer.QueryRow(ctx, query,
		id, np.Prefix, np.CountryID, np.OperatorID, numType, priority,
		np.EffectiveFrom, np.EffectiveTo, np.DataSource, status,
		version, now, actorID,
	).Scan(
		&out.ID, &out.Prefix, &out.CountryID, &out.OperatorID, &out.NumberType, &out.Priority,
		&out.EffectiveFrom, &out.EffectiveTo, &out.DataSource, &out.Status,
		&out.Version, &out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
	)
	if err != nil {
		return nil, fmt.Errorf("insert number prefix: %w", err)
	}

	snapshotJSON, _ := json.Marshal(out)
	historyQuery := `
		INSERT INTO number_prefixes_history (id, prefix_id, version, snapshot, actor_user_id, changed_at, reason)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
	`
	if tx != nil {
		_, err = tx.Exec(ctx, historyQuery, out.ID, out.Version, snapshotJSON, actorID, now, reason)
	} else {
		_, err = r.pool.Exec(ctx, historyQuery, out.ID, out.Version, snapshotJSON, actorID, now, reason)
	}
	if err != nil {
		return nil, fmt.Errorf("insert prefix history: %w", err)
	}

	return &out, nil
}

func (r *pgRepository) GetByID(ctx context.Context, id uuid.UUID) (*NumberPrefix, error) {
	query := `
		SELECT np.id, np.prefix, np.country_id, c.name AS country_name, c.iso2 AS country_iso2,
		       np.operator_id, COALESCE(o.name, '') AS operator_name,
		       np.number_type, np.priority, np.effective_from, np.effective_to,
		       np.data_source, np.status, np.version, np.created_at, np.updated_at,
		       np.created_by, np.updated_by
		FROM number_prefixes np
		JOIN countries c ON np.country_id = c.id
		LEFT JOIN operators o ON np.operator_id = o.id
		WHERE np.id = $1
	`
	var out NumberPrefix
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&out.ID, &out.Prefix, &out.CountryID, &out.CountryName, &out.CountryISO2,
		&out.OperatorID, &out.OperatorName,
		&out.NumberType, &out.Priority, &out.EffectiveFrom, &out.EffectiveTo,
		&out.DataSource, &out.Status, &out.Version, &out.CreatedAt, &out.UpdatedAt,
		&out.CreatedBy, &out.UpdatedBy,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPrefixNotFound
		}
		return nil, fmt.Errorf("get prefix by id: %w", err)
	}
	return &out, nil
}

func (r *pgRepository) List(ctx context.Context, f Filter) ([]NumberPrefix, int, error) {
	var where []string
	var args []any
	argIdx := 1

	if f.ActiveOnly {
		where = append(where, "np.status = 'active'")
	} else if f.Status != "" {
		where = append(where, fmt.Sprintf("np.status = $%d", argIdx))
		args = append(args, f.Status)
		argIdx++
	} else {
		where = append(where, "np.status != 'archived'")
	}

	if f.CountryID != nil {
		where = append(where, fmt.Sprintf("np.country_id = $%d", argIdx))
		args = append(args, *f.CountryID)
		argIdx++
	}
	if f.OperatorID != nil {
		where = append(where, fmt.Sprintf("np.operator_id = $%d", argIdx))
		args = append(args, *f.OperatorID)
		argIdx++
	}
	if f.NumberType != "" {
		where = append(where, fmt.Sprintf("np.number_type = $%d", argIdx))
		args = append(args, f.NumberType)
		argIdx++
	}
	if f.Search != "" {
		where = append(where, fmt.Sprintf("(np.prefix LIKE $%d || '%%' OR c.name ILIKE '%%' || $%d || '%%' OR o.name ILIKE '%%' || $%d || '%%')", argIdx, argIdx, argIdx))
		args = append(args, f.Search)
		argIdx++
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	countQuery := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM number_prefixes np
		JOIN countries c ON np.country_id = c.id
		LEFT JOIN operators o ON np.operator_id = o.id
		%s
	`, whereClause)
	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count prefixes: %w", err)
	}

	page := f.Page
	if page < 1 {
		page = 1
	}
	pageSize := f.PageSize
	if pageSize < 1 || pageSize > 500 {
		pageSize = 25
	}
	offset := (page - 1) * pageSize

	sortBy := "np.prefix"
	switch f.SortBy {
	case "priority", "created_at", "status", "version":
		sortBy = "np." + f.SortBy
	case "country_name":
		sortBy = "c.name"
	case "operator_name":
		sortBy = "o.name"
	}
	sortDir := "ASC"
	if strings.ToUpper(f.SortDir) == "DESC" {
		sortDir = "DESC"
	}

	query := fmt.Sprintf(`
		SELECT np.id, np.prefix, np.country_id, c.name AS country_name, c.iso2 AS country_iso2,
		       np.operator_id, COALESCE(o.name, '') AS operator_name,
		       np.number_type, np.priority, np.effective_from, np.effective_to,
		       np.data_source, np.status, np.version, np.created_at, np.updated_at,
		       np.created_by, np.updated_by
		FROM number_prefixes np
		JOIN countries c ON np.country_id = c.id
		LEFT JOIN operators o ON np.operator_id = o.id
		%s
		ORDER BY %s %s
		LIMIT $%d OFFSET $%d
	`, whereClause, sortBy, sortDir, argIdx, argIdx+1)

	args = append(args, pageSize, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list prefixes: %w", err)
	}
	defer rows.Close()

	var items []NumberPrefix
	for rows.Next() {
		var out NumberPrefix
		if err := rows.Scan(
			&out.ID, &out.Prefix, &out.CountryID, &out.CountryName, &out.CountryISO2,
			&out.OperatorID, &out.OperatorName,
			&out.NumberType, &out.Priority, &out.EffectiveFrom, &out.EffectiveTo,
			&out.DataSource, &out.Status, &out.Version, &out.CreatedAt, &out.UpdatedAt,
			&out.CreatedBy, &out.UpdatedBy,
		); err != nil {
			return nil, 0, fmt.Errorf("scan prefix: %w", err)
		}
		items = append(items, out)
	}

	return items, total, nil
}

func (r *pgRepository) ListAllActive(ctx context.Context) ([]NumberPrefix, error) {
	query := `
		SELECT np.id, np.prefix, np.country_id, c.name AS country_name, c.iso2 AS country_iso2,
		       np.operator_id, COALESCE(o.name, '') AS operator_name,
		       np.number_type, np.priority, np.effective_from, np.effective_to,
		       np.data_source, np.status, np.version, np.created_at, np.updated_at,
		       np.created_by, np.updated_by
		FROM number_prefixes np
		JOIN countries c ON np.country_id = c.id
		LEFT JOIN operators o ON np.operator_id = o.id
		WHERE np.status = 'active'
		ORDER BY LENGTH(np.prefix) DESC, np.priority ASC
	`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query all active prefixes: %w", err)
	}
	defer rows.Close()

	var items []NumberPrefix
	for rows.Next() {
		var out NumberPrefix
		if err := rows.Scan(
			&out.ID, &out.Prefix, &out.CountryID, &out.CountryName, &out.CountryISO2,
			&out.OperatorID, &out.OperatorName,
			&out.NumberType, &out.Priority, &out.EffectiveFrom, &out.EffectiveTo,
			&out.DataSource, &out.Status, &out.Version, &out.CreatedAt, &out.UpdatedAt,
			&out.CreatedBy, &out.UpdatedBy,
		); err != nil {
			return nil, fmt.Errorf("scan active prefix: %w", err)
		}
		items = append(items, out)
	}
	return items, nil
}

func (r *pgRepository) Update(ctx context.Context, tx pgx.Tx, np NumberPrefix, actorID *uuid.UUID, reason string) (*NumberPrefix, error) {
	now := time.Now()
	query := `
		UPDATE number_prefixes
		SET prefix = $1, operator_id = $2, number_type = $3, priority = $4,
		    effective_from = $5, effective_to = $6, data_source = $7,
		    version = version + 1, updated_at = $8, updated_by = $9
		WHERE id = $10
		RETURNING id, prefix, country_id, operator_id, number_type, priority,
		          effective_from, effective_to, data_source, status,
		          version, created_at, updated_at, created_by, updated_by
	`
	var out NumberPrefix
	err := tx.QueryRow(ctx, query,
		np.Prefix, np.OperatorID, np.NumberType, np.Priority,
		np.EffectiveFrom, np.EffectiveTo, np.DataSource,
		now, actorID, np.ID,
	).Scan(
		&out.ID, &out.Prefix, &out.CountryID, &out.OperatorID, &out.NumberType, &out.Priority,
		&out.EffectiveFrom, &out.EffectiveTo, &out.DataSource, &out.Status,
		&out.Version, &out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPrefixNotFound
		}
		return nil, fmt.Errorf("update number prefix: %w", err)
	}

	snapshotJSON, _ := json.Marshal(out)
	_, err = tx.Exec(ctx, `
		INSERT INTO number_prefixes_history (id, prefix_id, version, snapshot, actor_user_id, changed_at, reason)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
	`, out.ID, out.Version, snapshotJSON, actorID, now, reason)
	if err != nil {
		return nil, fmt.Errorf("insert prefix history: %w", err)
	}

	return &out, nil
}

func (r *pgRepository) SetStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string, actorID *uuid.UUID, reason string) (*NumberPrefix, error) {
	now := time.Now()
	query := `
		UPDATE number_prefixes
		SET status = $1, version = version + 1, updated_at = $2, updated_by = $3
		WHERE id = $4
		RETURNING id, prefix, country_id, operator_id, number_type, priority,
		          effective_from, effective_to, data_source, status,
		          version, created_at, updated_at, created_by, updated_by
	`
	var out NumberPrefix
	err := tx.QueryRow(ctx, query, status, now, actorID, id).Scan(
		&out.ID, &out.Prefix, &out.CountryID, &out.OperatorID, &out.NumberType, &out.Priority,
		&out.EffectiveFrom, &out.EffectiveTo, &out.DataSource, &out.Status,
		&out.Version, &out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPrefixNotFound
		}
		return nil, fmt.Errorf("set prefix status: %w", err)
	}

	snapshotJSON, _ := json.Marshal(out)
	_, err = tx.Exec(ctx, `
		INSERT INTO number_prefixes_history (id, prefix_id, version, snapshot, actor_user_id, changed_at, reason)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
	`, out.ID, out.Version, snapshotJSON, actorID, now, reason)
	if err != nil {
		return nil, fmt.Errorf("insert prefix history: %w", err)
	}

	return &out, nil
}

func (r *pgRepository) GetHistory(ctx context.Context, prefixID uuid.UUID) ([]HistoryEntry, error) {
	query := `
		SELECT id, prefix_id, version, snapshot, actor_user_id, changed_at, reason
		FROM number_prefixes_history
		WHERE prefix_id = $1
		ORDER BY version DESC
	`
	rows, err := r.pool.Query(ctx, query, prefixID)
	if err != nil {
		return nil, fmt.Errorf("query prefix history: %w", err)
	}
	defer rows.Close()

	var history []HistoryEntry
	for rows.Next() {
		var h HistoryEntry
		var snapshotBytes []byte
		if err := rows.Scan(
			&h.ID, &h.PrefixID, &h.Version, &snapshotBytes, &h.ActorUserID, &h.ChangedAt, &h.Reason,
		); err != nil {
			return nil, fmt.Errorf("scan prefix history entry: %w", err)
		}
		_ = json.Unmarshal(snapshotBytes, &h.Snapshot)
		history = append(history, h)
	}
	return history, nil
}

func (r *pgRepository) Rollback(ctx context.Context, tx pgx.Tx, id uuid.UUID, targetVersion int, actorID *uuid.UUID, reason string) (*NumberPrefix, error) {
	var snapshotBytes []byte
	err := tx.QueryRow(ctx, `
		SELECT snapshot FROM number_prefixes_history WHERE prefix_id = $1 AND version = $2
	`, id, targetVersion).Scan(&snapshotBytes)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrVersionNotFound
		}
		return nil, fmt.Errorf("find rollback prefix snapshot: %w", err)
	}

	var target NumberPrefix
	if err := json.Unmarshal(snapshotBytes, &target); err != nil {
		return nil, fmt.Errorf("unmarshal target prefix snapshot: %w", err)
	}

	now := time.Now()
	query := `
		UPDATE number_prefixes
		SET prefix = $1, operator_id = $2, number_type = $3, priority = $4,
		    effective_from = $5, effective_to = $6, data_source = $7, status = $8,
		    version = version + 1, updated_at = $9, updated_by = $10
		WHERE id = $11
		RETURNING id, prefix, country_id, operator_id, number_type, priority,
		          effective_from, effective_to, data_source, status,
		          version, created_at, updated_at, created_by, updated_by
	`
	var out NumberPrefix
	err = tx.QueryRow(ctx, query,
		target.Prefix, target.OperatorID, target.NumberType, target.Priority,
		target.EffectiveFrom, target.EffectiveTo, target.DataSource, target.Status,
		now, actorID, id,
	).Scan(
		&out.ID, &out.Prefix, &out.CountryID, &out.OperatorID, &out.NumberType, &out.Priority,
		&out.EffectiveFrom, &out.EffectiveTo, &out.DataSource, &out.Status,
		&out.Version, &out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
	)
	if err != nil {
		return nil, fmt.Errorf("rollback prefix record: %w", err)
	}

	newSnapshotJSON, _ := json.Marshal(out)
	rollbackReason := reason
	if rollbackReason == "" {
		rollbackReason = fmt.Sprintf("Rolled back prefix to snapshot from version %d", targetVersion)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO number_prefixes_history (id, prefix_id, version, snapshot, actor_user_id, changed_at, reason)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
	`, out.ID, out.Version, newSnapshotJSON, actorID, now, rollbackReason)
	if err != nil {
		return nil, fmt.Errorf("record prefix rollback history snapshot: %w", err)
	}

	return &out, nil
}
