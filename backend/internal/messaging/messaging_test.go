package messaging_test

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
	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/internal/routing"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

type msgRater struct {
	sellPrice decimal.Decimal
	buyPrice  decimal.Decimal
	currency  string
}

func (m *msgRater) LookupBuyRate(ctx context.Context, connectionID uuid.UUID, countryID uuid.UUID, operatorID *uuid.UUID, at time.Time) (*routing.RateLookupResult, error) {
	return &routing.RateLookupResult{
		Found:    true,
		Price:    m.buyPrice,
		Currency: m.currency,
	}, nil
}

func (m *msgRater) LookupSellRate(ctx context.Context, customerOrgID uuid.UUID, countryID uuid.UUID, operatorID *uuid.UUID, at time.Time) (*routing.RateLookupResult, error) {
	return &routing.RateLookupResult{
		Found:    true,
		Price:    m.sellPrice,
		Currency: m.currency,
	}, nil
}

type msgNumberer struct {
	defaultCountryID uuid.UUID
}

func (m *msgNumberer) Lookup(destinationE164 string) (*routing.NumberInfo, error) {
	return &routing.NumberInfo{CountryID: m.defaultCountryID}, nil
}

type msgKit struct {
	e             *testkit.Env
	repo          messaging.Repository
	svc           *messaging.Service
	dispatcher    *messaging.Dispatcher
	fakeSender    *messaging.FakeSender
	routingEngine *routing.Engine
	routingSvc    *routing.Service
	ledgerSvc     *ledger.Service
	keyring       *secrets.KeyRing

	custOrgID     uuid.UUID
	custUser      *tenant.Context
	walletID      uuid.UUID
	provConnID1   uuid.UUID
	provConnID2   uuid.UUID
	routePlanID   uuid.UUID
}

func newMsgKit(t *testing.T) *msgKit {
	t.Helper()
	e := testkit.New(t)

	keyring, err := secrets.NewKeyRing(secrets.DefaultKeyID, []byte("dev-secrets-key-32-bytes-only!!!"))
	if err != nil {
		t.Fatalf("new keyring: %v", err)
	}

	orgRepo := org.NewRepository(e.Pool)
	ledgerRepo := ledger.NewRepository(e.Pool)
	ledgerSvc := ledger.NewService(ledgerRepo, orgRepo, e.Approval, e.Audit, e.Pool)

	// Clean outbox queue for isolated test runs
	_, _ = e.Pool.Exec(context.Background(), "DELETE FROM message_outbox")

	// Ensure a country exists
	var countryID uuid.UUID
	err = e.Pool.QueryRow(context.Background(), `SELECT id FROM countries LIMIT 1`).Scan(&countryID)
	if err != nil {
		cReq := e.CountryRequest()
		cRes, cErr := e.Country.Create(context.Background(), e.Override, cReq)
		if cErr != nil {
			t.Fatalf("create test country: %v", cErr)
		}
		countryID = cRes.(*country.Country).ID
	}

	// Create test customer
	resellerOrg, resellerPath := e.ChildOrg(e.PlatformOrgID, e.PlatformOrgPath, "msg-reseller-"+testkit.RandHex(3), "reseller")
	custOrg, custPath := e.ChildOrg(resellerOrg, resellerPath, "msg-cust-"+testkit.RandHex(3), "customer")
	custActor := e.Actor("msg-user-"+testkit.RandHex(3), custOrg, custPath, "customer", false,
		"message.send", "message.view", "message.content.view", "message.cancel", "wallet.wallet.view",
	)

	// Create customer prepaid wallet with balance
	w, err := ledgerSvc.EnsureWallet(context.Background(), e.Override, custOrg, "USD", "prepaid")
	if err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	_, _, err = ledgerSvc.Topup(context.Background(), e.Override, w.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(100),
		Reason: "Seed test funds",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("top up wallet: %v", err)
	}

	// Create providers, connections, and coverage directly in SQL
	provCode := "PRV_" + strings.ToUpper(testkit.RandHex(4))
	var prov1ID uuid.UUID
	err = e.Pool.QueryRow(context.Background(), `
		INSERT INTO providers (name, code, provider_type, legal_entity_name, status, version)
		VALUES ($1, $2, 'smpp', 'Alpha Provider Legal', 'active', 1)
		RETURNING id
	`, "Provider "+provCode, provCode).Scan(&prov1ID)
	if err != nil {
		t.Fatalf("insert provider: %v", err)
	}

	var conn1ID, conn2ID uuid.UUID
	err = e.Pool.QueryRow(context.Background(), `
		INSERT INTO provider_connections (provider_id, name, protocol, status, version, max_tps)
		VALUES ($1, $2, 'smpp', 'active', 1, 100)
		RETURNING id
	`, prov1ID, "Alpha-Conn-1-"+testkit.RandHex(3)).Scan(&conn1ID)
	if err != nil {
		t.Fatalf("insert conn1: %v", err)
	}

	err = e.Pool.QueryRow(context.Background(), `
		INSERT INTO provider_connections (provider_id, name, protocol, status, version, max_tps)
		VALUES ($1, $2, 'smpp', 'active', 1, 100)
		RETURNING id
	`, prov1ID, "Alpha-Conn-2-"+testkit.RandHex(3)).Scan(&conn2ID)
	if err != nil {
		t.Fatalf("insert conn2: %v", err)
	}

	_, err = e.Pool.Exec(context.Background(), `
		INSERT INTO provider_coverage (provider_id, country_id, status, version)
		VALUES ($1, $2, 'active', 1)
		ON CONFLICT (provider_id, country_id, COALESCE(operator_id, '00000000-0000-0000-0000-000000000000'::uuid))
		DO UPDATE SET status = 'active'
	`, prov1ID, countryID)
	if err != nil {
		t.Fatalf("insert coverage: %v", err)
	}

	// Setup routing engine with rater and numberer
	rater := &msgRater{
		sellPrice: decimal.RequireFromString("0.050000"),
		buyPrice:  decimal.RequireFromString("0.020000"),
		currency:  "USD",
	}
	numberer := &msgNumberer{defaultCountryID: countryID}
	routingEngine := routing.NewEngine(e.Pool, rater, numberer, nil)
	routingRepo := routing.NewRepository(e.Pool)
	routingSvc := routing.NewService(routingRepo, e.Approval, e.Audit, e.Pool, routingEngine)

	msgRepo := messaging.NewRepository(e.Pool)
	fakeSender := messaging.NewFakeSender()
	cfg := messaging.DefaultConfig()

	msgSvc, err := messaging.NewService(
		e.Pool, msgRepo, cfg, routingEngine, ledgerSvc, keyring, e.Audit, fakeSender, nil,
	)
	if err != nil {
		t.Fatalf("new messaging service: %v", err)
	}

	dispatcher := messaging.NewDispatcher(e.Pool, msgRepo, routingEngine, ledgerSvc, fakeSender, nil)

	// Create Route Plan with primary (conn1) and fallback (conn2)
	plan, err := routingSvc.CreatePlan(context.Background(), e.Override, &routing.RoutePlan{
		Name:     "Test Plan " + testkit.RandHex(3),
		Code:     "TP_" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}

	_, _, err = routingSvc.CreateEntry(context.Background(), e.Override, &routing.RoutePlanEntry{
		PlanID:       plan.ID,
		ConnectionID: conn1ID,
		ScopeType:    "global",
		Priority:     1,
		Weight:       100,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("create entry 1: %v", err)
	}

	_, _, err = routingSvc.CreateEntry(context.Background(), e.Override, &routing.RoutePlanEntry{
		PlanID:       plan.ID,
		ConnectionID: conn2ID,
		ScopeType:    "global",
		Priority:     2,
		Weight:       100,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("create entry 2: %v", err)
	}

	_, _, err = routingSvc.RequestPlanActivation(context.Background(), e.Override, plan.ID)
	if err != nil {
		t.Fatalf("activate plan: %v", err)
	}

	// Assign plan to customer
	_, _, err = routingSvc.CreateAssignment(context.Background(), e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: custOrg,
		PlanID:        plan.ID,
		Priority:      1,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	// Reload snapshot
	routing.TryReload(context.Background(), e.Pool)

	return &msgKit{
		e:             e,
		repo:          msgRepo,
		svc:           msgSvc,
		dispatcher:    dispatcher,
		fakeSender:    fakeSender,
		routingEngine: routingEngine,
		routingSvc:    routingSvc,
		ledgerSvc:     ledgerSvc,
		keyring:       keyring,
		custOrgID:     custOrg,
		custUser:      custActor,
		walletID:      w.ID,
		provConnID1:   conn1ID,
		provConnID2:   conn2ID,
		routePlanID:   plan.ID,
	}
}

// -----------------------------------------------------------------------------
// 1. Config Hold Timing Startup Validation Tests
// -----------------------------------------------------------------------------
func TestConfig_HoldTimingValidation(t *testing.T) {
	// Valid configuration: HoldExpiry (72h) > TTL (24h) + UnknownWindow (24h) = 48h
	validCfg := messaging.Config{
		MessageTTL:               24 * time.Hour,
		UnknownResolutionWindow: 24 * time.Hour,
		HoldExpiry:               72 * time.Hour,
		MaxSegmentsLimit:         10,
		PartitionPrecreateMonths: 3,
	}
	if err := validCfg.Validate(); err != nil {
		t.Fatalf("expected valid config to pass, got: %v", err)
	}

	// Invalid configuration: HoldExpiry (48h) <= TTL (24h) + UnknownWindow (24h) = 48h
	invalidCfg := validCfg
	invalidCfg.HoldExpiry = 48 * time.Hour
	if err := invalidCfg.Validate(); err == nil {
		t.Fatal("expected invalid config (HoldExpiry == TTL + UnknownWindow) to fail, got nil")
	} else if !strings.Contains(err.Error(), "must exceed") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// Invalid configuration: negative TTL
	negTTLCfg := validCfg
	negTTLCfg.MessageTTL = -1 * time.Hour
	if err := negTTLCfg.Validate(); err == nil {
		t.Fatal("expected negative TTL to fail, got nil")
	}
}

// -----------------------------------------------------------------------------
// 2. State Machine Allowed and Illegal Transitions
// -----------------------------------------------------------------------------
func TestStateMachine_AllowedAndIllegalTransitions(t *testing.T) {
	// Valid Transitions
	validPairs := [][2]string{
		{messaging.StatusAccepted, messaging.StatusQueued},
		{messaging.StatusQueued, messaging.StatusSubmitting},
		{messaging.StatusSubmitting, messaging.StatusSubmitted},
		{messaging.StatusSubmitting, messaging.StatusUnknown},
		{messaging.StatusSubmitting, messaging.StatusFailed},
		{messaging.StatusSubmitted, messaging.StatusDelivered},
		{messaging.StatusSubmitted, messaging.StatusUndelivered},
		{messaging.StatusSubmitted, messaging.StatusExpired},
		{messaging.StatusSubmitted, messaging.StatusFailed},
		{messaging.StatusUnknown, messaging.StatusDelivered},
		{messaging.StatusUnknown, messaging.StatusFailed},
	}
	for _, p := range validPairs {
		if err := messaging.ValidateTransition(p[0], p[1]); err != nil {
			t.Errorf("expected transition %s -> %s to be valid, got: %v", p[0], p[1], err)
		}
	}

	// Illegal Transitions (Strictly Rejected)
	illegalPairs := [][2]string{
		{messaging.StatusDelivered, messaging.StatusFailed},
		{messaging.StatusDelivered, messaging.StatusAccepted},
		{messaging.StatusFailed, messaging.StatusSubmitted},
		{messaging.StatusExpired, messaging.StatusDelivered},
		{messaging.StatusRejected, messaging.StatusAccepted},
		{messaging.StatusUnknown, messaging.StatusSubmitting}, // The NEVER auto-resend rule
	}
	for _, p := range illegalPairs {
		if err := messaging.ValidateTransition(p[0], p[1]); err == nil {
			t.Errorf("CRITICAL INVARIANT VIOLATION: transition %s -> %s was allowed!", p[0], p[1])
		}
	}
}

// -----------------------------------------------------------------------------
// 3. GSM-7 & UCS-2 Segment Calculations
// -----------------------------------------------------------------------------
func TestEncoding_GSM7AndUCS2SegmentCalculations(t *testing.T) {
	// Single segment GSM-7: 160 chars
	gsmSingle := strings.Repeat("A", 160)
	enc, parts, length, err := messaging.CalculateSegments(gsmSingle, 10)
	if err != nil || enc != messaging.EncodingGSM7 || parts != 1 || length != 160 {
		t.Fatalf("gsm-7 single segment mismatch: enc=%s parts=%d len=%d err=%v", enc, parts, length, err)
	}

	// Multi-part GSM-7: 161 chars -> 2 parts (153 + 8)
	gsmMulti := strings.Repeat("A", 161)
	enc, parts, length, err = messaging.CalculateSegments(gsmMulti, 10)
	if err != nil || enc != messaging.EncodingGSM7 || parts != 2 || length != 161 {
		t.Fatalf("gsm-7 multi-part mismatch: enc=%s parts=%d len=%d err=%v", enc, parts, length, err)
	}

	// Single segment UCS-2: 70 chars with Arabic/Emoji
	ucsSingle := "مرحبا " + strings.Repeat("a", 60)
	enc, parts, length, err = messaging.CalculateSegments(ucsSingle, 10)
	if err != nil || enc != messaging.EncodingUCS2 || parts != 1 {
		t.Fatalf("ucs-2 single segment mismatch: enc=%s parts=%d len=%d err=%v", enc, parts, length, err)
	}

	// Multi-part UCS-2: 71 chars -> 2 parts (67 + 4)
	ucsMulti := "مرحبا " + strings.Repeat("a", 65)
	enc, parts, length, err = messaging.CalculateSegments(ucsMulti, 10)
	if err != nil || enc != messaging.EncodingUCS2 || parts != 2 {
		t.Fatalf("ucs-2 multi-part mismatch: enc=%s parts=%d len=%d err=%v", enc, parts, length, err)
	}

	// Max segments limit exceeded
	longText := strings.Repeat("A", 153*11) // 11 segments
	_, _, _, err = messaging.CalculateSegments(longText, 10)
	if err == nil || !strings.Contains(err.Error(), "maximum allowed") {
		t.Fatalf("expected max segments error, got: %v", err)
	}
}

// -----------------------------------------------------------------------------
// 4. Pipeline Order: No Hold Before Pricing & No Ack Before Durable
// -----------------------------------------------------------------------------
func TestSubmit_PipelineOrder_NoHoldBeforePricing_NoAckBeforeDurable(t *testing.T) {
	mk := newMsgKit(t)
	ctx := context.Background()

	wBefore, err := mk.ledgerSvc.GetWallet(ctx, mk.e.Override, mk.walletID)
	if err != nil {
		t.Fatalf("get wallet before: %v", err)
	}

	// Case 1: Bad destination -> Rejected BEFORE hold
	reqBadDest := messaging.SubmitRequest{
		Destination: "invalid-number",
		SenderID:    "TESTSMS",
		Text:        "Hello",
	}
	_, err = mk.svc.Submit(ctx, mk.custUser, reqBadDest)
	if err == nil {
		t.Fatal("expected invalid destination to fail, got nil")
	}

	wAfterBad, _ := mk.ledgerSvc.GetWallet(ctx, mk.e.Override, mk.walletID)
	if !wAfterBad.ActiveHolds.Equal(wBefore.ActiveHolds) {
		t.Fatalf("INVARIANT VIOLATION: Hold was placed before validation! Before: %v, After: %v",
			wBefore.ActiveHolds, wAfterBad.ActiveHolds)
	}

	// Case 2: Valid submission -> Hold and message exist durably
	reqValid := messaging.SubmitRequest{
		Destination: "+12025550199",
		SenderID:    "TESTSMS",
		Text:        "Hello Znero",
	}
	res, err := mk.svc.Submit(ctx, mk.custUser, reqValid)
	if err != nil {
		t.Fatalf("valid submit failed: %v", err)
	}
	if res.Status != messaging.StatusAccepted {
		t.Fatalf("expected status 'accepted', got: %s", res.Status)
	}

	// Verify durability in DB
	msg, parts, err := mk.repo.GetMessage(ctx, res.MessageID, res.CreatedAt)
	if err != nil || msg == nil {
		t.Fatalf("message not durable in DB: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	if msg.HoldID == nil {
		t.Fatal("expected message to have hold_id associated")
	}

	wAfterValid, _ := mk.ledgerSvc.GetWallet(ctx, mk.e.Override, mk.walletID)
	if !wAfterValid.ActiveHolds.GreaterThan(wBefore.ActiveHolds) {
		t.Fatalf("expected active holds to increase, before=%v after=%v", wBefore.ActiveHolds, wAfterValid.ActiveHolds)
	}
}

// -----------------------------------------------------------------------------
// 5. Pre-Hold Rejection Reasons (No Hold Placed, Recorded as REJECTED)
// -----------------------------------------------------------------------------
func TestSubmit_AllRejectedReasons(t *testing.T) {
	mk := newMsgKit(t)
	ctx := context.Background()

	cases := []struct {
		name       string
		req        messaging.SubmitRequest
		expectCode string
	}{
		{
			name: "InvalidDestination",
			req: messaging.SubmitRequest{
				Destination: "not-a-number",
				SenderID:    "TEST",
				Text:        "Hi",
			},
			expectCode: messaging.ReasonInvalidDestination,
		},
		{
			name: "EmptySenderID",
			req: messaging.SubmitRequest{
				Destination: "+12025550199",
				SenderID:    "",
				Text:        "Hi",
			},
			expectCode: messaging.ReasonInvalidSenderID,
		},
		{
			name: "MaxSegmentsExceeded",
			req: messaging.SubmitRequest{
				Destination: "+12025550199",
				SenderID:    "TEST",
				Text:        strings.Repeat("A", 153*12),
			},
			expectCode: messaging.ReasonMaxSegmentsExceeded,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := mk.svc.Submit(ctx, mk.custUser, tc.req)
			if err == nil {
				t.Fatalf("expected rejection %s, got nil", tc.expectCode)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// 6. Idempotency: 50 Concurrent Duplicates Under -race
// -----------------------------------------------------------------------------
func TestSubmit_Idempotency_50ConcurrentDuplicates(t *testing.T) {
	mk := newMsgKit(t)
	ctx := context.Background()

	clientRef := "idemp-conc-" + testkit.RandHex(4)
	req := messaging.SubmitRequest{
		Destination:     "+12025550199",
		SenderID:        "TESTSMS",
		Text:            "Concurrent idempotency test",
		ClientReference: &clientRef,
	}

	concurrency := 50
	var wg sync.WaitGroup
	wg.Add(concurrency)

	responses := make([]*messaging.SubmitResponse, concurrency)
	errorsList := make([]error, concurrency)

	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			defer wg.Done()
			res, err := mk.svc.Submit(ctx, mk.custUser, req)
			responses[idx] = res
			errorsList[idx] = err
		}(i)
	}

	wg.Wait()

	var firstMsgID uuid.UUID
	successCount := 0

	for i := 0; i < concurrency; i++ {
		if errorsList[i] != nil {
			// In concurrent conflict, error is also acceptable if duplicate key caught
			continue
		}
		if responses[i] != nil {
			successCount++
			if firstMsgID == uuid.Nil {
				firstMsgID = responses[i].MessageID
			} else if responses[i].MessageID != firstMsgID {
				t.Fatalf("DUPLICATE MESSAGE CREATED! Expected same message ID %s, got %s", firstMsgID, responses[i].MessageID)
			}
		}
	}

	if successCount == 0 {
		t.Fatal("expected at least one submit to succeed")
	}

	// Verify only ONE row in messages table
	var dbCount int
	_ = mk.e.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM messages WHERE client_reference = $1", clientRef).Scan(&dbCount)
	if dbCount != 1 {
		t.Fatalf("IDEMPOTENCY FAILED: Found %d message rows for client_reference %s", dbCount, clientRef)
	}
}

// -----------------------------------------------------------------------------
// 7. Idempotency Across Month Boundary (Amendment 2)
// -----------------------------------------------------------------------------
func TestSubmit_Idempotency_AcrossMonthBoundary(t *testing.T) {
	mk := newMsgKit(t)
	ctx := context.Background()

	clientRef := "month-cross-" + testkit.RandHex(4)

	// Insert row in month 1 (November 2026) directly into unpartitioned lookup
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	msgID1 := uuid.New()

	tx, err := mk.e.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)

	msg1 := &messaging.Message{
		ID:                   msgID1,
		CreatedAt:            now,
		CustomerOrgID:        mk.custOrgID,
		ClientReference:      &clientRef,
		OriginalDestination: "+12025550199",
		Destination:          "+12025550199",
		SenderID:             "TEST",
		MessageType:          "sms",
		Encoding:             "GSM7",
		EncryptedBody:        []byte("body1"),
		KeyID:                "v1",
		Status:               messaging.StatusAccepted,
		ExpiresAt:            now.Add(24 * time.Hour),
		UpdatedAt:            now,
	}
	if err := mk.repo.InsertMessageAtomic(ctx, tx, msg1, nil, nil, nil); err != nil {
		t.Fatalf("insert month 1 message: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit month 1: %v", err)
	}

	// Now in Month 2 (December 2026), attempt to submit with identical clientReference
	reqMonth2 := messaging.SubmitRequest{
		Destination:     "+12025550199",
		SenderID:        "TEST",
		Text:            "Hello in December",
		ClientReference: &clientRef,
	}

	// Submit should detect idempotency and return the original message ID without inserting duplicate
	res, err := mk.svc.Submit(ctx, mk.custUser, reqMonth2)
	if err != nil {
		t.Fatalf("month-cross submit failed: %v", err)
	}
	if res.MessageID != msgID1 {
		t.Fatalf("expected original message ID %s, got new ID %s", msgID1, res.MessageID)
	}
}

// -----------------------------------------------------------------------------
// 8. Hold Rollback On Persistence Failure
// -----------------------------------------------------------------------------
func TestSubmit_HoldRollbackOnPersistenceFailure(t *testing.T) {
	mk := newMsgKit(t)
	ctx := context.Background()

	wBefore, _ := mk.ledgerSvc.GetWallet(ctx, mk.e.Override, mk.walletID)

	// Simulate an atomic transaction where hold is called via PlaceHoldTx,
	// then an error occurs and tx is rolled back.
	tx, err := mk.e.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	refType := "message"
	refID := uuid.New()
	idempKey := "hold-abort-test"
	expiry := time.Now().Add(72 * time.Hour)

	_, err = mk.ledgerSvc.PlaceHoldTx(ctx, tx, mk.walletID, decimal.NewFromFloat(5.0), "USD", &refType, &refID, &idempKey, expiry)
	if err != nil {
		t.Fatalf("place hold tx: %v", err)
	}

	// Abort transaction
	_ = tx.Rollback(ctx)

	// Wallet active holds MUST remain unchanged
	wAfter, _ := mk.ledgerSvc.GetWallet(ctx, mk.e.Override, mk.walletID)
	if !wAfter.ActiveHolds.Equal(wBefore.ActiveHolds) {
		t.Fatalf("ROLLBACK FAILED: Wallet hold was not rolled back! Before=%v, After=%v",
			wBefore.ActiveHolds, wAfter.ActiveHolds)
	}
}

// -----------------------------------------------------------------------------
// 9. Tenant Isolation & Field Stripping
// -----------------------------------------------------------------------------
func TestTenant_FieldStrippingAndIsolation(t *testing.T) {
	mk := newMsgKit(t)
	ctx := context.Background()

	req := messaging.SubmitRequest{
		Destination: "+12025550199",
		SenderID:    "TESTSMS",
		Text:        "Confidential OTP: 981244",
	}
	res, err := mk.svc.Submit(ctx, mk.custUser, req)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// 1. Customer User WITH message.content.view
	viewCust, err := mk.svc.GetMessage(ctx, mk.custUser, res.MessageID, res.CreatedAt)
	if err != nil {
		t.Fatalf("customer get message: %v", err)
	}
	if viewCust.Body != req.Text {
		t.Fatalf("expected decrypted body %q, got %q", req.Text, viewCust.Body)
	}
	if viewCust.ProviderConnectionID != nil || viewCust.ActualBuyCost != nil || viewCust.Margin != nil {
		t.Fatal("SECURITY LEAK: Customer view exposes internal carrier ID or buy cost!")
	}

	// 2. Customer User WITHOUT message.content.view
	otherCustUser := mk.e.Actor("other-cust-user", mk.custOrgID, "", "customer", false, "message.view")
	viewMasked, err := mk.svc.GetMessage(ctx, otherCustUser, res.MessageID, res.CreatedAt)
	if err != nil {
		t.Fatalf("masked get message: %v", err)
	}
	if viewMasked.Body != "[ENCRYPTED - PERMISSION REQUIRED]" {
		t.Fatalf("expected masked body, got: %s", viewMasked.Body)
	}

	// 3. Platform Super Admin (sees everything)
	platformUser := mk.e.Actor("platform-admin", mk.e.PlatformOrgID, mk.e.PlatformOrgPath, "platform", true, "message.view", "message.content.view")
	viewPlatform, err := mk.svc.GetMessage(ctx, platformUser, res.MessageID, res.CreatedAt)
	if err != nil {
		t.Fatalf("platform get message: %v", err)
	}
	if viewPlatform.ProviderConnectionID == nil {
		t.Fatal("expected platform to see ProviderConnectionID")
	}

	// 4. Cross-Tenant Denial: Another customer gets 403
	alienCustOrg, alienPath := mk.e.ChildOrg(mk.e.PlatformOrgID, mk.e.PlatformOrgPath, "alien-cust", "customer")
	alienUser := mk.e.Actor("alien-user", alienCustOrg, alienPath, "customer", false, "message.view")
	_, err = mk.svc.GetMessage(ctx, alienUser, res.MessageID, res.CreatedAt)
	if err == nil || !errors.Is(err, tenant.ErrUnauthorizedTenant) {
		t.Fatalf("expected cross-tenant 403 ErrUnauthorizedTenant, got: %v", err)
	}
}

// -----------------------------------------------------------------------------
// 10. Phone Masking and Safe Logging
// -----------------------------------------------------------------------------
func TestLogs_MaskingAndSecretsSafety(t *testing.T) {
	phone := "+12025550199"
	masked := messaging.MaskPhone(phone)
	if masked != "+1202***0199" {
		t.Fatalf("expected '+1202***0199', got: %s", masked)
	}
	if strings.Contains(masked, "555") {
		t.Fatal("phone mask failed to redact middle digits")
	}
}

// -----------------------------------------------------------------------------
// 11. Partition Precreation and Default Partition (Amendment 3)
// -----------------------------------------------------------------------------
func TestPartitions_PrecreationAndDefaultPartition(t *testing.T) {
	mk := newMsgKit(t)
	ctx := context.Background()

	err := messaging.EnsurePartitions(ctx, mk.e.Pool, 3)
	if err != nil {
		t.Fatalf("ensure partitions: %v", err)
	}

	// Insert into far-future date (safely handled by messages_default)
	farFuture := time.Date(2035, 6, 15, 12, 0, 0, 0, time.UTC)
	futureMsgID := uuid.New()

	tx, err := mk.e.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)

	futureMsg := &messaging.Message{
		ID:                   futureMsgID,
		CreatedAt:            farFuture,
		CustomerOrgID:        mk.custOrgID,
		OriginalDestination: "+12025550199",
		Destination:          "+12025550199",
		SenderID:             "FUTURE",
		MessageType:          "sms",
		Encoding:             "GSM7",
		EncryptedBody:        []byte("future"),
		KeyID:                "v1",
		Status:               messaging.StatusAccepted,
		ExpiresAt:            farFuture.Add(24 * time.Hour),
		UpdatedAt:            farFuture,
	}

	if err := mk.repo.InsertMessageAtomic(ctx, tx, futureMsg, nil, nil, nil); err != nil {
		t.Fatalf("insert into default partition failed: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit future message: %v", err)
	}
}

// -----------------------------------------------------------------------------
// 12. Outbox & Dispatch: Accepted Outcome -> PROVIDER_ACCEPTED
// -----------------------------------------------------------------------------
func TestOutbox_RelayAndDispatch_AcceptedOutcome(t *testing.T) {
	mk := newMsgKit(t)
	ctx := context.Background()

	mk.fakeSender.SetDefaultOutcome(messaging.FakeSenderOutcomeConfig{
		Status:            messaging.SendStatusAccepted,
		ProviderMessageID: "smsc_ok_12345",
		BuyCost:           decimal.NewFromFloat(0.0018),
		Latency:           2 * time.Millisecond,
	})

	req := messaging.SubmitRequest{
		Destination: "+12025550199",
		SenderID:    "TESTSMS",
		Text:        "Test accepted outbox",
	}
	res, err := mk.svc.Submit(ctx, mk.custUser, req)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Dispatch outbox item
	processed := mk.dispatcher.ProcessBatch(ctx)
	if processed == 0 {
		t.Fatal("expected at least 1 outbox item to be processed")
	}

	// Message status should now be 'submitted' (PROVIDER_ACCEPTED)
	msg, _, err := mk.repo.GetMessage(ctx, res.MessageID, res.CreatedAt)
	if err != nil || msg == nil {
		t.Fatalf("get message: %v", err)
	}
	if msg.Status != messaging.StatusSubmitted {
		t.Fatalf("expected message status 'submitted', got %s", msg.Status)
	}
	if msg.ProviderMessageID == nil || *msg.ProviderMessageID != "smsc_ok_12345" {
		t.Fatalf("expected provider message ID 'smsc_ok_12345', got %v", msg.ProviderMessageID)
	}
}

// -----------------------------------------------------------------------------
// 13. Outbox: Failover on Retryable Rejection via Next()
// -----------------------------------------------------------------------------
func TestOutbox_FailoverOnRetryableRejection_NextCalled(t *testing.T) {
	mk := newMsgKit(t)
	ctx := context.Background()

	// Conn1 returns retryable reject (throttled)
	mk.fakeSender.ConfigureConnection(mk.provConnID1, messaging.FakeSenderOutcomeConfig{
		Status:       messaging.SendStatusRetryableReject,
		ErrorCode:    "ESME_RTHROTTLED",
		ErrorMessage: "provider queue full",
	})
	// Conn2 (fallback) accepts
	mk.fakeSender.ConfigureConnection(mk.provConnID2, messaging.FakeSenderOutcomeConfig{
		Status:            messaging.SendStatusAccepted,
		ProviderMessageID: "fallback_ok_999",
		BuyCost:           decimal.NewFromFloat(0.0022),
	})

	req := messaging.SubmitRequest{
		Destination: "+12025550199",
		SenderID:    "TESTSMS",
		Text:        "Test failover routing",
	}
	res, err := mk.svc.Submit(ctx, mk.custUser, req)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// First batch: processes conn1 -> rejects with retryable -> re-queues outbox for conn2
	_ = mk.dispatcher.ProcessBatch(ctx)

	// Second batch: processes fallback conn2 -> accepts
	_ = mk.dispatcher.ProcessBatch(ctx)

	msg, _, err := mk.repo.GetMessage(ctx, res.MessageID, res.CreatedAt)
	if err != nil || msg == nil {
		t.Fatalf("get message: %v", err)
	}
	if msg.Status != messaging.StatusSubmitted {
		t.Fatalf("expected message to reach 'submitted' on fallback route, got: %s", msg.Status)
	}
	if msg.ProviderConnectionID == nil || *msg.ProviderConnectionID != mk.provConnID2 {
		t.Fatalf("expected connection to switch to fallback conn2 (%s), got: %v", mk.provConnID2, msg.ProviderConnectionID)
	}
	// Sell price MUST remain unchanged
	if !msg.SellPrice.Equal(res.Price) {
		t.Fatalf("INVARIANT VIOLATION: Sell price changed on failover! Orig=%v, Now=%v", res.Price, msg.SellPrice)
	}
}

// -----------------------------------------------------------------------------
// 14. Outbox: Permanent Rejection -> No Failover & Hold Released
// -----------------------------------------------------------------------------
func TestOutbox_PermanentRejection_NoFailover_HoldReleased(t *testing.T) {
	mk := newMsgKit(t)
	ctx := context.Background()

	mk.fakeSender.SetDefaultOutcome(messaging.FakeSenderOutcomeConfig{
		Status:       messaging.SendStatusFatalReject,
		ErrorCode:    "ESME_RINVDSTADR",
		ErrorMessage: "invalid destination number",
	})

	req := messaging.SubmitRequest{
		Destination: "+12025550199",
		SenderID:    "TESTSMS",
		Text:        "Fatal reject test",
	}
	res, err := mk.svc.Submit(ctx, mk.custUser, req)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	wBefore, _ := mk.ledgerSvc.GetWallet(ctx, mk.e.Override, mk.walletID)

	_ = mk.dispatcher.ProcessBatch(ctx)

	msg, _, err := mk.repo.GetMessage(ctx, res.MessageID, res.CreatedAt)
	if err != nil || msg == nil {
		t.Fatalf("get message: %v", err)
	}
	if msg.Status != messaging.StatusFailed {
		t.Fatalf("expected message status 'failed', got: %s", msg.Status)
	}

	// Balance hold MUST be released on fatal reject
	wAfter, _ := mk.ledgerSvc.GetWallet(ctx, mk.e.Override, mk.walletID)
	if !wAfter.ActiveHolds.LessThan(wBefore.ActiveHolds) {
		t.Fatalf("expected hold to be released, before=%v after=%v", wBefore.ActiveHolds, wAfter.ActiveHolds)
	}
}

// -----------------------------------------------------------------------------
// 15. Outbox: Ambiguous Outcome -> UNKNOWN Status & NEVER Resent
// -----------------------------------------------------------------------------
func TestOutbox_AmbiguousOutcome_UnknownStatus_NeverResent(t *testing.T) {
	mk := newMsgKit(t)
	ctx := context.Background()

	mk.fakeSender.SetDefaultOutcome(messaging.FakeSenderOutcomeConfig{
		Status:       messaging.SendStatusUnknown,
		ErrorCode:    "TIMEOUT",
		ErrorMessage: "carrier socket read timeout",
	})

	req := messaging.SubmitRequest{
		Destination: "+12025550199",
		SenderID:    "TESTSMS",
		Text:        "Ambiguous test",
	}
	res, err := mk.svc.Submit(ctx, mk.custUser, req)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	wBefore, _ := mk.ledgerSvc.GetWallet(ctx, mk.e.Override, mk.walletID)

	_ = mk.dispatcher.ProcessBatch(ctx)

	msg, _, err := mk.repo.GetMessage(ctx, res.MessageID, res.CreatedAt)
	if err != nil || msg == nil {
		t.Fatalf("get message: %v", err)
	}
	if msg.Status != messaging.StatusUnknown {
		t.Fatalf("expected status 'unknown', got %s", msg.Status)
	}

	// Invariant: Customer balance hold is RETAINED (neither captured nor released)
	wAfter, _ := mk.ledgerSvc.GetWallet(ctx, mk.e.Override, mk.walletID)
	if !wAfter.ActiveHolds.Equal(wBefore.ActiveHolds) {
		t.Fatalf("INVARIANT VIOLATION: Hold was modified on ambiguous outcome! Before=%v after=%v",
			wBefore.ActiveHolds, wAfter.ActiveHolds)
	}

	// Ensure second outbox run does NOT re-send (NEVER auto-resend rule)
	mk.fakeSender.ClearAttempts()
	_ = mk.dispatcher.ProcessBatch(ctx)
	if len(mk.fakeSender.GetRecordedAttempts()) != 0 {
		t.Fatal("CRITICAL INVARIANT VIOLATION: Ambiguous message was auto-resent!")
	}
}

// -----------------------------------------------------------------------------
// 16. Outbox: Kill Switch Applied At Dispatch Time
// -----------------------------------------------------------------------------
func TestOutbox_KillSwitchAppliedAtDispatchTime(t *testing.T) {
	mk := newMsgKit(t)
	ctx := context.Background()

	req := messaging.SubmitRequest{
		Destination: "+12025550199",
		SenderID:    "TESTSMS",
		Text:        "Kill switch dispatch test",
	}
	res, err := mk.svc.Submit(ctx, mk.custUser, req)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Enable kill switch on primary connection before outbox dispatch
	_, err = mk.routingSvc.DisableConnection(ctx, mk.e.Override, mk.provConnID1, "emergency shutdown for test")
	if err != nil {
		t.Fatalf("disable connection: %v", err)
	}
	routing.TryReload(ctx, mk.e.Pool)

	mk.fakeSender.SetDefaultOutcome(messaging.FakeSenderOutcomeConfig{
		Status:            messaging.SendStatusAccepted,
		ProviderMessageID: "fallback_dispatched",
	})

	_ = mk.dispatcher.ProcessBatch(ctx)

	msg, _, err := mk.repo.GetMessage(ctx, res.MessageID, res.CreatedAt)
	if err != nil || msg == nil {
		t.Fatalf("get message: %v", err)
	}
	// The message must have been routed to conn2, not conn1
	if msg.ProviderConnectionID != nil && *msg.ProviderConnectionID == mk.provConnID1 {
		t.Fatalf("CRITICAL FLAW: Kill-switched connection %s was dispatched!", mk.provConnID1)
	}
}

// -----------------------------------------------------------------------------
// 17. Outbox: Worker Crash Mid-Flight & Recovery Without Duplicate Send
// -----------------------------------------------------------------------------
func TestOutbox_WorkerCrashMidFlight_NoDuplicateSend(t *testing.T) {
	mk := newMsgKit(t)
	ctx := context.Background()

	req := messaging.SubmitRequest{
		Destination: "+12025550199",
		SenderID:    "TESTSMS",
		Text:        "Worker crash test",
	}
	_, err := mk.svc.Submit(ctx, mk.custUser, req)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Worker 1 claims item with short lock (50ms) and crashes (exits without dispatch)
	items, err := mk.repo.FetchOutboxPending(ctx, 1, "worker-crashed", 50*time.Millisecond)
	if err != nil || len(items) == 0 {
		t.Fatalf("worker 1 claim failed: %v", err)
	}

	// Wait for lock to expire
	time.Sleep(75 * time.Millisecond)

	// Worker 2 starts and completes
	mk.fakeSender.ClearAttempts()
	_ = mk.dispatcher.ProcessBatch(ctx)

	if len(mk.fakeSender.GetRecordedAttempts()) != 1 {
		t.Fatalf("expected exactly 1 attempt after crash recovery, got %d", len(mk.fakeSender.GetRecordedAttempts()))
	}
}

// -----------------------------------------------------------------------------
// 18. Kafka Down: Ingestion Stays Durable & Backlog Drains Cleanly
// -----------------------------------------------------------------------------
func TestOutbox_KafkaDown_BacklogDrains(t *testing.T) {
	mk := newMsgKit(t)
	ctx := context.Background()

	// Submit 5 messages while dispatcher is stopped (simulating Kafka / network downtime)
	for i := 0; i < 5; i++ {
		_, err := mk.svc.Submit(ctx, mk.custUser, messaging.SubmitRequest{
			Destination: fmt.Sprintf("+120255501%02d", i),
			SenderID:    "TESTSMS",
			Text:        fmt.Sprintf("Backlog message %d", i),
		})
		if err != nil {
			t.Fatalf("submit %d failed during simulated downtime: %v", i, err)
		}
	}

	// Now dispatcher starts up and drains backlog
	processed := mk.dispatcher.ProcessBatch(ctx)
	if processed != 5 {
		t.Fatalf("expected all 5 messages to drain from outbox, got: %d", processed)
	}
}
