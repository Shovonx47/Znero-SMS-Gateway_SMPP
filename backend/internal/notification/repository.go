package notification

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
		logger: logger.With(slog.String("component", "notification_repo")),
	}
}

// Create stores a notification in the database.
func (r *Repository) Create(ctx context.Context, n *Notification) error {
	if n.ID == uuid.Nil {
		n.ID = uuid.New()
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}

	query := `
		INSERT INTO notifications (
			id, organization_id, user_id, category, severity, title, body, link_url, read_at, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	_, err := r.pool.Exec(ctx, query,
		n.ID,
		n.OrganizationID,
		n.UserID,
		n.Category,
		n.Severity,
		n.Title,
		n.Body,
		n.LinkURL,
		n.ReadAt,
		n.CreatedAt,
	)
	return err
}

// List retrieves paginated notifications filtered by tenant and preferences.
func (r *Repository) List(ctx context.Context, userID, orgID uuid.UUID, isPlatformAdmin bool, filter ListFilter) ([]*Notification, int64, error) {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}

	whereClauses := []string{}
	args := []interface{}{}
	argIdx := 1

	if isPlatformAdmin && userID == uuid.Nil {
		// Global view: platform admin can view all or platform org
		if orgID != uuid.Nil {
			whereClauses = append(whereClauses, fmt.Sprintf("organization_id = $%d", argIdx))
			args = append(args, orgID)
			argIdx++
		}
	} else {
		// Scoped view: specific user or org broadcast
		whereClauses = append(whereClauses, fmt.Sprintf("((user_id = $%d) OR (user_id IS NULL AND organization_id = $%d))", argIdx, argIdx+1))
		args = append(args, userID, orgID)
		argIdx += 2
	}

	if filter.Category != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("category = $%d", argIdx))
		args = append(args, filter.Category)
		argIdx++
	}

	if filter.Severity != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("severity = $%d", argIdx))
		args = append(args, filter.Severity)
		argIdx++
	}

	if filter.IsRead != nil {
		if *filter.IsRead {
			whereClauses = append(whereClauses, "read_at IS NOT NULL")
		} else {
			whereClauses = append(whereClauses, "read_at IS NULL")
		}
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + whereClauses[0]
		for i := 1; i < len(whereClauses); i++ {
			whereSQL += " AND " + whereClauses[i]
		}
	}

	query := fmt.Sprintf(`
		SELECT id, organization_id, user_id, category, severity, title, body, link_url, read_at, created_at,
		       COUNT(*) OVER() AS total_count
		FROM notifications
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d
	`, whereSQL, argIdx, argIdx+1)

	args = append(args, filter.Limit, filter.Offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var list []*Notification
	var totalCount int64

	for rows.Next() {
		var n Notification
		if err := rows.Scan(
			&n.ID,
			&n.OrganizationID,
			&n.UserID,
			&n.Category,
			&n.Severity,
			&n.Title,
			&n.Body,
			&n.LinkURL,
			&n.ReadAt,
			&n.CreatedAt,
			&totalCount,
		); err != nil {
			return nil, 0, err
		}
		list = append(list, &n)
	}

	return list, totalCount, rows.Err()
}

// GetUnreadCount returns the number of unread notifications for a user/org.
func (r *Repository) GetUnreadCount(ctx context.Context, userID, orgID uuid.UUID, isPlatformAdmin bool) (int64, error) {
	var query string
	var args []interface{}

	if isPlatformAdmin && userID == uuid.Nil {
		query = `SELECT COUNT(*) FROM notifications WHERE read_at IS NULL`
	} else {
		query = `
			SELECT COUNT(*)
			FROM notifications
			WHERE ((user_id = $1) OR (user_id IS NULL AND organization_id = $2))
			  AND read_at IS NULL
		`
		args = []interface{}{userID, orgID}
	}

	var count int64
	err := r.pool.QueryRow(ctx, query, args...).Scan(&count)
	return count, err
}

// MarkAsRead marks a single notification as read if owned by the user/org.
func (r *Repository) MarkAsRead(ctx context.Context, id, userID, orgID uuid.UUID, isPlatformAdmin bool) error {
	var query string
	var args []interface{}

	if isPlatformAdmin {
		query = `UPDATE notifications SET read_at = now() WHERE id = $1 AND read_at IS NULL`
		args = []interface{}{id}
	} else {
		query = `
			UPDATE notifications
			SET read_at = now()
			WHERE id = $1
			  AND ((user_id = $2) OR (user_id IS NULL AND organization_id = $3))
			  AND read_at IS NULL
		`
		args = []interface{}{id, userID, orgID}
	}

	tag, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotificationNotFound
	}
	return nil
}

// MarkAllAsRead marks all unread notifications as read for the user/org.
func (r *Repository) MarkAllAsRead(ctx context.Context, userID, orgID uuid.UUID, isPlatformAdmin bool) error {
	var query string
	var args []interface{}

	if isPlatformAdmin && userID == uuid.Nil {
		query = `UPDATE notifications SET read_at = now() WHERE read_at IS NULL`
	} else {
		query = `
			UPDATE notifications
			SET read_at = now()
			WHERE ((user_id = $1) OR (user_id IS NULL AND organization_id = $2))
			  AND read_at IS NULL
		`
		args = []interface{}{userID, orgID}
	}

	_, err := r.pool.Exec(ctx, query, args...)
	return err
}

// Delete removes a notification if owned by the user/org.
func (r *Repository) Delete(ctx context.Context, id, userID, orgID uuid.UUID, isPlatformAdmin bool) error {
	var query string
	var args []interface{}

	if isPlatformAdmin {
		query = `DELETE FROM notifications WHERE id = $1`
		args = []interface{}{id}
	} else {
		query = `
			DELETE FROM notifications
			WHERE id = $1
			  AND ((user_id = $2) OR (user_id IS NULL AND organization_id = $3))
		`
		args = []interface{}{id, userID, orgID}
	}

	tag, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotificationNotFound
	}
	return nil
}

// GetPreferences returns notification preferences for a user, or default preferences.
func (r *Repository) GetPreferences(ctx context.Context, userID uuid.UUID) (*NotificationPreferences, error) {
	query := `
		SELECT id, user_id, email_enabled, webhook_enabled, webhook_url, categories, created_at, updated_at
		FROM notification_preferences
		WHERE user_id = $1
	`
	var p NotificationPreferences
	var catBytes []byte

	err := r.pool.QueryRow(ctx, query, userID).Scan(
		&p.ID,
		&p.UserID,
		&p.EmailEnabled,
		&p.WebhookEnabled,
		&p.WebhookURL,
		&catBytes,
		&p.CreatedAt,
		&p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DefaultPreferences(userID), nil
		}
		return nil, err
	}

	if len(catBytes) > 0 {
		_ = json.Unmarshal(catBytes, &p.Categories)
	}
	if p.Categories == nil {
		p.Categories = DefaultPreferences(userID).Categories
	}
	return &p, nil
}

// UpsertPreferences saves or updates user notification preferences.
func (r *Repository) UpsertPreferences(ctx context.Context, p *NotificationPreferences) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	now := time.Now().UTC()
	p.UpdatedAt = now
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}

	catBytes, err := json.Marshal(p.Categories)
	if err != nil {
		return err
	}

	query := `
		INSERT INTO notification_preferences (
			id, user_id, email_enabled, webhook_enabled, webhook_url, categories, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (user_id) DO UPDATE
		SET email_enabled = EXCLUDED.email_enabled,
		    webhook_enabled = EXCLUDED.webhook_enabled,
		    webhook_url = EXCLUDED.webhook_url,
		    categories = EXCLUDED.categories,
		    updated_at = EXCLUDED.updated_at
	`
	_, err = r.pool.Exec(ctx, query,
		p.ID,
		p.UserID,
		p.EmailEnabled,
		p.WebhookEnabled,
		p.WebhookURL,
		catBytes,
		p.CreatedAt,
		p.UpdatedAt,
	)
	return err
}

// GetUserEmail retrieves user email from users table.
func (r *Repository) GetUserEmail(ctx context.Context, userID uuid.UUID) (string, error) {
	var email string
	query := `SELECT email FROM users WHERE id = $1`
	err := r.pool.QueryRow(ctx, query, userID).Scan(&email)
	if err != nil {
		return "", err
	}
	return email, nil
}
