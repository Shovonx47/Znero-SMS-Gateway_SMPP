package export

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
	"github.com/shopspring/decimal"
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
		logger: logger.With(slog.String("component", "export_repo")),
	}
}

// CreateJob persists a new export job.
func (r *Repository) CreateJob(ctx context.Context, job *ExportJob) error {
	filterBytes, err := json.Marshal(job.FilterParams)
	if err != nil {
		filterBytes = []byte("{}")
	}

	query := `
		INSERT INTO export_jobs (
			id, organization_id, user_id, export_type, format,
			status, filter_params, row_count, file_path, error_message,
			started_at, completed_at, expires_at, created_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12, $13, $14
		)
	`
	_, err = r.pool.Exec(ctx, query,
		job.ID, job.OrganizationID, job.UserID, job.ExportType, job.Format,
		job.Status, filterBytes, job.RowCount, job.FilePath, job.ErrorMessage,
		job.StartedAt, job.CompletedAt, job.ExpiresAt, job.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to insert export job: %w", err)
	}
	return nil
}

// GetJob retrieves an export job by ID.
func (r *Repository) GetJob(ctx context.Context, id uuid.UUID) (*ExportJob, error) {
	query := `
		SELECT
			id, organization_id, user_id, export_type, format,
			status, filter_params, row_count, file_path, error_message,
			started_at, completed_at, expires_at, created_at
		FROM export_jobs
		WHERE id = $1
	`
	var job ExportJob
	var filterBytes []byte

	err := r.pool.QueryRow(ctx, query, id).Scan(
		&job.ID, &job.OrganizationID, &job.UserID, &job.ExportType, &job.Format,
		&job.Status, &filterBytes, &job.RowCount, &job.FilePath, &job.ErrorMessage,
		&job.StartedAt, &job.CompletedAt, &job.ExpiresAt, &job.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrJobNotFound
		}
		return nil, fmt.Errorf("failed to query export job: %w", err)
	}

	if len(filterBytes) > 0 {
		_ = json.Unmarshal(filterBytes, &job.FilterParams)
	}
	return &job, nil
}

// ListJobs retrieves export jobs scoped to the caller's organization.
func (r *Repository) ListJobs(ctx context.Context, tc *tenant.Context, limit, offset int) ([]*ExportJob, int64, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	where := ""
	var args []interface{}
	if !tc.IsPlatformAdmin {
		where = "WHERE organization_id = $1"
		args = append(args, tc.ActorOrgID)
	}

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM export_jobs %s", where)
	var total int64
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count export jobs: %w", err)
	}

	queryArgs := append(args, limit, offset)
	limitOffsetIdx := len(args) + 1
	dataQuery := fmt.Sprintf(`
		SELECT
			id, organization_id, user_id, export_type, format,
			status, filter_params, row_count, file_path, error_message,
			started_at, completed_at, expires_at, created_at
		FROM export_jobs
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d
	`, where, limitOffsetIdx, limitOffsetIdx+1)

	rows, err := r.pool.Query(ctx, dataQuery, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query export jobs: %w", err)
	}
	defer rows.Close()

	var jobs []*ExportJob
	for rows.Next() {
		var job ExportJob
		var filterBytes []byte
		if err := rows.Scan(
			&job.ID, &job.OrganizationID, &job.UserID, &job.ExportType, &job.Format,
			&job.Status, &filterBytes, &job.RowCount, &job.FilePath, &job.ErrorMessage,
			&job.StartedAt, &job.CompletedAt, &job.ExpiresAt, &job.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("failed to scan export job: %w", err)
		}
		if len(filterBytes) > 0 {
			_ = json.Unmarshal(filterBytes, &job.FilterParams)
		}
		jobs = append(jobs, &job)
	}
	return jobs, total, nil
}

// UpdateJobProgress updates the status, row count, file path, and timestamps of a job.
func (r *Repository) UpdateJobProgress(ctx context.Context, id uuid.UUID, status string, rowCount int64, filePath *string, errMsg *string, startedAt *time.Time, completedAt *time.Time) error {
	query := `
		UPDATE export_jobs
		SET status = $2,
		    row_count = $3,
		    file_path = COALESCE($4, file_path),
		    error_message = $5,
		    started_at = COALESCE($6, started_at),
		    completed_at = COALESCE($7, completed_at)
		WHERE id = $1
	`
	_, err := r.pool.Exec(ctx, query, id, status, rowCount, filePath, errMsg, startedAt, completedAt)
	return err
}

// QueryMessagesExport exports message records strictly scoped to tenant.
func (r *Repository) QueryMessagesExport(ctx context.Context, tc *tenant.Context, filter map[string]interface{}) ([]string, [][]string, []map[string]interface{}, error) {
	where := "WHERE 1=1"
	var args []interface{}
	argIdx := 1

	if !tc.IsPlatformAdmin {
		where += fmt.Sprintf(" AND m.customer_org_id = $%d", argIdx)
		args = append(args, tc.ActorOrgID)
		argIdx++
	}

	if status, ok := filter["status"].(string); ok && status != "" {
		where += fmt.Sprintf(" AND m.status = $%d", argIdx)
		args = append(args, status)
		argIdx++
	}

	maxRows := 10000
	if limitVal, ok := filter["limit"].(float64); ok && limitVal > 0 {
		maxRows = int(limitVal)
		if maxRows > 50000 {
			maxRows = 50000
		}
	}

	var headers []string
	var selectCols string
	var fromClause string

	if tc.IsPlatformAdmin {
		headers = []string{
			"Message ID", "Created At", "Customer Org ID", "Destination", "Sender ID",
			"Status", "Sell Price", "Sell Currency", "Provider ID", "Provider Conn ID",
			"Actual Buy Cost", "Buy Currency",
		}
		selectCols = `
			m.id, m.created_at, m.customer_org_id, m.destination, m.sender_id,
			m.status, COALESCE(m.sell_price, 0), COALESCE(m.sell_currency, ''),
			COALESCE(pc.provider_id::text, ''), COALESCE(m.provider_connection_id::text, ''),
			COALESCE(m.actual_buy_cost, 0), COALESCE(m.buy_currency, '')
		`
		fromClause = "FROM messages m LEFT JOIN provider_connections pc ON pc.id = m.provider_connection_id"
	} else {
		// Strictly non-platform: NEVER reveal provider, buy cost, or margin
		headers = []string{
			"Message ID", "Created At", "Destination", "Sender ID",
			"Status", "Sell Price", "Sell Currency",
		}
		selectCols = `
			m.id, m.created_at, m.destination, m.sender_id,
			m.status, COALESCE(m.sell_price, 0), COALESCE(m.sell_currency, '')
		`
		fromClause = "FROM messages m"
	}

	query := fmt.Sprintf(`
		SELECT %s
		%s
		%s
		ORDER BY m.created_at DESC
		LIMIT $%d
	`, selectCols, fromClause, where, argIdx)
	args = append(args, maxRows)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to query messages for export: %w", err)
	}
	defer rows.Close()

	var matrix [][]string
	var records []map[string]interface{}

	for rows.Next() {
		if tc.IsPlatformAdmin {
			var id uuid.UUID
			var createdAt time.Time
			var customerOrgID uuid.UUID
			var destination, senderID, status string
			var sellPrice decimal.Decimal
			var sellCurrency, providerID, connID string
			var buyCost decimal.Decimal
			var buyCurrency string

			if err := rows.Scan(
				&id, &createdAt, &customerOrgID, &destination, &senderID,
				&status, &sellPrice, &sellCurrency,
				&providerID, &connID,
				&buyCost, &buyCurrency,
			); err != nil {
				return nil, nil, nil, err
			}

			row := []string{
				id.String(),
				createdAt.Format(time.RFC3339),
				customerOrgID.String(),
				destination,
				senderID,
				status,
				sellPrice.String(),
				sellCurrency,
				providerID,
				connID,
				buyCost.String(),
				buyCurrency,
			}
			matrix = append(matrix, row)

			rec := map[string]interface{}{
				"message_id":             id.String(),
				"created_at":             createdAt.Format(time.RFC3339),
				"customer_org_id":        customerOrgID.String(),
				"destination":            destination,
				"sender_id":              senderID,
				"status":                 status,
				"sell_price":             sellPrice.String(),
				"sell_currency":          sellCurrency,
				"provider_id":            providerID,
				"provider_connection_id": connID,
				"actual_buy_cost":        buyCost.String(),
				"buy_currency":           buyCurrency,
			}
			records = append(records, rec)
		} else {
			var id uuid.UUID
			var createdAt time.Time
			var destination, senderID, status string
			var sellPrice decimal.Decimal
			var sellCurrency string

			if err := rows.Scan(
				&id, &createdAt, &destination, &senderID,
				&status, &sellPrice, &sellCurrency,
			); err != nil {
				return nil, nil, nil, err
			}

			row := []string{
				id.String(),
				createdAt.Format(time.RFC3339),
				destination,
				senderID,
				status,
				sellPrice.String(),
				sellCurrency,
			}
			matrix = append(matrix, row)

			rec := map[string]interface{}{
				"message_id":    id.String(),
				"created_at":    createdAt.Format(time.RFC3339),
				"destination":   destination,
				"sender_id":     senderID,
				"status":        status,
				"sell_price":    sellPrice.String(),
				"sell_currency": sellCurrency,
			}
			records = append(records, rec)
		}
	}

	return headers, matrix, records, nil
}

// QueryDeliveryReportExport extracts delivery metrics by status and period.
func (r *Repository) QueryDeliveryReportExport(ctx context.Context, tc *tenant.Context, filter map[string]interface{}) ([]string, [][]string, []map[string]interface{}, error) {
	where := "WHERE 1=1"
	var args []interface{}
	argIdx := 1

	if !tc.IsPlatformAdmin {
		where += fmt.Sprintf(" AND customer_org_id = $%d", argIdx)
		args = append(args, tc.ActorOrgID)
	}

	query := fmt.Sprintf(`
		SELECT
			status,
			COUNT(*) AS total_count,
			COALESCE(SUM(sell_price), 0) AS total_revenue,
			sell_currency
		FROM messages
		%s
		GROUP BY status, sell_currency
		ORDER BY total_count DESC
	`, where)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to query delivery report: %w", err)
	}
	defer rows.Close()

	headers := []string{"Status", "Total Messages", "Total Value", "Currency"}
	var matrix [][]string
	var records []map[string]interface{}

	for rows.Next() {
		var status, currency string
		var totalCount int64
		var totalRev decimal.Decimal
		if err := rows.Scan(&status, &totalCount, &totalRev, &currency); err != nil {
			return nil, nil, nil, err
		}

		matrix = append(matrix, []string{
			status,
			fmt.Sprintf("%d", totalCount),
			totalRev.String(),
			currency,
		})
		records = append(records, map[string]interface{}{
			"status":         status,
			"total_messages": totalCount,
			"total_value":    totalRev.String(),
			"currency":       currency,
		})
	}

	return headers, matrix, records, nil
}

// QueryFinancialReportExport aggregates financials per currency.
func (r *Repository) QueryFinancialReportExport(ctx context.Context, tc *tenant.Context, filter map[string]interface{}) ([]string, [][]string, []map[string]interface{}, error) {
	where := "WHERE 1=1"
	var args []interface{}
	argIdx := 1

	if !tc.IsPlatformAdmin {
		where += fmt.Sprintf(" AND customer_org_id = $%d", argIdx)
		args = append(args, tc.ActorOrgID)
	}

	var headers []string
	var matrix [][]string
	var records []map[string]interface{}

	if tc.IsPlatformAdmin {
		headers = []string{"Currency", "Message Count", "Total Revenue", "Total Cost", "Gross Margin"}
		query := fmt.Sprintf(`
			SELECT
				sell_currency,
				COUNT(*) as msg_count,
				COALESCE(SUM(sell_price), 0) as revenue,
				COALESCE(SUM(actual_buy_cost), 0) as buy_cost
			FROM messages
			%s
			GROUP BY sell_currency
			ORDER BY msg_count DESC
		`, where)

		rows, err := r.pool.Query(ctx, query, args...)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to query financial report: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var currency string
			var msgCount int64
			var revenue, buyCost decimal.Decimal
			if err := rows.Scan(&currency, &msgCount, &revenue, &buyCost); err != nil {
				return nil, nil, nil, err
			}
			margin := revenue.Sub(buyCost)

			matrix = append(matrix, []string{
				currency,
				fmt.Sprintf("%d", msgCount),
				revenue.String(),
				buyCost.String(),
				margin.String(),
			})
			records = append(records, map[string]interface{}{
				"currency":      currency,
				"message_count": msgCount,
				"total_revenue": revenue.String(),
				"total_cost":    buyCost.String(),
				"gross_margin":  margin.String(),
			})
		}
	} else {
		// Non-platform customers only see their total spend, NEVER buy cost or margin
		headers = []string{"Currency", "Message Count", "Total Spend"}
		query := fmt.Sprintf(`
			SELECT
				sell_currency,
				COUNT(*) as msg_count,
				COALESCE(SUM(sell_price), 0) as spend
			FROM messages
			%s
			GROUP BY sell_currency
			ORDER BY msg_count DESC
		`, where)

		rows, err := r.pool.Query(ctx, query, args...)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to query financial report: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var currency string
			var msgCount int64
			var spend decimal.Decimal
			if err := rows.Scan(&currency, &msgCount, &spend); err != nil {
				return nil, nil, nil, err
			}

			matrix = append(matrix, []string{
				currency,
				fmt.Sprintf("%d", msgCount),
				spend.String(),
			})
			records = append(records, map[string]interface{}{
				"currency":      currency,
				"message_count": msgCount,
				"total_spend":   spend.String(),
			})
		}
	}

	return headers, matrix, records, nil
}
