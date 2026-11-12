package e2e_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/customer"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/smpp/mocksmsc"
	"github.com/znero-international/znero/backend/internal/smpp/pdu"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/pkg/kafkaclient"
)

func TestE2E_FailureMatrix_AllEdgeCases(t *testing.T) {
	h := newE2EHarness(t, true)
	ctx := context.Background()

	httpThenSmppPlanID := h.createPlanForConns([]uuid.UUID{h.connHTTPID, h.connSMPPBackupID}, "HTTP_FAILOVER")

	cFailoverSMPP := h.provisionCustomer("fail_smpp", "on_delivery", 24, "retain_until_resolution", "keep_charge", h.failoverPlanID)
	cFailoverHTTP := h.provisionCustomer("fail_http", "on_delivery", 24, "retain_until_resolution", "keep_charge", httpThenSmppPlanID)
	cNoDlrRetain := h.provisionCustomer("nodlr_ret", "on_delivery", 6, "retain_until_resolution", "keep_charge", h.smppPlanID)
	cNoDlrRelease := h.provisionCustomer("nodlr_rel", "on_delivery", 6, "immediate_release", "keep_charge", h.smppPlanID)
	cNoDlrCapture := h.provisionCustomer("nodlr_cap", "on_delivery", 6, "capture_at_ttl", "keep_charge", h.smppPlanID)
	cAcceptKeep := h.provisionCustomer("acc_keep", "on_provider_accept", 24, "retain_until_resolution", "keep_charge", h.smppPlanID)
	cAcceptRefund := h.provisionCustomer("acc_ref", "on_provider_accept", 24, "retain_until_resolution", "refund", h.smppPlanID)
	cLateDeliv := h.provisionCustomer("late_deliv", "on_delivery", 6, "retain_until_resolution", "keep_charge", h.smppPlanID)
	cDupDLR := h.provisionCustomer("dup_dlr", "on_delivery", 24, "retain_until_resolution", "keep_charge", h.smppPlanID)
	cEarlyDLR := h.provisionCustomer("early_dlr", "on_delivery", 24, "retain_until_resolution", "keep_charge", h.smppPlanID)
	cSnapImm := h.provisionCustomer("snap_imm", "on_delivery", 24, "retain_until_resolution", "keep_charge", h.httpPlanID)

	// 1a. Permanent reject on SMPP (0x0B invalid destination) EVEN WITH backup route -> instant release, ZERO backup attempts
	bkpBefore := len(h.smscBackup.GetSubmittedMessages())
	h.smsc.SetInjectedError(pdu.StatusRInvDstAdr)
	mPermSMPP := h.sendHTTP(cFailoverSMPP, "+12025550201", "Perm reject SMPP")
	h.dispatcher.ProcessBatch(ctx)
	h.assertMsgState(mPermSMPP, cFailoverSMPP, messaging.StatusFailed, "released", "100.00", "0.00")
	if gotBkp := len(h.smscBackup.GetSubmittedMessages()); gotBkp != bkpBefore {
		t.Fatalf("permanent SMPP reject must NOT failover: backup submits grew %d -> %d", bkpBefore, gotBkp)
	}
	h.smsc.SetInjectedError(0)

	// 1b. Permanent reject on HTTP (400 Bad Request) EVEN WITH backup route -> instant release, ZERO backup attempts
	h.httpNextAction.Store(httpActionFatal400)
	mPermHTTP := h.sendHTTP(cFailoverHTTP, "+12025550202", "Perm reject HTTP")
	h.dispatcher.ProcessBatch(ctx)
	h.assertMsgState(mPermHTTP, cFailoverHTTP, messaging.StatusFailed, "released", "100.00", "0.00")
	if gotBkp := len(h.smscBackup.GetSubmittedMessages()); gotBkp != bkpBefore {
		t.Fatalf("permanent HTTP 400 reject must NOT failover: backup submits grew %d -> %d", bkpBefore, gotBkp)
	}
	h.httpNextAction.Store(httpActionOK)

	// 2a. Temporary throttle on SMPP (0x58) -> failover to backup SMSC and succeed with single charge!
	h.smsc.SetInjectedError(pdu.StatusRThrottled)
	h.smscBackup.SetDLRMode(mocksmsc.DLRModeImmediate)
	mThrotSMPP := h.sendHTTP(cFailoverSMPP, "+12025550203", "SMPP throttle failover")
	h.dispatcher.ProcessBatch(ctx) // primary rejects 0x58 -> re-queues backup
	h.dispatcher.ProcessBatch(ctx) // backup submits & sends DELIVRD
	h.waitMessageStatus(mThrotSMPP, messaging.StatusDelivered, "SMPP failover DELIVRD")
	h.assertMsgState(mThrotSMPP, cFailoverSMPP, messaging.StatusDelivered, "captured", "99.95", "0.00")
	h.smsc.SetInjectedError(0)

	// 2b. Temporary throttle on HTTP (429) -> failover to backup SMSC and succeed cleanly!
	h.httpNextAction.Store(httpActionRetryable429)
	mThrotHTTP := h.sendHTTP(cFailoverHTTP, "+12025550204", "HTTP 429 failover")
	h.dispatcher.ProcessBatch(ctx) // HTTP 429 -> re-queues backup
	h.dispatcher.ProcessBatch(ctx) // backup submits & sends DELIVRD
	h.waitMessageStatus(mThrotHTTP, messaging.StatusDelivered, "HTTP 429 failover DELIVRD")
	h.assertMsgState(mThrotHTTP, cFailoverHTTP, messaging.StatusDelivered, "captured", "99.95", "0.00")
	h.httpNextAction.Store(httpActionOK)

	// 3. All 3 no_dlr_policy rules under TestClock + Reaper + settlement_exceptions verification
	h.smsc.SetDLRMode(mocksmsc.DLRModeNone)
	mRet := h.sendHTTP(cNoDlrRetain, "+12025550205", "NoDLR retain")
	mRel := h.sendHTTP(cNoDlrRelease, "+12025550206", "NoDLR release")
	mCap := h.sendHTTP(cNoDlrCapture, "+12025550207", "NoDLR capture")
	mLate := h.sendHTTP(cLateDeliv, "+12025550208", "UNKNOWN then late DELIVRD")
	h.dispatcher.ProcessBatch(ctx)

	h.clk.Advance(7 * time.Hour) // Past 6h dlr_wait_hours
	h.reaper.RunSweep(ctx)
	h.assertMsgState(mRel, cNoDlrRelease, messaging.StatusExpired, "released", "100.00", "0.00")
	h.assertMsgState(mCap, cNoDlrCapture, messaging.StatusExpired, "captured", "99.95", "0.00")
	h.assertMsgState(mRet, cNoDlrRetain, messaging.StatusUnknown, "active", "99.95", "0.05")
	h.assertMsgState(mLate, cLateDeliv, messaging.StatusUnknown, "active", "99.95", "0.05")

	var capExcCount int
	_ = h.e.Pool.QueryRow(ctx, `SELECT count(*) FROM settlement_exceptions WHERE message_id = $1 AND exception_type = 'capture_at_ttl'`, mCap).Scan(&capExcCount)
	if capExcCount != 1 {
		t.Fatalf("expected 1 capture_at_ttl settlement_exception row for %s, got %d", mCap, capExcCount)
	}

	// 5 (tested before advancing 13h): UNKNOWN then late DELIVRD resolves cleanly!
	var mLateProvID string
	_ = h.e.Pool.QueryRow(ctx, `SELECT provider_message_id FROM messages WHERE id = $1`, mLate).Scan(&mLateProvID)
	if err := h.dlrProc.ProcessParsedDLR(ctx, h.connSMPPID, &messaging.DLRReceipt{
		ProviderMessageID: mLateProvID, Stat: "DELIVRD", ErrCode: "000", RawReceipt: "stat:DELIVRD err:000",
	}); err != nil {
		t.Fatalf("late DELIVRD after unknown: %v", err)
	}
	h.assertMsgState(mLate, cLateDeliv, messaging.StatusDelivered, "captured", "99.95", "0.00")

	// Advance past UnknownResolutionWindow -> retain_until_resolution transitions to failed + released
	h.clk.Advance(13 * time.Hour)
	h.reaper.RunSweep(ctx)
	h.assertMsgState(mRet, cNoDlrRetain, messaging.StatusFailed, "released", "100.00", "0.00")

	// 4. FAILED after accept with BOTH keep_charge and refund
	mKeep := h.sendHTTP(cAcceptKeep, "+12025550209", "Accept failed keep_charge")
	mRef := h.sendHTTP(cAcceptRefund, "+12025550210", "Accept failed refund")
	h.dispatcher.ProcessBatch(ctx)
	h.assertMsgState(mKeep, cAcceptKeep, messaging.StatusSubmitted, "captured", "99.95", "0.00")
	h.assertMsgState(mRef, cAcceptRefund, messaging.StatusSubmitted, "captured", "99.95", "0.00")
	var mKeepProvID, mRefProvID string
	_ = h.e.Pool.QueryRow(ctx, `SELECT provider_message_id FROM messages WHERE id = $1`, mKeep).Scan(&mKeepProvID)
	_ = h.e.Pool.QueryRow(ctx, `SELECT provider_message_id FROM messages WHERE id = $1`, mRef).Scan(&mRefProvID)
	if err := h.dlrProc.ProcessParsedDLR(ctx, h.connSMPPID, &messaging.DLRReceipt{
		ProviderMessageID: mKeepProvID, Stat: "REJECTD", ErrCode: "011", RawReceipt: "stat:REJECTD",
	}); err != nil {
		t.Fatalf("keep_charge FAILED DLR: %v", err)
	}
	if err := h.dlrProc.ProcessParsedDLR(ctx, h.connSMPPID, &messaging.DLRReceipt{
		ProviderMessageID: mRefProvID, Stat: "REJECTD", ErrCode: "011", RawReceipt: "stat:REJECTD",
	}); err != nil {
		t.Fatalf("refund FAILED DLR: %v", err)
	}
	h.assertMsgState(mKeep, cAcceptKeep, messaging.StatusFailed, "captured", "99.95", "0.00")
	h.assertMsgState(mRef, cAcceptRefund, messaging.StatusFailed, "refunded", "100.00", "0.00")

	// 6. Duplicate DLR delivered 3x -> idempotent single capture & single charge
	mDup := h.sendHTTP(cDupDLR, "+12025550211", "Duplicate DLR test")
	h.dispatcher.ProcessBatch(ctx)
	var mDupProvID string
	_ = h.e.Pool.QueryRow(ctx, `SELECT provider_message_id FROM messages WHERE id = $1`, mDup).Scan(&mDupProvID)
	for i := 0; i < 3; i++ {
		if err := h.dlrProc.ProcessParsedDLR(ctx, h.connSMPPID, &messaging.DLRReceipt{
			ProviderMessageID: mDupProvID, Stat: "DELIVRD", ErrCode: "000", RawReceipt: "id:" + mDupProvID + " stat:DELIVRD",
		}); err != nil {
			t.Fatalf("duplicate DLR #%d failed: %v", i+1, err)
		}
	}
	h.assertMsgState(mDup, cDupDLR, messaging.StatusDelivered, "captured", "99.95", "0.00")

	// 7. DLR BEFORE submit_sm_resp (DLRModeBeforeSubmitResp) + Reaper retry proving BUG-018 fix
	h.smsc.SetDLRMode(mocksmsc.DLRModeBeforeSubmitResp)
	mEarly := h.sendHTTP(cEarlyDLR, "+12025550212", "Early DLR before submit_sm_resp")
	h.dispatcher.ProcessBatch(ctx)
	eventually(t, 3*time.Second, "unmatched DLR buffered before submit_sm_resp", func() bool {
		var cnt int
		_ = h.e.Pool.QueryRow(ctx, `SELECT count(*) FROM unmatched_dlrs WHERE connection_id = $1`, h.connSMPPID).Scan(&cnt)
		return cnt >= 1
	})
	// Advance clock slightly (< UnmatchedDLRRetryWindow of 5m) and run Reaper -> BUG-018 fix matches it!
	h.clk.Advance(30 * time.Second)
	h.reaper.RunSweep(ctx)
	eventually(t, 3*time.Second, "early DLR resolved by reaper", func() bool {
		var st string
		_ = h.e.Pool.QueryRow(ctx, `SELECT status FROM messages WHERE id = $1`, mEarly).Scan(&st)
		return st == messaging.StatusDelivered
	})
	h.assertMsgState(mEarly, cEarlyDLR, messaging.StatusDelivered, "captured", "99.95", "0.00")
	h.smsc.SetDLRMode(mocksmsc.DLRModeNone)

	// 8. Unknown provider message ID -> buffered in unmatched_dlrs then flagged_unmatched after retry window
	unknownProvID := "nonexistent-prov-id-" + testkitRandSuffix()
	if err := h.dlrProc.ProcessParsedDLR(ctx, h.connSMPPID, &messaging.DLRReceipt{
		ProviderMessageID: unknownProvID, Stat: "DELIVRD", ErrCode: "000", RawReceipt: "id:" + unknownProvID + " stat:DELIVRD",
	}); err != nil {
		t.Fatalf("process unknown prov msg id DLR: %v", err)
	}
	h.clk.Advance(10 * time.Minute) // Exceeds 5m UnmatchedDLRRetryWindow
	h.reaper.RunSweep(ctx)
	var unmStatus string
	_ = h.e.Pool.QueryRow(ctx, `SELECT match_status FROM unmatched_dlrs WHERE provider_message_id = $1`, unknownProvID).Scan(&unmStatus)
	if unmStatus != "flagged_unmatched" {
		t.Fatalf("expected unknown prov msg ID to become flagged_unmatched, got %q", unmStatus)
	}

	// 9. Ambiguous network drop/timeout after sending bytes EVEN WITH failover backup -> NEVER auto-resends!
	bkpAfter := len(h.smscBackup.GetSubmittedMessages())
	h.smsc.SetInjectedError(mocksmsc.InjectedDropConnection)
	mAmbSMPP := h.sendHTTP(cFailoverSMPP, "+12025550213", "Ambiguous SMPP drop")
	h.dispatcher.ProcessBatch(ctx)
	h.assertMsgState(mAmbSMPP, cFailoverSMPP, messaging.StatusUnknown, "active", "99.90", "0.05")
	if gotBkp := len(h.smscBackup.GetSubmittedMessages()); gotBkp != bkpAfter {
		t.Fatalf("AMBIGUOUS SMPP drop MUST NOT failover/resend: backup submits grew %d -> %d", bkpAfter, gotBkp)
	}
	h.smsc.SetInjectedError(0)

	h.httpNextAction.Store(httpActionSlowTimeout)
	mAmbHTTP := h.sendHTTP(cFailoverHTTP, "+12025550214", "Ambiguous HTTP timeout")
	h.dispatcher.ProcessBatch(ctx)
	h.assertMsgState(mAmbHTTP, cFailoverHTTP, messaging.StatusUnknown, "active", "99.90", "0.05")
	if gotBkp := len(h.smscBackup.GetSubmittedMessages()); gotBkp != bkpAfter {
		t.Fatalf("AMBIGUOUS HTTP timeout MUST NOT failover/resend: backup submits grew %d -> %d", bkpAfter, gotBkp)
	}
	h.httpNextAction.Store(httpActionOK)

	// 10. Mid-flight billing terms change does NOT alter snapshotted in-flight message
	mSnapOld := h.sendHTTP(cSnapImm, "+12025550215", "Inflight on_delivery")
	var profSnapID uuid.UUID
	_ = h.e.Pool.QueryRow(ctx, `SELECT id FROM customer_profiles WHERE organization_id = $1`, cSnapImm.orgID).Scan(&profSnapID)
	newMode := "on_provider_accept"
	newWait := 2
	if _, err := h.custSvc.UpdateProfile(ctx, h.e.Override, profSnapID, customer.UpdateProfileRequest{
		BillingMode:  &newMode,
		DLRWaitHours: &newWait,
		Reason:       "midflight deal change",
	}, "127.0.0.1", "e2e"); err != nil {
		t.Fatalf("update profile midflight: %v", err)
	}
	mSnapNew := h.sendHTTP(cSnapImm, "+12025550216", "New on_provider_accept")
	h.dispatcher.ProcessBatch(ctx)
	h.assertMsgState(mSnapOld, cSnapImm, messaging.StatusSubmitted, "active", "99.90", "0.05")
	h.assertMsgState(mSnapNew, cSnapImm, messaging.StatusSubmitted, "captured", "99.90", "0.05")
	if err := h.poller.PollOnce(ctx, true); err != nil {
		t.Fatalf("settle poll mSnapOld: %v", err)
	}
	h.assertMsgState(mSnapOld, cSnapImm, messaging.StatusDelivered, "captured", "99.90", "0.00")

	// Resolve ambiguous holds cleanly via TestClock window expiration and assert full ledger + reconciliation
	h.clk.Advance(13 * time.Hour)
	h.reaper.RunSweep(ctx)
	for _, cust := range []*e2eCustomer{cFailoverSMPP, cFailoverHTTP, cNoDlrRetain, cNoDlrRelease, cNoDlrCapture, cAcceptKeep, cAcceptRefund, cLateDeliv, cDupDLR, cEarlyDLR, cSnapImm} {
		h.assertLedgerBalancedForCustomer(cust)
	}
	h.assertNoSecretLeaks(cFailoverSMPP.apiKey, cEarlyDLR.apiKey)
	h.assertReconcileZero()
}

func TestE2E_Chaos_Concurrency_DBTx_Kafka_HTTPGarbage(t *testing.T) {
	h := newE2EHarness(t, true)
	ctx := context.Background()

	cBurst := h.provisionCustomer("chaos_burst", "on_provider_accept", 24, "retain_until_resolution", "keep_charge", h.httpPlanID)
	cChaos := h.provisionCustomer("chaos_edge", "on_delivery", 24, "retain_until_resolution", "keep_charge", h.httpPlanID)

	// 1. 3 concurrent Dispatcher workers processing a 20-message outbox backlog -> zero duplicates or losses!
	sendBefore := h.httpSendCount.Load()
	const burstCount = 20
	for i := 0; i < burstCount; i++ {
		h.sendHTTP(cBurst, fmt.Sprintf("+1202555%04d", 300+i), fmt.Sprintf("chaos-burst-%d", i))
	}
	var dispWG sync.WaitGroup
	for w := 0; w < 3; w++ {
		dispWG.Add(1)
		go func() {
			defer dispWG.Done()
			worker := messaging.NewDispatcher(h.e.Pool, h.msgRepo, h.routingEng, h.ledgerSvc, h.compSender, nil)
			for i := 0; i < 12; i++ {
				if worker.ProcessBatch(ctx) == 0 {
					time.Sleep(15 * time.Millisecond)
				}
			}
		}()
	}
	dispWG.Wait()
	if gotSent := h.httpSendCount.Load() - sendBefore; gotSent != burstCount {
		t.Fatalf("CONCURRENCY DUPLICATE/LOSS BUG: expected %d HTTP sends across 3 workers, got %d", burstCount, gotSent)
	}

	// 2. Real Kafka broker down (fail-fast) and up (EnsureTopic + Publish + Cleanup)
	kafkaTopic := "znero.it.e2e.chaos." + testkit.RandHex(6)
	kProd, kErr := kafkaclient.New(kafkaclient.Config{
		Brokers:        []string{"127.0.0.1:9092"},
		ClientID:       "e2e-chaos",
		DialTimeout:    2 * time.Second,
		RequestTimeout: 3 * time.Second,
	}, nil)
	if kErr != nil {
		t.Fatalf("kafka producer: %v", kErr)
	}
	t.Cleanup(func() {
		dCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = kProd.DeleteTopic(dCtx, kafkaTopic)
		kProd.Close()
	})
	if err := kProd.EnsureTopic(ctx, kafkaTopic, 1, 1); err != nil {
		t.Fatalf("kafka EnsureTopic: %v", err)
	}
	if err := kProd.Publish(ctx, kafkaTopic, []byte("k"), []byte(`{"chaos":"ok"}`)); err != nil {
		t.Fatalf("kafka Publish: %v", err)
	}

	downProd, _ := kafkaclient.New(kafkaclient.Config{
		Brokers:        []string{"127.0.0.1:1"},
		ClientID:       "e2e-down",
		DialTimeout:    200 * time.Millisecond,
		RequestTimeout: 400 * time.Millisecond,
	}, nil)
	if downProd != nil {
		downCtx, downCancel := context.WithTimeout(ctx, 800*time.Millisecond)
		pubErr := downProd.Publish(downCtx, kafkaTopic, []byte("k"), []byte("v"))
		downCancel()
		downProd.Close()
		if pubErr == nil {
			t.Fatalf("expected fast error when Kafka broker is down")
		}
	}

	// 3. Aborted/cancelled DB transaction mid-send leaves ZERO orphan holds or half-written messages!
	deadCtx, deadCancel := context.WithCancel(ctx)
	deadCancel()
	chaosActor := h.e.Actor("chaos", cChaos.orgID, h.e.PlatformOrgPath, "customer", false, "messaging:submit")
	if _, err := h.msgSvc.Submit(deadCtx, chaosActor, messaging.SubmitRequest{
		Destination:        "+12025550999",
		SenderID:           "ZNERO",
		Text:               "Should rollback cleanly",
		RequestFingerprint: "chaos-cancel-" + testkitRandSuffix(),
		IngressChannel:     "http",
		DLRRequested:       true,
	}); err == nil {
		t.Fatalf("expected aborted send error on cancelled context")
	}
	wAfterAbort, _ := h.ledgerSvc.GetWallet(ctx, h.e.Override, cChaos.walletID)
	if wAfterAbort.AvailableBalance.StringFixed(2) != "100.00" || wAfterAbort.ActiveHolds.StringFixed(2) != "0.00" {
		t.Fatalf("cancelled send leaked wallet state: avail=%s held=%s", wAfterAbort.AvailableBalance.StringFixed(2), wAfterAbort.ActiveHolds.StringFixed(2))
	}

	// 4. Slow HTTP provider hang exceeding RequestTimeoutMs + HTML/Binary garbage 200 OK -> zero panics, zero leaks!
	h.httpNextAction.Store(httpActionSlowTimeout)
	mSlow := h.sendHTTP(cChaos, "+12025550301", "Slow HTTP hang")
	startSlow := time.Now()
	h.dispatcher.ProcessBatch(ctx)
	if elapsed := time.Since(startSlow); elapsed > 4*time.Second {
		t.Fatalf("slow provider hang exceeded bounded timeout: elapsed=%s", elapsed)
	}
	h.assertMsgState(mSlow, cChaos, messaging.StatusUnknown, "active", "99.95", "0.05")

	h.httpNextAction.Store(httpActionHTMLGarbage)
	mHTML := h.sendHTTP(cChaos, "+12025550302", "HTML garbage")
	h.dispatcher.ProcessBatch(ctx)
	h.assertMsgState(mHTML, cChaos, messaging.StatusFailed, "released", "99.95", "0.05")

	h.httpNextAction.Store(httpActionBinaryTrash)
	mBin := h.sendHTTP(cChaos, "+12025550303", "Binary garbage")
	h.dispatcher.ProcessBatch(ctx)
	h.assertMsgState(mBin, cChaos, messaging.StatusFailed, "released", "99.95", "0.05")
	h.httpNextAction.Store(httpActionOK)

	// Settle slow timeout msg via TestClock window expiration & verify full consistency
	h.clk.Advance(13 * time.Hour)
	h.reaper.RunSweep(ctx)
	h.assertMsgState(mSlow, cChaos, messaging.StatusFailed, "released", "100.00", "0.00")

	h.assertLedgerBalancedForCustomer(cBurst)
	h.assertLedgerBalancedForCustomer(cChaos)
	h.assertNoSecretLeaks(cBurst.apiKey, cChaos.apiKey)
	h.assertReconcileZero()
}
