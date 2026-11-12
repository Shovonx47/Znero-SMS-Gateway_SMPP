package messaging_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/znero-international/znero/backend/internal/testkit"
)

func TestMessages500kPartitions_ExplainAnalyze_NoFullScan(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()

	// Create 4 synthetic historical monthly partitions that do not collide with live test months
	partMonths := []struct {
		name  string
		start time.Time
		end   time.Time
	}{
		{"messages_2025_06", time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)},
		{"messages_2025_07", time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 8, 1, 0, 0, 0, 0, time.UTC)},
		{"messages_2025_08", time.Date(2025, 8, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC)},
		{"messages_2025_09", time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)},
	}

	for _, pm := range partMonths {
		ddl := fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s PARTITION OF messages FOR VALUES FROM ('%s') TO ('%s')`,
			pm.name, pm.start.Format("2006-01-02 15:04:05+00"), pm.end.Format("2006-01-02 15:04:05+00"),
		)
		if _, err := e.Pool.Exec(ctx, ddl); err != nil {
			t.Fatalf("create synthetic partition %s: %v", pm.name, err)
		}
	}

	custOrgID, _ := e.ChildOrg(e.PlatformOrgID, e.PlatformOrgPath, "part500k-"+testkit.RandHex(4), "customer")
	otherCustID, _ := e.ChildOrg(e.PlatformOrgID, e.PlatformOrgPath, "part500k-other-"+testkit.RandHex(4), "customer")
	var provID, connID uuid.UUID
	_ = e.Pool.QueryRow(ctx, `
		INSERT INTO providers (code, name, provider_type, status, version)
		VALUES ($1, 'Part500k Prov', 'http', 'active', 1) RETURNING id`,
		"P500K_"+testkit.RandHex(4),
	).Scan(&provID)
	_ = e.Pool.QueryRow(ctx, `
		INSERT INTO provider_connections (provider_id, name, protocol, status, version, max_tps)
		VALUES ($1, $2, 'http', 'active', 1, 100) RETURNING id`,
		provID, "P500K-Conn-"+testkit.RandHex(4),
	).Scan(&connID)

	t.Cleanup(func() {
		bg := context.Background()
		for _, pm := range partMonths {
			_, _ = e.Pool.Exec(bg, fmt.Sprintf(`DROP TABLE IF EXISTS %s CASCADE`, pm.name))
		}
		_, _ = e.Pool.Exec(bg, `DELETE FROM provider_connections WHERE id = $1`, connID)
		_, _ = e.Pool.Exec(bg, `DELETE FROM providers WHERE id = $1`, provID)
	})

	// Bulk-load 500,000 rows (125,000 per synthetic partition) via pgx.CopyFrom.
	// Sparse distribution: 200 rows belong to target customer/submitted in 2025-09,
	// 499,800 rows belong to otherCustID with status='delivered'.
	const perPart = 125_000
	cols := []string{
		"id", "created_at", "customer_org_id", "original_destination", "destination",
		"sender_id", "encrypted_body", "key_id", "status", "provider_connection_id",
		"provider_message_id", "expires_at", "submitted_at",
		"billing_mode_snapshot", "dlr_wait_hours_snapshot", "no_dlr_policy_snapshot", "failed_after_accept_policy_snapshot",
	}

	var sampleID uuid.UUID
	var sampleCreatedAt time.Time
	encBody := []byte("enc500k")

	for pIdx, pm := range partMonths {
		rows := make([][]any, perPart)
		baseTS := pm.start.Add(24 * time.Hour)
		for i := 0; i < perPart; i++ {
			id := uuid.New()
			ts := baseTS.Add(time.Duration(i) * time.Second)
			org := otherCustID
			st := "delivered"
			provMsg := ""
			if pIdx == 3 && i < 200 {
				org = custOrgID
				if i < 50 {
					st = "submitted"
					provMsg = fmt.Sprintf("p500k-%d", i)
				}
				if i == 0 {
					sampleID = id
					sampleCreatedAt = ts
				}
			}
			rows[i] = []any{
				id, ts, org, "+12025550100", "+12025550100",
				"ZNERO", encBody, "v1", st, connID,
				provMsg, ts.Add(24 * time.Hour), ts,
				"on_delivery", 24, "retain_until_resolution", "keep_charge",
			}
		}
		copied, err := e.Pool.CopyFrom(ctx, pgx.Identifier{"messages"}, cols, pgx.CopyFromRows(rows))
		if err != nil {
			t.Fatalf("CopyFrom %s: %v", pm.name, err)
		}
		if copied != perPart {
			t.Fatalf("expected %d rows in %s, copied %d", perPart, pm.name, copied)
		}
	}

	if _, err := e.Pool.Exec(ctx, `ANALYZE messages`); err != nil {
		t.Fatalf("ANALYZE messages: %v", err)
	}

	// 1. Point lookup with partition key -> zero Seq Scan, prunes to 1 partition
	checkExplainPlan(t, e, "point_lookup",
		`EXPLAIN (ANALYZE, FORMAT JSON) SELECT id, status FROM messages WHERE id = $1 AND created_at = $2`,
		[]any{sampleID, sampleCreatedAt},
		false, true,
	)

	// 2. Customer list filtered by customer_org_id + created_at range -> Index Scan + partition pruning
	fromTS := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC)
	toTS := time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)
	checkExplainPlan(t, e, "customer_list_range",
		`EXPLAIN (ANALYZE, FORMAT JSON)
		 SELECT id, status, created_at FROM messages
		 WHERE customer_org_id = $1 AND created_at >= $2 AND created_at < $3
		 ORDER BY created_at DESC LIMIT 50`,
		[]any{custOrgID, fromTS, toTS},
		true, true,
	)

	// 3. HTTP DLR Poller query -> partition-pruned via created_at >= $2 AND index scan on sparse submitted status
	checkExplainPlan(t, e, "dlr_poller_window",
		`EXPLAIN (ANALYZE, FORMAT JSON)
		 SELECT id, created_at, provider_message_id FROM messages
		 WHERE provider_connection_id = $1
		   AND status = 'submitted'
		   AND provider_message_id IS NOT NULL
		   AND provider_message_id <> ''
		   AND created_at >= $2
		 ORDER BY created_at ASC LIMIT 100`,
		[]any{connID, fromTS},
		true, false,
	)

	// 4. Reaper No-DLR query -> must use index scan on sparse status='submitted', NEVER Seq Scan across 500k rows
	checkExplainPlan(t, e, "reaper_no_dlr",
		`EXPLAIN (ANALYZE, FORMAT JSON)
		 SELECT id, created_at FROM messages
		 WHERE status = 'submitted'
		   AND COALESCE(submitted_at, created_at) + make_interval(hours => dlr_wait_hours_snapshot) <= $1
		 LIMIT 100`,
		[]any{time.Date(2025, 10, 5, 0, 0, 0, 0, time.UTC)},
		false, true,
	)
}

func checkExplainPlan(t *testing.T, e *testkit.Env, name, query string, args []any, requirePruning bool, requireIndex bool) {
	t.Helper()
	var planJSON []byte
	if err := e.Pool.QueryRow(context.Background(), query, args...).Scan(&planJSON); err != nil {
		t.Fatalf("%s EXPLAIN ANALYZE failed: %v", name, err)
	}

	var top []map[string]any
	if err := json.Unmarshal(planJSON, &top); err != nil || len(top) == 0 {
		t.Fatalf("%s invalid explain JSON: %v", name, err)
	}
	root, _ := top[0]["Plan"].(map[string]any)

	var nodeTypes []string
	var seqScannedRelations []string
	var subplansRemoved float64
	var hasIndexScan bool

	var walk func(n map[string]any)
	walk = func(n map[string]any) {
		if nt, ok := n["Node Type"].(string); ok {
			nodeTypes = append(nodeTypes, nt)
			if strings.Contains(nt, "Index") {
				hasIndexScan = true
			}
			if nt == "Seq Scan" {
				rel, _ := n["Relation Name"].(string)
				rows, _ := n["Actual Rows"].(float64)
				removed, _ := n["Rows Removed by Filter"].(float64)
				if strings.HasPrefix(rel, "messages_2025_") || rows > 1000 || removed > 1000 {
					seqScannedRelations = append(seqScannedRelations, fmt.Sprintf("%s(rows=%.0f,removed=%.0f)", rel, rows, removed))
				}
			}
		}
		if sr, ok := n["Subplans Removed"].(float64); ok {
			subplansRemoved += sr
		}
		if plans, ok := n["Plans"].([]any); ok {
			for _, child := range plans {
				if cm, ok := child.(map[string]any); ok {
					walk(cm)
				}
			}
		}
	}
	walk(root)

	t.Logf("%s plan: nodes=%v subplans_removed=%.0f exec_ms=%v", name, nodeTypes, subplansRemoved, top[0]["Execution Time"])
	if len(seqScannedRelations) > 0 {
		t.Fatalf("%s fell back to Seq Scan on %v (nodes=%v)\n%s", name, seqScannedRelations, nodeTypes, string(planJSON))
	}
	if requireIndex && !hasIndexScan {
		t.Fatalf("%s expected Index Scan / Bitmap Index Scan, got %v\n%s", name, nodeTypes, string(planJSON))
	}
	if requirePruning && subplansRemoved == 0 && !strings.Contains(string(planJSON), `"messages_2025_09"`) {
		t.Fatalf("%s did not show partition pruning\n%s", name, string(planJSON))
	}
	if requirePruning && strings.Contains(string(planJSON), `"messages_2025_06"`) {
		t.Fatalf("%s unexpectedly included pruned partition messages_2025_06 in plan!\n%s", name, string(planJSON))
	}
}
