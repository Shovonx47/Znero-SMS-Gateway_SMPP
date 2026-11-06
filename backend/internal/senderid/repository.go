package senderid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type OrgInfo struct {
	ID       uuid.UUID
	Name     string
	Type     string
	ParentID *uuid.UUID
	Path     string
}

type Repository interface {
	BeginTx(ctx context.Context) (pgx.Tx, error)
	GetOrgInfo(ctx context.Context, orgID uuid.UUID) (*OrgInfo, error)

	GetByID(ctx context.Context, id uuid.UUID) (*SenderID, error)
	List(ctx context.Context, filter ListFilter) ([]SenderID, int, error)
	Create(ctx context.Context, tx pgx.Tx, s *SenderID, reason string) (*SenderID, error)
	Update(ctx context.Context, tx pgx.Tx, s *SenderID, reason string) (*SenderID, error)
	ListHistory(ctx context.Context, id uuid.UUID) ([]SenderIDHistory, error)
	GetHistoryVersion(ctx context.Context, id uuid.UUID, version int) (*SenderIDHistory, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

func (r *pgRepository) BeginTx(ctx context.Context) (pgx.Tx, error) {
	return r.pool.Begin(ctx)
}

func (r *pgRepository) GetOrgInfo(ctx context.Context, orgID uuid.UUID) (*OrgInfo, error) {
	var info OrgInfo
	err := r.pool.QueryRow(ctx, `SELECT id, name, type, parent_id, path FROM organizations WHERE id = $1`, orgID).
		Scan(&info.ID, &info.Name, &info.Type, &info.ParentID, &info.Path)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotCustomerOrg
		}
		return nil, err
	}
	return &info, nil
}

const selectCols = `
	s.id, s.organization_id, o.name,
	s.sender_id, s.sender_type,
	s.country_id, c.iso2, c.name, c.sender_id_policy_notes,
	s.status, s.approval_notes, s.effective_from, s.effective_to, s.data_source, s.archived,
	s.version, s.created_at, s.updated_at, s.created_by, s.updated_by
`

const fromJoins = `
	FROM sender_ids s
	JOIN organizations o ON o.id = s.organization_id
	LEFT JOIN countries c ON c.id = s.country_id
`

func scanSenderID(row pgx.Row) (*SenderID, error) {
	var s SenderID
	err := row.Scan(
		&s.ID, &s.OrganizationID, &s.OrganizationName,
		&s.SenderID, &s.SenderType,
		&s.CountryID, &s.CountryISO2, &s.CountryName, &s.CountrySenderIDPolicyNotes,
		&s.Status, &s.ApprovalNotes, &s.EffectiveFrom, &s.EffectiveTo, &s.DataSource, &s.Archived,
		&s.Version, &s.CreatedAt, &s.UpdatedAt, &s.CreatedBy, &s.UpdatedBy,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSenderIDNotFound
		}
		return nil, err
	}
	return &s, nil
}

func (r *pgRepository) GetByID(ctx context.Context, id uuid.UUID) (*SenderID, error) {
	q := `SELECT ` + selectCols + fromJoins + ` WHERE s.id = $1`
	return scanSenderID(r.pool.QueryRow(ctx, q, id))
}

func (r *pgRepository) List(ctx context.Context, filter ListFilter) ([]SenderID, int, error) {
	var where []string
	var args []any
	idx := 1

	if !filter.IncludeArchived {
		where = append(where, "s.archived = false")
	}
	if filter.OrganizationID != nil {
		where = append(where, fmt.Sprintf("s.organization_id = $%d", idx))
		args = append(args, *filter.OrganizationID)
		idx++
	}
	if filter.ParentOrgID != nil {
		where = append(where, fmt.Sprintf("o.parent_id = $%d", idx))
		args = append(args, *filter.ParentOrgID)
		idx++
	}
	if filter.CountryID != nil {
		where = append(where, fmt.Sprintf("s.country_id = $%d", idx))
		args = append(args, *filter.CountryID)
		idx++
	}
	if filter.SenderType != "" {
		where = append(where, fmt.Sprintf("s.sender_type = $%d", idx))
		args = append(args, filter.SenderType)
		idx++
	}
	if filter.Status != "" {
		where = append(where, fmt.Sprintf("s.status = $%d", idx))
		args = append(args, filter.Status)
		idx++
	}
	if filter.Search != "" {
		where = append(where, fmt.Sprintf("(s.sender_id ILIKE $%d OR o.name ILIKE $%d OR COALESCE(c.iso2, '') ILIKE $%d)", idx, idx, idx))
		args = append(args, "%"+strings.TrimSpace(filter.Search)+"%")
		idx++
	}

	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx, "SELECT COUNT(*) "+fromJoins+" "+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count sender_ids: %w", err)
	}

	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	page := filter.Page
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * limit

	q := fmt.Sprintf(`
		SELECT %s %s %s
		ORDER BY s.created_at DESC, s.sender_id ASC
		LIMIT $%d OFFSET $%d`, selectCols, fromJoins, whereSQL, idx, idx+1)
	args = append(args, limit, offset)

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list sender_ids: %w", err)
	}
	defer rows.Close()

	var items []SenderID
	for rows.Next() {
		s, err := scanSenderID(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *s)
	}
	return items, total, nil
}

func (r *pgRepository) Create(ctx context.Context, tx pgx.Tx, s *SenderID, reason string) (*SenderID, error) {
	q := `
		INSERT INTO sender_ids (
			organization_id, sender_id, sender_type, country_id,
			status, approval_notes, effective_from, effective_to, data_source, archived,
			version, created_by, updated_by
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8, $9, $10,
			1, $11, $11
		)
		RETURNING id, version, created_at, updated_at`

	err := tx.QueryRow(ctx, q,
		s.OrganizationID, s.SenderID, s.SenderType, s.CountryID,
		s.Status, s.ApprovalNotes, s.EffectiveFrom, s.EffectiveTo, s.DataSource, s.Archived,
		s.CreatedBy,
	).Scan(&s.ID, &s.Version, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrSenderIDExists
		}
		return nil, fmt.Errorf("insert sender_id: %w", err)
	}
	s.UpdatedBy = s.CreatedBy

	if err := r.insertHistory(ctx, tx, s, s.CreatedBy, reason); err != nil {
		return nil, err
	}
	return s, nil
}

func (r *pgRepository) Update(ctx context.Context, tx pgx.Tx, s *SenderID, reason string) (*SenderID, error) {
	q := `
		UPDATE sender_ids SET
			status = $2, approval_notes = $3, effective_from = $4, effective_to = $5,
			data_source = $6, archived = $7,
			version = version + 1, updated_at = now(), updated_by = $8
		WHERE id = $1
		RETURNING version, updated_at`

	err := tx.QueryRow(ctx, q,
		s.ID, s.Status, s.ApprovalNotes, s.EffectiveFrom, s.EffectiveTo,
		s.DataSource, s.Archived, s.UpdatedBy,
	).Scan(&s.Version, &s.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSenderIDNotFound
		}
		return nil, fmt.Errorf("update sender_id: %w", err)
	}

	if err := r.insertHistory(ctx, tx, s, s.UpdatedBy, reason); err != nil {
		return nil, err
	}
	return s, nil
}

func (r *pgRepository) insertHistory(ctx context.Context, tx pgx.Tx, s *SenderID, actorID *uuid.UUID, reason string) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO sender_ids_history (sender_id_ref, version, snapshot, actor_user_id, reason)
		VALUES ($1, $2, $3, $4, $5)`,
		s.ID, s.Version, raw, actorID, strings.TrimSpace(reason),
	)
	return err
}

func (r *pgRepository) ListHistory(ctx context.Context, id uuid.UUID) ([]SenderIDHistory, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, sender_id_ref, version, snapshot, actor_user_id, changed_at, reason
		FROM sender_ids_history
		WHERE sender_id_ref = $1
		ORDER BY version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SenderIDHistory
	for rows.Next() {
		var h SenderIDHistory
		var raw []byte
		if err := rows.Scan(&h.ID, &h.SenderIDRef, &h.Version, &raw, &h.ActorUserID, &h.ChangedAt, &h.Reason); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &h.Snapshot)
		out = append(out, h)
	}
	return out, nil
}

func (r *pgRepository) GetHistoryVersion(ctx context.Context, id uuid.UUID, version int) (*SenderIDHistory, error) {
	var h SenderIDHistory
	var raw []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id, sender_id_ref, version, snapshot, actor_user_id, changed_at, reason
		FROM sender_ids_history
		WHERE sender_id_ref = $1 AND version = $2`, id, version).
		Scan(&h.ID, &h.SenderIDRef, &h.Version, &raw, &h.ActorUserID, &h.ChangedAt, &h.Reason)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSenderIDNotFound
		}
		return nil, err
	}
	_ = json.Unmarshal(raw, &h.Snapshot)
	return &h, nil
}
