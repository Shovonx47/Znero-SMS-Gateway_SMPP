package operator

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
	ErrOperatorNotFound = errors.New("operator not found")
	ErrDuplicateMCCMNC  = errors.New("operator with this MCC and MNC already exists for this country")
	ErrVersionNotFound  = errors.New("target version snapshot not found in history")
)

type Repository interface {
	Create(ctx context.Context, tx pgx.Tx, op Operator, actorID *uuid.UUID, reason string) (*Operator, error)
	GetByID(ctx context.Context, id uuid.UUID) (*Operator, error)
	GetByMCCMNC(ctx context.Context, countryID uuid.UUID, mcc, mnc string) (*Operator, error)
	List(ctx context.Context, f Filter) ([]Operator, int, error)
	Update(ctx context.Context, tx pgx.Tx, op Operator, actorID *uuid.UUID, reason string) (*Operator, error)
	SetStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string, actorID *uuid.UUID, reason string) (*Operator, error)
	GetHistory(ctx context.Context, operatorID uuid.UUID) ([]HistoryEntry, error)
	Rollback(ctx context.Context, tx pgx.Tx, id uuid.UUID, targetVersion int, actorID *uuid.UUID, reason string) (*Operator, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

func (r *pgRepository) Create(ctx context.Context, tx pgx.Tx, op Operator, actorID *uuid.UUID, reason string) (*Operator, error) {
	id := op.ID
	if id == uuid.Nil {
		id = uuid.New()
	}
	now := time.Now()
	version := 1
	status := op.Status
	if status == "" {
		status = "active"
	}
	verStatus := op.VerificationStatus
	if verStatus == "" {
		verStatus = "unverified"
	}
	tech := op.NetworkTechnology
	if tech == nil {
		tech = []string{}
	}
	caps := op.Capabilities
	if len(caps) == 0 {
		caps = json.RawMessage("{}")
	}

	query := `
		INSERT INTO operators (
			id, name, brand_name, parent_company, country_id,
			mcc, mnc, network_technology, capabilities,
			data_source, verification_status, effective_date, status,
			version, created_at, updated_at, created_by, updated_by
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9,
			$10, $11, $12, $13,
			$14, $15, $15, $16, $16
		) RETURNING id, name, brand_name, parent_company, country_id,
		            mcc, mnc, network_technology, capabilities,
		            data_source, verification_status, effective_date, status,
		            version, created_at, updated_at, created_by, updated_by
	`

	var execer interface {
		QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	} = r.pool
	if tx != nil {
		execer = tx
	}

	var out Operator
	err := execer.QueryRow(ctx, query,
		id, op.Name, op.BrandName, op.ParentCompany, op.CountryID,
		op.MCC, op.MNC, tech, caps,
		op.DataSource, verStatus, op.EffectiveDate, status,
		version, now, actorID,
	).Scan(
		&out.ID, &out.Name, &out.BrandName, &out.ParentCompany, &out.CountryID,
		&out.MCC, &out.MNC, &out.NetworkTechnology, &out.Capabilities,
		&out.DataSource, &out.VerificationStatus, &out.EffectiveDate, &out.Status,
		&out.Version, &out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
	)
	if err != nil {
		if strings.Contains(err.Error(), "uq_operators_country_mcc_mnc") {
			return nil, ErrDuplicateMCCMNC
		}
		return nil, fmt.Errorf("insert operator: %w", err)
	}

	// Insert initial version into operators_history
	snapshotJSON, _ := json.Marshal(out)
	historyQuery := `
		INSERT INTO operators_history (id, operator_id, version, snapshot, actor_user_id, changed_at, reason)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
	`
	if tx != nil {
		_, err = tx.Exec(ctx, historyQuery, out.ID, out.Version, snapshotJSON, actorID, now, reason)
	} else {
		_, err = r.pool.Exec(ctx, historyQuery, out.ID, out.Version, snapshotJSON, actorID, now, reason)
	}
	if err != nil {
		return nil, fmt.Errorf("insert operator history: %w", err)
	}

	return &out, nil
}

func (r *pgRepository) GetByID(ctx context.Context, id uuid.UUID) (*Operator, error) {
	query := `
		SELECT o.id, o.name, o.brand_name, o.parent_company, o.country_id,
		       c.name AS country_name, c.iso2 AS country_iso2,
		       o.mcc, o.mnc, o.network_technology, o.capabilities,
		       o.data_source, o.verification_status, o.effective_date, o.status,
		       o.version, o.created_at, o.updated_at, o.created_by, o.updated_by
		FROM operators o
		JOIN countries c ON o.country_id = c.id
		WHERE o.id = $1
	`
	var out Operator
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&out.ID, &out.Name, &out.BrandName, &out.ParentCompany, &out.CountryID,
		&out.CountryName, &out.CountryISO2,
		&out.MCC, &out.MNC, &out.NetworkTechnology, &out.Capabilities,
		&out.DataSource, &out.VerificationStatus, &out.EffectiveDate, &out.Status,
		&out.Version, &out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrOperatorNotFound
		}
		return nil, fmt.Errorf("get operator by id: %w", err)
	}
	return &out, nil
}

func (r *pgRepository) GetByMCCMNC(ctx context.Context, countryID uuid.UUID, mcc, mnc string) (*Operator, error) {
	query := `
		SELECT o.id, o.name, o.brand_name, o.parent_company, o.country_id,
		       c.name AS country_name, c.iso2 AS country_iso2,
		       o.mcc, o.mnc, o.network_technology, o.capabilities,
		       o.data_source, o.verification_status, o.effective_date, o.status,
		       o.version, o.created_at, o.updated_at, o.created_by, o.updated_by
		FROM operators o
		JOIN countries c ON o.country_id = c.id
		WHERE o.country_id = $1 AND o.mcc = $2 AND o.mnc = $3
	`
	var out Operator
	err := r.pool.QueryRow(ctx, query, countryID, mcc, mnc).Scan(
		&out.ID, &out.Name, &out.BrandName, &out.ParentCompany, &out.CountryID,
		&out.CountryName, &out.CountryISO2,
		&out.MCC, &out.MNC, &out.NetworkTechnology, &out.Capabilities,
		&out.DataSource, &out.VerificationStatus, &out.EffectiveDate, &out.Status,
		&out.Version, &out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrOperatorNotFound
		}
		return nil, fmt.Errorf("get operator by mcc/mnc: %w", err)
	}
	return &out, nil
}

func (r *pgRepository) List(ctx context.Context, f Filter) ([]Operator, int, error) {
	var where []string
	var args []any
	argIdx := 1

	if f.ActiveOnly {
		where = append(where, "o.status = 'active'")
	} else if f.Status != "" {
		where = append(where, fmt.Sprintf("o.status = $%d", argIdx))
		args = append(args, f.Status)
		argIdx++
	} else {
		where = append(where, "o.status != 'archived'")
	}

	if f.CountryID != nil {
		where = append(where, fmt.Sprintf("o.country_id = $%d", argIdx))
		args = append(args, *f.CountryID)
		argIdx++
	}
	if f.Search != "" {
		where = append(where, fmt.Sprintf("(o.name ILIKE '%%' || $%d || '%%' OR o.brand_name ILIKE '%%' || $%d || '%%' OR c.name ILIKE '%%' || $%d || '%%')", argIdx, argIdx, argIdx))
		args = append(args, f.Search)
		argIdx++
	}
	if f.MCC != "" {
		where = append(where, fmt.Sprintf("o.mcc = $%d", argIdx))
		args = append(args, f.MCC)
		argIdx++
	}
	if f.MNC != "" {
		where = append(where, fmt.Sprintf("o.mnc = $%d", argIdx))
		args = append(args, f.MNC)
		argIdx++
	}
	if f.VerificationStatus != "" {
		where = append(where, fmt.Sprintf("o.verification_status = $%d", argIdx))
		args = append(args, f.VerificationStatus)
		argIdx++
	}
	if f.Technology != "" {
		where = append(where, fmt.Sprintf("$%d = ANY(o.network_technology)", argIdx))
		args = append(args, f.Technology)
		argIdx++
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	countQuery := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM operators o
		JOIN countries c ON o.country_id = c.id
		%s
	`, whereClause)
	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count operators: %w", err)
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

	sortBy := "o.name"
	switch f.SortBy {
	case "mcc", "mnc", "created_at", "status", "version":
		sortBy = "o." + f.SortBy
	case "country_name":
		sortBy = "c.name"
	}
	sortDir := "ASC"
	if strings.ToUpper(f.SortDir) == "DESC" {
		sortDir = "DESC"
	}

	query := fmt.Sprintf(`
		SELECT o.id, o.name, o.brand_name, o.parent_company, o.country_id,
		       c.name AS country_name, c.iso2 AS country_iso2,
		       o.mcc, o.mnc, o.network_technology, o.capabilities,
		       o.data_source, o.verification_status, o.effective_date, o.status,
		       o.version, o.created_at, o.updated_at, o.created_by, o.updated_by
		FROM operators o
		JOIN countries c ON o.country_id = c.id
		%s
		ORDER BY %s %s
		LIMIT $%d OFFSET $%d
	`, whereClause, sortBy, sortDir, argIdx, argIdx+1)

	args = append(args, pageSize, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list operators: %w", err)
	}
	defer rows.Close()

	var items []Operator
	for rows.Next() {
		var out Operator
		if err := rows.Scan(
			&out.ID, &out.Name, &out.BrandName, &out.ParentCompany, &out.CountryID,
			&out.CountryName, &out.CountryISO2,
			&out.MCC, &out.MNC, &out.NetworkTechnology, &out.Capabilities,
			&out.DataSource, &out.VerificationStatus, &out.EffectiveDate, &out.Status,
			&out.Version, &out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
		); err != nil {
			return nil, 0, fmt.Errorf("scan operator: %w", err)
		}
		items = append(items, out)
	}

	return items, total, nil
}

func (r *pgRepository) Update(ctx context.Context, tx pgx.Tx, op Operator, actorID *uuid.UUID, reason string) (*Operator, error) {
	now := time.Now()
	tech := op.NetworkTechnology
	if tech == nil {
		tech = []string{}
	}
	caps := op.Capabilities
	if len(caps) == 0 {
		caps = json.RawMessage("{}")
	}

	query := `
		UPDATE operators
		SET name = $1, brand_name = $2, parent_company = $3,
		    network_technology = $4, capabilities = $5,
		    data_source = $6, verification_status = $7, effective_date = $8,
		    version = version + 1, updated_at = $9, updated_by = $10
		WHERE id = $11
		RETURNING id, name, brand_name, parent_company, country_id,
		          mcc, mnc, network_technology, capabilities,
		          data_source, verification_status, effective_date, status,
		          version, created_at, updated_at, created_by, updated_by
	`

	var out Operator
	err := tx.QueryRow(ctx, query,
		op.Name, op.BrandName, op.ParentCompany,
		tech, caps, op.DataSource, op.VerificationStatus, op.EffectiveDate,
		now, actorID, op.ID,
	).Scan(
		&out.ID, &out.Name, &out.BrandName, &out.ParentCompany, &out.CountryID,
		&out.MCC, &out.MNC, &out.NetworkTechnology, &out.Capabilities,
		&out.DataSource, &out.VerificationStatus, &out.EffectiveDate, &out.Status,
		&out.Version, &out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrOperatorNotFound
		}
		return nil, fmt.Errorf("update operator: %w", err)
	}

	snapshotJSON, _ := json.Marshal(out)
	_, err = tx.Exec(ctx, `
		INSERT INTO operators_history (id, operator_id, version, snapshot, actor_user_id, changed_at, reason)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
	`, out.ID, out.Version, snapshotJSON, actorID, now, reason)
	if err != nil {
		return nil, fmt.Errorf("insert operator history: %w", err)
	}

	return &out, nil
}

func (r *pgRepository) SetStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string, actorID *uuid.UUID, reason string) (*Operator, error) {
	now := time.Now()
	query := `
		UPDATE operators
		SET status = $1, version = version + 1, updated_at = $2, updated_by = $3
		WHERE id = $4
		RETURNING id, name, brand_name, parent_company, country_id,
		          mcc, mnc, network_technology, capabilities,
		          data_source, verification_status, effective_date, status,
		          version, created_at, updated_at, created_by, updated_by
	`
	var out Operator
	err := tx.QueryRow(ctx, query, status, now, actorID, id).Scan(
		&out.ID, &out.Name, &out.BrandName, &out.ParentCompany, &out.CountryID,
		&out.MCC, &out.MNC, &out.NetworkTechnology, &out.Capabilities,
		&out.DataSource, &out.VerificationStatus, &out.EffectiveDate, &out.Status,
		&out.Version, &out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrOperatorNotFound
		}
		return nil, fmt.Errorf("set operator status: %w", err)
	}

	snapshotJSON, _ := json.Marshal(out)
	_, err = tx.Exec(ctx, `
		INSERT INTO operators_history (id, operator_id, version, snapshot, actor_user_id, changed_at, reason)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
	`, out.ID, out.Version, snapshotJSON, actorID, now, reason)
	if err != nil {
		return nil, fmt.Errorf("insert operator history: %w", err)
	}

	return &out, nil
}

func (r *pgRepository) GetHistory(ctx context.Context, operatorID uuid.UUID) ([]HistoryEntry, error) {
	query := `
		SELECT id, operator_id, version, snapshot, actor_user_id, changed_at, reason
		FROM operators_history
		WHERE operator_id = $1
		ORDER BY version DESC
	`
	rows, err := r.pool.Query(ctx, query, operatorID)
	if err != nil {
		return nil, fmt.Errorf("query operator history: %w", err)
	}
	defer rows.Close()

	var history []HistoryEntry
	for rows.Next() {
		var h HistoryEntry
		var snapshotBytes []byte
		if err := rows.Scan(
			&h.ID, &h.OperatorID, &h.Version, &snapshotBytes, &h.ActorUserID, &h.ChangedAt, &h.Reason,
		); err != nil {
			return nil, fmt.Errorf("scan history entry: %w", err)
		}
		_ = json.Unmarshal(snapshotBytes, &h.Snapshot)
		history = append(history, h)
	}
	return history, nil
}

func (r *pgRepository) Rollback(ctx context.Context, tx pgx.Tx, id uuid.UUID, targetVersion int, actorID *uuid.UUID, reason string) (*Operator, error) {
	var snapshotBytes []byte
	err := tx.QueryRow(ctx, `
		SELECT snapshot FROM operators_history WHERE operator_id = $1 AND version = $2
	`, id, targetVersion).Scan(&snapshotBytes)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrVersionNotFound
		}
		return nil, fmt.Errorf("find rollback operator snapshot: %w", err)
	}

	var target Operator
	if err := json.Unmarshal(snapshotBytes, &target); err != nil {
		return nil, fmt.Errorf("unmarshal target operator snapshot: %w", err)
	}

	now := time.Now()
	query := `
		UPDATE operators
		SET name = $1, brand_name = $2, parent_company = $3,
		    network_technology = $4, capabilities = $5,
		    data_source = $6, verification_status = $7, effective_date = $8, status = $9,
		    version = version + 1, updated_at = $10, updated_by = $11
		WHERE id = $12
		RETURNING id, name, brand_name, parent_company, country_id,
		          mcc, mnc, network_technology, capabilities,
		          data_source, verification_status, effective_date, status,
		          version, created_at, updated_at, created_by, updated_by
	`
	var out Operator
	err = tx.QueryRow(ctx, query,
		target.Name, target.BrandName, target.ParentCompany,
		target.NetworkTechnology, target.Capabilities,
		target.DataSource, target.VerificationStatus, target.EffectiveDate, target.Status,
		now, actorID, id,
	).Scan(
		&out.ID, &out.Name, &out.BrandName, &out.ParentCompany, &out.CountryID,
		&out.MCC, &out.MNC, &out.NetworkTechnology, &out.Capabilities,
		&out.DataSource, &out.VerificationStatus, &out.EffectiveDate, &out.Status,
		&out.Version, &out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
	)
	if err != nil {
		return nil, fmt.Errorf("rollback operator record: %w", err)
	}

	newSnapshotJSON, _ := json.Marshal(out)
	rollbackReason := reason
	if rollbackReason == "" {
		rollbackReason = fmt.Sprintf("Rolled back operator to snapshot from version %d", targetVersion)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO operators_history (id, operator_id, version, snapshot, actor_user_id, changed_at, reason)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
	`, out.ID, out.Version, newSnapshotJSON, actorID, now, rollbackReason)
	if err != nil {
		return nil, fmt.Errorf("record operator rollback history snapshot: %w", err)
	}

	return &out, nil
}
