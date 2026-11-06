package country

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
	ErrCountryNotFound = errors.New("country not found")
	ErrDuplicateISO2   = errors.New("country with this ISO2 code already exists")
	ErrDuplicateISO3   = errors.New("country with this ISO3 code already exists")
	ErrDuplicateNum    = errors.New("country with this numeric code already exists")
	ErrVersionNotFound = errors.New("target version snapshot not found in history")
)

type Repository interface {
	Create(ctx context.Context, tx pgx.Tx, c Country, actorID *uuid.UUID, reason string) (*Country, error)
	GetByID(ctx context.Context, id uuid.UUID) (*Country, error)
	GetByISO2(ctx context.Context, iso2 string) (*Country, error)
	GetByCallingCode(ctx context.Context, code string) ([]Country, error)
	List(ctx context.Context, f Filter) ([]Country, int, error)
	Update(ctx context.Context, tx pgx.Tx, c Country, actorID *uuid.UUID, reason string) (*Country, error)
	SetStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string, actorID *uuid.UUID, reason string) (*Country, error)
	GetHistory(ctx context.Context, countryID uuid.UUID) ([]HistoryEntry, error)
	Rollback(ctx context.Context, tx pgx.Tx, id uuid.UUID, targetVersion int, actorID *uuid.UUID, reason string) (*Country, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

func (r *pgRepository) Create(ctx context.Context, tx pgx.Tx, c Country, actorID *uuid.UUID, reason string) (*Country, error) {
	id := c.ID
	if id == uuid.Nil {
		id = uuid.New()
	}
	now := time.Now()
	version := 1
	status := c.Status
	if status == "" {
		status = "active"
	}
	verStatus := c.VerificationStatus
	if verStatus == "" {
		verStatus = "unverified"
	}
	timezones := c.Timezones
	if timezones == nil {
		timezones = []string{}
	}

	query := `
		INSERT INTO countries (
			id, name, iso2, iso3, numeric_code, calling_code,
			continent, region, currency_code, timezones,
			numbering_plan_notes, min_national_length, max_national_length,
			sender_id_policy_notes, dnd_policy_notes, consent_requirement_notes,
			data_source, verification_date, verification_status, status,
			version, created_at, updated_at, created_by, updated_by
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13,
			$14, $15, $16,
			$17, $18, $19, $20,
			$21, $22, $22, $23, $23
		) RETURNING id, name, iso2, iso3, numeric_code, calling_code,
		            continent, region, currency_code, timezones,
		            numbering_plan_notes, min_national_length, max_national_length,
		            sender_id_policy_notes, dnd_policy_notes, consent_requirement_notes,
		            data_source, verification_date, verification_status, status,
		            version, created_at, updated_at, created_by, updated_by
	`

	var execer interface {
		QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	} = r.pool
	if tx != nil {
		execer = tx
	}

	var out Country
	err := execer.QueryRow(ctx, query,
		id, c.Name, strings.ToUpper(c.ISO2), strings.ToUpper(c.ISO3), c.NumericCode, c.CallingCode,
		c.Continent, c.Region, strings.ToUpper(c.CurrencyCode), timezones,
		c.NumberingPlanNotes, c.MinNationalLength, c.MaxNationalLength,
		c.SenderIDPolicyNotes, c.DNDPolicyNotes, c.ConsentRequirementNotes,
		c.DataSource, c.VerificationDate, verStatus, status,
		version, now, actorID,
	).Scan(
		&out.ID, &out.Name, &out.ISO2, &out.ISO3, &out.NumericCode, &out.CallingCode,
		&out.Continent, &out.Region, &out.CurrencyCode, &out.Timezones,
		&out.NumberingPlanNotes, &out.MinNationalLength, &out.MaxNationalLength,
		&out.SenderIDPolicyNotes, &out.DNDPolicyNotes, &out.ConsentRequirementNotes,
		&out.DataSource, &out.VerificationDate, &out.VerificationStatus, &out.Status,
		&out.Version, &out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
	)
	if err != nil {
		if strings.Contains(err.Error(), "countries_iso2_key") {
			return nil, ErrDuplicateISO2
		}
		if strings.Contains(err.Error(), "countries_iso3_key") {
			return nil, ErrDuplicateISO3
		}
		if strings.Contains(err.Error(), "countries_numeric_code_key") {
			return nil, ErrDuplicateNum
		}
		return nil, fmt.Errorf("insert country: %w", err)
	}

	// Insert initial version into countries_history
	snapshotJSON, _ := json.Marshal(out)
	historyQuery := `
		INSERT INTO countries_history (id, country_id, version, snapshot, actor_user_id, changed_at, reason)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
	`
	if tx != nil {
		_, err = tx.Exec(ctx, historyQuery, out.ID, out.Version, snapshotJSON, actorID, now, reason)
	} else {
		_, err = r.pool.Exec(ctx, historyQuery, out.ID, out.Version, snapshotJSON, actorID, now, reason)
	}
	if err != nil {
		return nil, fmt.Errorf("insert country history: %w", err)
	}

	return &out, nil
}

func (r *pgRepository) GetByID(ctx context.Context, id uuid.UUID) (*Country, error) {
	query := `
		SELECT id, name, iso2, iso3, numeric_code, calling_code,
		       continent, region, currency_code, timezones,
		       numbering_plan_notes, min_national_length, max_national_length,
		       sender_id_policy_notes, dnd_policy_notes, consent_requirement_notes,
		       data_source, verification_date, verification_status, status,
		       version, created_at, updated_at, created_by, updated_by
		FROM countries WHERE id = $1
	`
	var out Country
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&out.ID, &out.Name, &out.ISO2, &out.ISO3, &out.NumericCode, &out.CallingCode,
		&out.Continent, &out.Region, &out.CurrencyCode, &out.Timezones,
		&out.NumberingPlanNotes, &out.MinNationalLength, &out.MaxNationalLength,
		&out.SenderIDPolicyNotes, &out.DNDPolicyNotes, &out.ConsentRequirementNotes,
		&out.DataSource, &out.VerificationDate, &out.VerificationStatus, &out.Status,
		&out.Version, &out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCountryNotFound
		}
		return nil, fmt.Errorf("get country by id: %w", err)
	}
	return &out, nil
}

func (r *pgRepository) GetByISO2(ctx context.Context, iso2 string) (*Country, error) {
	query := `
		SELECT id, name, iso2, iso3, numeric_code, calling_code,
		       continent, region, currency_code, timezones,
		       numbering_plan_notes, min_national_length, max_national_length,
		       sender_id_policy_notes, dnd_policy_notes, consent_requirement_notes,
		       data_source, verification_date, verification_status, status,
		       version, created_at, updated_at, created_by, updated_by
		FROM countries WHERE UPPER(iso2) = UPPER($1)
	`
	var out Country
	err := r.pool.QueryRow(ctx, query, iso2).Scan(
		&out.ID, &out.Name, &out.ISO2, &out.ISO3, &out.NumericCode, &out.CallingCode,
		&out.Continent, &out.Region, &out.CurrencyCode, &out.Timezones,
		&out.NumberingPlanNotes, &out.MinNationalLength, &out.MaxNationalLength,
		&out.SenderIDPolicyNotes, &out.DNDPolicyNotes, &out.ConsentRequirementNotes,
		&out.DataSource, &out.VerificationDate, &out.VerificationStatus, &out.Status,
		&out.Version, &out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCountryNotFound
		}
		return nil, fmt.Errorf("get country by iso2: %w", err)
	}
	return &out, nil
}

func (r *pgRepository) GetByCallingCode(ctx context.Context, code string) ([]Country, error) {
	query := `
		SELECT id, name, iso2, iso3, numeric_code, calling_code,
		       continent, region, currency_code, timezones,
		       numbering_plan_notes, min_national_length, max_national_length,
		       sender_id_policy_notes, dnd_policy_notes, consent_requirement_notes,
		       data_source, verification_date, verification_status, status,
		       version, created_at, updated_at, created_by, updated_by
		FROM countries WHERE calling_code = $1 AND status = 'active'
	`
	rows, err := r.pool.Query(ctx, query, code)
	if err != nil {
		return nil, fmt.Errorf("query countries by calling code: %w", err)
	}
	defer rows.Close()

	var res []Country
	for rows.Next() {
		var out Country
		if err := rows.Scan(
			&out.ID, &out.Name, &out.ISO2, &out.ISO3, &out.NumericCode, &out.CallingCode,
			&out.Continent, &out.Region, &out.CurrencyCode, &out.Timezones,
			&out.NumberingPlanNotes, &out.MinNationalLength, &out.MaxNationalLength,
			&out.SenderIDPolicyNotes, &out.DNDPolicyNotes, &out.ConsentRequirementNotes,
			&out.DataSource, &out.VerificationDate, &out.VerificationStatus, &out.Status,
			&out.Version, &out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
		); err != nil {
			return nil, fmt.Errorf("scan country: %w", err)
		}
		res = append(res, out)
	}
	return res, nil
}

func (r *pgRepository) List(ctx context.Context, f Filter) ([]Country, int, error) {
	var where []string
	var args []any
	argIdx := 1

	if f.ActiveOnly {
		where = append(where, "status = 'active'")
	} else if f.Status != "" {
		where = append(where, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, f.Status)
		argIdx++
	} else {
		// Hide archived by default unless requested
		where = append(where, "status != 'archived'")
	}

	if f.Search != "" {
		where = append(where, fmt.Sprintf("(name ILIKE '%%' || $%d || '%%' OR iso2 ILIKE $%d || '%%' OR iso3 ILIKE $%d || '%%' OR calling_code LIKE $%d || '%%')", argIdx, argIdx, argIdx, argIdx))
		args = append(args, f.Search)
		argIdx++
	}
	if f.Continent != "" {
		where = append(where, fmt.Sprintf("continent = $%d", argIdx))
		args = append(args, f.Continent)
		argIdx++
	}
	if f.CallingCode != "" {
		where = append(where, fmt.Sprintf("calling_code = $%d", argIdx))
		args = append(args, f.CallingCode)
		argIdx++
	}
	if f.VerificationStatus != "" {
		where = append(where, fmt.Sprintf("verification_status = $%d", argIdx))
		args = append(args, f.VerificationStatus)
		argIdx++
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM countries %s", whereClause)
	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count countries: %w", err)
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

	sortBy := "name"
	switch f.SortBy {
	case "iso2", "iso3", "calling_code", "continent", "created_at", "status", "version":
		sortBy = f.SortBy
	}
	sortDir := "ASC"
	if strings.ToUpper(f.SortDir) == "DESC" {
		sortDir = "DESC"
	}

	query := fmt.Sprintf(`
		SELECT id, name, iso2, iso3, numeric_code, calling_code,
		       continent, region, currency_code, timezones,
		       numbering_plan_notes, min_national_length, max_national_length,
		       sender_id_policy_notes, dnd_policy_notes, consent_requirement_notes,
		       data_source, verification_date, verification_status, status,
		       version, created_at, updated_at, created_by, updated_by
		FROM countries
		%s
		ORDER BY %s %s
		LIMIT $%d OFFSET $%d
	`, whereClause, sortBy, sortDir, argIdx, argIdx+1)

	args = append(args, pageSize, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list countries: %w", err)
	}
	defer rows.Close()

	var items []Country
	for rows.Next() {
		var out Country
		if err := rows.Scan(
			&out.ID, &out.Name, &out.ISO2, &out.ISO3, &out.NumericCode, &out.CallingCode,
			&out.Continent, &out.Region, &out.CurrencyCode, &out.Timezones,
			&out.NumberingPlanNotes, &out.MinNationalLength, &out.MaxNationalLength,
			&out.SenderIDPolicyNotes, &out.DNDPolicyNotes, &out.ConsentRequirementNotes,
			&out.DataSource, &out.VerificationDate, &out.VerificationStatus, &out.Status,
			&out.Version, &out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
		); err != nil {
			return nil, 0, fmt.Errorf("scan country: %w", err)
		}
		items = append(items, out)
	}

	return items, total, nil
}

func (r *pgRepository) Update(ctx context.Context, tx pgx.Tx, c Country, actorID *uuid.UUID, reason string) (*Country, error) {
	now := time.Now()
	timezones := c.Timezones
	if timezones == nil {
		timezones = []string{}
	}

	query := `
		UPDATE countries
		SET name = $1, calling_code = $2, continent = $3, region = $4,
		    currency_code = $5, timezones = $6, numbering_plan_notes = $7,
		    min_national_length = $8, max_national_length = $9,
		    sender_id_policy_notes = $10, dnd_policy_notes = $11,
		    consent_requirement_notes = $12, data_source = $13,
		    verification_status = $14,
		    version = version + 1, updated_at = $15, updated_by = $16
		WHERE id = $17
		RETURNING id, name, iso2, iso3, numeric_code, calling_code,
		          continent, region, currency_code, timezones,
		          numbering_plan_notes, min_national_length, max_national_length,
		          sender_id_policy_notes, dnd_policy_notes, consent_requirement_notes,
		          data_source, verification_date, verification_status, status,
		          version, created_at, updated_at, created_by, updated_by
	`

	var out Country
	err := tx.QueryRow(ctx, query,
		c.Name, c.CallingCode, c.Continent, c.Region,
		strings.ToUpper(c.CurrencyCode), timezones, c.NumberingPlanNotes,
		c.MinNationalLength, c.MaxNationalLength,
		c.SenderIDPolicyNotes, c.DNDPolicyNotes,
		c.ConsentRequirementNotes, c.DataSource,
		c.VerificationStatus, now, actorID, c.ID,
	).Scan(
		&out.ID, &out.Name, &out.ISO2, &out.ISO3, &out.NumericCode, &out.CallingCode,
		&out.Continent, &out.Region, &out.CurrencyCode, &out.Timezones,
		&out.NumberingPlanNotes, &out.MinNationalLength, &out.MaxNationalLength,
		&out.SenderIDPolicyNotes, &out.DNDPolicyNotes, &out.ConsentRequirementNotes,
		&out.DataSource, &out.VerificationDate, &out.VerificationStatus, &out.Status,
		&out.Version, &out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCountryNotFound
		}
		return nil, fmt.Errorf("update country: %w", err)
	}

	// Insert snapshot to countries_history
	snapshotJSON, _ := json.Marshal(out)
	_, err = tx.Exec(ctx, `
		INSERT INTO countries_history (id, country_id, version, snapshot, actor_user_id, changed_at, reason)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
	`, out.ID, out.Version, snapshotJSON, actorID, now, reason)
	if err != nil {
		return nil, fmt.Errorf("insert country history: %w", err)
	}

	return &out, nil
}

func (r *pgRepository) SetStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string, actorID *uuid.UUID, reason string) (*Country, error) {
	now := time.Now()
	query := `
		UPDATE countries
		SET status = $1, version = version + 1, updated_at = $2, updated_by = $3
		WHERE id = $4
		RETURNING id, name, iso2, iso3, numeric_code, calling_code,
		          continent, region, currency_code, timezones,
		          numbering_plan_notes, min_national_length, max_national_length,
		          sender_id_policy_notes, dnd_policy_notes, consent_requirement_notes,
		          data_source, verification_date, verification_status, status,
		          version, created_at, updated_at, created_by, updated_by
	`
	var out Country
	err := tx.QueryRow(ctx, query, status, now, actorID, id).Scan(
		&out.ID, &out.Name, &out.ISO2, &out.ISO3, &out.NumericCode, &out.CallingCode,
		&out.Continent, &out.Region, &out.CurrencyCode, &out.Timezones,
		&out.NumberingPlanNotes, &out.MinNationalLength, &out.MaxNationalLength,
		&out.SenderIDPolicyNotes, &out.DNDPolicyNotes, &out.ConsentRequirementNotes,
		&out.DataSource, &out.VerificationDate, &out.VerificationStatus, &out.Status,
		&out.Version, &out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCountryNotFound
		}
		return nil, fmt.Errorf("set country status: %w", err)
	}

	snapshotJSON, _ := json.Marshal(out)
	_, err = tx.Exec(ctx, `
		INSERT INTO countries_history (id, country_id, version, snapshot, actor_user_id, changed_at, reason)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
	`, out.ID, out.Version, snapshotJSON, actorID, now, reason)
	if err != nil {
		return nil, fmt.Errorf("insert country history: %w", err)
	}

	return &out, nil
}

func (r *pgRepository) GetHistory(ctx context.Context, countryID uuid.UUID) ([]HistoryEntry, error) {
	query := `
		SELECT id, country_id, version, snapshot, actor_user_id, changed_at, reason
		FROM countries_history
		WHERE country_id = $1
		ORDER BY version DESC
	`
	rows, err := r.pool.Query(ctx, query, countryID)
	if err != nil {
		return nil, fmt.Errorf("query country history: %w", err)
	}
	defer rows.Close()

	var history []HistoryEntry
	for rows.Next() {
		var h HistoryEntry
		var snapshotBytes []byte
		if err := rows.Scan(
			&h.ID, &h.CountryID, &h.Version, &snapshotBytes, &h.ActorUserID, &h.ChangedAt, &h.Reason,
		); err != nil {
			return nil, fmt.Errorf("scan history entry: %w", err)
		}
		_ = json.Unmarshal(snapshotBytes, &h.Snapshot)
		history = append(history, h)
	}
	return history, nil
}

func (r *pgRepository) Rollback(ctx context.Context, tx pgx.Tx, id uuid.UUID, targetVersion int, actorID *uuid.UUID, reason string) (*Country, error) {
	// 1. Fetch snapshot from countries_history for targetVersion
	var snapshotBytes []byte
	err := tx.QueryRow(ctx, `
		SELECT snapshot FROM countries_history WHERE country_id = $1 AND version = $2
	`, id, targetVersion).Scan(&snapshotBytes)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrVersionNotFound
		}
		return nil, fmt.Errorf("find rollback version snapshot: %w", err)
	}

	var target Country
	if err := json.Unmarshal(snapshotBytes, &target); err != nil {
		return nil, fmt.Errorf("unmarshal target version snapshot: %w", err)
	}

	// 2. Apply snapshot data to countries table, incrementing version (creates NEW version, preserves history)
	now := time.Now()
	query := `
		UPDATE countries
		SET name = $1, calling_code = $2, continent = $3, region = $4,
		    currency_code = $5, timezones = $6, numbering_plan_notes = $7,
		    min_national_length = $8, max_national_length = $9,
		    sender_id_policy_notes = $10, dnd_policy_notes = $11,
		    consent_requirement_notes = $12, data_source = $13,
		    verification_status = $14, status = $15,
		    version = version + 1, updated_at = $16, updated_by = $17
		WHERE id = $18
		RETURNING id, name, iso2, iso3, numeric_code, calling_code,
		          continent, region, currency_code, timezones,
		          numbering_plan_notes, min_national_length, max_national_length,
		          sender_id_policy_notes, dnd_policy_notes, consent_requirement_notes,
		          data_source, verification_date, verification_status, status,
		          version, created_at, updated_at, created_by, updated_by
	`
	var out Country
	err = tx.QueryRow(ctx, query,
		target.Name, target.CallingCode, target.Continent, target.Region,
		target.CurrencyCode, target.Timezones, target.NumberingPlanNotes,
		target.MinNationalLength, target.MaxNationalLength,
		target.SenderIDPolicyNotes, target.DNDPolicyNotes,
		target.ConsentRequirementNotes, target.DataSource,
		target.VerificationStatus, target.Status,
		now, actorID, id,
	).Scan(
		&out.ID, &out.Name, &out.ISO2, &out.ISO3, &out.NumericCode, &out.CallingCode,
		&out.Continent, &out.Region, &out.CurrencyCode, &out.Timezones,
		&out.NumberingPlanNotes, &out.MinNationalLength, &out.MaxNationalLength,
		&out.SenderIDPolicyNotes, &out.DNDPolicyNotes, &out.ConsentRequirementNotes,
		&out.DataSource, &out.VerificationDate, &out.VerificationStatus, &out.Status,
		&out.Version, &out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy,
	)
	if err != nil {
		return nil, fmt.Errorf("rollback country record: %w", err)
	}

	// 3. Record new snapshot row in countries_history
	newSnapshotJSON, _ := json.Marshal(out)
	rollbackReason := reason
	if rollbackReason == "" {
		rollbackReason = fmt.Sprintf("Rolled back to snapshot from version %d", targetVersion)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO countries_history (id, country_id, version, snapshot, actor_user_id, changed_at, reason)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)
	`, out.ID, out.Version, newSnapshotJSON, actorID, now, rollbackReason)
	if err != nil {
		return nil, fmt.Errorf("record rollback history snapshot: %w", err)
	}

	return &out, nil
}
