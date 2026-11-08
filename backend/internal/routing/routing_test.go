package routing_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/routing"
	"github.com/znero-international/znero/backend/internal/testkit"
)

type routingKit struct {
	e             *testkit.Env
	routingRepo   routing.Repository
	routingSvc    *routing.Service
	routingEngine *routing.Engine
	countryID     uuid.UUID
	customerOrgID uuid.UUID
}

func newRoutingKit(t *testing.T) *routingKit {
	t.Helper()
	e := testkit.New(t)

	repo := routing.NewRepository(e.Pool)
	engine := routing.NewEngine(e.Pool, nil, nil, nil)
	svc := routing.NewService(repo, e.Approval, e.Audit, e.Pool, engine)

	// Ensure at least one country exists in test DB
	_, _ = e.Pool.Exec(context.Background(), `UPDATE routing_rules SET status = 'archived' WHERE conditions = '{}'::jsonb OR conditions = 'null'::jsonb`)
	var cID uuid.UUID
	err := e.Pool.QueryRow(context.Background(), `SELECT id FROM countries LIMIT 1`).Scan(&cID)
	if err != nil {
		cReq := e.CountryRequest()
		cRes, cErr := e.Country.Create(context.Background(), e.Override, cReq)
		if cErr != nil {
			t.Fatalf("create test country: %v", cErr)
		}
		cID = cRes.(*country.Country).ID
	}

	// Create test customer organization
	resellerOrg, resellerPath := e.ChildOrg(e.PlatformOrgID, e.PlatformOrgPath, "test-reseller", "reseller")
	custOrg, _ := e.ChildOrg(resellerOrg, resellerPath, "test-cust", "customer")

	return &routingKit{
		e:             e,
		routingRepo:   repo,
		routingSvc:    svc,
		routingEngine: engine,
		countryID:     cID,
		customerOrgID: custOrg,
	}
}

type rateWithWindow struct {
	Result        *routing.RateLookupResult
	EffectiveFrom *time.Time
	EffectiveTo   *time.Time
}

type mockRater struct {
	buyRates        map[uuid.UUID]*routing.RateLookupResult
	sellRates       map[uuid.UUID]*routing.RateLookupResult
	timedBuyRates   map[uuid.UUID][]rateWithWindow
	timedSellRates  map[uuid.UUID][]rateWithWindow
}

func (m *mockRater) LookupBuyRate(ctx context.Context, connectionID uuid.UUID, countryID uuid.UUID, operatorID *uuid.UUID, at time.Time) (*routing.RateLookupResult, error) {
	if m.timedBuyRates != nil {
		if list, ok := m.timedBuyRates[connectionID]; ok {
			for _, rw := range list {
				if rw.EffectiveFrom != nil && at.Before(*rw.EffectiveFrom) {
					continue
				}
				if rw.EffectiveTo != nil && at.After(*rw.EffectiveTo) {
					continue
				}
				return rw.Result, nil
			}
			return &routing.RateLookupResult{Found: false}, nil
		}
	}
	if m.buyRates != nil {
		if res, ok := m.buyRates[connectionID]; ok {
			return res, nil
		}
	}
	return &routing.RateLookupResult{Found: false}, nil
}

func (m *mockRater) LookupSellRate(ctx context.Context, customerOrgID uuid.UUID, countryID uuid.UUID, operatorID *uuid.UUID, at time.Time) (*routing.RateLookupResult, error) {
	if m.timedSellRates != nil {
		if list, ok := m.timedSellRates[customerOrgID]; ok {
			for _, rw := range list {
				if rw.EffectiveFrom != nil && at.Before(*rw.EffectiveFrom) {
					continue
				}
				if rw.EffectiveTo != nil && at.After(*rw.EffectiveTo) {
					continue
				}
				return rw.Result, nil
			}
			return &routing.RateLookupResult{Found: false}, nil
		}
	}
	if m.sellRates != nil {
		if res, ok := m.sellRates[customerOrgID]; ok {
			return res, nil
		}
	}
	return &routing.RateLookupResult{Found: false}, nil
}

type mockNumberer struct {
	countries map[string]uuid.UUID
}

func (m *mockNumberer) Lookup(destinationE164 string) (*routing.NumberInfo, error) {
	if m.countries != nil {
		if cID, ok := m.countries[destinationE164]; ok {
			return &routing.NumberInfo{CountryID: cID}, nil
		}
	}
	if !strings.HasPrefix(destinationE164, "+") {
		return nil, errors.New("invalid number")
	}
	return nil, nil
}

func (rk *routingKit) createProvider(t *testing.T, status string) uuid.UUID {
	t.Helper()
	code := "PRV_" + strings.ToUpper(testkit.RandHex(4))
	var id uuid.UUID
	err := rk.e.Pool.QueryRow(context.Background(), `
		INSERT INTO providers (name, code, provider_type, legal_entity_name, status, version)
		VALUES ($1, $2, 'smpp', 'Test Provider Legal', $3, 1)
		RETURNING id
	`, "Test Provider "+code, code, status).Scan(&id)
	if err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	return id
}

func (rk *routingKit) createConnection(t *testing.T, provID uuid.UUID, status string) uuid.UUID {
	t.Helper()
	name := "CONN_" + strings.ToUpper(testkit.RandHex(4))
	var id uuid.UUID
	err := rk.e.Pool.QueryRow(context.Background(), `
		INSERT INTO provider_connections (provider_id, name, protocol, status, version, max_tps)
		VALUES ($1, $2, 'smpp', $3, 1, 100)
		RETURNING id
	`, provID, name, status).Scan(&id)
	if err != nil {
		t.Fatalf("insert connection: %v", err)
	}
	return id
}

func (rk *routingKit) setCoverage(t *testing.T, provID, countryID uuid.UUID, status string) {
	t.Helper()
	_, err := rk.e.Pool.Exec(context.Background(), `
		INSERT INTO provider_coverage (provider_id, country_id, status, version)
		VALUES ($1, $2, $3, 1)
		ON CONFLICT (provider_id, country_id, COALESCE(operator_id, '00000000-0000-0000-0000-000000000000'::uuid))
		DO UPDATE SET status = EXCLUDED.status, updated_at = now()
	`, provID, countryID, status)
	if err != nil {
		t.Fatalf("set provider coverage: %v", err)
	}
}

// -----------------------------------------------------------------------------
// 1. DETERMINISM TESTS
// -----------------------------------------------------------------------------

func TestDeterminism_SameInputSameResult(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	conn1 := rk.createConnection(t, provID, "active")
	conn2 := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	plan, err := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:     "Test Plan Priority " + testkit.RandHex(3),
		Code:     "DET-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}

	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: conn1, Priority: 1, Weight: 100, Status: "active",
	})
	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: conn2, Priority: 2, Weight: 100, Status: "active",
	})

	_, _, err = rk.routingSvc.RequestPlanActivation(ctx, rk.e.Override, plan.ID)
	if err != nil {
		t.Fatalf("activate plan: %v", err)
	}

	_, _, err = rk.routingSvc.CreateAssignment(ctx, rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID,
		PlanID:        plan.ID,
		Priority:      1,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	routing.TryReload(ctx, rk.e.Pool)

	req := routing.ResolveRequest{
		CustomerOrgID:  rk.customerOrgID,
		DestinationE164: "+12025550100",
		CountryID:      &rk.countryID,
		SenderIDType:   "alphanumeric",
	}

	// 100 iterations must yield identical candidate ordering
	firstRes, err := rk.routingEngine.Resolve(ctx, req)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if len(firstRes.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(firstRes.Candidates))
	}

	for i := 1; i < 100; i++ {
		res, err := rk.routingEngine.Resolve(ctx, req)
		if err != nil {
			t.Fatalf("iteration %d resolve failed: %v", i, err)
		}
		if len(res.Candidates) != len(firstRes.Candidates) {
			t.Fatalf("iteration %d: candidate count mismatch", i)
		}
		for j := range res.Candidates {
			if res.Candidates[j].ConnectionID != firstRes.Candidates[j].ConnectionID {
				t.Fatalf("iteration %d: candidate %d differs (%s != %s)", i, j, res.Candidates[j].ConnectionID, firstRes.Candidates[j].ConnectionID)
			}
		}
	}
}

func TestDeterminism_WeightedWithFixedSeed(t *testing.T) {
	conn1 := uuid.New()
	conn2 := uuid.New()

	weightedCandidates := []routing.Candidate{
		{ConnectionID: conn1, Weight: 70},
		{ConnectionID: conn2, Weight: 30},
	}

	// Run two rounds with identical random seed
	runRound := func(seed int64) []uuid.UUID {
		r := rand.New(rand.NewSource(seed))
		selected := make([]uuid.UUID, 100)
		for i := 0; i < 100; i++ {
			totalWeight := 100
			roll := r.Intn(totalWeight)
			cumulative := 0
			for _, c := range weightedCandidates {
				cumulative += c.Weight
				if roll < cumulative {
					selected[i] = c.ConnectionID
					break
				}
			}
		}
		return selected
	}

	round1 := runRound(42)
	round2 := runRound(42)

	for i := range round1 {
		if round1[i] != round2[i] {
			t.Fatalf("fixed seed produced divergent selection at index %d", i)
		}
	}
}

func TestDeterminism_NoDatabaseQueryPerResolve(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	plan, err := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:     "No-DB Plan " + testkit.RandHex(3),
		Code:     "NODB-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}

	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: connID, Priority: 1, Weight: 100, Status: "active",
	})
	_, _, _ = rk.routingSvc.RequestPlanActivation(ctx, rk.e.Override, plan.ID)
	_, _, _ = rk.routingSvc.CreateAssignment(ctx, rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID,
		PlanID:        plan.ID,
		Priority:      1,
		Status:        "active",
	})

	routing.TryReload(ctx, rk.e.Pool)

	// Create a disconnected engine with nil pool
	offlineEngine := routing.NewEngine(nil, nil, nil, nil)

	req := routing.ResolveRequest{
		CustomerOrgID:  rk.customerOrgID,
		DestinationE164: "+12025550100",
		CountryID:      &rk.countryID,
	}

	// Resolve must succeed completely in memory without attempting any DB query
	res, err := offlineEngine.Resolve(ctx, req)
	if err != nil {
		t.Fatalf("offline resolve failed: %v", err)
	}
	if len(res.Candidates) == 0 {
		t.Fatalf("expected candidates from in-memory snapshot, got 0")
	}
}

// -----------------------------------------------------------------------------
// 2. ELIGIBILITY FILTERING TESTS
// -----------------------------------------------------------------------------

func TestEligibility_KillSwitchedExcluded(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	plan, _ := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:     "KillSwitch Test " + testkit.RandHex(3),
		Code:     "KS-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})
	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: connID, Priority: 1, Weight: 100, Status: "active",
	})
	_, _, _ = rk.routingSvc.RequestPlanActivation(ctx, rk.e.Override, plan.ID)
	_, _, _ = rk.routingSvc.CreateAssignment(ctx, rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID, PlanID: plan.ID, Priority: 1, Status: "active",
	})

	routing.TryReload(ctx, rk.e.Pool)

	req := routing.ResolveRequest{
		CustomerOrgID:  rk.customerOrgID,
		DestinationE164: "+12025550100",
		CountryID:      &rk.countryID,
	}

	// Initially active
	res1, err := rk.routingEngine.Resolve(ctx, req)
	if err != nil || len(res1.Candidates) == 0 {
		t.Fatalf("initial resolve failed: %v", err)
	}

	// Disable via kill switch
	_, err = rk.routingSvc.DisableConnection(ctx, rk.e.Override, connID, "Emergency vendor outage")
	if err != nil {
		t.Fatalf("disable connection: %v", err)
	}

	// Resolve must now exclude the connection
	res2, err := rk.routingEngine.Resolve(ctx, req)
	if err != nil {
		t.Fatalf("resolve after kill switch failed: %v", err)
	}
	if len(res2.Candidates) != 0 {
		t.Fatalf("expected 0 candidates, got %d", len(res2.Candidates))
	}
	foundExclusion := false
	for _, ex := range res2.Exclusions {
		if ex.ConnectionID == connID && (ex.ReasonCode == routing.ReasonKillSwitchActive || ex.ReasonCode == "kill_switched") {
			foundExclusion = true
			break
		}
	}
	if !foundExclusion {
		t.Fatalf("expected exclusion reason '%s' for connection %s", routing.ReasonKillSwitchActive, connID)
	}
}

func TestEligibility_InactiveConnectionExcluded(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	conn1 := rk.createConnection(t, provID, "active")
	conn2 := rk.createConnection(t, provID, "disabled")
	rk.setCoverage(t, provID, rk.countryID, "active")

	plan, _ := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:     "InactiveConn Test " + testkit.RandHex(3),
		Code:     "INACT-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})
	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: conn1, Priority: 1, Weight: 100, Status: "active",
	})
	// Add entry directly to avoid activation check
	_, err := rk.e.Pool.Exec(ctx, `
		INSERT INTO route_plan_entries (plan_id, connection_id, priority, weight, status, version)
		VALUES ($1, $2, 2, 100, 'active', 1)
	`, plan.ID, conn2)
	if err != nil {
		t.Fatalf("insert disabled connection entry: %v", err)
	}

	_, _, _ = rk.routingSvc.RequestPlanActivation(ctx, rk.e.Override, plan.ID)
	_, _, _ = rk.routingSvc.CreateAssignment(ctx, rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID, PlanID: plan.ID, Priority: 1, Status: "active",
	})

	routing.TryReload(ctx, rk.e.Pool)

	req := routing.ResolveRequest{
		CustomerOrgID:  rk.customerOrgID,
		DestinationE164: "+12025550100",
		CountryID:      &rk.countryID,
	}

	res, err := rk.routingEngine.Resolve(ctx, req)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	for _, c := range res.Candidates {
		if c.ConnectionID == conn2 {
			t.Fatalf("disabled connection %s should not be a candidate", conn2)
		}
	}
}

func TestEligibility_InactiveProviderExcluded(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "inactive")
	connID := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	plan, _ := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:     "InactiveProv Test " + testkit.RandHex(3),
		Code:     "INACTP-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})
	_, _ = rk.e.Pool.Exec(ctx, `
		INSERT INTO route_plan_entries (plan_id, connection_id, priority, weight, status, version)
		VALUES ($1, $2, 1, 100, 'active', 1)
	`, plan.ID, connID)
	_, _ = rk.e.Pool.Exec(ctx, `UPDATE route_plans SET status = 'active' WHERE id = $1`, plan.ID)
	_, _, _ = rk.routingSvc.CreateAssignment(ctx, rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID, PlanID: plan.ID, Priority: 1, Status: "active",
	})

	routing.TryReload(ctx, rk.e.Pool)

	req := routing.ResolveRequest{
		CustomerOrgID:  rk.customerOrgID,
		DestinationE164: "+12025550100",
		CountryID:      &rk.countryID,
	}

	res, err := rk.routingEngine.Resolve(ctx, req)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Fatalf("expected 0 candidates for inactive provider, got %d", len(res.Candidates))
	}
}

func TestEligibility_NoCoverageExcluded(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")
	// Set coverage to suspended (not active)
	rk.setCoverage(t, provID, rk.countryID, "suspended")

	plan, _ := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:     "NoCoverage Test " + testkit.RandHex(3),
		Code:     "NOCOV-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})
	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: connID, Priority: 1, Weight: 100, Status: "active",
	})
	_, _, _ = rk.routingSvc.RequestPlanActivation(ctx, rk.e.Override, plan.ID)
	_, _, _ = rk.routingSvc.CreateAssignment(ctx, rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID, PlanID: plan.ID, Priority: 1, Status: "active",
	})

	routing.TryReload(ctx, rk.e.Pool)

	req := routing.ResolveRequest{
		CustomerOrgID:  rk.customerOrgID,
		DestinationE164: "+12025550100",
		CountryID:      &rk.countryID,
	}

	res, err := rk.routingEngine.Resolve(ctx, req)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Fatalf("expected 0 candidates when provider lacks active coverage, got %d", len(res.Candidates))
	}
}

func TestEligibility_AllExcluded_ReturnsEmptyCandidates(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	plan, _ := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:     "AllExcluded Test " + testkit.RandHex(3),
		Code:     "ALLEX-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})
	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: connID, Priority: 1, Weight: 100, Status: "active",
		AllowedSenderIDTypes: []string{"alphanumeric"},
	})
	_, _, _ = rk.routingSvc.RequestPlanActivation(ctx, rk.e.Override, plan.ID)
	_, _, _ = rk.routingSvc.CreateAssignment(ctx, rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID, PlanID: plan.ID, Priority: 1, Status: "active",
	})

	routing.TryReload(ctx, rk.e.Pool)

	// Sender ID is numeric -> connection only allows alphanumeric -> excluded
	req := routing.ResolveRequest{
		CustomerOrgID:  rk.customerOrgID,
		DestinationE164: "+12025550100",
		CountryID:      &rk.countryID,
		SenderIDType:   "numeric",
	}

	res, err := rk.routingEngine.Resolve(ctx, req)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Fatalf("expected 0 candidates, got %d", len(res.Candidates))
	}
	if len(res.Exclusions) == 0 {
		t.Fatalf("expected exclusion reasons to be captured")
	}
}

// -----------------------------------------------------------------------------
// 3. STRATEGY EXECUTION TESTS
// -----------------------------------------------------------------------------

func TestStrategy_Priority_Order(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	conn1 := rk.createConnection(t, provID, "active")
	conn2 := rk.createConnection(t, provID, "active")
	conn3 := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	plan, _ := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:     "Strategy Priority " + testkit.RandHex(3),
		Code:     "STRPRI-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})

	// Add out of order: Priority 3, then 1, then 2
	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: conn3, Priority: 3, Weight: 100, Status: "active",
	})
	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: conn1, Priority: 1, Weight: 100, Status: "active",
	})
	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: conn2, Priority: 2, Weight: 100, Status: "active",
	})

	_, _, _ = rk.routingSvc.RequestPlanActivation(ctx, rk.e.Override, plan.ID)
	_, _, _ = rk.routingSvc.CreateAssignment(ctx, rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID, PlanID: plan.ID, Priority: 1, Status: "active",
	})

	routing.TryReload(ctx, rk.e.Pool)

	req := routing.ResolveRequest{
		CustomerOrgID:  rk.customerOrgID,
		DestinationE164: "+12025550100",
		CountryID:      &rk.countryID,
	}

	res, err := rk.routingEngine.Resolve(ctx, req)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if len(res.Candidates) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(res.Candidates))
	}
	if res.Candidates[0].ConnectionID != conn1 || res.Candidates[1].ConnectionID != conn2 || res.Candidates[2].ConnectionID != conn3 {
		t.Fatalf("candidates not sorted by priority ascending: got [%s, %s, %s]",
			res.Candidates[0].ConnectionID, res.Candidates[1].ConnectionID, res.Candidates[2].ConnectionID)
	}
}

func TestStrategy_LeastCost_ExactDecimal(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	conn1 := rk.createConnection(t, provID, "active")
	conn2 := rk.createConnection(t, provID, "active")
	conn3 := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	// Set rates with exact decimal values:
	// conn1: 0.0025, conn2: 0.0015, conn3: 0.0020
	// Sorted least cost should be: conn2 (0.0015), conn3 (0.0020), conn1 (0.0025)
	snap := routing.CurrentSnapshot()
	if snap == nil {
		routing.TryReload(ctx, rk.e.Pool)
		snap = routing.CurrentSnapshot()
	}

	plan, _ := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:     "Least Cost Decimal " + testkit.RandHex(3),
		Code:     "LCD-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyLeastCost,
	})

	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: conn1, Priority: 1, Weight: 100, Status: "active",
	})
	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: conn2, Priority: 1, Weight: 100, Status: "active",
	})
	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: conn3, Priority: 1, Weight: 100, Status: "active",
	})

	_, _, _ = rk.routingSvc.RequestPlanActivation(ctx, rk.e.Override, plan.ID)
	_, _, _ = rk.routingSvc.CreateAssignment(ctx, rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID, PlanID: plan.ID, Priority: 1, Status: "active",
	})

	routing.TryReload(ctx, rk.e.Pool)
	snap = routing.CurrentSnapshot()

	// Inject exact decimal costs into snapshot connections
	c1 := snap.Connections[conn1]
	c1.Cost = decimal.NewFromFloat(0.0025)
	snap.Connections[conn1] = c1

	c2 := snap.Connections[conn2]
	c2.Cost = decimal.NewFromFloat(0.0015)
	snap.Connections[conn2] = c2

	c3 := snap.Connections[conn3]
	c3.Cost = decimal.NewFromFloat(0.0020)
	snap.Connections[conn3] = c3

	req := routing.ResolveRequest{
		CustomerOrgID:  rk.customerOrgID,
		DestinationE164: "+12025550100",
		CountryID:      &rk.countryID,
	}

	res, err := rk.routingEngine.Resolve(ctx, req)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if len(res.Candidates) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(res.Candidates))
	}
	if res.Candidates[0].ConnectionID != conn2 {
		t.Fatalf("expected cheapest conn2 (0.0015) first, got %s", res.Candidates[0].ConnectionID)
	}
	if res.Candidates[1].ConnectionID != conn3 {
		t.Fatalf("expected second conn3 (0.0020), got %s", res.Candidates[1].ConnectionID)
	}
	if res.Candidates[2].ConnectionID != conn1 {
		t.Fatalf("expected third conn1 (0.0025), got %s", res.Candidates[2].ConnectionID)
	}
}

func TestStrategy_Weighted_AllCandidatesReachable(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	conn1 := rk.createConnection(t, provID, "active")
	conn2 := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	plan, _ := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:     "Weighted 70-30 " + testkit.RandHex(3),
		Code:     "W7030-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyWeighted,
	})

	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: conn1, Priority: 1, Weight: 70, Status: "active",
	})
	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: conn2, Priority: 1, Weight: 30, Status: "active",
	})

	_, _, _ = rk.routingSvc.RequestPlanActivation(ctx, rk.e.Override, plan.ID)
	_, _, _ = rk.routingSvc.CreateAssignment(ctx, rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID, PlanID: plan.ID, Priority: 1, Status: "active",
	})

	routing.TryReload(ctx, rk.e.Pool)

	req := routing.ResolveRequest{
		CustomerOrgID:  rk.customerOrgID,
		DestinationE164: "+12025550100",
		CountryID:      &rk.countryID,
	}

	count1 := 0
	count2 := 0
	iterations := 10000

	for i := 0; i < iterations; i++ {
		res, err := rk.routingEngine.Resolve(ctx, req)
		if err != nil {
			t.Fatalf("resolve failed at iteration %d: %v", i, err)
		}
		if len(res.Candidates) == 0 {
			t.Fatalf("no candidates at iteration %d", i)
		}
		if res.Candidates[0].ConnectionID == conn1 {
			count1++
		} else if res.Candidates[0].ConnectionID == conn2 {
			count2++
		}
	}

	// 70% expected for conn1, 30% for conn2 (+/- 4% statistical tolerance)
	pct1 := float64(count1) / float64(iterations) * 100.0
	pct2 := float64(count2) / float64(iterations) * 100.0

	if pct1 < 66.0 || pct1 > 74.0 {
		t.Fatalf("conn1 weighted distribution out of tolerance: got %.2f%%, expected ~70%%", pct1)
	}
	if pct2 < 26.0 || pct2 > 34.0 {
		t.Fatalf("conn2 weighted distribution out of tolerance: got %.2f%%, expected ~30%%", pct2)
	}
}

func TestStrategy_Failover_Next(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	conn1 := rk.createConnection(t, provID, "active")
	conn2 := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	plan, _ := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:     "Failover Next " + testkit.RandHex(3),
		Code:     "FAILNEXT-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})

	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: conn1, Priority: 1, Weight: 100, Status: "active",
	})
	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: conn2, Priority: 2, Weight: 100, Status: "active",
	})

	_, _, _ = rk.routingSvc.RequestPlanActivation(ctx, rk.e.Override, plan.ID)
	_, _, _ = rk.routingSvc.CreateAssignment(ctx, rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID, PlanID: plan.ID, Priority: 1, Status: "active",
	})

	routing.TryReload(ctx, rk.e.Pool)

	req := routing.ResolveRequest{
		CustomerOrgID:  rk.customerOrgID,
		DestinationE164: "+12025550100",
		CountryID:      &rk.countryID,
	}

	// First attempt gives conn1
	firstRes, err := rk.routingEngine.Resolve(ctx, req)
	if err != nil || len(firstRes.Candidates) < 2 {
		t.Fatalf("initial resolve: %v", err)
	}
	if firstRes.Candidates[0].ConnectionID != conn1 {
		t.Fatalf("expected candidate 1 = conn1, got %s", firstRes.Candidates[0].ConnectionID)
	}

	// Primary failed: call Next() excluding conn1
	nextCand, err := rk.routingEngine.Next(ctx, req, []uuid.UUID{conn1})
	if err != nil {
		t.Fatalf("Next() failed: %v", err)
	}
	if nextCand == nil {
		t.Fatalf("expected next candidate, got nil")
	}
	if nextCand.ConnectionID != conn2 {
		t.Fatalf("expected candidate 2 = conn2, got %s", nextCand.ConnectionID)
	}

	// Both failed: call Next() excluding conn1 and conn2 -> returns nil
	noCand, err := rk.routingEngine.Next(ctx, req, []uuid.UUID{conn1, conn2})
	if err != nil {
		t.Fatalf("Next() failed: %v", err)
	}
	if noCand != nil {
		t.Fatalf("expected nil when all candidates excluded, got %v", noCand)
	}
}

// -----------------------------------------------------------------------------
// 4. CONCURRENCY & SNAPSHOT TESTS
// -----------------------------------------------------------------------------

func TestSnapshot_AtomicSwapUnderConcurrentResolve(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	plan, _ := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:     "Atomic Concurrency " + testkit.RandHex(3),
		Code:     "CONC-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})
	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: connID, Priority: 1, Weight: 100, Status: "active",
	})
	_, _, _ = rk.routingSvc.RequestPlanActivation(ctx, rk.e.Override, plan.ID)
	_, _, _ = rk.routingSvc.CreateAssignment(ctx, rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID, PlanID: plan.ID, Priority: 1, Status: "active",
	})

	routing.TryReload(ctx, rk.e.Pool)

	req := routing.ResolveRequest{
		CustomerOrgID:  rk.customerOrgID,
		DestinationE164: "+12025550100",
		CountryID:      &rk.countryID,
	}

	var wg sync.WaitGroup
	stopChan := make(chan struct{})

	// 100 reader goroutines calling Resolve() continuously
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopChan:
					return
				default:
					_, err := rk.routingEngine.Resolve(ctx, req)
					if err != nil {
						t.Errorf("concurrent resolve error: %v", err)
						return
					}
				}
			}
		}()
	}

	// Concurrently reload snapshot 10 times
	for i := 0; i < 10; i++ {
		routing.TryReload(ctx, rk.e.Pool)
		time.Sleep(10 * time.Millisecond)
	}

	close(stopChan)
	wg.Wait()
}

func TestSnapshot_FailedReloadKeepsLastGood(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	plan, _ := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:     "LastGood Test " + testkit.RandHex(3),
		Code:     "LASTGOOD-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})
	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: connID, Priority: 1, Weight: 100, Status: "active",
	})
	_, _, _ = rk.routingSvc.RequestPlanActivation(ctx, rk.e.Override, plan.ID)
	_, _, _ = rk.routingSvc.CreateAssignment(ctx, rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID, PlanID: plan.ID, Priority: 1, Status: "active",
	})

	routing.TryReload(ctx, rk.e.Pool)
	snapBefore := routing.CurrentSnapshot()
	if snapBefore == nil {
		t.Fatalf("expected non-nil snapshot")
	}

	// Try reload with nil pool (simulates catastrophic DB failure)
	routing.TryReload(ctx, nil)

	snapAfter := routing.CurrentSnapshot()
	if snapAfter == nil {
		t.Fatalf("snapshot should not be wiped on failed reload")
	}
	if snapAfter.BuildAt != snapBefore.BuildAt {
		t.Fatalf("snapshot should remain identical to last good snapshot")
	}
}

// -----------------------------------------------------------------------------
// 5. KILL SWITCH TESTS
// -----------------------------------------------------------------------------

func TestKillSwitch_InstantAndAudited(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")

	reason := "Emergency disable: fiber cut"
	_, err := rk.routingSvc.DisableConnection(ctx, rk.e.Override, connID, reason)
	if err != nil {
		t.Fatalf("disable connection: %v", err)
	}

	// Verify snapshot has it instantly
	snap := routing.CurrentSnapshot()
	if snap == nil {
		t.Fatalf("nil snapshot")
	}
	override, ok := snap.Overrides[connID]
	if !ok || override.State != "disabled" {
		t.Fatalf("expected kill switch override state disabled in snapshot")
	}

	// Verify audit row exists
	auditRows := rk.e.AuditRows("provider_connection_override", connID.String())
	if len(auditRows) == 0 {
		t.Fatalf("expected audit row for connection_override")
	}
	foundAction := false
	for _, row := range auditRows {
		if row.Action == "routing.killswitch.disable" {
			foundAction = true
			break
		}
	}
	if !foundAction {
		t.Fatalf("expected routing.killswitch.disable audit action")
	}
}

func TestKillSwitch_ReenableRequiresApproval(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")

	// Disable first
	_, _ = rk.routingSvc.DisableConnection(ctx, rk.e.Override, connID, "Vendor maintenance")

	// Maker attempts re-enable -> must submit change request
	cr, err := rk.routingSvc.EnableConnection(ctx, rk.e.Maker, connID, "Maintenance completed")
	if err != nil {
		t.Fatalf("request enable connection: %v", err)
	}
	if cr == nil {
		t.Fatalf("maker should receive a pending change request")
	}

	// Maker cannot approve own request
	makerWithApprove := testkit.WithPermissions(rk.e.Maker, "routing.killswitch", "routing.plan.approve")
	err = rk.e.Approval.Approve(ctx, makerWithApprove, cr.ID, "Self approve attempt")
	if !errors.Is(err, approval.ErrMakerCannotApproveOwnChange) {
		t.Fatalf("expected ErrMakerCannotApproveOwnChange, got %v", err)
	}

	// Checker approves -> succeeds
	err = rk.e.Approval.Approve(ctx, rk.e.Checker, cr.ID, "Approved after verifying connection")
	if err != nil {
		t.Fatalf("checker approve failed: %v", err)
	}

	// Verify connection is removed from overrides table
	var count int
	_ = rk.e.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM provider_connection_overrides WHERE connection_id = $1`, connID).Scan(&count)
	if count != 0 {
		t.Fatalf("expected override row to be deleted after approval, found %d", count)
	}
}

// -----------------------------------------------------------------------------
// 6. MAKER-CHECKER TESTS
// -----------------------------------------------------------------------------

func TestMakerChecker_CannotSelfApprove_PlanActivate(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")

	plan, _ := rk.routingSvc.CreatePlan(ctx, rk.e.Maker, &routing.RoutePlan{
		Name:     "MC Plan Activate " + testkit.RandHex(3),
		Code:     "MC-ACT-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})
	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Maker, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: connID, Priority: 1, Weight: 100, Status: "active",
	})

	// Maker requests activation
	planRes, cr, err := rk.routingSvc.RequestPlanActivation(ctx, rk.e.Maker, plan.ID)
	if err != nil {
		t.Fatalf("request activation: %v", err)
	}
	if cr == nil {
		t.Fatalf("expected change request, got nil (status was %s)", planRes.Status)
	}

	// Maker cannot approve own request
	makerWithApprove := testkit.WithPermissions(rk.e.Maker, "routing.plan.approve")
	err = rk.e.Approval.Approve(ctx, makerWithApprove, cr.ID, "Self approve")
	if !errors.Is(err, approval.ErrMakerCannotApproveOwnChange) {
		t.Fatalf("expected ErrMakerCannotApproveOwnChange, got %v", err)
	}

	// Checker approves
	err = rk.e.Approval.Approve(ctx, rk.e.Checker, cr.ID, "Checker approved")
	if err != nil {
		t.Fatalf("checker approval failed: %v", err)
	}

	// Plan should now be active
	updatedPlan, _ := rk.routingSvc.GetPlan(ctx, rk.e.Maker, plan.ID)
	if updatedPlan.Status != routing.PlanActive {
		t.Fatalf("expected plan status active, got %s", updatedPlan.Status)
	}
}

func TestMakerChecker_CannotSelfApprove_PlanDeactivate(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")

	// Create and activate with Override user
	plan, _ := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:     "MC Plan Deactivate " + testkit.RandHex(3),
		Code:     "MC-DEACT-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})
	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: connID, Priority: 1, Weight: 100, Status: "active",
	})
	_, _, _ = rk.routingSvc.RequestPlanActivation(ctx, rk.e.Override, plan.ID)

	// Maker requests deactivation
	_, cr, err := rk.routingSvc.RequestPlanDeactivation(ctx, rk.e.Maker, plan.ID)
	if err != nil {
		t.Fatalf("request deactivation: %v", err)
	}
	if cr == nil {
		t.Fatalf("expected change request for maker deactivation")
	}

	// Maker cannot approve own request
	makerWithApprove := testkit.WithPermissions(rk.e.Maker, "routing.plan.approve")
	err = rk.e.Approval.Approve(ctx, makerWithApprove, cr.ID, "Self approve")
	if !errors.Is(err, approval.ErrMakerCannotApproveOwnChange) {
		t.Fatalf("expected ErrMakerCannotApproveOwnChange, got %v", err)
	}

	// Checker approves
	err = rk.e.Approval.Approve(ctx, rk.e.Checker, cr.ID, "Checker approved deactivation")
	if err != nil {
		t.Fatalf("checker approval failed: %v", err)
	}

	updatedPlan, _ := rk.routingSvc.GetPlan(ctx, rk.e.Maker, plan.ID)
	if updatedPlan.Status != routing.PlanDeactivated {
		t.Fatalf("expected plan status deactivated, got %s", updatedPlan.Status)
	}
}

func TestMakerChecker_CannotSelfApprove_RuleChange(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")

	// Maker creates rule -> creates change request
	_, cr, err := rk.routingSvc.CreateRule(ctx, rk.e.Maker, &routing.RoutingRule{
		Name:               "MC Rule " + testkit.RandHex(3),
		Priority:           1,
		Status:             "active",
		ActionType:         "prefer",
		ActionConnectionID: &connID,
		Conditions:         map[string]interface{}{"prefix": "+999" + testkit.RandHex(2)},
	})
	if err != nil {
		t.Fatalf("maker create rule: %v", err)
	}
	if cr == nil {
		t.Fatalf("expected change request for maker rule creation")
	}

	// Maker cannot approve
	makerWithApprove := testkit.WithPermissions(rk.e.Maker, "routing.rule.approve")
	err = rk.e.Approval.Approve(ctx, makerWithApprove, cr.ID, "Self approve")
	if !errors.Is(err, approval.ErrMakerCannotApproveOwnChange) {
		t.Fatalf("expected ErrMakerCannotApproveOwnChange, got %v", err)
	}

	// Checker approves
	err = rk.e.Approval.Approve(ctx, rk.e.Checker, cr.ID, "Checker approved rule")
	if err != nil {
		t.Fatalf("checker approval failed: %v", err)
	}
}

func TestMakerChecker_CannotSelfApprove_AssignmentChange(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")

	plan, _ := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:     "MC Assign Plan " + testkit.RandHex(3),
		Code:     "MC-ASN-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})
	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: connID, Priority: 1, Weight: 100, Status: "active",
	})
	_, _, _ = rk.routingSvc.RequestPlanActivation(ctx, rk.e.Override, plan.ID)

	// Maker creates assignment -> change request
	_, cr, err := rk.routingSvc.CreateAssignment(ctx, rk.e.Maker, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID,
		PlanID:        plan.ID,
		Priority:      1,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("maker create assignment: %v", err)
	}
	if cr == nil {
		t.Fatalf("expected change request for maker assignment")
	}

	// Maker cannot approve
	makerWithApprove := testkit.WithPermissions(rk.e.Maker, "routing.assignment.approve")
	err = rk.e.Approval.Approve(ctx, makerWithApprove, cr.ID, "Self approve")
	if !errors.Is(err, approval.ErrMakerCannotApproveOwnChange) {
		t.Fatalf("expected ErrMakerCannotApproveOwnChange, got %v", err)
	}

	// Checker approves
	err = rk.e.Approval.Approve(ctx, rk.e.Checker, cr.ID, "Checker approved assignment")
	if err != nil {
		t.Fatalf("checker approval failed: %v", err)
	}
}

// -----------------------------------------------------------------------------
// 7. ACTIVATION VALIDATION TESTS
// -----------------------------------------------------------------------------

func TestActivation_BlockedOnInactiveConnection(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "disabled") // inactive!

	plan, _ := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:     "Blocked Inactive Conn " + testkit.RandHex(3),
		Code:     "BLK-CONN-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})

	// Direct insert entry pointing to disabled connection
	_, err := rk.e.Pool.Exec(ctx, `
		INSERT INTO route_plan_entries (plan_id, connection_id, priority, weight, status, version)
		VALUES ($1, $2, 1, 100, 'active', 1)
	`, plan.ID, connID)
	if err != nil {
		t.Fatalf("insert entry: %v", err)
	}

	_, _, err = rk.routingSvc.RequestPlanActivation(ctx, rk.e.Override, plan.ID)
	if !errors.Is(err, routing.ErrInactiveConnection) {
		t.Fatalf("expected ErrInactiveConnection, got %v", err)
	}
}

func TestActivation_BlockedOnWeightSumNot100(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	conn1 := rk.createConnection(t, provID, "active")
	conn2 := rk.createConnection(t, provID, "active")

	plan, _ := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:     "Bad Weight Plan " + testkit.RandHex(3),
		Code:     "BAD-WT-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyWeighted,
	})

	// Weights sum to 80 (50 + 30) instead of 100
	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: conn1, Priority: 1, Weight: 50, Status: "active",
	})
	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: conn2, Priority: 1, Weight: 30, Status: "active",
	})

	_, _, err := rk.routingSvc.RequestPlanActivation(ctx, rk.e.Override, plan.ID)
	if !errors.Is(err, routing.ErrWeightSumMismatch) {
		t.Fatalf("expected ErrWeightSumMismatch, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// 8. PERMISSIONS TESTS (Resellers and Customers get 403 on everything)
// -----------------------------------------------------------------------------

func TestPermissions_ResellersGet403_Plans(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	_, _, err := rk.routingSvc.ListPlans(ctx, rk.e.Reseller, "", 1, 20)
	if !errors.Is(err, routing.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for reseller ListPlans, got %v", err)
	}

	_, err = rk.routingSvc.CreatePlan(ctx, rk.e.Reseller, &routing.RoutePlan{
		Name: "Forbidden Plan", Code: "FORB", Strategy: routing.StrategyPriority,
	})
	if !errors.Is(err, routing.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for reseller CreatePlan, got %v", err)
	}
}

func TestPermissions_ResellersGet403_Simulate(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	_, err := rk.routingSvc.Simulate(ctx, rk.e.Reseller, routing.SimulateRequest{
		DestinationE164: "+12025550100",
	})
	if !errors.Is(err, routing.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for reseller Simulate, got %v", err)
	}
}

func TestPermissions_CustomersGet403_Plans(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	_, _, err := rk.routingSvc.ListPlans(ctx, rk.e.Customer, "", 1, 20)
	if !errors.Is(err, routing.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for customer ListPlans, got %v", err)
	}
}

func TestPermissions_CustomersGet403_KillSwitch(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	_, err := rk.routingSvc.DisableConnection(ctx, rk.e.Customer, uuid.New(), "Unauthorized disable")
	if !errors.Is(err, routing.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for customer DisableConnection, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// 9. AUDIT & VERSION ROLLBACK TESTS
// -----------------------------------------------------------------------------

func TestAudit_FreeOfSecretsAndSimulationAudited(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	plan, _ := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:     "Audit Sanitization Test " + testkit.RandHex(3),
		Code:     "AUD-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})
	_, _, _ = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID: plan.ID, ConnectionID: connID, Priority: 1, Weight: 100, Status: "active",
	})
	_, _, _ = rk.routingSvc.RequestPlanActivation(ctx, rk.e.Override, plan.ID)
	_, _, _ = rk.routingSvc.CreateAssignment(ctx, rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID, PlanID: plan.ID, Priority: 1, Status: "active",
	})

	routing.TryReload(ctx, rk.e.Pool)

	// Run simulation
	simRes, err := rk.routingSvc.Simulate(ctx, rk.e.Override, routing.SimulateRequest{
		CustomerOrgID:   &rk.customerOrgID,
		DestinationE164: "+12025550100",
		CountryID:       &rk.countryID,
	})
	if err != nil {
		t.Fatalf("simulate failed: %v", err)
	}
	if simRes == nil {
		t.Fatalf("nil simulation result")
	}

	// Verify simulation audit row was recorded
	var simAuditCount int
	_ = rk.e.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM audit_logs WHERE action = 'routing.simulate'`).Scan(&simAuditCount)
	if simAuditCount == 0 {
		t.Fatalf("expected audit row for routing.simulate")
	}

	// Verify no secrets appear in audit table for this plan
	rows, err := rk.e.Pool.Query(ctx, `
		SELECT before_value::text, after_value::text FROM audit_logs WHERE module = 'routing' AND entity_id = $1
	`, plan.ID.String())
	if err != nil {
		t.Fatalf("query audit logs: %v", err)
	}
	defer rows.Close()

	forbiddenKeywords := []string{"password", "secret", "private_key", "token"}
	for rows.Next() {
		var before, after *string
		if err := rows.Scan(&before, &after); err != nil {
			t.Fatalf("scan audit row: %v", err)
		}
		for _, kw := range forbiddenKeywords {
			if before != nil && strings.Contains(strings.ToLower(*before), kw) {
				t.Fatalf("audit before_state leaked keyword %q: %s", kw, *before)
			}
			if after != nil && strings.Contains(strings.ToLower(*after), kw) {
				t.Fatalf("audit after_state leaked keyword %q: %s", kw, *after)
			}
		}
	}
}

func TestAudit_KillSwitchAudited(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")

	_, err := rk.routingSvc.DisableConnection(ctx, rk.e.Override, connID, "Kill switch test audit")
	if err != nil {
		t.Fatalf("disable connection: %v", err)
	}

	var count int
	_ = rk.e.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM audit_logs WHERE action = 'routing.killswitch.disable' AND entity_id = $1
	`, connID.String()).Scan(&count)
	if count == 0 {
		t.Fatalf("expected audit log for kill switch disable")
	}
}

func TestVersioning_RollbackPlan(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	plan, err := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:        "Original Name",
		Code:        "ORIG-" + strings.ToUpper(testkit.RandHex(4)),
		Description: "Initial description",
		Strategy:    routing.StrategyPriority,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}

	// Update plan
	plan.Name = "Updated Name"
	plan.Description = "Updated description"
	updated, err := rk.routingSvc.UpdatePlan(ctx, rk.e.Override, plan)
	if err != nil {
		t.Fatalf("update plan: %v", err)
	}
	if updated.Version != 2 {
		t.Fatalf("expected version 2 after update, got %d", updated.Version)
	}

	// History should have 2 entries
	histories, err := rk.routingSvc.GetPlanHistory(ctx, rk.e.Override, plan.ID)
	if err != nil || len(histories) < 2 {
		t.Fatalf("get plan history: %v (found %d)", err, len(histories))
	}

	// Rollback to version 1 (which was the "create" entry)
	var v1History *routing.RoutePlanHistory
	for i := range histories {
		if histories[i].ChangeType == "create" {
			v1History = &histories[i]
			break
		}
	}
	if v1History == nil {
		t.Fatalf("create history entry not found")
	}

	rolledBack, err := rk.routingSvc.RollbackPlan(ctx, rk.e.Override, plan.ID, v1History.ID)
	if err != nil {
		t.Fatalf("rollback plan: %v", err)
	}
	if rolledBack.Name != "Original Name" {
		t.Fatalf("expected name to be restored to 'Original Name', got %s", rolledBack.Name)
	}
}

func TestVersioning_RollbackEntry(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	provID := rk.createProvider(t, "active")
	conn1 := rk.createConnection(t, provID, "active")
	conn2 := rk.createConnection(t, provID, "active")

	plan, _ := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:     "Entry Rollback Plan " + testkit.RandHex(3),
		Code:     "EROLL-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})

	entry, _, err := rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID:       plan.ID,
		ConnectionID: conn1,
		Priority:     1,
		Weight:       100,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}

	// Update entry: change connection to conn2, priority 5
	entry.ConnectionID = conn2
	entry.Priority = 5
	updated, _, err := rk.routingSvc.UpdateEntry(ctx, rk.e.Override, entry)
	if err != nil {
		t.Fatalf("update entry: %v", err)
	}
	if updated.Version != 2 {
		t.Fatalf("expected version 2, got %d", updated.Version)
	}

	histories, err := rk.routingRepo.GetEntryHistory(ctx, entry.ID)
	if err != nil || len(histories) < 2 {
		t.Fatalf("expected >= 2 history records, got %d", len(histories))
	}

	var createHist *routing.RoutePlanEntryHistory
	for i := range histories {
		if histories[i].ChangeType == "create" {
			createHist = &histories[i]
			break
		}
	}
	if createHist == nil {
		t.Fatalf("create history record not found")
	}

	rolledBack, err := rk.routingSvc.RollbackEntry(ctx, rk.e.Override, entry.ID, createHist.ID)
	if err != nil {
		t.Fatalf("rollback entry: %v", err)
	}
	if rolledBack.ConnectionID != conn1 || rolledBack.Priority != 1 {
		t.Fatalf("entry rollback failed: conn=%s, prio=%d", rolledBack.ConnectionID, rolledBack.Priority)
	}
}

// -----------------------------------------------------------------------------
// 10. BENCHMARK
// -----------------------------------------------------------------------------

func BenchmarkResolve_RealisticDataset(b *testing.B) {
	// Setup an in-memory realistic snapshot with 50 plans and 500 entries
	custOrgID := uuid.New()
	countryID := uuid.New()
	targetPlanID := uuid.New()

	snap := &routing.RoutingSnapshot{
		BuildAt:     time.Now().UTC(),
		Overrides:   make(map[uuid.UUID]routing.ConnectionOverride),
		Connections: make(map[uuid.UUID]routing.SnapshotConnection),
		Coverage:    make(map[string]string),
		Plans:       make([]routing.RoutePlan, 0, 50),
		Assignments: []routing.CustomerRouteAssignment{
			{
				CustomerOrgID: custOrgID,
				PlanID:        targetPlanID,
				CountryID:     &countryID,
				Priority:      1,
				Status:        "active",
			},
		},
		Rules: []routing.RoutingRule{
			{
				ID:         uuid.New(),
				Name:       "Rule OTP",
				Priority:   1,
				Status:     "active",
				Conditions: map[string]interface{}{"traffic_profile": "otp"},
				ActionType: "prefer",
			},
		},
	}

	// 50 plans, each with 10 entries = 500 entries total
	for p := 0; p < 50; p++ {
		planID := uuid.New()
		if p == 0 {
			planID = targetPlanID
		}
		plan := routing.RoutePlan{
			ID:       planID,
			Name:     fmt.Sprintf("Benchmark Plan %d", p),
			Code:     fmt.Sprintf("BENCH-P%d", p),
			Strategy: routing.StrategyPriority,
			Status:   routing.PlanActive,
		}

		for e := 0; e < 10; e++ {
			connID := uuid.New()
			provID := uuid.New()

			snap.Connections[connID] = routing.SnapshotConnection{
				ID:               connID,
				ProviderID:       provID,
				ConnectionStatus: "active",
				ProviderStatus:   "active",
				Cost:             decimal.NewFromFloat(0.0010 + float64(e)*0.0001),
			}
			snap.Coverage[fmt.Sprintf("%s:%s", provID, countryID)] = "active"

			plan.Entries = append(plan.Entries, routing.RoutePlanEntry{
				ID:                      uuid.New(),
				PlanID:                  planID,
				ConnectionID:            connID,
				Priority:                e + 1,
				Weight:                  100,
				Status:                  "active",
				DestinationNumberFormat: "e164",
			})
		}
		snap.Plans = append(snap.Plans, plan)
	}

	routing.SwapSnapshot(snap)

	engine := routing.NewEngine(nil, nil, nil, nil)
	req := routing.ResolveRequest{
		CustomerOrgID:   custOrgID,
		DestinationE164: "+12025550100",
		CountryID:       &countryID,
		TrafficProfile:  "otp",
	}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := engine.Resolve(ctx, req)
		if err != nil {
			b.Fatalf("resolve: %v", err)
		}
	}
}

// -----------------------------------------------------------------------------
// Point 2 & 4: Exact Decimal Ordering & Reason Codes
// -----------------------------------------------------------------------------

func TestDecimals_ExactOrderingWithRate0004521(t *testing.T) {
	rk := newRoutingKit(t)
	provID := rk.createProvider(t, "active")
	connA := rk.createConnection(t, provID, "active")
	connB := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	rater := &mockRater{
		buyRates: map[uuid.UUID]*routing.RateLookupResult{
			connA: {Found: true, Price: decimal.RequireFromString("0.004521"), Currency: "USD"},
			connB: {Found: true, Price: decimal.RequireFromString("0.004520"), Currency: "USD"},
		},
		sellRates: map[uuid.UUID]*routing.RateLookupResult{
			rk.customerOrgID: {Found: true, Price: decimal.RequireFromString("0.010000"), Currency: "USD"},
		},
	}
	engine := routing.NewEngine(rk.e.Pool, rater, nil, nil)

	plan, err := rk.routingSvc.CreatePlan(context.Background(), rk.e.Override, &routing.RoutePlan{
		Name:     "Least Cost Exact Decimal Plan",
		Code:     "DECIMAL-TEST-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyLeastCost,
		Status:   routing.PlanDraft,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}

	_, _, _ = rk.routingSvc.CreateEntry(context.Background(), rk.e.Override, &routing.RoutePlanEntry{
		PlanID:       plan.ID,
		ConnectionID: connA,
		Priority:     1,
		Weight:       100,
		Status:       "active",
	})
	_, _, _ = rk.routingSvc.CreateEntry(context.Background(), rk.e.Override, &routing.RoutePlanEntry{
		PlanID:       plan.ID,
		ConnectionID: connB,
		Priority:     1,
		Weight:       100,
		Status:       "active",
	})
	_, _, _ = rk.routingSvc.RequestPlanActivation(context.Background(), rk.e.Override, plan.ID)

	_, _, _ = rk.routingSvc.CreateAssignment(context.Background(), rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID,
		PlanID:        plan.ID,
		Status:        "active",
	})

	_, err = routing.Reload(context.Background(), rk.e.Pool)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	res, err := engine.Resolve(context.Background(), routing.ResolveRequest{
		CustomerOrgID:   rk.customerOrgID,
		DestinationE164: "+12025550100",
		CountryID:       &rk.countryID,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(res.Candidates))
	}
	// ConnB (0.004520) must be ranked #1 ahead of ConnA (0.004521)
	if res.Candidates[0].ConnectionID != connB {
		t.Fatalf("expected ConnB (0.004520) to win least cost, but candidate #1 was %s", res.Candidates[0].ConnectionID)
	}
	if res.Candidates[1].ConnectionID != connA {
		t.Fatalf("expected ConnA (0.004521) to be ranked #2, but was %s", res.Candidates[1].ConnectionID)
	}
}

func TestStrategy_LeastCost_ExactTiesTieBrokenByPriorityThenConnID(t *testing.T) {
	rk := newRoutingKit(t)
	provID := rk.createProvider(t, "active")
	connP2 := rk.createConnection(t, provID, "active")
	connP1 := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	rater := &mockRater{
		buyRates: map[uuid.UUID]*routing.RateLookupResult{
			connP1: {Found: true, Price: decimal.RequireFromString("0.005000"), Currency: "USD"},
			connP2: {Found: true, Price: decimal.RequireFromString("0.005000"), Currency: "USD"},
		},
		sellRates: map[uuid.UUID]*routing.RateLookupResult{
			rk.customerOrgID: {Found: true, Price: decimal.RequireFromString("0.010000"), Currency: "USD"},
		},
	}
	engine := routing.NewEngine(rk.e.Pool, rater, nil, nil)

	plan, err := rk.routingSvc.CreatePlan(context.Background(), rk.e.Override, &routing.RoutePlan{
		Name:     "Least Cost Exact Ties Plan",
		Code:     "TIE-TEST-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyLeastCost,
		Status:   routing.PlanDraft,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}

	_, _, _ = rk.routingSvc.CreateEntry(context.Background(), rk.e.Override, &routing.RoutePlanEntry{
		PlanID:       plan.ID,
		ConnectionID: connP2,
		Priority:     2,
		Weight:       100,
		Status:       "active",
	})
	_, _, _ = rk.routingSvc.CreateEntry(context.Background(), rk.e.Override, &routing.RoutePlanEntry{
		PlanID:       plan.ID,
		ConnectionID: connP1,
		Priority:     1, // higher priority (lower number)
		Weight:       100,
		Status:       "active",
	})
	_, _, _ = rk.routingSvc.RequestPlanActivation(context.Background(), rk.e.Override, plan.ID)

	_, _, _ = rk.routingSvc.CreateAssignment(context.Background(), rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID,
		PlanID:        plan.ID,
		Status:        "active",
	})

	_, err = routing.Reload(context.Background(), rk.e.Pool)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	res, err := engine.Resolve(context.Background(), routing.ResolveRequest{
		CustomerOrgID:   rk.customerOrgID,
		DestinationE164: "+12025550100",
		CountryID:       &rk.countryID,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(res.Candidates))
	}
	// Even though buy rates are identical (0.005000), Priority 1 must win tie-break over Priority 2
	if res.Candidates[0].ConnectionID != connP1 {
		t.Fatalf("expected connP1 (Priority 1) to win tie-break over connP2 (Priority 2), got %s", res.Candidates[0].ConnectionID)
	}
}

func TestStrategy_Weighted_ExpectedProportions(t *testing.T) {
	snap := &routing.RoutingSnapshot{
		BuildAt:     time.Now().UTC(),
		Overrides:   make(map[uuid.UUID]routing.ConnectionOverride),
		Connections: make(map[uuid.UUID]routing.SnapshotConnection),
		Coverage:    make(map[string]string),
	}
	custOrgID := uuid.New()
	countryID := uuid.New()
	planID := uuid.New()

	conn70 := uuid.New()
	conn30 := uuid.New()
	provID := uuid.New()

	snap.Connections[conn70] = routing.SnapshotConnection{ID: conn70, ProviderID: provID, ConnectionStatus: "active", ProviderStatus: "active"}
	snap.Connections[conn30] = routing.SnapshotConnection{ID: conn30, ProviderID: provID, ConnectionStatus: "active", ProviderStatus: "active"}
	snap.Coverage[fmt.Sprintf("%s:%s", provID, countryID)] = "active"

	snap.Assignments = append(snap.Assignments, routing.CustomerRouteAssignment{
		CustomerOrgID: custOrgID,
		PlanID:        planID,
		Status:        "active",
	})

	snap.Plans = append(snap.Plans, routing.RoutePlan{
		ID:       planID,
		Code:     "WEIGHT-PROP",
		Strategy: routing.StrategyWeighted,
		Status:   routing.PlanActive,
		Entries: []routing.RoutePlanEntry{
			{ConnectionID: conn70, Weight: 70, Status: "active"},
			{ConnectionID: conn30, Weight: 30, Status: "active"},
		},
	})
	routing.SwapSnapshot(snap)

	// Fixed seed for deterministic repeatable proportion test
	fixedRand := rand.NewSource(123456789)
	engine := routing.NewEngine(nil, nil, nil, fixedRand)

	req := routing.ResolveRequest{
		CustomerOrgID:   custOrgID,
		DestinationE164: "+12025550100",
		CountryID:       &countryID,
	}

	count70 := 0
	count30 := 0
	const iterations = 10000

	for i := 0; i < iterations; i++ {
		res, err := engine.Resolve(context.Background(), req)
		if err != nil {
			t.Fatalf("resolve failed at iter %d: %v", i, err)
		}
		if res.Candidate.ConnectionID == conn70 {
			count70++
		} else if res.Candidate.ConnectionID == conn30 {
			count30++
		}
	}

	pct70 := float64(count70) / float64(iterations) * 100.0
	pct30 := float64(count30) / float64(iterations) * 100.0

	t.Logf("Weighted 70/30 distribution over %d runs: conn70=%.2f%%, conn30=%.2f%%", iterations, pct70, pct30)

	if pct70 < 67.0 || pct70 > 73.0 {
		t.Fatalf("expected ~70%%, got %.2f%%", pct70)
	}
	if pct30 < 27.0 || pct30 > 33.0 {
		t.Fatalf("expected ~30%%, got %.2f%%", pct30)
	}
}

func TestEligibility_MissingSellRateExcluded(t *testing.T) {
	rk := newRoutingKit(t)
	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	rater := &mockRater{
		buyRates: map[uuid.UUID]*routing.RateLookupResult{
			connID: {Found: true, Price: decimal.RequireFromString("0.005000"), Currency: "USD"},
		},
		sellRates: map[uuid.UUID]*routing.RateLookupResult{}, // empty: no sell rate!
	}
	engine := routing.NewEngine(rk.e.Pool, rater, nil, nil)

	plan, _ := rk.routingSvc.CreatePlan(context.Background(), rk.e.Override, &routing.RoutePlan{
		Name:     "Missing Sell Rate Plan",
		Code:     "NO-SELL-RATE-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
		Status:   routing.PlanDraft,
	})
	_, _, _ = rk.routingSvc.CreateEntry(context.Background(), rk.e.Override, &routing.RoutePlanEntry{
		PlanID:       plan.ID,
		ConnectionID: connID,
		Priority:     1,
		Weight:       100,
		Status:       "active",
	})
	_, _, _ = rk.routingSvc.RequestPlanActivation(context.Background(), rk.e.Override, plan.ID)
	_, _, _ = rk.routingSvc.CreateAssignment(context.Background(), rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID,
		PlanID:        plan.ID,
		Status:        "active",
	})
	_, _ = routing.Reload(context.Background(), rk.e.Pool)

	res, err := engine.Resolve(context.Background(), routing.ResolveRequest{
		CustomerOrgID:   rk.customerOrgID,
		DestinationE164: "+12025550100",
		CountryID:       &rk.countryID,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Fatalf("expected candidate to be excluded due to missing sell rate, got %d candidates", len(res.Candidates))
	}
	foundReason := false
	for _, exc := range res.Excluded {
		if exc.ReasonCode == routing.ReasonNoSellRate {
			foundReason = true
			break
		}
	}
	if !foundReason {
		t.Fatalf("expected reason code %s in exclusions, got: %+v", routing.ReasonNoSellRate, res.Excluded)
	}
}

func TestEligibility_CurrencyMismatchExcluded(t *testing.T) {
	rk := newRoutingKit(t)
	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	rater := &mockRater{
		buyRates: map[uuid.UUID]*routing.RateLookupResult{
			connID: {Found: true, Price: decimal.RequireFromString("0.005000"), Currency: "USD"},
		},
		sellRates: map[uuid.UUID]*routing.RateLookupResult{
			rk.customerOrgID: {Found: true, Price: decimal.RequireFromString("0.010000"), Currency: "EUR"},
		},
	}
	engine := routing.NewEngine(rk.e.Pool, rater, nil, nil)

	plan, _ := rk.routingSvc.CreatePlan(context.Background(), rk.e.Override, &routing.RoutePlan{
		Name:     "Currency Mismatch Plan",
		Code:     "CURR-MISMATCH-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
		Status:   routing.PlanDraft,
	})
	_, _, _ = rk.routingSvc.CreateEntry(context.Background(), rk.e.Override, &routing.RoutePlanEntry{
		PlanID:       plan.ID,
		ConnectionID: connID,
		Priority:     1,
		Weight:       100,
		Status:       "active",
	})
	_, _, _ = rk.routingSvc.RequestPlanActivation(context.Background(), rk.e.Override, plan.ID)
	_, _, _ = rk.routingSvc.CreateAssignment(context.Background(), rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID,
		PlanID:        plan.ID,
		Status:        "active",
	})
	_, _ = routing.Reload(context.Background(), rk.e.Pool)

	res, err := engine.Resolve(context.Background(), routing.ResolveRequest{
		CustomerOrgID:   rk.customerOrgID,
		DestinationE164: "+12025550100",
		CountryID:       &rk.countryID,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Fatalf("expected candidate to be excluded due to currency mismatch, got %d candidates", len(res.Candidates))
	}
	foundReason := false
	for _, exc := range res.Excluded {
		if exc.ReasonCode == routing.ReasonCurrencyMismatch {
			foundReason = true
			break
		}
	}
	if !foundReason {
		t.Fatalf("expected reason code %s in exclusions, got: %+v", routing.ReasonCurrencyMismatch, res.Excluded)
	}
}

func TestEligibility_MarginBelowMinimumExcluded(t *testing.T) {
	rk := newRoutingKit(t)
	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	minMargin := decimal.RequireFromString("0.005000")
	rater := &mockRater{
		buyRates: map[uuid.UUID]*routing.RateLookupResult{
			connID: {Found: true, Price: decimal.RequireFromString("0.009000"), Currency: "USD"},
		},
		sellRates: map[uuid.UUID]*routing.RateLookupResult{
			rk.customerOrgID: {Found: true, Price: decimal.RequireFromString("0.010000"), Currency: "USD"},
		},
	}
	engine := routing.NewEngine(rk.e.Pool, rater, nil, nil)

	plan, _ := rk.routingSvc.CreatePlan(context.Background(), rk.e.Override, &routing.RoutePlan{
		Name:     "Margin Deficit Plan",
		Code:     "MARGIN-DEFICIT-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
		Status:   routing.PlanDraft,
	})
	_, _, _ = rk.routingSvc.CreateEntry(context.Background(), rk.e.Override, &routing.RoutePlanEntry{
		PlanID:            plan.ID,
		ConnectionID:      connID,
		Priority:          1,
		Weight:            100,
		Status:            "active",
		MinMarginOverride: &minMargin,
	})
	_, _, _ = rk.routingSvc.RequestPlanActivation(context.Background(), rk.e.Override, plan.ID)
	_, _, _ = rk.routingSvc.CreateAssignment(context.Background(), rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID,
		PlanID:        plan.ID,
		Status:        "active",
	})
	_, _ = routing.Reload(context.Background(), rk.e.Pool)

	res, err := engine.Resolve(context.Background(), routing.ResolveRequest{
		CustomerOrgID:   rk.customerOrgID,
		DestinationE164: "+12025550100",
		CountryID:       &rk.countryID,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Fatalf("expected candidate to be excluded due to margin below minimum, got %d candidates", len(res.Candidates))
	}
	foundReason := false
	for _, exc := range res.Excluded {
		if exc.ReasonCode == routing.ReasonMarginBelowMinimum {
			foundReason = true
			break
		}
	}
	if !foundReason {
		t.Fatalf("expected reason code %s in exclusions, got: %+v", routing.ReasonMarginBelowMinimum, res.Excluded)
	}
}

func TestActivation_BlockedOnOverlappingDuplicateEntries(t *testing.T) {
	rk := newRoutingKit(t)
	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")

	plan, _ := rk.routingSvc.CreatePlan(context.Background(), rk.e.Override, &routing.RoutePlan{
		Name:     "Duplicate Entries Plan",
		Code:     "DUP-PLAN-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
		Status:   routing.PlanDraft,
	})

	_, _, _ = rk.routingSvc.CreateEntry(context.Background(), rk.e.Override, &routing.RoutePlanEntry{
		PlanID:       plan.ID,
		ConnectionID: connID,
		ScopeType:    "global",
		Priority:     1,
		Weight:       100,
		Status:       "active",
	})

	_, _, _ = rk.routingSvc.CreateEntry(context.Background(), rk.e.Override, &routing.RoutePlanEntry{
		PlanID:       plan.ID,
		ConnectionID: connID,
		ScopeType:    "global",
		Priority:     2,
		Weight:       100,
		Status:       "active",
	})

	err := rk.routingSvc.ValidatePlanForActivation(context.Background(), plan.ID)
	if err == nil {
		t.Fatal("expected ValidatePlanForActivation to fail on duplicate entries, but got nil")
	}
	if !strings.Contains(err.Error(), "overlapping duplicate entry") {
		t.Fatalf("expected error to mention 'overlapping duplicate entry', got: %v", err)
	}
}

func TestPermissions_ResellerOrgAdminGets403OnAllEndpoints(t *testing.T) {
	rk := newRoutingKit(t)

	resellerOrg, resellerPath := rk.e.ChildOrg(rk.e.PlatformOrgID, rk.e.PlatformOrgPath, "test-reseller-org", "reseller")
	resellerAdmin := rk.e.Actor("reseller-admin", resellerOrg, resellerPath, "reseller", false, "org.organization.view", "user.account.create")

	// 1. List Plans -> 403
	_, _, err := rk.routingSvc.ListPlans(context.Background(), resellerAdmin, "", 1, 10)
	if !errors.Is(err, routing.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for reseller ListPlans, got: %v", err)
	}

	// 2. List Assignments -> 403
	_, _, err = rk.routingSvc.ListAssignments(context.Background(), resellerAdmin, nil, 1, 10)
	if !errors.Is(err, routing.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for reseller ListAssignments, got: %v", err)
	}

	// 3. List Rules -> 403
	_, _, err = rk.routingSvc.ListRules(context.Background(), resellerAdmin, "", 1, 10)
	if !errors.Is(err, routing.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for reseller ListRules, got: %v", err)
	}

	// 4. Simulate -> 403
	_, err = rk.routingSvc.Simulate(context.Background(), resellerAdmin, routing.SimulateRequest{DestinationE164: "+12025550100"})
	if !errors.Is(err, routing.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for reseller Simulate, got: %v", err)
	}

	// 5. Snapshot Status -> 403
	_, err = rk.routingSvc.SnapshotStatus(context.Background(), resellerAdmin)
	if !errors.Is(err, routing.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for reseller SnapshotStatus, got: %v", err)
	}

	// 6. Coverage Report -> 403
	_, err = rk.routingSvc.CoverageReport(context.Background(), resellerAdmin)
	if !errors.Is(err, routing.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for reseller CoverageReport, got: %v", err)
	}
}

func TestSoftDelete_Entries_Assignments_Rules_AreArchivedWithHistory(t *testing.T) {
	rk := newRoutingKit(t)
	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")

	plan, _ := rk.routingSvc.CreatePlan(context.Background(), rk.e.Override, &routing.RoutePlan{
		Name:     "Soft Delete Plan",
		Code:     "SOFT-DEL-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
		Status:   routing.PlanDraft,
	})

	entry, _, _ := rk.routingSvc.CreateEntry(context.Background(), rk.e.Override, &routing.RoutePlanEntry{
		PlanID:       plan.ID,
		ConnectionID: connID,
		Priority:     1,
		Weight:       100,
		Status:       "active",
	})

	assign, _, _ := rk.routingSvc.CreateAssignment(context.Background(), rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID,
		PlanID:        plan.ID,
		Status:        "active",
	})

	rule, _, err := rk.routingSvc.CreateRule(context.Background(), rk.e.Override, &routing.RoutingRule{
		Name:               "Soft Delete Rule",
		Priority:           1,
		Status:             "active",
		ActionType:         "avoid",
		ActionConnectionID: &connID,
		Conditions:         map[string]interface{}{"prefix": "+998" + testkit.RandHex(2)},
	})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}

	_, err = rk.routingSvc.DeleteEntry(context.Background(), rk.e.Override, entry.ID)
	if err != nil {
		t.Fatalf("delete entry: %v", err)
	}

	_, err = rk.routingSvc.DeleteAssignment(context.Background(), rk.e.Override, assign.ID)
	if err != nil {
		t.Fatalf("delete assignment: %v", err)
	}

	_, err = rk.routingSvc.DeleteRule(context.Background(), rk.e.Override, rule.ID)
	if err != nil {
		t.Fatalf("delete rule: %v", err)
	}

	// Verify database rows STILL EXIST with status = 'archived' (never hard deleted!)
	var entryStatus string
	err = rk.e.Pool.QueryRow(context.Background(), `SELECT status FROM route_plan_entries WHERE id = $1`, entry.ID).Scan(&entryStatus)
	if err != nil || entryStatus != "archived" {
		t.Fatalf("expected entry to exist with status 'archived', got status=%s, err=%v", entryStatus, err)
	}

	var assignStatus string
	err = rk.e.Pool.QueryRow(context.Background(), `SELECT status FROM customer_route_assignments WHERE id = $1`, assign.ID).Scan(&assignStatus)
	if err != nil || assignStatus != "archived" {
		t.Fatalf("expected assignment to exist with status 'archived', got status=%s, err=%v", assignStatus, err)
	}

	var ruleStatus string
	err = rk.e.Pool.QueryRow(context.Background(), `SELECT status FROM routing_rules WHERE id = $1`, rule.ID).Scan(&ruleStatus)
	if err != nil || ruleStatus != "archived" {
		t.Fatalf("expected rule to exist with status 'archived', got status=%s, err=%v", ruleStatus, err)
	}
}

func TestCoverageReport_CalculatesFourRequiredMetrics(t *testing.T) {
	rk := newRoutingKit(t)
	report, err := rk.routingSvc.CoverageReport(context.Background(), rk.e.Override)
	if err != nil {
		t.Fatalf("coverage report: %v", err)
	}

	if _, ok := report["destinations_with_no_route"]; !ok {
		t.Fatal("missing 'destinations_with_no_route' in coverage report")
	}
	if _, ok := report["single_provider_destinations"]; !ok {
		t.Fatal("missing 'single_provider_destinations' in coverage report")
	}
	if _, ok := report["customers_without_route"]; !ok {
		t.Fatal("missing 'customers_without_route' in coverage report")
	}
	if _, ok := report["entries_missing_buy_rate"]; !ok {
		t.Fatal("missing 'entries_missing_buy_rate' in coverage report")
	}
}

// -----------------------------------------------------------------------------
// Step 0c: Rate Freshness Tests (Dynamic Rate Checking Without Snapshot Reload)
// -----------------------------------------------------------------------------

func TestRateFreshness_RateStartsBetweenReloads(t *testing.T) {
	rk := newRoutingKit(t)
	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	t0 := time.Now().UTC()
	t1 := t0.Add(10 * time.Second)

	rater := &mockRater{
		buyRates: map[uuid.UUID]*routing.RateLookupResult{
			connID: {Found: true, Price: decimal.RequireFromString("0.005000"), Currency: "USD"},
		},
		timedSellRates: map[uuid.UUID][]rateWithWindow{
			rk.customerOrgID: {
				{
					Result:        &routing.RateLookupResult{Found: true, Price: decimal.RequireFromString("0.010000"), Currency: "USD"},
					EffectiveFrom: &t1, // Becomes active at T1
				},
			},
		},
	}
	engine := routing.NewEngine(rk.e.Pool, rater, nil, nil)

	plan, err := rk.routingSvc.CreatePlan(context.Background(), rk.e.Override, &routing.RoutePlan{
		Name:     "Freshness Start Plan",
		Code:     "FRESH-START-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
		Status:   routing.PlanDraft,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	_, _, err = rk.routingSvc.CreateEntry(context.Background(), rk.e.Override, &routing.RoutePlanEntry{
		PlanID:       plan.ID,
		ConnectionID: connID,
		Priority:     1,
		Weight:       100,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	_, _, err = rk.routingSvc.RequestPlanActivation(context.Background(), rk.e.Override, plan.ID)
	if err != nil {
		t.Fatalf("activate plan: %v", err)
	}
	_, _, err = rk.routingSvc.CreateAssignment(context.Background(), rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID,
		PlanID:        plan.ID,
		Priority:      1,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	// Reload snapshot once at T0
	_, err = routing.Reload(context.Background(), rk.e.Pool)
	if err != nil {
		t.Fatalf("reload snapshot: %v", err)
	}

	// At T0: sell rate not yet effective -> Rejected with NO_SELL_RATE
	resT0, err := engine.Resolve(context.Background(), routing.ResolveRequest{
		CustomerOrgID: rk.customerOrgID,
		CountryID:     &rk.countryID,
		Timestamp:     &t0,
	})
	if err != nil {
		t.Fatalf("resolve at T0: %v", err)
	}
	if len(resT0.Candidates) != 0 {
		t.Fatalf("expected 0 candidates at T0, got %d", len(resT0.Candidates))
	}
	hasNoSellRate := false
	for _, exc := range resT0.Excluded {
		if exc.ReasonCode == routing.ReasonNoSellRate {
			hasNoSellRate = true
			break
		}
	}
	if !hasNoSellRate {
		t.Fatalf("expected ReasonNoSellRate at T0, got exclusions: %+v", resT0.Excluded)
	}

	// At T1 + 1s WITHOUT snapshot reload: dynamically evaluated and accepted!
	t1After := t1.Add(time.Second)
	resT1, err := engine.Resolve(context.Background(), routing.ResolveRequest{
		CustomerOrgID: rk.customerOrgID,
		CountryID:     &rk.countryID,
		Timestamp:     &t1After,
	})
	if err != nil {
		t.Fatalf("resolve at T1: %v", err)
	}
	if len(resT1.Candidates) != 1 {
		t.Fatalf("expected 1 candidate at T1 without reload, got %d", len(resT1.Candidates))
	}
	if resT1.Candidate == nil || resT1.Candidate.ConnectionID != connID {
		t.Fatalf("expected candidate %s, got %+v", connID, resT1.Candidate)
	}
}

func TestRateFreshness_RateExpiresBetweenReloads(t *testing.T) {
	rk := newRoutingKit(t)
	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	t0 := time.Now().UTC()
	t1 := t0.Add(10 * time.Second)

	rater := &mockRater{
		buyRates: map[uuid.UUID]*routing.RateLookupResult{
			connID: {Found: true, Price: decimal.RequireFromString("0.005000"), Currency: "USD"},
		},
		timedSellRates: map[uuid.UUID][]rateWithWindow{
			rk.customerOrgID: {
				{
					Result:      &routing.RateLookupResult{Found: true, Price: decimal.RequireFromString("0.010000"), Currency: "USD"},
					EffectiveTo: &t1, // Expires at T1
				},
			},
		},
	}
	engine := routing.NewEngine(rk.e.Pool, rater, nil, nil)

	plan, err := rk.routingSvc.CreatePlan(context.Background(), rk.e.Override, &routing.RoutePlan{
		Name:     "Freshness Expire Plan",
		Code:     "FRESH-EXP-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
		Status:   routing.PlanDraft,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	_, _, err = rk.routingSvc.CreateEntry(context.Background(), rk.e.Override, &routing.RoutePlanEntry{
		PlanID:       plan.ID,
		ConnectionID: connID,
		Priority:     1,
		Weight:       100,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	_, _, err = rk.routingSvc.RequestPlanActivation(context.Background(), rk.e.Override, plan.ID)
	if err != nil {
		t.Fatalf("activate plan: %v", err)
	}
	_, _, err = rk.routingSvc.CreateAssignment(context.Background(), rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID,
		PlanID:        plan.ID,
		Priority:      1,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	// Reload snapshot once at T0
	_, err = routing.Reload(context.Background(), rk.e.Pool)
	if err != nil {
		t.Fatalf("reload snapshot: %v", err)
	}

	// At T0: sell rate is active -> candidate accepted
	resT0, err := engine.Resolve(context.Background(), routing.ResolveRequest{
		CustomerOrgID: rk.customerOrgID,
		CountryID:     &rk.countryID,
		Timestamp:     &t0,
	})
	if err != nil {
		t.Fatalf("resolve at T0: %v", err)
	}
	if len(resT0.Candidates) != 1 {
		t.Fatalf("expected 1 candidate at T0, got %d", len(resT0.Candidates))
	}

	// At T1 + 1s WITHOUT reload: sell rate expired -> rejected with NO_SELL_RATE
	t1After := t1.Add(time.Second)
	resT1, err := engine.Resolve(context.Background(), routing.ResolveRequest{
		CustomerOrgID: rk.customerOrgID,
		CountryID:     &rk.countryID,
		Timestamp:     &t1After,
	})
	if err != nil {
		t.Fatalf("resolve at T1: %v", err)
	}
	if len(resT1.Candidates) != 0 {
		t.Fatalf("expected 0 candidates at T1, got %d", len(resT1.Candidates))
	}
	hasNoSellRate := false
	for _, exc := range resT1.Excluded {
		if exc.ReasonCode == routing.ReasonNoSellRate {
			hasNoSellRate = true
			break
		}
	}
	if !hasNoSellRate {
		t.Fatalf("expected ReasonNoSellRate at T1, got exclusions: %+v", resT1.Excluded)
	}
}

// -----------------------------------------------------------------------------
// Step 0d: Missing Routing Rules & Failover Tests
// -----------------------------------------------------------------------------

func TestRoutingRules_Block_Force_Prefer_Avoid(t *testing.T) {
	rk := newRoutingKit(t)
	provID := rk.createProvider(t, "active")
	connA := rk.createConnection(t, provID, "active")
	connB := rk.createConnection(t, provID, "active")
	connC := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	rater := &mockRater{
		buyRates: map[uuid.UUID]*routing.RateLookupResult{
			connA: {Found: true, Price: decimal.RequireFromString("0.005000"), Currency: "USD"},
			connB: {Found: true, Price: decimal.RequireFromString("0.006000"), Currency: "USD"},
			connC: {Found: true, Price: decimal.RequireFromString("0.007000"), Currency: "USD"},
		},
		sellRates: map[uuid.UUID]*routing.RateLookupResult{
			rk.customerOrgID: {Found: true, Price: decimal.RequireFromString("0.010000"), Currency: "USD"},
		},
	}
	engine := routing.NewEngine(rk.e.Pool, rater, nil, nil)

	plan, err := rk.routingSvc.CreatePlan(context.Background(), rk.e.Override, &routing.RoutePlan{
		Name:     "Rules Test Plan",
		Code:     "RULES-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
		Status:   routing.PlanDraft,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	for i, c := range []uuid.UUID{connA, connB, connC} {
		_, _, err = rk.routingSvc.CreateEntry(context.Background(), rk.e.Override, &routing.RoutePlanEntry{
			PlanID:       plan.ID,
			ConnectionID: c,
			Priority:     i + 1,
			Weight:       33,
			Status:       "active",
		})
		if err != nil {
			t.Fatalf("create entry: %v", err)
		}
	}
	_, _, err = rk.routingSvc.RequestPlanActivation(context.Background(), rk.e.Override, plan.ID)
	if err != nil {
		t.Fatalf("activate plan: %v", err)
	}
	_, _, err = rk.routingSvc.CreateAssignment(context.Background(), rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID,
		PlanID:        plan.ID,
		Priority:      1,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	// 1. Test PREFER rule: prefer connC (which is priority 3). ConnC should become rank 1!
	prefRule, _, err := rk.routingSvc.CreateRule(context.Background(), rk.e.Override, &routing.RoutingRule{
		Name:               "Prefer Connection C",
		Priority:           1,
		Status:             "active",
		ActionType:         "prefer",
		ActionConnectionID: &connC,
		Conditions:         map[string]interface{}{"customer_org_id": rk.customerOrgID.String()},
	})
	if err != nil {
		t.Fatalf("create prefer rule: %v", err)
	}
	_, _ = routing.Reload(context.Background(), rk.e.Pool)

	resPref, err := engine.Resolve(context.Background(), routing.ResolveRequest{
		CustomerOrgID: rk.customerOrgID,
		CountryID:     &rk.countryID,
	})
	if err != nil || len(resPref.Candidates) != 3 {
		t.Fatalf("resolve prefer: %v, len=%d", err, len(resPref.Candidates))
	}
	if resPref.Candidates[0].ConnectionID != connC {
		t.Fatalf("expected preferred connC at rank 1, got %s", resPref.Candidates[0].ConnectionID)
	}

	// 2. Test AVOID rule: avoid connA (which was naturally rank 1). ConnA should move to last!
	_, err = rk.routingSvc.DeleteRule(context.Background(), rk.e.Override, prefRule.ID)
	if err != nil {
		t.Fatalf("delete rule: %v", err)
	}
	avoidRule, _, err := rk.routingSvc.CreateRule(context.Background(), rk.e.Override, &routing.RoutingRule{
		Name:               "Avoid Connection A",
		Priority:           1,
		Status:             "active",
		ActionType:         "avoid",
		ActionConnectionID: &connA,
		Conditions:         map[string]interface{}{"customer_org_id": rk.customerOrgID.String()},
	})
	if err != nil {
		t.Fatalf("create avoid rule: %v", err)
	}
	_, _ = routing.Reload(context.Background(), rk.e.Pool)

	resAvoid, err := engine.Resolve(context.Background(), routing.ResolveRequest{
		CustomerOrgID: rk.customerOrgID,
		CountryID:     &rk.countryID,
	})
	if err != nil || len(resAvoid.Candidates) != 3 {
		t.Fatalf("resolve avoid: %v", err)
	}
	if resAvoid.Candidates[2].ConnectionID != connA {
		t.Fatalf("expected avoided connA at last rank, got %s", resAvoid.Candidates[2].ConnectionID)
	}

	// 3. Test FORCE rule: force connB. Only connB should be eligible!
	_, err = rk.routingSvc.DeleteRule(context.Background(), rk.e.Override, avoidRule.ID)
	if err != nil {
		t.Fatalf("delete rule: %v", err)
	}
	forceRule, _, err := rk.routingSvc.CreateRule(context.Background(), rk.e.Override, &routing.RoutingRule{
		Name:               "Force Connection B",
		Priority:           1,
		Status:             "active",
		ActionType:         "force",
		ActionConnectionID: &connB,
		Conditions:         map[string]interface{}{"customer_org_id": rk.customerOrgID.String()},
	})
	if err != nil {
		t.Fatalf("create force rule: %v", err)
	}
	_, _ = routing.Reload(context.Background(), rk.e.Pool)

	resForce, err := engine.Resolve(context.Background(), routing.ResolveRequest{
		CustomerOrgID: rk.customerOrgID,
		CountryID:     &rk.countryID,
	})
	if err != nil || len(resForce.Candidates) != 1 {
		t.Fatalf("resolve force: %v, len=%d", err, len(resForce.Candidates))
	}
	if resForce.Candidates[0].ConnectionID != connB {
		t.Fatalf("expected forced connB, got %s", resForce.Candidates[0].ConnectionID)
	}

	// 4. Test BLOCK rule: block traffic matching customer. Must return early with ReasonRuleBlock!
	_, err = rk.routingSvc.DeleteRule(context.Background(), rk.e.Override, forceRule.ID)
	if err != nil {
		t.Fatalf("delete rule: %v", err)
	}
	_, _, err = rk.routingSvc.CreateRule(context.Background(), rk.e.Override, &routing.RoutingRule{
		Name:              "Emergency Regulatory Block",
		Priority:          1,
		Status:            "active",
		ActionType:        "block",
		ActionBlockReason: "Regulatory restriction for destination",
		Conditions:        map[string]interface{}{"customer_org_id": rk.customerOrgID.String()},
	})
	if err != nil {
		t.Fatalf("create block rule: %v", err)
	}
	_, _ = routing.Reload(context.Background(), rk.e.Pool)

	resBlock, err := engine.Resolve(context.Background(), routing.ResolveRequest{
		CustomerOrgID: rk.customerOrgID,
		CountryID:     &rk.countryID,
	})
	if err != nil {
		t.Fatalf("resolve block: %v", err)
	}
	if len(resBlock.Candidates) != 0 {
		t.Fatalf("expected 0 candidates on block, got %d", len(resBlock.Candidates))
	}
	foundBlock := false
	for _, exc := range resBlock.Excluded {
		if exc.ReasonCode == routing.ReasonRuleBlock && strings.Contains(exc.Detail, "Regulatory restriction") {
			foundBlock = true
			break
		}
	}
	if !foundBlock {
		t.Fatalf("expected ReasonRuleBlock with custom reason, got: %+v", resBlock.Excluded)
	}
}

func TestRoutingRules_SaveTimeConflictDetection(t *testing.T) {
	rk := newRoutingKit(t)

	// 1. Creating force rule without action connection ID must be rejected
	_, _, err := rk.routingSvc.CreateRule(context.Background(), rk.e.Override, &routing.RoutingRule{
		Name:       "Invalid Force Rule Without Connection ID",
		Priority:   1,
		ActionType: "force",
	})
	if err == nil {
		t.Fatal("expected error creating force rule without ActionConnectionID, got nil")
	}

	// 2. Create an active rule
	provID := rk.createProvider(t, "active")
	conn1 := rk.createConnection(t, provID, "active")
	conn2 := rk.createConnection(t, provID, "active")
	uniquePrefix := "+1202" + testkit.RandHex(2)
	_, _, err = rk.routingSvc.CreateRule(context.Background(), rk.e.Override, &routing.RoutingRule{
		Name:               "Conflict Rule 1",
		Priority:           5,
		ActionType:         "force",
		ActionConnectionID: &conn1,
		Conditions:         map[string]interface{}{"prefix": uniquePrefix},
	})
	if err != nil {
		t.Fatalf("create rule 1: %v", err)
	}

	// 3. Create another active rule with identical conditions and priority but conflicting action (force conn2)
	_, _, err = rk.routingSvc.CreateRule(context.Background(), rk.e.Override, &routing.RoutingRule{
		Name:               "Conflicting Rule 2",
		Priority:           5,
		ActionType:         "force",
		ActionConnectionID: &conn2,
		Conditions:         map[string]interface{}{"prefix": uniquePrefix},
	})
	if err == nil {
		t.Fatal("expected conflict error when saving conflicting rule, got nil")
	}
	if !strings.Contains(err.Error(), "conflicting rule detected") {
		t.Fatalf("expected 'conflicting rule detected', got: %v", err)
	}
}

func TestFailover_Next_And_CandidateRankOrder(t *testing.T) {
	rk := newRoutingKit(t)
	provID := rk.createProvider(t, "active")
	conn1 := rk.createConnection(t, provID, "active")
	conn2 := rk.createConnection(t, provID, "active")
	conn3 := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	rater := &mockRater{
		buyRates: map[uuid.UUID]*routing.RateLookupResult{
			conn1: {Found: true, Price: decimal.RequireFromString("0.005000"), Currency: "USD"},
			conn2: {Found: true, Price: decimal.RequireFromString("0.006000"), Currency: "USD"},
			conn3: {Found: true, Price: decimal.RequireFromString("0.007000"), Currency: "USD"},
		},
		sellRates: map[uuid.UUID]*routing.RateLookupResult{
			rk.customerOrgID: {Found: true, Price: decimal.RequireFromString("0.010000"), Currency: "USD"},
		},
	}
	engine := routing.NewEngine(rk.e.Pool, rater, nil, nil)

	plan, err := rk.routingSvc.CreatePlan(context.Background(), rk.e.Override, &routing.RoutePlan{
		Name:     "Failover Priority Plan",
		Code:     "FAILOVER-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
		Status:   routing.PlanDraft,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	for i, c := range []uuid.UUID{conn1, conn2, conn3} {
		_, _, err = rk.routingSvc.CreateEntry(context.Background(), rk.e.Override, &routing.RoutePlanEntry{
			PlanID:       plan.ID,
			ConnectionID: c,
			Priority:     i + 1,
			Weight:       33,
			Status:       "active",
		})
		if err != nil {
			t.Fatalf("create entry: %v", err)
		}
	}
	_, _, err = rk.routingSvc.RequestPlanActivation(context.Background(), rk.e.Override, plan.ID)
	if err != nil {
		t.Fatalf("activate plan: %v", err)
	}
	_, _, err = rk.routingSvc.CreateAssignment(context.Background(), rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID,
		PlanID:        plan.ID,
		Priority:      1,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	_, _ = routing.Reload(context.Background(), rk.e.Pool)

	// Initial Resolve: returns ranked candidates 1, 2, 3
	res1, err := engine.Resolve(context.Background(), routing.ResolveRequest{
		CustomerOrgID: rk.customerOrgID,
		CountryID:     &rk.countryID,
	})
	if err != nil || len(res1.Candidates) != 3 {
		t.Fatalf("initial resolve: %v, len=%d", err, len(res1.Candidates))
	}
	if res1.Candidates[0].Rank != 1 || res1.Candidates[0].ConnectionID != conn1 {
		t.Fatalf("expected conn1 at rank 1, got %+v", res1.Candidates[0])
	}
	if res1.Candidates[1].Rank != 2 || res1.Candidates[1].ConnectionID != conn2 {
		t.Fatalf("expected conn2 at rank 2, got %+v", res1.Candidates[1])
	}
	if res1.Candidates[2].Rank != 3 || res1.Candidates[2].ConnectionID != conn3 {
		t.Fatalf("expected conn3 at rank 3, got %+v", res1.Candidates[2])
	}

	// Next() / Failover: provider conn1 failed delivery. Call Next excluding conn1.
	res2, err := engine.Next(context.Background(), routing.ResolveRequest{
		CustomerOrgID: rk.customerOrgID,
		CountryID:     &rk.countryID,
		ExcludeIDs:    []uuid.UUID{conn1},
	})
	if err != nil || res2 == nil {
		t.Fatalf("next resolve: %v", err)
	}
	if res2.ConnectionID != conn2 {
		t.Fatalf("expected failover to conn2, got %s", res2.ConnectionID)
	}

	// Verify conn1 was marked excluded with ReasonExcludedByCaller
	resFull, _ := engine.Resolve(context.Background(), routing.ResolveRequest{
		CustomerOrgID: rk.customerOrgID,
		CountryID:     &rk.countryID,
		ExcludeIDs:    []uuid.UUID{conn1},
	})
	foundCallerExcluded := false
	for _, exc := range resFull.Excluded {
		if exc.ConnectionID == conn1 && exc.ReasonCode == routing.ReasonExcludedByCaller {
			foundCallerExcluded = true
			break
		}
	}
	if !foundCallerExcluded {
		t.Fatalf("expected ReasonExcludedByCaller for conn1, got: %+v", resFull.Excluded)
	}
}

func TestCandidate_DestinationNumberFormat_Applied(t *testing.T) {
	rk := newRoutingKit(t)
	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	rater := &mockRater{
		buyRates: map[uuid.UUID]*routing.RateLookupResult{
			connID: {Found: true, Price: decimal.RequireFromString("0.005000"), Currency: "USD"},
		},
		sellRates: map[uuid.UUID]*routing.RateLookupResult{
			rk.customerOrgID: {Found: true, Price: decimal.RequireFromString("0.010000"), Currency: "USD"},
		},
	}
	engine := routing.NewEngine(rk.e.Pool, rater, nil, nil)

	plan, err := rk.routingSvc.CreatePlan(context.Background(), rk.e.Override, &routing.RoutePlan{
		Name:     "Format Test Plan",
		Code:     "FMT-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
		Status:   routing.PlanDraft,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	_, _, err = rk.routingSvc.CreateEntry(context.Background(), rk.e.Override, &routing.RoutePlanEntry{
		PlanID:                  plan.ID,
		ConnectionID:            connID,
		Priority:                1,
		Weight:                  100,
		DestinationNumberFormat: "international_no_plus",
		Status:                  "active",
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	_, _, err = rk.routingSvc.RequestPlanActivation(context.Background(), rk.e.Override, plan.ID)
	if err != nil {
		t.Fatalf("activate plan: %v", err)
	}
	_, _, err = rk.routingSvc.CreateAssignment(context.Background(), rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID,
		PlanID:        plan.ID,
		Priority:      1,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	_, _ = routing.Reload(context.Background(), rk.e.Pool)

	res, err := engine.Resolve(context.Background(), routing.ResolveRequest{
		CustomerOrgID:   rk.customerOrgID,
		CountryID:       &rk.countryID,
		DestinationE164: "+12025550199",
	})
	if err != nil || len(res.Candidates) != 1 {
		t.Fatalf("resolve: %v, len=%d", err, len(res.Candidates))
	}
	cand := res.Candidates[0]
	if cand.DestinationNumberFormat != "international_no_plus" {
		t.Fatalf("expected format 'international_no_plus', got '%s'", cand.DestinationNumberFormat)
	}
	if cand.FormattedDestination != "12025550199" {
		t.Fatalf("expected formatted destination '12025550199', got '%s'", cand.FormattedDestination)
	}
}

func TestMargin_EntryOverrideVsGlobalMinMargin(t *testing.T) {
	rk := newRoutingKit(t)
	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	// Sell = 0.010000, Buy = 0.009500 -> Margin = 0.000500
	rater := &mockRater{
		buyRates: map[uuid.UUID]*routing.RateLookupResult{
			connID: {Found: true, Price: decimal.RequireFromString("0.009500"), Currency: "USD"},
		},
		sellRates: map[uuid.UUID]*routing.RateLookupResult{
			rk.customerOrgID: {Found: true, Price: decimal.RequireFromString("0.010000"), Currency: "USD"},
		},
	}
	engine := routing.NewEngine(rk.e.Pool, rater, nil, nil)
	// Global minimum margin is 0.001000 (which is greater than 0.000500)
	engine.SetGlobalMinMargin(decimal.RequireFromString("0.001000"))

	plan, err := rk.routingSvc.CreatePlan(context.Background(), rk.e.Override, &routing.RoutePlan{
		Name:     "Margin Override Plan",
		Code:     "MARGIN-OV-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
		Status:   routing.PlanDraft,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	entry, _, err := rk.routingSvc.CreateEntry(context.Background(), rk.e.Override, &routing.RoutePlanEntry{
		PlanID:       plan.ID,
		ConnectionID: connID,
		Priority:     1,
		Weight:       100,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	_, _, err = rk.routingSvc.RequestPlanActivation(context.Background(), rk.e.Override, plan.ID)
	if err != nil {
		t.Fatalf("activate plan: %v", err)
	}
	_, _, err = rk.routingSvc.CreateAssignment(context.Background(), rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID,
		PlanID:        plan.ID,
		Priority:      1,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	_, _ = routing.Reload(context.Background(), rk.e.Pool)

	// 1. Without entry override: Global min margin (0.001000) causes MARGIN_BELOW_MINIMUM exclusion
	res1, err := engine.Resolve(context.Background(), routing.ResolveRequest{
		CustomerOrgID: rk.customerOrgID,
		CountryID:     &rk.countryID,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(res1.Candidates) != 0 {
		t.Fatalf("expected 0 candidates with margin 0.000500 < 0.001000, got %d", len(res1.Candidates))
	}
	foundBelowMargin := false
	for _, exc := range res1.Excluded {
		if exc.ReasonCode == routing.ReasonMarginBelowMinimum {
			foundBelowMargin = true
			break
		}
	}
	if !foundBelowMargin {
		t.Fatalf("expected ReasonMarginBelowMinimum, got: %+v", res1.Excluded)
	}

	// 2. With entry override: set min_margin_override to 0.000200. Entry must now pass!
	overrideMargin := decimal.RequireFromString("0.000200")
	entry.MinMarginOverride = &overrideMargin
	_, _, err = rk.routingSvc.UpdateEntry(context.Background(), rk.e.Override, entry)
	if err != nil {
		t.Fatalf("update entry: %v", err)
	}
	_, _ = routing.Reload(context.Background(), rk.e.Pool)

	res2, err := engine.Resolve(context.Background(), routing.ResolveRequest{
		CustomerOrgID: rk.customerOrgID,
		CountryID:     &rk.countryID,
	})
	if err != nil || len(res2.Candidates) != 1 {
		t.Fatalf("expected 1 candidate with entry override 0.000200, got %d (err: %v)", len(res2.Candidates), err)
	}
}

func TestUntestedExclusions_Comprehensive(t *testing.T) {
	rk := newRoutingKit(t)
	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	rater := &mockRater{
		buyRates: map[uuid.UUID]*routing.RateLookupResult{
			connID: {Found: true, Price: decimal.RequireFromString("0.005000"), Currency: "USD"},
		},
		sellRates: map[uuid.UUID]*routing.RateLookupResult{
			rk.customerOrgID: {Found: true, Price: decimal.RequireFromString("0.010000"), Currency: "USD"},
		},
	}
	engine := routing.NewEngine(rk.e.Pool, rater, nil, nil)

	var draftPlan *routing.RoutePlan
	var entry *routing.RoutePlanEntry

	t.Run("1_ReasonPlanInactive", func(t *testing.T) {
		var err error
		draftPlan, err = rk.routingSvc.CreatePlan(context.Background(), rk.e.Override, &routing.RoutePlan{
			Name:     "Draft Plan",
			Code:     "DRAFT-EXC-" + strings.ToUpper(testkit.RandHex(4)),
			Strategy: routing.StrategyPriority,
			Status:   routing.PlanDraft,
		})
		if err != nil {
			t.Fatalf("create draft plan: %v", err)
		}
		_, _, err = rk.routingSvc.CreateAssignment(context.Background(), rk.e.Override, &routing.CustomerRouteAssignment{
			CustomerOrgID: rk.customerOrgID,
			PlanID:        draftPlan.ID,
			Priority:      1,
			Status:        "active",
		})
		if err != nil {
			t.Fatalf("create assignment: %v", err)
		}
		_, _ = routing.Reload(context.Background(), rk.e.Pool)

		resPlanInactive, _ := engine.Resolve(context.Background(), routing.ResolveRequest{
			CustomerOrgID: rk.customerOrgID,
			CountryID:     &rk.countryID,
		})
		if len(resPlanInactive.Candidates) != 0 || resPlanInactive.Excluded[0].ReasonCode != routing.ReasonPlanInactive {
			t.Fatalf("expected ReasonPlanInactive, got: %+v", resPlanInactive.Excluded)
		}
	})

	t.Run("2_ReasonEntryInactive", func(t *testing.T) {
		var err error
		// Create entry while plan is draft, then activate plan using UpdatePlan/DB so disabled entry can be tested on active plan
		entry, _, err = rk.routingSvc.CreateEntry(context.Background(), rk.e.Override, &routing.RoutePlanEntry{
			PlanID:       draftPlan.ID,
			ConnectionID: connID,
			Priority:     1,
			Weight:       100,
			Status:       "active",
		})
		if err != nil {
			t.Fatalf("create entry: %v", err)
		}
		_, _, err = rk.routingSvc.RequestPlanActivation(context.Background(), rk.e.Override, draftPlan.ID)
		if err != nil {
			t.Fatalf("activate plan: %v", err)
		}
		entry.Status = "disabled"
		entry, _, err = rk.routingSvc.UpdateEntry(context.Background(), rk.e.Override, entry)
		if err != nil {
			t.Fatalf("disable entry: %v", err)
		}
		_, _ = routing.Reload(context.Background(), rk.e.Pool)

		resEntryInactive, _ := engine.Resolve(context.Background(), routing.ResolveRequest{
			CustomerOrgID: rk.customerOrgID,
			CountryID:     &rk.countryID,
		})
		if len(resEntryInactive.Candidates) != 0 || resEntryInactive.Excluded[0].ReasonCode != routing.ReasonEntryInactive {
			t.Fatalf("expected ReasonEntryInactive, got: %+v", resEntryInactive.Excluded)
		}

		entry.Status = "active"
		entry, _, err = rk.routingSvc.UpdateEntry(context.Background(), rk.e.Override, entry)
		if err != nil {
			t.Fatalf("update entry: %v", err)
		}
	})

	t.Run("3_ReasonCoverageTestedDisallowed", func(t *testing.T) {
		rk.setCoverage(t, provID, rk.countryID, "tested")
		_, _ = routing.Reload(context.Background(), rk.e.Pool)

		resTested, _ := engine.Resolve(context.Background(), routing.ResolveRequest{
			CustomerOrgID: rk.customerOrgID,
			CountryID:     &rk.countryID,
		})
		if len(resTested.Candidates) != 0 || resTested.Excluded[0].ReasonCode != routing.ReasonCoverageTestedDisallowed {
			t.Fatalf("expected ReasonCoverageTestedDisallowed, got: %+v", resTested.Excluded)
		}
		rk.setCoverage(t, provID, rk.countryID, "active")
	})

	t.Run("4_ReasonSenderIDTypeNotAllowed", func(t *testing.T) {
		entry.AllowedSenderIDTypes = []string{"alphanumeric"}
		var err error
		entry, _, err = rk.routingSvc.UpdateEntry(context.Background(), rk.e.Override, entry)
		if err != nil {
			t.Fatalf("update entry: %v", err)
		}
		_, _ = routing.Reload(context.Background(), rk.e.Pool)

		resSenderType, _ := engine.Resolve(context.Background(), routing.ResolveRequest{
			CustomerOrgID: rk.customerOrgID,
			CountryID:     &rk.countryID,
			SenderIDType:  "numeric",
		})
		if len(resSenderType.Candidates) != 0 || resSenderType.Excluded[0].ReasonCode != routing.ReasonSenderIDTypeNotAllowed {
			t.Fatalf("expected ReasonSenderIDTypeNotAllowed, got: %+v", resSenderType.Excluded)
		}
		entry.AllowedSenderIDTypes = []string{"numeric", "alphanumeric"}
		entry, _, _ = rk.routingSvc.UpdateEntry(context.Background(), rk.e.Override, entry)
		_, _ = routing.Reload(context.Background(), rk.e.Pool)
	})

	t.Run("5_ReasonSenderIDNotApproved", func(t *testing.T) {
		snap := routing.CurrentSnapshot()
		snap.ApprovedSenderIDs["dummy"] = routing.SnapshotSenderID{ID: uuid.New()} // make non-empty
		resSenderNotApp, _ := engine.Resolve(context.Background(), routing.ResolveRequest{
			CustomerOrgID: rk.customerOrgID,
			CountryID:     &rk.countryID,
			SenderID:      "UNAPPROVED_BRAND",
		})
		if len(resSenderNotApp.Candidates) != 0 || resSenderNotApp.Excluded[0].ReasonCode != routing.ReasonSenderIDNotApproved {
			t.Fatalf("expected ReasonSenderIDNotApproved, got: %+v", resSenderNotApp.Excluded)
		}
		delete(snap.ApprovedSenderIDs, "dummy")
	})

	t.Run("6_ReasonCustomerSuspended", func(t *testing.T) {
		snap := routing.CurrentSnapshot()
		snap.CustomerProfiles[rk.customerOrgID] = routing.SnapshotCustomerProfile{
			OrgID:     rk.customerOrgID,
			OrgStatus: "suspended",
		}
		resSuspended, _ := engine.Resolve(context.Background(), routing.ResolveRequest{
			CustomerOrgID: rk.customerOrgID,
			CountryID:     &rk.countryID,
		})
		if len(resSuspended.Candidates) != 0 || resSuspended.Excluded[0].ReasonCode != routing.ReasonCustomerSuspended {
			t.Fatalf("expected ReasonCustomerSuspended, got: %+v", resSuspended.Excluded)
		}
	})

	t.Run("7_ReasonDestinationNotAllowed", func(t *testing.T) {
		snap := routing.CurrentSnapshot()
		allowedOtherCountry := uuid.New()
		snap.CustomerProfiles[rk.customerOrgID] = routing.SnapshotCustomerProfile{
			OrgID:            rk.customerOrgID,
			OrgStatus:        "active",
			AllowedCountries: []uuid.UUID{allowedOtherCountry},
		}
		resDestNotAllowed, _ := engine.Resolve(context.Background(), routing.ResolveRequest{
			CustomerOrgID: rk.customerOrgID,
			CountryID:     &rk.countryID,
		})
		if len(resDestNotAllowed.Candidates) != 0 || resDestNotAllowed.Excluded[0].ReasonCode != routing.ReasonDestinationNotAllowed {
			t.Fatalf("expected ReasonDestinationNotAllowed, got: %+v", resDestNotAllowed.Excluded)
		}
	})

	t.Run("8_ReasonInvalidNumber", func(t *testing.T) {
		numEngine := routing.NewEngine(rk.e.Pool, rater, &mockNumberer{}, nil)
		resInvalidNum, _ := numEngine.Resolve(context.Background(), routing.ResolveRequest{
			CustomerOrgID:   rk.customerOrgID,
			DestinationE164: "invalid_not_e164",
		})
		if len(resInvalidNum.Candidates) != 0 || resInvalidNum.Excluded[0].ReasonCode != routing.ReasonInvalidNumber {
			t.Fatalf("expected ReasonInvalidNumber, got: %+v", resInvalidNum.Excluded)
		}
	})
}

// -----------------------------------------------------------------------------
// Step 0f: Governance, RBAC, Rollback & Export Tests
// -----------------------------------------------------------------------------

func TestMakerChecker_CannotSelfApprove_EntryChange(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()
	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")

	plan, err := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:     "Entry Governance Plan",
		Code:     "GOV-ENTRY-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
		Status:   routing.PlanDraft,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	entry, _, err := rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID:       plan.ID,
		ConnectionID: connID,
		Priority:     1,
		Weight:       100,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	_, _, err = rk.routingSvc.RequestPlanActivation(ctx, rk.e.Override, plan.ID)
	if err != nil {
		t.Fatalf("activate plan: %v", err)
	}

	// Maker attempts to update entry on ACTIVE plan -> creates pending change request
	entry.Priority = 2
	_, cr, err := rk.routingSvc.UpdateEntry(ctx, rk.e.Maker, entry)
	if err != nil {
		t.Fatalf("maker update entry: %v", err)
	}
	if cr == nil {
		t.Fatal("expected pending change request for active plan entry update by maker, got nil")
	}

	// Maker cannot self-approve
	makerWithApprove := testkit.WithPermissions(rk.e.Maker, "routing.entry.approve")
	err = rk.e.Approval.Approve(ctx, makerWithApprove, cr.ID, "self approve attempt")
	if !errors.Is(err, approval.ErrMakerCannotApproveOwnChange) {
		t.Fatalf("expected ErrMakerCannotApproveOwnChange, got: %v", err)
	}

	// Checker approves
	err = rk.e.Approval.Approve(ctx, rk.e.Checker, cr.ID, "checker valid approval")
	if err != nil {
		t.Fatalf("checker approve: %v", err)
	}
}

func TestMakerChecker_CannotSelfApprove_KillSwitchReenable(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()
	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")

	// Instant disable with reason
	_, err := rk.routingSvc.DisableConnection(ctx, rk.e.Override, connID, "Emergency hardware maintenance")
	if err != nil {
		t.Fatalf("disable connection: %v", err)
	}

	// Maker requests re-enable -> creates pending change request
	cr, err := rk.routingSvc.EnableConnection(ctx, rk.e.Maker, connID, "Maintenance completed")
	if err != nil {
		t.Fatalf("maker enable connection: %v", err)
	}
	if cr == nil {
		t.Fatal("expected pending change request for re-enable by maker, got nil")
	}

	// Maker cannot self-approve
	makerWithApprove := testkit.WithPermissions(rk.e.Maker, "routing.plan.approve")
	err = rk.e.Approval.Approve(ctx, makerWithApprove, cr.ID, "self reenable attempt")
	if !errors.Is(err, approval.ErrMakerCannotApproveOwnChange) {
		t.Fatalf("expected ErrMakerCannotApproveOwnChange for killswitch reenable, got: %v", err)
	}

	// Checker approves
	err = rk.e.Approval.Approve(ctx, rk.e.Checker, cr.ID, "maintenance complete approved")
	if err != nil {
		t.Fatalf("checker approve reenable: %v", err)
	}
}

func TestMakerChecker_RejectionRequiresReason(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()
	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")

	_, _ = rk.routingSvc.DisableConnection(ctx, rk.e.Override, connID, "Killswitch test")
	cr, err := rk.routingSvc.EnableConnection(ctx, rk.e.Maker, connID, "Ready to re-enable")
	if err != nil {
		t.Fatalf("enable connection: %v", err)
	}

	// Rejection with empty reason must be rejected
	err = rk.e.Approval.Reject(ctx, rk.e.Checker, cr.ID, "   ")
	if err == nil {
		t.Fatal("expected error when rejecting with empty reason, got nil")
	}

	// Rejection with valid reason succeeds
	err = rk.e.Approval.Reject(ctx, rk.e.Checker, cr.ID, "Provider still reporting hardware degradation")
	if err != nil {
		t.Fatalf("reject with valid reason: %v", err)
	}
}

func TestVersioning_RollbackAssignment(t *testing.T) {
	rk := newRoutingKit(t)
	plan, err := rk.routingSvc.CreatePlan(context.Background(), rk.e.Override, &routing.RoutePlan{
		Name:     "Rollback Assignment Plan",
		Code:     "RB-ASN-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
		Status:   routing.PlanDraft,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}

	assign, _, err := rk.routingSvc.CreateAssignment(context.Background(), rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID,
		PlanID:        plan.ID,
		Priority:      1,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	// Update assignment priority to 5
	assign.Priority = 5
	updated, _, err := rk.routingSvc.UpdateAssignment(context.Background(), rk.e.Override, assign)
	if err != nil {
		t.Fatalf("update assignment: %v", err)
	}
	if updated.Priority != 5 {
		t.Fatalf("expected priority 5, got %d", updated.Priority)
	}

	// Get history
	history, err := rk.routingSvc.GetAssignmentHistory(context.Background(), rk.e.Override, assign.ID)
	if err != nil || len(history) < 2 {
		t.Fatalf("get history: %v, len=%d", err, len(history))
	}

	// Rollback to original creation snapshot (where priority was 1)
	origSnapshotID := history[len(history)-1].ID
	rolledBack, err := rk.routingSvc.RollbackAssignment(context.Background(), rk.e.Override, assign.ID, origSnapshotID)
	if err != nil {
		t.Fatalf("rollback assignment: %v", err)
	}
	if rolledBack.Priority != 1 {
		t.Fatalf("expected restored priority 1 after rollback, got %d", rolledBack.Priority)
	}
	if rolledBack.Version <= updated.Version {
		t.Fatalf("expected version incremented after rollback, got %d <= %d", rolledBack.Version, updated.Version)
	}
}

func TestVersioning_RollbackRule(t *testing.T) {
	rk := newRoutingKit(t)
	rule, _, err := rk.routingSvc.CreateRule(context.Background(), rk.e.Override, &routing.RoutingRule{
		Name:              "Rollback Rule Test",
		Priority:          1,
		ActionType:        "block",
		ActionBlockReason: "Initial block",
		Status:            "active",
		Conditions:        map[string]interface{}{"prefix": "+44"},
	})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}

	// Update rule priority to 10
	rule.Priority = 10
	updated, _, err := rk.routingSvc.UpdateRule(context.Background(), rk.e.Override, rule)
	if err != nil {
		t.Fatalf("update rule: %v", err)
	}
	if updated.Priority != 10 {
		t.Fatalf("expected priority 10, got %d", updated.Priority)
	}

	// Get history
	history, err := rk.routingSvc.GetRuleHistory(context.Background(), rk.e.Override, rule.ID)
	if err != nil || len(history) < 2 {
		t.Fatalf("get rule history: %v, len=%d", err, len(history))
	}

	// Rollback to original creation snapshot (where priority was 1)
	origSnapshotID := history[len(history)-1].ID
	rolledBack, err := rk.routingSvc.RollbackRule(context.Background(), rk.e.Override, rule.ID, origSnapshotID)
	if err != nil {
		t.Fatalf("rollback rule: %v", err)
	}
	if rolledBack.Priority != 1 {
		t.Fatalf("expected restored priority 1, got %d", rolledBack.Priority)
	}
	if rolledBack.Version <= updated.Version {
		t.Fatalf("expected version incremented after rollback, got %d <= %d", rolledBack.Version, updated.Version)
	}
}

func TestExportEntries_CSVAndJSON(t *testing.T) {
	rk := newRoutingKit(t)
	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")

	plan, err := rk.routingSvc.CreatePlan(context.Background(), rk.e.Override, &routing.RoutePlan{
		Name:     "Export Test Plan",
		Code:     "EXP-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
		Status:   routing.PlanDraft,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	_, _, err = rk.routingSvc.CreateEntry(context.Background(), rk.e.Override, &routing.RoutePlanEntry{
		PlanID:       plan.ID,
		ConnectionID: connID,
		Priority:     1,
		Weight:       100,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}

	// 1. Test CSV export
	csvBytes, err := rk.routingSvc.ExportEntriesCSV(context.Background(), rk.e.Override, plan.ID)
	if err != nil {
		t.Fatalf("export entries csv: %v", err)
	}
	csvStr := string(csvBytes)
	if !strings.Contains(csvStr, "Connection ID") || !strings.Contains(csvStr, connID.String()) {
		t.Fatalf("expected CSV to contain header and connID, got: %s", csvStr)
	}

	// 2. Test JSON listing
	entries, err := rk.routingSvc.ListEntries(context.Background(), rk.e.Override, plan.ID)
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	if len(entries) != 1 || entries[0].ConnectionID != connID {
		t.Fatalf("expected 1 entry with connID %s, got: %+v", connID, entries)
	}
}

// -----------------------------------------------------------------------------
// Step 0 Follow-Up: Points 6, 7, 8, and 9 Verification Tests
// -----------------------------------------------------------------------------

func TestInvalidNumber_UnconditionalWithoutNumberer(t *testing.T) {
	rk := newRoutingKit(t)
	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	rater := &mockRater{
		buyRates: map[uuid.UUID]*routing.RateLookupResult{
			connID: {Found: true, Price: decimal.RequireFromString("0.005000"), Currency: "USD"},
		},
		sellRates: map[uuid.UUID]*routing.RateLookupResult{
			rk.customerOrgID: {Found: true, Price: decimal.RequireFromString("0.010000"), Currency: "USD"},
		},
	}
	// Number intelligence explicitly nil!
	engine := routing.NewEngine(rk.e.Pool, rater, nil, nil)

	invalidNumbers := []string{
		"invalid_not_e164",
		"12025550199",  // missing '+'
		"+0123456789",  // starts with '+0'
		"+12",          // too short
		"+1202555019A", // contains non-digit
	}

	for _, badNum := range invalidNumbers {
		res, err := engine.Resolve(context.Background(), routing.ResolveRequest{
			CustomerOrgID:   rk.customerOrgID,
			CountryID:       &rk.countryID, // even when CountryID is provided and numberer == nil!
			DestinationE164: badNum,
		})
		if err != nil {
			t.Fatalf("resolve(%q): %v", badNum, err)
		}
		if len(res.Candidates) != 0 {
			t.Fatalf("expected 0 candidates for non-E.164 number %q with numberer=nil, got %d", badNum, len(res.Candidates))
		}
		if len(res.Excluded) == 0 || res.Excluded[0].ReasonCode != routing.ReasonInvalidNumber {
			t.Fatalf("expected ReasonInvalidNumber for %q with numberer=nil, got %+v", badNum, res.Excluded)
		}
	}
}

func TestEffectiveDates_PlanEntryAssignmentExclusions(t *testing.T) {
	custOrgID := uuid.New()
	countryID := uuid.New()
	provID := uuid.New()
	connID := uuid.New()
	planID := uuid.New()

	now := time.Now().UTC()
	future := now.Add(1 * time.Hour)
	past := now.Add(-1 * time.Hour)

	baseSnap := func() *routing.RoutingSnapshot {
		s := &routing.RoutingSnapshot{
			BuildAt:           now,
			Overrides:         make(map[uuid.UUID]routing.ConnectionOverride),
			Connections:       make(map[uuid.UUID]routing.SnapshotConnection),
			Coverage:          make(map[string]string),
			CustomerProfiles:  make(map[uuid.UUID]routing.SnapshotCustomerProfile),
			ApprovedSenderIDs: make(map[string]routing.SnapshotSenderID),
		}
		s.Connections[connID] = routing.SnapshotConnection{
			ID:               connID,
			ProviderID:       provID,
			ConnectionStatus: "active",
			ProviderStatus:   "active",
			Cost:             decimal.RequireFromString("0.005000"),
		}
		s.Coverage[fmt.Sprintf("%s:%s", provID, countryID)] = "active"
		return s
	}

	engine := routing.NewEngine(nil, nil, nil, nil)

	t.Run("Assignment_NotStarted", func(t *testing.T) {
		snap := baseSnap()
		snap.Assignments = []routing.CustomerRouteAssignment{
			{
				CustomerOrgID: custOrgID,
				PlanID:        planID,
				Priority:      1,
				Status:        "active",
				EffectiveFrom: &future,
			},
		}
		snap.Plans = []routing.RoutePlan{
			{
				ID:       planID,
				Code:     "EFF-PLAN",
				Strategy: routing.StrategyPriority,
				Status:   routing.PlanActive,
				Entries:  []routing.RoutePlanEntry{{ConnectionID: connID, Priority: 1, Weight: 100, Status: "active"}},
			},
		}
		routing.SwapSnapshot(snap)

		res, err := engine.Resolve(context.Background(), routing.ResolveRequest{
			CustomerOrgID: custOrgID,
			CountryID:     &countryID,
			Timestamp:     &now,
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if len(res.Candidates) != 0 || len(res.Excluded) == 0 || res.Excluded[0].ReasonCode != routing.ReasonEffectiveDateNotStarted {
			t.Fatalf("expected ReasonEffectiveDateNotStarted for assignment, got %+v", res.Excluded)
		}
	})

	t.Run("Assignment_Expired", func(t *testing.T) {
		snap := baseSnap()
		snap.Assignments = []routing.CustomerRouteAssignment{
			{
				CustomerOrgID: custOrgID,
				PlanID:        planID,
				Priority:      1,
				Status:        "active",
				EffectiveTo:   &past,
			},
		}
		snap.Plans = []routing.RoutePlan{
			{
				ID:       planID,
				Code:     "EFF-PLAN",
				Strategy: routing.StrategyPriority,
				Status:   routing.PlanActive,
				Entries:  []routing.RoutePlanEntry{{ConnectionID: connID, Priority: 1, Weight: 100, Status: "active"}},
			},
		}
		routing.SwapSnapshot(snap)

		res, err := engine.Resolve(context.Background(), routing.ResolveRequest{
			CustomerOrgID: custOrgID,
			CountryID:     &countryID,
			Timestamp:     &now,
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if len(res.Candidates) != 0 || len(res.Excluded) == 0 || res.Excluded[0].ReasonCode != routing.ReasonEffectiveDateExpired {
			t.Fatalf("expected ReasonEffectiveDateExpired for assignment, got %+v", res.Excluded)
		}
	})

	t.Run("Plan_NotStarted", func(t *testing.T) {
		snap := baseSnap()
		snap.Assignments = []routing.CustomerRouteAssignment{
			{
				CustomerOrgID: custOrgID,
				PlanID:        planID,
				Priority:      1,
				Status:        "active",
			},
		}
		snap.Plans = []routing.RoutePlan{
			{
				ID:            planID,
				Code:          "EFF-PLAN",
				Strategy:      routing.StrategyPriority,
				Status:        routing.PlanActive,
				EffectiveFrom: &future,
				Entries:       []routing.RoutePlanEntry{{ConnectionID: connID, Priority: 1, Weight: 100, Status: "active"}},
			},
		}
		routing.SwapSnapshot(snap)

		res, err := engine.Resolve(context.Background(), routing.ResolveRequest{
			CustomerOrgID: custOrgID,
			CountryID:     &countryID,
			Timestamp:     &now,
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if len(res.Candidates) != 0 || len(res.Excluded) == 0 || res.Excluded[0].ReasonCode != routing.ReasonEffectiveDateNotStarted {
			t.Fatalf("expected ReasonEffectiveDateNotStarted for plan, got %+v", res.Excluded)
		}
	})

	t.Run("Plan_Expired", func(t *testing.T) {
		snap := baseSnap()
		snap.Assignments = []routing.CustomerRouteAssignment{
			{
				CustomerOrgID: custOrgID,
				PlanID:        planID,
				Priority:      1,
				Status:        "active",
			},
		}
		snap.Plans = []routing.RoutePlan{
			{
				ID:          planID,
				Code:        "EFF-PLAN",
				Strategy:    routing.StrategyPriority,
				Status:      routing.PlanActive,
				EffectiveTo: &past,
				Entries:     []routing.RoutePlanEntry{{ConnectionID: connID, Priority: 1, Weight: 100, Status: "active"}},
			},
		}
		routing.SwapSnapshot(snap)

		res, err := engine.Resolve(context.Background(), routing.ResolveRequest{
			CustomerOrgID: custOrgID,
			CountryID:     &countryID,
			Timestamp:     &now,
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if len(res.Candidates) != 0 || len(res.Excluded) == 0 || res.Excluded[0].ReasonCode != routing.ReasonEffectiveDateExpired {
			t.Fatalf("expected ReasonEffectiveDateExpired for plan, got %+v", res.Excluded)
		}
	})

	t.Run("Entry_NotStarted", func(t *testing.T) {
		snap := baseSnap()
		snap.Assignments = []routing.CustomerRouteAssignment{
			{
				CustomerOrgID: custOrgID,
				PlanID:        planID,
				Priority:      1,
				Status:        "active",
			},
		}
		snap.Plans = []routing.RoutePlan{
			{
				ID:       planID,
				Code:     "EFF-PLAN",
				Strategy: routing.StrategyPriority,
				Status:   routing.PlanActive,
				Entries: []routing.RoutePlanEntry{
					{
						ConnectionID:  connID,
						Priority:      1,
						Weight:        100,
						Status:        "active",
						EffectiveFrom: &future,
					},
				},
			},
		}
		routing.SwapSnapshot(snap)

		res, err := engine.Resolve(context.Background(), routing.ResolveRequest{
			CustomerOrgID: custOrgID,
			CountryID:     &countryID,
			Timestamp:     &now,
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if len(res.Candidates) != 0 || len(res.Excluded) == 0 || res.Excluded[0].ReasonCode != routing.ReasonEffectiveDateNotStarted {
			t.Fatalf("expected ReasonEffectiveDateNotStarted for entry, got %+v", res.Excluded)
		}
	})

	t.Run("Entry_Expired", func(t *testing.T) {
		snap := baseSnap()
		snap.Assignments = []routing.CustomerRouteAssignment{
			{
				CustomerOrgID: custOrgID,
				PlanID:        planID,
				Priority:      1,
				Status:        "active",
			},
		}
		snap.Plans = []routing.RoutePlan{
			{
				ID:       planID,
				Code:     "EFF-PLAN",
				Strategy: routing.StrategyPriority,
				Status:   routing.PlanActive,
				Entries: []routing.RoutePlanEntry{
					{
						ConnectionID: connID,
						Priority:     1,
						Weight:       100,
						Status:       "active",
						EffectiveTo:  &past,
					},
				},
			},
		}
		routing.SwapSnapshot(snap)

		res, err := engine.Resolve(context.Background(), routing.ResolveRequest{
			CustomerOrgID: custOrgID,
			CountryID:     &countryID,
			Timestamp:     &now,
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if len(res.Candidates) != 0 || len(res.Excluded) == 0 || res.Excluded[0].ReasonCode != routing.ReasonEffectiveDateExpired {
			t.Fatalf("expected ReasonEffectiveDateExpired for entry, got %+v", res.Excluded)
		}
	})
}

func TestTestedCoverage_AllowedWhenFlagTrue(t *testing.T) {
	rk := newRoutingKit(t)
	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")
	// Set coverage status to "tested"
	rk.setCoverage(t, provID, rk.countryID, "tested")

	rater := &mockRater{
		buyRates: map[uuid.UUID]*routing.RateLookupResult{
			connID: {Found: true, Price: decimal.RequireFromString("0.005000"), Currency: "USD"},
		},
		sellRates: map[uuid.UUID]*routing.RateLookupResult{
			rk.customerOrgID: {Found: true, Price: decimal.RequireFromString("0.010000"), Currency: "USD"},
		},
	}
	engine := routing.NewEngine(rk.e.Pool, rater, nil, nil)

	plan, err := rk.routingSvc.CreatePlan(context.Background(), rk.e.Override, &routing.RoutePlan{
		Name:     "Tested Coverage Allowed Plan",
		Code:     "TESTED-OK-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
		Status:   routing.PlanDraft,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	_, _, err = rk.routingSvc.CreateEntry(context.Background(), rk.e.Override, &routing.RoutePlanEntry{
		PlanID:              plan.ID,
		ConnectionID:        connID,
		Priority:            1,
		Weight:              100,
		AllowTestedCoverage: true, // explicitly allow tested coverage!
		Status:              "active",
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	_, _, err = rk.routingSvc.RequestPlanActivation(context.Background(), rk.e.Override, plan.ID)
	if err != nil {
		t.Fatalf("activate plan: %v", err)
	}
	_, _, err = rk.routingSvc.CreateAssignment(context.Background(), rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID,
		PlanID:        plan.ID,
		Priority:      1,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	_, _ = routing.Reload(context.Background(), rk.e.Pool)

	res, err := engine.Resolve(context.Background(), routing.ResolveRequest{
		CustomerOrgID:   rk.customerOrgID,
		CountryID:       &rk.countryID,
		DestinationE164: "+12025550199",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(res.Candidates) != 1 || res.Candidates[0].ConnectionID != connID {
		t.Fatalf("expected 1 candidate when coverage is 'tested' and AllowTestedCoverage=true, got candidates=%+v, excluded=%+v", res.Candidates, res.Excluded)
	}
}

func TestStrategy_Weighted_RepeatableWithFixedSeed(t *testing.T) {
	custOrgID := uuid.New()
	countryID := uuid.New()
	planID := uuid.New()
	provID := uuid.New()
	connA := uuid.New()
	connB := uuid.New()
	connC := uuid.New()

	snap := &routing.RoutingSnapshot{
		BuildAt:     time.Now().UTC(),
		Overrides:   make(map[uuid.UUID]routing.ConnectionOverride),
		Connections: make(map[uuid.UUID]routing.SnapshotConnection),
		Coverage:    make(map[string]string),
		Assignments: []routing.CustomerRouteAssignment{
			{CustomerOrgID: custOrgID, PlanID: planID, Status: "active"},
		},
		Plans: []routing.RoutePlan{
			{
				ID:       planID,
				Code:     "WEIGHT-SEED",
				Strategy: routing.StrategyWeighted,
				Status:   routing.PlanActive,
				Entries: []routing.RoutePlanEntry{
					{ConnectionID: connA, Weight: 50, Status: "active"},
					{ConnectionID: connB, Weight: 30, Status: "active"},
					{ConnectionID: connC, Weight: 20, Status: "active"},
				},
			},
		},
	}
	for _, c := range []uuid.UUID{connA, connB, connC} {
		snap.Connections[c] = routing.SnapshotConnection{ID: c, ProviderID: provID, ConnectionStatus: "active", ProviderStatus: "active"}
	}
	snap.Coverage[fmt.Sprintf("%s:%s", provID, countryID)] = "active"
	routing.SwapSnapshot(snap)

	const fixedSeed = 42
	engine1 := routing.NewEngine(nil, nil, nil, rand.NewSource(fixedSeed))
	engine2 := routing.NewEngine(nil, nil, nil, rand.NewSource(fixedSeed))

	req := routing.ResolveRequest{
		CustomerOrgID:   custOrgID,
		DestinationE164: "+12025550100",
		CountryID:       &countryID,
	}

	for i := 0; i < 100; i++ {
		r1, err1 := engine1.Resolve(context.Background(), req)
		r2, err2 := engine2.Resolve(context.Background(), req)
		if err1 != nil || err2 != nil {
			t.Fatalf("resolve error at step %d: %v / %v", i, err1, err2)
		}
		if r1.Candidate.ConnectionID != r2.Candidate.ConnectionID {
			t.Fatalf("step %d: fixed seed %d produced different top candidate: %s vs %s", i, fixedSeed, r1.Candidate.ConnectionID, r2.Candidate.ConnectionID)
		}
		for rankIdx := range r1.Candidates {
			if r1.Candidates[rankIdx].ConnectionID != r2.Candidates[rankIdx].ConnectionID {
				t.Fatalf("step %d rank %d: fixed seed %d produced different order: %s vs %s", i, rankIdx, fixedSeed, r1.Candidates[rankIdx].ConnectionID, r2.Candidates[rankIdx].ConnectionID)
			}
		}
	}
}

func TestDestinationNumberFormats_AllThree(t *testing.T) {
	rk := newRoutingKit(t)
	provID := rk.createProvider(t, "active")
	connPlus := rk.createConnection(t, provID, "active")
	connDigits := rk.createConnection(t, provID, "active")
	connNational := rk.createConnection(t, provID, "active")
	rk.setCoverage(t, provID, rk.countryID, "active")

	rater := &mockRater{
		buyRates: map[uuid.UUID]*routing.RateLookupResult{
			connPlus:     {Found: true, Price: decimal.RequireFromString("0.005000"), Currency: "USD"},
			connDigits:   {Found: true, Price: decimal.RequireFromString("0.005000"), Currency: "USD"},
			connNational: {Found: true, Price: decimal.RequireFromString("0.005000"), Currency: "USD"},
		},
		sellRates: map[uuid.UUID]*routing.RateLookupResult{
			rk.customerOrgID: {Found: true, Price: decimal.RequireFromString("0.010000"), Currency: "USD"},
		},
	}
	engine := routing.NewEngine(rk.e.Pool, rater, nil, nil)

	plan, err := rk.routingSvc.CreatePlan(context.Background(), rk.e.Override, &routing.RoutePlan{
		Name:     "All Three Formats Plan",
		Code:     "FMT3-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
		Status:   routing.PlanDraft,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}

	formats := []struct {
		connID uuid.UUID
		format string
		prio   int
		want   string
	}{
		{connPlus, "e164_plus", 1, "+12025550199"},
		{connDigits, "e164_digits", 2, "12025550199"},
		{connNational, "national", 3, "2025550199"},
	}

	for _, f := range formats {
		_, _, err := rk.routingSvc.CreateEntry(context.Background(), rk.e.Override, &routing.RoutePlanEntry{
			PlanID:                  plan.ID,
			ConnectionID:            f.connID,
			Priority:                f.prio,
			Weight:                  33,
			DestinationNumberFormat: f.format,
			Status:                  "active",
		})
		if err != nil {
			t.Fatalf("create entry (%s): %v", f.format, err)
		}
	}

	_, _, err = rk.routingSvc.RequestPlanActivation(context.Background(), rk.e.Override, plan.ID)
	if err != nil {
		t.Fatalf("activate plan: %v", err)
	}
	_, _, err = rk.routingSvc.CreateAssignment(context.Background(), rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID,
		PlanID:        plan.ID,
		Priority:      1,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	_, _ = routing.Reload(context.Background(), rk.e.Pool)

	res, err := engine.Resolve(context.Background(), routing.ResolveRequest{
		CustomerOrgID:   rk.customerOrgID,
		CountryID:       &rk.countryID,
		DestinationE164: "+12025550199",
	})
	if err != nil || len(res.Candidates) != 3 {
		t.Fatalf("resolve: err=%v, candidates=%d", err, len(res.Candidates))
	}

	for i, f := range formats {
		got := res.Candidates[i]
		if got.DestinationNumberFormat != f.format {
			t.Fatalf("candidate %d: expected format %q, got %q", i, f.format, got.DestinationNumberFormat)
		}
		if got.FormattedDestination != f.want {
			t.Fatalf("candidate %d (%s): expected FormattedDestination %q, got %q", i, f.format, f.want, got.FormattedDestination)
		}
	}
}

func TestGovernance_DeleteEntryOnActivePlanRequiresApproval(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()
	provID := rk.createProvider(t, "active")
	conn1 := rk.createConnection(t, provID, "active")
	conn2 := rk.createConnection(t, provID, "active")

	plan, err := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:     "Active Delete Entry Governance Plan",
		Code:     "DEL-GOV-" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
		Status:   routing.PlanDraft,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	entry1, _, err := rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID:       plan.ID,
		ConnectionID: conn1,
		Priority:     1,
		Weight:       50,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("create entry1: %v", err)
	}
	_, _, err = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID:       plan.ID,
		ConnectionID: conn2,
		Priority:     2,
		Weight:       50,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("create entry2: %v", err)
	}
	_, _, err = rk.routingSvc.RequestPlanActivation(ctx, rk.e.Override, plan.ID)
	if err != nil {
		t.Fatalf("activate plan: %v", err)
	}

	// Maker deletes entry1 on ACTIVE plan -> must NOT archive immediately, must return ChangeRequest
	cr, err := rk.routingSvc.DeleteEntry(ctx, rk.e.Maker, entry1.ID)
	if err != nil {
		t.Fatalf("maker delete entry on active plan: %v", err)
	}
	if cr == nil {
		t.Fatal("expected pending ChangeRequest when Maker deletes entry on ACTIVE plan, got nil")
	}
	if cr.Operation != "archive" || cr.EntityType != "route_plan_entry_change" {
		t.Fatalf("unexpected change request operation/type: %+v", cr)
	}

	// Verify entry1 is STILL active in DB before approval
	var statusBefore string
	_ = rk.e.Pool.QueryRow(ctx, `SELECT status FROM route_plan_entries WHERE id = $1`, entry1.ID).Scan(&statusBefore)
	if statusBefore != "active" {
		t.Fatalf("expected entry status to remain 'active' before approval, got %q", statusBefore)
	}

	// Maker cannot self-approve
	makerWithApprove := testkit.WithPermissions(rk.e.Maker, "routing.entry.approve")
	err = rk.e.Approval.Approve(ctx, makerWithApprove, cr.ID, "self approve delete")
	if !errors.Is(err, approval.ErrMakerCannotApproveOwnChange) {
		t.Fatalf("expected ErrMakerCannotApproveOwnChange, got %v", err)
	}

	// Checker approves -> archives entry1
	err = rk.e.Approval.Approve(ctx, rk.e.Checker, cr.ID, "Checker approved entry removal")
	if err != nil {
		t.Fatalf("checker approve entry removal: %v", err)
	}

	var statusAfter string
	_ = rk.e.Pool.QueryRow(ctx, `SELECT status FROM route_plan_entries WHERE id = $1`, entry1.ID).Scan(&statusAfter)
	if statusAfter != "archived" {
		t.Fatalf("expected entry status 'archived' after Checker approval, got %q", statusAfter)
	}
}

func TestExports_AllFourEntities_CSVAndJSON_FormulaProtection(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()
	provID := rk.createProvider(t, "active")
	connID := rk.createConnection(t, provID, "active")

	plan, err := rk.routingSvc.CreatePlan(ctx, rk.e.Override, &routing.RoutePlan{
		Name:        "=CMD|' /C calc'!A0 Plan",
		Code:        "EXP4-" + strings.ToUpper(testkit.RandHex(4)),
		Description: "+SUM(1,2)",
		Strategy:    routing.StrategyPriority,
		Status:      routing.PlanDraft,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}

	_, _, err = rk.routingSvc.CreateEntry(ctx, rk.e.Override, &routing.RoutePlanEntry{
		PlanID:       plan.ID,
		ConnectionID: connID,
		Priority:     1,
		Weight:       100,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}

	_, _, err = rk.routingSvc.CreateAssignment(ctx, rk.e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: rk.customerOrgID,
		PlanID:        plan.ID,
		Priority:      1,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	_, _, err = rk.routingSvc.CreateRule(ctx, rk.e.Override, &routing.RoutingRule{
		Name:              "@MALICIOUS_RULE",
		Priority:          1,
		ActionType:        "block",
		ActionBlockReason: "-2+3+cmd",
		Status:            "active",
		Conditions:        map[string]interface{}{"prefix": "+1999"},
	})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}

	// 1. Plans CSV & JSON
	plansCSV, err := rk.routingSvc.ExportPlansCSV(ctx, rk.e.Override)
	if err != nil {
		t.Fatalf("ExportPlansCSV: %v", err)
	}
	if !strings.Contains(string(plansCSV), "'=CMD|") || !strings.Contains(string(plansCSV), "'+SUM(1,2)") {
		t.Fatalf("expected formula sanitization in Plans CSV, got: %s", string(plansCSV))
	}
	plansJSON, _, err := rk.routingSvc.ListPlans(ctx, rk.e.Override, "", 1, 10000)
	if err != nil || len(plansJSON) == 0 {
		t.Fatalf("ListPlans JSON: err=%v, len=%d", err, len(plansJSON))
	}

	// 2. Entries CSV & JSON
	entriesCSV, err := rk.routingSvc.ExportEntriesCSV(ctx, rk.e.Override, plan.ID)
	if err != nil {
		t.Fatalf("ExportEntriesCSV: %v", err)
	}
	if !strings.Contains(string(entriesCSV), connID.String()) {
		t.Fatalf("expected connID in Entries CSV, got: %s", string(entriesCSV))
	}
	entriesJSON, err := rk.routingSvc.ListEntries(ctx, rk.e.Override, plan.ID)
	if err != nil || len(entriesJSON) != 1 {
		t.Fatalf("ListEntries JSON: err=%v, len=%d", err, len(entriesJSON))
	}

	// 3. Assignments CSV & JSON
	assignsCSV, err := rk.routingSvc.ExportAssignmentsCSV(ctx, rk.e.Override)
	if err != nil {
		t.Fatalf("ExportAssignmentsCSV: %v", err)
	}
	if !strings.Contains(string(assignsCSV), "'=CMD|") {
		t.Fatalf("expected formula sanitization of PlanName in Assignments CSV, got: %s", string(assignsCSV))
	}
	assignsJSON, _, err := rk.routingSvc.ListAssignments(ctx, rk.e.Override, nil, 1, 10000)
	if err != nil || len(assignsJSON) == 0 {
		t.Fatalf("ListAssignments JSON: err=%v, len=%d", err, len(assignsJSON))
	}

	// 4. Rules CSV & JSON
	rulesCSV, err := rk.routingSvc.ExportRulesCSV(ctx, rk.e.Override)
	if err != nil {
		t.Fatalf("ExportRulesCSV: %v", err)
	}
	if !strings.Contains(string(rulesCSV), "'@MALICIOUS_RULE") || !strings.Contains(string(rulesCSV), "'-2+3+cmd") {
		t.Fatalf("expected formula sanitization in Rules CSV, got: %s", string(rulesCSV))
	}
	rulesJSON, _, err := rk.routingSvc.ListRules(ctx, rk.e.Override, "", 1, 10000)
	if err != nil || len(rulesJSON) == 0 {
		t.Fatalf("ListRules JSON: err=%v, len=%d", err, len(rulesJSON))
	}
}

func TestDedicatedRoutingRoles_MasterDataRolesLackRoutingPerms(t *testing.T) {
	rk := newRoutingKit(t)
	ctx := context.Background()

	// 1. Verify Master Data Editor and Master Data Approver hold ZERO routing.* permissions
	var mdRoutingCount int
	err := rk.e.Pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM role_permissions rp
		JOIN roles r ON r.id = rp.role_id
		WHERE r.name IN ('Master Data Editor', 'Master Data Approver')
		  AND rp.permission_code LIKE 'routing.%'
	`).Scan(&mdRoutingCount)
	if err != nil {
		t.Fatalf("query master data routing perms: %v", err)
	}
	if mdRoutingCount != 0 {
		t.Fatalf("expected Master Data Editor/Approver to hold 0 routing.* permissions, got %d", mdRoutingCount)
	}

	// 2. Verify Routing Maker and Routing Checker exist and hold routing.* permissions
	var makerCount, checkerCount int
	err = rk.e.Pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM role_permissions rp
		JOIN roles r ON r.id = rp.role_id
		WHERE r.name = 'Routing Maker' AND rp.permission_code LIKE 'routing.%'
	`).Scan(&makerCount)
	if err != nil || makerCount == 0 {
		t.Fatalf("expected Routing Maker role to hold routing.* permissions, count=%d, err=%v", makerCount, err)
	}

	err = rk.e.Pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM role_permissions rp
		JOIN roles r ON r.id = rp.role_id
		WHERE r.name = 'Routing Checker' AND rp.permission_code LIKE 'routing.%'
	`).Scan(&checkerCount)
	if err != nil || checkerCount == 0 {
		t.Fatalf("expected Routing Checker role to hold routing.* permissions, count=%d, err=%v", checkerCount, err)
	}

	// 3. Verify neither Routing Maker nor Routing Checker holds routing.override or routing.entry.override
	var overrideCount int
	err = rk.e.Pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM role_permissions rp
		JOIN roles r ON r.id = rp.role_id
		WHERE r.name IN ('Routing Maker', 'Routing Checker')
		  AND rp.permission_code IN ('routing.override', 'routing.entry.override')
	`).Scan(&overrideCount)
	if err != nil {
		t.Fatalf("query override perms: %v", err)
	}
	if overrideCount != 0 {
		t.Fatalf("expected Routing Maker and Routing Checker to hold 0 override permissions, got %d", overrideCount)
	}
}

