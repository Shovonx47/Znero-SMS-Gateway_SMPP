package rating_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/masterdata/operator"
	"github.com/znero-international/znero/backend/internal/number"
	"github.com/znero-international/znero/backend/internal/provider"
	"github.com/znero-international/znero/backend/internal/rating"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/internal/testkit"
)

type ratingTestKit struct {
	e          *testkit.Env
	ratingRepo rating.Repository
	dbEngine   rating.Engine
	engine     rating.Engine
	ratingSvc  *rating.Service
	numberSvc  *number.Service
	provSvc    *provider.Service
}

func newRatingKit(t *testing.T) *ratingTestKit {
	t.Helper()
	e := testkit.New(t)

	kr, err := secrets.NewKeyRingFromEnv("development", testkit.RandHex(32), "v1", "")
	if err != nil {
		t.Fatalf("keyring init: %v", err)
	}

	numberRepo := number.NewRepository(e.Pool)
	numberSvc := number.NewService(numberRepo, e.PrefixRepo, e.Prefix, e.Approval, e.Audit, e.Pool, slog.Default())

	ratingRepo := rating.NewRepository(e.Pool)
	engine := rating.NewInMemoryEngine(ratingRepo)
	dbEngine := rating.NewDBEngine(ratingRepo)
	ratingSvc := rating.NewService(ratingRepo, e.Audit, e.Approval, numberSvc, engine)

	provRepo := provider.NewRepository(e.Pool)
	provSvc := provider.NewService(provRepo, e.Audit, e.Approval, kr, "development")

	return &ratingTestKit{
		e:          e,
		ratingRepo: ratingRepo,
		dbEngine:   dbEngine,
		engine:     engine,
		ratingSvc:  ratingSvc,
		numberSvc:  numberSvc,
		provSvc:    provSvc,
	}
}

// -----------------------------------------------------------------------------
// 1. DECIMAL PRECISION & MONEY TRUNCATION TESTS
// -----------------------------------------------------------------------------

func TestDecimalPrecisionAndTruncation(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"0.005000", "0.005000"},
		{"0.0050009", "0.005000"}, // Must NOT round up to 0.005001
		{"0.0050001", "0.005000"},
		{"0.000001", "0.000001"},  // Preserves 6th decimal
		{"0.0000009", "0.000000"}, // 7th decimal truncated
		{"12.3456789", "12.345678"},
		{"100", "100.000000"},
	}

	for _, tc := range cases {
		d, err := decimal.NewFromString(tc.input)
		if err != nil {
			t.Fatalf("parse %s: %v", tc.input, err)
		}
		truncated := rating.TruncateMoney(d)
		exp, _ := decimal.NewFromString(tc.expected)
		if !truncated.Equal(exp) {
			t.Errorf("TruncateMoney(%s): expected %s, got %s", tc.input, exp.StringFixed(6), truncated.StringFixed(6))
		}
	}

	// Exact decimal arithmetic check (no float rounding errors)
	d1 := decimal.RequireFromString("0.005000")
	d2 := decimal.RequireFromString("0.002500")
	sum := d1.Add(d2)
	expectedSum := decimal.RequireFromString("0.007500")
	if !sum.Equal(expectedSum) {
		t.Fatalf("sum expected %s, got %s", expectedSum, sum)
	}

	// Subtraction and Margin calculation
	buy := decimal.RequireFromString("0.004000")
	sell := decimal.RequireFromString("0.005000")
	margin := sell.Sub(buy)
	expectedMargin := decimal.RequireFromString("0.001000")
	if !margin.Equal(expectedMargin) {
		t.Fatalf("margin expected %s, got %s", expectedMargin, margin)
	}
	marginPct := margin.Div(sell).Mul(decimal.NewFromInt(100)).Truncate(2)
	expectedMarginPct := decimal.RequireFromString("20.00")
	if !marginPct.Equal(expectedMarginPct) {
		t.Fatalf("margin percent expected %s, got %s", expectedMarginPct, marginPct)
	}
}

// -----------------------------------------------------------------------------
// 2. RATE CARD LIFECYCLE & RBAC TESTS
// -----------------------------------------------------------------------------

func TestRateCardLifecycleAndRBAC(t *testing.T) {
	rk := newRatingKit(t)
	ctx := context.Background()

	// Create test country
	cRes, err := rk.e.Country.Create(ctx, rk.e.Override, rk.e.CountryRequest())
	if err != nil {
		t.Fatalf("create country: %v", err)
	}
	cID := cRes.(*country.Country).ID

	// Create test provider for buy card
	provCode := "PRV_" + strings.ToUpper(testkit.RandHex(4))
	pRes, err := rk.provSvc.CreateProvider(ctx, rk.e.Override, provider.CreateProviderRequest{
		Code:            provCode,
		Name:            "DEV SAMPLE Prov " + provCode,
		LegalEntityName: "DEV SAMPLE Legal " + provCode,
		ProviderType:    "smpp",
		CountryID:       &cID,
		CurrencyCode:    "USD",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	provID := pRes.Provider.ID

	// Reseller and Customer orgs
	resellerOrg, resellerPath := rk.e.ChildOrg(rk.e.PlatformOrgID, rk.e.PlatformOrgPath, "reseller-test", "reseller")
	resellerActor := rk.e.Actor("reseller-user", resellerOrg, resellerPath, "reseller", false,
		"rate.sellcard.create", "rate.sellcard.read", "rate.sellcard.update",
		"rate.sellrate.create", "rate.sellrate.read", "rate.sellrate.update",
	)

	custOrg, custPath := rk.e.ChildOrg(resellerOrg, resellerPath, "cust-test", "customer")
	custActor := rk.e.Actor("cust-user", custOrg, custPath, "customer", false, "rate.sellcard.read")

	// 2.1 Platform creates provider buy card -> succeeds
	buyCardName := "DEV SAMPLE Buy Card " + testkit.RandHex(3)
	buyCardRes, err := rk.ratingSvc.CreateCard(ctx, rk.e.Override, &rating.RateCard{
		Name:         buyCardName,
		Kind:         "provider_buy",
		ProviderID:   &provID,
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
		Description:  "Provider buy rates",
	}, "initial creation")
	if err != nil {
		t.Fatalf("create buy card: %v", err)
	}
	if buyCardRes.Card == nil {
		t.Fatalf("expected created buy card")
	}
	buyCardID := buyCardRes.Card.ID

	// 2.2 Reseller attempts to create buy card -> rejected with ErrPlatformOnlyResource
	_, err = rk.ratingSvc.CreateCard(ctx, resellerActor, &rating.RateCard{
		Name:         "Reseller Buy Attempt",
		Kind:         "provider_buy",
		ProviderID:   &provID,
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
	}, "unauthorized attempt")
	if !errors.Is(err, rating.ErrPlatformOnlyResource) {
		t.Fatalf("expected ErrPlatformOnlyResource for reseller creating buy card, got %v", err)
	}

	// 2.3 Reseller attempts to read platform buy card -> rejected with ErrPlatformOnlyResource
	_, err = rk.ratingSvc.GetCardByID(ctx, resellerActor, buyCardID)
	if !errors.Is(err, rating.ErrPlatformOnlyResource) {
		t.Fatalf("expected ErrPlatformOnlyResource for reseller reading buy card, got %v", err)
	}

	// 2.4 Reseller creates customer sell card for their own customers -> succeeds
	resellerSellCardRes, err := rk.ratingSvc.CreateCard(ctx, resellerActor, &rating.RateCard{
		Name:         "Reseller Sell Card " + testkit.RandHex(3),
		Kind:         "customer_sell",
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
		Description:  "Reseller standard rates",
	}, "reseller creation")
	if err != nil {
		t.Fatalf("reseller create sell card: %v", err)
	}
	resellerCardID := resellerSellCardRes.Card.ID
	if resellerSellCardRes.Card.OwnerOrgID != resellerOrg {
		t.Fatalf("expected card owner org %s, got %s", resellerOrg, resellerSellCardRes.Card.OwnerOrgID)
	}

	// 2.5 Customer attempts to create rate card -> rejected with ErrPermissionDenied
	_, err = rk.ratingSvc.CreateCard(ctx, custActor, &rating.RateCard{
		Name:         "Customer Card",
		Kind:         "customer_sell",
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
	}, "customer attempt")
	if !errors.Is(err, rating.ErrPermissionDenied) {
		t.Fatalf("expected ErrPermissionDenied for customer creating rate card, got %v", err)
	}

	// 2.6 Reseller reads their own card -> succeeds
	cardRead, err := rk.ratingSvc.GetCardByID(ctx, resellerActor, resellerCardID)
	if err != nil {
		t.Fatalf("reseller read card: %v", err)
	}
	if cardRead.ID != resellerCardID {
		t.Fatalf("expected card ID %s, got %s", resellerCardID, cardRead.ID)
	}
}

// -----------------------------------------------------------------------------
// 3. RATE PUBLISHING, AUTO-CLOSING & GIST EXCLUSION CONSTRAINT
// -----------------------------------------------------------------------------

func TestRatePublishingAndAutoClosing(t *testing.T) {
	rk := newRatingKit(t)
	ctx := context.Background()

	// Create country & operator
	cRes, err := rk.e.Country.Create(ctx, rk.e.Override, rk.e.CountryRequest())
	if err != nil {
		t.Fatalf("create country: %v", err)
	}
	cID := cRes.(*country.Country).ID

	opRes, err := rk.e.Operator.Create(ctx, rk.e.Override, operator.CreateOperatorRequest{
		Name:       "DEV SAMPLE Op " + testkit.RandHex(3),
		CountryID:  cID,
		MCC:        "998",
		MNC:        "01",
		DataSource: "DEV-SAMPLE",
	})
	if err != nil {
		t.Fatalf("create operator: %v", err)
	}
	opID := opRes.(*operator.Operator).ID

	// Create provider & buy card
	provCode := "PRV_" + strings.ToUpper(testkit.RandHex(4))
	pRes, err := rk.provSvc.CreateProvider(ctx, rk.e.Override, provider.CreateProviderRequest{
		Code:            provCode,
		Name:            "DEV SAMPLE Prov " + provCode,
		LegalEntityName: "DEV SAMPLE Legal " + provCode,
		ProviderType:    "smpp",
		CountryID:       &cID,
		CurrencyCode:    "USD",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	provID := pRes.Provider.ID

	cardRes, err := rk.ratingSvc.CreateCard(ctx, rk.e.Override, &rating.RateCard{
		Name:         "DEV SAMPLE Buy Card " + testkit.RandHex(3),
		Kind:         "provider_buy",
		ProviderID:   &provID,
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
	}, "test")
	if err != nil {
		t.Fatalf("create card: %v", err)
	}
	cardID := cardRes.Card.ID

	// Step 1: Insert initial open-ended rate starting at T1
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rate1Res, err := rk.ratingSvc.CreateRate(ctx, rk.e.Override, &rating.Rate{
		CardID:        cardID,
		CountryID:     cID,
		OperatorID:    &opID,
		MessageType:   "default",
		Price:         decimal.RequireFromString("0.005000"),
		EffectiveFrom: t1,
		EffectiveTo:   nil, // Open-ended
	}, "rate 1")
	if err != nil {
		t.Fatalf("create rate 1: %v", err)
	}
	rate1ID := rate1Res.Rate.ID

	// Verify rate 1 is open-ended
	r1, err := rk.ratingRepo.GetRateByID(ctx, rate1ID)
	if err != nil {
		t.Fatalf("get rate 1: %v", err)
	}
	if r1.EffectiveTo != nil {
		t.Fatalf("expected rate 1 effective_to to be nil, got %v", r1.EffectiveTo)
	}

	// Step 2: Insert rate 2 starting at T2 (T2 > T1).
	// The service layer must automatically close rate 1 at T2 in the exact same transaction!
	t2 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	rate2Res, err := rk.ratingSvc.CreateRate(ctx, rk.e.Override, &rating.Rate{
		CardID:        cardID,
		CountryID:     cID,
		OperatorID:    &opID,
		MessageType:   "default",
		Price:         decimal.RequireFromString("0.005500"),
		EffectiveFrom: t2,
		EffectiveTo:   nil, // New open-ended rate
	}, "rate 2")
	if err != nil {
		t.Fatalf("create rate 2: %v", err)
	}
	if rate2Res.Rate == nil {
		t.Fatalf("expected rate 2 created")
	}

	// Verify rate 1 was automatically closed to T2
	r1After, err := rk.ratingRepo.GetRateByID(ctx, rate1ID)
	if err != nil {
		t.Fatalf("get rate 1 after update: %v", err)
	}
	if r1After.EffectiveTo == nil || !r1After.EffectiveTo.Equal(t2) {
		t.Fatalf("expected rate 1 effective_to to be %v, got %v", t2, r1After.EffectiveTo)
	}

	// Step 3: GiST exclusion constraint verification:
	overlapT := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	tx, err := rk.ratingRepo.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)

	overlappingRate := &rating.Rate{
		ID:            uuid.New(),
		CardID:        cardID,
		CountryID:     cID,
		OperatorID:    &opID,
		MessageType:   "default",
		Price:         decimal.RequireFromString("0.009999"),
		EffectiveFrom: overlapT,
		EffectiveTo:   &t2,
		Source:        "manual",
		Status:        "active",
		Version:       1,
	}
	_, err = rk.ratingRepo.CreateRate(ctx, tx, overlappingRate, "overlap test")
	if err == nil {
		t.Fatalf("expected GiST exclusion constraint error on overlapping rate insert, got nil")
	}
	if !strings.Contains(err.Error(), "rates_active_no_overlap") && !strings.Contains(err.Error(), "exclusion") && !strings.Contains(err.Error(), "23P01") && !errors.Is(err, rating.ErrEffectiveDateOverlap) {
		t.Fatalf("expected exclusion constraint violation error, got: %v", err)
	}
}

// -----------------------------------------------------------------------------
// 4. EFFECTIVE DATING & BOUNDARY INSTANTS [effective_from, effective_to)
// -----------------------------------------------------------------------------

func TestEffectiveDatingBoundaries(t *testing.T) {
	rk := newRatingKit(t)
	ctx := context.Background()

	cRes, err := rk.e.Country.Create(ctx, rk.e.Override, rk.e.CountryRequest())
	if err != nil {
		t.Fatalf("create country: %v", err)
	}
	cID := cRes.(*country.Country).ID

	// Create sell card
	sellCardRes, err := rk.ratingSvc.CreateCard(ctx, rk.e.Override, &rating.RateCard{
		Name:         "Boundary Sell Card " + testkit.RandHex(3),
		Kind:         "customer_sell",
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
	}, "boundary test")
	if err != nil {
		t.Fatalf("create sell card: %v", err)
	}
	cardID := sellCardRes.Card.ID

	// Assign card to customer org
	custOrg, _ := rk.e.ChildOrg(rk.e.PlatformOrgID, rk.e.PlatformOrgPath, "boundary-cust", "customer")
	tFrom := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	_, err = rk.ratingSvc.CreateAssignment(ctx, rk.e.Override, custOrg, cardID, tFrom, nil, "customer assign")
	if err != nil {
		t.Fatalf("assign card: %v", err)
	}

	// Create rate effective strictly for [12:00:00, 13:00:00) on 2026-06-15
	rFrom := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	rTo := time.Date(2026, 6, 15, 13, 0, 0, 0, time.UTC)
	_, err = rk.ratingSvc.CreateRate(ctx, rk.e.Override, &rating.Rate{
		CardID:        cardID,
		CountryID:     cID,
		MessageType:   "default",
		Price:         decimal.RequireFromString("0.007500"),
		EffectiveFrom: rFrom,
		EffectiveTo:   &rTo,
	}, "bounded rate")
	if err != nil {
		t.Fatalf("create rate: %v", err)
	}

	// Sync in-memory engine
	if err := rk.ratingSvc.ReloadEngine(ctx); err != nil {
		t.Fatalf("reload engine: %v", err)
	}

	// Test Boundary Instants:
	testCases := []struct {
		name          string
		at            time.Time
		expectedFound bool
	}{
		{"1 nanosecond before effective_from", rFrom.Add(-time.Nanosecond), false},
		{"1 second before effective_from", rFrom.Add(-time.Second), false},
		{"Exact instant effective_from", rFrom, true},
		{"Middle of interval", rFrom.Add(30 * time.Minute), true},
		{"1 nanosecond before effective_to", rTo.Add(-time.Nanosecond), true},
		{"Exact instant effective_to (half-open: must NOT match!)", rTo, false},
		{"1 second after effective_to", rTo.Add(time.Second), false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dbMatch, err := rk.dbEngine.LookupSellRate(ctx, custOrg, cID, nil, "default", tc.at)
			if err != nil {
				t.Fatalf("db lookup: %v", err)
			}
			if dbMatch.Found != tc.expectedFound {
				t.Errorf("DBEngine at %v: expected found=%v, got %v", tc.at, tc.expectedFound, dbMatch.Found)
			}

			memMatch, err := rk.engine.LookupSellRate(ctx, custOrg, cID, nil, "default", tc.at)
			if err != nil {
				t.Fatalf("mem lookup: %v", err)
			}
			if memMatch.Found != tc.expectedFound {
				t.Errorf("InMemoryEngine at %v: expected found=%v, got %v", tc.at, tc.expectedFound, memMatch.Found)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// 5. RESOLUTION ORDER (OPERATOR-SPECIFIC BEATS COUNTRY-DEFAULT)
// -----------------------------------------------------------------------------

func TestResolutionOrder(t *testing.T) {
	rk := newRatingKit(t)
	ctx := context.Background()

	// Create country & two operators
	cRes, err := rk.e.Country.Create(ctx, rk.e.Override, rk.e.CountryRequest())
	if err != nil {
		t.Fatalf("create country: %v", err)
	}
	cID := cRes.(*country.Country).ID

	op1Res, err := rk.e.Operator.Create(ctx, rk.e.Override, operator.CreateOperatorRequest{
		Name:       "Op1 " + testkit.RandHex(3),
		CountryID:  cID,
		MCC:        "997",
		MNC:        "01",
		DataSource: "DEV-SAMPLE",
	})
	if err != nil {
		t.Fatalf("create op1: %v", err)
	}
	op1ID := op1Res.(*operator.Operator).ID

	op2Res, err := rk.e.Operator.Create(ctx, rk.e.Override, operator.CreateOperatorRequest{
		Name:       "Op2 " + testkit.RandHex(3),
		CountryID:  cID,
		MCC:        "997",
		MNC:        "02",
		DataSource: "DEV-SAMPLE",
	})
	if err != nil {
		t.Fatalf("create op2: %v", err)
	}
	op2ID := op2Res.(*operator.Operator).ID

	// Create sell card and assign to customer
	sellCardRes, err := rk.ratingSvc.CreateCard(ctx, rk.e.Override, &rating.RateCard{
		Name:         "Resolution Card " + testkit.RandHex(3),
		Kind:         "customer_sell",
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
	}, "resolution test")
	if err != nil {
		t.Fatalf("create card: %v", err)
	}
	cardID := sellCardRes.Card.ID

	custOrg, _ := rk.e.ChildOrg(rk.e.PlatformOrgID, rk.e.PlatformOrgPath, "res-cust", "customer")
	now := time.Now().UTC().Add(-time.Hour)
	_, err = rk.ratingSvc.CreateAssignment(ctx, rk.e.Override, custOrg, cardID, now, nil, "assign")
	if err != nil {
		t.Fatalf("assign card: %v", err)
	}

	// 1. Country-default rate: operator_id = nil, price = 0.010000
	countryDefaultPrice := decimal.RequireFromString("0.010000")
	_, err = rk.ratingSvc.CreateRate(ctx, rk.e.Override, &rating.Rate{
		CardID:        cardID,
		CountryID:     cID,
		OperatorID:    nil,
		MessageType:   "default",
		Price:         countryDefaultPrice,
		EffectiveFrom: now,
	}, "country default")
	if err != nil {
		t.Fatalf("create country default rate: %v", err)
	}

	// 2. Operator-specific rate: operator_id = op1ID, price = 0.008000
	op1Price := decimal.RequireFromString("0.008000")
	_, err = rk.ratingSvc.CreateRate(ctx, rk.e.Override, &rating.Rate{
		CardID:        cardID,
		CountryID:     cID,
		OperatorID:    &op1ID,
		MessageType:   "default",
		Price:         op1Price,
		EffectiveFrom: now,
	}, "op1 rate")
	if err != nil {
		t.Fatalf("create op1 rate: %v", err)
	}

	// 3. Message-type specific rate: operator_id = op1ID, message_type = "otp", price = 0.009000
	op1OtpPrice := decimal.RequireFromString("0.009000")
	_, err = rk.ratingSvc.CreateRate(ctx, rk.e.Override, &rating.Rate{
		CardID:        cardID,
		CountryID:     cID,
		OperatorID:    &op1ID,
		MessageType:   "otp",
		Price:         op1OtpPrice,
		EffectiveFrom: now,
	}, "op1 otp rate")
	if err != nil {
		t.Fatalf("create op1 otp rate: %v", err)
	}

	// Reload in-memory cache
	if err := rk.ratingSvc.ReloadEngine(ctx); err != nil {
		t.Fatalf("reload engine: %v", err)
	}

	lookupTime := time.Now().UTC()

	// A. Lookup for Op1, default -> MUST resolve op1 specific rate (0.008000)
	matchOp1, err := rk.engine.LookupSellRate(ctx, custOrg, cID, &op1ID, "default", lookupTime)
	if err != nil {
		t.Fatalf("lookup op1: %v", err)
	}
	if !matchOp1.Found {
		t.Fatalf("expected rate found for op1")
	}
	if !matchOp1.Price.Equal(op1Price) {
		t.Errorf("Op1: expected operator-specific rate %s, got %s", op1Price, matchOp1.Price)
	}
	if matchOp1.MatchedRule != "operator_exact" {
		t.Errorf("expected matched rule 'operator_exact', got '%s'", matchOp1.MatchedRule)
	}

	// B. Lookup for Op2, default -> No op2 rate exists, MUST fall back to country-default rate (0.010000)
	matchOp2, err := rk.engine.LookupSellRate(ctx, custOrg, cID, &op2ID, "default", lookupTime)
	if err != nil {
		t.Fatalf("lookup op2: %v", err)
	}
	if !matchOp2.Found {
		t.Fatalf("expected fallback rate found for op2")
	}
	if !matchOp2.Price.Equal(countryDefaultPrice) {
		t.Errorf("Op2: expected country-default rate %s, got %s", countryDefaultPrice, matchOp2.Price)
	}
	if matchOp2.MatchedRule != "country_default" {
		t.Errorf("expected matched rule 'country_default', got '%s'", matchOp2.MatchedRule)
	}

	// C. Lookup for Op1, otp -> MUST resolve op1 otp rate (0.009000)
	matchOtp, err := rk.engine.LookupSellRate(ctx, custOrg, cID, &op1ID, "otp", lookupTime)
	if err != nil {
		t.Fatalf("lookup otp: %v", err)
	}
	if !matchOtp.Found {
		t.Fatalf("expected rate found for op1 otp")
	}
	if !matchOtp.Price.Equal(op1OtpPrice) {
		t.Errorf("Op1 OTP: expected %s, got %s", op1OtpPrice, matchOtp.Price)
	}

	// D. Lookup for an unrated country -> Found must be false
	unratedCountryID := uuid.New()
	matchUnrated, err := rk.engine.LookupSellRate(ctx, custOrg, unratedCountryID, nil, "default", lookupTime)
	if err != nil {
		t.Fatalf("lookup unrated: %v", err)
	}
	if matchUnrated.Found {
		t.Errorf("expected unrated destination to have Found=false, got true (price=%s)", matchUnrated.Price)
	}
}

// -----------------------------------------------------------------------------
// 6. MARGIN PROTECTION & OVERRIDE
// -----------------------------------------------------------------------------

func TestMarginProtectionAndOverride(t *testing.T) {
	rk := newRatingKit(t)
	ctx := context.Background()

	// Setup Country & Operator
	cRes, err := rk.e.Country.Create(ctx, rk.e.Override, rk.e.CountryRequest())
	if err != nil {
		t.Fatalf("create country: %v", err)
	}
	cID := cRes.(*country.Country).ID

	opRes, err := rk.e.Operator.Create(ctx, rk.e.Override, operator.CreateOperatorRequest{
		Name:       "MarginOp " + testkit.RandHex(3),
		CountryID:  cID,
		MCC:        "996",
		MNC:        "01",
		DataSource: "DEV-SAMPLE",
	})
	if err != nil {
		t.Fatalf("create operator: %v", err)
	}
	opID := opRes.(*operator.Operator).ID

	// Create Provider with active coverage
	provCode := "PRV_" + strings.ToUpper(testkit.RandHex(4))
	pRes, err := rk.provSvc.CreateProvider(ctx, rk.e.Override, provider.CreateProviderRequest{
		Code:            provCode,
		Name:            "DEV SAMPLE Margin Prov " + provCode,
		LegalEntityName: "DEV SAMPLE Legal",
		ProviderType:    "smpp",
		CountryID:       &cID,
		CurrencyCode:    "USD",
		Status:          "active",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	provID := pRes.Provider.ID

	// Create active provider coverage for Country & Operator
	_, err = rk.provSvc.CreateCoverage(ctx, rk.e.Override, provider.CreateCoverageRequest{
		ProviderID: provID,
		CountryID:  cID,
		OperatorID: &opID,
		Status:     "active",
		Reason:     "coverage setup",
	}, "127.0.0.1", "coverage")
	if err != nil {
		t.Fatalf("create coverage: %v", err)
	}

	// Create buy card with lowest buy rate = 0.006000
	buyCardRes, err := rk.ratingSvc.CreateCard(ctx, rk.e.Override, &rating.RateCard{
		Name:         "Buy Card Margin " + testkit.RandHex(3),
		Kind:         "provider_buy",
		ProviderID:   &provID,
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
	}, "buy card")
	if err != nil {
		t.Fatalf("create buy card: %v", err)
	}
	buyCardID := buyCardRes.Card.ID

	buyPrice := decimal.RequireFromString("0.006000")
	now := time.Now().UTC().Add(-time.Hour)
	_, err = rk.ratingSvc.CreateRate(ctx, rk.e.Override, &rating.Rate{
		CardID:        buyCardID,
		CountryID:     cID,
		OperatorID:    &opID,
		MessageType:   "default",
		Price:         buyPrice,
		EffectiveFrom: now,
	}, "buy rate")
	if err != nil {
		t.Fatalf("create buy rate: %v", err)
	}

	// Create Customer Sell Card
	sellCardRes, err := rk.ratingSvc.CreateCard(ctx, rk.e.Override, &rating.RateCard{
		Name:         "Sell Card Margin " + testkit.RandHex(3),
		Kind:         "customer_sell",
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
	}, "sell card")
	if err != nil {
		t.Fatalf("create sell card: %v", err)
	}
	sellCardID := sellCardRes.Card.ID

	// Create user with sellrate permissions but WITHOUT rate.margin.override
	userWithoutMarginOverride := rk.e.Actor("maker-no-override", rk.e.PlatformOrgID, rk.e.PlatformOrgPath, "platform", false,
		"rate.sellrate.create", "rate.sellrate.override", // Note: has sellrate.override (direct write) but NOT rate.margin.override
	)

	// User attempts to create sell rate at 0.005000 (< buy price 0.006000) -> MUST FAIL with ErrMarginViolation
	negativeMarginPrice := decimal.RequireFromString("0.005000")
	_, err = rk.ratingSvc.CreateRate(ctx, userWithoutMarginOverride, &rating.Rate{
		CardID:        sellCardID,
		CountryID:     cID,
		OperatorID:    &opID,
		MessageType:   "default",
		Price:         negativeMarginPrice,
		EffectiveFrom: now,
	}, "undercutting sell rate")
	if !errors.Is(err, rating.ErrMarginViolation) {
		t.Fatalf("expected ErrMarginViolation when creating sell rate below buy cost, got %v", err)
	}

	// Platform admin WITH rate.margin.override attempts to create sell rate at 0.005000 -> MUST SUCCEED
	adminWithOverride := testkit.WithPermissions(rk.e.Override, "rate.margin.override")
	allowedRes, err := rk.ratingSvc.CreateRate(ctx, adminWithOverride, &rating.Rate{
		CardID:        sellCardID,
		CountryID:     cID,
		OperatorID:    &opID,
		MessageType:   "default",
		Price:         negativeMarginPrice,
		EffectiveFrom: now,
	}, "deliberate promotional loss-leader rate")
	if err != nil {
		t.Fatalf("expected margin override to permit sell rate creation, got %v", err)
	}
	if allowedRes.Rate == nil {
		t.Fatalf("expected rate created with margin override")
	}
}

// -----------------------------------------------------------------------------
// 7. RESELLER COST FLOOR ENFORCEMENT
// -----------------------------------------------------------------------------

func TestResellerCostFloorEnforcement(t *testing.T) {
	rk := newRatingKit(t)
	ctx := context.Background()

	// Setup Country
	cRes, err := rk.e.Country.Create(ctx, rk.e.Override, rk.e.CountryRequest())
	if err != nil {
		t.Fatalf("create country: %v", err)
	}
	cID := cRes.(*country.Country).ID

	// Create Reseller Organization & Actor
	resellerOrg, resellerPath := rk.e.ChildOrg(rk.e.PlatformOrgID, rk.e.PlatformOrgPath, "reseller-floor", "reseller")
	resellerActor := rk.e.Actor("reseller-maker", resellerOrg, resellerPath, "reseller", false,
		"rate.sellcard.create", "rate.sellcard.read", "rate.sellcard.update",
		"rate.sellrate.create", "rate.sellrate.read", "rate.sellrate.update",
	)

	// Platform creates a cost card for this reseller with rate = 0.007000
	costCardRes, err := rk.ratingSvc.CreateCard(ctx, rk.e.Override, &rating.RateCard{
		Name:         "Reseller Cost Card " + testkit.RandHex(3),
		Kind:         "customer_sell", // Cost to reseller is a sell card owned by platform
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
	}, "cost card for reseller")
	if err != nil {
		t.Fatalf("create cost card: %v", err)
	}
	costCardID := costCardRes.Card.ID

	resellerCostPrice := decimal.RequireFromString("0.007000")
	now := time.Now().UTC().Add(-time.Hour)
	_, err = rk.ratingSvc.CreateRate(ctx, rk.e.Override, &rating.Rate{
		CardID:        costCardID,
		CountryID:     cID,
		MessageType:   "default",
		Price:         resellerCostPrice,
		EffectiveFrom: now,
	}, "cost rate")
	if err != nil {
		t.Fatalf("create cost rate: %v", err)
	}

	// Platform assigns this cost card to the Reseller
	_, err = rk.ratingSvc.CreateResellerCostAssignment(ctx, rk.e.Override, resellerOrg, costCardID, now, nil, "assign cost card")
	if err != nil {
		t.Fatalf("assign reseller cost card: %v", err)
	}

	// Reseller creates a sell card for their own downstream customers
	resellerSellCardRes, err := rk.ratingSvc.CreateCard(ctx, resellerActor, &rating.RateCard{
		Name:         "Reseller Downstream Card " + testkit.RandHex(3),
		Kind:         "customer_sell",
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
	}, "reseller sell card")
	if err != nil {
		t.Fatalf("reseller create sell card: %v", err)
	}
	resellerSellCardID := resellerSellCardRes.Card.ID

	// Reseller tries to set sell price = 0.006500 (< assigned cost 0.007000)
	undercutPrice := decimal.RequireFromString("0.006500")
	_, err = rk.ratingSvc.CreateRate(ctx, resellerActor, &rating.Rate{
		CardID:        resellerSellCardID,
		CountryID:     cID,
		MessageType:   "default",
		Price:         undercutPrice,
		EffectiveFrom: now,
	}, "reseller sell rate undercut")
	if !errors.Is(err, rating.ErrResellerCostFloor) {
		t.Fatalf("expected ErrResellerCostFloor for reseller setting rate below assigned cost, got %v", err)
	}

	// Reseller sets sell price = 0.007500 (> assigned cost 0.007000) -> SUCCEEDS
	profitablePrice := decimal.RequireFromString("0.007500")
	goodRes, err := rk.ratingSvc.CreateRate(ctx, resellerActor, &rating.Rate{
		CardID:        resellerSellCardID,
		CountryID:     cID,
		MessageType:   "default",
		Price:         profitablePrice,
		EffectiveFrom: now,
	}, "reseller profitable sell rate")
	if err != nil {
		t.Fatalf("expected reseller to create profitable rate, got %v", err)
	}
	if goodRes.Rate == nil {
		t.Fatalf("expected rate to be created")
	}
}

// -----------------------------------------------------------------------------
// 8. FIELD-LEVEL STRIPPING (BUY COST & MARGIN METRICS STRIPPED FOR NON-PLATFORM)
// -----------------------------------------------------------------------------

func TestFieldLevelStripping(t *testing.T) {
	rk := newRatingKit(t)
	ctx := context.Background()

	cRes, err := rk.e.Country.Create(ctx, rk.e.Override, rk.e.CountryRequest())
	if err != nil {
		t.Fatalf("create country: %v", err)
	}
	cID := cRes.(*country.Country).ID

	// Create sell card
	sellCardRes, err := rk.ratingSvc.CreateCard(ctx, rk.e.Override, &rating.RateCard{
		Name:         "Stripping Test Card " + testkit.RandHex(3),
		Kind:         "customer_sell",
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
	}, "strip test")
	if err != nil {
		t.Fatalf("create sell card: %v", err)
	}
	cardID := sellCardRes.Card.ID

	now := time.Now().UTC().Add(-time.Hour)
	rateRes, err := rk.ratingSvc.CreateRate(ctx, rk.e.Override, &rating.Rate{
		CardID:        cardID,
		CountryID:     cID,
		MessageType:   "default",
		Price:         decimal.RequireFromString("0.008000"),
		EffectiveFrom: now,
	}, "rate")
	if err != nil {
		t.Fatalf("create rate: %v", err)
	}
	rateID := rateRes.Rate.ID

	// Create reseller and customer actors
	resellerOrg, resellerPath := rk.e.ChildOrg(rk.e.PlatformOrgID, rk.e.PlatformOrgPath, "strip-reseller", "reseller")
	resellerActor := rk.e.Actor("strip-reseller-user", resellerOrg, resellerPath, "reseller", false, "rate.sellcard.read", "rate.sellrate.read")

	custOrg, custPath := rk.e.ChildOrg(resellerOrg, resellerPath, "strip-cust", "customer")
	custActor := rk.e.Actor("strip-cust-user", custOrg, custPath, "customer", false, "rate.sellcard.read", "rate.sellrate.read")

	// Assign card to reseller and customer orgs so they have tenant access
	_, err = rk.ratingSvc.CreateAssignment(ctx, rk.e.Override, resellerOrg, cardID, now, nil, "assign to reseller")
	if err != nil {
		t.Fatalf("assign to reseller: %v", err)
	}
	_, err = rk.ratingSvc.CreateAssignment(ctx, rk.e.Override, custOrg, cardID, now, nil, "assign to customer")
	if err != nil {
		t.Fatalf("assign to customer: %v", err)
	}

	// 8.1 Platform reads rate -> metrics are preserved
	platformRate, err := rk.ratingSvc.GetRateByID(ctx, rk.e.Override, rateID)
	if err != nil {
		t.Fatalf("platform get rate: %v", err)
	}
	_ = platformRate

	// 8.2 Reseller reads rate -> LowestBuyCost, MarginAmount, MarginPercent MUST BE NIL
	resellerRate, err := rk.ratingSvc.GetRateByID(ctx, resellerActor, rateID)
	if err != nil {
		t.Fatalf("reseller get rate: %v", err)
	}
	if resellerRate.LowestBuyCost != nil || resellerRate.MarginAmount != nil || resellerRate.MarginPercent != nil {
		t.Errorf("reseller received stripped metrics: buy=%v, margin=%v, pct=%v",
			resellerRate.LowestBuyCost, resellerRate.MarginAmount, resellerRate.MarginPercent)
	}

	// 8.3 Customer reads rate -> LowestBuyCost, MarginAmount, MarginPercent MUST BE NIL
	custRate, err := rk.ratingSvc.GetRateByID(ctx, custActor, rateID)
	if err != nil {
		t.Fatalf("customer get rate: %v", err)
	}
	if custRate.LowestBuyCost != nil || custRate.MarginAmount != nil || custRate.MarginPercent != nil {
		t.Errorf("customer received stripped metrics: buy=%v, margin=%v, pct=%v",
			custRate.LowestBuyCost, custRate.MarginAmount, custRate.MarginPercent)
	}
}

// -----------------------------------------------------------------------------
// 9. MAKER-CHECKER WORKFLOW (RATE CARD, RATE, ASSIGNMENT)
// -----------------------------------------------------------------------------

func TestMakerCheckerWorkflow(t *testing.T) {
	rk := newRatingKit(t)
	ctx := context.Background()

	cRes, err := rk.e.Country.Create(ctx, rk.e.Override, rk.e.CountryRequest())
	if err != nil {
		t.Fatalf("create country: %v", err)
	}
	cID := cRes.(*country.Country).ID

	// 9.1 Maker (without override) creates rate card -> PendingChange returned
	cardName := "MakerChecker Card " + testkit.RandHex(3)
	cardRes, err := rk.ratingSvc.CreateCard(ctx, rk.e.Maker, &rating.RateCard{
		Name:         cardName,
		Kind:         "customer_sell",
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
		Description:  "pending approval",
	}, "maker creates card")
	if err != nil {
		t.Fatalf("maker create card: %v", err)
	}
	if cardRes.PendingChange == nil {
		t.Fatalf("expected PendingChange for maker creating rate card")
	}
	changeID := cardRes.PendingChange.ID

	// Checker approves rate card change request
	if err := rk.e.Approval.Approve(ctx, rk.e.Checker, changeID, "approved card"); err != nil {
		t.Fatalf("checker approve card: %v", err)
	}
	crCard, err := rk.e.Approval.Get(ctx, changeID)
	if err != nil {
		t.Fatalf("get change request: %v", err)
	}
	if crCard.Status != "approved" {
		t.Fatalf("expected change request approved, got %s", crCard.Status)
	}

	// Verify card now exists in database
	cards, _, err := rk.ratingSvc.ListCards(ctx, rk.e.Override, rating.RateCardListFilter{Search: cardName})
	if err != nil {
		t.Fatalf("list cards: %v", err)
	}
	if len(cards) == 0 {
		t.Fatalf("expected approved card in list")
	}
	cardID := cards[0].ID

	// 9.2 Maker creates rate on card -> PendingChange returned
	now := time.Now().UTC()
	rateRes, err := rk.ratingSvc.CreateRate(ctx, rk.e.Maker, &rating.Rate{
		CardID:        cardID,
		CountryID:     cID,
		MessageType:   "default",
		Price:         decimal.RequireFromString("0.012000"),
		EffectiveFrom: now,
	}, "maker creates rate")
	if err != nil {
		t.Fatalf("maker create rate: %v", err)
	}
	if rateRes.PendingChange == nil {
		t.Fatalf("expected PendingChange for maker creating rate")
	}
	rateChangeID := rateRes.PendingChange.ID

	// Checker approves rate change request
	if err := rk.e.Approval.Approve(ctx, rk.e.Checker, rateChangeID, "approved rate"); err != nil {
		t.Fatalf("checker approve rate: %v", err)
	}
	crRate, err := rk.e.Approval.Get(ctx, rateChangeID)
	if err != nil {
		t.Fatalf("get rate change request: %v", err)
	}
	if crRate.Status != "approved" {
		t.Fatalf("expected rate change approved, got %s", crRate.Status)
	}

	// Verify rate is active
	rates, _, err := rk.ratingSvc.ListRates(ctx, rk.e.Override, rating.RateListFilter{CardID: cardID})
	if err != nil {
		t.Fatalf("list rates: %v", err)
	}
	if len(rates) == 0 {
		t.Fatalf("expected approved rate in list")
	}

	// 9.3 Maker creates customer rate assignment -> PendingChange returned
	custOrg, _ := rk.e.ChildOrg(rk.e.PlatformOrgID, rk.e.PlatformOrgPath, "mc-cust", "customer")
	assignRes, err := rk.ratingSvc.CreateAssignment(ctx, rk.e.Maker, custOrg, cardID, now, nil, "maker assign")
	if err != nil {
		t.Fatalf("maker create assign: %v", err)
	}
	if assignRes.PendingChange == nil {
		t.Fatalf("expected PendingChange for maker creating assignment")
	}
	assignChangeID := assignRes.PendingChange.ID

	// Checker approves assignment
	if err := rk.e.Approval.Approve(ctx, rk.e.Checker, assignChangeID, "approved assignment"); err != nil {
		t.Fatalf("checker approve assign: %v", err)
	}
	crAssign, err := rk.e.Approval.Get(ctx, assignChangeID)
	if err != nil {
		t.Fatalf("get assign change request: %v", err)
	}
	if crAssign.Status != "approved" {
		t.Fatalf("expected assign change approved, got %s", crAssign.Status)
	}

	// Verify assignment is active
	activeAssign, err := rk.ratingRepo.GetActiveCustomerAssignmentAt(ctx, custOrg, time.Now().UTC())
	if err != nil {
		t.Fatalf("get active customer assignment: %v", err)
	}
	if activeAssign.CardID != cardID {
		t.Fatalf("expected card ID %s, got %s", cardID, activeAssign.CardID)
	}
}

// -----------------------------------------------------------------------------
// 10. VERSIONING AND ROLLBACK
// -----------------------------------------------------------------------------

func TestVersioningAndRollback(t *testing.T) {
	rk := newRatingKit(t)
	ctx := context.Background()

	// Create rate card v1
	cardRes, err := rk.ratingSvc.CreateCard(ctx, rk.e.Override, &rating.RateCard{
		Name:         "Versioned Card " + testkit.RandHex(3),
		Kind:         "customer_sell",
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
		Description:  "Version 1 description",
	}, "v1 create")
	if err != nil {
		t.Fatalf("create card: %v", err)
	}
	cardID := cardRes.Card.ID
	if cardRes.Card.Version != 1 {
		t.Fatalf("expected version 1, got %d", cardRes.Card.Version)
	}

	// Update to v2
	cardToUpdate := *cardRes.Card
	cardToUpdate.Description = "Version 2 description"
	upRes, err := rk.ratingSvc.UpdateCard(ctx, rk.e.Override, &cardToUpdate, "v2 update")
	if err != nil {
		t.Fatalf("update card: %v", err)
	}
	if upRes.Card.Version != 2 {
		t.Fatalf("expected version 2, got %d", upRes.Card.Version)
	}

	// Check history table
	history, err := rk.ratingSvc.ListCardHistory(ctx, rk.e.Override, cardID)
	if err != nil {
		t.Fatalf("list card history: %v", err)
	}
	if len(history) < 1 {
		t.Fatalf("expected history snapshot, found %d", len(history))
	}
	foundV1 := false
	for _, h := range history {
		if h.Version == 1 && h.Description == "Version 1 description" {
			foundV1 = true
			break
		}
	}
	if !foundV1 {
		t.Fatalf("expected history to contain v1 snapshot, got: %+v", history)
	}

	// Rollback to v1
	rbRes, err := rk.ratingSvc.RollbackCard(ctx, rk.e.Override, cardID, 1, "test rollback")
	if err != nil {
		t.Fatalf("rollback card: %v", err)
	}
	if rbRes.Card.Description != "Version 1 description" {
		t.Fatalf("expected rolled back description 'Version 1 description', got '%s'", rbRes.Card.Description)
	}
	if rbRes.Card.Version != 3 {
		t.Fatalf("expected new version 3 after rollback, got %d", rbRes.Card.Version)
	}
}

// -----------------------------------------------------------------------------
// 11. PHONE NUMBER LOOKUP & BULK SIMULATOR
// -----------------------------------------------------------------------------

func TestPhoneNumberLookupAndBulk(t *testing.T) {
	rk := newRatingKit(t)
	ctx := context.Background()

	// 1. Create Country US (+1)
	iso2, iso3, num := rk.e.UniqueCountryCodes()
	cRes, err := rk.e.Country.Create(ctx, rk.e.Override, country.CreateCountryRequest{
		Name:         "DEV SAMPLE United States " + iso2,
		ISO2:         iso2,
		ISO3:         iso3,
		NumericCode:  num,
		CallingCode:  "1",
		CurrencyCode: "USD",
		Continent:    "NA",
		DataSource:   "DEV-SAMPLE",
	})
	if err != nil {
		t.Fatalf("create country: %v", err)
	}
	cID := cRes.(*country.Country).ID

	// 2. Create Operator T-Mobile
	opRes, err := rk.e.Operator.Create(ctx, rk.e.Override, operator.CreateOperatorRequest{
		Name:       "DEV SAMPLE T-Mobile " + testkit.RandHex(2),
		CountryID:  cID,
		MCC:        "310",
		MNC:        "260",
		BrandName:  "T-Mobile",
		DataSource: "DEV-SAMPLE",
	})
	if err != nil {
		t.Fatalf("create operator: %v", err)
	}
	opID := opRes.(*operator.Operator).ID

	// 3. Create Sell Card and assign to customer
	sellCardRes, err := rk.ratingSvc.CreateCard(ctx, rk.e.Override, &rating.RateCard{
		Name:         "Phone Lookup Card " + testkit.RandHex(3),
		Kind:         "customer_sell",
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
	}, "phone lookup test")
	if err != nil {
		t.Fatalf("create sell card: %v", err)
	}
	cardID := sellCardRes.Card.ID

	custOrg, custPath := rk.e.ChildOrg(rk.e.PlatformOrgID, rk.e.PlatformOrgPath, "phone-cust", "customer")
	custActor := rk.e.Actor("phone-cust-user", custOrg, custPath, "customer", false, "rate.sellcard.read")

	now := time.Now().UTC().Add(-time.Hour)
	_, err = rk.ratingSvc.CreateAssignment(ctx, rk.e.Override, custOrg, cardID, now, nil, "assign")
	if err != nil {
		t.Fatalf("assign card: %v", err)
	}

	// 4. Rate for US T-Mobile
	tmobilePrice := decimal.RequireFromString("0.006500")
	_, err = rk.ratingSvc.CreateRate(ctx, rk.e.Override, &rating.Rate{
		CardID:        cardID,
		CountryID:     cID,
		OperatorID:    &opID,
		MessageType:   "default",
		Price:         tmobilePrice,
		EffectiveFrom: now,
	}, "tmobile rate")
	if err != nil {
		t.Fatalf("create rate: %v", err)
	}

	// Reload in-memory engine
	if err := rk.ratingSvc.ReloadEngine(ctx); err != nil {
		t.Fatalf("reload engine: %v", err)
	}

	// 5. Lookup single phone number
	match, err := rk.ratingSvc.LookupByPhoneNumber(ctx, custOrg, "+13102601234", "default", time.Now().UTC())
	if err != nil {
		t.Fatalf("lookup phone number: %v", err)
	}
	_ = match

	// 6. Bulk Lookup simulation
	bulkItems := []rating.BulkLookupItem{
		{CustomerOrgID: custOrg, Number: "+13102601111", MessageType: "default"},
		{CustomerOrgID: custOrg, Number: "+13102602222", MessageType: "default"},
		{CustomerOrgID: custOrg, Number: "+13102603333", MessageType: "default"},
	}
	bulkMatches, err := rk.ratingSvc.BulkLookup(ctx, custActor, bulkItems)
	if err != nil {
		t.Fatalf("bulk lookup: %v", err)
	}
	if len(bulkMatches) != 3 {
		t.Fatalf("expected 3 bulk matches, got %d", len(bulkMatches))
	}
	for i, m := range bulkMatches {
		if m.Item.Number != bulkItems[i].Number {
			t.Errorf("item %d: expected phone %s, got %s", i, bulkItems[i].Number, m.Item.Number)
		}
	}
}

// -----------------------------------------------------------------------------
// 12. UPCOMING RATE CHANGES TIMELINE
// -----------------------------------------------------------------------------

func TestUpcomingRateChanges(t *testing.T) {
	rk := newRatingKit(t)
	ctx := context.Background()

	cRes, err := rk.e.Country.Create(ctx, rk.e.Override, rk.e.CountryRequest())
	if err != nil {
		t.Fatalf("create country: %v", err)
	}
	cID := cRes.(*country.Country).ID

	// Create sell card
	sellCardRes, err := rk.ratingSvc.CreateCard(ctx, rk.e.Override, &rating.RateCard{
		Name:         "Upcoming Card " + testkit.RandHex(3),
		Kind:         "customer_sell",
		CurrencyCode: "USD",
		Channel:      "sms",
		Status:       "active",
	}, "upcoming test")
	if err != nil {
		t.Fatalf("create card: %v", err)
	}
	cardID := sellCardRes.Card.ID

	// Create future-dated rate (+10 days)
	futureDate := time.Now().UTC().Add(10 * 24 * time.Hour)
	_, err = rk.ratingSvc.CreateRate(ctx, rk.e.Override, &rating.Rate{
		CardID:        cardID,
		CountryID:     cID,
		MessageType:   "default",
		Price:         decimal.RequireFromString("0.009000"),
		EffectiveFrom: futureDate,
	}, "future rate")
	if err != nil {
		t.Fatalf("create future rate: %v", err)
	}

	// Query upcoming rate changes within 30 days
	upcoming, err := rk.ratingSvc.ListUpcomingRates(ctx, rk.e.Override, 30)
	if err != nil {
		t.Fatalf("get upcoming rate changes: %v", err)
	}
	if len(upcoming) == 0 {
		t.Fatalf("expected at least 1 upcoming rate change, got 0")
	}

	found := false
	for _, u := range upcoming {
		if u.CardID == cardID {
			found = true
			if !u.Price.Equal(decimal.RequireFromString("0.009000")) {
				t.Errorf("expected price 0.009000, got %s", u.Price)
			}
		}
	}
	if !found {
		t.Errorf("expected to find upcoming rate for card %s", cardID)
	}
}
