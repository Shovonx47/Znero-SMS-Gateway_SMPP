package e2e_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/config"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/smpp/mocksmsc"
)

func TestE2E_SSRFGuard_ProductionBlocksLoopback_TestSwitchAllows(t *testing.T) {
	// 1. Production config strictly forbids ALLOW_LOOPBACK_IN_TEST=true at startup.
	if _, err := config.Parse(func(k string) string {
		switch k {
		case "APP_ENV":
			return config.EnvProduction
		case "DATABASE_URL":
			return "postgres://znero_app:pass@db.example.com:5432/znero_prod"
		case "KAFKA_BROKERS":
			return "kafka.example.com:9092"
		case "ALLOW_LOOPBACK_IN_TEST":
			return "true"
		default:
			return ""
		}
	}); err == nil || !strings.Contains(err.Error(), "ALLOW_LOOPBACK_IN_TEST") {
		t.Fatalf("expected production config rejection for ALLOW_LOOPBACK_IN_TEST=true, got err=%v", err)
	}

	// 2. With AllowLoopback=false (production SSRF behavior), loopback HTTP provider,
	//    loopback SMPP SMSC, and loopback customer webhook receiver MUST all be blocked.
	h := newE2EHarness(t, false)
	ctx := context.Background()

	cHTTP := h.provisionCustomer("ssrf_http", "on_delivery", 24, "retain_until_resolution", "keep_charge", h.httpPlanID)
	cSMPP := h.provisionCustomer("ssrf_smpp", "on_delivery", 24, "retain_until_resolution", "keep_charge", h.smppPlanID)

	mHTTP := h.sendHTTP(cHTTP, "+12025550101", "SSRF HTTP block test")
	mSMPP := h.sendHTTP(cSMPP, "+12025550102", "SSRF SMPP block test")
	h.dispatcher.ProcessBatch(ctx)

	if hits := h.httpSendCount.Load(); hits != 0 {
		t.Fatalf("SSRF guard failed on HTTP provider: expected 0 hits to 127.0.0.1, got %d", hits)
	}
	if subs := len(h.smsc.GetSubmittedMessages()); subs != 0 {
		t.Fatalf("SSRF guard failed on SMPP provider: expected 0 submits to 127.0.0.1, got %d", subs)
	}

	// HTTP send should fatally fail due to SSRF and trigger a webhook delivery that MUST also be blocked by SSRF guard.
	eventually(t, 2*time.Second, "webhook ssrf block attempt", func() bool {
		h.whDisp.DispatchPending(ctx)
		var cnt int
		_ = h.e.Pool.QueryRow(ctx, `SELECT count(*) FROM customer_webhook_attempts WHERE error_class = 'ssrf_blocked'`).Scan(&cnt)
		return cnt >= 1
	})
	h.whMu.Lock()
	whLen := len(h.receivedWH[mHTTP]) + len(h.receivedWH[mSMPP])
	h.whMu.Unlock()
	if whLen != 0 {
		t.Fatalf("SSRF guard failed on webhook receiver: expected 0 delivered webhooks to 127.0.0.1, got %d", whLen)
	}
}

func TestE2E_Full2x2x2Matrix_AllEightCells(t *testing.T) {
	h := newE2EHarness(t, true)
	ctx := context.Background()

	// Provision all 8 distinct matrix combinations:
	//   Customer(HTTP | SMPP) x Provider(SMPP | HTTP) x BillingMode(on_delivery | on_provider_accept)
	c1 := h.provisionCustomer("c1_h2s_deliv", "on_delivery", 24, "retain_until_resolution", "keep_charge", h.smppPlanID)
	c2 := h.provisionCustomer("c2_h2s_acc", "on_provider_accept", 24, "retain_until_resolution", "refund", h.smppPlanID)
	c3 := h.provisionCustomer("c3_h2h_deliv", "on_delivery", 24, "retain_until_resolution", "keep_charge", h.httpPlanID)
	c4 := h.provisionCustomer("c4_h2h_acc", "on_provider_accept", 24, "retain_until_resolution", "keep_charge", h.httpCbPlanID)
	c5 := h.provisionCustomer("c5_s2s_deliv", "on_delivery", 24, "retain_until_resolution", "keep_charge", h.smppPlanID)
	c6 := h.provisionCustomer("c6_s2s_acc", "on_provider_accept", 24, "retain_until_resolution", "refund", h.smppPlanID)
	c7 := h.provisionCustomer("c7_s2h_deliv", "on_delivery", 24, "retain_until_resolution", "keep_charge", h.httpPlanID)
	c8 := h.provisionCustomer("c8_s2h_acc", "on_provider_accept", 24, "retain_until_resolution", "keep_charge", h.httpCbPlanID)

	// CELL 1: Customer HTTP -> Provider SMPP | on_delivery -> DELIVRD
	secretBody1 := "E2E_SECRET_C1_" + testkitRandSuffix()
	m1 := h.sendHTTP(c1, "+12025550111", secretBody1)
	if n := h.dispatcher.ProcessBatch(ctx); n == 0 {
		t.Fatalf("cell 1 dispatcher processed 0 items")
	}
	h.waitMessageStatus(m1, messaging.StatusDelivered, "cell 1 DELIVRD")
	h.assertMsgState(m1, c1, messaging.StatusDelivered, "captured", "99.95", "0.00")
	h.assertEventSequence(m1, messaging.StatusSubmitted, messaging.StatusDelivered)
	h.waitWebhook(m1, messaging.StatusDelivered)
	h.assertCustomerAPISafe(c1, m1, messaging.CustomerBillingTerms{
		BillingMode: "on_delivery", DLRWaitHours: 24, NoDLRPolicy: "retain_until_resolution", FailedAfterAcceptPolicy: "keep_charge",
	})
	h.assertLedgerBalancedForCustomer(c1)

	// CELL 2: Customer HTTP -> Provider SMPP | on_provider_accept + refund -> UNDELIV
	h.smsc.SetDLRMode(mocksmsc.DLRModeNone) // Accept first so we assert immediate capture before later DLR
	m2 := h.sendHTTP(c2, "+12025550112", "Cell 2 accept then refund")
	h.dispatcher.ProcessBatch(ctx)
	h.assertMsgState(m2, c2, messaging.StatusSubmitted, "captured", "99.95", "0.00")
	var m2ProvID string
	_ = h.e.Pool.QueryRow(ctx, `SELECT provider_message_id FROM messages WHERE id = $1`, m2).Scan(&m2ProvID)
	if err := h.dlrProc.ProcessParsedDLR(ctx, h.connSMPPID, &messaging.DLRReceipt{
		ProviderMessageID: m2ProvID, Stat: "UNDELIV", ErrCode: "045", RawReceipt: "stat:UNDELIV err:045",
	}); err != nil {
		t.Fatalf("cell 2 later UNDELIV DLR: %v", err)
	}
	h.assertMsgState(m2, c2, messaging.StatusUndelivered, "refunded", "100.00", "0.00")
	h.assertEventSequence(m2, messaging.StatusSubmitted, messaging.StatusUndelivered)
	h.waitWebhook(m2, messaging.StatusUndelivered)
	h.assertLedgerBalancedForCustomer(c2)

	// CELL 3: Customer HTTP -> Provider HTTP | on_delivery via DLRPoller -> DELIVRD
	m3 := h.sendHTTP(c3, "+12025550113", "Cell 3 HTTP polling DELIVRD")
	h.dispatcher.ProcessBatch(ctx)
	h.assertMsgState(m3, c3, messaging.StatusSubmitted, "active", "99.95", "0.05")
	if err := h.poller.PollOnce(ctx, true); err != nil {
		t.Fatalf("cell 3 PollOnce: %v", err)
	}
	h.assertMsgState(m3, c3, messaging.StatusDelivered, "captured", "99.95", "0.00")
	h.waitWebhook(m3, messaging.StatusDelivered)
	h.assertCustomerAPISafe(c3, m3, messaging.CustomerBillingTerms{
		BillingMode: "on_delivery", DLRWaitHours: 24, NoDLRPolicy: "retain_until_resolution", FailedAfterAcceptPolicy: "keep_charge",
	})
	h.assertLedgerBalancedForCustomer(c3)

	// CELL 4: Customer HTTP -> Provider HTTP | on_provider_accept + keep_charge -> callback FAILED
	m4 := h.sendHTTP(c4, "+12025550114", "Cell 4 HTTP accept + FAILED keep_charge")
	h.dispatcher.ProcessBatch(ctx)
	h.assertMsgState(m4, c4, messaging.StatusSubmitted, "captured", "99.95", "0.00")
	var m4ProvID string
	_ = h.e.Pool.QueryRow(ctx, `SELECT provider_message_id FROM messages WHERE id = $1`, m4).Scan(&m4ProvID)
	h.sendHTTPCallbackDLR(m4ProvID, "FAILED", "501")
	// httpprovider.MapDLRStatus normalizes "FAILED" -> "UNDELIV" (messaging.StatusUndelivered)
	h.assertMsgState(m4, c4, messaging.StatusUndelivered, "captured", "99.95", "0.00")
	h.waitWebhook(m4, messaging.StatusUndelivered)
	h.assertLedgerBalancedForCustomer(c4)

	// CELL 5: Customer SMPP -> Provider SMPP | on_delivery -> DELIVRD -> customer deliver_sm
	h.smsc.SetDLRMode(mocksmsc.DLRModeImmediate)
	esme5 := h.dialESME(c5)
	defer esme5.Close()
	m5 := h.sendSMPP(esme5, 2, "+12025550115", "Cell 5 SMPP to SMPP on_delivery")
	h.dispatcher.ProcessBatch(ctx)
	h.waitMessageStatus(m5, messaging.StatusDelivered, "cell 5 delivered")
	h.assertMsgState(m5, c5, messaging.StatusDelivered, "captured", "99.95", "0.00")
	h.deliverCustomerSMPPDLRAndWait(esme5, "stat:DELIVRD")
	h.assertLedgerBalancedForCustomer(c5)

	// CELL 6: Customer SMPP -> Provider SMPP | on_provider_accept + refund -> UNDELIV -> customer deliver_sm
	h.smsc.SetDLRMode(mocksmsc.DLRModeNone)
	esme6 := h.dialESME(c6)
	defer esme6.Close()
	m6 := h.sendSMPP(esme6, 2, "+12025550116", "Cell 6 SMPP to SMPP on_provider_accept")
	h.dispatcher.ProcessBatch(ctx)
	h.assertMsgState(m6, c6, messaging.StatusSubmitted, "captured", "99.95", "0.00")
	var m6ProvID string
	_ = h.e.Pool.QueryRow(ctx, `SELECT provider_message_id FROM messages WHERE id = $1`, m6).Scan(&m6ProvID)
	if err := h.dlrProc.ProcessParsedDLR(ctx, h.connSMPPID, &messaging.DLRReceipt{
		ProviderMessageID: m6ProvID, Stat: "UNDELIV", ErrCode: "045", RawReceipt: "stat:UNDELIV err:045",
	}); err != nil {
		t.Fatalf("cell 6 later UNDELIV DLR: %v", err)
	}
	h.assertMsgState(m6, c6, messaging.StatusUndelivered, "refunded", "100.00", "0.00")
	h.deliverCustomerSMPPDLRAndWait(esme6, "stat:UNDELIV")
	h.assertLedgerBalancedForCustomer(c6)

	// CELL 7: Customer SMPP -> Provider HTTP | on_delivery via DLRPoller -> DELIVRD -> customer deliver_sm
	esme7 := h.dialESME(c7)
	defer esme7.Close()
	m7 := h.sendSMPP(esme7, 2, "+12025550117", "Cell 7 SMPP to HTTP on_delivery")
	h.dispatcher.ProcessBatch(ctx)
	h.assertMsgState(m7, c7, messaging.StatusSubmitted, "active", "99.95", "0.05")
	if err := h.poller.PollOnce(ctx, true); err != nil {
		t.Fatalf("cell 7 PollOnce: %v", err)
	}
	h.assertMsgState(m7, c7, messaging.StatusDelivered, "captured", "99.95", "0.00")
	h.deliverCustomerSMPPDLRAndWait(esme7, "stat:DELIVRD")
	h.assertLedgerBalancedForCustomer(c7)

	// CELL 8: Customer SMPP -> Provider HTTP | on_provider_accept -> callback DELIVRD -> customer deliver_sm
	esme8 := h.dialESME(c8)
	defer esme8.Close()
	m8 := h.sendSMPP(esme8, 2, "+12025550118", "Cell 8 SMPP to HTTP on_provider_accept")
	h.dispatcher.ProcessBatch(ctx)
	h.assertMsgState(m8, c8, messaging.StatusSubmitted, "captured", "99.95", "0.00")
	var m8ProvID string
	_ = h.e.Pool.QueryRow(ctx, `SELECT provider_message_id FROM messages WHERE id = $1`, m8).Scan(&m8ProvID)
	h.sendHTTPCallbackDLR(m8ProvID, "DELIVRD", "000")
	h.assertMsgState(m8, c8, messaging.StatusDelivered, "captured", "99.95", "0.00")
	h.deliverCustomerSMPPDLRAndWait(esme8, "stat:DELIVRD")
	h.assertLedgerBalancedForCustomer(c8)

	// Final Invariants: secret/PII scan + clean-slate reconciliation
	h.assertNoSecretLeaks(
		c1.apiKey, c2.apiKey, c5.smppPass, c6.smppPass, secretBody1, "+12025550111",
	)
	h.assertReconcileZero()
}

func testkitRandSuffix() string {
	return uuid.NewString()[:8]
}
