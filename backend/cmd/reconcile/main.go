package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/shopspring/decimal"
	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/pkg/database"
)

func main() {
	repairFlag := flag.Bool("repair", false, "Repair mismatched cached balances to computed double-entry sum")
	flag.Parse()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "ERROR: DATABASE_URL environment variable is required")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := database.Connect(ctx, database.Config{
		URL:            dbURL,
		MaxConns:       5,
		ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: failed to connect to database: %v\n", err)
		os.Exit(2)
	}
	defer pool.Close()

	repo := ledger.NewRepository(pool)
	wallets, err := repo.ListAllWalletsForReconciliation(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: failed to list wallets: %v\n", err)
		os.Exit(2)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "WALLET ID\tORGANIZATION\tCURRENCY\tCACHED BALANCE\tCOMPUTED BALANCE\tDIFF\tENTRIES\tSTATUS")
	fmt.Fprintln(w, "---------\t------------\t--------\t--------------\t----------------\t----\t-------\t------")

	mismatchCount := 0
	repairedCount := 0

	for _, wallet := range wallets {
		computed, count, err := repo.ComputeWalletBalanceFromEntries(ctx, wallet.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed to compute balance for wallet %s: %v\n", wallet.ID, err)
			continue
		}

		diff := wallet.CachedBalance.Sub(computed)
		status := "MATCHED"

		if !diff.IsZero() {
			if *repairFlag {
				err := repo.UpdateWalletCachedBalance(ctx, nil, wallet.ID, computed, wallet.Version)
				if err != nil {
					status = fmt.Sprintf("REPAIR_FAILED: %v", err)
					mismatchCount++
				} else {
					status = "REPAIRED"
					repairedCount++
					wallet.CachedBalance = computed
					diff = decimal.Zero
				}
			} else {
				status = "MISMATCH"
				mismatchCount++
			}
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
			wallet.ID.String()[:8]+"...",
			truncate(wallet.OrganizationName, 18),
			wallet.Currency,
			wallet.CachedBalance.StringFixed(4),
			computed.StringFixed(4),
			diff.StringFixed(4),
			count,
			status,
		)
	}
	w.Flush()

	fmt.Printf("\nReconciliation Summary: %d wallets checked, %d matched, %d mismatches, %d repaired\n",
		len(wallets), len(wallets)-mismatchCount-repairedCount, mismatchCount, repairedCount)

	if mismatchCount > 0 {
		fmt.Fprintln(os.Stderr, "WARNING: Ledger mismatches detected! Run with -repair to synchronize cached balances.")
		os.Exit(1)
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
