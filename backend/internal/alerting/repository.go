package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

func NewRepository(pool *pgxpool.Pool, logger *slog.Logger) *Repository {
	if logger == nil {
		logger = slog.Default()
	}
	return &Repository{
		pool:   pool,
		logger: logger.With(slog.String("component", "alerting_repo")),
	}
}

// ==================== Alert Rules ====================

func (r *Repository) CreateRule(ctx context.Context, rule *AlertRule) error {
	if rule.ID == uuid.Nil {
		rule.ID = uuid.New()
	}
	now := time.Now().UTC()
	rule.CreatedAt = now
	rule.UpdatedAt = now

	paramsJSON, err := json.Marshal(rule.Params)
	if err != nil {
		paramsJSON = []byte("{}")
	}

	query := `
		INSERT INTO alert_rules (
			id, name, condition_type, threshold, duration_seconds, severity, scope, enabled, params, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`
	_, err = r.pool.Exec(ctx, query,
		rule.ID,
		rule.Name,
		rule.ConditionType,
		rule.Threshold,
		rule.DurationSeconds,
		rule.Severity,
		rule.Scope,
		rule.Enabled,
		paramsJSON,
		rule.CreatedAt,
		rule.UpdatedAt,
	)
	return err
}

func (r *Repository) UpdateRule(ctx context.Context, rule *AlertRule) error {
	rule.UpdatedAt = time.Now().UTC()
	paramsJSON, err := json.Marshal(rule.Params)
	if err != nil {
		paramsJSON = []byte("{}")
	}

	query := `
		UPDATE alert_rules
		SET name = $2,
		    condition_type = $3,
		    threshold = $4,
		    duration_seconds = $5,
		    severity = $6,
		    scope = $7,
		    enabled = $8,
		    params = $9,
		    updated_at = $10
		WHERE id = $1
	`
	tag, err := r.pool.Exec(ctx, query,
		rule.ID,
		rule.Name,
		rule.ConditionType,
		rule.Threshold,
		rule.DurationSeconds,
		rule.Severity,
		rule.Scope,
		rule.Enabled,
		paramsJSON,
		rule.UpdatedAt,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrRuleNotFound
	}
	return nil
}

func (r *Repository) DeleteRule(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM alert_rules WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrRuleNotFound
	}
	return nil
}

func (r *Repository) GetRule(ctx context.Context, id uuid.UUID) (*AlertRule, error) {
	query := `
		SELECT id, name, condition_type, threshold, duration_seconds, severity, scope, enabled, params, created_at, updated_at
		FROM alert_rules
		WHERE id = $1
	`
	var rule AlertRule
	var paramsRaw []byte
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&rule.ID,
		&rule.Name,
		&rule.ConditionType,
		&rule.Threshold,
		&rule.DurationSeconds,
		&rule.Severity,
		&rule.Scope,
		&rule.Enabled,
		&paramsRaw,
		&rule.CreatedAt,
		&rule.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRuleNotFound
		}
		return nil, err
	}
	if len(paramsRaw) > 0 {
		_ = json.Unmarshal(paramsRaw, &rule.Params)
	}
	return &rule, nil
}

func (r *Repository) ListRules(ctx context.Context) ([]*AlertRule, error) {
	query := `
		SELECT id, name, condition_type, threshold, duration_seconds, severity, scope, enabled, params, created_at, updated_at
		FROM alert_rules
		ORDER BY created_at ASC
	`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []*AlertRule
	for rows.Next() {
		var rule AlertRule
		var paramsRaw []byte
		if err := rows.Scan(
			&rule.ID,
			&rule.Name,
			&rule.ConditionType,
			&rule.Threshold,
			&rule.DurationSeconds,
			&rule.Severity,
			&rule.Scope,
			&rule.Enabled,
			&paramsRaw,
			&rule.CreatedAt,
			&rule.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if len(paramsRaw) > 0 {
			_ = json.Unmarshal(paramsRaw, &rule.Params)
		}
		rules = append(rules, &rule)
	}
	return rules, rows.Err()
}

func (r *Repository) ListActiveRules(ctx context.Context) ([]*AlertRule, error) {
	query := `
		SELECT id, name, condition_type, threshold, duration_seconds, severity, scope, enabled, params, created_at, updated_at
		FROM alert_rules
		WHERE enabled = true
		ORDER BY created_at ASC
	`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []*AlertRule
	for rows.Next() {
		var rule AlertRule
		var paramsRaw []byte
		if err := rows.Scan(
			&rule.ID,
			&rule.Name,
			&rule.ConditionType,
			&rule.Threshold,
			&rule.DurationSeconds,
			&rule.Severity,
			&rule.Scope,
			&rule.Enabled,
			&paramsRaw,
			&rule.CreatedAt,
			&rule.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if len(paramsRaw) > 0 {
			_ = json.Unmarshal(paramsRaw, &rule.Params)
		}
		rules = append(rules, &rule)
	}
	return rules, rows.Err()
}

// ==================== Alerts ====================

func (r *Repository) CreateAlert(ctx context.Context, a *Alert) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	now := time.Now().UTC()
	if a.StartedAt.IsZero() {
		a.StartedAt = now
	}
	a.CreatedAt = now
	a.UpdatedAt = now

	labelsJSON, err := json.Marshal(a.Labels)
	if err != nil {
		labelsJSON = []byte("{}")
	}

	query := `
		INSERT INTO alerts (
			id, rule_id, status, severity, summary, description, labels, started_at, resolved_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`
	_, err = r.pool.Exec(ctx, query,
		a.ID,
		a.RuleID,
		a.Status,
		a.Severity,
		a.Summary,
		a.Description,
		labelsJSON,
		a.StartedAt,
		a.ResolvedAt,
		a.CreatedAt,
		a.UpdatedAt,
	)
	return err
}

func (r *Repository) GetActiveAlertByRule(ctx context.Context, ruleID uuid.UUID) (*Alert, error) {
	query := `
		SELECT id, rule_id, status, severity, summary, description, labels, started_at, resolved_at, created_at, updated_at
		FROM alerts
		WHERE rule_id = $1 AND status IN ('firing', 'silenced')
		ORDER BY started_at DESC
		LIMIT 1
	`
	var a Alert
	var labelsRaw []byte
	err := r.pool.QueryRow(ctx, query, ruleID).Scan(
		&a.ID,
		&a.RuleID,
		&a.Status,
		&a.Severity,
		&a.Summary,
		&a.Description,
		&labelsRaw,
		&a.StartedAt,
		&a.ResolvedAt,
		&a.CreatedAt,
		&a.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if len(labelsRaw) > 0 {
		_ = json.Unmarshal(labelsRaw, &a.Labels)
	}
	return &a, nil
}

func (r *Repository) ResolveAlert(ctx context.Context, id uuid.UUID) error {
	now := time.Now().UTC()
	query := `
		UPDATE alerts
		SET status = 'resolved',
		    resolved_at = $2,
		    updated_at = $2
		WHERE id = $1 AND status IN ('firing', 'silenced')
	`
	tag, err := r.pool.Exec(ctx, query, id, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrAlertNotFound
	}
	return nil
}

func (r *Repository) SilenceAlert(ctx context.Context, id uuid.UUID, silenceUntil time.Time, reason string) error {
	now := time.Now().UTC()
	query := `
		UPDATE alerts
		SET status = 'silenced',
		    labels = jsonb_set(labels, '{silenced_until}', to_jsonb($2::text), true) || jsonb_build_object('silence_reason', $3::text),
		    updated_at = $4
		WHERE id = $1 AND status = 'firing'
	`
	tag, err := r.pool.Exec(ctx, query, id, silenceUntil.Format(time.RFC3339), reason, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrAlertNotFound
	}
	return nil
}

func (r *Repository) ListAlerts(ctx context.Context, status string) ([]*Alert, error) {
	whereSQL := ""
	args := []interface{}{}
	if status != "" {
		whereSQL = "WHERE status = $1"
		args = append(args, status)
	}

	query := fmt.Sprintf(`
		SELECT id, rule_id, status, severity, summary, description, labels, started_at, resolved_at, created_at, updated_at
		FROM alerts
		%s
		ORDER BY started_at DESC
		LIMIT 100
	`, whereSQL)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var alerts []*Alert
	for rows.Next() {
		var a Alert
		var labelsRaw []byte
		if err := rows.Scan(
			&a.ID,
			&a.RuleID,
			&a.Status,
			&a.Severity,
			&a.Summary,
			&a.Description,
			&labelsRaw,
			&a.StartedAt,
			&a.ResolvedAt,
			&a.CreatedAt,
			&a.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if len(labelsRaw) > 0 {
			_ = json.Unmarshal(labelsRaw, &a.Labels)
		}
		alerts = append(alerts, &a)
	}
	return alerts, rows.Err()
}

func (r *Repository) CountFiringAlerts(ctx context.Context) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM alerts WHERE status = 'firing'`).Scan(&count)
	return count, err
}

// ==================== Incidents ====================

func (r *Repository) CreateIncident(ctx context.Context, inc *Incident) error {
	if inc.ID == uuid.Nil {
		inc.ID = uuid.New()
	}
	now := time.Now().UTC()
	if inc.StartedAt.IsZero() {
		inc.StartedAt = now
	}
	inc.CreatedAt = now
	inc.UpdatedAt = now

	query := `
		INSERT INTO incidents (
			id, title, severity, status, lead_user_id, root_cause, started_at, resolved_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	_, err := r.pool.Exec(ctx, query,
		inc.ID,
		inc.Title,
		inc.Severity,
		inc.Status,
		inc.LeadUserID,
		inc.RootCause,
		inc.StartedAt,
		inc.ResolvedAt,
		inc.CreatedAt,
		inc.UpdatedAt,
	)
	return err
}

func (r *Repository) GetIncident(ctx context.Context, id uuid.UUID) (*Incident, error) {
	query := `
		SELECT id, title, severity, status, lead_user_id, root_cause, started_at, resolved_at, created_at, updated_at
		FROM incidents
		WHERE id = $1
	`
	var inc Incident
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&inc.ID,
		&inc.Title,
		&inc.Severity,
		&inc.Status,
		&inc.LeadUserID,
		&inc.RootCause,
		&inc.StartedAt,
		&inc.ResolvedAt,
		&inc.CreatedAt,
		&inc.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrIncidentNotFound
		}
		return nil, err
	}
	return &inc, nil
}

func (r *Repository) UpdateIncidentStatus(ctx context.Context, id uuid.UUID, status string, rootCause *string, resolvedAt *time.Time) error {
	now := time.Now().UTC()
	query := `
		UPDATE incidents
		SET status = $2,
		    root_cause = COALESCE($3, root_cause),
		    resolved_at = $4,
		    updated_at = $5
		WHERE id = $1
	`
	tag, err := r.pool.Exec(ctx, query, id, status, rootCause, resolvedAt, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrIncidentNotFound
	}
	return nil
}

func (r *Repository) ListIncidents(ctx context.Context, status, severity string, limit, offset int) ([]*Incident, int64, error) {
	if limit <= 0 {
		limit = 50
	}
	whereClauses := []string{}
	args := []interface{}{}
	argIdx := 1

	if status != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, status)
		argIdx++
	}
	if severity != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("severity = $%d", argIdx))
		args = append(args, severity)
		argIdx++
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + whereClauses[0]
		for i := 1; i < len(whereClauses); i++ {
			whereSQL += " AND " + whereClauses[i]
		}
	}

	query := fmt.Sprintf(`
		SELECT id, title, severity, status, lead_user_id, root_cause, started_at, resolved_at, created_at, updated_at,
		       COUNT(*) OVER() AS total_count
		FROM incidents
		%s
		ORDER BY started_at DESC
		LIMIT $%d OFFSET $%d
	`, whereSQL, argIdx, argIdx+1)

	args = append(args, limit, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var list []*Incident
	var total int64
	for rows.Next() {
		var inc Incident
		if err := rows.Scan(
			&inc.ID,
			&inc.Title,
			&inc.Severity,
			&inc.Status,
			&inc.LeadUserID,
			&inc.RootCause,
			&inc.StartedAt,
			&inc.ResolvedAt,
			&inc.CreatedAt,
			&inc.UpdatedAt,
			&total,
		); err != nil {
			return nil, 0, err
		}
		list = append(list, &inc)
	}
	return list, total, rows.Err()
}

// ==================== Timeline ====================

func (r *Repository) AddTimelineEntry(ctx context.Context, entry *IncidentTimeline) error {
	if entry.ID == uuid.Nil {
		entry.ID = uuid.New()
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}

	query := `
		INSERT INTO incident_timeline (id, incident_id, author_user_id, entry_type, content, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`
	_, err := r.pool.Exec(ctx, query,
		entry.ID,
		entry.IncidentID,
		entry.AuthorUserID,
		entry.EntryType,
		entry.Content,
		entry.CreatedAt,
	)
	return err
}

func (r *Repository) GetIncidentTimeline(ctx context.Context, incidentID uuid.UUID) ([]*IncidentTimeline, error) {
	query := `
		SELECT id, incident_id, author_user_id, entry_type, content, created_at
		FROM incident_timeline
		WHERE incident_id = $1
		ORDER BY created_at ASC
	`
	rows, err := r.pool.Query(ctx, query, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var timeline []*IncidentTimeline
	for rows.Next() {
		var t IncidentTimeline
		if err := rows.Scan(
			&t.ID,
			&t.IncidentID,
			&t.AuthorUserID,
			&t.EntryType,
			&t.Content,
			&t.CreatedAt,
		); err != nil {
			return nil, err
		}
		timeline = append(timeline, &t)
	}
	return timeline, rows.Err()
}
