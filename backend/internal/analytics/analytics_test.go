package analytics_test

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/znero-international/znero/backend/internal/analytics"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

func TestLatencyHistogram(t *testing.T) {
	h := analytics.NewLatencyHistogram()

	// Observe known latencies
	samples := []int{20, 30, 45, 60, 80, 150, 200, 300, 800, 1500, 4000, 12000, 45000}
	for _, s := range samples {
		h.Observe(s)
	}

	if h.TotalSamples() != int64(len(samples)) {
		t.Fatalf("expected %d samples, got %d", len(samples), h.TotalSamples())
	}

	p50, p95, p99 := h.CalculatePercentiles()
	if p50 <= 0 || p95 <= p50 || p99 <= p95 {
		t.Fatalf("percentiles out of order: p50=%d, p95=%d, p99=%d", p50, p95, p99)
	}

	// Merge test
	h2 := analytics.NewLatencyHistogram()
	h2.Observe(10)
	h.Merge(h2)
	if h.TotalSamples() != int64(len(samples)+1) {
		t.Fatalf("expected %d samples after merge, got %d", len(samples)+1, h.TotalSamples())
	}
}

func TestResolveCountryISO(t *testing.T) {
	cases := []struct {
		dest string
		want string
	}{
		{"+12025550123", "US"},
		{"+442079460958", "GB"},
		{"+8801711223344", "BD"},
		{"+919876543210", "IN"},
		{"+9779841234567", "NP"},
		{"invalid", "XX"},
		{"", "XX"},
	}

	for _, tc := range cases {
		got := analytics.ResolveCountryISO(tc.dest)
		if got != tc.want {
			t.Errorf("ResolveCountryISO(%q) = %q, want %q", tc.dest, got, tc.want)
		}
	}
}

// Integration tests against real znero_test database
func TestAnalyticsEngine_DifferentialAndTieOut(t *testing.T) {
	dbURL := testkit.RequireIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	// Provision isolated organizations, customer, and provider connection
	var orgID uuid.UUID
	err = pool.QueryRow(ctx, `SELECT id FROM organizations WHERE type = 'platform' LIMIT 1`).Scan(&orgID)
	if err != nil {
		t.Fatalf("fetch platform org: %v", err)
	}

	customerOrgID := uuid.New()
	customerPath := "/" + orgID.String() + "/" + customerOrgID.String() + "/"
	providerID := uuid.New()
	connID := uuid.New()

	_, err = pool.Exec(ctx, `
		INSERT INTO organizations (id, name, type, parent_id, path, status)
		VALUES ($1, 'Test Analytics Customer', 'customer', $2, $3, 'active')
		ON CONFLICT (id) DO NOTHING;
	`, customerOrgID, orgID, customerPath)
	if err != nil {
		t.Fatalf("seed customer org: %v", err)
	}

	provCode := "TEST_PROV_" + testkit.RandHex(4)
	_, err = pool.Exec(ctx, `
		INSERT INTO providers (id, name, code, provider_type, status)
		VALUES ($1, 'Test Analytics Provider', $2, 'http', 'active')
		ON CONFLICT (id) DO NOTHING;
	`, providerID, provCode)
	if err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO provider_connections (id, provider_id, name, protocol, status, max_tps)
		VALUES ($1, $2, 'Test Analytics Conn', 'http', 'active', 100)
		ON CONFLICT (id) DO NOTHING;
	`, connID, providerID)
	if err != nil {
		t.Fatalf("seed provider connection: %v", err)
	}

	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		_, _ = pool.Exec(cctx, `DELETE FROM messages WHERE customer_org_id = $1`, customerOrgID)
		_, _ = pool.Exec(cctx, `DELETE FROM analytics_rollup_customer_15m WHERE customer_id = $1`, customerOrgID)
		_, _ = pool.Exec(cctx, `DELETE FROM analytics_rollup_provider_15m WHERE connection_id = $1`, connID)
		_, _ = pool.Exec(cctx, `DELETE FROM provider_connections WHERE id = $1`, connID)
		_, _ = pool.Exec(cctx, `DELETE FROM providers WHERE id = $1`, providerID)
		_, _ = pool.Exec(cctx, `DELETE FROM organizations WHERE id = $1`, customerOrgID)
	})

	repo := analytics.NewRepository(pool, nil)
	svc := analytics.NewService(repo, nil)

	baseTime := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	rng := rand.New(rand.NewSource(20261112))

	// Clean slate rollups and set cursor to right before baseTime
	_, _ = pool.Exec(ctx, `DELETE FROM analytics_rollup_customer_15m WHERE customer_id = $1`, customerOrgID)
	_, _ = pool.Exec(ctx, `DELETE FROM analytics_rollup_provider_15m WHERE connection_id = $1`, connID)
	_, _ = pool.Exec(ctx, `
		INSERT INTO analytics_cursors (cursor_name, last_event_id, last_event_time, updated_at)
		VALUES ('message_events', NULL, $1, now())
		ON CONFLICT (cursor_name) DO UPDATE SET last_event_id = NULL, last_event_time = EXCLUDED.last_event_time
	`, baseTime.Add(-1*time.Minute))

	// Insert realistic messages and lifecycle events across different timestamps
	destinations := []string{"+12025550100", "+447911123400", "+8801711223300", "+919876543200"}
	expectedSubmitted := 0
	expectedDelivered := 0
	expectedFailed := 0
	expectedCaptured := decimal.Zero

	for i := 0; i < 50; i++ {
		msgID := uuid.New()
		msgTime := baseTime.Add(time.Duration(i*2) * time.Minute)
		dest := destinations[rng.Intn(len(destinations))]
		price := decimal.NewFromFloat(0.05)
		cost := decimal.NewFromFloat(0.02)
		isDelivered := i%2 == 0
		isFailed := !isDelivered && i%5 == 0

		subTime := msgTime.Add(1 * time.Second)
		delTime := subTime.Add(250 * time.Millisecond)
		expiresTime := msgTime.Add(24 * time.Hour)

		status := "submitted"
		if isDelivered {
			status = "delivered"
		} else if isFailed {
			status = "failed"
		}

		// Insert into partitioned messages
		_, err := pool.Exec(ctx, `
			INSERT INTO messages (
				id, created_at, customer_org_id, original_destination, destination,
				sender_id, encrypted_body, key_id, status, sell_price, sell_currency,
				estimated_buy_cost, actual_buy_cost, buy_currency, provider_connection_id,
				expires_at, submitted_at, delivered_at
			) VALUES (
				$1, $2, $3, $4, $4,
				'TEST', 'testbody'::bytea, 'k1', $5, $6, 'USD',
				$7, $7, 'USD', $8,
				$9, $10, $11
			)
		`, msgID, msgTime, customerOrgID, dest, status, price, cost, connID, expiresTime, subTime, delTime)
		if err != nil {
			t.Fatalf("insert message %d: %v", i, err)
		}

		// Insert lifecycle events
		// 1. Accepted event
		_, _ = pool.Exec(ctx, `
			INSERT INTO message_events (id, message_id, message_created_at, event_type, from_status, to_status, created_at)
			VALUES ($1, $2, $3, 'accepted', NULL, 'accepted', $4)
		`, uuid.New(), msgID, msgTime, msgTime)

		// 2. Submitted event
		_, _ = pool.Exec(ctx, `
			INSERT INTO message_events (id, message_id, message_created_at, event_type, from_status, to_status, created_at)
			VALUES ($1, $2, $3, 'submitted', 'accepted', 'submitted', $4)
		`, uuid.New(), msgID, msgTime, subTime)
		expectedSubmitted++

		// 3. Terminal event
		if isDelivered {
			_, _ = pool.Exec(ctx, `
				INSERT INTO message_events (id, message_id, message_created_at, event_type, from_status, to_status, created_at)
				VALUES ($1, $2, $3, 'delivered', 'submitted', 'delivered', $4)
			`, uuid.New(), msgID, msgTime, delTime)
			expectedDelivered++
			expectedCaptured = expectedCaptured.Add(price)
		} else if isFailed {
			_, _ = pool.Exec(ctx, `
				INSERT INTO message_events (id, message_id, message_created_at, event_type, from_status, to_status, created_at)
				VALUES ($1, $2, $3, 'failed', 'submitted', 'failed', $4)
			`, uuid.New(), msgID, msgTime, delTime)
			expectedFailed++
		}
	}

	// 1. Run batch processing via repository
	n, err := repo.ProcessMessageEventsBatch(ctx, 1000)
	if err != nil {
		t.Fatalf("ProcessMessageEventsBatch failed: %v", err)
	}
	if n == 0 {
		t.Fatalf("expected events to be processed, got 0")
	}

	// 2. Differential Test: Assert rollup figures match brute force SQL
	var rollupSub, rollupDel, rollupFail int64
	var rollupCaptured decimal.Decimal
	err = pool.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(submitted_count), 0),
			COALESCE(SUM(delivered_count), 0),
			COALESCE(SUM(failed_count), 0),
			COALESCE(SUM(revenue_captured), 0)
		FROM analytics_rollup_customer_15m
		WHERE customer_id = $1
	`, customerOrgID).Scan(&rollupSub, &rollupDel, &rollupFail, &rollupCaptured)
	if err != nil {
		t.Fatalf("query rollups: %v", err)
	}

	if int(rollupSub) != expectedSubmitted {
		t.Errorf("differential mismatch: submitted rollup=%d, expected=%d", rollupSub, expectedSubmitted)
	}
	if int(rollupDel) != expectedDelivered {
		t.Errorf("differential mismatch: delivered rollup=%d, expected=%d", rollupDel, expectedDelivered)
	}
	if int(rollupFail) != expectedFailed {
		t.Errorf("differential mismatch: failed rollup=%d, expected=%d", rollupFail, expectedFailed)
	}
	if !rollupCaptured.Equal(expectedCaptured) {
		t.Errorf("financial tie-out mismatch: rollup captured=%s, expected=%s", rollupCaptured, expectedCaptured)
	}

	// 3. Test Service Dashboards with Tenant Scoping
	platformTc := &tenant.Context{
		ActorUserID:     uuid.New(),
		ActorOrgID:      orgID,
		ActorOrgType:    "platform",
		IsPlatformAdmin: true,
	}

	customerTc := &tenant.Context{
		ActorUserID:     uuid.New(),
		ActorOrgID:      customerOrgID,
		ActorOrgType:    "customer",
		IsPlatformAdmin: false,
	}

	filter := analytics.DashboardFilter{
		StartTime: baseTime.Add(-1 * time.Hour),
		EndTime:   baseTime.Add(24 * time.Hour),
	}

	// Platform view sees cost and margin
	execDash, err := svc.GetExecutiveDashboard(ctx, filter, platformTc)
	if err != nil {
		t.Fatalf("GetExecutiveDashboard platform: %v", err)
	}
	if execDash.TotalVolume == 0 {
		t.Errorf("expected executive dashboard volume > 0")
	}

	// Customer view NEVER sees cost or margin
	custDash, err := svc.GetExecutiveDashboard(ctx, filter, customerTc)
	if err != nil {
		t.Fatalf("GetExecutiveDashboard customer: %v", err)
	}
	if len(custDash.CostByCur) != 0 || len(custDash.MarginByCur) != 0 {
		t.Errorf("TENANCY LEAK: customer executive dashboard returned non-nil cost or margin: %v", custDash.CostByCur)
	}
}

func TestAnalyticsTimezoneRealignment(t *testing.T) {
	dbURL := testkit.RequireIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	repo := analytics.NewRepository(pool, nil)

	// Timezone tests across non-hourly timezones: India (+5:30) and Nepal (+5:45)
	timezones := []string{"UTC", "Asia/Dhaka", "Asia/Kolkata", "Asia/Kathmandu"}
	for _, tz := range timezones {
		filter := analytics.DashboardFilter{
			StartTime:   time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
			EndTime:     time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC),
			Timezone:    tz,
			Granularity: "1d",
		}

		pts, err := repo.QueryTimeSeries(ctx, filter)
		if err != nil {
			t.Fatalf("QueryTimeSeries failed for tz %s: %v", tz, err)
		}
		// Confirm query succeeds without SQL syntax or timezone errors
		t.Logf("Timezone %s returned %d aggregated daily points", tz, len(pts))
	}
}

func TestAnalyticsWorkerAdvisoryLock(t *testing.T) {
	dbURL := testkit.RequireIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	repo := analytics.NewRepository(pool, nil)

	worker1 := analytics.NewWorker(pool, repo, analytics.WorkerConfig{
		PollInterval: 50 * time.Millisecond,
		BatchSize:    100,
		InstanceID:   "worker-test-1",
	}, nil)

	worker1.Start(ctx)
	defer worker1.Stop()

	// Give worker1 time to acquire advisory lock
	time.Sleep(200 * time.Millisecond)

	// Verify advisory lock 8899001 is held in pg_locks
	var held bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_locks
			WHERE locktype = 'advisory' AND objid = 8899001
		)
	`).Scan(&held)
	if err != nil {
		t.Fatalf("check pg_locks: %v", err)
	}
	if !held {
		t.Errorf("advisory lock 8899001 is not held by running worker")
	}

	worker1.Stop()

	// Verify lock released
	time.Sleep(200 * time.Millisecond)
	_ = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_locks
			WHERE locktype = 'advisory' AND objid = 8899001
		)
	`).Scan(&held)
	if held {
		t.Errorf("advisory lock 8899001 still held after worker stopped")
	}
}

func TestAnalyticsCrashAndResumeCursor(t *testing.T) {
	dbURL := testkit.RequireIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	repo := analytics.NewRepository(pool, nil)

	// Fetch current cursor
	cur1, err := repo.GetCursor(ctx, "message_events")
	if err != nil {
		t.Fatalf("get cursor 1: %v", err)
	}
	if cur1 == nil {
		t.Fatalf("cursor expected to exist")
	}

	// Process batch with small batch size 5
	n1, err := repo.ProcessMessageEventsBatch(ctx, 5)
	if err != nil {
		t.Fatalf("process batch 1: %v", err)
	}

	cur2, err := repo.GetCursor(ctx, "message_events")
	if err != nil {
		t.Fatalf("get cursor 2: %v", err)
	}

	if n1 > 0 {
		if cur2.LastEventID == nil {
			t.Errorf("cursor LastEventID is nil after processing %d events", n1)
		}
	}

	// Verify idempotency: re-running with small batch size continues from cur2
	n2, err := repo.ProcessMessageEventsBatch(ctx, 5)
	if err != nil {
		t.Fatalf("process batch 2: %v", err)
	}

	cur3, err := repo.GetCursor(ctx, "message_events")
	if err != nil {
		t.Fatalf("get cursor 3: %v", err)
	}

	if n2 > 0 && cur2.LastEventID != nil {
		if cur3.LastEventID != nil && *cur3.LastEventID == *cur2.LastEventID && cur3.LastEventTime.Equal(*cur2.LastEventTime) {
			t.Errorf("cursor failed to advance between batches: %v", cur3)
		}
	}
}
