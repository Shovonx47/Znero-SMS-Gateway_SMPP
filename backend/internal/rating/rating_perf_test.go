package rating_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/masterdata/importjob"
	"github.com/znero-international/znero/backend/internal/rating"
	"github.com/znero-international/znero/backend/internal/testkit"
)

// -----------------------------------------------------------------------------
// 100,000-ROW STREAMING CSV PARSE PERFORMANCE TEST
// -----------------------------------------------------------------------------

func TestLargeCSVStreamingParsePerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 100,000-row performance test in short mode")
	}

	const rowCount = 100000
	t.Logf("Generating %d-row rating CSV in memory...", rowCount)

	var sb strings.Builder
	// Write standard rate sheet CSV header
	sb.WriteString("country_iso2,operator_mcc,operator_mnc,message_type,price,effective_from\n")

	// Pre-generate sample data
	for i := 1; i <= rowCount; i++ {
		sb.WriteString(fmt.Sprintf("US,310,%03d,default,0.%06d,2026-06-01T00:00:00Z\n", i%999+1, 5000+(i%1000)))
	}

	csvData := sb.String()
	dataSizeMB := float64(len(csvData)) / (1024 * 1024)
	t.Logf("Generated CSV size: %.2f MB", dataSizeMB)

	// Measure baseline memory before streaming parse
	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	startTime := time.Now()
	rowsProcessed := 0
	batchCount := 0

	// StreamParseCSV in batches of 500 rows
	err := importjob.StreamParseCSV(strings.NewReader(csvData), 500, func(chunk []importjob.ParsedRow) error {
		batchCount++
		rowsProcessed += len(chunk)

		// Simulate validation on chunk
		for _, row := range chunk {
			_ = row.Data["country_iso2"]
			_ = row.Data["price"]
		}
		return nil
	})
	if err != nil {
		t.Fatalf("StreamParseCSV failed: %v", err)
	}

	duration := time.Since(startTime)
	throughput := float64(rowsProcessed) / duration.Seconds()

	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	heapAllocMB := float64(memAfter.HeapAlloc-memBefore.HeapAlloc) / (1024 * 1024)

	t.Logf("=== 100,000-Row Streaming CSV Parse Benchmark ===")
	t.Logf("Total Rows Parsed:  %d in %d batches", rowsProcessed, batchCount)
	t.Logf("Elapsed Time:       %v", duration)
	t.Logf("Throughput:         %.0f rows/sec", throughput)
	t.Logf("Heap Alloc Delta:   %.2f MB", heapAllocMB)

	if rowsProcessed != rowCount {
		t.Fatalf("expected %d rows processed, got %d", rowCount, rowsProcessed)
	}
	// Sanity check: 100k rows should be parsed well within 15 seconds
	if duration > 15*time.Second {
		t.Errorf("streaming parse took too long: %v (expected < 15s)", duration)
	}
}

// -----------------------------------------------------------------------------
// LOOKUP BENCHMARKS: DB LOOKUP vs IN-MEMORY LOOKUP
// -----------------------------------------------------------------------------

func BenchmarkLookup(b *testing.B) {
	e := testkit.New(b)
	ctx := context.Background()

	// Setup country
	cRes, err := e.Country.Create(ctx, e.Override, e.CountryRequest())
	if err != nil {
		b.Fatalf("create country: %v", err)
	}
	cID := cRes.(*country.Country).ID

	ratingRepo := rating.NewRepository(e.Pool)
	engine := rating.NewInMemoryEngine(ratingRepo)
	dbEngine := rating.NewDBEngine(ratingRepo)
	ratingSvc := rating.NewService(ratingRepo, e.Audit, e.Approval, nil, engine)

	// Create sell card
	cardRes, err := ratingSvc.CreateCard(ctx, e.Override, &rating.RateCard{
		Name:         "Bench Card " + testkit.RandHex(3),
		Kind:         "customer_sell",
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
	}, "bench")
	if err != nil {
		b.Fatalf("create card: %v", err)
	}
	cardID := cardRes.Card.ID

	// Assign card to customer
	custOrg, _ := e.ChildOrg(e.PlatformOrgID, e.PlatformOrgPath, "bench-cust", "customer")
	now := time.Now().UTC().Add(-time.Hour)
	_, err = ratingSvc.CreateAssignment(ctx, e.Override, custOrg, cardID, now, nil, "assign")
	if err != nil {
		b.Fatalf("assign card: %v", err)
	}

	// Insert rate
	_, err = ratingSvc.CreateRate(ctx, e.Override, &rating.Rate{
		CardID:        cardID,
		CountryID:     cID,
		MessageType:   "default",
		Price:         decimal.RequireFromString("0.007500"),
		EffectiveFrom: now,
	}, "bench rate")
	if err != nil {
		b.Fatalf("create rate: %v", err)
	}

	// Reload in-memory snapshot
	if err := ratingSvc.ReloadEngine(ctx); err != nil {
		b.Fatalf("reload engine: %v", err)
	}

	lookupTime := time.Now().UTC()

	// Benchmark Database Query Lookup
	b.Run("DBEngine_LookupSellRate", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			match, err := dbEngine.LookupSellRate(ctx, custOrg, cID, nil, "default", lookupTime)
			if err != nil || !match.Found {
				b.Fatalf("db lookup failed: %v", err)
			}
		}
	})

	// Benchmark In-Memory Atomic Pointer Lookup
	b.Run("InMemoryEngine_LookupSellRate", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			match, err := engine.LookupSellRate(ctx, custOrg, cID, nil, "default", lookupTime)
			if err != nil || !match.Found {
				b.Fatalf("in-memory lookup failed: %v", err)
			}
		}
	})
}

// -----------------------------------------------------------------------------
// FUTURE-DATED RATES DYNAMIC EVALUATION TEST (WITHOUT ENGINE RELOAD)
// -----------------------------------------------------------------------------

func TestInMemoryEngine_FutureDatedRatesWithoutReload(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()

	cRes, err := e.Country.Create(ctx, e.Override, e.CountryRequest())
	if err != nil {
		t.Fatalf("create country: %v", err)
	}
	cID := cRes.(*country.Country).ID

	ratingRepo := rating.NewRepository(e.Pool)
	engine := rating.NewInMemoryEngine(ratingRepo)
	ratingSvc := rating.NewService(ratingRepo, e.Audit, e.Approval, nil, engine)

	cardRes, err := ratingSvc.CreateCard(ctx, e.Override, &rating.RateCard{
		Name:         "Future Card " + testkit.RandHex(3),
		Kind:         "customer_sell",
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
	}, "future test")
	if err != nil {
		t.Fatalf("create card: %v", err)
	}
	cardID := cardRes.Card.ID

	custOrg, _ := e.ChildOrg(e.PlatformOrgID, e.PlatformOrgPath, "future-cust", "customer")
	t0 := time.Now().UTC().Truncate(time.Second)
	t1 := t0.Add(1 * time.Hour)

	_, err = ratingSvc.CreateAssignment(ctx, e.Override, custOrg, cardID, t0.Add(-time.Hour), nil, "assign")
	if err != nil {
		t.Fatalf("assign card: %v", err)
	}

	// Rate 1: effective [t0, t1) @ 0.050000
	_, err = ratingSvc.CreateRate(ctx, e.Override, &rating.Rate{
		CardID:        cardID,
		CountryID:     cID,
		MessageType:   "default",
		Price:         decimal.RequireFromString("0.050000"),
		EffectiveFrom: t0,
		EffectiveTo:   &t1,
	}, "rate 1")
	if err != nil {
		t.Fatalf("create rate 1: %v", err)
	}

	// Rate 2: effective [t1, nil) @ 0.080000 (future-dated rate)
	_, err = ratingSvc.CreateRate(ctx, e.Override, &rating.Rate{
		CardID:        cardID,
		CountryID:     cID,
		MessageType:   "default",
		Price:         decimal.RequireFromString("0.080000"),
		EffectiveFrom: t1,
	}, "rate 2")
	if err != nil {
		t.Fatalf("create rate 2: %v", err)
	}

	// Load snapshot into in-memory engine ONCE
	if err := ratingSvc.ReloadEngine(ctx); err != nil {
		t.Fatalf("reload engine: %v", err)
	}

	// 1. Query at t0 + 10m -> MUST return Rate 1 (0.050000)
	m1, err := engine.LookupSellRate(ctx, custOrg, cID, nil, "default", t0.Add(10*time.Minute))
	if err != nil || !m1.Found || !m1.Price.Equal(decimal.RequireFromString("0.050000")) {
		t.Fatalf("expected rate 1 (0.050000) at t0+10m, got found=%v match=%+v err=%v", m1.Found, m1, err)
	}

	// 2. Query at t1 + 10m (WITHOUT calling ReloadEngine!) -> MUST return Rate 2 (0.080000)
	// This proves future-dated rates take effect at their instant without an engine reload!
	m2, err := engine.LookupSellRate(ctx, custOrg, cID, nil, "default", t1.Add(10*time.Minute))
	if err != nil || !m2.Found || !m2.Price.Equal(decimal.RequireFromString("0.080000")) {
		t.Fatalf("expected future rate 2 (0.080000) at t1+10m without reload, got found=%v match=%+v err=%v", m2.Found, m2, err)
	}

	// 3. Query at exact boundary t1 -> MUST return Rate 2 (half-open [t1, nil))
	mExact, err := engine.LookupSellRate(ctx, custOrg, cID, nil, "default", t1)
	if err != nil || !mExact.Found || !mExact.Price.Equal(decimal.RequireFromString("0.080000")) {
		t.Fatalf("expected rate 2 at exact boundary t1, got found=%v match=%+v err=%v", mExact.Found, mExact, err)
	}

	// 4. Query before t0 -> MUST return not found
	mBefore, err := engine.LookupSellRate(ctx, custOrg, cID, nil, "default", t0.Add(-10*time.Minute))
	if err != nil || mBefore.Found {
		t.Fatalf("expected not found before t0, got found=%v match=%+v err=%v", mBefore.Found, mBefore, err)
	}
}

// -----------------------------------------------------------------------------
// END-TO-END LARGE RATE SHEET IMPORT, MAKER-CHECKER, OVERLAP & ROLLBACK TEST
// -----------------------------------------------------------------------------

func TestImport100kRatesEndToEnd(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()

	// Wire rating repo and services into import service
	ratingRepo := rating.NewRepository(e.Pool)
	engine := rating.NewInMemoryEngine(ratingRepo)
	ratingSvc := rating.NewService(ratingRepo, e.Audit, e.Approval, nil, engine)
	e.Import.SetRatingServices(ratingRepo, ratingSvc)

	// Setup country
	cRes, err := e.Country.Create(ctx, e.Override, e.CountryRequest())
	if err != nil {
		t.Fatalf("create country: %v", err)
	}
	c := cRes.(*country.Country)

	// Setup rate card
	cardRes, err := ratingSvc.CreateCard(ctx, e.Override, &rating.RateCard{
		Name:         "Wholesale 100k Card " + testkit.RandHex(3),
		Kind:         "customer_sell",
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
	}, "wholesale 100k card")
	if err != nil {
		t.Fatalf("create card: %v", err)
	}
	cardID := cardRes.Card.ID

	// Allow full 100,000 rows when env ZNERO_100K_TEST=1, else 25,000 rows (exceeds default 20k import limit)
	rowCount := 25000
	if os.Getenv("ZNERO_100K_TEST") == "1" {
		rowCount = 100000
	} else if testing.Short() {
		rowCount = 5000
	}

	t.Logf("Generating %d wholesale rate rows in CSV format...", rowCount)
	var sb strings.Builder
	sb.WriteString("rate_card_id,country_iso2,message_type,price,effective_from,effective_to\n")
	now := time.Now().UTC().Truncate(time.Second)
	for i := 1; i <= rowCount; i++ {
		effFrom := now.Add(time.Duration(i) * time.Hour).Format(time.RFC3339)
		effToStr := ""
		if i < rowCount {
			effToStr = now.Add(time.Duration(i+1) * time.Hour).Format(time.RFC3339)
		}
		sb.WriteString(fmt.Sprintf("%s,%s,default,0.%06d,%s,%s\n", cardID, c.ISO2, 1000+(i%9000), effFrom, effToStr))
	}
	csvData := sb.String()

	t.Logf("Generated CSV size: %.2f MB. Beginning UploadAndPreview...", float64(len(csvData))/(1024*1024))
	t0 := time.Now()

	// 1. Upload & Preview
	job, err := e.Import.UploadAndPreview(ctx, e.Maker, "customer_sell_rate", "wholesale_100k.csv", int64(len(csvData)), "text/csv", strings.NewReader(csvData))
	if err != nil {
		t.Fatalf("UploadAndPreview failed: %v", err)
	}
	previewDuration := time.Since(t0)
	t.Logf("Upload & Preview completed in %v (throughput: %.0f rows/sec)", previewDuration, float64(rowCount)/previewDuration.Seconds())

	if job.TotalRows != rowCount || job.ValidRows != rowCount || job.InvalidRows != 0 {
		t.Fatalf("expected total=%d valid=%d invalid=0, got total=%d valid=%d invalid=%d", rowCount, rowCount, job.TotalRows, job.ValidRows, job.InvalidRows)
	}

	// 2. Maker confirms -> routes to Maker-Checker approval
	t1 := time.Now()
	res, err := e.Import.ConfirmImport(ctx, e.Maker, job.ID, false)
	if err != nil {
		t.Fatalf("maker ConfirmImport: %v", err)
	}
	cr, ok := res.(*approval.ChangeRequest)
	if !ok || cr.EntityType != "import_job" || cr.Operation != "confirm_import" || cr.Status != "pending" {
		t.Fatalf("expected pending import_job ChangeRequest, got %T %+v", res, res)
	}

	// Verify ChangeRequest ProposedData has summary and rate diff samples
	var proposed struct {
		JobID           uuid.UUID `json:"job_id"`
		TotalRows       int       `json:"total_rows"`
		ValidRows       int       `json:"valid_rows"`
		RateDiffSamples []any     `json:"rate_diff_samples"`
	}
	_ = json.Unmarshal(cr.ProposedData, &proposed)
	if proposed.TotalRows != rowCount || len(proposed.RateDiffSamples) == 0 {
		t.Fatalf("expected proposed data with %d total rows and samples, got %+v", rowCount, proposed)
	}

	// 3. Checker Approves -> executes batched insert and closes predecessors
	if err := e.Approval.Approve(ctx, e.Checker, cr.ID, "checker approved wholesale 100k import"); err != nil {
		t.Fatalf("checker Approve failed: %v", err)
	}
	importDuration := time.Since(t1)
	t.Logf("Checker approval & batched import completed in %v (throughput: %.0f rows/sec)", importDuration, float64(rowCount)/importDuration.Seconds())

	// 4. Verify rates in PostgreSQL
	var dbCount int
	if err := e.Pool.QueryRow(ctx, `SELECT count(*) FROM rates WHERE card_id = $1 AND status = 'active'`, cardID).Scan(&dbCount); err != nil {
		t.Fatalf("query db count: %v", err)
	}
	if dbCount != rowCount {
		t.Fatalf("expected %d active rates in DB, got %d", rowCount, dbCount)
	}

	// 5. Test PostgreSQL exclusion constraint (overlap protection)
	conflictingRate := &rating.Rate{
		CardID:        cardID,
		CountryID:     c.ID,
		MessageType:   "default",
		Price:         decimal.RequireFromString("0.099999"),
		EffectiveFrom: now.Add(1 * time.Hour), // overlaps with row 1!
		Status:        "active",
	}
	_, err = ratingRepo.CreateRate(ctx, nil, conflictingRate, "overlap test")
	if err == nil {
		t.Fatalf("expected PostgreSQL overlap constraint violation error, got nil")
	}
	t.Logf("PostgreSQL overlap constraint verified: rejected conflicting rate: %v", err)

	// 6. Test Rollback: Rollback the entire import job
	t2 := time.Now()
	if err := e.Import.RollbackImport(ctx, e.Override, job.ID); err != nil {
		t.Fatalf("RollbackImport failed: %v", err)
	}
	rollbackDuration := time.Since(t2)
	t.Logf("Rollback completed in %v", rollbackDuration)

	// Verify all rates are deactivated in DB
	var activeAfterRollback int
	if err := e.Pool.QueryRow(ctx, `SELECT count(*) FROM rates WHERE card_id = $1 AND status = 'active'`, cardID).Scan(&activeAfterRollback); err != nil {
		t.Fatalf("query db count after rollback: %v", err)
	}
	if activeAfterRollback != 0 {
		t.Fatalf("expected 0 active rates after rollback, got %d", activeAfterRollback)
	}
}
