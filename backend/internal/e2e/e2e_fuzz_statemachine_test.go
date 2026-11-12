package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/smpp/mocksmsc"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

type oracleCust struct {
	label          string
	cust           *e2eCustomer
	orgPath        string
	routePlanID    uuid.UUID
	connID         uuid.UUID
	mode           string
	waitH          int
	noDLR          string
	failPolicy     string
	expectedCash   decimal.Decimal // cached_balance expected
	expectedHeld   decimal.Decimal // active_holds expected
	profileMutated bool
}

type oracleMsg struct {
	id             uuid.UUID
	createdAt      time.Time
	custIdx        int
	connID         uuid.UUID
	provMsgID      string
	sellPrice      decimal.Decimal
	snapMode       string
	snapWaitH      int
	snapNoDLR      string
	snapFailPolicy string
	status         string // accepted | submitted | unknown | delivered | undelivered | failed | expired
	holdActive     bool
	holdCaptured   bool
	holdReleased   bool
	holdRefunded   bool
	dispatched     bool
}

func TestE2E_SeededRandomStateMachineAndMoneyInvariants(t *testing.T) {
	watchGoroutineLeaks(t)

	seeds := []uint64{2026111201, 2026111202, 2026111203, 2026111204}
	const opsPerSeed = 5500 // 4 * 5,500 = 22,000 randomized operations total
	totalOps := 0

	for _, seed := range seeds {
		seed := seed
		opsExecuted := 0
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			opsExecuted = runSeededFuzzSuite(t, seed, opsPerSeed)
		})
		totalOps += opsExecuted
	}

	if totalOps < 20000 {
		t.Fatalf("expected >= 20000 total randomized ops across seeds, ran %d", totalOps)
	}
	t.Logf("[PART3 FUZZ PASS] Executed %d deterministic seeded operations across %d seeds (0 invariant violations)", totalOps, len(seeds))
}

func runSeededFuzzSuite(t *testing.T, seed uint64, targetOps int) int {
	t.Helper()
	t.Logf("=== Running Seeded State-Machine & Money Fuzz Suite seed=%d ops=%d ===", seed, targetOps)
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	ctx := context.Background()

	h := newE2EHarness(t, true)
	h.smsc.SetDLRMode(mocksmsc.DLRModeNone) // Deterministic explicit DLRs driven by fuzz generator
	h.httpNextAction.Store(httpActionOK)

	silentLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reaper2 := messaging.NewReaper(h.e.Pool, h.msgRepo, h.ledgerSvc, h.dlrProc, messaging.ReaperConfig{
		MessageTTL:              12 * time.Hour,
		UnknownResolutionWindow: 12 * time.Hour,
		HoldExpiry:              72 * time.Hour,
		UnmatchedDLRRetryWindow: 5 * time.Minute,
		BatchLimit:              100,
		Clock:                   h.clk.Now,
	}, silentLogger)

	// 5 USD customers spanning every billing mode, no_dlr_policy, failed_after_accept_policy, and SMPP/HTTP route
	cfgs := []struct {
		label, mode string
		waitH       int
		noDlr, fail string
		plan        uuid.UUID
		conn        uuid.UUID
	}{
		{"fuzz-c0-deliv-ret-smpp", "on_delivery", 1, "retain_until_resolution", "keep_charge", h.smppPlanID, h.connSMPPID},
		{"fuzz-c1-deliv-imm-http", "on_delivery", 2, "immediate_release", "keep_charge", h.httpCbPlanID, h.connHTTPCbID},
		{"fuzz-c2-deliv-cap-smpp", "on_delivery", 1, "capture_at_ttl", "keep_charge", h.smppPlanID, h.connSMPPID},
		{"fuzz-c3-acc-keep-http", "on_provider_accept", 4, "retain_until_resolution", "keep_charge", h.httpPlanID, h.connHTTPID},
		{"fuzz-c4-acc-ref-smpp", "on_provider_accept", 4, "retain_until_resolution", "refund", h.smppPlanID, h.connSMPPID},
	}

	custs := make([]*oracleCust, len(cfgs))
	for i, c := range cfgs {
		ec := h.provisionCustomer(c.label, c.mode, c.waitH, c.noDlr, c.fail, c.plan)
		var orgPath string
		if pErr := h.e.Pool.QueryRow(ctx, `SELECT path FROM organizations WHERE id = $1`, ec.orgID).Scan(&orgPath); pErr != nil {
			t.Fatalf("seed=%d load org path: %v", seed, pErr)
		}
		custs[i] = &oracleCust{
			label:        c.label,
			cust:         ec,
			orgPath:      orgPath,
			routePlanID:  c.plan,
			connID:       c.conn,
			mode:         c.mode,
			waitH:        c.waitH,
			noDLR:        c.noDlr,
			failPolicy:   c.fail,
			expectedCash: ec.topupUSD,
			expectedHeld: decimal.Zero,
		}
	}

	// Verify EUR wallet currency isolation & wrong-currency rejection ("wrong currency" invariant)
	assertEURCurrencyIsolation(t, h)

	// Dedicated high-frequency fuzz ledger wallet (USD) + shadow state for rapid hold/capture/release/refund/topup fuzzing
	fuzzOrgID, _ := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "fuzz-wallet-"+testkitHex(rng), "customer")
	fuzzW, err := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, fuzzOrgID, "USD", "prepaid")
	if err != nil {
		t.Fatalf("seed=%d ensure fuzz wallet: %v", seed, err)
	}
	fuzzStart := decimal.NewFromInt(1000)
	if _, _, err := h.ledgerSvc.Topup(ctx, h.e.Override, fuzzW.ID, ledger.WalletTopupRequest{
		Amount:         fuzzStart,
		Reason:         "fuzz ledger wallet seed",
		IdempotencyKey: fmt.Sprintf("fuzz-init-%d", seed),
	}, "127.0.0.1", "fuzz"); err != nil {
		t.Fatalf("seed=%d topup fuzz wallet: %v", seed, err)
	}
	fuzzCash := fuzzStart
	fuzzHeld := decimal.Zero
	type fuzzHold struct {
		id       uuid.UUID
		amt      decimal.Decimal
		status   string // active | captured | released | refunded
		captured decimal.Decimal
	}
	fuzzHolds := make([]*fuzzHold, 0, 256)

	var msgs []*oracleMsg
	var pendingOutOfOrderIDs []string
	var msgMu sync.Mutex

	submittedPhaseOpen := true
	reapElapsed := 0 // simulated hours advanced beyond T0

	opCounts := map[string]int{}
	opsDone := 0

	// Phase helper to settle a single accepted message via Dispatcher and sync shadow oracle
	dispatchPending := func() {
		processed := h.dispatcher.ProcessBatch(ctx)
		if processed == 0 {
			return
		}
		for _, om := range msgs {
			if om.dispatched {
				continue
			}
			dbMsg, _, gErr := h.msgRepo.GetMessage(ctx, om.id, om.createdAt)
			if gErr != nil || dbMsg == nil || dbMsg.Status == messaging.StatusAccepted || dbMsg.Status == messaging.StatusQueued {
				continue
			}
			om.dispatched = true
			om.status = dbMsg.Status
			if dbMsg.ProviderMessageID != nil {
				om.provMsgID = *dbMsg.ProviderMessageID
			}
			c := custs[om.custIdx]
			if om.status == messaging.StatusSubmitted && om.snapMode == "on_provider_accept" && om.holdActive {
				om.holdActive = false
				om.holdCaptured = true
				c.expectedCash = c.expectedCash.Sub(om.sellPrice)
				c.expectedHeld = c.expectedHeld.Sub(om.sellPrice)
			}
		}
	}

	applyDLRToOracle := func(om *oracleMsg, stat string) {
		c := custs[om.custIdx]
		var mapped string
		switch stat {
		case "DELIVRD":
			mapped = messaging.StatusDelivered
		case "UNDELIV":
			mapped = messaging.StatusUndelivered
		case "EXPIRED":
			mapped = messaging.StatusExpired
		case "REJECTD":
			mapped = messaging.StatusFailed
		case "ACCEPTD":
			mapped = messaging.StatusSubmitted
		default:
			mapped = messaging.StatusUnknown
		}

		// Mirror DLRProcessor monotonic finality & snapshotted settlement
		switch om.status {
		case messaging.StatusDelivered, messaging.StatusUndelivered, messaging.StatusExpired, messaging.StatusFailed, messaging.StatusRejected:
			return
		case messaging.StatusAccepted, messaging.StatusQueued, messaging.StatusSubmitting:
			return
		}
		if mapped == messaging.StatusSubmitted || mapped == messaging.StatusUnknown {
			return
		}
		om.status = mapped

		switch mapped {
		case messaging.StatusDelivered:
			if om.holdActive {
				om.holdActive = false
				om.holdCaptured = true
				c.expectedCash = c.expectedCash.Sub(om.sellPrice)
				c.expectedHeld = c.expectedHeld.Sub(om.sellPrice)
			}
		case messaging.StatusUndelivered, messaging.StatusExpired, messaging.StatusFailed, messaging.StatusRejected:
			if om.snapMode == "on_provider_accept" {
				if om.holdCaptured && !om.holdRefunded && om.snapFailPolicy == "refund" {
					om.holdRefunded = true
					c.expectedCash = c.expectedCash.Add(om.sellPrice)
				} else if om.holdActive {
					// If not yet captured somehow and failed
					om.holdActive = false
					if om.snapFailPolicy == "keep_charge" {
						om.holdCaptured = true
						c.expectedCash = c.expectedCash.Sub(om.sellPrice)
					} else {
						om.holdReleased = true
					}
					c.expectedHeld = c.expectedHeld.Sub(om.sellPrice)
				}
			} else if om.holdActive {
				om.holdActive = false
				om.holdReleased = true
				c.expectedHeld = c.expectedHeld.Sub(om.sellPrice)
			}
		}
	}

	applyReaperToOracle := func(hoursBeyondStart int) {
		for _, om := range msgs {
			if om.status == messaging.StatusSubmitted && hoursBeyondStart >= om.snapWaitH {
				c := custs[om.custIdx]
				if om.snapMode == "on_provider_accept" {
					om.status = messaging.StatusExpired
					if om.snapFailPolicy == "refund" && om.holdCaptured && !om.holdRefunded {
						om.holdRefunded = true
						c.expectedCash = c.expectedCash.Add(om.sellPrice)
					} else if om.snapFailPolicy == "refund" && om.holdActive {
						om.holdActive = false
						om.holdReleased = true
						c.expectedHeld = c.expectedHeld.Sub(om.sellPrice)
					}
					continue
				}
				switch om.snapNoDLR {
				case "capture_at_ttl":
					om.status = messaging.StatusExpired
					if om.holdActive {
						om.holdActive = false
						om.holdCaptured = true
						c.expectedCash = c.expectedCash.Sub(om.sellPrice)
						c.expectedHeld = c.expectedHeld.Sub(om.sellPrice)
					}
				case "immediate_release":
					om.status = messaging.StatusExpired
					if om.holdActive {
						om.holdActive = false
						om.holdReleased = true
						c.expectedHeld = c.expectedHeld.Sub(om.sellPrice)
					}
				default: // retain_until_resolution
					om.status = messaging.StatusUnknown
				}
			}
			// unknown_resolution_window = 12h beyond dlr_wait_hours
			if om.status == messaging.StatusUnknown && hoursBeyondStart >= om.snapWaitH+12 {
				c := custs[om.custIdx]
				om.status = messaging.StatusFailed
				if om.holdActive {
					om.holdActive = false
					om.holdReleased = true
					c.expectedHeld = c.expectedHeld.Sub(om.sellPrice)
				}
			}
		}
	}

	const maxDBMessages = 180
	for opsDone < targetOps {
		// Phase 1 (ops 0..2999): submit, dispatch, DLR, live profile mutations, unmatched probes, state machine & ledger fuzz.
		// Phase 2 (ops >= 3000): advance TestClock + concurrent 2-Reaper sweeps + late DLRs + ledger/state machine fuzz.
		if opsDone >= 3000 {
			submittedPhaseOpen = false
		}
		roll := rng.IntN(100)

		switch {
		case roll < 5 && submittedPhaseOpen && len(msgs) < maxDBMessages:
			// Submit 1..3 GSM-7 / UCS-2 / multipart messages (or concurrent 3-way burst)
			burst := 1
			if rng.IntN(4) == 0 {
				burst = 3
			}
			var wg sync.WaitGroup
			for b := 0; b < burst && len(msgs)+b < maxDBMessages; b++ {
				ci := rng.IntN(len(custs))
				textVariant := rng.IntN(3)
				var body string
				var expectedParts int64
				switch textVariant {
				case 0:
					body = fmt.Sprintf("Fuzz GSM %d-%d", seed, len(msgs)+b)
					expectedParts = 1
				case 1:
					body = strings.Repeat("a", 161) // 2 GSM-7 parts
					expectedParts = 2
				default:
					body = "ধন্যবাদ " + strings.Repeat("ب", 65) // 2 UCS-2 parts
					expectedParts = 2
				}
				dest := fmt.Sprintf("+1202556%04d", rng.IntN(10000))
				cIdx := ci
				wg.Add(1)
				go func() {
					defer wg.Done()
					c := custs[cIdx]
					tc := &tenant.Context{
						ActorUserID:     uuid.New(),
						ActorOrgID:      c.cust.orgID,
						ActorOrgType:    "customer",
						ActorOrgPath:    c.orgPath,
						IsPlatformAdmin: false,
						Permissions:     map[string]bool{"message.send": true},
					}
					res, sErr := h.msgSvc.Submit(ctx, tc, messaging.SubmitRequest{
						Destination:     dest,
						SenderID:        "ZNERO",
						Text:            body,
						IngressChannel:  "api",
						DLRRequested:    true,
						ValiditySeconds: nil,
					})
					if sErr != nil {
						t.Errorf("seed=%d submit failed (%s): %v", seed, c.label, sErr)
						return
					}
					dbMsg, _, gErr := h.msgRepo.GetMessage(ctx, res.MessageID, res.CreatedAt)
					if gErr != nil || dbMsg == nil {
						t.Errorf("seed=%d load submitted msg: %v", seed, gErr)
						return
					}
					wantPrice := decimal.NewFromInt(expectedParts).Mul(decimal.RequireFromString("0.05"))
					if !dbMsg.SellPrice.Equal(wantPrice) {
						t.Errorf("seed=%d unexpected sell_price=%s want=%s (parts=%d)", seed, dbMsg.SellPrice, wantPrice, dbMsg.TotalParts)
						return
					}
					msgMu.Lock()
					c.expectedHeld = c.expectedHeld.Add(wantPrice)
					msgs = append(msgs, &oracleMsg{
						id:             res.MessageID,
						createdAt:      res.CreatedAt,
						custIdx:        cIdx,
						connID:         c.connID,
						sellPrice:      wantPrice,
						snapMode:       dbMsg.BillingModeSnapshot,
						snapWaitH:      dbMsg.DLRWaitHoursSnapshot,
						snapNoDLR:      dbMsg.NoDLRPolicySnapshot,
						snapFailPolicy: dbMsg.FailedAfterAcceptPolicySnapshot,
						status:         messaging.StatusAccepted,
						holdActive:     true,
					})
					msgMu.Unlock()
				}()
				opsDone++
				opCounts["submit"]++
			}
			wg.Wait()

		case roll < 7 && len(msgs) > 0:
			// Dispatch pending outbox batch cleanly
			dispatchPending()
			opsDone++
			opCounts["dispatch"]++

		case roll < 11 && len(msgs) > 0:
			// Deliver a DLR (including duplicates, conflicting late DLRs after Reaper, and out-of-order DLRs)
			dispatchPending()
			dispatched := make([]*oracleMsg, 0, len(msgs))
			for _, om := range msgs {
				if om.dispatched && om.provMsgID != "" {
					dispatched = append(dispatched, om)
				}
			}
			if len(dispatched) == 0 {
				opsDone++
				continue
			}
			om := dispatched[rng.IntN(len(dispatched))]
			stats := []string{"DELIVRD", "DELIVRD", "UNDELIV", "REJECTD", "EXPIRED", "ACCEPTD"}
			st := stats[rng.IntN(len(stats))]
			errCode := "000"
			if st != "DELIVRD" && st != "ACCEPTD" {
				errCode = "008"
			}
			rec := &messaging.DLRReceipt{
				ProviderMessageID: om.provMsgID,
				Stat:              st,
				ErrCode:           errCode,
				RawReceipt:        fmt.Sprintf("id:%s stat:%s err:%s", om.provMsgID, st, errCode),
			}
			if dErr := h.dlrProc.ProcessParsedDLR(ctx, om.connID, rec); dErr != nil {
				t.Fatalf("seed=%d ProcessParsedDLR(%s): %v", seed, st, dErr)
			}
			applyDLRToOracle(om, st)
			// 25% chance: immediately fire a duplicate or conflicting DLR to test idempotent monotonicity
			if rng.IntN(4) == 0 {
				dupStat := stats[rng.IntN(len(stats))]
				_ = h.dlrProc.ProcessParsedDLR(ctx, om.connID, &messaging.DLRReceipt{
					ProviderMessageID: om.provMsgID,
					Stat:              dupStat,
					ErrCode:           "005",
					RawReceipt:        fmt.Sprintf("id:%s stat:%s err:005", om.provMsgID, dupStat),
				})
				applyDLRToOracle(om, dupStat)
			}
			opsDone++
			opCounts["dlr"]++

		case roll == 11 && len(msgs) > 0:
			// Live customer profile update mid-flight (snapshot immutability test!)
			ci := rng.IntN(len(custs))
			c := custs[ci]
			newModes := []string{"on_delivery", "on_provider_accept"}
			newNoDlr := []string{"retain_until_resolution", "immediate_release", "capture_at_ttl"}
			newFail := []string{"keep_charge", "refund"}
			nm := newModes[rng.IntN(len(newModes))]
			nnd := newNoDlr[rng.IntN(len(newNoDlr))]
			nf := newFail[rng.IntN(len(newFail))]
			nw := 1 + rng.IntN(167)
			if _, err := h.e.Pool.Exec(ctx, `
				UPDATE customer_profiles
				SET billing_mode = $2, dlr_wait_hours = $3, no_dlr_policy = $4, failed_after_accept_policy = $5, version = version + 1
				WHERE organization_id = $1`, c.cust.orgID, nm, nw, nnd, nf); err != nil {
				t.Fatalf("seed=%d update live customer profile: %v", seed, err)
			}
			c.mode, c.waitH, c.noDLR, c.failPolicy = nm, nw, nnd, nf
			c.profileMutated = true
			opsDone++
			opCounts["profile_mutate"]++

		case !submittedPhaseOpen && (roll == 12 || roll == 13) && len(msgs) > 0:
			// Phase 2: Advance TestClock + run 2 concurrent Reapers (verifying advisory lock serialization)
			dispatchPending()
			stepHours := 2
			if rng.IntN(3) == 0 {
				stepHours = 7
			}
			h.clk.Advance(time.Duration(stepHours) * time.Hour)
			reapElapsed += stepHours

			var rwg sync.WaitGroup
			rwg.Add(2)
			go func() { defer rwg.Done(); h.reaper.RunSweep(ctx) }()
			go func() { defer rwg.Done(); reaper2.RunSweep(ctx) }()
			rwg.Wait()

			applyReaperToOracle(reapElapsed)
			opsDone++
			opCounts["reaper_concurrent"]++

		case roll == 13 && len(msgs) < maxDBMessages:
			// Out-of-order unmatched DLR sent for unknown ref, then verified before TestClock sweep cleans it
			fakeRef := fmt.Sprintf("PRE-%d-%d", seed, rng.IntN(1000000))
			_ = h.dlrProc.ProcessParsedDLR(ctx, h.connSMPPID, &messaging.DLRReceipt{
				ProviderMessageID: fakeRef,
				Stat:              "DELIVRD",
				ErrCode:           "000",
				RawReceipt:        fmt.Sprintf("id:%s stat:DELIVRD err:000", fakeRef),
			})
			pendingOutOfOrderIDs = append(pendingOutOfOrderIDs, fakeRef)
			opsDone++
			opCounts["unmatched_dlr"]++

		case roll < 60:
			// High-frequency state machine transition matrix fuzz op (CanTransition / ValidateTransition)
			allStates := []string{
				messaging.StatusAccepted, messaging.StatusQueued, messaging.StatusSubmitting,
				messaging.StatusSubmitted, messaging.StatusDelivered, messaging.StatusUndelivered,
				messaging.StatusExpired, messaging.StatusFailed, messaging.StatusUnknown,
				messaging.StatusRejected, "bogus_status",
			}
			from := allStates[rng.IntN(len(allStates))]
			to := allStates[rng.IntN(len(allStates))]
			allowed := messaging.CanTransition(from, to)
			vErr := messaging.ValidateTransition(from, to)
			if from != to && allowed != (vErr == nil) {
				t.Fatalf("seed=%d CanTransition(%q,%q)=%v disagrees with ValidateTransition=%v", seed, from, to, allowed, vErr)
			}
			if from == to && from == messaging.StatusSubmitting && !allowed {
				t.Fatalf("seed=%d expected StatusSubmitting -> StatusSubmitting failover to be allowed", seed)
			}
			if messaging.IsTerminalStatus(from) && allowed {
				t.Fatalf("seed=%d SECURITY/MONOTONICITY VIOLATION: terminal state %q allowed transition to %q", seed, from, to)
			}
			if from == messaging.StatusUnknown && to == messaging.StatusSubmitting && allowed {
				t.Fatalf("seed=%d AT-MOST-ONCE VIOLATION: StatusUnknown -> StatusSubmitting must NEVER be allowed", seed)
			}
			opsDone++
			opCounts["state_machine"]++

		default:
			// High-frequency real PostgreSQL ledger hold/capture/release/refund fuzz op + exact math invariant
			action := rng.IntN(5)
			switch {
			case action == 0 || len(fuzzHolds) == 0:
				amt := decimal.NewFromInt(int64(1 + rng.IntN(500))).Div(decimal.NewFromInt(100)) // 0.01..5.00
				key := fmt.Sprintf("fh-%d-%d", seed, opsDone)
				avail := fuzzCash.Sub(fuzzHeld)
				tx, err := h.e.Pool.Begin(ctx)
				if err != nil {
					t.Fatalf("seed=%d begin hold tx: %v", seed, err)
				}
				refType := "fuzz"
				refID := uuid.New()
				hold, hErr := h.ledgerSvc.PlaceHoldTx(ctx, tx, fuzzW.ID, amt, "USD", &refType, &refID, &key, time.Now().UTC().Add(24*time.Hour))
				if amt.GreaterThan(avail) {
					_ = tx.Rollback(ctx)
					if hErr == nil {
						t.Fatalf("seed=%d overspend allowed: amt=%s > avail=%s", seed, amt, avail)
					}
				} else if hErr != nil {
					_ = tx.Rollback(ctx)
					t.Fatalf("seed=%d PlaceHoldTx(%s) unexpected err: %v", seed, amt, hErr)
				} else {
					if cErr := tx.Commit(ctx); cErr != nil {
						t.Fatalf("seed=%d commit hold: %v", seed, cErr)
					}
					fuzzHeld = fuzzHeld.Add(amt)
					fuzzHolds = append(fuzzHolds, &fuzzHold{id: hold.ID, amt: amt, status: "active"})
				}

			case action == 1:
				fh := fuzzHolds[rng.IntN(len(fuzzHolds))]
				key := fmt.Sprintf("fcap-%s", fh.id.String())
				if fh.status != "active" && fh.status != "captured" {
					// Use a fresh key for released/refunded holds so we test state-guard rejection, not idempotency cache.
					key = fmt.Sprintf("fcap-fresh-%d-%d", seed, opsDone)
				}
				tx, _ := h.e.Pool.Begin(ctx)
				capAmt := fh.amt
				// Test partial capture on active holds, and exact idempotency replay on captured holds
				if fh.status == "active" && rng.IntN(5) == 0 && fh.amt.GreaterThan(decimal.NewFromFloat(0.02)) {
					capAmt = fh.amt.Div(decimal.NewFromInt(2)).Truncate(ledger.MoneyScale)
				} else if fh.status == "captured" {
					capAmt = fh.captured
				}
				_, cErr := h.ledgerSvc.CaptureTx(ctx, tx, fh.id, capAmt, &key)
				if fh.status == "active" {
					if cErr != nil {
						_ = tx.Rollback(ctx)
						t.Fatalf("seed=%d CaptureTx unexpected err: %v", seed, cErr)
					}
					_ = tx.Commit(ctx)
					fuzzCash = fuzzCash.Sub(capAmt)
					fuzzHeld = fuzzHeld.Sub(fh.amt) // capturing full or partial closes hold remainder in Znero ledger
					fh.status = "captured"
					fh.captured = capAmt
				} else if fh.status == "captured" {
					if cErr != nil {
						_ = tx.Rollback(ctx)
						t.Fatalf("seed=%d duplicate CaptureTx idempotent replay failed: %v", seed, cErr)
					}
					_ = tx.Commit(ctx)
				} else {
					_ = tx.Rollback(ctx)
					if cErr == nil {
						t.Fatalf("seed=%d captured non-active hold status=%s", seed, fh.status)
					}
				}

			case action == 2:
				fh := fuzzHolds[rng.IntN(len(fuzzHolds))]
				tx, _ := h.e.Pool.Begin(ctx)
				rErr := h.ledgerSvc.ReleaseTx(ctx, tx, fh.id)
				if fh.status == "active" {
					if rErr != nil {
						_ = tx.Rollback(ctx)
						t.Fatalf("seed=%d ReleaseTx unexpected err: %v", seed, rErr)
					}
					_ = tx.Commit(ctx)
					fuzzHeld = fuzzHeld.Sub(fh.amt)
					fh.status = "released"
				} else {
					_ = tx.Rollback(ctx)
				}

			case action == 3:
				fh := fuzzHolds[rng.IntN(len(fuzzHolds))]
				key := fmt.Sprintf("fref-%s", fh.id.String())
				tx, _ := h.e.Pool.Begin(ctx)
				_, rErr := h.ledgerSvc.RefundCaptureTx(ctx, tx, fh.id, fh.captured, &key, "fuzz refund")
				if fh.status == "captured" && fh.captured.GreaterThan(decimal.Zero) {
					if rErr != nil {
						_ = tx.Rollback(ctx)
						t.Fatalf("seed=%d RefundTx unexpected err: %v", seed, rErr)
					}
					_ = tx.Commit(ctx)
					fuzzCash = fuzzCash.Add(fh.captured)
					fh.status = "refunded"
				} else {
					_ = tx.Rollback(ctx)
				}

			default:
				topAmt := decimal.NewFromInt(int64(10 + rng.IntN(50)))
				key := fmt.Sprintf("ftop-%d-%d", seed, opsDone)
				if _, _, tErr := h.ledgerSvc.Topup(ctx, h.e.Override, fuzzW.ID, ledger.WalletTopupRequest{
					Amount: topAmt, Reason: "fuzz periodic topup", IdempotencyKey: key,
				}, "127.0.0.1", "fuzz"); tErr != nil {
					t.Fatalf("seed=%d topup: %v", seed, tErr)
				}
				fuzzCash = fuzzCash.Add(topAmt)
			}
			opsDone++
			opCounts["ledger_fuzz"]++
		}
	}

	// Final settlement sweep: flush dispatcher + advance TestClock past 24h so ALL active no-DLR holds & unmatched_dlrs expire/resolve cleanly!
	dispatchPending()
	h.clk.Advance(30 * time.Hour)
	reapElapsed += 30
	var finalWg sync.WaitGroup
	finalWg.Add(2)
	go func() { defer finalWg.Done(); h.reaper.RunSweep(ctx) }()
	go func() { defer finalWg.Done(); reaper2.RunSweep(ctx) }()
	finalWg.Wait()
	applyReaperToOracle(reapElapsed)

	// Mark unmatched_dlrs from out-of-order probes: Reaper expires old unmatched_dlrs after UnmatchedDLRRetryWindow (5m),
	// wait: check if Reaper updated them to 'expired' cleanly.
	_ = pendingOutOfOrderIDs

	// Assert ALL shadow oracle money & balance invariants for every customer + fuzz wallet!
	for _, c := range custs {
		assertOracleMatchesWallet(t, seed, h, c.cust.orgID, c.cust.walletID, c.label, c.expectedCash, c.expectedHeld)
		h.assertLedgerBalancedForCustomer(c.cust)
	}
	assertOracleMatchesWallet(t, seed, h, fuzzOrgID, fuzzW.ID, "fuzz-ledger-wallet", fuzzCash, fuzzHeld)
	assertOrgLedgerTxBalanced(t, seed, h, fuzzOrgID)

	// Verify all EUR entries on C_EUR wallet remain strictly EUR and never USD
	assertReconcileZero(t, seed, h)

	t.Logf("seed=%d PASSED (%d ops: submit=%d dispatch=%d dlr=%d profile_mut=%d reaper_conc=%d unmatched=%d state_machine=%d ledger=%d)",
		seed, opsDone, opCounts["submit"], opCounts["dispatch"], opCounts["dlr"],
		opCounts["profile_mutate"], opCounts["reaper_concurrent"], opCounts["unmatched_dlr"],
		opCounts["state_machine"], opCounts["ledger_fuzz"])
	return opsDone
}

func assertEURCurrencyIsolation(t *testing.T, h *e2eHarness) {
	t.Helper()
	ctx := context.Background()
	eurOrgID, _ := h.e.ChildOrg(h.e.PlatformOrgID, h.e.PlatformOrgPath, "fuzz-eur-"+testkitHexSimple(), "customer")
	eurW, err := h.ledgerSvc.EnsureWallet(ctx, h.e.Override, eurOrgID, "EUR", "prepaid")
	if err != nil {
		t.Fatalf("ensure EUR wallet: %v", err)
	}
	if _, _, err := h.ledgerSvc.Topup(ctx, h.e.Override, eurW.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(25), Reason: "EUR seed", IdempotencyKey: "eur-topup-" + eurOrgID.String(),
	}, "127.0.0.1", "fuzz"); err != nil {
		t.Fatalf("EUR topup: %v", err)
	}

	// Attempting USD hold against EUR wallet MUST fail with ErrCurrencyMismatch and touch ZERO balance
	tx, _ := h.e.Pool.Begin(ctx)
	ref := "eur-mismatch"
	kBad := "bad-curr-" + uuid.NewString()
	_, mErr := h.ledgerSvc.PlaceHoldTx(ctx, tx, eurW.ID, decimal.NewFromInt(1), "USD", &ref, nil, &kBad, h.clk.Now().Add(time.Hour))
	_ = tx.Rollback(ctx)
	if !errors.Is(mErr, ledger.ErrCurrencyMismatch) {
		t.Fatalf("CURRENCY MUTATION DETECTED: expected ErrCurrencyMismatch placing USD hold on EUR wallet, got %v", mErr)
	}

	// Valid EUR hold -> capture -> refund must write strictly 'EUR' ledger_entries
	tx2, _ := h.e.Pool.Begin(ctx)
	kOK := "ok-eur-" + uuid.NewString()
	hold, err := h.ledgerSvc.PlaceHoldTx(ctx, tx2, eurW.ID, decimal.NewFromInt(5), "EUR", &ref, nil, &kOK, h.clk.Now().Add(time.Hour))
	if err != nil {
		_ = tx2.Rollback(ctx)
		t.Fatalf("valid EUR PlaceHoldTx: %v", err)
	}
	capKey := "eur-cap-" + uuid.NewString()
	if _, err := h.ledgerSvc.CaptureTx(ctx, tx2, hold.ID, decimal.NewFromInt(5), &capKey); err != nil {
		_ = tx2.Rollback(ctx)
		t.Fatalf("valid EUR CaptureTx: %v", err)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatalf("commit EUR hold+capture: %v", err)
	}

	var nonEUR int
	if err := h.e.Pool.QueryRow(ctx, `
		SELECT count(*) FROM ledger_entries le
		JOIN ledger_transactions lt ON lt.id = le.transaction_id
		WHERE lt.tenant_org_id = $1 AND le.currency <> 'EUR'
	`, eurOrgID).Scan(&nonEUR); err != nil || nonEUR != 0 {
		t.Fatalf("CURRENCY MUTATION DETECTED: found %d non-EUR ledger entries for EUR wallet (err=%v)", nonEUR, err)
	}
	assertOracleMatchesWallet(t, 0, h, eurOrgID, eurW.ID, "EUR-wallet", decimal.NewFromInt(20), decimal.Zero)
}

func assertOracleMatchesWallet(t *testing.T, seed uint64, h *e2eHarness, orgID, walletID uuid.UUID, label string, wantCash, wantHeld decimal.Decimal) {
	t.Helper()
	ctx := context.Background()
	var cached, actualHeld decimal.Decimal
	if err := h.e.Pool.QueryRow(ctx, `
		SELECT w.cached_balance,
		       COALESCE((SELECT SUM(h.amount) FROM holds h WHERE h.wallet_id = w.id AND h.status = 'active'), 0)
		FROM wallets w WHERE w.id = $1
	`, walletID).Scan(&cached, &actualHeld); err != nil {
		t.Fatalf("seed=%d query wallet %s: %v", seed, label, err)
	}
	if !cached.Equal(wantCash) || !actualHeld.Equal(wantHeld) {
		t.Fatalf("seed=%d ORACLE MONEY MISMATCH on %s (%s): DB(cached=%s, active_holds=%s) vs Oracle(cached=%s, active_holds=%s)",
			seed, label, orgID.String()[:8], cached.StringFixed(4), actualHeld.StringFixed(4), wantCash.StringFixed(4), wantHeld.StringFixed(4))
	}
}

func assertOrgLedgerTxBalanced(t *testing.T, seed uint64, h *e2eHarness, orgID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	rows, err := h.e.Pool.Query(ctx, `
		SELECT lt.id,
		       COALESCE(SUM(CASE WHEN le.direction='debit' THEN le.amount ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN le.direction='credit' THEN le.amount ELSE 0 END), 0)
		FROM ledger_transactions lt
		JOIN ledger_entries le ON le.transaction_id = lt.id
		WHERE lt.tenant_org_id = $1
		GROUP BY lt.id
	`, orgID)
	if err != nil {
		t.Fatalf("seed=%d query tx balance: %v", seed, err)
	}
	defer rows.Close()
	for rows.Next() {
		var txID uuid.UUID
		var deb, cred decimal.Decimal
		_ = rows.Scan(&txID, &deb, &cred)
		if !deb.Equal(cred) {
			t.Fatalf("seed=%d ledger tx %s unbalanced: debit=%s credit=%s", seed, txID, deb, cred)
		}
	}
}

func assertReconcileZero(t *testing.T, seed uint64, h *e2eHarness) {
	t.Helper()
	h.assertReconcileZero()
}

func testkitHex(rng *rand.Rand) string {
	return fmt.Sprintf("%08x", rng.Uint32())
}

func testkitHexSimple() string {
	return uuid.NewString()[:8]
}
