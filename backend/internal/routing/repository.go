package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound       = errors.New("resource not found")
	ErrConflict       = errors.New("resource conflict")
	ErrVersionMismatch = errors.New("version mismatch")
)

type Repository interface {
	// Plans
	CreatePlan(ctx context.Context, tx pgx.Tx, plan *RoutePlan) (*RoutePlan, error)
	UpdatePlan(ctx context.Context, tx pgx.Tx, plan *RoutePlan) (*RoutePlan, error)
	GetPlanByID(ctx context.Context, id uuid.UUID) (*RoutePlan, error)
	GetPlanByCode(ctx context.Context, code string) (*RoutePlan, error)
	ListPlans(ctx context.Context, status string, page, pageSize int) ([]RoutePlan, int, error)
	RecordPlanHistory(ctx context.Context, tx pgx.Tx, planID, changedBy uuid.UUID, changeType string, snapshot any) error
	GetPlanHistory(ctx context.Context, planID uuid.UUID) ([]RoutePlanHistory, error)

	// Entries
	CreateEntry(ctx context.Context, tx pgx.Tx, entry *RoutePlanEntry) (*RoutePlanEntry, error)
	UpdateEntry(ctx context.Context, tx pgx.Tx, entry *RoutePlanEntry) (*RoutePlanEntry, error)
	DeleteEntry(ctx context.Context, tx pgx.Tx, entryID uuid.UUID) error
	GetEntryByID(ctx context.Context, id uuid.UUID) (*RoutePlanEntry, error)
	ListEntriesByPlanID(ctx context.Context, planID uuid.UUID) ([]RoutePlanEntry, error)
	RecordEntryHistory(ctx context.Context, tx pgx.Tx, entryID, changedBy uuid.UUID, changeType string, snapshot any) error
	GetEntryHistory(ctx context.Context, entryID uuid.UUID) ([]RoutePlanEntryHistory, error)

	// Customer Route Assignments
	CreateAssignment(ctx context.Context, tx pgx.Tx, a *CustomerRouteAssignment) (*CustomerRouteAssignment, error)
	UpdateAssignment(ctx context.Context, tx pgx.Tx, a *CustomerRouteAssignment) (*CustomerRouteAssignment, error)
	DeleteAssignment(ctx context.Context, tx pgx.Tx, id uuid.UUID) error
	GetAssignmentByID(ctx context.Context, id uuid.UUID) (*CustomerRouteAssignment, error)
	ListAssignments(ctx context.Context, customerOrgID *uuid.UUID, page, pageSize int) ([]CustomerRouteAssignment, int, error)
	RecordAssignmentHistory(ctx context.Context, tx pgx.Tx, assignmentID, changedBy uuid.UUID, changeType string, snapshot any) error
	GetAssignmentHistory(ctx context.Context, assignmentID uuid.UUID) ([]CustomerRouteAssignmentHistory, error)

	// Routing Rules
	CreateRule(ctx context.Context, tx pgx.Tx, r *RoutingRule) (*RoutingRule, error)
	UpdateRule(ctx context.Context, tx pgx.Tx, r *RoutingRule) (*RoutingRule, error)
	DeleteRule(ctx context.Context, tx pgx.Tx, id uuid.UUID) error
	GetRuleByID(ctx context.Context, id uuid.UUID) (*RoutingRule, error)
	ListRules(ctx context.Context, status string, page, pageSize int) ([]RoutingRule, int, error)
	RecordRuleHistory(ctx context.Context, tx pgx.Tx, ruleID, changedBy uuid.UUID, changeType string, snapshot any) error
	GetRuleHistory(ctx context.Context, ruleID uuid.UUID) ([]RoutingRuleHistory, error)

	// Overrides / Kill Switch
	SetConnectionOverride(ctx context.Context, tx pgx.Tx, o *ConnectionOverride) (*ConnectionOverride, error)
	RemoveConnectionOverride(ctx context.Context, tx pgx.Tx, connectionID uuid.UUID) error
	GetConnectionOverride(ctx context.Context, connectionID uuid.UUID) (*ConnectionOverride, error)
	ListConnectionOverrides(ctx context.Context) ([]ConnectionOverride, error)

	// Helpers for validation
	GetConnectionDetails(ctx context.Context, connectionID uuid.UUID) (*SnapshotConnection, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

// -----------------------------------------------------------------------------
// Route Plans
// -----------------------------------------------------------------------------

func (r *pgRepository) CreatePlan(ctx context.Context, tx pgx.Tx, plan *RoutePlan) (*RoutePlan, error) {
	query := `
		INSERT INTO route_plans (
			name, code, description, strategy, status, notes, created_by, created_at, updated_at, version
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, now(), now(), 1
		)
		RETURNING id, name, code, COALESCE(description, ''), strategy, status, COALESCE(notes, ''),
		          created_by, created_at, updated_at, version
	`
	q := r.pool.QueryRow
	if tx != nil {
		q = tx.QueryRow
	}

	var res RoutePlan
	err := q(ctx, query, plan.Name, plan.Code, plan.Description, plan.Strategy, plan.Status, plan.Notes, plan.CreatedBy).Scan(
		&res.ID, &res.Name, &res.Code, &res.Description, &res.Strategy, &res.Status, &res.Notes,
		&res.CreatedBy, &res.CreatedAt, &res.UpdatedAt, &res.Version,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, fmt.Errorf("%w: route plan code already exists", ErrConflict)
		}
		return nil, fmt.Errorf("create route plan: %w", err)
	}
	return &res, nil
}

func (r *pgRepository) UpdatePlan(ctx context.Context, tx pgx.Tx, plan *RoutePlan) (*RoutePlan, error) {
	query := `
		UPDATE route_plans
		SET name = $1, code = $2, description = $3, strategy = $4, status = $5, notes = $6,
		    updated_at = now(), version = version + 1
		WHERE id = $7 AND version = $8
		RETURNING id, name, code, COALESCE(description, ''), strategy, status, COALESCE(notes, ''),
		          created_by, created_at, updated_at, version
	`
	q := r.pool.QueryRow
	if tx != nil {
		q = tx.QueryRow
	}

	var res RoutePlan
	err := q(ctx, query, plan.Name, plan.Code, plan.Description, plan.Strategy, plan.Status, plan.Notes, plan.ID, plan.Version).Scan(
		&res.ID, &res.Name, &res.Code, &res.Description, &res.Strategy, &res.Status, &res.Notes,
		&res.CreatedBy, &res.CreatedAt, &res.UpdatedAt, &res.Version,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrVersionMismatch
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, fmt.Errorf("%w: route plan code already exists", ErrConflict)
		}
		return nil, fmt.Errorf("update route plan: %w", err)
	}
	return &res, nil
}

func (r *pgRepository) GetPlanByID(ctx context.Context, id uuid.UUID) (*RoutePlan, error) {
	query := `
		SELECT id, name, code, COALESCE(description, ''), strategy, status, COALESCE(notes, ''),
		       created_by, created_at, updated_at, version
		FROM route_plans
		WHERE id = $1
	`
	var p RoutePlan
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&p.ID, &p.Name, &p.Code, &p.Description, &p.Strategy, &p.Status, &p.Notes,
		&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt, &p.Version,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get route plan by id: %w", err)
	}

	// Fetch entries
	entries, err := r.ListEntriesByPlanID(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	p.Entries = entries
	return &p, nil
}

func (r *pgRepository) GetPlanByCode(ctx context.Context, code string) (*RoutePlan, error) {
	query := `
		SELECT id, name, code, COALESCE(description, ''), strategy, status, COALESCE(notes, ''),
		       created_by, created_at, updated_at, version
		FROM route_plans
		WHERE code = $1
	`
	var p RoutePlan
	err := r.pool.QueryRow(ctx, query, code).Scan(
		&p.ID, &p.Name, &p.Code, &p.Description, &p.Strategy, &p.Status, &p.Notes,
		&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt, &p.Version,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get route plan by code: %w", err)
	}

	entries, err := r.ListEntriesByPlanID(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	p.Entries = entries
	return &p, nil
}

func (r *pgRepository) ListPlans(ctx context.Context, status string, page, pageSize int) ([]RoutePlan, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	baseWhere := "WHERE status != 'archived'"
	var args []interface{}
	argIdx := 1
	if status != "" {
		baseWhere += fmt.Sprintf(" AND status = $%d", argIdx)
		args = append(args, status)
		argIdx++
	}

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM route_plans %s", baseWhere)
	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count route plans: %w", err)
	}

	query := fmt.Sprintf(`
		SELECT id, name, code, COALESCE(description, ''), strategy, status, COALESCE(notes, ''),
		       created_by, created_at, updated_at, version
		FROM route_plans
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d
	`, baseWhere, argIdx, argIdx+1)

	args = append(args, pageSize, offset)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list route plans: %w", err)
	}
	defer rows.Close()

	var plans []RoutePlan
	for rows.Next() {
		var p RoutePlan
		if err := rows.Scan(
			&p.ID, &p.Name, &p.Code, &p.Description, &p.Strategy, &p.Status, &p.Notes,
			&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt, &p.Version,
		); err != nil {
			return nil, 0, fmt.Errorf("scan route plan: %w", err)
		}
		plans = append(plans, p)
	}

	return plans, total, nil
}

func (r *pgRepository) RecordPlanHistory(ctx context.Context, tx pgx.Tx, planID, changedBy uuid.UUID, changeType string, snapshot any) error {
	snapBytes, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal plan snapshot: %w", err)
	}

	query := `
		INSERT INTO route_plan_history (plan_id, changed_by, changed_at, change_type, snapshot)
		VALUES ($1, $2, now(), $3, $4)
	`
	q := r.pool.Exec
	if tx != nil {
		q = tx.Exec
	}

	_, err = q(ctx, query, planID, changedBy, changeType, snapBytes)
	if err != nil {
		return fmt.Errorf("record plan history: %w", err)
	}
	return nil
}

func (r *pgRepository) GetPlanHistory(ctx context.Context, planID uuid.UUID) ([]RoutePlanHistory, error) {
	query := `
		SELECT id, plan_id, changed_by, changed_at, change_type, snapshot
		FROM route_plan_history
		WHERE plan_id = $1
		ORDER BY changed_at DESC
	`
	rows, err := r.pool.Query(ctx, query, planID)
	if err != nil {
		return nil, fmt.Errorf("get plan history: %w", err)
	}
	defer rows.Close()

	var history []RoutePlanHistory
	for rows.Next() {
		var h RoutePlanHistory
		if err := rows.Scan(&h.ID, &h.PlanID, &h.ChangedBy, &h.ChangedAt, &h.ChangeType, &h.Snapshot); err != nil {
			return nil, fmt.Errorf("scan plan history: %w", err)
		}
		history = append(history, h)
	}
	return history, nil
}

// -----------------------------------------------------------------------------
// Route Plan Entries
// -----------------------------------------------------------------------------

func (r *pgRepository) CreateEntry(ctx context.Context, tx pgx.Tx, entry *RoutePlanEntry) (*RoutePlanEntry, error) {
	senderIDTypes := entry.AllowedSenderIDTypes
	if senderIDTypes == nil {
		senderIDTypes = []string{}
	}
	scopeType := entry.ScopeType
	if scopeType == "" {
		scopeType = "global"
	}

	query := `
		INSERT INTO route_plan_entries (
			plan_id, connection_id, scope_type, scope_country_id, scope_operator_id, scope_prefix,
			priority, weight, min_margin_override, allowed_sender_id_types, destination_number_format,
			allow_tested_coverage, status, version, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 1, now(), now()
		)
		RETURNING id, plan_id, connection_id, scope_type, scope_country_id, scope_operator_id,
		          COALESCE(scope_prefix, ''), priority, weight, min_margin_override,
		          allowed_sender_id_types, destination_number_format, allow_tested_coverage,
		          status, version, created_at, updated_at
	`
	q := r.pool.QueryRow
	if tx != nil {
		q = tx.QueryRow
	}

	var res RoutePlanEntry
	err := q(ctx, query,
		entry.PlanID, entry.ConnectionID, scopeType, entry.ScopeCountryID, entry.ScopeOperatorID,
		entry.ScopePrefix, entry.Priority, entry.Weight, entry.MinMarginOverride,
		senderIDTypes, entry.DestinationNumberFormat, entry.AllowTestedCoverage, entry.Status,
	).Scan(
		&res.ID, &res.PlanID, &res.ConnectionID, &res.ScopeType, &res.ScopeCountryID, &res.ScopeOperatorID,
		&res.ScopePrefix, &res.Priority, &res.Weight, &res.MinMarginOverride,
		&res.AllowedSenderIDTypes, &res.DestinationNumberFormat, &res.AllowTestedCoverage,
		&res.Status, &res.Version, &res.CreatedAt, &res.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create route plan entry: %w", err)
	}
	return &res, nil
}

func (r *pgRepository) UpdateEntry(ctx context.Context, tx pgx.Tx, entry *RoutePlanEntry) (*RoutePlanEntry, error) {
	senderIDTypes := entry.AllowedSenderIDTypes
	if senderIDTypes == nil {
		senderIDTypes = []string{}
	}

	query := `
		UPDATE route_plan_entries
		SET connection_id = $1, scope_type = $2, scope_country_id = $3, scope_operator_id = $4,
		    scope_prefix = $5, priority = $6, weight = $7, min_margin_override = $8,
		    allowed_sender_id_types = $9, destination_number_format = $10, allow_tested_coverage = $11,
		    status = $12, updated_at = now(), version = version + 1
		WHERE id = $13 AND version = $14
		RETURNING id, plan_id, connection_id, scope_type, scope_country_id, scope_operator_id,
		          COALESCE(scope_prefix, ''), priority, weight, min_margin_override,
		          allowed_sender_id_types, destination_number_format, allow_tested_coverage,
		          status, version, created_at, updated_at
	`
	q := r.pool.QueryRow
	if tx != nil {
		q = tx.QueryRow
	}

	var res RoutePlanEntry
	err := q(ctx, query,
		entry.ConnectionID, entry.ScopeType, entry.ScopeCountryID, entry.ScopeOperatorID,
		entry.ScopePrefix, entry.Priority, entry.Weight, entry.MinMarginOverride,
		senderIDTypes, entry.DestinationNumberFormat, entry.AllowTestedCoverage,
		entry.Status, entry.ID, entry.Version,
	).Scan(
		&res.ID, &res.PlanID, &res.ConnectionID, &res.ScopeType, &res.ScopeCountryID, &res.ScopeOperatorID,
		&res.ScopePrefix, &res.Priority, &res.Weight, &res.MinMarginOverride,
		&res.AllowedSenderIDTypes, &res.DestinationNumberFormat, &res.AllowTestedCoverage,
		&res.Status, &res.Version, &res.CreatedAt, &res.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrVersionMismatch
		}
		return nil, fmt.Errorf("update route plan entry: %w", err)
	}
	return &res, nil
}

func (r *pgRepository) DeleteEntry(ctx context.Context, tx pgx.Tx, entryID uuid.UUID) error {
	query := `UPDATE route_plan_entries SET status = 'archived', version = version + 1, updated_at = now() WHERE id = $1`
	q := r.pool.Exec
	if tx != nil {
		q = tx.Exec
	}
	ct, err := q(ctx, query, entryID)
	if err != nil {
		return fmt.Errorf("archive entry: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *pgRepository) GetEntryByID(ctx context.Context, id uuid.UUID) (*RoutePlanEntry, error) {
	query := `
		SELECT e.id, e.plan_id, e.connection_id, e.scope_type, e.scope_country_id, e.scope_operator_id,
		       COALESCE(e.scope_prefix, ''), e.priority, e.weight, e.min_margin_override,
		       e.allowed_sender_id_types, e.destination_number_format, e.allow_tested_coverage,
		       e.status, e.version, e.created_at, e.updated_at,
		       c.name AS connection_name, p.name AS provider_name
		FROM route_plan_entries e
		JOIN provider_connections c ON c.id = e.connection_id
		JOIN providers p ON p.id = c.provider_id
		WHERE e.id = $1
	`
	var e RoutePlanEntry
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&e.ID, &e.PlanID, &e.ConnectionID, &e.ScopeType, &e.ScopeCountryID, &e.ScopeOperatorID,
		&e.ScopePrefix, &e.Priority, &e.Weight, &e.MinMarginOverride,
		&e.AllowedSenderIDTypes, &e.DestinationNumberFormat, &e.AllowTestedCoverage,
		&e.Status, &e.Version, &e.CreatedAt, &e.UpdatedAt,
		&e.ConnectionName, &e.ProviderName,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get route plan entry by id: %w", err)
	}
	return &e, nil
}

func (r *pgRepository) ListEntriesByPlanID(ctx context.Context, planID uuid.UUID) ([]RoutePlanEntry, error) {
	query := `
		SELECT e.id, e.plan_id, e.connection_id, e.scope_type, e.scope_country_id, e.scope_operator_id,
		       COALESCE(e.scope_prefix, ''), e.priority, e.weight, e.min_margin_override,
		       e.allowed_sender_id_types, e.destination_number_format, e.allow_tested_coverage,
		       e.status, e.version, e.created_at, e.updated_at,
		       c.name AS connection_name, p.name AS provider_name
		FROM route_plan_entries e
		JOIN provider_connections c ON c.id = e.connection_id
		JOIN providers p ON p.id = c.provider_id
		WHERE e.plan_id = $1 AND e.status != 'archived'
		ORDER BY e.priority ASC, e.id ASC
	`
	rows, err := r.pool.Query(ctx, query, planID)
	if err != nil {
		return nil, fmt.Errorf("list route plan entries: %w", err)
	}
	defer rows.Close()

	var entries []RoutePlanEntry
	for rows.Next() {
		var e RoutePlanEntry
		if err := rows.Scan(
			&e.ID, &e.PlanID, &e.ConnectionID, &e.ScopeType, &e.ScopeCountryID, &e.ScopeOperatorID,
			&e.ScopePrefix, &e.Priority, &e.Weight, &e.MinMarginOverride,
			&e.AllowedSenderIDTypes, &e.DestinationNumberFormat, &e.AllowTestedCoverage,
			&e.Status, &e.Version, &e.CreatedAt, &e.UpdatedAt,
			&e.ConnectionName, &e.ProviderName,
		); err != nil {
			return nil, fmt.Errorf("scan route plan entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func (r *pgRepository) RecordEntryHistory(ctx context.Context, tx pgx.Tx, entryID, changedBy uuid.UUID, changeType string, snapshot any) error {
	snapBytes, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal entry snapshot: %w", err)
	}

	query := `
		INSERT INTO route_plan_entry_history (entry_id, changed_by, changed_at, change_type, snapshot)
		VALUES ($1, $2, now(), $3, $4)
	`
	q := r.pool.Exec
	if tx != nil {
		q = tx.Exec
	}

	_, err = q(ctx, query, entryID, changedBy, changeType, snapBytes)
	if err != nil {
		return fmt.Errorf("record entry history: %w", err)
	}
	return nil
}

func (r *pgRepository) GetEntryHistory(ctx context.Context, entryID uuid.UUID) ([]RoutePlanEntryHistory, error) {
	query := `
		SELECT id, entry_id, changed_by, changed_at, change_type, snapshot
		FROM route_plan_entry_history
		WHERE entry_id = $1
		ORDER BY changed_at DESC
	`
	rows, err := r.pool.Query(ctx, query, entryID)
	if err != nil {
		return nil, fmt.Errorf("get entry history: %w", err)
	}
	defer rows.Close()

	var history []RoutePlanEntryHistory
	for rows.Next() {
		var h RoutePlanEntryHistory
		if err := rows.Scan(&h.ID, &h.EntryID, &h.ChangedBy, &h.ChangedAt, &h.ChangeType, &h.Snapshot); err != nil {
			return nil, fmt.Errorf("scan entry history: %w", err)
		}
		history = append(history, h)
	}
	return history, nil
}

// -----------------------------------------------------------------------------
// Customer Route Assignments
// -----------------------------------------------------------------------------

func (r *pgRepository) CreateAssignment(ctx context.Context, tx pgx.Tx, a *CustomerRouteAssignment) (*CustomerRouteAssignment, error) {
	query := `
		INSERT INTO customer_route_assignments (
			customer_org_id, plan_id, country_id, priority, effective_from, effective_to,
			status, version, created_by, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, 1, $8, now(), now()
		)
		RETURNING id, customer_org_id, plan_id, country_id, priority, effective_from, effective_to,
		          status, version, created_by, created_at, updated_at
	`
	q := r.pool.QueryRow
	if tx != nil {
		q = tx.QueryRow
	}

	var res CustomerRouteAssignment
	err := q(ctx, query,
		a.CustomerOrgID, a.PlanID, a.CountryID, a.Priority, a.EffectiveFrom, a.EffectiveTo,
		a.Status, a.CreatedBy,
	).Scan(
		&res.ID, &res.CustomerOrgID, &res.PlanID, &res.CountryID, &res.Priority,
		&res.EffectiveFrom, &res.EffectiveTo, &res.Status, &res.Version, &res.CreatedBy,
		&res.CreatedAt, &res.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, fmt.Errorf("%w: customer assignment already exists for this scope", ErrConflict)
		}
		return nil, fmt.Errorf("create customer assignment: %w", err)
	}
	return &res, nil
}

func (r *pgRepository) UpdateAssignment(ctx context.Context, tx pgx.Tx, a *CustomerRouteAssignment) (*CustomerRouteAssignment, error) {
	query := `
		UPDATE customer_route_assignments
		SET plan_id = $1, country_id = $2, priority = $3, effective_from = $4, effective_to = $5,
		    status = $6, updated_at = now(), version = version + 1
		WHERE id = $7 AND version = $8
		RETURNING id, customer_org_id, plan_id, country_id, priority, effective_from, effective_to,
		          status, version, created_by, created_at, updated_at
	`
	q := r.pool.QueryRow
	if tx != nil {
		q = tx.QueryRow
	}

	var res CustomerRouteAssignment
	err := q(ctx, query,
		a.PlanID, a.CountryID, a.Priority, a.EffectiveFrom, a.EffectiveTo,
		a.Status, a.ID, a.Version,
	).Scan(
		&res.ID, &res.CustomerOrgID, &res.PlanID, &res.CountryID, &res.Priority,
		&res.EffectiveFrom, &res.EffectiveTo, &res.Status, &res.Version, &res.CreatedBy,
		&res.CreatedAt, &res.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrVersionMismatch
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, fmt.Errorf("%w: customer assignment already exists for this scope", ErrConflict)
		}
		return nil, fmt.Errorf("update customer assignment: %w", err)
	}
	return &res, nil
}

func (r *pgRepository) DeleteAssignment(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	query := `UPDATE customer_route_assignments SET status = 'archived', version = version + 1, updated_at = now() WHERE id = $1`
	q := r.pool.Exec
	if tx != nil {
		q = tx.Exec
	}
	ct, err := q(ctx, query, id)
	if err != nil {
		return fmt.Errorf("archive assignment: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *pgRepository) GetAssignmentByID(ctx context.Context, id uuid.UUID) (*CustomerRouteAssignment, error) {
	query := `
		SELECT a.id, a.customer_org_id, a.plan_id, a.country_id, a.priority,
		       a.effective_from, a.effective_to, a.status, a.version, a.created_by,
		       a.created_at, a.updated_at,
		       o.name AS customer_name, rp.name AS plan_name, COALESCE(c.name, '') AS country_name
		FROM customer_route_assignments a
		JOIN organizations o ON o.id = a.customer_org_id
		JOIN route_plans rp ON rp.id = a.plan_id
		LEFT JOIN countries c ON c.id = a.country_id
		WHERE a.id = $1
	`
	var a CustomerRouteAssignment
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&a.ID, &a.CustomerOrgID, &a.PlanID, &a.CountryID, &a.Priority,
		&a.EffectiveFrom, &a.EffectiveTo, &a.Status, &a.Version, &a.CreatedBy,
		&a.CreatedAt, &a.UpdatedAt,
		&a.CustomerName, &a.PlanName, &a.CountryName,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get customer assignment by id: %w", err)
	}
	return &a, nil
}

func (r *pgRepository) ListAssignments(ctx context.Context, customerOrgID *uuid.UUID, page, pageSize int) ([]CustomerRouteAssignment, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	baseWhere := "WHERE a.status != 'archived'"
	var args []interface{}
	argIdx := 1
	if customerOrgID != nil {
		baseWhere += fmt.Sprintf(" AND a.customer_org_id = $%d", argIdx)
		args = append(args, *customerOrgID)
		argIdx++
	}

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM customer_route_assignments a %s", baseWhere)
	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count customer assignments: %w", err)
	}

	query := fmt.Sprintf(`
		SELECT a.id, a.customer_org_id, a.plan_id, a.country_id, a.priority,
		       a.effective_from, a.effective_to, a.status, a.version, a.created_by,
		       a.created_at, a.updated_at,
		       o.name AS customer_name, rp.name AS plan_name, COALESCE(c.name, '') AS country_name
		FROM customer_route_assignments a
		JOIN organizations o ON o.id = a.customer_org_id
		JOIN route_plans rp ON rp.id = a.plan_id
		LEFT JOIN countries c ON c.id = a.country_id
		%s
		ORDER BY a.priority ASC, a.created_at DESC
		LIMIT $%d OFFSET $%d
	`, baseWhere, argIdx, argIdx+1)

	args = append(args, pageSize, offset)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list customer assignments: %w", err)
	}
	defer rows.Close()

	var assignments []CustomerRouteAssignment
	for rows.Next() {
		var a CustomerRouteAssignment
		if err := rows.Scan(
			&a.ID, &a.CustomerOrgID, &a.PlanID, &a.CountryID, &a.Priority,
			&a.EffectiveFrom, &a.EffectiveTo, &a.Status, &a.Version, &a.CreatedBy,
			&a.CreatedAt, &a.UpdatedAt,
			&a.CustomerName, &a.PlanName, &a.CountryName,
		); err != nil {
			return nil, 0, fmt.Errorf("scan customer assignment: %w", err)
		}
		assignments = append(assignments, a)
	}

	return assignments, total, nil
}

func (r *pgRepository) RecordAssignmentHistory(ctx context.Context, tx pgx.Tx, assignmentID, changedBy uuid.UUID, changeType string, snapshot any) error {
	snapBytes, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal assignment snapshot: %w", err)
	}

	query := `
		INSERT INTO customer_route_assignment_history (assignment_id, changed_by, changed_at, change_type, snapshot)
		VALUES ($1, $2, now(), $3, $4)
	`
	q := r.pool.Exec
	if tx != nil {
		q = tx.Exec
	}

	_, err = q(ctx, query, assignmentID, changedBy, changeType, snapBytes)
	if err != nil {
		return fmt.Errorf("record assignment history: %w", err)
	}
	return nil
}

func (r *pgRepository) GetAssignmentHistory(ctx context.Context, assignmentID uuid.UUID) ([]CustomerRouteAssignmentHistory, error) {
	query := `
		SELECT id, assignment_id, changed_by, changed_at, change_type, snapshot
		FROM customer_route_assignment_history
		WHERE assignment_id = $1
		ORDER BY changed_at DESC
	`
	rows, err := r.pool.Query(ctx, query, assignmentID)
	if err != nil {
		return nil, fmt.Errorf("get assignment history: %w", err)
	}
	defer rows.Close()

	var history []CustomerRouteAssignmentHistory
	for rows.Next() {
		var h CustomerRouteAssignmentHistory
		if err := rows.Scan(&h.ID, &h.AssignmentID, &h.ChangedBy, &h.ChangedAt, &h.ChangeType, &h.Snapshot); err != nil {
			return nil, fmt.Errorf("scan assignment history: %w", err)
		}
		history = append(history, h)
	}
	return history, nil
}

// -----------------------------------------------------------------------------
// Routing Rules
// -----------------------------------------------------------------------------

func (r *pgRepository) CreateRule(ctx context.Context, tx pgx.Tx, rule *RoutingRule) (*RoutingRule, error) {
	condBytes, err := json.Marshal(rule.Conditions)
	if err != nil {
		return nil, fmt.Errorf("marshal rule conditions: %w", err)
	}

	query := `
		INSERT INTO routing_rules (
			name, priority, status, effective_from, effective_to, conditions,
			action_type, action_connection_id, action_block_reason_code, version,
			created_by, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, 1, $10, now(), now()
		)
		RETURNING id, name, priority, status, effective_from, effective_to, conditions,
		          action_type, action_connection_id, COALESCE(action_block_reason_code, ''),
		          version, created_by, created_at, updated_at
	`
	q := r.pool.QueryRow
	if tx != nil {
		q = tx.QueryRow
	}

	var res RoutingRule
	var resCondBytes []byte
	err = q(ctx, query,
		rule.Name, rule.Priority, rule.Status, rule.EffectiveFrom, rule.EffectiveTo, condBytes,
		rule.ActionType, rule.ActionConnectionID, rule.ActionBlockReason, rule.CreatedBy,
	).Scan(
		&res.ID, &res.Name, &res.Priority, &res.Status, &res.EffectiveFrom, &res.EffectiveTo,
		&resCondBytes, &res.ActionType, &res.ActionConnectionID, &res.ActionBlockReason,
		&res.Version, &res.CreatedBy, &res.CreatedAt, &res.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create routing rule: %w", err)
	}
	if len(resCondBytes) > 0 {
		_ = json.Unmarshal(resCondBytes, &res.Conditions)
	}
	return &res, nil
}

func (r *pgRepository) UpdateRule(ctx context.Context, tx pgx.Tx, rule *RoutingRule) (*RoutingRule, error) {
	condBytes, err := json.Marshal(rule.Conditions)
	if err != nil {
		return nil, fmt.Errorf("marshal rule conditions: %w", err)
	}

	query := `
		UPDATE routing_rules
		SET name = $1, priority = $2, status = $3, effective_from = $4, effective_to = $5,
		    conditions = $6, action_type = $7, action_connection_id = $8,
		    action_block_reason_code = $9, updated_at = now(), version = version + 1
		WHERE id = $10 AND version = $11
		RETURNING id, name, priority, status, effective_from, effective_to, conditions,
		          action_type, action_connection_id, COALESCE(action_block_reason_code, ''),
		          version, created_by, created_at, updated_at
	`
	q := r.pool.QueryRow
	if tx != nil {
		q = tx.QueryRow
	}

	var res RoutingRule
	var resCondBytes []byte
	err = q(ctx, query,
		rule.Name, rule.Priority, rule.Status, rule.EffectiveFrom, rule.EffectiveTo, condBytes,
		rule.ActionType, rule.ActionConnectionID, rule.ActionBlockReason, rule.ID, rule.Version,
	).Scan(
		&res.ID, &res.Name, &res.Priority, &res.Status, &res.EffectiveFrom, &res.EffectiveTo,
		&resCondBytes, &res.ActionType, &res.ActionConnectionID, &res.ActionBlockReason,
		&res.Version, &res.CreatedBy, &res.CreatedAt, &res.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrVersionMismatch
		}
		return nil, fmt.Errorf("update routing rule: %w", err)
	}
	if len(resCondBytes) > 0 {
		_ = json.Unmarshal(resCondBytes, &res.Conditions)
	}
	return &res, nil
}

func (r *pgRepository) DeleteRule(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	query := `UPDATE routing_rules SET status = 'archived', version = version + 1, updated_at = now() WHERE id = $1`
	q := r.pool.Exec
	if tx != nil {
		q = tx.Exec
	}
	ct, err := q(ctx, query, id)
	if err != nil {
		return fmt.Errorf("archive routing rule: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *pgRepository) GetRuleByID(ctx context.Context, id uuid.UUID) (*RoutingRule, error) {
	query := `
		SELECT id, name, priority, status, effective_from, effective_to, conditions,
		       action_type, action_connection_id, COALESCE(action_block_reason_code, ''),
		       version, created_by, created_at, updated_at
		FROM routing_rules
		WHERE id = $1
	`
	var res RoutingRule
	var resCondBytes []byte
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&res.ID, &res.Name, &res.Priority, &res.Status, &res.EffectiveFrom, &res.EffectiveTo,
		&resCondBytes, &res.ActionType, &res.ActionConnectionID, &res.ActionBlockReason,
		&res.Version, &res.CreatedBy, &res.CreatedAt, &res.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get routing rule by id: %w", err)
	}
	if len(resCondBytes) > 0 {
		_ = json.Unmarshal(resCondBytes, &res.Conditions)
	}
	return &res, nil
}

func (r *pgRepository) ListRules(ctx context.Context, status string, page, pageSize int) ([]RoutingRule, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	baseWhere := "WHERE status != 'archived'"
	var args []interface{}
	argIdx := 1
	if status != "" {
		baseWhere += fmt.Sprintf(" AND status = $%d", argIdx)
		args = append(args, status)
		argIdx++
	}

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM routing_rules %s", baseWhere)
	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count routing rules: %w", err)
	}

	query := fmt.Sprintf(`
		SELECT id, name, priority, status, effective_from, effective_to, conditions,
		       action_type, action_connection_id, COALESCE(action_block_reason_code, ''),
		       version, created_by, created_at, updated_at
		FROM routing_rules
		%s
		ORDER BY priority ASC, id ASC
		LIMIT $%d OFFSET $%d
	`, baseWhere, argIdx, argIdx+1)

	args = append(args, pageSize, offset)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list routing rules: %w", err)
	}
	defer rows.Close()

	var rules []RoutingRule
	for rows.Next() {
		var res RoutingRule
		var resCondBytes []byte
		if err := rows.Scan(
			&res.ID, &res.Name, &res.Priority, &res.Status, &res.EffectiveFrom, &res.EffectiveTo,
			&resCondBytes, &res.ActionType, &res.ActionConnectionID, &res.ActionBlockReason,
			&res.Version, &res.CreatedBy, &res.CreatedAt, &res.UpdatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan routing rule: %w", err)
		}
		if len(resCondBytes) > 0 {
			_ = json.Unmarshal(resCondBytes, &res.Conditions)
		}
		rules = append(rules, res)
	}

	return rules, total, nil
}

func (r *pgRepository) RecordRuleHistory(ctx context.Context, tx pgx.Tx, ruleID, changedBy uuid.UUID, changeType string, snapshot any) error {
	snapBytes, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal rule snapshot: %w", err)
	}

	query := `
		INSERT INTO routing_rule_history (rule_id, changed_by, changed_at, change_type, snapshot)
		VALUES ($1, $2, now(), $3, $4)
	`
	q := r.pool.Exec
	if tx != nil {
		q = tx.Exec
	}

	_, err = q(ctx, query, ruleID, changedBy, changeType, snapBytes)
	if err != nil {
		return fmt.Errorf("record rule history: %w", err)
	}
	return nil
}

func (r *pgRepository) GetRuleHistory(ctx context.Context, ruleID uuid.UUID) ([]RoutingRuleHistory, error) {
	query := `
		SELECT id, rule_id, changed_by, changed_at, change_type, snapshot
		FROM routing_rule_history
		WHERE rule_id = $1
		ORDER BY changed_at DESC
	`
	rows, err := r.pool.Query(ctx, query, ruleID)
	if err != nil {
		return nil, fmt.Errorf("get rule history: %w", err)
	}
	defer rows.Close()

	var history []RoutingRuleHistory
	for rows.Next() {
		var h RoutingRuleHistory
		if err := rows.Scan(&h.ID, &h.RuleID, &h.ChangedBy, &h.ChangedAt, &h.ChangeType, &h.Snapshot); err != nil {
			return nil, fmt.Errorf("scan rule history: %w", err)
		}
		history = append(history, h)
	}
	return history, nil
}

// -----------------------------------------------------------------------------
// Connection Overrides (Kill Switch)
// -----------------------------------------------------------------------------

func (r *pgRepository) SetConnectionOverride(ctx context.Context, tx pgx.Tx, o *ConnectionOverride) (*ConnectionOverride, error) {
	query := `
		INSERT INTO provider_connection_overrides (connection_id, state, reason, set_by, set_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (connection_id) DO UPDATE
		SET state = EXCLUDED.state, reason = EXCLUDED.reason, set_by = EXCLUDED.set_by, set_at = now()
		RETURNING id, connection_id, state, reason, set_by, set_at
	`
	q := r.pool.QueryRow
	if tx != nil {
		q = tx.QueryRow
	}

	var res ConnectionOverride
	err := q(ctx, query, o.ConnectionID, o.State, o.Reason, o.SetBy).Scan(
		&res.ID, &res.ConnectionID, &res.State, &res.Reason, &res.SetBy, &res.SetAt,
	)
	if err != nil {
		return nil, fmt.Errorf("set connection override: %w", err)
	}
	return &res, nil
}

func (r *pgRepository) RemoveConnectionOverride(ctx context.Context, tx pgx.Tx, connectionID uuid.UUID) error {
	query := `DELETE FROM provider_connection_overrides WHERE connection_id = $1`
	q := r.pool.Exec
	if tx != nil {
		q = tx.Exec
	}
	ct, err := q(ctx, query, connectionID)
	if err != nil {
		return fmt.Errorf("remove connection override: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *pgRepository) GetConnectionOverride(ctx context.Context, connectionID uuid.UUID) (*ConnectionOverride, error) {
	query := `
		SELECT o.id, o.connection_id, o.state, o.reason, o.set_by, o.set_at, c.name AS connection_name
		FROM provider_connection_overrides o
		JOIN provider_connections c ON c.id = o.connection_id
		WHERE o.connection_id = $1
	`
	var o ConnectionOverride
	err := r.pool.QueryRow(ctx, query, connectionID).Scan(
		&o.ID, &o.ConnectionID, &o.State, &o.Reason, &o.SetBy, &o.SetAt, &o.ConnectionName,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get connection override: %w", err)
	}
	return &o, nil
}

func (r *pgRepository) ListConnectionOverrides(ctx context.Context) ([]ConnectionOverride, error) {
	query := `
		SELECT o.id, o.connection_id, o.state, o.reason, o.set_by, o.set_at, c.name AS connection_name
		FROM provider_connection_overrides o
		JOIN provider_connections c ON c.id = o.connection_id
		ORDER BY o.set_at DESC
	`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list connection overrides: %w", err)
	}
	defer rows.Close()

	var overrides []ConnectionOverride
	for rows.Next() {
		var o ConnectionOverride
		if err := rows.Scan(&o.ID, &o.ConnectionID, &o.State, &o.Reason, &o.SetBy, &o.SetAt, &o.ConnectionName); err != nil {
			return nil, fmt.Errorf("scan connection override: %w", err)
		}
		overrides = append(overrides, o)
	}
	return overrides, nil
}

func (r *pgRepository) GetConnectionDetails(ctx context.Context, connectionID uuid.UUID) (*SnapshotConnection, error) {
	query := `
		SELECT c.id, c.provider_id, p.status AS provider_status, c.status AS connection_status
		FROM provider_connections c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.id = $1 AND NOT c.archived AND NOT p.archived
	`
	var sc SnapshotConnection
	err := r.pool.QueryRow(ctx, query, connectionID).Scan(
		&sc.ID, &sc.ProviderID, &sc.ProviderStatus, &sc.ConnectionStatus,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get connection details: %w", err)
	}
	return &sc, nil
}
