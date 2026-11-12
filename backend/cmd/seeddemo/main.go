package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/znero-international/znero/backend/internal/export"
	"github.com/znero-international/znero/backend/pkg/crypto/password"
)

func main() {
	dbFlag := flag.String("db", "", "Database connection URL (defaults to znero_demo)")
	flag.Parse()

	dbURL := *dbFlag
	if dbURL == "" {
		dbURL = "postgres://znero_app:pinhbk0lx2YbUNbxqxnEY7NCUSqUFC8t@localhost:5432/znero_demo?sslmode=disable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: connect to database %s: %v\n", dbURL, err)
		os.Exit(1)
	}
	defer pool.Close()

	fmt.Println("==================================================")
	fmt.Println("  ZNERO INTERNATIONAL: SEEDING DEMO STACK (znero_demo)")
	fmt.Println("==================================================")

	// 1. Ensure Platform Org
	var platID uuid.UUID
	err = pool.QueryRow(ctx, `SELECT id FROM organizations WHERE type = 'platform' LIMIT 1`).Scan(&platID)
	if err != nil {
		platID = uuid.New()
		path := fmt.Sprintf("/%s/", platID.String())
		_, err = pool.Exec(ctx, `
			INSERT INTO organizations (id, name, type, parent_id, path, status, created_at, updated_at)
			VALUES ($1, 'Znero International Platform', 'platform', NULL, $2, 'active', now(), now())
		`, platID, path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed inserting platform org: %v\n", err)
			os.Exit(1)
		}
	}
	fmt.Printf("[OK] Platform Organization ID: %s\n", platID)

	// 2. Ensure Roles in Platform Org
	ensureRole := func(orgID uuid.UUID, name string) uuid.UUID {
		var roleID uuid.UUID
		err := pool.QueryRow(ctx, `SELECT id FROM roles WHERE organization_id = $1 AND name = $2`, orgID, name).Scan(&roleID)
		if err != nil {
			roleID = uuid.New()
			_, _ = pool.Exec(ctx, `
				INSERT INTO roles (id, organization_id, name, description, is_system, created_at, updated_at)
				VALUES ($1, $2, $3, 'System role', true, now(), now())
			`, roleID, orgID, name)
		}
		// Bind all permissions if Super Admin
		if name == "Super Admin" {
			_, _ = pool.Exec(ctx, `
				INSERT INTO role_permissions (role_id, permission_code)
				SELECT $1, code FROM permissions
				ON CONFLICT DO NOTHING
			`, roleID)
		}
		return roleID
	}

	superAdminRoleID := ensureRole(platID, "Super Admin")

	// 3. Seed Users
	hashPwd := func(raw string) string {
		h, _ := password.Hash(raw)
		return h
	}

	createUser := func(email, rawPwd string, orgID, roleID uuid.UUID) uuid.UUID {
		var userID uuid.UUID
		err := pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&userID)
		if err != nil {
			userID = uuid.New()
			_, _ = pool.Exec(ctx, `
				INSERT INTO users (id, organization_id, email, password_hash, status, mfa_enabled, created_at, updated_at)
				VALUES ($1, $2, $3, $4, 'active', false, now(), now())
			`, userID, orgID, email, hashPwd(rawPwd))
			_, _ = pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, roleID)
		} else {
			_, _ = pool.Exec(ctx, `UPDATE users SET password_hash = $1, organization_id = $2 WHERE id = $3`, hashPwd(rawPwd), orgID, userID)
			_, _ = pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, roleID)
		}
		return userID
	}

	adminUserID := createUser("admin@znero.internal", "AdminSecret123!", platID, superAdminRoleID)
	createUser("operator@znero.internal", "NocSecret123!", platID, superAdminRoleID)
	fmt.Printf("[OK] Seeded Platform Admin: admin@znero.internal / AdminSecret123!\n")

	// 4. Seed Resellers & Customers
	createOrg := func(name, orgType string, parentID uuid.UUID) (uuid.UUID, uuid.UUID) {
		var oID uuid.UUID
		err := pool.QueryRow(ctx, `SELECT id FROM organizations WHERE name = $1`, name).Scan(&oID)
		if err != nil {
			oID = uuid.New()
			var parentPath string
			_ = pool.QueryRow(ctx, `SELECT path FROM organizations WHERE id = $1`, parentID).Scan(&parentPath)
			path := fmt.Sprintf("%s%s/", parentPath, oID.String())
			_, _ = pool.Exec(ctx, `
				INSERT INTO organizations (id, name, type, parent_id, path, status, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, 'active', now(), now())
			`, oID, name, orgType, parentID, path)
		}
		roleName := "Org Admin"
		if orgType == "reseller" {
			roleName = "Reseller Admin"
		} else if orgType == "customer" {
			roleName = "Customer Admin"
		}
		rID := ensureRole(oID, roleName)
		return oID, rID
	}

	reseller1ID, reseller1Role := createOrg("Apex Global Telecom", "reseller", platID)
	reseller2ID, reseller2Role := createOrg("TeleNexus Communications", "reseller", platID)

	createUser("admin@apextelecom.net", "ResellerSecret123!", reseller1ID, reseller1Role)
	createUser("admin@telenexus.io", "ResellerSecret123!", reseller2ID, reseller2Role)
	fmt.Printf("[OK] Seeded Resellers: Apex Global & TeleNexus\n")

	cust1ID, cust1Role := createOrg("Fintech Pay SMS", "customer", reseller1ID)
	cust2ID, _ := createOrg("HealthAlert Notifications", "customer", reseller1ID)
	cust3ID, cust3Role := createOrg("E-Commerce Express OTP", "customer", reseller2ID)
	cust4ID, _ := createOrg("Logistics Dispatcher SMS", "customer", reseller2ID)
	directCustID, directCustRole := createOrg("Direct Enterprise Telecom", "customer", platID)

	createUser("dev@fintechpay.com", "CustomerSecret123!", cust1ID, cust1Role)
	createUser("ops@ecommerceexpress.com", "CustomerSecret123!", cust3ID, cust3Role)
	createUser("enterprise@directsms.com", "CustomerSecret123!", directCustID, directCustRole)
	fmt.Printf("[OK] Seeded 5 Customer Organizations\n")

	// 5. Seed Providers & Connections
	type ProvConn struct {
		ProvID   uuid.UUID
		ConnID   uuid.UUID
		Name     string
		Code     string
		Protocol string
	}

	providers := []struct {
		Name     string
		Code     string
		Type     string
		ConnName string
	}{
		{"Vodafone Global Wholesale", "VODAFONE_UK", "smpp", "Vodafone Primary TX"},
		{"Tata Communications Gateway", "TATA_INT", "smpp", "Tata Tier-1 TX"},
		{"Twilio Carrier API", "TWILIO_API", "http", "Twilio REST Webhook Link"},
		{"Infobip Direct Route", "INFOBIP_EU", "http", "Infobip JSON Connector"},
	}

	var seededConns []ProvConn
	for _, p := range providers {
		var provID uuid.UUID
		err := pool.QueryRow(ctx, `SELECT id FROM providers WHERE code = $1`, p.Code).Scan(&provID)
		if err != nil {
			provID = uuid.New()
			_, _ = pool.Exec(ctx, `
				INSERT INTO providers (id, name, code, provider_type, status, created_at, updated_at)
				VALUES ($1, $2, $3, $4, 'active', now(), now())
			`, provID, p.Name, p.Code, p.Type)
		}

		var connID uuid.UUID
		err = pool.QueryRow(ctx, `SELECT id FROM provider_connections WHERE provider_id = $1 AND name = $2`, provID, p.ConnName).Scan(&connID)
		if err != nil {
			connID = uuid.New()
			_, _ = pool.Exec(ctx, `
				INSERT INTO provider_connections (id, provider_id, name, protocol, status, created_at, updated_at)
				VALUES ($1, $2, $3, $4, 'active', now(), now())
			`, connID, provID, p.ConnName, p.Type)
		}

		seededConns = append(seededConns, ProvConn{
			ProvID:   provID,
			ConnID:   connID,
			Name:     p.Name,
			Code:     p.Code,
			Protocol: p.Type,
		})
	}
	fmt.Printf("[OK] Seeded 4 Upstream Providers with Live Connections\n")

	// 6. Seed 3,500 Messages across past 30 days
	custIDs := []uuid.UUID{cust1ID, cust2ID, cust3ID, cust4ID, directCustID}
	currencies := []string{"USD", "EUR", "GBP", "BDT"}
	statuses := []string{"delivered", "delivered", "delivered", "delivered", "delivered", "delivered", "delivered", "failed", "accepted", "expired"}

	r := rand.New(rand.NewSource(42))
	now := time.Now().UTC()

	fmt.Println("Seeding 3,500 realistic demo messages with DLRs and multi-currency pricing...")
	for i := 0; i < 3500; i++ {
		msgID := uuid.New()
		// Random time over past 30 days
		minutesAgo := r.Intn(30 * 24 * 60)
		msgTime := now.Add(-time.Duration(minutesAgo) * time.Minute)

		cID := custIDs[r.Intn(len(custIDs))]
		cur := currencies[r.Intn(len(currencies))]
		status := statuses[r.Intn(len(statuses))]
		conn := seededConns[r.Intn(len(seededConns))]

		sellPrice := decimal.NewFromFloat(0.0450 + r.Float64()*0.03)
		buyCost := decimal.NewFromFloat(0.0150 + r.Float64()*0.012)
		dest := fmt.Sprintf("+1202555%04d", r.Intn(9999))

		_, _ = pool.Exec(ctx, `
			INSERT INTO messages (
				id, created_at, customer_org_id, original_destination, destination,
				sender_id, encrypted_body, key_id, status, sell_price, sell_currency,
				provider_connection_id, estimated_buy_cost, actual_buy_cost, buy_currency, expires_at
			) VALUES (
				$1, $2, $3, $4, $4,
				'DEMO', 'encrypted_payload'::bytea, 'k1', $5, $6, $7,
				$8, $9, $9, $7, $2 + interval '24 hours'
			)
			ON CONFLICT DO NOTHING
		`, msgID, msgTime, cID, dest, status, sellPrice, cur, conn.ConnID, buyCost)
	}
	fmt.Printf("[OK] 3,500 messages seeded\n")

	// 7. Seed Pre-computed Rollups (15m, 1h, 1d)
	fmt.Println("Seeding pre-computed analytics rollups...")
	// 7a. 1h rollups for past 30 days
	for h := 0; h < 24*30; h++ {
		bStart := now.Add(-time.Duration(h*60) * time.Minute).Truncate(time.Hour)
		bEnd := bStart.Add(time.Hour)
		subCount := int64(150 + r.Intn(200))
		delCount := int64(float64(subCount) * (0.85 + r.Float64()*0.10))
		failCount := subCount - delCount
		revUSD := decimal.NewFromFloat(float64(subCount) * 0.05)
		costUSD := decimal.NewFromFloat(float64(subCount) * 0.02)
		marginUSD := revUSD.Sub(costUSD)

		histJSON := `{"10ms":20,"25ms":45,"50ms":110,"100ms":30,"250ms":8,"500ms":2}`

		_, _ = pool.Exec(ctx, `
			INSERT INTO analytics_rollup_1h (
				bucket_start, bucket_end, submitted_count, delivered_count, failed_count,
				expired_count, rejected_count, queued_count, revenue_captured, buy_cost_incurred,
				gross_margin, currency, avg_delivery_latency_ms, p95_delivery_latency_ms,
				latency_histogram, created_at, updated_at
			) VALUES (
				$1, $2, $3, $4, $5,
				0, 0, 0, $6, $7,
				$8, 'USD', 48.5, 92.0,
				$9::jsonb, now(), now()
			)
			ON CONFLICT DO NOTHING
		`, bStart, bEnd, subCount, delCount, failCount, revUSD, costUSD, marginUSD, histJSON)
	}

	// 7b. 15m rollups for the past 24 hours
	for m := 0; m < 96; m++ {
		bStart := now.Add(-time.Duration(m*15) * time.Minute).Truncate(15 * time.Minute)
		bEnd := bStart.Add(15 * time.Minute)
		subCount := int64(40 + r.Intn(50))
		delCount := int64(float64(subCount) * 0.90)
		failCount := subCount - delCount
		revUSD := decimal.NewFromFloat(float64(subCount) * 0.05)
		costUSD := decimal.NewFromFloat(float64(subCount) * 0.02)
		marginUSD := revUSD.Sub(costUSD)

		_, _ = pool.Exec(ctx, `
			INSERT INTO analytics_rollup_15m (
				bucket_start, bucket_end, submitted_count, delivered_count, failed_count,
				expired_count, rejected_count, queued_count, revenue_captured, buy_cost_incurred,
				gross_margin, currency, avg_delivery_latency_ms, p95_delivery_latency_ms,
				latency_histogram, created_at, updated_at
			) VALUES (
				$1, $2, $3, $4, $5,
				0, 0, 0, $6, $7,
				$8, 'USD', 42.1, 88.0,
				'{}'::jsonb, now(), now()
			)
			ON CONFLICT DO NOTHING
		`, bStart, bEnd, subCount, delCount, failCount, revUSD, costUSD, marginUSD)

		// Also seed customer rollup
		for _, cID := range custIDs {
			cSub := int64(10 + r.Intn(15))
			cDel := int64(float64(cSub) * 0.90)
			cRev := decimal.NewFromFloat(float64(cSub) * 0.05)
			_, _ = pool.Exec(ctx, `
				INSERT INTO analytics_rollup_customer_15m (
					bucket_start, bucket_end, organization_id, customer_id,
					submitted_count, delivered_count, failed_count, revenue_captured, currency,
					created_at, updated_at
				) VALUES (
					$1, $2, $3, $3,
					$4, $5, $6, $7, 'USD',
					now(), now()
				)
				ON CONFLICT DO NOTHING
			`, bStart, bEnd, cID, cSub, cDel, cSub-cDel, cRev)
		}
	}
	fmt.Printf("[OK] Seeded analytics rollups (15m, 1h, customer breakdown)\n")

	// 8. Seed Alert Rules & Firing Alerts
	alertRules := []struct {
		Name      string
		Desc      string
		Metric    string
		Condition string
		Threshold float64
		Duration  int
		Severity  string
		Status    string
	}{
		{"Provider Connection Latency Spike", "Triggers when carrier p95 round-trip latency exceeds SLA", "provider_latency_p95", ">", 150.0, 60, "warning", "ok"},
		{"Tata Tier-1 DLR Failure Rate Exceeded", "Triggers when downstream DLR error rate exceeds 15%", "dlr_failure_rate", ">", 0.15, 120, "critical", "firing"},
		{"Platform Outbound Queue Surge", "Triggers when undispatched messages in queue exceed 5000", "queue_depth", ">", 5000.0, 30, "error", "ok"},
		{"Gross Margin Compression", "Triggers when customer traffic yields net margin under 8%", "gross_margin", "<", 0.08, 300, "warning", "ok"},
	}

	for _, ar := range alertRules {
		ruleID := uuid.New()
		_, _ = pool.Exec(ctx, `
			INSERT INTO alert_rules (
				id, name, description, metric_name, condition, threshold,
				duration_seconds, severity, is_enabled, status, last_evaluated_at,
				created_at, updated_at
			) VALUES (
				$1, $2, $3, $4, $5, $6,
				$7, $8, true, $9, now(),
				now(), now()
			)
			ON CONFLICT DO NOTHING
		`, ruleID, ar.Name, ar.Desc, ar.Metric, ar.Condition, ar.Threshold, ar.Duration, ar.Severity, ar.Status)

		if ar.Status == "firing" {
			_, _ = pool.Exec(ctx, `
				INSERT INTO alert_events (
					id, rule_id, status, current_value, threshold, message, started_at
				) VALUES (
					gen_random_uuid(), $1, 'firing', 0.184, $2,
					'Tata Tier-1 DLR failure rate at 18.4% exceeding 15% threshold across UK routes',
					now() - interval '25 minutes'
				)
			`, ruleID, ar.Threshold)
		}
	}
	fmt.Printf("[OK] Seeded 4 Alert Rules & 1 Active Firing Alert\n")

	// 9. Seed Incidents
	inc1ID := uuid.New()
	_, _ = pool.Exec(ctx, `
		INSERT INTO incidents (
			id, title, severity, status, summary, declared_at, lead_user_id, created_at, updated_at
		) VALUES (
			$1, 'Upstream Carrier Route Flapping on Tata Tier-1', 'high', 'investigating',
			'Subscribers experiencing intermittent DLR delivery delays on UK and Germany mobile networks.',
			now() - interval '45 minutes', $2, now() - interval '45 minutes', now()
		)
		ON CONFLICT DO NOTHING
	`, inc1ID, adminUserID)

	_, _ = pool.Exec(ctx, `
		INSERT INTO incident_timeline (incident_id, event_type, notes, actor_user_id, created_at)
		VALUES
			($1, 'status_change', 'Incident declared as HIGH severity. NOC investigating route telemetry.', $2, now() - interval '45 minutes'),
			($1, 'investigation', 'Tata NOC confirmed fiber cut in Frankfurt POP. Traffic rerouted to Vodafone UK.', $2, now() - interval '20 minutes')
	`, inc1ID, adminUserID)

	inc2ID := uuid.New()
	resolvedAt := now.Add(-2 * time.Hour)
	_, _ = pool.Exec(ctx, `
		INSERT INTO incidents (
			id, title, severity, status, summary, resolution_notes, declared_at, resolved_at, lead_user_id, created_at, updated_at
		) VALUES (
			$1, 'Customer Webhook Delivery Transient Backpressure', 'medium', 'resolved',
			'Outbound webhook dispatch latency elevated due to third-party customer endpoint rate limiting.',
			'Customer scaled webhook receiver capacity. Backlog drained to 0 in 15 minutes.',
			now() - interval '5 hours', $3, $2, now() - interval '5 hours', now() - interval '2 hours'
		)
		ON CONFLICT DO NOTHING
	`, inc2ID, adminUserID, resolvedAt)
	fmt.Printf("[OK] Seeded 2 Platform Incidents with Full Timeline\n")

	// 10. Seed Completed Export Jobs with Real Files
	storageDir := filepath.Join("tmp", "exports")
	_ = os.MkdirAll(storageDir, 0755)

	gen := export.NewGenerator()

	// 10a. CSV Export
	job1ID := uuid.New()
	csvHeaders := []string{"Message ID", "Created At", "Destination", "Sender ID", "Status", "Sell Price", "Currency"}
	csvRows := [][]string{
		{uuid.New().String(), now.Format(time.RFC3339), "+12025550111", "VERIFY", "delivered", "0.0500", "USD"},
		{uuid.New().String(), now.Format(time.RFC3339), "+447700900122", "ALERT", "delivered", "0.0450", "GBP"},
		{uuid.New().String(), now.Format(time.RFC3339), "+8801711000133", "FINTECH", "delivered", "2.50", "BDT"},
	}
	csvData, _ := gen.GenerateCSV(csvHeaders, csvRows)
	csvPath := filepath.Join(storageDir, fmt.Sprintf("%s.csv", job1ID.String()))
	_ = os.WriteFile(csvPath, csvData, 0644)

	expAt := now.Add(24 * time.Hour)
	_, _ = pool.Exec(ctx, `
		INSERT INTO export_jobs (
			id, organization_id, user_id, export_type, format, status,
			filter_params, row_count, file_path, expires_at, created_at, completed_at
		) VALUES (
			$1, $2, $3, 'messages', 'csv', 'completed',
			'{"status":"delivered"}'::jsonb, 3, $4, $5, now() - interval '1 hour', now() - interval '59 minutes'
		)
	`, job1ID, platID, adminUserID, csvPath, expAt)

	// 10b. XLSX Export
	job2ID := uuid.New()
	xlsxData, _ := gen.GenerateXLSX("DeliveryReport", csvHeaders, csvRows)
	xlsxPath := filepath.Join(storageDir, fmt.Sprintf("%s.xlsx", job2ID.String()))
	_ = os.WriteFile(xlsxPath, xlsxData, 0644)

	_, _ = pool.Exec(ctx, `
		INSERT INTO export_jobs (
			id, organization_id, user_id, export_type, format, status,
			filter_params, row_count, file_path, expires_at, created_at, completed_at
		) VALUES (
			$1, $2, $3, 'delivery_report', 'xlsx', 'completed',
			'{}'::jsonb, 3, $4, $5, now() - interval '30 minutes', now() - interval '29 minutes'
		)
	`, job2ID, platID, adminUserID, xlsxPath, expAt)

	// 10c. JSON Export
	job3ID := uuid.New()
	jsonData, _ := gen.GenerateJSON([]map[string]interface{}{
		{"currency": "USD", "messages": 2850, "revenue": "142.50", "cost": "57.00", "margin": "85.50"},
		{"currency": "EUR", "messages": 450, "revenue": "24.75", "cost": "9.90", "margin": "14.85"},
	})
	jsonPath := filepath.Join(storageDir, fmt.Sprintf("%s.json", job3ID.String()))
	_ = os.WriteFile(jsonPath, jsonData, 0644)

	_, _ = pool.Exec(ctx, `
		INSERT INTO export_jobs (
			id, organization_id, user_id, export_type, format, status,
			filter_params, row_count, file_path, expires_at, created_at, completed_at
		) VALUES (
			$1, $2, $3, 'financial_report', 'json', 'completed',
			'{}'::jsonb, 2, $4, $5, now() - interval '10 minutes', now() - interval '9 minutes'
		)
	`, job3ID, platID, adminUserID, jsonPath, expAt)
	fmt.Printf("[OK] Seeded 3 Completed Export Jobs (CSV, XLSX, JSON) with physical download files\n")

	// 11. Seed User Preferences & Saved Filters
	_, _ = pool.Exec(ctx, `
		INSERT INTO user_preferences (user_id, theme, locale, dashboard_layout, updated_at)
		VALUES ($1, 'system', 'en', '{"columns": 4}'::jsonb, now())
		ON CONFLICT (user_id) DO NOTHING
	`, adminUserID)

	filterParams, _ := json.Marshal(map[string]interface{}{"status": "delivered", "currency": "USD"})
	_, _ = pool.Exec(ctx, `
		INSERT INTO saved_filters (id, organization_id, user_id, view, name, filter_params, is_default, created_at)
		VALUES (gen_random_uuid(), $1, $2, 'messages', 'US Dollar Delivered SMS', $3, true, now())
		ON CONFLICT DO NOTHING
	`, platID, adminUserID, filterParams)
	fmt.Printf("[OK] Seeded User Preferences and Saved Filters\n")

	fmt.Println("==================================================")
	fmt.Println("  DEMO STACK SEEDING COMPLETE FOR znero_demo!")
	fmt.Println("==================================================")
}
