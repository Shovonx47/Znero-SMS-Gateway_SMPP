// Command seedratessample loads or cleans up clearly labeled development sample
// rate cards, rates, customer assignments, reseller cost assignments, and upcoming rate changes
// through the service layer. Strictly forbidden from running outside APP_ENV=development.
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
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/rating"
	"github.com/znero-international/znero/backend/pkg/database"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

func main() {
	cleanup := flag.Bool("cleanup", false, "Remove all DEV SAMPLE rate cards, rates, and assignments")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	if cfg.Env != config.EnvDevelopment {
		fmt.Fprintf(os.Stderr, "FATAL: Rates sample seed is strictly prohibited outside APP_ENV=development. Current environment: %q\n", cfg.Env)
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
		fmt.Println("=== Cleaning up DEV SAMPLE rate cards, rates, and assignments ===")
		if err := runCleanup(ctx, pool); err != nil {
			fmt.Fprintf(os.Stderr, "Cleanup failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Cleanup completed successfully.")
		return
	}

	fmt.Println("=== Seeding DEV SAMPLE rate cards, rates, and assignments via service layer ===")
	if err := runSeed(ctx, pool); err != nil {
		fmt.Fprintf(os.Stderr, "Seed failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("DEV SAMPLE Phase 5 seed completed successfully.")
}

func runCleanup(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	stmts := []string{
		`DELETE FROM customer_rate_assignments_history WHERE card_id IN (SELECT id FROM rate_cards WHERE name LIKE 'DEV SAMPLE%')`,
		`DELETE FROM customer_rate_assignments WHERE card_id IN (SELECT id FROM rate_cards WHERE name LIKE 'DEV SAMPLE%')`,
		`DELETE FROM reseller_cost_assignments_history WHERE card_id IN (SELECT id FROM rate_cards WHERE name LIKE 'DEV SAMPLE%')`,
		`DELETE FROM reseller_cost_assignments WHERE card_id IN (SELECT id FROM rate_cards WHERE name LIKE 'DEV SAMPLE%')`,
		`DELETE FROM rates_history WHERE card_id IN (SELECT id FROM rate_cards WHERE name LIKE 'DEV SAMPLE%')`,
		`DELETE FROM rates WHERE card_id IN (SELECT id FROM rate_cards WHERE name LIKE 'DEV SAMPLE%')`,
		`DELETE FROM rate_cards_history WHERE card_id IN (SELECT id FROM rate_cards WHERE name LIKE 'DEV SAMPLE%')`,
		`DELETE FROM rate_cards WHERE name LIKE 'DEV SAMPLE%'`,
	}

	for _, s := range stmts {
		tag, err := tx.Exec(ctx, s)
		if err != nil {
			return fmt.Errorf("cleanup statement failed: %w", err)
		}
		fmt.Printf("Affected %d rows running: %s\n", tag.RowsAffected(), s[:30])
	}

	return tx.Commit(ctx)
}

func runSeed(ctx context.Context, pool *pgxpool.Pool) error {
	// First clean any existing DEV SAMPLE rates
	_ = runCleanup(ctx, pool)

	var platformOrgID uuid.UUID
	var platformOrgPath string
	if err := pool.QueryRow(ctx, `SELECT id, path FROM organizations WHERE type = 'platform' LIMIT 1`).Scan(&platformOrgID, &platformOrgPath); err != nil {
		return fmt.Errorf("platform organization not found (run bootstrap-admin first): %w", err)
	}

	var actorUserID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE organization_id = $1 AND status = 'active' ORDER BY created_at ASC LIMIT 1`, platformOrgID).Scan(&actorUserID); err != nil {
		return fmt.Errorf("active platform user not found: %w", err)
	}

	platformTC := &tenant.Context{
		ActorUserID:     actorUserID,
		ActorOrgID:      platformOrgID,
		ActorOrgType:    "platform",
		ActorOrgPath:    platformOrgPath,
		IsPlatformAdmin: true,
		Permissions: map[string]bool{
			"rate.buycard.create":      true,
			"rate.buycard.read":        true,
			"rate.buycard.update":      true,
			"rate.buycard.override":    true,
			"rate.buyrate.create":      true,
			"rate.buyrate.read":        true,
			"rate.buyrate.update":      true,
			"rate.buyrate.override":    true,
			"rate.sellcard.create":     true,
			"rate.sellcard.read":       true,
			"rate.sellcard.update":     true,
			"rate.sellcard.override":   true,
			"rate.sellrate.create":     true,
			"rate.sellrate.read":       true,
			"rate.sellrate.update":     true,
			"rate.sellrate.override":   true,
			"rate.assignment.create":   true,
			"rate.assignment.read":     true,
			"rate.assignment.update":   true,
			"rate.assignment.override": true,
			"rate.margin.view":         true,
			"rate.margin.override":     true,
		},
	}

	auditSvc := audit.NewService(audit.NewRepository(pool))
	approvalSvc := approval.NewService(approval.NewRepository(pool), auditSvc, pool)
	ratingRepo := rating.NewRepository(pool)
	inMemoryEngine := rating.NewInMemoryEngine(ratingRepo)
	ratingSvc := rating.NewService(ratingRepo, auditSvc, approvalSvc, nil, inMemoryEngine)

	// Find sample provider
	var sampleProviderID uuid.UUID
	err := pool.QueryRow(ctx, `SELECT id FROM providers WHERE status = 'active' ORDER BY created_at ASC LIMIT 1`).Scan(&sampleProviderID)
	if err != nil {
		// Try any provider
		if err2 := pool.QueryRow(ctx, `SELECT id FROM providers LIMIT 1`).Scan(&sampleProviderID); err2 != nil {
			return fmt.Errorf("no provider found (run seed-providers-sample first): %w", err2)
		}
	}

	// Find sample countries: US, GB, DE (or fallbacks)
	countryRepo := country.NewRepository(pool)
	sampleISOs := []string{"US", "GB", "DE"}
	countryMap := make(map[string]uuid.UUID)
	for _, iso := range sampleISOs {
		c, err := countryRepo.GetByISO2(ctx, iso)
		if err == nil && c != nil {
			countryMap[iso] = c.ID
		}
	}
	if len(countryMap) == 0 {
		// Fallback to any 3 countries
		rows, err := pool.Query(ctx, `SELECT id, iso2 FROM countries LIMIT 3`)
		if err != nil {
			return fmt.Errorf("no countries found: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			var iso string
			if err := rows.Scan(&id, &iso); err == nil {
				countryMap[iso] = id
			}
		}
	}

	now := time.Now().UTC()
	futureEffective := now.Add(7 * 24 * time.Hour) // 7 days upcoming

	// 1. Create DEV SAMPLE Buy Card
	buyCardRes, err := ratingSvc.CreateCard(ctx, platformTC, &rating.RateCard{
		Name:         "DEV SAMPLE Alpha Provider Buy Card",
		Kind:         "provider_buy",
		ProviderID:   &sampleProviderID,
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
		Description:  "Sample buy card for Alpha Provider",
	}, "seed buy card")
	if err != nil {
		return fmt.Errorf("create buy card: %w", err)
	}
	buyCardID := buyCardRes.Card.ID
	fmt.Printf("Created Buy Card: %s (%s)\n", buyCardRes.Card.Name, buyCardID)

	// Buy rates
	buyPrices := map[string]string{
		"US": "0.005000",
		"GB": "0.012000",
		"DE": "0.025000",
	}
	for iso, cID := range countryMap {
		priceStr, ok := buyPrices[iso]
		if !ok {
			priceStr = "0.010000"
		}
		_, err := ratingSvc.CreateRate(ctx, platformTC, &rating.Rate{
			CardID:        buyCardID,
			CountryID:     cID,
			MessageType:   "default",
			Price:         decimal.RequireFromString(priceStr),
			EffectiveFrom: now.Add(-24 * time.Hour),
		}, "seed buy rate")
		if err != nil {
			return fmt.Errorf("create buy rate for %s: %w", iso, err)
		}
	}

	// 2. Create DEV SAMPLE Standard Sell Card
	sellCardRes, err := ratingSvc.CreateCard(ctx, platformTC, &rating.RateCard{
		Name:         "DEV SAMPLE Standard Sell Card",
		Kind:         "customer_sell",
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
		Description:  "Platform standard SMS rates",
	}, "seed standard sell card")
	if err != nil {
		return fmt.Errorf("create standard sell card: %w", err)
	}
	sellCardID := sellCardRes.Card.ID
	fmt.Printf("Created Standard Sell Card: %s (%s)\n", sellCardRes.Card.Name, sellCardID)

	// Standard sell rates
	sellPrices := map[string]string{
		"US": "0.007500",
		"GB": "0.016000",
		"DE": "0.032000",
	}
	for iso, cID := range countryMap {
		priceStr, ok := sellPrices[iso]
		if !ok {
			priceStr = "0.015000"
		}
		_, err := ratingSvc.CreateRate(ctx, platformTC, &rating.Rate{
			CardID:        sellCardID,
			CountryID:     cID,
			MessageType:   "default",
			Price:         decimal.RequireFromString(priceStr),
			EffectiveFrom: now.Add(-24 * time.Hour),
		}, "seed sell rate")
		if err != nil {
			return fmt.Errorf("create sell rate for %s: %w", iso, err)
		}
	}

	// Upcoming rate change for US
	if usID, ok := countryMap["US"]; ok {
		_, err := ratingSvc.CreateRate(ctx, platformTC, &rating.Rate{
			CardID:        sellCardID,
			CountryID:     usID,
			MessageType:   "default",
			Price:         decimal.RequireFromString("0.008000"),
			EffectiveFrom: futureEffective,
		}, "seed upcoming rate change")
		if err != nil {
			return fmt.Errorf("create upcoming sell rate: %w", err)
		}
		fmt.Printf("Created upcoming rate change for US: 0.008000 USD effective %s\n", futureEffective.Format(time.RFC3339))
	}

	// 3. Create DEV SAMPLE Premium Sell Card
	premiumCardRes, err := ratingSvc.CreateCard(ctx, platformTC, &rating.RateCard{
		Name:         "DEV SAMPLE Premium Sell Card",
		Kind:         "customer_sell",
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
		Description:  "Platform premium high-priority SMS rates",
	}, "seed premium sell card")
	if err != nil {
		return fmt.Errorf("create premium sell card: %w", err)
	}
	premiumCardID := premiumCardRes.Card.ID
	fmt.Printf("Created Premium Sell Card: %s (%s)\n", premiumCardRes.Card.Name, premiumCardID)

	premPrices := map[string]string{
		"US": "0.009000",
		"GB": "0.019000",
		"DE": "0.038000",
	}
	for iso, cID := range countryMap {
		priceStr, ok := premPrices[iso]
		if !ok {
			priceStr = "0.020000"
		}
		_, err := ratingSvc.CreateRate(ctx, platformTC, &rating.Rate{
			CardID:        premiumCardID,
			CountryID:     cID,
			MessageType:   "default",
			Price:         decimal.RequireFromString(priceStr),
			EffectiveFrom: now.Add(-24 * time.Hour),
		}, "seed premium sell rate")
		if err != nil {
			return fmt.Errorf("create premium rate for %s: %w", iso, err)
		}
	}

	// 4. Create DEV SAMPLE Reseller Cost Card
	costCardRes, err := ratingSvc.CreateCard(ctx, platformTC, &rating.RateCard{
		Name:         "DEV SAMPLE Reseller Cost Card",
		Kind:         "customer_sell",
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
		Description:  "Assigned cost card for wholesale resellers",
	}, "seed reseller cost card")
	if err != nil {
		return fmt.Errorf("create reseller cost card: %w", err)
	}
	costCardID := costCardRes.Card.ID
	fmt.Printf("Created Reseller Cost Card: %s (%s)\n", costCardRes.Card.Name, costCardID)

	costPrices := map[string]string{
		"US": "0.006000",
		"GB": "0.014000",
		"DE": "0.028000",
	}
	for iso, cID := range countryMap {
		priceStr, ok := costPrices[iso]
		if !ok {
			priceStr = "0.012000"
		}
		_, err := ratingSvc.CreateRate(ctx, platformTC, &rating.Rate{
			CardID:        costCardID,
			CountryID:     cID,
			MessageType:   "default",
			Price:         decimal.RequireFromString(priceStr),
			EffectiveFrom: now.Add(-24 * time.Hour),
		}, "seed cost rate")
		if err != nil {
			return fmt.Errorf("create cost rate for %s: %w", iso, err)
		}
	}

	// 5. Assign to Customer Org
	var customerOrgID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT organization_id FROM users WHERE email = 'dev-customer-admin@dev.znero.test'`).Scan(&customerOrgID); err != nil {
		_ = pool.QueryRow(ctx, `SELECT id FROM organizations WHERE type = 'customer' LIMIT 1`).Scan(&customerOrgID)
	}
	if customerOrgID != uuid.Nil {
		_, err = ratingSvc.CreateAssignment(ctx, platformTC, customerOrgID, sellCardID, now.Add(-24*time.Hour), nil, "assign standard card to customer")
		if err != nil {
			fmt.Printf("Notice: could not assign to customer: %v\n", err)
		} else {
			fmt.Printf("Assigned Standard Sell Card to Customer Org %s\n", customerOrgID)
		}
	}

	// 6. Assign Cost Card to Reseller Org
	var resellerOrgID uuid.UUID
	var resellerOrgPath string
	if err := pool.QueryRow(ctx, `SELECT u.organization_id, o.path FROM users u JOIN organizations o ON o.id = u.organization_id WHERE u.email = 'dev-reseller-admin@dev.znero.test'`).Scan(&resellerOrgID, &resellerOrgPath); err != nil {
		_ = pool.QueryRow(ctx, `SELECT id, path FROM organizations WHERE type = 'reseller' LIMIT 1`).Scan(&resellerOrgID, &resellerOrgPath)
	}
	if resellerOrgID != uuid.Nil {
		_, err = ratingSvc.CreateResellerCostAssignment(ctx, platformTC, resellerOrgID, costCardID, now.Add(-24*time.Hour), nil, "assign cost card to reseller")
		if err != nil {
			fmt.Printf("Notice: could not assign cost card to reseller: %v\n", err)
		} else {
			fmt.Printf("Assigned Reseller Cost Card to Reseller Org %s\n", resellerOrgID)
		}

		// Reseller creates downstream sell card
		var resellerUserID uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE organization_id = $1 LIMIT 1`, resellerOrgID).Scan(&resellerUserID); err == nil {
			resellerTC := &tenant.Context{
				ActorUserID:     resellerUserID,
				ActorOrgID:      resellerOrgID,
				ActorOrgType:    "reseller",
				ActorOrgPath:    resellerOrgPath,
				IsPlatformAdmin: false,
				Permissions: map[string]bool{
					"rate.sellcard.create": true,
					"rate.sellcard.read":   true,
					"rate.sellcard.update": true,
					"rate.sellrate.create": true,
					"rate.sellrate.read":   true,
					"rate.sellrate.update": true,
				},
			}
			resellerSellCardRes, err := ratingSvc.CreateCard(ctx, resellerTC, &rating.RateCard{
				Name:         "DEV SAMPLE Reseller Downstream Card",
				Kind:         "customer_sell",
				CurrencyCode: "USD",
				Channel:      "sms",
				Status:       "active",
				Description:  "Rates offered by Reseller to downstream accounts",
			}, "seed reseller downstream card")
			if err == nil && resellerSellCardRes.Card != nil {
				rSellCardID := resellerSellCardRes.Card.ID
				fmt.Printf("Created Reseller Downstream Sell Card: %s (%s)\n", resellerSellCardRes.Card.Name, rSellCardID)
				for iso, cID := range countryMap {
					// Set price higher than reseller cost
					rPrice := "0.007000"
					if iso == "GB" {
						rPrice = "0.016000"
					} else if iso == "DE" {
						rPrice = "0.032000"
					}
					_, _ = ratingSvc.CreateRate(ctx, resellerTC, &rating.Rate{
						CardID:        rSellCardID,
						CountryID:     cID,
						MessageType:   "default",
						Price:         decimal.RequireFromString(rPrice),
						EffectiveFrom: now.Add(-24 * time.Hour),
					}, "seed reseller sell rate")
				}
			}
		}
	}

	// Reload In-Memory Engine
	if err := ratingSvc.ReloadEngine(ctx); err != nil {
		fmt.Printf("Notice: in-memory engine reload: %v\n", err)
	} else {
		fmt.Println("Reloaded In-Memory Rating Engine snapshot.")
	}

	return nil
}
