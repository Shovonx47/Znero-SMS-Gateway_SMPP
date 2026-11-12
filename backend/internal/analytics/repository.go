package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nyaruka/phonenumbers"
	"github.com/shopspring/decimal"
)

// Repository defines database operations for the analytics engine.
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
		logger: logger,
	}
}

// ResolveCountryISO resolves a phone number's country ISO2 code using libphonenumber.
func ResolveCountryISO(destination string) string {
	cleaned := strings.TrimSpace(destination)
	if cleaned == "" {
		return "XX"
	}
	if !strings.HasPrefix(cleaned, "+") {
		cleaned = "+" + cleaned
	}
	num, err := phonenumbers.Parse(cleaned, "")
	if err == nil {
		region := phonenumbers.GetRegionCodeForNumber(num)
		if region != "" && region != "ZZ" {
			return region
		}
	}
	return "XX"
}

// GetCursor returns the stored cursor or nil if not found.
func (r *Repository) GetCursor(ctx context.Context, name string) (*Cursor, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT cursor_name, last_event_id, last_event_time, updated_at
		FROM analytics_cursors
		WHERE cursor_name = $1
	`, name)

	var c Cursor
	err := row.Scan(&c.Name, &c.LastEventID, &c.LastEventTime, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get cursor %s: %w", name, err)
	}
	return &c, nil
}

// rawEventRow represents a joined message_event and message row for aggregation.
type rawEventRow struct {
	EventID              uuid.UUID
	MessageID            uuid.UUID
	MessageCreatedAt     time.Time
	EventType            string
	FromStatus           *string
	ToStatus             string
	EventCreatedAt       time.Time
	CustomerOrgID        uuid.UUID
	SellPrice            decimal.Decimal
	SellCurrency         string
	TotalParts           int
	ProviderConnectionID *uuid.UUID
	EstimatedBuyCost     *decimal.Decimal
	ActualBuyCost        *decimal.Decimal
	BuyCurrency          *string
	Destination          string
	SubmittedAt          *time.Time
	DeliveredAt          *time.Time
	ProviderID           uuid.UUID
}

type customerAggKey struct {
	BucketStart time.Time
	CustomerID  uuid.UUID
	Currency    string
}

type providerAggKey struct {
	BucketStart  time.Time
	ConnectionID uuid.UUID
	Currency     string
}

type destinationAggKey struct {
	BucketStart time.Time
	CountryISO  string
	OperatorID  uuid.UUID
	Currency    string
}

// ProcessMessageEventsBatch reads the next unconsumed batch of events and updates
// rollups and cursors atomically within a single transaction.
func (r *Repository) ProcessMessageEventsBatch(ctx context.Context, batchSize int) (int, error) {
	if batchSize <= 0 {
		batchSize = 1000
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin batch tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the cursor row (or create if not present)
	var lastEventID *uuid.UUID
	var lastEventTime *time.Time
	err = tx.QueryRow(ctx, `
		SELECT last_event_id, last_event_time
		FROM analytics_cursors
		WHERE cursor_name = 'message_events'
		FOR UPDATE
	`).Scan(&lastEventID, &lastEventTime)

	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `
			INSERT INTO analytics_cursors (cursor_name, last_event_id, last_event_time, updated_at)
			VALUES ('message_events', NULL, NULL, now())
			ON CONFLICT (cursor_name) DO NOTHING
		`)
		if err != nil {
			return 0, fmt.Errorf("insert initial cursor: %w", err)
		}
	} else if err != nil {
		return 0, fmt.Errorf("lock cursor: %w", err)
	}

	// Fetch next batch of events
	query := `
		SELECT
			me.id, me.message_id, me.message_created_at, me.event_type, me.from_status, me.to_status, me.created_at,
			m.customer_org_id, m.sell_price, m.sell_currency, m.total_parts,
			m.provider_connection_id, m.estimated_buy_cost, m.actual_buy_cost, m.buy_currency,
			m.destination, m.submitted_at, m.delivered_at,
			COALESCE(pc.provider_id, '00000000-0000-0000-0000-000000000000'::uuid) as provider_id
		FROM message_events me
		JOIN messages m ON m.id = me.message_id AND m.created_at = me.message_created_at
		LEFT JOIN provider_connections pc ON pc.id = m.provider_connection_id
		WHERE ($1::timestamptz IS NULL OR me.created_at > $1 OR (me.created_at = $1 AND me.id > $2))
		ORDER BY me.created_at ASC, me.id ASC
		LIMIT $3
	`

	rows, err := tx.Query(ctx, query, lastEventTime, lastEventID, batchSize)
	if err != nil {
		return 0, fmt.Errorf("fetch event batch: %w", err)
	}
	defer rows.Close()

	var events []rawEventRow
	for rows.Next() {
		var e rawEventRow
		err := rows.Scan(
			&e.EventID, &e.MessageID, &e.MessageCreatedAt, &e.EventType, &e.FromStatus, &e.ToStatus, &e.EventCreatedAt,
			&e.CustomerOrgID, &e.SellPrice, &e.SellCurrency, &e.TotalParts,
			&e.ProviderConnectionID, &e.EstimatedBuyCost, &e.ActualBuyCost, &e.BuyCurrency,
			&e.Destination, &e.SubmittedAt, &e.DeliveredAt,
			&e.ProviderID,
		)
		if err != nil {
			return 0, fmt.Errorf("scan event row: %w", err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate event rows: %w", err)
	}

	if len(events) == 0 {
		return 0, tx.Commit(ctx)
	}

	// In-memory aggregates for the batch
	custAggs := make(map[customerAggKey]*CustomerRollup)
	provAggs := make(map[providerAggKey]*ProviderRollup)
	destAggs := make(map[destinationAggKey]*DestinationRollup)

	var maxEventID uuid.UUID
	var maxEventTime time.Time

	for _, e := range events {
		maxEventID = e.EventID
		maxEventTime = e.EventCreatedAt

		bucketStart := e.EventCreatedAt.UTC().Truncate(15 * time.Minute)

		// 1. Customer Aggregation
		cKey := customerAggKey{
			BucketStart: bucketStart,
			CustomerID:  e.CustomerOrgID,
			Currency:    e.SellCurrency,
		}
		cr, ok := custAggs[cKey]
		if !ok {
			cr = &CustomerRollup{
				BucketStart:      bucketStart,
				OrganizationID:   e.CustomerOrgID,
				CustomerID:       e.CustomerOrgID,
				Currency:         e.SellCurrency,
				LatencyHistogram: NewLatencyHistogram(),
			}
			custAggs[cKey] = cr
		}

		// 2. Provider Aggregation (if connection assigned)
		var pr *ProviderRollup
		if e.ProviderConnectionID != nil && *e.ProviderConnectionID != uuid.Nil {
			buyCur := e.SellCurrency
			if e.BuyCurrency != nil && *e.BuyCurrency != "" {
				buyCur = *e.BuyCurrency
			}
			pKey := providerAggKey{
				BucketStart:  bucketStart,
				ConnectionID: *e.ProviderConnectionID,
				Currency:     buyCur,
			}
			var pOk bool
			pr, pOk = provAggs[pKey]
			if !pOk {
				pr = &ProviderRollup{
					BucketStart:  bucketStart,
					ProviderID:   e.ProviderID,
					ConnectionID: *e.ProviderConnectionID,
					Currency:     buyCur,
				}
				provAggs[pKey] = pr
			}
		}

		// 3. Destination Aggregation
		countryISO := ResolveCountryISO(e.Destination)
		dKey := destinationAggKey{
			BucketStart: bucketStart,
			CountryISO:  countryISO,
			OperatorID:  uuid.Nil,
			Currency:    e.SellCurrency,
		}
		dr, dOk := destAggs[dKey]
		if !dOk {
			dr = &DestinationRollup{
				BucketStart: bucketStart,
				CountryISO:  countryISO,
				OperatorID:  uuid.Nil,
				Currency:    e.SellCurrency,
			}
			destAggs[dKey] = dr
		}

		// Update metrics based on target status
		switch e.ToStatus {
		case "submitted":
			cr.SubmittedCount++
			cr.PartsCount += int64(e.TotalParts)
			cr.RevenueHeld = cr.RevenueHeld.Add(e.SellPrice)
			dr.SubmittedCount++
			if pr != nil {
				pr.SubmittedCount++
				if e.EstimatedBuyCost != nil {
					pr.CostEstimated = pr.CostEstimated.Add(*e.EstimatedBuyCost)
					dr.CostEstimated = dr.CostEstimated.Add(*e.EstimatedBuyCost)
				}
			}
			dr.RevenueEstimated = dr.RevenueEstimated.Add(e.SellPrice)

		case "delivered":
			cr.DeliveredCount++
			cr.RevenueCaptured = cr.RevenueCaptured.Add(e.SellPrice)
			cr.RevenueHeld = cr.RevenueHeld.Sub(e.SellPrice)
			dr.DeliveredCount++
			if pr != nil {
				pr.DeliveredCount++
			}

			// Latency observation
			latencyMs := 0
			if e.DeliveredAt != nil && e.SubmittedAt != nil {
				latencyMs = int(e.DeliveredAt.Sub(*e.SubmittedAt).Milliseconds())
			} else {
				latencyMs = int(e.EventCreatedAt.Sub(e.MessageCreatedAt).Milliseconds())
			}
			if latencyMs < 0 {
				latencyMs = 0
			}
			cr.LatencySamplesCount++
			cr.LatencySumMs += int64(latencyMs)
			cr.LatencyHistogram.Observe(latencyMs)
			if pr != nil {
				pr.LatencySamplesCount++
				pr.LatencySumMs += int64(latencyMs)
			}

		case "failed":
			cr.FailedCount++
			cr.RevenueHeld = cr.RevenueHeld.Sub(e.SellPrice)
			dr.FailedCount++
			if pr != nil {
				pr.FailedCount++
			}

		case "undelivered":
			cr.UndeliveredCount++
			cr.RevenueHeld = cr.RevenueHeld.Sub(e.SellPrice)
			dr.UndeliveredCount++
			if pr != nil {
				pr.UndeliveredCount++
			}

		case "expired":
			cr.ExpiredCount++
			cr.RevenueHeld = cr.RevenueHeld.Sub(e.SellPrice)
			if pr != nil {
				pr.ExpiredCount++
			}

		case "unknown":
			cr.UnknownCount++
		}
	}

	// Upsert Customer Rollups
	for _, cr := range custAggs {
		histJSON, _ := json.Marshal(cr.LatencyHistogram)
		p50, p95, p99 := cr.LatencyHistogram.CalculatePercentiles()

		_, err := tx.Exec(ctx, `
			INSERT INTO analytics_rollup_customer_15m (
				bucket_start, organization_id, customer_id, currency,
				submitted_count, delivered_count, failed_count, undelivered_count, expired_count, unknown_count, parts_count,
				revenue_captured, revenue_held, revenue_refunded,
				latency_samples_count, latency_sum_ms, latency_histogram,
				latency_p50_ms, latency_p95_ms, latency_p99_ms
			) VALUES (
				$1, $2, $3, $4,
				$5, $6, $7, $8, $9, $10, $11,
				$12, $13, $14,
				$15, $16, $17,
				$18, $19, $20
			)
			ON CONFLICT (bucket_start, customer_id, currency) DO UPDATE SET
				submitted_count = analytics_rollup_customer_15m.submitted_count + EXCLUDED.submitted_count,
				delivered_count = analytics_rollup_customer_15m.delivered_count + EXCLUDED.delivered_count,
				failed_count = analytics_rollup_customer_15m.failed_count + EXCLUDED.failed_count,
				undelivered_count = analytics_rollup_customer_15m.undelivered_count + EXCLUDED.undelivered_count,
				expired_count = analytics_rollup_customer_15m.expired_count + EXCLUDED.expired_count,
				unknown_count = analytics_rollup_customer_15m.unknown_count + EXCLUDED.unknown_count,
				parts_count = analytics_rollup_customer_15m.parts_count + EXCLUDED.parts_count,
				revenue_captured = analytics_rollup_customer_15m.revenue_captured + EXCLUDED.revenue_captured,
				revenue_held = analytics_rollup_customer_15m.revenue_held + EXCLUDED.revenue_held,
				revenue_refunded = analytics_rollup_customer_15m.revenue_refunded + EXCLUDED.revenue_refunded,
				latency_samples_count = analytics_rollup_customer_15m.latency_samples_count + EXCLUDED.latency_samples_count,
				latency_sum_ms = analytics_rollup_customer_15m.latency_sum_ms + EXCLUDED.latency_sum_ms,
				latency_p50_ms = EXCLUDED.latency_p50_ms,
				latency_p95_ms = EXCLUDED.latency_p95_ms,
				latency_p99_ms = EXCLUDED.latency_p99_ms,
				updated_at = now()
		`,
			cr.BucketStart, cr.OrganizationID, cr.CustomerID, cr.Currency,
			cr.SubmittedCount, cr.DeliveredCount, cr.FailedCount, cr.UndeliveredCount, cr.ExpiredCount, cr.UnknownCount, cr.PartsCount,
			cr.RevenueCaptured, cr.RevenueHeld, cr.RevenueRefunded,
			cr.LatencySamplesCount, cr.LatencySumMs, histJSON,
			p50, p95, p99,
		)
		if err != nil {
			return 0, fmt.Errorf("upsert customer rollup: %w", err)
		}
	}

	// Upsert Provider Rollups
	for _, pr := range provAggs {
		var p50, p95 *int
		if pr.LatencySamplesCount > 0 {
			avg := int(pr.LatencySumMs / pr.LatencySamplesCount)
			p50 = &avg
			p95 = &avg
		}

		_, err := tx.Exec(ctx, `
			INSERT INTO analytics_rollup_provider_15m (
				bucket_start, provider_id, connection_id, currency,
				submitted_count, delivered_count, failed_count, undelivered_count, expired_count,
				cost_estimated, latency_samples_count, latency_sum_ms, latency_p50_ms, latency_p95_ms
			) VALUES (
				$1, $2, $3, $4,
				$5, $6, $7, $8, $9,
				$10, $11, $12, $13, $14
			)
			ON CONFLICT (bucket_start, connection_id, currency) DO UPDATE SET
				submitted_count = analytics_rollup_provider_15m.submitted_count + EXCLUDED.submitted_count,
				delivered_count = analytics_rollup_provider_15m.delivered_count + EXCLUDED.delivered_count,
				failed_count = analytics_rollup_provider_15m.failed_count + EXCLUDED.failed_count,
				undelivered_count = analytics_rollup_provider_15m.undelivered_count + EXCLUDED.undelivered_count,
				expired_count = analytics_rollup_provider_15m.expired_count + EXCLUDED.expired_count,
				cost_estimated = analytics_rollup_provider_15m.cost_estimated + EXCLUDED.cost_estimated,
				latency_samples_count = analytics_rollup_provider_15m.latency_samples_count + EXCLUDED.latency_samples_count,
				latency_sum_ms = analytics_rollup_provider_15m.latency_sum_ms + EXCLUDED.latency_sum_ms,
				latency_p50_ms = EXCLUDED.latency_p50_ms,
				latency_p95_ms = EXCLUDED.latency_p95_ms,
				updated_at = now()
		`,
			pr.BucketStart, pr.ProviderID, pr.ConnectionID, pr.Currency,
			pr.SubmittedCount, pr.DeliveredCount, pr.FailedCount, pr.UndeliveredCount, pr.ExpiredCount,
			pr.CostEstimated, pr.LatencySamplesCount, pr.LatencySumMs, p50, p95,
		)
		if err != nil {
			return 0, fmt.Errorf("upsert provider rollup: %w", err)
		}
	}

	// Upsert Destination Rollups
	for _, dr := range destAggs {
		_, err := tx.Exec(ctx, `
			INSERT INTO analytics_rollup_destination_15m (
				bucket_start, country_iso, operator_id, currency,
				submitted_count, delivered_count, failed_count, undelivered_count,
				revenue_estimated, cost_estimated
			) VALUES (
				$1, $2, $3, $4,
				$5, $6, $7, $8,
				$9, $10
			)
			ON CONFLICT (bucket_start, country_iso, operator_id, currency) DO UPDATE SET
				submitted_count = analytics_rollup_destination_15m.submitted_count + EXCLUDED.submitted_count,
				delivered_count = analytics_rollup_destination_15m.delivered_count + EXCLUDED.delivered_count,
				failed_count = analytics_rollup_destination_15m.failed_count + EXCLUDED.failed_count,
				undelivered_count = analytics_rollup_destination_15m.undelivered_count + EXCLUDED.undelivered_count,
				revenue_estimated = analytics_rollup_destination_15m.revenue_estimated + EXCLUDED.revenue_estimated,
				cost_estimated = analytics_rollup_destination_15m.cost_estimated + EXCLUDED.cost_estimated,
				updated_at = now()
		`,
			dr.BucketStart, dr.CountryISO, dr.OperatorID, dr.Currency,
			dr.SubmittedCount, dr.DeliveredCount, dr.FailedCount, dr.UndeliveredCount,
			dr.RevenueEstimated, dr.CostEstimated,
		)
		if err != nil {
			return 0, fmt.Errorf("upsert destination rollup: %w", err)
		}
	}

	// Update cursor to advance high-water mark
	_, err = tx.Exec(ctx, `
		UPDATE analytics_cursors
		SET last_event_id = $1, last_event_time = $2, updated_at = now()
		WHERE cursor_name = 'message_events'
	`, maxEventID, maxEventTime)
	if err != nil {
		return 0, fmt.Errorf("update cursor: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit batch tx: %w", err)
	}

	return len(events), nil
}

// QueryTimeSeries returns aggregated time series points realigned to the requested timezone and granularity.
func (r *Repository) QueryTimeSeries(ctx context.Context, filter DashboardFilter) ([]TimeSeriesPoint, error) {
	tz := filter.Timezone
	if tz == "" {
		tz = "UTC"
	}

	trunc := "hour"
	if filter.Granularity == "1d" {
		trunc = "day"
	} else if filter.Granularity == "15m" {
		trunc = "15m"
	}

	var truncExpr string
	if trunc == "15m" {
		truncExpr = "bucket_start"
	} else {
		truncExpr = fmt.Sprintf("date_trunc('%s', bucket_start AT TIME ZONE '%s') AT TIME ZONE '%s'", trunc, tz, tz)
	}

	whereClauses := []string{"bucket_start >= $1", "bucket_start < $2"}
	args := []interface{}{filter.StartTime, filter.EndTime}
	argIdx := 3

	if filter.CustomerID != nil && *filter.CustomerID != uuid.Nil {
		whereClauses = append(whereClauses, fmt.Sprintf("customer_id = $%d", argIdx))
		args = append(args, *filter.CustomerID)
		argIdx++
	}
	if filter.OrgID != nil && *filter.OrgID != uuid.Nil {
		whereClauses = append(whereClauses, fmt.Sprintf("organization_id = $%d", argIdx))
		args = append(args, *filter.OrgID)
		argIdx++
	}
	if filter.Currency != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("currency = $%d", argIdx))
		args = append(args, filter.Currency)
	}

	whereSQL := strings.Join(whereClauses, " AND ")

	query := fmt.Sprintf(`
		SELECT
			%s AS period_start,
			COALESCE(SUM(submitted_count), 0),
			COALESCE(SUM(delivered_count), 0),
			COALESCE(SUM(failed_count), 0),
			COALESCE(SUM(undelivered_count), 0),
			COALESCE(SUM(expired_count), 0),
			COALESCE(SUM(revenue_captured), 0),
			COALESCE(SUM(revenue_held), 0),
			COALESCE(SUM(latency_samples_count), 0),
			COALESCE(SUM(latency_sum_ms), 0)
		FROM analytics_rollup_customer_15m
		WHERE %s
		GROUP BY period_start
		ORDER BY period_start ASC
	`, truncExpr, whereSQL)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query time series: %w", err)
	}
	defer rows.Close()

	var points []TimeSeriesPoint
	for rows.Next() {
		var p TimeSeriesPoint
		var samplesCount, sumLatency int64
		err := rows.Scan(
			&p.Timestamp,
			&p.Submitted,
			&p.Delivered,
			&p.Failed,
			&p.Undelivered,
			&p.Expired,
			&p.Revenue,
			&p.RevenueHeld,
			&samplesCount,
			&sumLatency,
		)
		if err != nil {
			return nil, fmt.Errorf("scan time series row: %w", err)
		}

		if p.Submitted > 0 {
			p.DeliveryRate = float64(p.Delivered) / float64(p.Submitted) * 100.0
		}
		if samplesCount > 0 {
			avg := int(sumLatency / samplesCount)
			p.LatencyP50Ms = &avg
			p.LatencyP95Ms = &avg
		}
		points = append(points, p)
	}

	return points, rows.Err()
}

// QueryExecutiveDashboard computes high-level executive metrics.
func (r *Repository) QueryExecutiveDashboard(ctx context.Context, filter DashboardFilter) (*ExecutiveDashboard, error) {
	tsPoints, err := r.QueryTimeSeries(ctx, filter)
	if err != nil {
		return nil, err
	}

	dash := &ExecutiveDashboard{
		RevenueByCur: make(map[string]decimal.Decimal),
		CostByCur:    make(map[string]decimal.Decimal),
		MarginByCur:  make(map[string]decimal.Decimal),
		TimeSeries:   tsPoints,
	}

	for _, pt := range tsPoints {
		dash.TotalVolume += pt.Submitted
		dash.DeliveredVolume += pt.Delivered
		dash.FailedVolume += pt.Failed
	}
	if dash.TotalVolume > 0 {
		dash.DeliveryRate = float64(dash.DeliveredVolume) / float64(dash.TotalVolume) * 100.0
	}

	// Currency totals from rollups
	curRows, err := r.pool.Query(ctx, `
		SELECT currency, COALESCE(SUM(revenue_captured), 0)
		FROM analytics_rollup_customer_15m
		WHERE bucket_start >= $1 AND bucket_start < $2
		GROUP BY currency
	`, filter.StartTime, filter.EndTime)
	if err == nil {
		for curRows.Next() {
			var cur string
			var rev decimal.Decimal
			if curRows.Scan(&cur, &rev) == nil {
				dash.RevenueByCur[cur] = rev
			}
		}
		curRows.Close()
	}

	// Cost by currency (Platform only)
	costRows, err := r.pool.Query(ctx, `
		SELECT currency, COALESCE(SUM(cost_estimated), 0)
		FROM analytics_rollup_provider_15m
		WHERE bucket_start >= $1 AND bucket_start < $2
		GROUP BY currency
	`, filter.StartTime, filter.EndTime)
	if err == nil {
		for costRows.Next() {
			var cur string
			var cost decimal.Decimal
			if costRows.Scan(&cur, &cost) == nil {
				dash.CostByCur[cur] = cost
				if rev, ok := dash.RevenueByCur[cur]; ok {
					dash.MarginByCur[cur] = rev.Sub(cost)
				}
			}
		}
		costRows.Close()
	}

	// Top destinations
	destRows, err := r.pool.Query(ctx, `
		SELECT country_iso, COALESCE(SUM(submitted_count), 0), COALESCE(SUM(delivered_count), 0)
		FROM analytics_rollup_destination_15m
		WHERE bucket_start >= $1 AND bucket_start < $2
		GROUP BY country_iso
		ORDER BY SUM(submitted_count) DESC
		LIMIT 10
	`, filter.StartTime, filter.EndTime)
	if err == nil {
		for destRows.Next() {
			var d DestinationSummary
			var sub, del int64
			if destRows.Scan(&d.CountryISO, &sub, &del) == nil {
				d.Volume = sub
				if sub > 0 {
					d.DeliveryRate = float64(del) / float64(sub) * 100.0
				}
				dash.TopDestinations = append(dash.TopDestinations, d)
			}
		}
		destRows.Close()
	}

	return dash, nil
}

// QueryNOCDashboard returns operational statistics for the NOC view.
func (r *Repository) QueryNOCDashboard(ctx context.Context, filter DashboardFilter) (*NOCDashboard, error) {
	tsPoints, err := r.QueryTimeSeries(ctx, filter)
	if err != nil {
		return nil, err
	}

	dash := &NOCDashboard{
		TimeSeries: tsPoints,
	}

	// Outbox depth and oldest age
	_ = r.pool.QueryRow(ctx, `
		SELECT
			count(*),
			coalesce(extract(epoch from now() - min(created_at)), 0)
		FROM message_outbox
		WHERE status = 'pending'
	`).Scan(&dash.OutboxDepth, &dash.OutboxOldestAgeSec)

	// Rolling 60s ingress and egress TPS
	_ = r.pool.QueryRow(ctx, `
		SELECT
			ROUND(count(*)::numeric / 60.0, 2)
		FROM message_events
		WHERE created_at >= now() - interval '60 seconds'
		  AND to_status = 'accepted'
	`).Scan(&dash.CurrentIngressTPS)

	_ = r.pool.QueryRow(ctx, `
		SELECT
			ROUND(count(*)::numeric / 60.0, 2)
		FROM message_events
		WHERE created_at >= now() - interval '60 seconds'
		  AND to_status = 'submitted'
	`).Scan(&dash.CurrentEgressTPS)

	// 24h delivery success rate
	var sub24h, del24h int64
	_ = r.pool.QueryRow(ctx, `
		SELECT
			coalesce(sum(submitted_count), 0),
			coalesce(sum(delivered_count), 0)
		FROM analytics_rollup_customer_15m
		WHERE bucket_start >= now() - interval '24 hours'
	`).Scan(&sub24h, &del24h)
	if sub24h > 0 {
		dash.DeliverySuccess24h = float64(del24h) / float64(sub24h) * 100.0
	}

	// Active connection health
	connRows, err := r.pool.Query(ctx, `
		SELECT
			pc.id, pc.name, p.name, pc.protocol, pc.status
		FROM provider_connections pc
		JOIN providers p ON p.id = pc.provider_id
		WHERE pc.status = 'active'
		ORDER BY pc.name ASC
	`)
	if err == nil {
		for connRows.Next() {
			var ch ConnectionHealthPoint
			if connRows.Scan(&ch.ConnectionID, &ch.ConnectionName, &ch.ProviderName, &ch.Protocol, &ch.Status) == nil {
				dash.ActiveConnections = append(dash.ActiveConnections, ch)
			}
		}
		connRows.Close()
	}

	// Heartbeats
	dash.Heartbeats, _ = r.ListHeartbeats(ctx)

	// Alert and Incident counts
	_ = r.pool.QueryRow(ctx, `SELECT count(*) FROM alerts WHERE status = 'firing'`).Scan(&dash.FiringAlertsCount)
	_ = r.pool.QueryRow(ctx, `SELECT count(*) FROM incidents WHERE status <> 'resolved'`).Scan(&dash.OpenIncidentsCount)

	return dash, nil
}

// QueryResellerDashboard returns reseller-scoped analytics.
func (r *Repository) QueryResellerDashboard(ctx context.Context, filter DashboardFilter) (*ResellerDashboard, error) {
	tsPoints, err := r.QueryTimeSeries(ctx, filter)
	if err != nil {
		return nil, err
	}

	dash := &ResellerDashboard{
		RevenueByCur: make(map[string]decimal.Decimal),
		TimeSeries:   tsPoints,
	}

	for _, pt := range tsPoints {
		dash.TotalVolume += pt.Submitted
		dash.DeliveredVolume += pt.Delivered
	}
	if dash.TotalVolume > 0 {
		dash.DeliveryRate = float64(dash.DeliveredVolume) / float64(dash.TotalVolume) * 100.0
	}

	// Top customers under this reseller
	if filter.OrgID != nil {
		custRows, err := r.pool.Query(ctx, `
			SELECT
				rc.customer_id,
				o.name,
				COALESCE(SUM(rc.submitted_count), 0),
				COALESCE(SUM(rc.delivered_count), 0),
				COALESCE(SUM(rc.revenue_captured), 0),
				rc.currency
			FROM analytics_rollup_customer_15m rc
			JOIN organizations o ON o.id = rc.customer_id
			WHERE rc.organization_id = $1 AND rc.bucket_start >= $2 AND rc.bucket_start < $3
			GROUP BY rc.customer_id, o.name, rc.currency
			ORDER BY SUM(rc.submitted_count) DESC
			LIMIT 10
		`, *filter.OrgID, filter.StartTime, filter.EndTime)
		if err == nil {
			for custRows.Next() {
				var cs CustomerSummary
				var sub, del int64
				if custRows.Scan(&cs.CustomerID, &cs.CustomerName, &sub, &del, &cs.Revenue, &cs.Currency) == nil {
					cs.Volume = sub
					if sub > 0 {
						cs.DeliveryRate = float64(del) / float64(sub) * 100.0
					}
					dash.TopCustomers = append(dash.TopCustomers, cs)
				}
			}
			custRows.Close()
		}
		dash.ActiveCustomers = len(dash.TopCustomers)
	}

	return dash, nil
}

// QueryCustomerDashboard returns customer-scoped self-service dashboard data.
func (r *Repository) QueryCustomerDashboard(ctx context.Context, filter DashboardFilter) (*CustomerDashboard, error) {
	tsPoints, err := r.QueryTimeSeries(ctx, filter)
	if err != nil {
		return nil, err
	}

	dash := &CustomerDashboard{
		TimeSeries: tsPoints,
	}

	for _, pt := range tsPoints {
		dash.TotalVolume += pt.Submitted
		dash.DeliveredVolume += pt.Delivered
		dash.TotalSpent = dash.TotalSpent.Add(pt.Revenue)
	}
	if dash.TotalVolume > 0 {
		dash.DeliveryRate = float64(dash.DeliveredVolume) / float64(dash.TotalVolume) * 100.0
	}

	// Customer balance remaining
	if filter.CustomerID != nil {
		_ = r.pool.QueryRow(ctx, `
			SELECT currency, balance
			FROM wallets
			WHERE organization_id = $1
			ORDER BY created_at ASC
			LIMIT 1
		`, *filter.CustomerID).Scan(&dash.Currency, &dash.BalanceRemaining)
	}

	return dash, nil
}

// RecordHeartbeat upserts an instance's heartbeat record.
func (r *Repository) RecordHeartbeat(ctx context.Context, hb ServiceHeartbeat) error {
	mJSON, err := json.Marshal(hb.MetricsJSON)
	if err != nil {
		mJSON = []byte("{}")
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO service_heartbeats (role, instance_id, hostname, last_seen_at, metrics_json)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (role, instance_id) DO UPDATE SET
			hostname = EXCLUDED.hostname,
			last_seen_at = EXCLUDED.last_seen_at,
			metrics_json = EXCLUDED.metrics_json
	`, hb.Role, hb.InstanceID, hb.Hostname, hb.LastSeenAt, mJSON)
	return err
}

// ListHeartbeats retrieves all active service heartbeats seen in the last 5 minutes.
func (r *Repository) ListHeartbeats(ctx context.Context) ([]ServiceHeartbeat, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT role, instance_id, hostname, last_seen_at, metrics_json
		FROM service_heartbeats
		WHERE last_seen_at >= now() - interval '5 minutes'
		ORDER BY role ASC, instance_id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hbs []ServiceHeartbeat
	for rows.Next() {
		var hb ServiceHeartbeat
		var mJSON []byte
		if err := rows.Scan(&hb.Role, &hb.InstanceID, &hb.Hostname, &hb.LastSeenAt, &mJSON); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(mJSON, &hb.MetricsJSON)
		hbs = append(hbs, hb)
	}
	return hbs, rows.Err()
}

// RebuildRollups truncates rollups after fromTime and resets the cursor to replay events.
func (r *Repository) RebuildRollups(ctx context.Context, fromTime time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM analytics_rollup_customer_15m WHERE bucket_start >= $1`, fromTime); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM analytics_rollup_provider_15m WHERE bucket_start >= $1`, fromTime); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM analytics_rollup_destination_15m WHERE bucket_start >= $1`, fromTime); err != nil {
		return err
	}

	// Reset cursor
	_, err = tx.Exec(ctx, `
		UPDATE analytics_cursors
		SET last_event_id = NULL, last_event_time = $1, updated_at = now()
		WHERE cursor_name = 'message_events'
	`, fromTime)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}
