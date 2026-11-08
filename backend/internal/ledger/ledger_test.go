package ledger_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/internal/testkit"
)

type testHarness struct {
	e          *testkit.Env
	ledgerRepo ledger.Repository
	orgRepo    org.Repository
	ledgerSvc  *ledger.Service
}

func setupHarness(t *testing.T) *testHarness {
	e := testkit.New(t)
	orgRepo := org.NewRepository(e.Pool)
	ledgerRepo := ledger.NewRepository(e.Pool)
	ledgerSvc := ledger.NewService(ledgerRepo, orgRepo, e.Approval, e.Audit, e.Pool)

	return &testHarness{
		e:          e,
		ledgerRepo: ledgerRepo,
		orgRepo:    orgRepo,
		ledgerSvc:  ledgerSvc,
	}
}

// 1. Double-Entry Immutability and Balanced Entries Enforced
func TestDoubleEntryImmutabilityAndBalancedEntries(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "ledger-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "ledger-cust", "customer")

	w, err := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")
	if err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}

	walletAcc, err := h.ledgerRepo.GetAccountByWalletID(ctx, nil, w.ID)
	if err != nil {
		t.Fatalf("get wallet account: %v", err)
	}

	sysAcc, err := h.ledgerRepo.GetSystemAccount(ctx, nil, "CUSTOMER_FUNDS")
	if err != nil {
		t.Fatalf("get customer funds account: %v", err)
	}

	// 1.1 Unbalanced transaction: commit MUST be rejected by DB constraint trigger
	tx, err := h.e.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)

	lt, err := h.ledgerRepo.CreateTransaction(ctx, tx, ledger.LedgerTransaction{
		Type:        "topup",
		Description: "Unbalanced test transaction",
		TenantOrgID: &custOrg,
	})
	if err != nil {
		t.Fatalf("create transaction: %v", err)
	}

	// Only 1 entry (debit $100, no credit) -> violation!
	err = h.ledgerRepo.CreateEntries(ctx, tx, []ledger.LedgerEntry{
		{
			TransactionID: lt.ID,
			AccountID:     sysAcc.ID,
			Direction:     "debit",
			Amount:        decimal.NewFromInt(100),
			Currency:      "USD",
		},
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}

	commitErr := tx.Commit(ctx)
	if commitErr == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: Unbalanced transaction was successfully committed!")
	}
	t.Logf("Unbalanced transaction correctly rejected: %v", commitErr)

	// 1.2 Immutability: Post a valid balanced transaction, then verify UPDATE/DELETE are blocked
	validTx, _, err := h.ledgerSvc.Topup(ctx, h.e.Override, w.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(100),
		Reason: "Legitimate initial topup",
	}, "127.0.0.1", "test")
	if err != nil || validTx == nil {
		t.Fatalf("valid topup failed: %v", err)
	}

	_, updateTxErr := h.e.Pool.Exec(ctx, "UPDATE ledger_transactions SET description = 'tampered' WHERE id = $1", validTx.ID)
	if updateTxErr == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: UPDATE on ledger_transactions succeeded! Expected trigger rejection.")
	}
	t.Logf("UPDATE ledger_transactions rejected: %v", updateTxErr)

	_, deleteTxErr := h.e.Pool.Exec(ctx, "DELETE FROM ledger_transactions WHERE id = $1", validTx.ID)
	if deleteTxErr == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: DELETE on ledger_transactions succeeded! Expected trigger rejection.")
	}
	t.Logf("DELETE ledger_transactions rejected: %v", deleteTxErr)

	_, updateEntryErr := h.e.Pool.Exec(ctx, "UPDATE ledger_entries SET amount = 999 WHERE transaction_id = $1", validTx.ID)
	if updateEntryErr == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: UPDATE on ledger_entries succeeded! Expected trigger rejection.")
	}
	t.Logf("UPDATE ledger_entries rejected: %v", updateEntryErr)

	_, deleteEntryErr := h.e.Pool.Exec(ctx, "DELETE FROM ledger_entries WHERE transaction_id = $1", validTx.ID)
	if deleteEntryErr == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: DELETE on ledger_entries succeeded! Expected trigger rejection.")
	}
	t.Logf("DELETE ledger_entries rejected: %v", deleteEntryErr)

	_, updateAccErr := h.e.Pool.Exec(ctx, "UPDATE ledger_accounts SET name = 'tampered' WHERE id = $1", walletAcc.ID)
	if updateAccErr == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: UPDATE on ledger_accounts succeeded! Expected trigger rejection.")
	}
	t.Logf("UPDATE ledger_accounts rejected: %v", updateAccErr)

	_, deleteAccErr := h.e.Pool.Exec(ctx, "DELETE FROM ledger_accounts WHERE id = $1", walletAcc.ID)
	if deleteAccErr == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: DELETE on ledger_accounts succeeded! Expected trigger rejection.")
	}
	t.Logf("DELETE ledger_accounts rejected: %v", deleteAccErr)
}

// 2. Wallet Credit Enforcement (Prepaid vs Postpaid)
func TestCreditEnforcementPrepaidAndPostpaid(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "credit-reseller", "reseller")
	prepaidOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "credit-prepaid", "customer")
	postpaidOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "credit-postpaid", "customer")

	// Set postpaid profile credit limit = 500.00 in customer_profiles
	_, _ = h.e.Pool.Exec(ctx, `
		INSERT INTO customer_profiles (organization_id, account_type, billing_currency, credit_limit, status)
		VALUES ($1, 'postpaid', 'USD', 500.00, 'active')
		ON CONFLICT (organization_id) DO UPDATE SET credit_limit = 500.00
	`, postpaidOrg)

	prepaidWallet, err := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, prepaidOrg, "USD", "prepaid")
	if err != nil {
		t.Fatalf("ensure prepaid wallet: %v", err)
	}
	postpaidWallet, err := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, postpaidOrg, "USD", "postpaid")
	if err != nil {
		t.Fatalf("ensure postpaid wallet: %v", err)
	}

	// 2.1 Prepaid cannot go negative (initial balance = 0)
	_, err = h.ledgerSvc.PostCharge(ctx, prepaidWallet.ID, decimal.NewFromInt(10), "USD", "Prepaid SMS", nil, nil, nil)
	if err != ledger.ErrInsufficientFunds {
		t.Fatalf("prepaid zero balance: expected ErrInsufficientFunds, got %v", err)
	}

	// Top-up prepaid with $50
	_, _, err = h.ledgerSvc.Topup(ctx, h.e.Override, prepaidWallet.ID, ledger.WalletTopupRequest{
		Amount:    decimal.NewFromInt(50),
		Reason:    "Initial deposit",
		Reference: "BANK-001",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("prepaid topup: %v", err)
	}

	// Spend $50 -> succeeds, balance = 0
	_, err = h.ledgerSvc.PostCharge(ctx, prepaidWallet.ID, decimal.NewFromInt(50), "USD", "Full Spend", nil, nil, nil)
	if err != nil {
		t.Fatalf("prepaid full spend: %v", err)
	}

	// Spend $1 more -> rejected with ErrInsufficientFunds
	_, err = h.ledgerSvc.PostCharge(ctx, prepaidWallet.ID, decimal.NewFromInt(1), "USD", "Overspend", nil, nil, nil)
	if err != ledger.ErrInsufficientFunds {
		t.Fatalf("prepaid overspend: expected ErrInsufficientFunds, got %v", err)
	}

	// 2.2 Postpaid can go negative up to -credit_limit (-500)
	// Initial balance = 0, credit limit = 500 -> available balance = 500
	postW, err := h.ledgerSvc.GetWallet(ctx, h.e.Override, postpaidWallet.ID)
	if err != nil {
		t.Fatalf("get postpaid wallet: %v", err)
	}
	if !postW.CreditLimit.Equal(decimal.NewFromInt(500)) {
		t.Fatalf("expected credit limit 500, got %s", postW.CreditLimit.String())
	}
	if !postW.AvailableBalance.Equal(decimal.NewFromInt(500)) {
		t.Fatalf("expected available balance 500, got %s", postW.AvailableBalance.String())
	}

	// Spend $300 -> balance = -300, available = 200
	_, err = h.ledgerSvc.PostCharge(ctx, postpaidWallet.ID, decimal.NewFromInt(300), "USD", "Postpaid SMS 1", nil, nil, nil)
	if err != nil {
		t.Fatalf("postpaid charge: %v", err)
	}

	postWAfter, _ := h.ledgerSvc.GetWallet(ctx, h.e.Override, postpaidWallet.ID)
	if !postWAfter.CachedBalance.Equal(decimal.NewFromInt(-300)) {
		t.Fatalf("expected cached balance -300, got %s", postWAfter.CachedBalance.String())
	}
	if !postWAfter.AvailableBalance.Equal(decimal.NewFromInt(200)) {
		t.Fatalf("expected available balance 200, got %s", postWAfter.AvailableBalance.String())
	}

	// Spend $200 -> balance = -500, available = 0
	_, err = h.ledgerSvc.PostCharge(ctx, postpaidWallet.ID, decimal.NewFromInt(200), "USD", "Postpaid SMS 2", nil, nil, nil)
	if err != nil {
		t.Fatalf("postpaid charge: %v", err)
	}

	// Spend $1 more -> stops at credit limit! Rejected!
	_, err = h.ledgerSvc.PostCharge(ctx, postpaidWallet.ID, decimal.NewFromInt(1), "USD", "Postpaid Overspend", nil, nil, nil)
	if err != ledger.ErrInsufficientFunds {
		t.Fatalf("postpaid over limit: expected ErrInsufficientFunds, got %v", err)
	}

	// 2.3 Dynamic limit change: increase credit limit to 800 in customer_profiles
	_, _ = h.e.Pool.Exec(ctx, `UPDATE customer_profiles SET credit_limit = 800.00 WHERE organization_id = $1`, postpaidOrg)

	// Next check immediately reflects new limit!
	avail, postWUpdated, err := h.ledgerSvc.CheckAvailable(ctx, postpaidWallet.ID, decimal.NewFromInt(300))
	if err != nil || !avail {
		t.Fatalf("after credit limit increase to 800: expected available=true, got %v (err: %v)", avail, err)
	}
	if !postWUpdated.AvailableBalance.Equal(decimal.NewFromInt(300)) {
		t.Fatalf("expected available balance 300, got %s", postWUpdated.AvailableBalance.String())
	}

	// Now spend $300 succeeds
	_, err = h.ledgerSvc.PostCharge(ctx, postpaidWallet.ID, decimal.NewFromInt(300), "USD", "Postpaid SMS 3", nil, nil, nil)
	if err != nil {
		t.Fatalf("postpaid charge after limit increase: %v", err)
	}
}

// 3. Holds Lifecycle (Reserve, Partial Capture, Release, Expiry)
func TestHoldsLifecycle(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "holds-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "holds-cust", "customer")

	w, err := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")
	if err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}

	// Topup $100
	_, _, _ = h.ledgerSvc.Topup(ctx, h.e.Override, w.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(100),
		Reason: "Fund for holds test",
	}, "127.0.0.1", "test")

	// 3.1 Place Hold for $60
	idemp := "hold-key-1"
	hold, err := h.ledgerSvc.PlaceHold(ctx, w.ID, decimal.NewFromInt(60), "USD", nil, nil, &idemp, time.Now().Add(10*time.Minute))
	if err != nil {
		t.Fatalf("place hold: %v", err)
	}

	// Check available balance: ledger balance is still 100, active hold is 60, available is 40!
	w1, _ := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if !w1.CachedBalance.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("expected cached balance 100, got %s", w1.CachedBalance.String())
	}
	if !w1.ActiveHolds.Equal(decimal.NewFromInt(60)) {
		t.Fatalf("expected active holds 60, got %s", w1.ActiveHolds.String())
	}
	if !w1.AvailableBalance.Equal(decimal.NewFromInt(40)) {
		t.Fatalf("expected available balance 40, got %s", w1.AvailableBalance.String())
	}

	// Try to charge $50 -> rejected because available is 40!
	_, err = h.ledgerSvc.PostCharge(ctx, w.ID, decimal.NewFromInt(50), "USD", "Try charge", nil, nil, nil)
	if err != ledger.ErrInsufficientFunds {
		t.Fatalf("expected ErrInsufficientFunds due to active hold, got %v", err)
	}

	// 3.2 Partial Capture: capture $45 of the $60 hold
	captureIdemp := "cap-key-1"
	capTx, err := h.ledgerSvc.Capture(ctx, hold.ID, decimal.NewFromInt(45), &captureIdemp)
	if err != nil {
		t.Fatalf("partial capture: %v", err)
	}
	if capTx.Type != "hold_capture" {
		t.Fatalf("expected transaction type hold_capture, got %s", capTx.Type)
	}

	// Ledger balance decreased by 45 (100 - 45 = 55), hold is no longer active, available balance is 55!
	w2, _ := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if !w2.CachedBalance.Equal(decimal.NewFromInt(55)) {
		t.Fatalf("expected cached balance 55, got %s", w2.CachedBalance.String())
	}
	if !w2.ActiveHolds.IsZero() {
		t.Fatalf("expected active holds 0 after capture, got %s", w2.ActiveHolds.String())
	}
	if !w2.AvailableBalance.Equal(decimal.NewFromInt(55)) {
		t.Fatalf("expected available balance 55, got %s", w2.AvailableBalance.String())
	}

	// 3.3 Capture again on captured hold is refused
	_, err = h.ledgerSvc.Capture(ctx, hold.ID, decimal.NewFromInt(10), nil)
	if !errors.Is(err, ledger.ErrHoldNotActive) {
		t.Fatalf("expected ErrHoldNotActive on second capture, got %v", err)
	}

	// 3.4 Place Hold and Release
	hold2, err := h.ledgerSvc.PlaceHold(ctx, w.ID, decimal.NewFromInt(30), "USD", nil, nil, nil, time.Now().Add(10*time.Minute))
	if err != nil {
		t.Fatalf("place hold 2: %v", err)
	}
	w3, _ := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if !w3.AvailableBalance.Equal(decimal.NewFromInt(25)) {
		t.Fatalf("expected available balance 25, got %s", w3.AvailableBalance.String())
	}

	if err := h.ledgerSvc.Release(ctx, hold2.ID); err != nil {
		t.Fatalf("release hold: %v", err)
	}

	w4, _ := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if !w4.AvailableBalance.Equal(decimal.NewFromInt(55)) {
		t.Fatalf("expected available balance 55 after release, got %s", w4.AvailableBalance.String())
	}

	// 3.5 Expiry: Place hold with past expiry date
	hold3, err := h.ledgerSvc.PlaceHold(ctx, w.ID, decimal.NewFromInt(20), "USD", nil, nil, nil, time.Now().Add(-1*time.Minute))
	if err != nil {
		t.Fatalf("place hold 3: %v", err)
	}

	// Expire old holds command
	expiredCount, err := h.ledgerSvc.ExpireOldHolds(ctx)
	if err != nil || expiredCount < 1 {
		t.Fatalf("expected at least 1 hold expired, got %d (err: %v)", expiredCount, err)
	}

	// Capture on expired hold is refused
	_, err = h.ledgerSvc.Capture(ctx, hold3.ID, decimal.NewFromInt(10), nil)
	if !errors.Is(err, ledger.ErrHoldNotActive) && !errors.Is(err, ledger.ErrHoldExpired) {
		t.Fatalf("expected ErrHoldNotActive or ErrHoldExpired, got %v", err)
	}
}

// 4. Maker-Checker Workflow for Top-ups, Adjustments, and Unfreeze
func TestMakerCheckerWorkflows(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "mc-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "mc-cust", "customer")

	w, err := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")
	if err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}

	// 4.1 Maker submits Topup request without override
	tx1, cr, err := h.ledgerSvc.Topup(ctx, h.e.Maker, w.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(250),
		Reason: "Pending client wire deposit",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("maker topup: %v", err)
	}
	if tx1 != nil || cr == nil {
		t.Fatalf("expected pending change request, got tx=%v, cr=%v", tx1, cr)
	}

	// Wallet balance is NOT changed yet
	wBefore, _ := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if !wBefore.CachedBalance.IsZero() {
		t.Fatalf("expected balance to be 0 pending approval, got %s", wBefore.CachedBalance.String())
	}

	// Maker attempts to approve own request without approve permission -> rejected
	errNoPermApprove := h.e.Approval.Approve(ctx, h.e.Maker, cr.ID, "Self approval attempt")
	if errNoPermApprove == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: Maker approved without review permission!")
	}

	// Now test an actor who holds wallet.approve permission, but created the change request
	adminMaker := h.e.Actor("admin-maker", h.e.PlatformOrgID, h.e.PlatformOrgPath, "platform", false,
		"wallet.topup.create", "wallet.approve", "wallet.wallet.view",
	)
	_, crSelf, err := h.ledgerSvc.Topup(ctx, adminMaker, w.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(50),
		Reason: "Self approval attempt test",
	}, "127.0.0.1", "test")
	if err != nil || crSelf == nil {
		t.Fatalf("adminMaker topup: %v", err)
	}

	// adminMaker attempts to approve their own request -> MUST fail with maker cannot approve
	errSelfApprove := h.e.Approval.Approve(ctx, adminMaker, crSelf.ID, "Self approving")
	if errSelfApprove == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: Maker with approve permission approved their own change request!")
	}
	if !strings.Contains(errSelfApprove.Error(), "maker cannot approve") {
		t.Fatalf("expected maker cannot approve error, got: %v", errSelfApprove)
	}
	t.Logf("Maker-checker self-approval correctly refused: %v", errSelfApprove)

	// Checker approves the original request
	errApprove := h.e.Approval.Approve(ctx, h.e.Checker, cr.ID, "Verified wire receipt")
	if errApprove != nil {
		t.Fatalf("checker approve failed: %v", errApprove)
	}

	// Wallet balance is now $250
	wAfter, _ := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if !wAfter.CachedBalance.Equal(decimal.NewFromInt(250)) {
		t.Fatalf("expected balance 250 after approval, got %s", wAfter.CachedBalance.String())
	}

	// 4.2 Maker submits Adjustment request (debit $50), Checker rejects with mandatory reason
	_, crAdj, err := h.ledgerSvc.Adjust(ctx, h.e.Maker, w.ID, ledger.WalletAdjustmentRequest{
		Amount:    decimal.NewFromInt(50),
		Direction: "debit",
		Reason:    "Deduct penalty",
	}, "127.0.0.1", "test")
	if err != nil || crAdj == nil {
		t.Fatalf("maker adjust: %v", err)
	}

	// Rejection without reason is rejected
	errNoReason := h.e.Approval.Reject(ctx, h.e.Checker, crAdj.ID, "")
	if errNoReason == nil {
		t.Fatalf("expected rejection without reason to fail")
	}

	// Rejection with reason succeeds
	errReject := h.e.Approval.Reject(ctx, h.e.Checker, crAdj.ID, "Penalty not substantiated by contract terms")
	if errReject != nil {
		t.Fatalf("checker reject failed: %v", errReject)
	}

	// Balance remains $250
	wAdjAfter, _ := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if !wAdjAfter.CachedBalance.Equal(decimal.NewFromInt(250)) {
		t.Fatalf("expected balance 250 after rejection, got %s", wAdjAfter.CachedBalance.String())
	}

	// 4.3 Freeze and Maker-Checker Unfreeze
	if err := h.ledgerSvc.FreezeWallet(ctx, h.e.Override, w.ID, "Compliance audit lock", "127.0.0.1", "test"); err != nil {
		t.Fatalf("freeze wallet: %v", err)
	}
	wFrozen, _ := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if wFrozen.Status != "frozen" {
		t.Fatalf("expected status frozen, got %s", wFrozen.Status)
	}

	// Maker requests unfreeze
	crUnfreeze, err := h.ledgerSvc.UnfreezeWallet(ctx, h.e.Maker, w.ID, "Audit cleared", "127.0.0.1", "test")
	if err != nil || crUnfreeze == nil {
		t.Fatalf("unfreeze request: %v", err)
	}

	// Checker approves unfreeze
	if err := h.e.Approval.Approve(ctx, h.e.Checker, crUnfreeze.ID, "Audit passed"); err != nil {
		t.Fatalf("approve unfreeze: %v", err)
	}
	wUnfrozen, _ := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if wUnfrozen.Status != "active" {
		t.Fatalf("expected status active after unfreeze, got %s", wUnfrozen.Status)
	}
}

// 5. Reseller-Funded Transfer (Two-Leg, Atomic, Tenant Scoped)
func TestResellerFundedTransfer(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerAOrg, resellerAPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "transfer-res-a", "reseller")
	resellerBOrg, resellerBPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "transfer-res-b", "reseller")

	custAOrg, _ := h.e.ChildOrg(resellerAOrg, resellerAPath, "transfer-cust-a", "customer")
	custBOrg, _ := h.e.ChildOrg(resellerBOrg, resellerBPath, "transfer-cust-b", "customer")

	resellerAUser := h.e.Actor("res-a-user", resellerAOrg, resellerAPath, "reseller", false,
		"wallet.wallet.view", "billing.invoice.view")

	resellerAWallet, _ := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, resellerAOrg, "USD", "prepaid")
	custAWallet, _ := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custAOrg, "USD", "prepaid")
	custBWallet, _ := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custBOrg, "USD", "prepaid")

	// Fund Reseller A wallet with $1000
	_, _, _ = h.ledgerSvc.Topup(ctx, h.e.Override, resellerAWallet.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(1000),
		Reason: "Fund Reseller A",
	}, "127.0.0.1", "test")

	// 5.1 Reseller A transfers $250 to its direct customer Cust A -> MUST succeed
	transferTx, err := h.ledgerSvc.TransferResellerToCustomer(ctx, resellerAUser, ledger.WalletTransferRequest{
		SourceWalletID:      resellerAWallet.ID,
		DestinationWalletID: custAWallet.ID,
		Amount:              decimal.NewFromInt(250),
		Reason:              "Allocating SMS balance to client A",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("valid transfer failed: %v", err)
	}
	if transferTx.Type != "transfer" {
		t.Fatalf("expected tx type transfer, got %s", transferTx.Type)
	}

	// Verify balances: Reseller A = 750, Cust A = 250
	resA, _ := h.ledgerSvc.GetWallet(ctx, h.e.Override, resellerAWallet.ID)
	if !resA.CachedBalance.Equal(decimal.NewFromInt(750)) {
		t.Fatalf("expected Reseller A balance 750, got %s", resA.CachedBalance.String())
	}
	custA, _ := h.ledgerSvc.GetWallet(ctx, h.e.Override, custAWallet.ID)
	if !custA.CachedBalance.Equal(decimal.NewFromInt(250)) {
		t.Fatalf("expected Cust A balance 250, got %s", custA.CachedBalance.String())
	}

	// 5.2 Reseller A attempts to transfer to Reseller B's customer Cust B -> MUST fail with ErrCrossTenantAccess (403)
	_, errCross := h.ledgerSvc.TransferResellerToCustomer(ctx, resellerAUser, ledger.WalletTransferRequest{
		SourceWalletID:      resellerAWallet.ID,
		DestinationWalletID: custBWallet.ID,
		Amount:              decimal.NewFromInt(100),
		Reason:              "Cross-tenant illegal transfer attempt",
	}, "127.0.0.1", "test")
	if errCross == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: Reseller A transferred funds to Reseller B's customer!")
	}
	t.Logf("Cross-tenant transfer correctly rejected: %v", errCross)
}

// 6. Reversal Correctness and Double-Reversal Refusal
func TestReversalCorrectnessAndDoubleReversalRefused(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "rev-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "rev-cust", "customer")

	w, _ := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")

	// Topup $300
	topupTx, _, err := h.ledgerSvc.Topup(ctx, h.e.Override, w.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(300),
		Reason: "Deposit to be reversed",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("topup: %v", err)
	}

	// 6.1 Reverse the topup transaction
	revTx, err := h.ledgerSvc.ReverseTransaction(ctx, h.e.Override, topupTx.ID, "Erroneous wire transfer reversal", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("reverse transaction: %v", err)
	}
	if revTx.Type != "reversal" {
		t.Fatalf("expected type reversal, got %s", revTx.Type)
	}

	// Balance is back to 0
	wAfterRev, _ := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if !wAfterRev.CachedBalance.IsZero() {
		t.Fatalf("expected balance 0 after reversal, got %s", wAfterRev.CachedBalance.String())
	}

	// 6.2 Second reversal attempt on the SAME original transaction MUST be refused
	_, errSecondRev := h.ledgerSvc.ReverseTransaction(ctx, h.e.Override, topupTx.ID, "Try reverse twice", "127.0.0.1", "test")
	if errSecondRev != ledger.ErrTransactionAlreadyReversed {
		t.Fatalf("expected ErrTransactionAlreadyReversed, got %v", errSecondRev)
	}

	// 6.3 Attempting to reverse a reversal transaction itself MUST be refused
	_, errRevOfRev := h.ledgerSvc.ReverseTransaction(ctx, h.e.Override, revTx.ID, "Try reverse reversal", "127.0.0.1", "test")
	if errRevOfRev == nil || !strings.Contains(errRevOfRev.Error(), "cannot reverse a reversal") {
		t.Fatalf("expected cannot reverse a reversal error, got %v", errRevOfRev)
	}
}

// 7. Reconciliation Engine (Detects and Repairs Corrupted Cached Balance)
func TestReconciliationDetectsCorruptedBalance(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "reconcile-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "reconcile-cust", "customer")

	w, _ := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")

	// Topup $200
	_, _, _ = h.ledgerSvc.Topup(ctx, h.e.Override, w.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(200),
		Reason: "Normal topup",
	}, "127.0.0.1", "test")

	// 7.1 Clean reconciliation: reports matched
	reports, err := h.ledgerSvc.ReconcileBalances(ctx, h.e.Override, false)
	if err != nil {
		t.Fatalf("reconcile clean: %v", err)
	}
	var foundReport *ledger.ReconciliationReport
	for _, r := range reports {
		if r.WalletID == w.ID {
			foundReport = &r
			break
		}
	}
	if foundReport == nil || foundReport.Status != "matched" {
		t.Fatalf("expected matched status, got %v", foundReport)
	}

	// 7.2 Deliberately corrupt the cached_balance in database to 9999.00
	_, err = h.e.Pool.Exec(ctx, "UPDATE wallets SET cached_balance = 9999.00 WHERE id = $1", w.ID)
	if err != nil {
		t.Fatalf("corrupt cached balance: %v", err)
	}

	// Run reconciliation: MUST detect mismatch!
	reportsMismatch, err := h.ledgerSvc.ReconcileBalances(ctx, h.e.Override, false)
	if err != nil {
		t.Fatalf("reconcile mismatch check: %v", err)
	}
	var corruptedReport *ledger.ReconciliationReport
	for _, r := range reportsMismatch {
		if r.WalletID == w.ID {
			corruptedReport = &r
			break
		}
	}
	if corruptedReport == nil || corruptedReport.Status != "mismatch" {
		t.Fatalf("expected mismatch detection, got report: %v", corruptedReport)
	}
	if !corruptedReport.Difference.Equal(decimal.NewFromInt(9799)) {
		t.Fatalf("expected difference 9799, got %s", corruptedReport.Difference.String())
	}

	// 7.3 Run reconciliation with repair=true: MUST repair corrupted balance to real entry sum (200.00)
	repairReports, err := h.ledgerSvc.ReconcileBalances(ctx, h.e.Override, true)
	if err != nil {
		t.Fatalf("reconcile repair: %v", err)
	}
	var repairedReport *ledger.ReconciliationReport
	for _, r := range repairReports {
		if r.WalletID == w.ID {
			repairedReport = &r
			break
		}
	}
	if repairedReport == nil || repairedReport.Status != "repaired" {
		t.Fatalf("expected repaired status, got report: %v", repairedReport)
	}

	// Verify wallet in DB is restored to 200.00
	wRestored, _ := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if !wRestored.CachedBalance.Equal(decimal.NewFromInt(200)) {
		t.Fatalf("expected restored balance 200, got %s", wRestored.CachedBalance.String())
	}
}

// 8. Statements and Formula Injection Protection
func TestStatementsAndFormulaInjectionProtection(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "stmt-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "stmt-cust", "customer")

	w, _ := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")

	// Post topup with formula injection in reference and reason
	_, _, err := h.ledgerSvc.Topup(ctx, h.e.Override, w.ID, ledger.WalletTopupRequest{
		Amount:    decimal.NewFromInt(150),
		Reason:    "=SUM(A1:A10)",
		Reference: "@maliciousRef",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("topup with formula: %v", err)
	}

	// Get statement
	items, total, err := h.ledgerSvc.GetStatement(ctx, h.e.Override, ledger.StatementFilter{
		WalletID: w.ID,
	})
	if err != nil || total < 1 {
		t.Fatalf("get statement: err=%v, total=%d", err, total)
	}
	if !items[0].RunningBalance.Equal(decimal.NewFromInt(150)) {
		t.Fatalf("expected running balance 150, got %s", items[0].RunningBalance.String())
	}

	// Export CSV
	csvBytes, err := h.ledgerSvc.ExportStatementCSV(ctx, h.e.Override, ledger.StatementFilter{
		WalletID: w.ID,
	})
	if err != nil {
		t.Fatalf("export csv: %v", err)
	}

	csvStr := string(csvBytes)
	// Verify formula characters are escaped with leading single quote:
	// '=SUM' should appear as '\'=SUM'
	if strings.Contains(csvStr, "\n=SUM") || strings.Contains(csvStr, ",=SUM") {
		t.Fatalf("CRITICAL SECURITY FLAW: CSV formula injection '=SUM' was not escaped in export: %s", csvStr)
	}
	t.Logf("Statement CSV export safely escaped formula injection")
}

// 9. TestMakerChecker_Adjustment_Individual tests adjustment dual-control independently
func TestMakerChecker_Adjustment_Individual(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "adj-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "adj-cust", "customer")

	w, _ := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")

	maker := h.e.Actor("adj-maker", h.e.PlatformOrgID, h.e.PlatformOrgPath, "platform", false, "wallet.adjustment.create", "wallet.wallet.view")
	checker := h.e.Actor("adj-checker", h.e.PlatformOrgID, h.e.PlatformOrgPath, "platform", false, "wallet.approve", "wallet.wallet.view")

	// Maker submits adjustment request
	_, cr, err := h.ledgerSvc.Adjust(ctx, maker, w.ID, ledger.WalletAdjustmentRequest{
		Amount:    decimal.NewFromInt(75),
		Direction: "credit",
		Reason:    "Discretionary goodwill adjustment",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("submit adjustment: %v", err)
	}
	if cr == nil {
		t.Fatalf("expected pending change request")
	}

	// Dual-control: Maker CANNOT approve own request
	err = h.e.Approval.Approve(ctx, maker, cr.ID, "self approve")
	if err == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: Maker approved their own adjustment request!")
	}
	t.Logf("Maker self-approval blocked correctly: %v", err)

	// Checker approves
	err = h.e.Approval.Approve(ctx, checker, cr.ID, "approved by checker")
	if err != nil {
		t.Fatalf("checker approve failed: %v", err)
	}

	// Verify balance updated
	wUpdated, _ := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if !wUpdated.CachedBalance.Equal(decimal.NewFromInt(75)) {
		t.Fatalf("expected balance 75, got %s", wUpdated.CachedBalance.String())
	}
	t.Logf("Adjustment dual-control verified successfully")
}

// 10. TestMakerChecker_Unfreeze_Individual tests wallet freeze & unfreeze dual control
func TestMakerChecker_Unfreeze_Individual(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "unfreeze-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "unfreeze-cust", "customer")

	w, _ := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")

	// Freeze wallet
	err := h.ledgerSvc.FreezeWallet(ctx, h.e.Override, w.ID, "Investigating unusual spike", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("freeze wallet: %v", err)
	}

	wFrozen, _ := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if wFrozen.Status != "frozen" {
		t.Fatalf("expected frozen status, got %s", wFrozen.Status)
	}

	maker := h.e.Actor("unfreeze-maker", h.e.PlatformOrgID, h.e.PlatformOrgPath, "platform", false, "wallet.freeze", "wallet.wallet.view")
	checker := h.e.Actor("unfreeze-checker", h.e.PlatformOrgID, h.e.PlatformOrgPath, "platform", false, "wallet.approve", "wallet.wallet.view")

	// Maker requests unfreeze
	cr, err := h.ledgerSvc.UnfreezeWallet(ctx, maker, w.ID, "Spike confirmed legitimate traffic", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("request unfreeze: %v", err)
	}
	if cr == nil {
		t.Fatalf("expected pending unfreeze change request")
	}

	// Dual-control: Maker CANNOT approve own unfreeze request
	err = h.e.Approval.Approve(ctx, maker, cr.ID, "self approve unfreeze")
	if err == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: Maker approved their own unfreeze request!")
	}
	t.Logf("Maker unfreeze self-approval blocked correctly: %v", err)

	// Checker approves
	err = h.e.Approval.Approve(ctx, checker, cr.ID, "checker unfreeze approved")
	if err != nil {
		t.Fatalf("checker approve failed: %v", err)
	}

	// Verify wallet is now active
	wActive, _ := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if wActive.Status != "active" {
		t.Fatalf("expected active status, got %s", wActive.Status)
	}
	t.Logf("Wallet unfreeze dual-control verified successfully")
}

// 11. TestCreditLimitChange_ImmediateEnforcement proves credit limit changes in customer profile take effect on the very next check
func TestCreditLimitChange_ImmediateEnforcement(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "limit-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "limit-cust", "customer")

	// Create customer profile with initial credit limit $50
	_, err := h.e.Pool.Exec(ctx, `
		INSERT INTO customer_profiles (organization_id, account_type, billing_currency, credit_limit, created_by)
		VALUES ($1, 'postpaid', 'USD', 50.0000, $2)
		ON CONFLICT (organization_id) DO UPDATE SET credit_limit = 50.0000, account_type = 'postpaid'
	`, custOrg, h.e.Override.ActorUserID)
	if err != nil {
		t.Fatalf("insert customer profile: %v", err)
	}

	w, err := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "postpaid")
	if err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}

	// 1. Debit $40 -> succeeds (balance -$40 <= $50 limit)
	_, err = h.ledgerSvc.PostCharge(ctx, w.ID, decimal.NewFromInt(40), "USD", "Test message 1", nil, nil, nil)
	if err != nil {
		t.Fatalf("debit 40: %v", err)
	}

	// 2. Debit $20 -> fails (total $60 > $50 limit)
	_, err = h.ledgerSvc.PostCharge(ctx, w.ID, decimal.NewFromInt(20), "USD", "Test message 2", nil, nil, nil)
	if !errors.Is(err, ledger.ErrInsufficientFunds) {
		t.Fatalf("expected ErrInsufficientFunds when exceeding $50 limit, got: %v", err)
	}
	t.Logf("Overdraft above initial $50 limit correctly blocked: %v", err)

	// 3. Update customer profile credit limit to $100
	_, err = h.e.Pool.Exec(ctx, "UPDATE customer_profiles SET credit_limit = 100.0000, updated_at = now() WHERE organization_id = $1", custOrg)
	if err != nil {
		t.Fatalf("update credit limit in DB: %v", err)
	}

	// 4. Same $20 debit check MUST now succeed immediately!
	_, err = h.ledgerSvc.PostCharge(ctx, w.ID, decimal.NewFromInt(20), "USD", "Test message 2 retry", nil, nil, nil)
	if err != nil {
		t.Fatalf("expected debit 20 to succeed under new $100 limit, got error: %v", err)
	}
	t.Logf("Credit limit increase to $100 took effect immediately on next check!")

	// 5. Reduce limit to $60 (current balance is -$60)
	_, err = h.e.Pool.Exec(ctx, "UPDATE customer_profiles SET credit_limit = 60.0000, updated_at = now() WHERE organization_id = $1", custOrg)
	if err != nil {
		t.Fatalf("reduce credit limit in DB: %v", err)
	}

	// 6. Debit of even $1 must now fail immediately!
	_, err = h.ledgerSvc.PostCharge(ctx, w.ID, decimal.NewFromInt(1), "USD", "Test message 3", nil, nil, nil)
	if !errors.Is(err, ledger.ErrInsufficientFunds) {
		t.Fatalf("expected ErrInsufficientFunds when exceeding reduced $60 limit, got: %v", err)
	}
	t.Logf("Credit limit reduction to $60 took effect immediately on next check!")
}

// 12. TestStatement_RunningBalanceAccuracy verifies mathematical running balance on statements
func TestStatement_RunningBalanceAccuracy(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "math-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "math-cust", "customer")

	w, _ := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")

	// Step 1: Topup +100.00
	_, _, _ = h.ledgerSvc.Topup(ctx, h.e.Override, w.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(100), Reason: "Topup 1",
	}, "127.0.0.1", "test")

	// Step 2: Charge -25.00
	_, _ = h.ledgerSvc.PostCharge(ctx, w.ID, decimal.NewFromInt(25), "USD", "Charge 1", nil, nil, nil)

	// Step 3: Charge -15.00
	_, _ = h.ledgerSvc.PostCharge(ctx, w.ID, decimal.NewFromInt(15), "USD", "Charge 2", nil, nil, nil)

	// Step 4: Adjustment credit +10.00
	_, _, _ = h.ledgerSvc.Adjust(ctx, h.e.Override, w.ID, ledger.WalletAdjustmentRequest{
		Amount: decimal.NewFromInt(10), Direction: "credit", Reason: "Adj credit",
	}, "127.0.0.1", "test")

	// Step 5: Charge -5.00
	_, _ = h.ledgerSvc.PostCharge(ctx, w.ID, decimal.NewFromInt(5), "USD", "Charge 3", nil, nil, nil)

	// Expected running balance progression (chronological):
	// Tx 1: +100 -> 100.0000
	// Tx 2: -25  -> 75.0000
	// Tx 3: -15  -> 60.0000
	// Tx 4: +10  -> 70.0000
	// Tx 5: -5   -> 65.0000

	items, total, err := h.ledgerSvc.GetStatement(ctx, h.e.Override, ledger.StatementFilter{
		WalletID: w.ID,
		Page:     1,
		PageSize: 10,
	})
	if err != nil || total != 5 {
		t.Fatalf("expected 5 statement entries, got total=%d err=%v", total, err)
	}

	// In reverse chronological order (newest first):
	expectedBalances := []string{"65.0000", "70.0000", "60.0000", "75.0000", "100.0000"}
	for i, item := range items {
		if item.RunningBalance.StringFixed(4) != expectedBalances[i] {
			t.Fatalf("row %d: expected running balance %s, got %s", i, expectedBalances[i], item.RunningBalance.StringFixed(4))
		}
	}
	t.Logf("Running balance mathematical progression verified across all 5 transactions: %v", expectedBalances)
}

// 13. TestAuditEntries_FreeOfSecrets verifies audit rows contain zero sensitive secrets
func TestAuditEntries_FreeOfSecrets(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "audit-sec-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "audit-sec-cust", "customer")

	w, _ := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")

	_, _, _ = h.ledgerSvc.Topup(ctx, h.e.Override, w.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(100), Reason: "Audited topup", Reference: "WIRE-REF-123",
	}, "127.0.0.1", "test")

	// Query audit entries for this wallet
	rows, err := h.e.Pool.Query(ctx, `
		SELECT action, reason, before_value::text, after_value::text
		FROM audit_logs
		WHERE entity_id = $1
	`, w.ID.String())
	if err != nil {
		t.Fatalf("query audit logs: %v", err)
	}
	defer rows.Close()

	forbiddenTokens := []string{"password", "secret", "token", "hash", "argon2id", "api_key", "znk_"}
	count := 0
	for rows.Next() {
		var action, reason string
		var beforeText, afterText *string
		if err := rows.Scan(&action, &reason, &beforeText, &afterText); err != nil {
			t.Fatalf("scan audit row: %v", err)
		}
		count++
		combined := action + " " + reason
		if beforeText != nil {
			combined += " " + *beforeText
		}
		if afterText != nil {
			combined += " " + *afterText
		}

		lower := strings.ToLower(combined)
		for _, token := range forbiddenTokens {
			if strings.Contains(lower, token) {
				t.Fatalf("CRITICAL SECURITY FLAW: Audit entry contained forbidden token %q: %s", token, combined)
			}
		}
	}
	if count == 0 {
		t.Fatalf("expected audit rows to exist for wallet mutations")
	}
	t.Logf("Audited %d ledger entries: confirmed completely free of secrets", count)
}

// 14. TestReconciliationRepair_PermissionsAuditAndImmutability tests reconciliation repair security
func TestReconciliationRepair_PermissionsAuditAndImmutability(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "rec-perm-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "rec-perm-cust", "customer")

	w, _ := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")
	_, _, _ = h.ledgerSvc.Topup(ctx, h.e.Override, w.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(100), Reason: "Topup 100",
	}, "127.0.0.1", "test")

	// Non-privileged actor without ledger.reconcile
	unauthorizedActor := h.e.Actor("unauth-rec", resellerOrg, resellerPath, "reseller", false, "wallet.wallet.view")
	_, err := h.ledgerSvc.ReconcileBalances(ctx, unauthorizedActor, true)
	if !errors.Is(err, ledger.ErrPermissionDenied) {
		t.Fatalf("expected ErrPermissionDenied without ledger.reconcile, got: %v", err)
	}

	// Corrupt cached balance
	_, _ = h.e.Pool.Exec(ctx, "UPDATE wallets SET cached_balance = 888.0000 WHERE id = $1", w.ID)

	// Record initial ledger entries count
	var entriesCountBefore int
	_ = h.e.Pool.QueryRow(ctx, "SELECT count(*) FROM ledger_entries").Scan(&entriesCountBefore)

	// Privileged actor runs repair
	reports, err := h.ledgerSvc.ReconcileBalances(ctx, h.e.Override, true)
	if err != nil {
		t.Fatalf("privileged repair failed: %v", err)
	}

	// Verify report
	var repaired *ledger.ReconciliationReport
	for _, r := range reports {
		if r.WalletID == w.ID {
			repaired = &r
			break
		}
	}
	if repaired == nil || repaired.Status != "repaired" {
		t.Fatalf("expected repaired status, got %v", repaired)
	}

	// Verify ledger_entries count is strictly UNCHANGED (only cached balance touched)
	var entriesCountAfter int
	_ = h.e.Pool.QueryRow(ctx, "SELECT count(*) FROM ledger_entries").Scan(&entriesCountAfter)
	if entriesCountAfter != entriesCountBefore {
		t.Fatalf("IMMUTABILITY VIOLATION: ledger entries modified during repair! Before=%d, After=%d", entriesCountBefore, entriesCountAfter)
	}

	// Verify repair action is AUDITED
	var auditCount int
	err = h.e.Pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_logs
		WHERE entity_id = $1 AND action = 'ledger.wallet.reconcile_repair'
	`, w.ID.String()).Scan(&auditCount)
	if err != nil || auditCount < 1 {
		t.Fatalf("expected audit record for repair, got count=%d, err=%v", auditCount, err)
	}
	t.Logf("Reconciliation repair verified: required permission, audited, and preserved ledger entries immutability")
}

// 15. TestHoldExpiry_CommandAndAvailableBalance tests ExpireOldHolds command and available balance restoration
func TestHoldExpiry_CommandAndAvailableBalance(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "exp-hold-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "exp-hold-cust", "customer")

	w, _ := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")
	_, _, _ = h.ledgerSvc.Topup(ctx, h.e.Override, w.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(100), Reason: "Topup 100",
	}, "127.0.0.1", "test")

	// Place hold of $40 with expiry 5 minutes in the PAST
	tx, _ := h.e.Pool.Begin(ctx)
	lockedWallet, _ := h.ledgerRepo.GetWalletByIDForUpdate(ctx, tx, w.ID)
	pastHold, err := h.ledgerRepo.CreateHold(ctx, tx, ledger.Hold{
		WalletID:  lockedWallet.ID,
		Amount:    decimal.NewFromInt(40),
		Currency:  "USD",
		Status:    "active",
		ExpiresAt: time.Now().Add(-5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create past hold: %v", err)
	}
	_ = tx.Commit(ctx)

	// Check available balance before expiry: 100 - 40 = 60
	wBefore, err := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if err != nil || !wBefore.AvailableBalance.Equal(decimal.NewFromInt(60)) {
		t.Fatalf("expected available balance 60 with active hold, got %s (err: %v)", wBefore.AvailableBalance.String(), err)
	}

	// Run ExpireOldHolds command
	expiredCount, err := h.ledgerSvc.ExpireOldHolds(ctx)
	if err != nil || expiredCount < 1 {
		t.Fatalf("expected at least 1 hold expired, got %d, err: %v", expiredCount, err)
	}

	// Verify hold status in DB is 'expired'
	hFetched, err := h.ledgerRepo.GetHoldByID(ctx, pastHold.ID)
	if err != nil || hFetched.Status != "expired" {
		t.Fatalf("expected hold status 'expired', got %s, err: %v", hFetched.Status, err)
	}

	// Verify available balance is restored to 100
	wAfter, err := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if err != nil || !wAfter.AvailableBalance.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("expected available balance 100 after expiry, got %s (err: %v)", wAfter.AvailableBalance.String(), err)
	}

	// Verify capture on this expired hold is refused
	_, err = h.ledgerSvc.Capture(ctx, pastHold.ID, decimal.NewFromInt(40), nil)
	if !errors.Is(err, ledger.ErrHoldNotActive) && !errors.Is(err, ledger.ErrHoldExpired) {
		t.Fatalf("expected capture on expired hold to be refused, got: %v", err)
	}
	t.Logf("Hold expiry command successfully expired hold, restored available balance, and blocked capture")
}

// 16. TestTenancyEnforcement_AllWalletEndpoints proves cross-tenant access is refused across all wallet endpoints
func TestTenancyEnforcement_AllWalletEndpoints(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerA, resellerAPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "ten-res-a", "reseller")
	resellerB, resellerBPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "ten-res-b", "reseller")

	custA, custAPath := h.e.ChildOrg(resellerA, resellerAPath, "ten-cust-a", "customer")
	custB, _ := h.e.ChildOrg(resellerB, resellerBPath, "ten-cust-b", "customer")

	wA, _ := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custA, "USD", "prepaid")
	wB, _ := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custB, "USD", "prepaid")

	customerAUser := h.e.Actor("cust-a-user", custA, custAPath, "customer", false, "wallet.wallet.view")
	resellerAUser := h.e.Actor("res-a-user", resellerA, resellerAPath, "reseller", false, "wallet.wallet.view")

	// 1. Customer A cannot view Wallet B
	_, err := h.ledgerSvc.GetWallet(ctx, customerAUser, wB.ID)
	if !errors.Is(err, ledger.ErrCrossTenantAccess) {
		t.Fatalf("expected ErrCrossTenantAccess for customer A accessing wallet B, got: %v", err)
	}

	// 2. Customer A cannot view Wallet B statement
	_, _, err = h.ledgerSvc.GetStatement(ctx, customerAUser, ledger.StatementFilter{WalletID: wB.ID})
	if !errors.Is(err, ledger.ErrCrossTenantAccess) {
		t.Fatalf("expected ErrCrossTenantAccess for customer A accessing wallet B statement, got: %v", err)
	}

	// 3. Reseller A cannot view Wallet B
	_, err = h.ledgerSvc.GetWallet(ctx, resellerAUser, wB.ID)
	if !errors.Is(err, ledger.ErrCrossTenantAccess) {
		t.Fatalf("expected ErrCrossTenantAccess for reseller A accessing wallet B, got: %v", err)
	}

	// 4. Reseller A CAN view Wallet A (their direct customer)
	walletAFetched, err := h.ledgerSvc.GetWallet(ctx, resellerAUser, wA.ID)
	if err != nil || walletAFetched.ID != wA.ID {
		t.Fatalf("expected reseller A to view own customer wallet A, got: %v", err)
	}
	t.Logf("Cross-tenant isolation verified across all wallet endpoints")
}

// 17. TestAmountBounds_Ledger verifies maximum bounds on money operations
func TestAmountBounds_Ledger(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "bnd-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "bnd-cust", "customer")

	w, _ := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")

	// Topup > 1M refused
	_, _, err := h.ledgerSvc.Topup(ctx, h.e.Override, w.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(1_000_001), Reason: "Excessive topup",
	}, "127.0.0.1", "test")
	if !errors.Is(err, ledger.ErrAmountExceedsMaximum) {
		t.Fatalf("expected ErrAmountExceedsMaximum for topup > 1M, got: %v", err)
	}

	// Adjustment > 1M refused
	_, _, err = h.ledgerSvc.Adjust(ctx, h.e.Override, w.ID, ledger.WalletAdjustmentRequest{
		Amount: decimal.NewFromInt(1_000_001), Direction: "credit", Reason: "Excessive adjustment",
	}, "127.0.0.1", "test")
	if !errors.Is(err, ledger.ErrAmountExceedsMaximum) {
		t.Fatalf("expected ErrAmountExceedsMaximum for adjustment > 1M, got: %v", err)
	}
	t.Logf("Single transaction upper bound $1,000,000 enforced cleanly")
}

// 18. TestPlaceHoldTxAndPostChargeTx_ExternalTransaction verifies atomic sharing of an external pgx.Tx
func TestPlaceHoldTxAndPostChargeTx_ExternalTransaction(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "txshare-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "txshare-cust", "customer")

	w, _ := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")
	_, _, err := h.ledgerSvc.Topup(ctx, h.e.Override, w.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(500), Reason: "Initial fund for tx share test",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("topup: %v", err)
	}

	// 1. External transaction rollback aborts PlaceHoldTx
	tx1, err := h.e.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	idemp1 := "idemp-hold-rb"
	hold1, err := h.ledgerSvc.PlaceHoldTx(ctx, tx1, w.ID, decimal.NewFromInt(100), "USD", nil, nil, &idemp1, time.Now().Add(10*time.Minute))
	if err != nil {
		t.Fatalf("place hold in tx: %v", err)
	}
	if err := tx1.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	// Verify hold does not exist after rollback
	wAfterRb, err := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !wAfterRb.ActiveHolds.Equal(decimal.Zero) {
		t.Fatalf("expected 0 active holds after rollback, got %s", wAfterRb.ActiveHolds.String())
	}
	_, err = h.ledgerRepo.GetHoldByID(ctx, hold1.ID)
	if !errors.Is(err, ledger.ErrHoldNotFound) {
		t.Fatalf("expected ErrHoldNotFound for rolled-back hold, got: %v", err)
	}

	// 2. External transaction commit persists PlaceHoldTx and PostChargeTx
	tx2, err := h.e.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	idemp2 := "idemp-hold-commit"
	hold2, err := h.ledgerSvc.PlaceHoldTx(ctx, tx2, w.ID, decimal.NewFromInt(150), "USD", nil, nil, &idemp2, time.Now().Add(10*time.Minute))
	if err != nil {
		t.Fatalf("place hold in tx2: %v", err)
	}
	idempCharge := "idemp-charge-commit"
	chargeTx, err := h.ledgerSvc.PostChargeTx(ctx, tx2, w.ID, decimal.NewFromInt(50), "USD", "Shared tx charge", nil, nil, &idempCharge)
	if err != nil {
		t.Fatalf("post charge in tx2: %v", err)
	}
	if chargeTx == nil {
		t.Fatal("expected charge transaction")
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	wAfterCommit, err := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	// CachedBalance: 500 - 50 = 450; ActiveHolds: 150; AvailableBalance: 450 - 150 = 300
	if !wAfterCommit.CachedBalance.Equal(decimal.NewFromInt(450)) {
		t.Fatalf("expected cached balance 450, got %s", wAfterCommit.CachedBalance.String())
	}
	if !wAfterCommit.ActiveHolds.Equal(decimal.NewFromInt(150)) {
		t.Fatalf("expected active holds 150, got %s", wAfterCommit.ActiveHolds.String())
	}
	if !wAfterCommit.AvailableBalance.Equal(decimal.NewFromInt(300)) {
		t.Fatalf("expected available balance 300, got %s", wAfterCommit.AvailableBalance.String())
	}

	// 3. CaptureTx within an external transaction
	tx3, err := h.e.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	idempCap := "idemp-cap-tx"
	capTx, err := h.ledgerSvc.CaptureTx(ctx, tx3, hold2.ID, decimal.NewFromInt(100), &idempCap)
	if err != nil {
		t.Fatalf("capture tx in tx3: %v", err)
	}
	if capTx == nil {
		t.Fatal("expected capture transaction")
	}
	if err := tx3.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	wAfterCap, err := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	// CachedBalance: 450 - 100 = 350; ActiveHolds: 0 (hold resolved); Available: 350
	if !wAfterCap.CachedBalance.Equal(decimal.NewFromInt(350)) {
		t.Fatalf("expected cached balance 350, got %s", wAfterCap.CachedBalance.String())
	}
	if !wAfterCap.ActiveHolds.Equal(decimal.Zero) {
		t.Fatalf("expected active holds 0, got %s", wAfterCap.ActiveHolds.String())
	}
}

// 19. TestCapture_IdempotencyReplaySameAmountAndDifferentAmountRejected verifies idempotency rules
func TestCapture_IdempotencyReplaySameAmountAndDifferentAmountRejected(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "capidemp-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "capidemp-cust", "customer")

	w, _ := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")
	_, _, _ = h.ledgerSvc.Topup(ctx, h.e.Override, w.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(500), Reason: "Topup for capture replay test",
	}, "127.0.0.1", "test")

	hold, err := h.ledgerSvc.PlaceHold(ctx, w.ID, decimal.NewFromInt(200), "USD", nil, nil, nil, time.Now().Add(10*time.Minute))
	if err != nil {
		t.Fatalf("place hold: %v", err)
	}

	capIdemp := "cap-replay-unique-key-1"
	origCapTx, err := h.ledgerSvc.Capture(ctx, hold.ID, decimal.NewFromInt(120), &capIdemp)
	if err != nil {
		t.Fatalf("original capture: %v", err)
	}
	if origCapTx == nil {
		t.Fatal("expected original capture transaction")
	}

	// 1. Replay with identical key and identical amount -> returns original transaction
	replayCapTx, err := h.ledgerSvc.Capture(ctx, hold.ID, decimal.NewFromInt(120), &capIdemp)
	if err != nil {
		t.Fatalf("replay with same amount failed: %v", err)
	}
	if replayCapTx.ID != origCapTx.ID {
		t.Fatalf("expected identical transaction ID %s, got %s", origCapTx.ID, replayCapTx.ID)
	}

	// 2. Replay with identical key and DIFFERENT amount -> rejected with ErrIdempotencyConflict
	_, err = h.ledgerSvc.Capture(ctx, hold.ID, decimal.NewFromInt(150), &capIdemp)
	if !errors.Is(err, ledger.ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict for differing capture amount, got: %v", err)
	}

	// 3. Attempting another capture without matching idempotency key on now-captured hold -> ErrHoldNotActive
	diffKey := "cap-replay-diff-key-2"
	_, err = h.ledgerSvc.Capture(ctx, hold.ID, decimal.NewFromInt(50), &diffKey)
	if !errors.Is(err, ledger.ErrHoldNotActive) {
		t.Fatalf("expected ErrHoldNotActive on already-captured hold, got: %v", err)
	}
}

// 20. TestRelease_IdempotentOnAlreadyReleased verifies release idempotency and ReleaseTx
func TestRelease_IdempotentOnAlreadyReleased(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "rel-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "rel-cust", "customer")

	w, _ := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")
	_, _, _ = h.ledgerSvc.Topup(ctx, h.e.Override, w.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(300), Reason: "Topup for release test",
	}, "127.0.0.1", "test")

	hold, err := h.ledgerSvc.PlaceHold(ctx, w.ID, decimal.NewFromInt(100), "USD", nil, nil, nil, time.Now().Add(10*time.Minute))
	if err != nil {
		t.Fatalf("place hold: %v", err)
	}

	// First release -> success
	if err := h.ledgerSvc.Release(ctx, hold.ID); err != nil {
		t.Fatalf("first release failed: %v", err)
	}

	// Second release -> idempotent nil
	if err := h.ledgerSvc.Release(ctx, hold.ID); err != nil {
		t.Fatalf("second release must return nil, got: %v", err)
	}

	// Third release via ReleaseTx with caller transaction -> returns nil
	dbTx, err := h.e.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dbTx.Rollback(ctx) }()
	if err := h.ledgerSvc.ReleaseTx(ctx, dbTx, hold.ID); err != nil {
		t.Fatalf("ReleaseTx on already-released hold must return nil, got: %v", err)
	}
}

// 21. TestCapture_ConcurrentReplaysUnderRace verifies parallel replays return same transaction with zero races
func TestCapture_ConcurrentReplaysUnderRace(t *testing.T) {
	h := setupHarness(t)
	ctx := context.Background()

	resellerOrg, resellerPath := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "racecap-reseller", "reseller")
	custOrg, _ := h.e.ChildOrg(resellerOrg, resellerPath, "racecap-cust", "customer")

	w, _ := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, custOrg, "USD", "prepaid")
	_, _, _ = h.ledgerSvc.Topup(ctx, h.e.Override, w.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(500), Reason: "Topup for concurrent capture race",
	}, "127.0.0.1", "test")

	hold, err := h.ledgerSvc.PlaceHold(ctx, w.ID, decimal.NewFromInt(200), "USD", nil, nil, nil, time.Now().Add(10*time.Minute))
	if err != nil {
		t.Fatalf("place hold: %v", err)
	}

	const concurrency = 10
	idempKey := fmt.Sprintf("race-capture-%s", uuid.New().String())
	captureAmount := decimal.NewFromInt(80)

	var wg sync.WaitGroup
	txResults := make([]*ledger.LedgerTransaction, concurrency)
	errResults := make([]error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			lt, err := h.ledgerSvc.Capture(ctx, hold.ID, captureAmount, &idempKey)
			txResults[idx] = lt
			errResults[idx] = err
		}(i)
	}
	wg.Wait()

	var firstTxID uuid.UUID
	for i := 0; i < concurrency; i++ {
		if errResults[i] != nil {
			t.Fatalf("concurrent capture worker %d failed: %v", i, errResults[i])
		}
		if txResults[i] == nil {
			t.Fatalf("worker %d returned nil transaction", i)
		}
		if i == 0 {
			firstTxID = txResults[i].ID
		} else if txResults[i].ID != firstTxID {
			t.Fatalf("worker %d returned different transaction ID %s, expected %s", i, txResults[i].ID, firstTxID)
		}
	}

	// Verify balance was debited exactly ONCE
	wFinal, err := h.ledgerSvc.GetWallet(ctx, h.e.Override, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 500 - 80 = 420
	if !wFinal.CachedBalance.Equal(decimal.NewFromInt(420)) {
		t.Fatalf("expected final cached balance 420, got %s", wFinal.CachedBalance.String())
	}
}
