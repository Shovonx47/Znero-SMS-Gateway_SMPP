package messaging_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

func setCustomerBillingTerms(t *testing.T, mk *msgKit, orgID uuid.UUID, mode string, waitH int, noDlrPolicy, failedPolicy string) {
	t.Helper()
	_, err := mk.e.Pool.Exec(context.Background(), `
		INSERT INTO customer_profiles (
			organization_id, account_type, billing_currency, default_max_tps,
			billing_mode, dlr_wait_hours, no_dlr_policy, failed_after_accept_policy, version
		) VALUES ($1, 'prepaid', 'USD', 50, $2, $3, $4, $5, 1)
		ON CONFLICT (organization_id) DO UPDATE SET
			billing_mode = EXCLUDED.billing_mode,
			dlr_wait_hours = EXCLUDED.dlr_wait_hours,
			no_dlr_policy = EXCLUDED.no_dlr_policy,
			failed_after_accept_policy = EXCLUDED.failed_after_accept_policy,
			version = customer_profiles.version + 1`,
		orgID, mode, waitH, noDlrPolicy, failedPolicy,
	)
	if err != nil {
		t.Fatalf("update customer billing terms: %v", err)
	}
}

func assertMsgHoldBalances(t *testing.T, mk *msgKit, msgID uuid.UUID, createdAt time.Time, wantMsgStatus, wantHoldStatus, wantCachedBal string) {
	t.Helper()
	ctx := context.Background()
	msg, _, err := mk.repo.GetMessage(ctx, msgID, createdAt)
	if err != nil {
		t.Fatalf("get message %s: %v", msgID, err)
	}
	if msg.Status != wantMsgStatus {
		t.Fatalf("message %s status = %q, want %q", msgID, msg.Status, wantMsgStatus)
	}
	if msg.HoldID == nil {
		t.Fatalf("message %s hold_id is nil", msgID)
	}
	var holdStatus string
	if err := mk.e.Pool.QueryRow(ctx, `SELECT status FROM holds WHERE id = $1`, *msg.HoldID).Scan(&holdStatus); err != nil {
		t.Fatalf("query hold %s: %v", *msg.HoldID, err)
	}
	if holdStatus != wantHoldStatus {
		t.Fatalf("hold %s status = %q, want %q", *msg.HoldID, holdStatus, wantHoldStatus)
	}
	w, err := mk.ledgerSvc.GetWalletByOrgIDInternal(ctx, msg.CustomerOrgID)
	if err != nil {
		t.Fatalf("get wallet: %v", err)
	}
	wantDec := decimal.RequireFromString(wantCachedBal)
	if !w.CachedBalance.Equal(wantDec) {
		t.Fatalf("wallet cached_balance = %s, want %s", w.CachedBalance.String(), wantDec.String())
	}
}

func TestPerCustomerBilling_OnDelivery_DeliveredAndFailed(t *testing.T) {
	mk := newMsgKit(t)
	ctx := context.Background()

	// Wallet starts at $100.00 (seeded by newMsgKit). Price per SMS = $0.05.
	setCustomerBillingTerms(t, mk, mk.custOrgID, "on_delivery", 12, "retain_until_resolution", "keep_charge")
	dlrProc := messaging.NewDLRProcessor(mk.repo, mk.ledgerSvc, "on_delivery", nil)

	// 1. Accepted -> submitted (hold STILL active until DELIVRD) -> DELIVRD captures hold ($99.95)
	mk.fakeSender.ConfigureDestination("+12025550111", messaging.FakeSenderOutcomeConfig{
		Status:            messaging.SendStatusAccepted,
		ProviderMessageID: "od-deliv-1",
	})
	res1, err := mk.svc.Submit(ctx, mk.custUser, messaging.SubmitRequest{
		Destination: "+12025550111",
		SenderID:    "ZNERO",
		Text:        "on_delivery deliver test",
	})
	if err != nil {
		t.Fatalf("submit 1: %v", err)
	}
	mk.dispatcher.ProcessBatch(ctx)
	assertMsgHoldBalances(t, mk, res1.MessageID, res1.CreatedAt, messaging.StatusSubmitted, "active", "100.00")

	if err := dlrProc.ProcessParsedDLR(ctx, mk.provConnID1, &messaging.DLRReceipt{
		ProviderMessageID: "od-deliv-1",
		Stat:              "DELIVRD",
		ErrCode:           "000",
	}); err != nil {
		t.Fatalf("dlr 1: %v", err)
	}
	assertMsgHoldBalances(t, mk, res1.MessageID, res1.CreatedAt, messaging.StatusDelivered, "captured", "99.95")

	// 2. Second SMS: Accepted -> DLR UNDELIV releases hold (balance remains $99.95)
	mk.fakeSender.ConfigureDestination("+12025550112", messaging.FakeSenderOutcomeConfig{
		Status:            messaging.SendStatusAccepted,
		ProviderMessageID: "od-fail-2",
	})
	res2, err := mk.svc.Submit(ctx, mk.custUser, messaging.SubmitRequest{
		Destination: "+12025550112",
		SenderID:    "ZNERO",
		Text:        "on_delivery fail test",
	})
	if err != nil {
		t.Fatalf("submit 2: %v", err)
	}
	mk.dispatcher.ProcessBatch(ctx)
	if err := dlrProc.ProcessParsedDLR(ctx, mk.provConnID1, &messaging.DLRReceipt{
		ProviderMessageID: "od-fail-2",
		Stat:              "UNDELIV",
		ErrCode:           "034",
	}); err != nil {
		t.Fatalf("dlr 2: %v", err)
	}
	assertMsgHoldBalances(t, mk, res2.MessageID, res2.CreatedAt, messaging.StatusUndelivered, "released", "99.95")
}

func TestPerCustomerBilling_OnProviderAccept_KeepChargeAndRefund(t *testing.T) {
	mk := newMsgKit(t)
	ctx := context.Background()
	dlrProc := messaging.NewDLRProcessor(mk.repo, mk.ledgerSvc, "on_delivery", nil)

	// Part A: failed_after_accept_policy = 'keep_charge'
	setCustomerBillingTerms(t, mk, mk.custOrgID, "on_provider_accept", 24, "retain_until_resolution", "keep_charge")
	mk.fakeSender.ConfigureDestination("+12025550121", messaging.FakeSenderOutcomeConfig{
		Status:            messaging.SendStatusAccepted,
		ProviderMessageID: "opa-keep-1",
	})
	resKeep, err := mk.svc.Submit(ctx, mk.custUser, messaging.SubmitRequest{
		Destination: "+12025550121",
		SenderID:    "ZNERO",
		Text:        "on_provider_accept keep test",
	})
	if err != nil {
		t.Fatalf("submit keep: %v", err)
	}
	mk.dispatcher.ProcessBatch(ctx)
	// Captured IMMEDIATELY on accept!
	assertMsgHoldBalances(t, mk, resKeep.MessageID, resKeep.CreatedAt, messaging.StatusSubmitted, "captured", "99.95")

	// Later DLR arrives saying UNDELIV -> keep_charge preserves captured charge
	if err := dlrProc.ProcessParsedDLR(ctx, mk.provConnID1, &messaging.DLRReceipt{
		ProviderMessageID: "opa-keep-1",
		Stat:              "UNDELIV",
		ErrCode:           "034",
	}); err != nil {
		t.Fatalf("dlr keep: %v", err)
	}
	assertMsgHoldBalances(t, mk, resKeep.MessageID, resKeep.CreatedAt, messaging.StatusUndelivered, "captured", "99.95")

	// Part B: Switch customer terms to failed_after_accept_policy = 'refund'
	setCustomerBillingTerms(t, mk, mk.custOrgID, "on_provider_accept", 24, "retain_until_resolution", "refund")
	mk.fakeSender.ConfigureDestination("+12025550122", messaging.FakeSenderOutcomeConfig{
		Status:            messaging.SendStatusAccepted,
		ProviderMessageID: "opa-refund-2",
	})
	resRefund, err := mk.svc.Submit(ctx, mk.custUser, messaging.SubmitRequest{
		Destination: "+12025550122",
		SenderID:    "ZNERO",
		Text:        "on_provider_accept refund test",
	})
	if err != nil {
		t.Fatalf("submit refund: %v", err)
	}
	mk.dispatcher.ProcessBatch(ctx)
	// Captured immediately ($99.95 - $0.05 = $99.90)
	assertMsgHoldBalances(t, mk, resRefund.MessageID, resRefund.CreatedAt, messaging.StatusSubmitted, "captured", "99.90")

	// Later DLR arrives saying UNDELIV -> RefundCaptureTx credits wallet back to $99.95 and hold becomes 'refunded'
	if err := dlrProc.ProcessParsedDLR(ctx, mk.provConnID1, &messaging.DLRReceipt{
		ProviderMessageID: "opa-refund-2",
		Stat:              "UNDELIV",
		ErrCode:           "034",
	}); err != nil {
		t.Fatalf("dlr refund: %v", err)
	}
	assertMsgHoldBalances(t, mk, resRefund.MessageID, resRefund.CreatedAt, messaging.StatusUndelivered, "refunded", "99.95")

	// Also verify ledger reconciliation matches cleanly (zero drift)
	rep, err := mk.ledgerSvc.ReconcileBalances(ctx, mk.e.Override, false)
	if err != nil {
		t.Fatalf("ledger reconcile: %v", err)
	}
	for _, r := range rep {
		if r.WalletID == mk.walletID && !r.Difference.IsZero() {
			t.Fatalf("ledger wallet discrepancy after refund: %s", r.Difference.String())
		}
	}
}

func TestPerCustomerBilling_NoDLRPolicies_DeterministicClockReaper(t *testing.T) {
	mk := newMsgKit(t)
	ctx := context.Background()
	clk := testkit.NewTestClock(time.Now().UTC())
	dlrProc := messaging.NewDLRProcessor(mk.repo, mk.ledgerSvc, "on_delivery", nil)
	reaper := messaging.NewReaper(mk.e.Pool, mk.repo, mk.ledgerSvc, dlrProc, messaging.ReaperConfig{
		UnknownResolutionWindow: 24 * time.Hour,
		BatchLimit:              100,
	}, nil)
	reaper.SetClock(clk.Now)

	// 1. Policy retain_until_resolution (wait 2h)
	setCustomerBillingTerms(t, mk, mk.custOrgID, "on_delivery", 2, "retain_until_resolution", "keep_charge")
	mk.fakeSender.ConfigureDestination("+12025550131", messaging.FakeSenderOutcomeConfig{
		Status: messaging.SendStatusAccepted, ProviderMessageID: "nodlr-ret-1",
	})
	rRet, err := mk.svc.Submit(ctx, mk.custUser, messaging.SubmitRequest{Destination: "+12025550131", SenderID: "ZNERO", Text: "retain"})
	if err != nil {
		t.Fatalf("submit retain: %v", err)
	}

	// 2. Policy immediate_release (wait 2h)
	setCustomerBillingTerms(t, mk, mk.custOrgID, "on_delivery", 2, "immediate_release", "keep_charge")
	mk.fakeSender.ConfigureDestination("+12025550132", messaging.FakeSenderOutcomeConfig{
		Status: messaging.SendStatusAccepted, ProviderMessageID: "nodlr-imm-2",
	})
	rImm, err := mk.svc.Submit(ctx, mk.custUser, messaging.SubmitRequest{Destination: "+12025550132", SenderID: "ZNERO", Text: "immediate"})
	if err != nil {
		t.Fatalf("submit imm: %v", err)
	}

	// 3. Policy capture_at_ttl (wait 2h)
	setCustomerBillingTerms(t, mk, mk.custOrgID, "on_delivery", 2, "capture_at_ttl", "keep_charge")
	mk.fakeSender.ConfigureDestination("+12025550133", messaging.FakeSenderOutcomeConfig{
		Status: messaging.SendStatusAccepted, ProviderMessageID: "nodlr-cap-3",
	})
	rCap, err := mk.svc.Submit(ctx, mk.custUser, messaging.SubmitRequest{Destination: "+12025550133", SenderID: "ZNERO", Text: "capture_at_ttl"})
	if err != nil {
		t.Fatalf("submit cap: %v", err)
	}
	mk.dispatcher.ProcessBatch(ctx)

	// Sweep before 2h elapses -> none should be touched
	reaper.RunSweep(ctx)
	for _, item := range []*messaging.SubmitResponse{rRet, rImm, rCap} {
		msg, _, _ := mk.repo.GetMessage(ctx, item.MessageID, item.CreatedAt)
		if msg.Status != messaging.StatusSubmitted {
			t.Fatalf("premature reap for %s: status=%s", item.MessageID, msg.Status)
		}
	}

	// Advance clock by 3 hours -> all 3 enter their snapshotted no-DLR policy outcomes!
	clk.Advance(3 * time.Hour)
	reaper.RunSweep(ctx)

	// - retain_until_resolution -> unknown + active hold
	// - immediate_release -> expired + released hold
	// - capture_at_ttl -> expired + captured hold ($99.95)
	assertMsgHoldBalances(t, mk, rImm.MessageID, rImm.CreatedAt, messaging.StatusExpired, "released", "99.95")
	assertMsgHoldBalances(t, mk, rCap.MessageID, rCap.CreatedAt, messaging.StatusExpired, "captured", "99.95")
	assertMsgHoldBalances(t, mk, rRet.MessageID, rRet.CreatedAt, messaging.StatusUnknown, "active", "99.95")

	// Verify settlement exceptions logged for retain_until_resolution exposure and capture_at_ttl
	var retExCount, capExCount int
	_ = mk.e.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM settlement_exceptions WHERE message_id = $1 AND exception_type = 'no_dlr_retained_exposure'`, rRet.MessageID).Scan(&retExCount)
	_ = mk.e.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM settlement_exceptions WHERE message_id = $1 AND exception_type = 'capture_at_ttl'`, rCap.MessageID).Scan(&capExCount)
	if retExCount == 0 || capExCount == 0 {
		t.Fatalf("expected settlement exceptions recorded: retain=%d capture_at_ttl=%d", retExCount, capExCount)
	}

	// Advance past UnknownResolutionWindow (24h+) -> unknown resolves to failed & hold is released
	clk.Advance(25 * time.Hour)
	reaper.RunSweep(ctx)
	assertMsgHoldBalances(t, mk, rRet.MessageID, rRet.CreatedAt, messaging.StatusFailed, "released", "99.95")
}

func TestPerCustomerBilling_SnapshotImmutability_MidFlightProfileChange(t *testing.T) {
	mk := newMsgKit(t)
	ctx := context.Background()
	clk := testkit.NewTestClock(time.Now().UTC())
	dlrProc := messaging.NewDLRProcessor(mk.repo, mk.ledgerSvc, "on_delivery", nil)
	reaper := messaging.NewReaper(mk.e.Pool, mk.repo, mk.ledgerSvc, dlrProc, messaging.ReaperConfig{
		UnknownResolutionWindow: 24 * time.Hour,
		BatchLimit:              100,
	}, nil)
	reaper.SetClock(clk.Now)

	// Start as on_delivery / 24h wait / immediate_release
	setCustomerBillingTerms(t, mk, mk.custOrgID, "on_delivery", 24, "immediate_release", "keep_charge")
	mk.fakeSender.ConfigureDestination("+12025550141", messaging.FakeSenderOutcomeConfig{
		Status: messaging.SendStatusAccepted, ProviderMessageID: "mid-1",
	})
	inFlightMsg, err := mk.svc.Submit(ctx, mk.custUser, messaging.SubmitRequest{
		Destination: "+12025550141",
		SenderID:    "ZNERO",
		Text:        "mid-flight snapshot test",
	})
	if err != nil {
		t.Fatalf("submit 1: %v", err)
	}

	// Now change customer's profile mid-flight BEFORE dispatch & BEFORE reaper!
	setCustomerBillingTerms(t, mk, mk.custOrgID, "on_provider_accept", 2, "capture_at_ttl", "keep_charge")

	// Submit second message under the new profile
	mk.fakeSender.ConfigureDestination("+12025550142", messaging.FakeSenderOutcomeConfig{
		Status: messaging.SendStatusAccepted, ProviderMessageID: "mid-2",
	})
	newMsg, err := mk.svc.Submit(ctx, mk.custUser, messaging.SubmitRequest{
		Destination: "+12025550142",
		SenderID:    "ZNERO",
		Text:        "post-change message",
	})
	if err != nil {
		t.Fatalf("submit 2: %v", err)
	}

	// Dispatch both:
	// - inFlightMsg MUST honor on_delivery snapshot (hold stays active!)
	// - newMsg MUST honor on_provider_accept snapshot (hold captured immediately -> $99.95!)
	mk.dispatcher.ProcessBatch(ctx)
	assertMsgHoldBalances(t, mk, inFlightMsg.MessageID, inFlightMsg.CreatedAt, messaging.StatusSubmitted, "active", "99.95")
	assertMsgHoldBalances(t, mk, newMsg.MessageID, newMsg.CreatedAt, messaging.StatusSubmitted, "captured", "99.95")

	// Advance +3h (past new profile's 2h wait, BEFORE inFlightMsg's 24h wait):
	// inFlightMsg MUST remain submitted & active!
	clk.Advance(3 * time.Hour)
	reaper.RunSweep(ctx)
	assertMsgHoldBalances(t, mk, inFlightMsg.MessageID, inFlightMsg.CreatedAt, messaging.StatusSubmitted, "active", "99.95")

	// Advance +22h (total +25h, past inFlightMsg's 24h snapshot wait):
	// inFlightMsg MUST honor its immediate_release snapshot (NOT new profile's capture_at_ttl)!
	clk.Advance(22 * time.Hour)
	reaper.RunSweep(ctx)
	assertMsgHoldBalances(t, mk, inFlightMsg.MessageID, inFlightMsg.CreatedAt, messaging.StatusExpired, "released", "99.95")

	// Check customer-specific reconciliation: zero discrepancies for mk.custOrgID
	reconcileSvc := messaging.NewReconciliationService(mk.e.Pool, mk.repo, mk.ledgerSvc, nil)
	report, err := reconcileSvc.RunReconciliation(ctx, &mk.e.Override.ActorUserID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	for _, disc := range report.Discrepancies {
		if cid, ok := disc["customer"].(string); ok && strings.EqualFold(cid, mk.custOrgID.String()) {
			t.Fatalf("unexpected discrepancy for test customer %s: %+v", mk.custOrgID, disc)
		}
	}
	_ = tenant.Context{}
	_ = ledger.Wallet{}
}

func TestReaper_RetryUnmatchedHTTPJSONDLR(t *testing.T) {
	mk := newMsgKit(t)
	ctx := context.Background()
	clk := testkit.NewTestClock(time.Now().UTC())
	dlrProc := messaging.NewDLRProcessor(mk.repo, mk.ledgerSvc, "on_delivery", nil)
	reaper := messaging.NewReaper(mk.e.Pool, mk.repo, mk.ledgerSvc, dlrProc, messaging.ReaperConfig{
		UnknownResolutionWindow: 24 * time.Hour,
		UnmatchedDLRRetryWindow: 10 * time.Minute,
		BatchLimit:              100,
	}, nil)
	reaper.SetClock(clk.Now)

	// Simulate an unmatched HTTP JSON callback stored before provider_lookups committed
	mk.fakeSender.ConfigureDestination("+12025550198", messaging.FakeSenderOutcomeConfig{
		Status:            messaging.SendStatusAccepted,
		ProviderMessageID: "http-json-unmatched-1",
	})
	res, err := mk.svc.Submit(ctx, mk.custUser, messaging.SubmitRequest{
		Destination: "+12025550198",
		SenderID:    "ZNERO",
		Text:        "unmatched http json callback test",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	rawJSON := `{"error_code":"000","provider_message_id":"http-json-unmatched-1","status":"DELIVRD"}`
	errCode := "000"
	if err := mk.repo.InsertUnmatchedDLR(ctx, nil, messaging.UnmatchedDLR{
		ConnectionID:      &mk.provConnID1,
		ProviderMessageID: "http-json-unmatched-1",
		Status:            "DELIVRD",
		ErrorCode:         &errCode,
		RawReceipt:        &rawJSON,
		MatchStatus:       "unmatched",
	}); err != nil {
		t.Fatalf("insert unmatched dlr: %v", err)
	}

	// Dispatcher commits submitted + provider_lookup; Reaper retries unmatched HTTP JSON callback
	mk.dispatcher.ProcessBatch(ctx)
	reaper.RunSweep(ctx)

	assertMsgHoldBalances(t, mk, res.MessageID, res.CreatedAt, messaging.StatusDelivered, "captured", "99.95")
}
