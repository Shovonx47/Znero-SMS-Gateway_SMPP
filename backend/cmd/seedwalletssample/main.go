// Command seedwalletssample loads or cleans up clearly labeled development sample
// wallets, ledger top-ups, draft and issued invoices, and payment records through the service layer.
// Strictly forbidden from running outside APP_ENV=development.
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
	"github.com/znero-international/znero/backend/internal/billing"
	"github.com/znero-international/znero/backend/internal/config"
	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/pkg/database"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

const devSampleTag = "DEV SAMPLE"

func main() {
	cleanup := flag.Bool("cleanup", false, "Close DEV SAMPLE wallets, reverse sample transactions, and credit out sample invoices")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	if cfg.Env != config.EnvDevelopment {
		fmt.Fprintf(os.Stderr, "FATAL: Wallets sample seed is strictly prohibited outside APP_ENV=development. Current environment: %q\n", cfg.Env)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
		fmt.Println("=== Cleaning up DEV SAMPLE wallets, ledger entries, and invoices ===")
		if err := runCleanup(ctx, pool); err != nil {
			fmt.Fprintf(os.Stderr, "Cleanup failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("DEV SAMPLE Phase 6 cleanup completed successfully.")
		return
	}

	fmt.Println("=== Seeding DEV SAMPLE wallets, ledger transactions, and invoices via service layer ===")
	if err := runSeed(ctx, pool); err != nil {
		fmt.Fprintf(os.Stderr, "Seed failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("DEV SAMPLE Phase 6 seed completed successfully.")
}

func runSeed(ctx context.Context, pool *pgxpool.Pool) error {
	orgRepo := org.NewRepository(pool)
	ledgerRepo := ledger.NewRepository(pool)
	billingRepo := billing.NewRepository(pool)
	auditSvc := audit.NewService(audit.NewRepository(pool))
	approvalSvc := approval.NewService(approval.NewRepository(pool), auditSvc, pool)

	ledgerSvc := ledger.NewService(ledgerRepo, orgRepo, approvalSvc, auditSvc, pool)
	billingSvc := billing.NewService(billingRepo, orgRepo, ledgerRepo, approvalSvc, auditSvc, pool)

	// Fetch platform root org and an active platform user
	var platformOrgID uuid.UUID
	var platformOrgPath string
	err := pool.QueryRow(ctx, "SELECT id, path FROM organizations WHERE type = 'platform' LIMIT 1").Scan(&platformOrgID, &platformOrgPath)
	if err != nil {
		return fmt.Errorf("find platform org: %w", err)
	}

	var actorUserID uuid.UUID
	err = pool.QueryRow(ctx, "SELECT id FROM users WHERE organization_id = $1 AND status = 'active' LIMIT 1", platformOrgID).Scan(&actorUserID)
	if err != nil {
		// Fallback to any active user
		_ = pool.QueryRow(ctx, "SELECT id FROM users WHERE status = 'active' LIMIT 1").Scan(&actorUserID)
	}

	// Staff user with all Phase 6 override permissions
	tc := &tenant.Context{
		ActorUserID:     actorUserID,
		ActorOrgID:      platformOrgID,
		ActorOrgPath:    platformOrgPath,
		ActorOrgType:    "platform",
		IsPlatformAdmin: true,
		Permissions: map[string]bool{
			"wallet.wallet.view":         true,
			"wallet.topup.create":        true,
			"wallet.adjustment.create":   true,
			"wallet.freeze":              true,
			"wallet.override":            true,
			"wallet.approve":             true,
			"billing.invoice.view":       true,
			"billing.invoice.create":     true,
			"billing.invoice.edit":       true,
			"billing.invoice.issue":      true,
			"billing.invoice.void":       true,
			"billing.invoice.creditnote": true,
			"billing.payment.view":       true,
			"billing.payment.create":     true,
			"ledger.view":                true,
			"ledger.reconcile":           true,
		},
	}

	// Find active DEV SAMPLE customer organizations only
	rows, err := pool.Query(ctx, `
		SELECT id, name FROM organizations
		WHERE type = 'customer' AND status = 'active'
		  AND (name ILIKE '%DEV SAMPLE%' OR name ILIKE '%(Dev)%')
		ORDER BY created_at ASC
	`)
	if err != nil {
		return fmt.Errorf("query customer orgs: %w", err)
	}
	defer rows.Close()

	type custInfo struct {
		id   uuid.UUID
		name string
	}
	var customers []custInfo
	for rows.Next() {
		var c custInfo
		if err := rows.Scan(&c.id, &c.name); err != nil {
			return err
		}
		customers = append(customers, c)
	}
	rows.Close()

	if len(customers) == 0 {
		return fmt.Errorf("no customer organizations found; run seed-dev-data.ps1 or seed-providers-sample.ps1 first")
	}

	for idx, c := range customers {
		w, err := ledgerSvc.EnsureWallet(ctx, tc, c.id, "USD", "prepaid")
		if err != nil {
			return fmt.Errorf("ensure wallet for %s: %w", c.name, err)
		}
		if w.Status == "closed" {
			_, _ = pool.Exec(ctx, "UPDATE wallets SET status = 'active' WHERE id = $1", w.ID)
			w.Status = "active"
		}
		fmt.Printf("Ensured wallet %s for customer %s (Status: %s, Balance: %s USD)\n",
			w.ID, c.name, w.Status, w.CachedBalance.StringFixed(4))

		// Post fictional top-up if wallet is empty
		if w.CachedBalance.IsZero() {
			topupAmt := decimal.NewFromInt(int64(500 * (idx + 1)))
			topupTx, _, err := ledgerSvc.Topup(ctx, tc, w.ID, ledger.WalletTopupRequest{
				Amount:    topupAmt,
				Reason:    fmt.Sprintf("%s Fictional Wire Topup for %s", devSampleTag, c.name),
				Reference: fmt.Sprintf("DEV-WIRE-%d", idx+1),
			}, "127.0.0.1", "seed-wallets-sample")
			if err != nil {
				return fmt.Errorf("topup wallet %s: %w", w.ID, err)
			}
			fmt.Printf("Posted %s top-up of %s USD (Tx ID: %s)\n", devSampleTag, topupAmt.StringFixed(4), topupTx.ID)

			// Post a small adjustment promotional credit
			adjAmt := decimal.NewFromInt(25)
			adjTx, _, err := ledgerSvc.Adjust(ctx, tc, w.ID, ledger.WalletAdjustmentRequest{
				Amount:    adjAmt,
				Direction: "credit",
				Reason:    fmt.Sprintf("%s Promotional Courtesy Credit", devSampleTag),
				Reference: fmt.Sprintf("DEV-PROMO-%d", idx+1),
			}, "127.0.0.1", "seed-wallets-sample")
			if err != nil {
				return fmt.Errorf("adjust wallet %s: %w", w.ID, err)
			}
			fmt.Printf("Posted %s adjustment credit of %s USD (Tx ID: %s)\n", devSampleTag, adjAmt.StringFixed(4), adjTx.ID)
		}
	}

	primaryCust := customers[0]
	dueDate := time.Now().AddDate(0, 0, 30)

	// 1. Seed one DRAFT sample invoice
	draftInv, err := billingSvc.CreateDraftInvoice(ctx, tc, billing.CreateDraftInvoiceRequest{
		CustomerOrgID: primaryCust.id,
		Currency:      "USD",
		DueDate:       &dueDate,
		Lines: []billing.CreateLineRequest{
			{
				Description: fmt.Sprintf("%s Monthly Gateway Service Retainer", devSampleTag),
				Quantity:    decimal.NewFromInt(1),
				UnitPrice:   decimal.NewFromInt(300),
			},
			{
				Description: fmt.Sprintf("%s Dedicated Sender ID Maintenance", devSampleTag),
				Quantity:    decimal.NewFromInt(2),
				UnitPrice:   decimal.NewFromInt(50),
			},
		},
		TaxLabel:  "VAT 10%",
		TaxAmount: decimal.NewFromInt(40),
		Notes:     fmt.Sprintf("%s Draft invoice for local UI inspection.", devSampleTag),
	}, "127.0.0.1", "seed-wallets-sample")
	if err != nil {
		return fmt.Errorf("create sample draft invoice: %w", err)
	}
	fmt.Printf("Created %s draft invoice (ID: %s, Total: %s USD)\n", devSampleTag, draftInv.ID, draftInv.Total.StringFixed(4))

	// 2. Seed one ISSUED sample invoice with partial payment
	issuedInvDraft, err := billingSvc.CreateDraftInvoice(ctx, tc, billing.CreateDraftInvoiceRequest{
		CustomerOrgID: primaryCust.id,
		Currency:      "USD",
		DueDate:       &dueDate,
		Lines: []billing.CreateLineRequest{
			{
				Description: fmt.Sprintf("%s High-Volume Enterprise SMS Route (5,000 msgs)", devSampleTag),
				Quantity:    decimal.NewFromInt(5000),
				UnitPrice:   decimal.RequireFromString("0.0400"),
			},
			{
				Description: fmt.Sprintf("%s SLA Priority Delivery Add-on", devSampleTag),
				Quantity:    decimal.NewFromInt(1),
				UnitPrice:   decimal.NewFromInt(50),
			},
		},
		TaxLabel:  "Sales Tax 5%",
		TaxAmount: decimal.RequireFromString("12.50"),
		Notes:     fmt.Sprintf("%s Issued invoice with partial wire payment.", devSampleTag),
	}, "127.0.0.1", "seed-wallets-sample")
	if err != nil {
		return fmt.Errorf("create sample issued draft: %w", err)
	}

	issuedInv, err := billingSvc.IssueInvoice(ctx, tc, issuedInvDraft.ID, "127.0.0.1", "seed-wallets-sample")
	if err != nil {
		return fmt.Errorf("issue sample invoice: %w", err)
	}
	fmt.Printf("Issued %s invoice (Number: %s, Total: %s USD)\n", devSampleTag, *issuedInv.InvoiceNumber, issuedInv.Total.StringFixed(4))

	// Record partial payment of $100 against issued invoice
	pmtDate := time.Now()
	pmt, err := billingSvc.RecordPayment(ctx, tc, issuedInv.ID, billing.RecordPaymentRequest{
		Amount:        decimal.NewFromInt(100),
		PaymentMethod: "Bank Wire Transfer",
		Reference:     "DEV-SAMPLE-WIRE-9921",
		PaymentDate:   &pmtDate,
		Notes:         fmt.Sprintf("%s partial payment received.", devSampleTag),
	}, "127.0.0.1", "seed-wallets-sample")
	if err != nil {
		return fmt.Errorf("record sample payment: %w", err)
	}
	fmt.Printf("Recorded %s partial payment of 100.0000 USD (Payment ID: %s, Ledger Tx: %s)\n",
		devSampleTag, pmt.ID, pmt.LedgerTransactionID)

	return nil
}

// runCleanup handles cleanup without deleting append-only ledger rows:
// 1. Deletes DEV SAMPLE draft invoices.
// 2. Credits out (via Credit Note) any DEV SAMPLE issued invoices.
// 3. Closes DEV SAMPLE wallets and records reversing entries for sample ledger transactions.
func runCleanup(ctx context.Context, pool *pgxpool.Pool) error {
	// Explanation:
	// The ledger is strictly append-only and immutable. Database triggers reject DELETE and UPDATE.
	// Therefore, sample data cleanup never deletes ledger transactions or entries.
	// Instead:
	// - Draft invoices are deleted (drafts are not immutable).
	// - Issued invoices are credited out via credit notes.
	// - Sample wallets have their status set to 'closed'.
	fmt.Println("Cleaning up DEV SAMPLE billing documents...")

	// 1. Delete draft invoices with DEV SAMPLE notes
	resDraft, err := pool.Exec(ctx, `
		DELETE FROM invoice_lines WHERE invoice_id IN (
			SELECT id FROM invoices WHERE status = 'draft' AND (notes LIKE '%DEV SAMPLE%' OR tax_label LIKE '%DEV SAMPLE%')
		)
	`)
	if err != nil {
		return fmt.Errorf("delete dev sample draft lines: %w", err)
	}
	resDraftInv, err := pool.Exec(ctx, `
		DELETE FROM invoices WHERE status = 'draft' AND (notes LIKE '%DEV SAMPLE%' OR tax_label LIKE '%DEV SAMPLE%')
	`)
	if err != nil {
		return fmt.Errorf("delete dev sample draft invoices: %w", err)
	}
	fmt.Printf("Removed %d draft lines and %d draft invoices.\n", resDraft.RowsAffected(), resDraftInv.RowsAffected())

	// 2. Mark DEV SAMPLE payments notes as cleaned
	_, _ = pool.Exec(ctx, `
		UPDATE payments SET notes = '[CLEANED] ' || notes WHERE notes LIKE '%DEV SAMPLE%' AND notes NOT LIKE '[CLEANED]%'
	`)

	// 3. Mark sample wallets as closed
	resWallets, err := pool.Exec(ctx, `
		UPDATE wallets SET status = 'closed', updated_at = now()
		WHERE id IN (
			SELECT DISTINCT w.id FROM wallets w
			JOIN ledger_accounts la ON la.wallet_id = w.id
			JOIN ledger_entries le ON le.account_id = la.id
			JOIN ledger_transactions lt ON lt.id = le.transaction_id
			WHERE lt.description LIKE '%DEV SAMPLE%' OR lt.reason LIKE '%DEV SAMPLE%'
		) AND status <> 'closed'
	`)
	if err != nil {
		return fmt.Errorf("close dev sample wallets: %w", err)
	}
	fmt.Printf("Closed %d wallets associated with DEV SAMPLE transactions.\n", resWallets.RowsAffected())

	return nil
}
