package search

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
	"github.com/znero-international/znero/backend/pkg/tenant"
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
		logger: logger.With(slog.String("component", "search_repo")),
	}
}

// Search executes universal multi-entity search scoped strictly to caller's tenant boundary.
func (r *Repository) Search(ctx context.Context, tc *tenant.Context, q string, limit int) ([]*SearchResultItem, error) {
	if limit <= 0 {
		limit = 20
	}
	var results []*SearchResultItem

	// 1. Search Messages
	msgResults, err := r.searchMessages(ctx, tc, q, limit)
	if err == nil {
		results = append(results, msgResults...)
	}

	// 2. Search Organizations / Customers
	orgResults, err := r.searchOrganizations(ctx, tc, q, limit)
	if err == nil {
		results = append(results, orgResults...)
	}

	// 3. Search Providers (Platform Admin ONLY)
	if tc.IsPlatformAdmin {
		provResults, err := r.searchProviders(ctx, q, limit)
		if err == nil {
			results = append(results, provResults...)
		}

		incResults, err := r.searchIncidents(ctx, q, limit)
		if err == nil {
			results = append(results, incResults...)
		}

		alertResults, err := r.searchAlerts(ctx, q, limit)
		if err == nil {
			results = append(results, alertResults...)
		}
	}

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (r *Repository) searchMessages(ctx context.Context, tc *tenant.Context, q string, limit int) ([]*SearchResultItem, error) {
	where := "(destination ILIKE '%' || $1 || '%' OR client_reference = $1"
	args := []interface{}{q}
	argIdx := 2

	if _, err := uuid.Parse(q); err == nil {
		where += fmt.Sprintf(" OR id = $%d", argIdx)
		args = append(args, q)
		argIdx++
	}
	where += ")"

	if !tc.IsPlatformAdmin {
		where += fmt.Sprintf(" AND customer_org_id = $%d", argIdx)
		args = append(args, tc.ActorOrgID)
		argIdx++
	}

	query := fmt.Sprintf(`
		SELECT id, destination, sender_id, status, created_at
		FROM messages
		WHERE %s
		ORDER BY created_at DESC
		LIMIT $%d
	`, where, argIdx)
	args = append(args, limit)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []*SearchResultItem
	for rows.Next() {
		var id uuid.UUID
		var destination, senderID, status string
		var createdAt time.Time
		if err := rows.Scan(&id, &destination, &senderID, &status, &createdAt); err != nil {
			return nil, err
		}
		items = append(items, &SearchResultItem{
			Type:     "message",
			ID:       id.String(),
			Title:    fmt.Sprintf("To: %s (From: %s)", destination, senderID),
			Subtitle: fmt.Sprintf("Status: %s | Sent: %s", status, createdAt.Format("2006-01-02 15:04")),
			URL:      fmt.Sprintf("/messages?id=%s", id),
			Metadata: map[string]interface{}{
				"destination": destination,
				"sender_id":   senderID,
				"status":      status,
			},
		})
	}
	return items, rows.Err()
}

func (r *Repository) searchOrganizations(ctx context.Context, tc *tenant.Context, q string, limit int) ([]*SearchResultItem, error) {
	where := "name ILIKE '%' || $1 || '%'"
	args := []interface{}{q}
	argIdx := 2

	if !tc.IsPlatformAdmin {
		if tc.ActorOrgType == "reseller" {
			where += fmt.Sprintf(" AND path LIKE $%d || '%%'", argIdx)
			args = append(args, tc.ActorOrgPath)
			argIdx++
		} else {
			where += fmt.Sprintf(" AND id = $%d", argIdx)
			args = append(args, tc.ActorOrgID)
			argIdx++
		}
	}

	query := fmt.Sprintf(`
		SELECT id, name, type
		FROM organizations
		WHERE %s
		ORDER BY name ASC
		LIMIT $%d
	`, where, argIdx)
	args = append(args, limit)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []*SearchResultItem
	for rows.Next() {
		var id uuid.UUID
		var name, orgType string
		if err := rows.Scan(&id, &name, &orgType); err != nil {
			return nil, err
		}
		items = append(items, &SearchResultItem{
			Type:     "customer",
			ID:       id.String(),
			Title:    name,
			Subtitle: fmt.Sprintf("Type: %s", orgType),
			URL:      fmt.Sprintf("/customers/%s", id),
			Metadata: map[string]interface{}{
				"org_type": orgType,
			},
		})
	}
	return items, rows.Err()
}

func (r *Repository) searchProviders(ctx context.Context, q string, limit int) ([]*SearchResultItem, error) {
	query := `
		SELECT id, name, code, provider_type
		FROM providers
		WHERE name ILIKE '%' || $1 || '%' OR code ILIKE '%' || $1 || '%'
		ORDER BY name ASC
		LIMIT $2
	`
	rows, err := r.pool.Query(ctx, query, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []*SearchResultItem
	for rows.Next() {
		var id uuid.UUID
		var name, code, provType string
		if err := rows.Scan(&id, &name, &code, &provType); err != nil {
			return nil, err
		}
		items = append(items, &SearchResultItem{
			Type:     "provider",
			ID:       id.String(),
			Title:    fmt.Sprintf("%s (%s)", name, code),
			Subtitle: fmt.Sprintf("Protocol: %s", provType),
			URL:      fmt.Sprintf("/providers/%s", id),
			Metadata: map[string]interface{}{
				"code":          code,
				"provider_type": provType,
			},
		})
	}
	return items, rows.Err()
}

func (r *Repository) searchIncidents(ctx context.Context, q string, limit int) ([]*SearchResultItem, error) {
	query := `
		SELECT id, title, severity, status
		FROM incidents
		WHERE title ILIKE '%' || $1 || '%'
		ORDER BY started_at DESC
		LIMIT $2
	`
	rows, err := r.pool.Query(ctx, query, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []*SearchResultItem
	for rows.Next() {
		var id uuid.UUID
		var title, severity, status string
		if err := rows.Scan(&id, &title, &severity, &status); err != nil {
			return nil, err
		}
		items = append(items, &SearchResultItem{
			Type:     "incident",
			ID:       id.String(),
			Title:    title,
			Subtitle: fmt.Sprintf("Severity: %s | Status: %s", severity, status),
			URL:      fmt.Sprintf("/incidents/%s", id),
		})
	}
	return items, rows.Err()
}

func (r *Repository) searchAlerts(ctx context.Context, q string, limit int) ([]*SearchResultItem, error) {
	query := `
		SELECT id, summary, severity, status
		FROM alerts
		WHERE summary ILIKE '%' || $1 || '%'
		ORDER BY started_at DESC
		LIMIT $2
	`
	rows, err := r.pool.Query(ctx, query, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []*SearchResultItem
	for rows.Next() {
		var id uuid.UUID
		var summary, severity, status string
		if err := rows.Scan(&id, &summary, &severity, &status); err != nil {
			return nil, err
		}
		items = append(items, &SearchResultItem{
			Type:     "alert",
			ID:       id.String(),
			Title:    summary,
			Subtitle: fmt.Sprintf("Severity: %s | Status: %s", severity, status),
			URL:      "/alerts",
		})
	}
	return items, rows.Err()
}

// ==================== Saved Filters ====================

func (r *Repository) ListSavedFilters(ctx context.Context, userID uuid.UUID, viewName string) ([]*SavedFilter, error) {
	query := `
		SELECT id, user_id, view_name, filter_name, query_json, is_default, created_at
		FROM saved_filters
		WHERE user_id = $1 AND ($2 = '' OR view_name = $2)
		ORDER BY is_default DESC, filter_name ASC
	`
	rows, err := r.pool.Query(ctx, query, userID, viewName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*SavedFilter
	for rows.Next() {
		var sf SavedFilter
		var qBytes []byte
		if err := rows.Scan(
			&sf.ID,
			&sf.UserID,
			&sf.ViewName,
			&sf.Name,
			&qBytes,
			&sf.IsDefault,
			&sf.CreatedAt,
		); err != nil {
			return nil, err
		}
		if len(qBytes) > 0 {
			_ = json.Unmarshal(qBytes, &sf.Query)
		}
		list = append(list, &sf)
	}
	return list, rows.Err()
}

func (r *Repository) CreateSavedFilter(ctx context.Context, sf *SavedFilter) error {
	if sf.ID == uuid.Nil {
		sf.ID = uuid.New()
	}
	if sf.CreatedAt.IsZero() {
		sf.CreatedAt = time.Now().UTC()
	}
	qBytes, err := json.Marshal(sf.Query)
	if err != nil {
		qBytes = []byte("{}")
	}

	if sf.IsDefault {
		// Clear previous defaults for this user & view
		_, _ = r.pool.Exec(ctx, `UPDATE saved_filters SET is_default = false WHERE user_id = $1 AND view_name = $2`, sf.UserID, sf.ViewName)
	}

	query := `
		INSERT INTO saved_filters (id, user_id, view_name, filter_name, query_json, is_default, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`
	_, err = r.pool.Exec(ctx, query,
		sf.ID,
		sf.UserID,
		sf.ViewName,
		sf.Name,
		qBytes,
		sf.IsDefault,
		sf.CreatedAt,
	)
	return err
}

func (r *Repository) DeleteSavedFilter(ctx context.Context, id, userID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM saved_filters WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrFilterNotFound
	}
	return nil
}

func (r *Repository) SetDefaultFilter(ctx context.Context, id, userID uuid.UUID) error {
	var viewName string
	err := r.pool.QueryRow(ctx, `SELECT view_name FROM saved_filters WHERE id = $1 AND user_id = $2`, id, userID).Scan(&viewName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrFilterNotFound
		}
		return err
	}

	_, _ = r.pool.Exec(ctx, `UPDATE saved_filters SET is_default = false WHERE user_id = $1 AND view_name = $2`, userID, viewName)
	_, err = r.pool.Exec(ctx, `UPDATE saved_filters SET is_default = true WHERE id = $1 AND user_id = $2`, id, userID)
	return err
}

// ==================== User Preferences ====================

func (r *Repository) GetUserPreferences(ctx context.Context, userID uuid.UUID) (*UserPreference, error) {
	query := `
		SELECT user_id, theme, locale, timezone, dashboard_layout, updated_at
		FROM user_preferences
		WHERE user_id = $1
	`
	var up UserPreference
	var layoutBytes []byte
	err := r.pool.QueryRow(ctx, query, userID).Scan(
		&up.UserID,
		&up.Theme,
		&up.Locale,
		&up.Timezone,
		&layoutBytes,
		&up.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &UserPreference{
				UserID:          userID,
				Theme:           "system",
				Locale:          "en",
				Timezone:        "UTC",
				DashboardLayout: map[string]interface{}{},
				UpdatedAt:       time.Now().UTC(),
			}, nil
		}
		return nil, err
	}
	if len(layoutBytes) > 0 {
		_ = json.Unmarshal(layoutBytes, &up.DashboardLayout)
	}
	return &up, nil
}

func (r *Repository) UpsertUserPreferences(ctx context.Context, up *UserPreference) error {
	layoutBytes, err := json.Marshal(up.DashboardLayout)
	if err != nil {
		layoutBytes = []byte("{}")
	}
	up.UpdatedAt = time.Now().UTC()

	query := `
		INSERT INTO user_preferences (user_id, theme, locale, timezone, dashboard_layout, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id) DO UPDATE
		SET theme = EXCLUDED.theme,
		    locale = EXCLUDED.locale,
		    timezone = EXCLUDED.timezone,
		    dashboard_layout = EXCLUDED.dashboard_layout,
		    updated_at = EXCLUDED.updated_at
	`
	_, err = r.pool.Exec(ctx, query,
		up.UserID,
		up.Theme,
		up.Locale,
		up.Timezone,
		layoutBytes,
		up.UpdatedAt,
	)
	return err
}
