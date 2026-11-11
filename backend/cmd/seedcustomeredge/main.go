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
	"github.com/znero-international/znero/backend/internal/config"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/pkg/crypto/password"
	"github.com/znero-international/znero/backend/pkg/database"
)

const (
	SampleCustomerName = "DEV SAMPLE CUSTOMER ACME"
	SampleInboundNum   = "+14155550199"
	SampleWebhookURL   = "http://localhost:8085/webhook"
)

// randomHex returns n hex-encoded random bytes.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("failed to generate random bytes: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func main() {
	cleanup := flag.Bool("cleanup", false, "Clean up sample customer edge configs")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	if cfg.Env != config.EnvDevelopment {
		fmt.Fprintf(os.Stderr, "FATAL: Customer edge sample seed is strictly prohibited outside APP_ENV=development. Current environment: %q\n", cfg.Env)
		os.Exit(1)
	}

	ctx := context.Background()
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

	keyRing, err := secrets.NewKeyRingFromEnv(cfg.Env, cfg.Secrets.EncryptionKey, cfg.Secrets.KeyID, cfg.Secrets.PreviousKeys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize keyRing: %v\n", err)
		os.Exit(1)
	}

	if *cleanup {
		runCleanup(ctx, pool)
		return
	}

	runSeed(ctx, pool, keyRing)
}

func runSeed(ctx context.Context, pool *pgxpool.Pool, keyRing *secrets.KeyRing) {
	fmt.Println("--- Seeding Phase 8B Customer Edge Sample ---")

	// 1. Find or create Sample Customer Org
	var custOrgID uuid.UUID
	err := pool.QueryRow(ctx, "SELECT id FROM organizations WHERE name = $1", SampleCustomerName).Scan(&custOrgID)
	if err != nil {
		custOrgID = uuid.New()
		_, err = pool.Exec(ctx, `
			INSERT INTO organizations (id, name, type, status, currency, max_tps, created_at, updated_at)
			VALUES ($1, $2, 'customer', 'active', 'USD', 100, now(), now())
		`, custOrgID, SampleCustomerName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create sample customer org: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Created Customer Organization: %s (%s)\n", SampleCustomerName, custOrgID)
	} else {
		fmt.Printf("Using existing Customer Organization: %s (%s)\n", SampleCustomerName, custOrgID)
	}

	// 2. Seed Customer API Key — credentials are random per run; never hardcoded.
	generatedKey, err := secrets.GenerateAPIKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to generate API key: %v\n", err)
		os.Exit(1)
	}

	var keyID uuid.UUID
	// Check if any active dev key already exists for this org to avoid duplicates
	err = pool.QueryRow(ctx, "SELECT id FROM customer_api_keys WHERE customer_org_id = $1 AND label = 'Dev Sample Key' LIMIT 1", custOrgID).Scan(&keyID)
	if err != nil {
		// No existing key — insert new one
		keyID = uuid.New()
		_, err = pool.Exec(ctx, `
			INSERT INTO customer_api_keys (id, customer_org_id, label, key_prefix, key_hash, scopes, is_active, created_at, updated_at)
			VALUES ($1, $2, 'Dev Sample Key', $3, $4, '["sms:send", "sms:read", "account:read", "inbound:read"]'::jsonb, true, now(), now())
		`, keyID, custOrgID, generatedKey.Prefix, generatedKey.KeyHash)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to insert API key: %v\n", err)
			os.Exit(1)
		}
	} else {
		// Update existing key with new random token
		_, err = pool.Exec(ctx, `
			UPDATE customer_api_keys SET key_prefix = $1, key_hash = $2, updated_at = now() WHERE id = $3
		`, generatedKey.Prefix, generatedKey.KeyHash, keyID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to update API key: %v\n", err)
			os.Exit(1)
		}
	}

	// 3. Seed Customer SMPP Account — random system_id and password per run.
	smppSystemID := fmt.Sprintf("dev_%s", randomHex(4))
	smppPassword, err := secrets.GenerateRandomPassword(16)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to generate SMPP password: %v\n", err)
		os.Exit(1)
	}

	pwHash, err := password.Hash(smppPassword)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to hash SMPP password: %v\n", err)
		os.Exit(1)
	}

	// Delete any existing dev SMPP accounts for this org before inserting a new random one
	_, _ = pool.Exec(ctx, "DELETE FROM customer_smpp_accounts WHERE customer_org_id = $1", custOrgID)
	smppAccID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO customer_smpp_accounts (
			id, customer_org_id, system_id, password_hash, allowed_bind_types,
			max_binds, max_tps, status, version, created_at, updated_at
		) VALUES ($1, $2, $3, $4, '["tx", "rx", "trx"]'::jsonb, 4, 100, 'active', 1, now(), now())
	`, smppAccID, custOrgID, smppSystemID, pwHash)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to insert SMPP account: %v\n", err)
		os.Exit(1)
	}

	// 4. Seed Customer Webhook — random signing secret per run.
	webhookSigningSecret, err := secrets.GenerateRandomPassword(24)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to generate webhook signing secret: %v\n", err)
		os.Exit(1)
	}
	encSecret, err := keyRing.Encrypt(webhookSigningSecret)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to encrypt webhook signing secret: %v\n", err)
		os.Exit(1)
	}

	var whID uuid.UUID
	err = pool.QueryRow(ctx, "SELECT id FROM customer_webhooks WHERE customer_org_id = $1 AND url = $2", custOrgID, SampleWebhookURL).Scan(&whID)
	if err != nil {
		whID = uuid.New()
		_, err = pool.Exec(ctx, `
			INSERT INTO customer_webhooks (
				id, customer_org_id, url, secret_encrypted, secret_key_id, events,
				status, consecutive_failures, version, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, '["message.delivered", "message.failed", "message.inbound"]'::jsonb, 'active', 0, 1, now(), now())
		`, whID, custOrgID, SampleWebhookURL, encSecret, keyRing.ActiveKeyID())
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to insert Customer Webhook: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Seeded Customer Webhook: %s\n", SampleWebhookURL)
	} else {
		// Rotate signing secret on existing webhook
		_, _ = pool.Exec(ctx, `
			UPDATE customer_webhooks SET secret_encrypted = $1, secret_key_id = $2, updated_at = now() WHERE id = $3
		`, encSecret, keyRing.ActiveKeyID(), whID)
		fmt.Printf("Rotated Webhook signing secret: %s\n", SampleWebhookURL)
	}

	// 5. Seed Inbound Virtual Number
	msgRepo := messaging.NewRepository(pool)
	inboundNum, _ := msgRepo.MatchInboundNumber(ctx, SampleInboundNum)
	if inboundNum == nil {
		newNum := &messaging.InboundNumber{
			ID:            uuid.New(),
			NumberRaw:     SampleInboundNum,
			NumberE164:    SampleInboundNum,
			CustomerOrgID: &custOrgID,
			Type:          "longcode",
			Status:        "active",
			Capabilities:  []string{"sms"},
		}
		if err := msgRepo.CreateInboundNumber(ctx, newNum); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to insert Inbound Number: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Seeded Inbound Virtual Number: %s (Assigned to %s)\n", SampleInboundNum, SampleCustomerName)
	} else {
		fmt.Printf("Inbound Number already exists: %s\n", SampleInboundNum)
	}

	// 6. Ensure Customer Wallet has funds
	var walletID uuid.UUID
	err = pool.QueryRow(ctx, "SELECT id FROM wallets WHERE org_id = $1", custOrgID).Scan(&walletID)
	if err != nil {
		walletID = uuid.New()
		_, _ = pool.Exec(ctx, `
			INSERT INTO wallets (id, org_id, currency, cached_balance, credit_limit, status, version, created_at, updated_at)
			VALUES ($1, $2, 'USD', 250.00, 500.00, 'active', 1, now(), now())
		`, walletID, custOrgID)
		fmt.Printf("Created Wallet for Customer Org: %s with $250.00 balance\n", walletID)
	} else {
		_, _ = pool.Exec(ctx, `UPDATE wallets SET cached_balance = 250.00, credit_limit = 500.00, status = 'active' WHERE id = $1`, walletID)
		fmt.Printf("Updated Wallet %s with $250.00 balance\n", walletID)
	}

	// Print generated credentials once — DEV ONLY, never commit these values.
	fmt.Println("--- Customer Edge Sample Seed Complete! ---")
	fmt.Println("=== DEV ONLY — NEVER COMMIT THESE VALUES ===")
	fmt.Printf("API Key:          %s\n", generatedKey.PlaintextKey)
	fmt.Printf("API Key Prefix:   %s\n", generatedKey.Prefix)
	fmt.Printf("SMPP System ID:   %s\n", smppSystemID)
	fmt.Printf("SMPP Password:    %s  (Port :2776)\n", smppPassword)
	fmt.Printf("Webhook Secret:   %s\n", webhookSigningSecret)
	fmt.Printf("Inbound Num:      %s\n", SampleInboundNum)
	fmt.Printf("Webhook URL:      %s\n", SampleWebhookURL)
	fmt.Println("============================================")
}

func runCleanup(ctx context.Context, pool *pgxpool.Pool) {
	fmt.Println("--- Cleaning up Phase 8B Customer Edge Sample ---")
	var custOrgID uuid.UUID
	err := pool.QueryRow(ctx, "SELECT id FROM organizations WHERE name = $1", SampleCustomerName).Scan(&custOrgID)
	if err == nil {
		_, _ = pool.Exec(ctx, "DELETE FROM customer_api_keys WHERE customer_org_id = $1 AND label = 'Dev Sample Key'", custOrgID)
		_, _ = pool.Exec(ctx, "DELETE FROM customer_smpp_accounts WHERE customer_org_id = $1", custOrgID)
		_, _ = pool.Exec(ctx, "DELETE FROM customer_webhooks WHERE url = $1", SampleWebhookURL)
		_, _ = pool.Exec(ctx, "DELETE FROM inbound_numbers WHERE number_raw = $1", SampleInboundNum)
	}
	fmt.Println("Customer Edge Sample cleanup complete.")
}
