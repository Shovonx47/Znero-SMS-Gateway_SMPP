package number

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
	ErrRuleNotFound    = errors.New("number transformation rule not found")
	ErrVersionNotFound = errors.New("target version snapshot not found in rule history")
)

type Repository interface {
	CreateRule(ctx context.Context, tx pgx.Tx, r TransformRule, actorID *uuid.UUID, reason string) (*TransformRule, error)
	GetRuleByID(ctx context.Context, id uuid.UUID) (*TransformRule, error)
	ListRules(ctx context.Context, f RuleFilter) ([]TransformRule, int, error)
	ListAllActiveRules(ctx context.Context) ([]TransformRule, error)
	UpdateRule(ctx context.Context, tx pgx.Tx, r TransformRule, actorID *uuid.UUID, reason string) (*TransformRule, error)
	DeleteRule(ctx context.Context, tx pgx.Tx, id uuid.UUID, actorID *uuid.UUID, reason string) error
	GetRuleHistory(ctx context.Context, ruleID uuid.UUID) ([]RuleHistoryEntry, error)
	RollbackRule(ctx context.Context, tx pgx.Tx, id uuid.UUID, targetVersion int, actorID *uuid.UUID, reason string) (*TransformRule, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

func (r *pgRepository) CreateRule(ctx context.Context, tx pgx.Tx, rule TransformRule, actorID *uuid.UUID, reason string) (*TransformRule, error) {
	id := rule.ID
	if id == uuid.Nil {
		id = uuid.New()
	}
	now := time.Now()
	version := 1
	status := rule.Status
	if status == "" {
		status = "active"
	}
	direction := rule.Direction
	if direction == "" {
		direction = "both"
	}
	priority := rule.Priority
	if priority <= 0 {
		priority = 100
	}
	paramsJSON, _ := json.Marshal(rule.Params)

	query := `
		INSERT INTO number_transform_rules (
			id, name, description, operation, match_pattern, params,
			scope, country_id, operator_id, direction, priority,
			active_from, active_to, status, version,
			created_at, updated_at, created_by, updated_by
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11,
			$12, $13, $14, $15,
			$16, $16, $17, $17
		) RETURNING id, name, description, operation, match_pattern, params,
		            scope, country_id, operator_id, direction, priority,
		            active_from, active_to, status, version,
		            created_at, updated_at, created_by, updated_by
	`

	var execer interface {
		QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	} = r.pool
	if tx != nil {
		execer = tx
	}

	var out TransformRule
	var outParamsBytes []byte
	err := execer.QueryRow(ctx, query,
		id, rule.Name, rule.Description, rule.Operation, rule.MatchPattern, paramsJSON,
		rule.Scope, rule.CountryID, rule.OperatorID, direction, priority,
		rule.ActiveFrom, rule.ActiveTo, status, version,
		now, actorID,
	).Scan(
		&out.ID, &out.Name, &out.Description, &out.Operation, &out.MatchPattern, &outParamsBytes,
		&out.Scope, &out.CountryID, &out.OperatorID, &out.Direction, &out.Priority,
		&out.ActiveFrom, &out.ActiveTo, &out.Status, &out.Version,
		&out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
	)
	if err != nil {
		return nil, fmt.Errorf("insert transform rule: %w", err)
	}
	_ = json.Unmarshal(outParamsBytes, &out.Params)

	snapshotJSON, _ := json.Marshal(out)
	historyQuery := `
		INSERT INTO number_transform_rules_history (id, rule_id, version, snapshot, actor_user_id, changed_at, reason)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
	`
	if tx != nil {
		_, err = tx.Exec(ctx, historyQuery, out.ID, out.Version, snapshotJSON, actorID, now, reason)
	} else {
		_, err = r.pool.Exec(ctx, historyQuery, out.ID, out.Version, snapshotJSON, actorID, now, reason)
	}
	if err != nil {
		return nil, fmt.Errorf("insert rule history: %w", err)
	}

	return &out, nil
}

func (r *pgRepository) GetRuleByID(ctx context.Context, id uuid.UUID) (*TransformRule, error) {
	query := `
		SELECT r.id, r.name, r.description, r.operation, r.match_pattern, r.params,
		       r.scope, r.country_id, COALESCE(c.name, '') AS country_name, COALESCE(c.iso2, '') AS country_iso2,
		       r.operator_id, COALESCE(o.name, '') AS operator_name,
		       r.direction, r.priority, r.active_from, r.active_to, r.status, r.version,
		       r.created_at, r.updated_at, r.created_by, r.updated_by
		FROM number_transform_rules r
		LEFT JOIN countries c ON r.country_id = c.id
		LEFT JOIN operators o ON r.operator_id = o.id
		WHERE r.id = $1
	`
	var out TransformRule
	var paramsBytes []byte
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&out.ID, &out.Name, &out.Description, &out.Operation, &out.MatchPattern, &paramsBytes,
		&out.Scope, &out.CountryID, &out.CountryName, &out.CountryISO2,
		&out.OperatorID, &out.OperatorName,
		&out.Direction, &out.Priority, &out.ActiveFrom, &out.ActiveTo, &out.Status, &out.Version,
		&out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRuleNotFound
		}
		return nil, fmt.Errorf("get rule by id: %w", err)
	}
	_ = json.Unmarshal(paramsBytes, &out.Params)
	return &out, nil
}

func (r *pgRepository) ListRules(ctx context.Context, f RuleFilter) ([]TransformRule, int, error) {
	var where []string
	var args []any
	argIdx := 1

	if f.Scope != "" {
		where = append(where, fmt.Sprintf("r.scope = $%d", argIdx))
		args = append(args, f.Scope)
		argIdx++
	}
	if f.Status != "" {
		where = append(where, fmt.Sprintf("r.status = $%d", argIdx))
		args = append(args, f.Status)
		argIdx++
	}
	if f.CountryID != nil {
		where = append(where, fmt.Sprintf("r.country_id = $%d", argIdx))
		args = append(args, *f.CountryID)
		argIdx++
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM number_transform_rules r %s", whereClause)
	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count rules: %w", err)
	}

	page := f.Page
	if page < 1 {
		page = 1
	}
	pageSize := f.PageSize
	if pageSize < 1 || pageSize > 500 {
		pageSize = 50
	}
	offset := (page - 1) * pageSize

	query := fmt.Sprintf(`
		SELECT r.id, r.name, r.description, r.operation, r.match_pattern, r.params,
		       r.scope, r.country_id, COALESCE(c.name, '') AS country_name, COALESCE(c.iso2, '') AS country_iso2,
		       r.operator_id, COALESCE(o.name, '') AS operator_name,
		       r.direction, r.priority, r.active_from, r.active_to, r.status, r.version,
		       r.created_at, r.updated_at, r.created_by, r.updated_by
		FROM number_transform_rules r
		LEFT JOIN countries c ON r.country_id = c.id
		LEFT JOIN operators o ON r.operator_id = o.id
		%s
		ORDER BY r.priority ASC, r.created_at ASC
		LIMIT $%d OFFSET $%d
	`, whereClause, argIdx, argIdx+1)

	args = append(args, pageSize, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list rules: %w", err)
	}
	defer rows.Close()

	var items []TransformRule
	for rows.Next() {
		var out TransformRule
		var paramsBytes []byte
		if err := rows.Scan(
			&out.ID, &out.Name, &out.Description, &out.Operation, &out.MatchPattern, &paramsBytes,
			&out.Scope, &out.CountryID, &out.CountryName, &out.CountryISO2,
			&out.OperatorID, &out.OperatorName,
			&out.Direction, &out.Priority, &out.ActiveFrom, &out.ActiveTo, &out.Status, &out.Version,
			&out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
		); err != nil {
			return nil, 0, fmt.Errorf("scan rule: %w", err)
		}
		_ = json.Unmarshal(paramsBytes, &out.Params)
		items = append(items, out)
	}

	return items, total, nil
}

func (r *pgRepository) ListAllActiveRules(ctx context.Context) ([]TransformRule, error) {
	query := `
		SELECT r.id, r.name, r.description, r.operation, r.match_pattern, r.params,
		       r.scope, r.country_id, COALESCE(c.name, '') AS country_name, COALESCE(c.iso2, '') AS country_iso2,
		       r.operator_id, COALESCE(o.name, '') AS operator_name,
		       r.direction, r.priority, r.active_from, r.active_to, r.status, r.version,
		       r.created_at, r.updated_at, r.created_by, r.updated_by
		FROM number_transform_rules r
		LEFT JOIN countries c ON r.country_id = c.id
		LEFT JOIN operators o ON r.operator_id = o.id
		WHERE r.status = 'active'
		ORDER BY r.priority ASC, r.created_at ASC
	`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list active rules: %w", err)
	}
	defer rows.Close()

	var items []TransformRule
	for rows.Next() {
		var out TransformRule
		var paramsBytes []byte
		if err := rows.Scan(
			&out.ID, &out.Name, &out.Description, &out.Operation, &out.MatchPattern, &paramsBytes,
			&out.Scope, &out.CountryID, &out.CountryName, &out.CountryISO2,
			&out.OperatorID, &out.OperatorName,
			&out.Direction, &out.Priority, &out.ActiveFrom, &out.ActiveTo, &out.Status, &out.Version,
			&out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
		); err != nil {
			return nil, fmt.Errorf("scan active rule: %w", err)
		}
		_ = json.Unmarshal(paramsBytes, &out.Params)
		items = append(items, out)
	}

	return items, nil
}

func (r *pgRepository) UpdateRule(ctx context.Context, tx pgx.Tx, rule TransformRule, actorID *uuid.UUID, reason string) (*TransformRule, error) {
	now := time.Now()
	paramsJSON, _ := json.Marshal(rule.Params)

	query := `
		UPDATE number_transform_rules
		SET name = $1, description = $2, operation = $3, match_pattern = $4, params = $5,
		    scope = $6, country_id = $7, operator_id = $8, direction = $9, priority = $10,
		    active_from = $11, active_to = $12, status = $13,
		    version = version + 1, updated_at = $14, updated_by = $15
		WHERE id = $16
		RETURNING id, name, description, operation, match_pattern, params,
		          scope, country_id, operator_id, direction, priority,
		          active_from, active_to, status, version,
		          created_at, updated_at, created_by, updated_by
	`
	var out TransformRule
	var paramsBytes []byte
	err := tx.QueryRow(ctx, query,
		rule.Name, rule.Description, rule.Operation, rule.MatchPattern, paramsJSON,
		rule.Scope, rule.CountryID, rule.OperatorID, rule.Direction, rule.Priority,
		rule.ActiveFrom, rule.ActiveTo, rule.Status,
		now, actorID, rule.ID,
	).Scan(
		&out.ID, &out.Name, &out.Description, &out.Operation, &out.MatchPattern, &paramsBytes,
		&out.Scope, &out.CountryID, &out.OperatorID, &out.Direction, &out.Priority,
		&out.ActiveFrom, &out.ActiveTo, &out.Status, &out.Version,
		&out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRuleNotFound
		}
		return nil, fmt.Errorf("update rule: %w", err)
	}
	_ = json.Unmarshal(paramsBytes, &out.Params)

	snapshotJSON, _ := json.Marshal(out)
	_, err = tx.Exec(ctx, `
		INSERT INTO number_transform_rules_history (id, rule_id, version, snapshot, actor_user_id, changed_at, reason)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
	`, out.ID, out.Version, snapshotJSON, actorID, now, reason)
	if err != nil {
		return nil, fmt.Errorf("insert rule history: %w", err)
	}

	return &out, nil
}

func (r *pgRepository) DeleteRule(ctx context.Context, tx pgx.Tx, id uuid.UUID, actorID *uuid.UUID, reason string) error {
	now := time.Now()
	res, err := tx.Exec(ctx, `
		UPDATE number_transform_rules
		SET status = 'inactive', version = version + 1, updated_at = $1, updated_by = $2
		WHERE id = $3
	`, now, actorID, id)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return ErrRuleNotFound
	}
	return nil
}

func (r *pgRepository) GetRuleHistory(ctx context.Context, ruleID uuid.UUID) ([]RuleHistoryEntry, error) {
	query := `
		SELECT id, rule_id, version, snapshot, actor_user_id, changed_at, reason
		FROM number_transform_rules_history
		WHERE rule_id = $1
		ORDER BY version DESC
	`
	rows, err := r.pool.Query(ctx, query, ruleID)
	if err != nil {
		return nil, fmt.Errorf("query rule history: %w", err)
	}
	defer rows.Close()

	var history []RuleHistoryEntry
	for rows.Next() {
		var h RuleHistoryEntry
		if err := rows.Scan(
			&h.ID, &h.RuleID, &h.Version, &h.Snapshot, &h.ActorUserID, &h.ChangedAt, &h.Reason,
		); err != nil {
			return nil, fmt.Errorf("scan rule history: %w", err)
		}
		history = append(history, h)
	}
	return history, nil
}

func (r *pgRepository) RollbackRule(ctx context.Context, tx pgx.Tx, id uuid.UUID, targetVersion int, actorID *uuid.UUID, reason string) (*TransformRule, error) {
	var snapshotBytes []byte
	err := tx.QueryRow(ctx, `
		SELECT snapshot FROM number_transform_rules_history WHERE rule_id = $1 AND version = $2
	`, id, targetVersion).Scan(&snapshotBytes)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrVersionNotFound
		}
		return nil, fmt.Errorf("find rollback rule snapshot: %w", err)
	}

	var target TransformRule
	if err := json.Unmarshal(snapshotBytes, &target); err != nil {
		return nil, fmt.Errorf("unmarshal target rule snapshot: %w", err)
	}

	now := time.Now()
	paramsJSON, _ := json.Marshal(target.Params)

	query := `
		UPDATE number_transform_rules
		SET name = $1, description = $2, operation = $3, match_pattern = $4, params = $5,
		    scope = $6, country_id = $7, operator_id = $8, direction = $9, priority = $10,
		    active_from = $11, active_to = $12, status = $13,
		    version = version + 1, updated_at = $14, updated_by = $15
		WHERE id = $16
		RETURNING id, name, description, operation, match_pattern, params,
		          scope, country_id, operator_id, direction, priority,
		          active_from, active_to, status, version,
		          created_at, updated_at, created_by, updated_by
	`
	var out TransformRule
	var paramsBytes []byte
	err = tx.QueryRow(ctx, query,
		target.Name, target.Description, target.Operation, target.MatchPattern, paramsJSON,
		target.Scope, target.CountryID, target.OperatorID, target.Direction, target.Priority,
		target.ActiveFrom, target.ActiveTo, target.Status,
		now, actorID, id,
	).Scan(
		&out.ID, &out.Name, &out.Description, &out.Operation, &out.MatchPattern, &paramsBytes,
		&out.Scope, &out.CountryID, &out.OperatorID, &out.Direction, &out.Priority,
		&out.ActiveFrom, &out.ActiveTo, &out.Status, &out.Version,
		&out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
	)
	if err != nil {
		return nil, fmt.Errorf("rollback rule record: %w", err)
	}
	_ = json.Unmarshal(paramsBytes, &out.Params)

	newSnapshotJSON, _ := json.Marshal(out)
	rollbackReason := reason
	if rollbackReason == "" {
		rollbackReason = fmt.Sprintf("Rolled back rule to snapshot from version %d", targetVersion)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO number_transform_rules_history (id, rule_id, version, snapshot, actor_user_id, changed_at, reason)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
	`, out.ID, out.Version, newSnapshotJSON, actorID, now, rollbackReason)
	if err != nil {
		return nil, fmt.Errorf("record rule rollback history snapshot: %w", err)
	}

	return &out, nil
}
