// Command seedmessagingsample loads or cleans up development sample data for messaging.
// Configures a DEV SAMPLE connection pointing at the mock simulator on loopback,
// a route plan, rates, and customer funds through the service layer.
// Refuses to run outside APP_ENV=development. Cleanup never deletes ledger rows.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/config"
	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/internal/rating"
	"github.com/znero-international/znero/backend/internal/routing"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/pkg/database"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

const (
	SampleProviderName   = "DEV SAMPLE MOCK PROVIDER"
	SampleConnectionName = "DEV SAMPLE MOCK SMPP"
	SamplePlanName       = "DEV SAMPLE DEFAULT PLAN"
	SampleSellCardName   = "DEV SAMPLE STANDARD SELL"
	SampleBuyCardName    = "DEV SAMPLE MOCK BUY"
)

func main() {
	cleanup := flag.Bool("cleanup", false, "Clean up sample messaging configs (never deletes ledger rows)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	if cfg.Env != config.EnvDevelopment {
		fmt.Fprintf(os.Stderr, "FATAL: Messaging sample seed is strictly prohibited outside APP_ENV=development. Current environment: %q\n", cfg.Env)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := database.Connect(ctx, database.Config{
		URL:            cfg.Database.URL,
		MaxConns:       5,
		ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Database connection failed: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	if *cleanup {
		fmt.Println("=== Cleaning up DEV SAMPLE messaging configurations ===")
		if err := runCleanup(ctx, pool); err != nil {
			fmt.Fprintf(os.Stderr, "Cleanup failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Cleanup completed successfully (ledger rows strictly preserved).")
		return
	}

	fmt.Println("=== Seeding DEV SAMPLE messaging environment ===")
	if err := runSeed(ctx, pool, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Seeding failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Seeding completed successfully.")
}

func runCleanup(ctx context.Context, pool *pgxpool.Pool) error {
	// Disable and archive sample connection
	_, _ = pool.Exec(ctx, `
		UPDATE provider_connections
		SET status = 'disabled', archived = true
		WHERE name = $1
	`, SampleConnectionName)

	// Clean outbox items for dev sample
	_, _ = pool.Exec(ctx, `
		DELETE FROM message_outbox
		WHERE payload->>'destination' LIKE '+1555%'
	`)

	// Note: Triggers strictly block DELETE on messages, events, dlrs, and ledger rows.
	return nil
}

func runSeed(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config) error {
	// 1. Fetch platform organization and customer organization
	var platformOrgID, customerOrgID, superAdminID uuid.UUID
	err := pool.QueryRow(ctx, "SELECT id FROM organizations WHERE type = 'platform' LIMIT 1").Scan(&platformOrgID)
	if err != nil {
		return fmt.Errorf("platform organization not found: %w", err)
	}

	err = pool.QueryRow(ctx, "SELECT id FROM organizations WHERE type = 'customer' AND status = 'active' LIMIT 1").Scan(&customerOrgID)
	if err != nil {
		return fmt.Errorf("active customer organization not found: %w", err)
	}

	err = pool.QueryRow(ctx, "SELECT id FROM users WHERE organization_id = $1 LIMIT 1", platformOrgID).Scan(&superAdminID)
	if err != nil {
		return fmt.Errorf("super admin user not found: %w", err)
	}

	platformTC := &tenant.Context{
		ActorUserID:     superAdminID,
		ActorOrgID:      platformOrgID,
		ActorOrgType:    "platform",
		IsPlatformAdmin: true,
	}

	keyRing, err := secrets.NewKeyRingFromEnv(
		cfg.Env,
		os.Getenv("SECRETS_ENCRYPTION_KEY"),
		os.Getenv("SECRETS_KEY_ID"),
		os.Getenv("SECRETS_PREVIOUS_KEYS"),
	)
	if err != nil {
		return fmt.Errorf("keyring: %w", err)
	}

	auditRepo := audit.NewRepository(pool)
	auditSvc := audit.NewService(auditRepo)
	approvalRepo := approval.NewRepository(pool)
	approvalSvc := approval.NewService(approvalRepo, auditSvc, pool)
	orgRepo := org.NewRepository(pool)
	ledgerRepo := ledger.NewRepository(pool)
	ledgerSvc := ledger.NewService(ledgerRepo, orgRepo, approvalSvc, auditSvc, pool)
	ratingRepo := rating.NewRepository(pool)

	// 2. Ensure Provider exists
	var providerID uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO providers (
			id, organization_id, name, legal_name, protocol, country_code,
			currency_code, status, created_by
		) VALUES (
			gen_random_uuid(), $1, $2, $2, 'smpp', 'US', 'USD', 'active', $3
		)
		ON CONFLICT (name) DO UPDATE SET status = 'active'
		RETURNING id
	`, platformOrgID, SampleProviderName, superAdminID).Scan(&providerID)
	if err != nil {
		_ = pool.QueryRow(ctx, "SELECT id FROM providers WHERE name = $1", SampleProviderName).Scan(&providerID)
	}

	// Encrypt sample SMPP password
	pwEnvelope, err := keyRing.Encrypt("mock_pass")
	if err != nil {
		return fmt.Errorf("encrypt mock smpp password: %w", err)
	}

	// 3. Ensure Provider Connection pointing at Mock SMSC on loopback
	var connID uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO provider_connections (
			id, provider_id, name, protocol, status, max_tps,
			smpp_host, smpp_port, smpp_system_id, smpp_password_encrypted,
			smpp_system_type, smpp_bind_type, smpp_tls, smpp_window_size,
			smpp_enquire_link_sec, smpp_source_ton, smpp_source_npi,
			smpp_dest_ton, smpp_dest_npi, smpp_charset, created_by
		) VALUES (
			gen_random_uuid(), $1, $2, 'smpp', 'active', 50,
			'127.0.0.1', 2775, 'mock_smsc', $3,
			'SMPP', 'transceiver', false, 10,
			30, 1, 1, 1, 1, 'GSM', $4
		)
		ON CONFLICT (provider_id, name) DO UPDATE SET
			status = 'active',
			archived = false,
			smpp_host = '127.0.0.1',
			smpp_port = 2775,
			smpp_system_id = 'mock_smsc',
			smpp_password_encrypted = EXCLUDED.smpp_password_encrypted
		RETURNING id
	`, providerID, SampleConnectionName, pwEnvelope, superAdminID).Scan(&connID)
	if err != nil {
		_ = pool.QueryRow(ctx, "SELECT id FROM provider_connections WHERE name = $1", SampleConnectionName).Scan(&connID)
	}

	// 4. Ensure Route Plan exists
	var planID uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO route_plans (
			id, name, description, strategy, status, created_by
		) VALUES (
			gen_random_uuid(), $1, 'Sample Route Plan pointing to Mock SMSC', 'priority', 'active', $2
		)
		ON CONFLICT (name) DO UPDATE SET status = 'active'
		RETURNING id
	`, SamplePlanName, superAdminID).Scan(&planID)
	if err != nil {
		_ = pool.QueryRow(ctx, "SELECT id FROM route_plans WHERE name = $1", SamplePlanName).Scan(&planID)
	}

	// Ensure Route Plan Entry pointing to sample connection
	_, _ = pool.Exec(ctx, `
		INSERT INTO route_plan_entries (
			id, route_plan_id, provider_connection_id, priority, weight, status, created_by
		) VALUES (
			gen_random_uuid(), $1, $2, 1, 100, 'active', $3
		)
		ON CONFLICT (route_plan_id, provider_connection_id) DO UPDATE SET
			status = 'active',
			priority = 1
	`, planID, connID, superAdminID)

	// Ensure customer assigned to this route plan
	_, _ = pool.Exec(ctx, `
		INSERT INTO customer_route_assignments (
			id, customer_org_id, route_plan_id, created_by
		) VALUES (
			gen_random_uuid(), $1, $2, $3
		)
		ON CONFLICT (customer_org_id, COALESCE(country_id, '00000000-0000-0000-0000-000000000000'::uuid)) DO UPDATE SET
			route_plan_id = EXCLUDED.route_plan_id
	`, customerOrgID, planID, superAdminID)

	// 5. Ensure Customer Wallet has funds for sending
	wallet, err := ledgerSvc.EnsureWallet(ctx, platformTC, customerOrgID, "USD", "prepaid")
	if err == nil && wallet != nil {
		if wallet.Status == "closed" {
			_, _ = pool.Exec(ctx, "UPDATE wallets SET status = 'active' WHERE id = $1", wallet.ID)
			wallet.Status = "active"
		}

		if wallet.CachedBalance.LessThan(decimal.NewFromFloat(50.0)) {
			topupAmt := decimal.NewFromFloat(100.0)
			idemp := fmt.Sprintf("dev_sample_seed_%d", time.Now().Unix())
			_, _, topupErr := ledgerSvc.Topup(ctx, platformTC, wallet.ID, ledger.WalletTopupRequest{
				Amount:    topupAmt,
				Reason:    "DEV SAMPLE initial test funds",
				Reference: idemp,
			}, "127.0.0.1", "seed-messaging-sample")
			if topupErr == nil {
				fmt.Printf("✓ Seeded customer wallet %s with $100.00 USD\n", wallet.ID)
			}
		}
	}

	// 6. Ensure Active Rates exist
	// Sell rate card: standard sell card with default rate
	var sellCardID uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO rate_cards (
			id, name, type, currency, status, created_by
		) VALUES (
			gen_random_uuid(), $1, 'customer_sell', 'USD', 'active', $2
		)
		ON CONFLICT (name) DO UPDATE SET status = 'active'
		RETURNING id
	`, SampleSellCardName, superAdminID).Scan(&sellCardID)
	if err != nil {
		_ = pool.QueryRow(ctx, "SELECT id FROM rate_cards WHERE name = $1", SampleSellCardName).Scan(&sellCardID)
	}

	// Buy rate card
	var buyCardID uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO rate_cards (
			id, name, type, currency, status, created_by
		) VALUES (
			gen_random_uuid(), $1, 'provider_buy', 'USD', 'active', $2
		)
		ON CONFLICT (name) DO UPDATE SET status = 'active'
		RETURNING id
	`, SampleBuyCardName, superAdminID).Scan(&buyCardID)
	if err != nil {
		_ = pool.QueryRow(ctx, "SELECT id FROM rate_cards WHERE name = $1", SampleBuyCardName).Scan(&buyCardID)
	}

	// Fetch a sample country ID (e.g. US or first active country)
	var usCountryID uuid.UUID
	err = pool.QueryRow(ctx, "SELECT id FROM countries WHERE iso2 = 'US' OR iso3 = 'USA' LIMIT 1").Scan(&usCountryID)
	if err == nil {
		// Insert active sell rate ($0.015000)
		_, _ = pool.Exec(ctx, `
			INSERT INTO rates (
				id, rate_card_id, country_id, rate, effective_from, status, created_by
			) VALUES (
				gen_random_uuid(), $1, $2, 0.015000, now() - interval '1 day', 'active', $3
			)
			ON CONFLICT (rate_card_id, COALESCE(country_id, '00000000-0000-0000-0000-000000000000'::uuid), COALESCE(operator_id, '00000000-0000-0000-0000-000000000000'::uuid), msg_type, effective_from)
			DO NOTHING
		`, sellCardID, usCountryID, superAdminID)

		// Insert active buy rate ($0.008000)
		_, _ = pool.Exec(ctx, `
			INSERT INTO rates (
				id, rate_card_id, country_id, rate, effective_from, status, created_by
			) VALUES (
				gen_random_uuid(), $1, $2, 0.008000, now() - interval '1 day', 'active', $3
			)
			ON CONFLICT (rate_card_id, COALESCE(country_id, '00000000-0000-0000-0000-000000000000'::uuid), COALESCE(operator_id, '00000000-0000-0000-0000-000000000000'::uuid), msg_type, effective_from)
			DO NOTHING
		`, buyCardID, usCountryID, superAdminID)

		// Ensure provider coverage for this country
		_, _ = pool.Exec(ctx, `
			INSERT INTO provider_coverage (
				id, provider_id, country_id, status, created_by
			) VALUES (
				gen_random_uuid(), $1, $2, 'active', $3
			)
			ON CONFLICT (provider_id, country_id, COALESCE(operator_id, '00000000-0000-0000-0000-000000000000'::uuid))
			DO UPDATE SET status = 'active'
		`, providerID, usCountryID, superAdminID)
	}

	// Assign sell card to customer
	_, _ = pool.Exec(ctx, `
		INSERT INTO customer_rate_assignments (
			id, customer_org_id, rate_card_id, created_by
		) VALUES (
			gen_random_uuid(), $1, $2, $3
		)
		ON CONFLICT (customer_org_id) DO UPDATE SET
			rate_card_id = EXCLUDED.rate_card_id
	`, customerOrgID, sellCardID, superAdminID)

	// Ensure provider assigned buy card
	_, _ = pool.Exec(ctx, `
		UPDATE providers
		SET default_buy_rate_card_id = $1
		WHERE id = $2
	`, buyCardID, providerID)

	// Reload rating and routing snapshot in memory
	ratingEngine := rating.NewInMemoryEngine(ratingRepo)
	_ = ratingEngine.Reload(ctx)
	routing.TryReload(ctx, pool)

	fmt.Println("✓ Provider Connection :", SampleConnectionName, "(127.0.0.1:2775)")
	fmt.Println("✓ Route Plan         :", SamplePlanName)
	fmt.Println("✓ Customer Org ID    :", customerOrgID.String())
	fmt.Println("✓ Sell Rate Card     :", SampleSellCardName, "(Assigned to Customer)")
	fmt.Println("✓ Buy Rate Card      :", SampleBuyCardName, "(Assigned to Provider)")
	fmt.Println("✓ System Status      : READY FOR TEST SEND")

	return nil
}
