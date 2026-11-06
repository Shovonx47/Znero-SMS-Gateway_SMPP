// Command seedproviderssample loads or cleans up clearly labeled development sample
// providers, provider connections, provider coverage, customer commercial profiles,
// and sender IDs through the service layer so every record has version history and
// an audit entry. Strictly forbidden from running outside APP_ENV=development.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/config"
	"github.com/znero-international/znero/backend/internal/customer"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/masterdata/operator"
	"github.com/znero-international/znero/backend/internal/provider"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/internal/senderid"
	"github.com/znero-international/znero/backend/pkg/database"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

const devSampleTag = "DEV SAMPLE"

func runtimeSecret() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "dev-runtime-" + hex.EncodeToString(b)
}

func main() {
	cleanup := flag.Bool("cleanup", false, "Remove all DEV SAMPLE providers, connections, coverage, customer profiles, and sender IDs")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	if cfg.Env != config.EnvDevelopment {
		fmt.Fprintf(os.Stderr, "FATAL: Provider sample seed is strictly prohibited outside APP_ENV=development. Current environment: %q\n", cfg.Env)
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
		fmt.Println("=== Cleaning up DEV SAMPLE providers, coverage, customer profiles, and sender IDs ===")
		if err := runCleanup(ctx, pool); err != nil {
			fmt.Fprintf(os.Stderr, "Cleanup failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Cleanup completed successfully.")
		return
	}

	fmt.Println("=== Seeding DEV SAMPLE providers, coverage, and customer commercial profiles via service layer ===")
	if err := runSeed(ctx, pool, cfg.Env); err != nil {
		fmt.Fprintf(os.Stderr, "Seed failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("DEV SAMPLE Phase 4 seed completed successfully.")
}

func runCleanup(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tagCov, err := tx.Exec(ctx, `
		DELETE FROM provider_coverage
		WHERE data_source LIKE 'DEV SAMPLE%'
		   OR provider_id IN (SELECT id FROM providers WHERE code IN ('DEV_ALPHA', 'DEV_BETA') OR name LIKE 'DEV SAMPLE%')
	`)
	if err != nil {
		return fmt.Errorf("clean provider_coverage: %w", err)
	}
	fmt.Printf("Removed %d DEV SAMPLE provider coverage rows.\n", tagCov.RowsAffected())

	tagProv, err := tx.Exec(ctx, `
		DELETE FROM providers
		WHERE code IN ('DEV_ALPHA', 'DEV_BETA') OR name LIKE 'DEV SAMPLE%'
	`)
	if err != nil {
		return fmt.Errorf("clean providers: %w", err)
	}
	fmt.Printf("Removed %d DEV SAMPLE providers (and their connections).\n", tagProv.RowsAffected())

	tagSid, err := tx.Exec(ctx, `
		DELETE FROM sender_ids
		WHERE data_source LIKE 'DEV SAMPLE%'
	`)
	if err != nil {
		return fmt.Errorf("clean sender_ids: %w", err)
	}
	fmt.Printf("Removed %d DEV SAMPLE sender IDs.\n", tagSid.RowsAffected())

	tagProf, err := tx.Exec(ctx, `
		DELETE FROM customer_profiles
		WHERE notes LIKE '%DEV SAMPLE%'
	`)
	if err != nil {
		return fmt.Errorf("clean customer_profiles: %w", err)
	}
	fmt.Printf("Removed %d DEV SAMPLE customer profiles.\n", tagProf.RowsAffected())

	return tx.Commit(ctx)
}

func runSeed(ctx context.Context, pool *pgxpool.Pool, appEnv string) error {
	var platformOrgID uuid.UUID
	var platformOrgPath string
	if err := pool.QueryRow(ctx, `SELECT id, path FROM organizations WHERE type = 'platform'`).Scan(&platformOrgID, &platformOrgPath); err != nil {
		return fmt.Errorf("platform organization not found (run bootstrap-admin first): %w", err)
	}
	var actorUserID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE organization_id = $1 AND status = 'active' ORDER BY created_at ASC LIMIT 1`, platformOrgID).Scan(&actorUserID); err != nil {
		return fmt.Errorf("active platform user not found (run bootstrap-admin first): %w", err)
	}

	tc := &tenant.Context{
		ActorUserID:     actorUserID,
		ActorOrgID:      platformOrgID,
		ActorOrgType:    "platform",
		ActorOrgPath:    platformOrgPath,
		IsPlatformAdmin: true,
		Permissions: map[string]bool{
			"provider.provider.view":            true,
			"provider.provider.create":          true,
			"provider.provider.edit":            true,
			"provider.provider.override":        true,
			"provider.connection.view":          true,
			"provider.connection.create":        true,
			"provider.connection.edit":          true,
			"provider.connection.secret.update": true,
			"provider.connection.status":        true,
			"provider.connection.override":      true,
			"provider.coverage.view":            true,
			"provider.coverage.create":          true,
			"provider.coverage.edit":            true,
			"provider.coverage.override":        true,
			"customer.profile.view":             true,
			"customer.profile.create":           true,
			"customer.profile.edit":             true,
			"customer.profile.override":         true,
			"customer.senderid.view":            true,
			"customer.senderid.create":          true,
			"customer.senderid.edit":            true,
			"customer.senderid.override":        true,
		},
	}

	keyRing, err := secrets.NewKeyRingFromEnv(
		appEnv,
		os.Getenv("SECRETS_ENCRYPTION_KEY"),
		os.Getenv("SECRETS_KEY_ID"),
		os.Getenv("SECRETS_PREVIOUS_KEYS"),
	)
	if err != nil {
		return fmt.Errorf("secrets keyring: %w", err)
	}

	auditSvc := audit.NewService(audit.NewRepository(pool))
	approvalSvc := approval.NewService(approval.NewRepository(pool), auditSvc, pool)

	countryRepo := country.NewRepository(pool)
	operatorRepo := operator.NewRepository(pool)
	providerRepo := provider.NewRepository(pool)
	providerSvc := provider.NewService(providerRepo, auditSvc, approvalSvc, keyRing, appEnv)
	customerRepo := customer.NewRepository(pool)
	customerSvc := customer.NewService(customerRepo, auditSvc, approvalSvc)
	senderIDRepo := senderid.NewRepository(pool)
	senderIDSvc := senderid.NewService(senderIDRepo, auditSvc, approvalSvc)

	var usCountryID *uuid.UUID
	if us, err := countryRepo.GetByISO2(ctx, "US"); err == nil && us != nil {
		usCountryID = &us.ID
	}

	// 1. Seed DEV SAMPLE Provider Alpha (DEV_ALPHA)
	alpha, err := providerRepo.GetProviderByCode(ctx, "DEV_ALPHA")
	if err != nil || alpha == nil {
		res, err := providerSvc.CreateProvider(ctx, tc, provider.CreateProviderRequest{
			Name:              "DEV SAMPLE Provider Alpha",
			Code:              "DEV_ALPHA",
			ProviderType:      "smpp",
			LegalEntityName:   "DEV SAMPLE Alpha Carrier Example Ltd.",
			ContactTechnical:  "noc-alpha@example.com",
			ContactCommercial: "commercial-alpha@example.com",
			ContactNOC:        "noc-alpha@example.com",
			CountryID:         usCountryID,
			Timezone:          "UTC",
			CurrencyCode:      "USD",
			Notes:             "DEV SAMPLE fictional SMPP upstream supplier for local development only.",
			Status:            "active",
			Reason:            "DEV SAMPLE provider seed via service layer",
		}, "127.0.0.1", "seed-providers-sample")
		if err != nil {
			return fmt.Errorf("create DEV_ALPHA: %w", err)
		}
		alpha = res.Provider
		fmt.Println("Created provider DEV SAMPLE Provider Alpha (DEV_ALPHA).")
	}

	connsAlpha, _ := providerRepo.ListConnectionsByProvider(ctx, alpha.ID)
	if len(connsAlpha) == 0 {
		port := 2775
		win := 10
		enq := 30
		tlsVal := true
		alphaSec := runtimeSecret()
		_, err := providerSvc.CreateConnection(ctx, tc, alpha.ID, provider.CreateConnectionRequest{
			Name:               "DEV SAMPLE Alpha SMPP Primary",
			Protocol:           "smpp",
			RoleNotes:          "DEV SAMPLE primary transceiver bind (fictional host)",
			Status:             "active",
			MaxTPS:             100,
			SMPPHost:           "smpp-alpha.example.com",
			SMPPPort:           &port,
			SMPPSystemID:       "dev_alpha_sys",
			SMPPPassword:       &alphaSec,
			SMPPSystemType:     "CMT",
			SMPPBindType:       "transceiver",
			SMPPTLS:            &tlsVal,
			SMPPWindowSize:     &win,
			SMPPEnquireLinkSec: &enq,
			SMPPCharset:        "GSM7",
			IPAllowlistNotes:   "198.51.100.10/32 (TEST-NET-2 RFC 5737 documentation range)",
			Reason:             "DEV SAMPLE connection seed with runtime-generated random secret",
		}, "127.0.0.1", "seed-providers-sample")
		if err != nil {
			return fmt.Errorf("create DEV_ALPHA connection: %w", err)
		}
		fmt.Println("Created connection DEV SAMPLE Alpha SMPP Primary (smpp-alpha.example.com:2775).")
	}

	// 2. Seed DEV SAMPLE Provider Beta (DEV_BETA)
	beta, err := providerRepo.GetProviderByCode(ctx, "DEV_BETA")
	if err != nil || beta == nil {
		res, err := providerSvc.CreateProvider(ctx, tc, provider.CreateProviderRequest{
			Name:              "DEV SAMPLE Provider Beta",
			Code:              "DEV_BETA",
			ProviderType:      "http",
			LegalEntityName:   "DEV SAMPLE Beta Messaging Example Inc.",
			ContactTechnical:  "tech-beta@example.com",
			ContactCommercial: "sales-beta@example.com",
			ContactNOC:        "noc-beta@example.com",
			CountryID:         usCountryID,
			Timezone:          "UTC",
			CurrencyCode:      "EUR",
			Notes:             "DEV SAMPLE fictional HTTP upstream supplier for local development only.",
			Status:            "draft",
			Reason:            "DEV SAMPLE provider seed via service layer",
		}, "127.0.0.1", "seed-providers-sample")
		if err != nil {
			return fmt.Errorf("create DEV_BETA: %w", err)
		}
		beta = res.Provider
		fmt.Println("Created provider DEV SAMPLE Provider Beta (DEV_BETA).")
	}

	connsBeta, _ := providerRepo.ListConnectionsByProvider(ctx, beta.ID)
	if len(connsBeta) == 0 {
		betaSec := runtimeSecret()
		_, err := providerSvc.CreateConnection(ctx, tc, beta.ID, provider.CreateConnectionRequest{
			Name:                 "DEV SAMPLE Beta HTTP Gateway",
			Protocol:             "http",
			RoleNotes:            "DEV SAMPLE HTTPS REST endpoint (fictional host)",
			Status:               "draft",
			MaxTPS:               50,
			HTTPBaseURL:          "https://api-beta.example.com/v1/sms",
			HTTPAuthType:         "header_token",
			HTTPCredentials:      &betaSec,
			HTTPFormatNotes:      "JSON POST payload (fictional spec)",
			HTTPDLRCallbackNotes: "Webhook callback to /dlr/beta (Phase 8)",
			Reason:               "DEV SAMPLE HTTP connection seed with runtime-generated random secret",
		}, "127.0.0.1", "seed-providers-sample")
		if err != nil {
			return fmt.Errorf("create DEV_BETA connection: %w", err)
		}
		fmt.Println("Created connection DEV SAMPLE Beta HTTP Gateway (https://api-beta.example.com/v1/sms).")
	}

	// 3. Seed sample coverage rows against existing DEV SAMPLE operators
	ops, _, err := operatorRepo.List(ctx, operator.Filter{Page: 1, PageSize: 20})
	if err == nil {
		seededCov := 0
		for i, op := range ops {
			targetProv := alpha
			covStatus := "active"
			if i%2 == 1 {
				targetProv = beta
				covStatus = "declared"
			}
			opID := op.ID
			existing, total, _ := providerRepo.ListCoverage(ctx, provider.CoverageListFilter{
				ProviderID: &targetProv.ID,
				CountryID:  &op.CountryID,
				OperatorID: &opID,
			})
			if total > 0 || len(existing) > 0 {
				continue
			}
			_, err := providerSvc.CreateCoverage(ctx, tc, provider.CreateCoverageRequest{
				ProviderID: targetProv.ID,
				CountryID:  op.CountryID,
				OperatorID: &opID,
				Status:     covStatus,
				Notes:      fmt.Sprintf("DEV SAMPLE coverage for %s (%s/%s)", op.Name, op.MCC, op.MNC),
				DataSource: devSampleTag,
				Reason:     "DEV SAMPLE provider coverage seed via service layer",
			}, "127.0.0.1", "seed-providers-sample")
			if err == nil {
				seededCov++
			}
		}
		fmt.Printf("Seeded %d DEV SAMPLE provider coverage rows.\n", seededCov)
	}

	// 4. Seed sample customer commercial profile & sender ID for dev customer org (if present)
	var custOrgID uuid.UUID
	var custOrgName string
	err = pool.QueryRow(ctx, `
		SELECT id, name FROM organizations
		WHERE type = 'customer' AND status = 'active' AND name NOT ILIKE 'Purged%'
		ORDER BY CASE WHEN name ILIKE '%BlueStar%' OR name ILIKE '%Dev%' THEN 0 ELSE 1 END, created_at ASC
		LIMIT 1
	`).Scan(&custOrgID, &custOrgName)
	if err == nil && custOrgID != uuid.Nil {
		if _, err := customerRepo.GetProfileByOrgID(ctx, custOrgID); err != nil {
			credit := 5000.0
			tps := 50
			daily := int64(100000)
			monthly := int64(2000000)
			_, err := customerSvc.CreateProfile(ctx, tc, customer.CreateProfileRequest{
				OrganizationID:    custOrgID,
				AccountType:       "prepaid",
				BillingCurrency:   "USD",
				Timezone:          "UTC",
				DefaultLanguage:   "en",
				BillingContact:    "billing@bluestar-dev.example.com",
				TechnicalContact:  "noc@bluestar-dev.example.com",
				CreditLimit:       &credit,
				DefaultMaxTPS:     &tps,
				DailyMessageCap:   &daily,
				MonthlyMessageCap: &monthly,
				AllowedCountries:  []string{"US", "GB", "BD"},
				DestinationPolicy: "allow_listed",
				Status:            "active",
				Notes:             "DEV SAMPLE customer commercial profile for local development testing.",
				Reason:            "DEV SAMPLE customer profile seed via service layer",
			}, "127.0.0.1", "seed-providers-sample")
			if err != nil {
				return fmt.Errorf("create DEV SAMPLE customer profile: %w", err)
			}
			fmt.Printf("Created DEV SAMPLE customer commercial profile for %s.\n", custOrgName)
		}

		// Seed a sample Sender ID for this dev customer org
		existingSids, totalSids, _ := senderIDRepo.List(ctx, senderid.ListFilter{
			OrganizationID: &custOrgID,
			Search:         "DEV-SMS",
		})
		if totalSids == 0 && len(existingSids) == 0 {
			_, err := senderIDSvc.Create(ctx, tc, senderid.CreateSenderIDRequest{
				OrganizationID: custOrgID,
				SenderID:       "DEV-SMS",
				SenderType:     "alphanumeric",
				CountryID:      usCountryID,
				Status:         "approved",
				ApprovalNotes:  "DEV SAMPLE pre-approved sender ID for local testing",
				DataSource:     devSampleTag,
				Reason:         "DEV SAMPLE sender ID seed via service layer",
			}, "127.0.0.1", "seed-providers-sample")
			if err == nil {
				fmt.Printf("Created DEV SAMPLE sender ID 'DEV-SMS' for %s.\n", custOrgName)
			}
		}
	}

	return nil
}
