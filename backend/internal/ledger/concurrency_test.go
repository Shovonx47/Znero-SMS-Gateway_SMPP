package ledger_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/znero-international/znero/backend/internal/ledger"
)

// TestConcurrency50ParallelDebitsNeverOverspend fires 60 concurrent debits of $2 on a $100 wallet.
// Exactly 50 debits must succeed ($100 total) and 10 must be rejected with ErrInsufficientFunds.
// Final balance must be exactly $0.00, and ledger entries must equal $0.00.
func TestConcurrency50ParallelDebitsNeverOverspend(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "conc-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "conc-cust", "customer")

	w, err := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")
	if err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}

	// Initial topup of $100
	_, _, err = h.ledgerSvc.Topup(ctx, h.e.Override, w.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(100),
		Reason: "Initial concurrency seed",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("initial topup: %v", err)
	}

	const totalGoroutines = 60
	debitAmount := decimal.NewFromInt(2)

	var successCount int64
	var insufficientCount int64
	var unexpectedErrors []error
	var errMu sync.Mutex

	var wg sync.WaitGroup
	wg.Add(totalGoroutines)

	startBarrier := make(chan struct{})

	for i := 0; i < totalGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			<-startBarrier // synchronize all goroutines to fire at the exact same instant

			refType := "concurrency_test"
			refID := uuid.New()
			idemp := fmt.Sprintf("idemp-debit-%d-%s", idx, uuid.NewString())
			desc := fmt.Sprintf("Parallel debit #%d", idx)

			_, err := h.ledgerSvc.PostCharge(
				ctx,
				w.ID,
				debitAmount,
				"USD",
				desc,
				&refType,
				&refID,
				&idemp,
			)
			if err == nil {
				atomic.AddInt64(&successCount, 1)
			} else if errors.Is(err, ledger.ErrInsufficientFunds) {
				atomic.AddInt64(&insufficientCount, 1)
			} else {
				errMu.Lock()
				unexpectedErrors = append(unexpectedErrors, fmt.Errorf("goroutine %d: %w", idx, err))
				errMu.Unlock()
			}
		}(i)
	}

	// Release all goroutines simultaneously
	close(startBarrier)
	wg.Wait()

	if len(unexpectedErrors) > 0 {
		t.Fatalf("unexpected concurrency errors: %v", unexpectedErrors)
	}

	if successCount != 50 {
		t.Fatalf("expected exactly 50 successful debits, got %d", successCount)
	}
	if insufficientCount != 10 {
		t.Fatalf("expected exactly 10 insufficient funds rejections, got %d", insufficientCount)
	}

	wFinal, err := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if err != nil {
		t.Fatalf("get wallet: %v", err)
	}

	if !wFinal.CachedBalance.IsZero() {
		t.Fatalf("expected wallet balance 0.0000, got %s", wFinal.CachedBalance.String())
	}

	// Verify entries sum equals cached balance
	reports, err := h.ledgerSvc.ReconcileBalances(ctx, h.e.Override, false)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	for _, r := range reports {
		if r.WalletID == w.ID {
			if r.Status != "matched" {
				t.Fatalf("expected matched reconciliation, got %s (diff %s)", r.Status, r.Difference.String())
			}
			if !r.ComputedBalance.IsZero() {
				t.Fatalf("expected computed balance 0.0000, got %s", r.ComputedBalance.String())
			}
			break
		}
	}
}

// TestConcurrencyParallelHoldsNeverOvercommit verifies parallel hold placements lock and serialize properly.
func TestConcurrencyParallelHoldsNeverOvercommit(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "hold-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "hold-cust", "customer")

	w, err := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")
	if err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}

	// Initial topup of $100
	_, _, err = h.ledgerSvc.Topup(ctx, h.e.Override, w.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(100),
		Reason: "Initial concurrency hold seed",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("initial topup: %v", err)
	}

	const totalGoroutines = 60
	holdAmount := decimal.NewFromInt(2)

	var successCount int64
	var insufficientCount int64
	var unexpectedErrors []error
	var errMu sync.Mutex

	var wg sync.WaitGroup
	wg.Add(totalGoroutines)

	startBarrier := make(chan struct{})

	for i := 0; i < totalGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			<-startBarrier

			refType := "parallel_hold"
			refID := uuid.New()
			idempKey := fmt.Sprintf("idemp-hold-%d-%s", idx, uuid.NewString())

			_, err := h.ledgerSvc.PlaceHold(
				ctx,
				w.ID,
				holdAmount,
				"USD",
				&refType,
				&refID,
				&idempKey,
				time.Now().Add(1*time.Hour),
			)
			if err == nil {
				atomic.AddInt64(&successCount, 1)
			} else if errors.Is(err, ledger.ErrInsufficientFunds) {
				atomic.AddInt64(&insufficientCount, 1)
			} else {
				errMu.Lock()
				unexpectedErrors = append(unexpectedErrors, fmt.Errorf("hold goroutine %d: %w", idx, err))
				errMu.Unlock()
			}
		}(i)
	}

	close(startBarrier)
	wg.Wait()

	if len(unexpectedErrors) > 0 {
		t.Fatalf("unexpected hold errors: %v", unexpectedErrors)
	}

	if successCount != 50 {
		t.Fatalf("expected exactly 50 successful holds, got %d", successCount)
	}
	if insufficientCount != 10 {
		t.Fatalf("expected exactly 10 insufficient funds rejections, got %d", insufficientCount)
	}

	wFinal, err := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if err != nil {
		t.Fatalf("get wallet: %v", err)
	}

	// Ledger balance remains $100
	if !wFinal.CachedBalance.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("expected ledger balance 100, got %s", wFinal.CachedBalance.String())
	}
	// Active holds equal $100
	if !wFinal.ActiveHolds.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("expected active holds 100, got %s", wFinal.ActiveHolds.String())
	}
	// Available balance is $0
	if !wFinal.AvailableBalance.IsZero() {
		t.Fatalf("expected available balance 0, got %s", wFinal.AvailableBalance.String())
	}
}

// TestConcurrencyIdempotentRetriesPostOnce verifies that parallel submissions of the same idempotency key post only once.
func TestConcurrencyIdempotentRetriesPostOnce(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "idemp-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "idemp-cust", "customer")

	w, err := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")
	if err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}

	const totalGoroutines = 20
	sharedIdempKey := "fixed-idempotency-key-" + uuid.NewString()

	var returnedTxIDs sync.Map
	var unexpectedErrors []error
	var errMu sync.Mutex

	var wg sync.WaitGroup
	wg.Add(totalGoroutines)

	startBarrier := make(chan struct{})

	for i := 0; i < totalGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			<-startBarrier

			tx, _, err := h.ledgerSvc.Topup(ctx, h.e.Override, w.ID, ledger.WalletTopupRequest{
				Amount:         decimal.NewFromInt(75),
				Reason:         "Parallel idempotent topup",
				IdempotencyKey: sharedIdempKey,
			}, "127.0.0.1", "test")
			if err != nil {
				errMu.Lock()
				unexpectedErrors = append(unexpectedErrors, fmt.Errorf("goroutine %d: %w", idx, err))
				errMu.Unlock()
				return
			}
			if tx != nil {
				returnedTxIDs.Store(tx.ID.String(), true)
			}
		}(i)
	}

	close(startBarrier)
	wg.Wait()

	if len(unexpectedErrors) > 0 {
		t.Fatalf("unexpected idempotency errors: %v", unexpectedErrors)
	}

	// Exactly 1 unique transaction ID must have been returned across all 20 calls
	var uniqueIDs []string
	returnedTxIDs.Range(func(key, value any) bool {
		uniqueIDs = append(uniqueIDs, key.(string))
		return true
	})

	if len(uniqueIDs) != 1 {
		t.Fatalf("expected exactly 1 unique transaction ID, got %d: %v", len(uniqueIDs), uniqueIDs)
	}

	wFinal, err := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if err != nil {
		t.Fatalf("get wallet: %v", err)
	}

	// Balance must be exactly $75, NOT $75 * 20
	if !wFinal.CachedBalance.Equal(decimal.NewFromInt(75)) {
		t.Fatalf("expected balance 75, got %s", wFinal.CachedBalance.String())
	}
}
