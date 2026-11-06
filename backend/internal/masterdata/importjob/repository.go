package importjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrJobNotFound = errors.New("import job not found")
)

type Repository interface {
	CreateJob(ctx context.Context, job ImportJob) (*ImportJob, error)
	GetJobByID(ctx context.Context, id uuid.UUID) (*ImportJob, error)
	ListJobs(ctx context.Context, f Filter) ([]ImportJob, int, error)
	UpdateJobStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string, valid, invalid, imported, skipped int, errSummary json.RawMessage) error
	CreateJobRows(ctx context.Context, rows []ImportJobRow) error
	ListJobRows(ctx context.Context, jobID uuid.UUID, statusFilter string, page, pageSize int) ([]ImportJobRow, int, error)
	GetJobRowsForImport(ctx context.Context, jobID uuid.UUID, onlyValid bool) ([]ImportJobRow, error)
	UpdateRowImported(ctx context.Context, tx pgx.Tx, rowID uuid.UUID, entityID uuid.UUID) error
	UpdateRowsImportedBatch(ctx context.Context, tx pgx.Tx, rowIDs []uuid.UUID, entityIDs []uuid.UUID) error
	GetImportedEntityIDs(ctx context.Context, jobID uuid.UUID) ([]uuid.UUID, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

func (r *pgRepository) CreateJob(ctx context.Context, job ImportJob) (*ImportJob, error) {
	id := job.ID
	if id == uuid.Nil {
		id = uuid.New()
	}
	now := time.Now()
	errSummary := job.ErrorSummary
	if len(errSummary) == 0 {
		errSummary = json.RawMessage("{}")
	}

	query := `
		INSERT INTO import_jobs (
			id, entity_type, file_name, file_size, content_type, stored_path,
			status, total_rows, valid_rows, invalid_rows, imported_rows, skipped_rows,
			allow_partial, error_summary, actor_user_id, created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11, $12,
			$13, $14, $15, $16
		) RETURNING id, entity_type, file_name, file_size, content_type, stored_path,
		            status, total_rows, valid_rows, invalid_rows, imported_rows, skipped_rows,
		            allow_partial, error_summary, actor_user_id, created_at, completed_at
	`
	var out ImportJob
	err := r.pool.QueryRow(ctx, query,
		id, job.EntityType, job.FileName, job.FileSize, job.ContentType, job.StoredPath,
		job.Status, job.TotalRows, job.ValidRows, job.InvalidRows, job.ImportedRows, job.SkippedRows,
		job.AllowPartial, errSummary, job.ActorUserID, now,
	).Scan(
		&out.ID, &out.EntityType, &out.FileName, &out.FileSize, &out.ContentType, &out.StoredPath,
		&out.Status, &out.TotalRows, &out.ValidRows, &out.InvalidRows, &out.ImportedRows, &out.SkippedRows,
		&out.AllowPartial, &out.ErrorSummary, &out.ActorUserID, &out.CreatedAt, &out.CompletedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create import job: %w", err)
	}
	return &out, nil
}

func (r *pgRepository) GetJobByID(ctx context.Context, id uuid.UUID) (*ImportJob, error) {
	query := `
		SELECT j.id, j.entity_type, j.file_name, j.file_size, j.content_type, j.stored_path,
		       j.status, j.total_rows, j.valid_rows, j.invalid_rows, j.imported_rows, j.skipped_rows,
		       j.allow_partial, j.error_summary, j.actor_user_id, COALESCE(u.email, '') AS actor_email,
		       j.created_at, j.completed_at
		FROM import_jobs j
		LEFT JOIN users u ON j.actor_user_id = u.id
		WHERE j.id = $1
	`
	var out ImportJob
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&out.ID, &out.EntityType, &out.FileName, &out.FileSize, &out.ContentType, &out.StoredPath,
		&out.Status, &out.TotalRows, &out.ValidRows, &out.InvalidRows, &out.ImportedRows, &out.SkippedRows,
		&out.AllowPartial, &out.ErrorSummary, &out.ActorUserID, &out.ActorEmail,
		&out.CreatedAt, &out.CompletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrJobNotFound
		}
		return nil, fmt.Errorf("get import job by id: %w", err)
	}
	return &out, nil
}

func (r *pgRepository) ListJobs(ctx context.Context, f Filter) ([]ImportJob, int, error) {
	var where []string
	var args []any
	argIdx := 1

	if f.EntityType != "" {
		where = append(where, fmt.Sprintf("j.entity_type = $%d", argIdx))
		args = append(args, f.EntityType)
		argIdx++
	}
	if f.Status != "" {
		where = append(where, fmt.Sprintf("j.status = $%d", argIdx))
		args = append(args, f.Status)
		argIdx++
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + where[0]
		for i := 1; i < len(where); i++ {
			whereClause += " AND " + where[i]
		}
	}

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM import_jobs j %s", whereClause)
	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count import jobs: %w", err)
	}

	page := f.Page
	if page < 1 {
		page = 1
	}
	pageSize := f.PageSize
	if pageSize < 1 || pageSize > 100 {
		pageSize = 25
	}
	offset := (page - 1) * pageSize

	query := fmt.Sprintf(`
		SELECT j.id, j.entity_type, j.file_name, j.file_size, j.content_type, j.stored_path,
		       j.status, j.total_rows, j.valid_rows, j.invalid_rows, j.imported_rows, j.skipped_rows,
		       j.allow_partial, j.error_summary, j.actor_user_id, COALESCE(u.email, '') AS actor_email,
		       j.created_at, j.completed_at
		FROM import_jobs j
		LEFT JOIN users u ON j.actor_user_id = u.id
		%s
		ORDER BY j.created_at DESC
		LIMIT $%d OFFSET $%d
	`, whereClause, argIdx, argIdx+1)

	args = append(args, pageSize, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list import jobs: %w", err)
	}
	defer rows.Close()

	var items []ImportJob
	for rows.Next() {
		var out ImportJob
		if err := rows.Scan(
			&out.ID, &out.EntityType, &out.FileName, &out.FileSize, &out.ContentType, &out.StoredPath,
			&out.Status, &out.TotalRows, &out.ValidRows, &out.InvalidRows, &out.ImportedRows, &out.SkippedRows,
			&out.AllowPartial, &out.ErrorSummary, &out.ActorUserID, &out.ActorEmail,
			&out.CreatedAt, &out.CompletedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan import job: %w", err)
		}
		items = append(items, out)
	}

	return items, total, nil
}

func (r *pgRepository) UpdateJobStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string, valid, invalid, imported, skipped int, errSummary json.RawMessage) error {
	query := `
		UPDATE import_jobs
		SET status = $1::varchar, valid_rows = $2, invalid_rows = $3, imported_rows = $4, skipped_rows = $5,
		    error_summary = $6, completed_at = CASE WHEN $1::text IN ('completed', 'failed', 'rolled_back') THEN now() ELSE completed_at END
		WHERE id = $7
	`
	if tx != nil {
		_, err := tx.Exec(ctx, query, status, valid, invalid, imported, skipped, errSummary, id)
		return err
	}
	_, err := r.pool.Exec(ctx, query, status, valid, invalid, imported, skipped, errSummary, id)
	return err
}

func (r *pgRepository) CreateJobRows(ctx context.Context, rows []ImportJobRow) error {
	if len(rows) == 0 {
		return nil
	}

	const chunkSize = 5000
	for start := 0; start < len(rows); start += chunkSize {
		end := start + chunkSize
		if end > len(rows) {
			end = len(rows)
		}
		chunk := rows[start:end]
		n := len(chunk)

		ids := make([]uuid.UUID, n)
		jobIDs := make([]uuid.UUID, n)
		rowNums := make([]int, n)
		rawDatas := make([]string, n)
		statuses := make([]string, n)
		errJSONs := make([]string, n)

		for i, row := range chunk {
			rowID := row.ID
			if rowID == uuid.Nil {
				rowID = uuid.New()
			}
			ids[i] = rowID
			jobIDs[i] = row.JobID
			rowNums[i] = row.RowNumber
			if len(row.RawData) == 0 {
				rawDatas[i] = "{}"
			} else {
				rawDatas[i] = string(row.RawData)
			}
			statuses[i] = row.Status
			if len(row.Errors) == 0 {
				errJSONs[i] = "[]"
			} else {
				errsBytes, _ := json.Marshal(row.Errors)
				errJSONs[i] = string(errsBytes)
			}
		}

		_, err := r.pool.Exec(ctx, `
			INSERT INTO import_job_rows (id, job_id, row_number, raw_data, status, errors)
			SELECT u.id, u.job_id, u.row_number, u.raw_data::jsonb, u.status, u.errors::jsonb
			FROM UNNEST($1::uuid[], $2::uuid[], $3::int[], $4::text[], $5::text[], $6::text[])
			     AS u(id, job_id, row_number, raw_data, status, errors)
		`, ids, jobIDs, rowNums, rawDatas, statuses, errJSONs)
		if err != nil {
			return fmt.Errorf("batch insert job rows (%d..%d): %w", start, end, err)
		}
	}
	return nil
}

func (r *pgRepository) ListJobRows(ctx context.Context, jobID uuid.UUID, statusFilter string, page, pageSize int) ([]ImportJobRow, int, error) {
	where := "WHERE job_id = $1"
	args := []any{jobID}
	argIdx := 2

	if statusFilter != "" {
		where += fmt.Sprintf(" AND status = $%d", argIdx)
		args = append(args, statusFilter)
		argIdx++
	}

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM import_job_rows %s", where)
	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count job rows: %w", err)
	}

	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 500 {
		pageSize = 50
	}
	offset := (page - 1) * pageSize

	query := fmt.Sprintf(`
		SELECT id, job_id, row_number, raw_data, status, errors, created_entity_id
		FROM import_job_rows
		%s
		ORDER BY row_number ASC
		LIMIT $%d OFFSET $%d
	`, where, argIdx, argIdx+1)

	args = append(args, pageSize, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list job rows: %w", err)
	}
	defer rows.Close()

	var items []ImportJobRow
	for rows.Next() {
		var out ImportJobRow
		var errsBytes []byte
		if err := rows.Scan(
			&out.ID, &out.JobID, &out.RowNumber, &out.RawData, &out.Status, &errsBytes, &out.CreatedEntityID,
		); err != nil {
			return nil, 0, fmt.Errorf("scan job row: %w", err)
		}
		_ = json.Unmarshal(errsBytes, &out.Errors)
		items = append(items, out)
	}

	return items, total, nil
}

func (r *pgRepository) GetJobRowsForImport(ctx context.Context, jobID uuid.UUID, onlyValid bool) ([]ImportJobRow, error) {
	where := "WHERE job_id = $1"
	if onlyValid {
		where += " AND status = 'valid'"
	} else {
		where += " AND status IN ('valid', 'duplicate', 'conflict')"
	}

	query := fmt.Sprintf(`
		SELECT id, job_id, row_number, raw_data, status, errors, created_entity_id
		FROM import_job_rows
		%s
		ORDER BY row_number ASC
	`, where)

	rows, err := r.pool.Query(ctx, query, jobID)
	if err != nil {
		return nil, fmt.Errorf("query rows for import: %w", err)
	}
	defer rows.Close()

	var items []ImportJobRow
	for rows.Next() {
		var out ImportJobRow
		var errsBytes []byte
		if err := rows.Scan(
			&out.ID, &out.JobID, &out.RowNumber, &out.RawData, &out.Status, &errsBytes, &out.CreatedEntityID,
		); err != nil {
			return nil, fmt.Errorf("scan job row: %w", err)
		}
		_ = json.Unmarshal(errsBytes, &out.Errors)
		items = append(items, out)
	}

	return items, nil
}

func (r *pgRepository) UpdateRowImported(ctx context.Context, tx pgx.Tx, rowID uuid.UUID, entityID uuid.UUID) error {
	query := `
		UPDATE import_job_rows
		SET status = 'imported', created_entity_id = $1
		WHERE id = $2
	`
	_, err := tx.Exec(ctx, query, entityID, rowID)
	return err
}

func (r *pgRepository) UpdateRowsImportedBatch(ctx context.Context, tx pgx.Tx, rowIDs []uuid.UUID, entityIDs []uuid.UUID) error {
	if len(rowIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
		UPDATE import_job_rows r
		SET status = 'imported', created_entity_id = u.entity_id
		FROM UNNEST($1::uuid[], $2::uuid[]) AS u(row_id, entity_id)
		WHERE r.id = u.row_id
	`, rowIDs, entityIDs)
	return err
}

func (r *pgRepository) GetImportedEntityIDs(ctx context.Context, jobID uuid.UUID) ([]uuid.UUID, error) {
	query := `
		SELECT created_entity_id FROM import_job_rows
		WHERE job_id = $1 AND status = 'imported' AND created_entity_id IS NOT NULL
	`
	rows, err := r.pool.Query(ctx, query, jobID)
	if err != nil {
		return nil, fmt.Errorf("query imported entity ids: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
