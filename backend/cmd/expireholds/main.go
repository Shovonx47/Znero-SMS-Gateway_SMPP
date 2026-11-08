// Command expireholds scans the holds table and transitions past-due active holds to 'expired',
// releasing the reserved balance back to the wallets' available balance.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/config"
	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/pkg/database"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := database.Connect(ctx, database.Config{
		URL:            cfg.Database.URL,
		MaxConns:       3,
		ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Database connection failed: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	auditRepo := audit.NewRepository(pool)
	auditSvc := audit.NewService(auditRepo)
	approvalRepo := approval.NewRepository(pool)
	approvalSvc := approval.NewService(approvalRepo, auditSvc, pool)
	orgRepo := org.NewRepository(pool)
	ledgerRepo := ledger.NewRepository(pool)
	ledgerSvc := ledger.NewService(ledgerRepo, orgRepo, approvalSvc, auditSvc, pool)

	fmt.Println("Running holds expiry job...")
	count, err := ledgerSvc.ExpireOldHolds(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error expiring holds: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Successfully expired %d overdue hold(s).\n", count)
}
