package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

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
		logger: logger.With(slog.String("component", "settings_repo")),
	}
}

func (r *Repository) ListSettings(ctx context.Context, category string) ([]*Setting, error) {
	whereSQL := ""
	args := []interface{}{}
	if category != "" {
		whereSQL = "WHERE category = $1"
		args = append(args, category)
	}

	query := fmt.Sprintf(`
		SELECT key, value_json, value_type, category, COALESCE(description, ''), is_secret, updated_by_user_id, updated_at
		FROM settings_registry
		%s
		ORDER BY category ASC, key ASC
	`, whereSQL)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*Setting
	for rows.Next() {
		var s Setting
		var valBytes []byte
		if err := rows.Scan(
			&s.Key,
			&valBytes,
			&s.ValueType,
			&s.Category,
			&s.Description,
			&s.IsSecret,
			&s.UpdatedByUserID,
			&s.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if len(valBytes) > 0 {
			var parsed any
			if err := json.Unmarshal(valBytes, &parsed); err == nil {
				s.Value = parsed
			} else {
				s.Value = string(valBytes)
			}
		}
		list = append(list, &s)
	}
	return list, rows.Err()
}

func (r *Repository) GetSetting(ctx context.Context, key string) (*Setting, error) {
	query := `
		SELECT key, value_json, value_type, category, COALESCE(description, ''), is_secret, updated_by_user_id, updated_at
		FROM settings_registry
		WHERE key = $1
	`
	var s Setting
	var valBytes []byte
	err := r.pool.QueryRow(ctx, query, key).Scan(
		&s.Key,
		&valBytes,
		&s.ValueType,
		&s.Category,
		&s.Description,
		&s.IsSecret,
		&s.UpdatedByUserID,
		&s.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSettingNotFound
		}
		return nil, err
	}
	if len(valBytes) > 0 {
		var parsed any
		if err := json.Unmarshal(valBytes, &parsed); err == nil {
			s.Value = parsed
		} else {
			s.Value = string(valBytes)
		}
	}
	return &s, nil
}

func (r *Repository) UpsertSetting(ctx context.Context, s *Setting) error {
	valBytes, err := json.Marshal(s.Value)
	if err != nil {
		return err
	}
	s.UpdatedAt = time.Now().UTC()

	query := `
		INSERT INTO settings_registry (
			key, value_json, value_type, category, description, is_secret, updated_by_user_id, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (key) DO UPDATE
		SET value_json = EXCLUDED.value_json,
		    updated_by_user_id = EXCLUDED.updated_by_user_id,
		    updated_at = EXCLUDED.updated_at
	`
	_, err = r.pool.Exec(ctx, query,
		s.Key,
		valBytes,
		s.ValueType,
		s.Category,
		s.Description,
		s.IsSecret,
		s.UpdatedByUserID,
		s.UpdatedAt,
	)
	return err
}

func (r *Repository) ResetSetting(ctx context.Context, key string) error {
	defaults := map[string]struct {
		val any
		typ string
		cat string
	}{
		"system.company_name":              {"Znero International", TypeString, CategoryGeneral},
		"system.support_email":             {"support@znero.com", TypeString, CategoryGeneral},
		"system.default_timezone":          {"UTC", TypeString, CategoryGeneral},
		"system.maintenance_mode":          {false, TypeBoolean, CategoryGeneral},
		"messaging.default_max_tps":        {100, TypeInteger, CategoryMessaging},
		"messaging.default_dlr_wait_hours": {24, TypeInteger, CategoryMessaging},
		"messaging.retention_days":         {90, TypeInteger, CategoryMessaging},
	}

	def, found := defaults[key]
	if !found {
		return fmt.Errorf("%w: no default value registered for %q", ErrSettingNotFound, key)
	}

	valBytes, err := json.Marshal(def.val)
	if err != nil {
		return err
	}

	query := `
		UPDATE settings_registry
		SET value_json = $2,
		    updated_at = now()
		WHERE key = $1
	`
	tag, err := r.pool.Exec(ctx, query, key, valBytes)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSettingNotFound
	}
	return nil
}
