package routing

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nyaruka/phonenumbers"
	"github.com/shopspring/decimal"
)

type Engine struct {
	pool            *pgxpool.Pool
	rater           RaterInterface
	numberer        NumbererInterface
	randMu          sync.Mutex
	randSrc         rand.Source
	globalMinMargin decimal.Decimal
}

func isValidE164(s string) bool {
	if len(s) < 8 || len(s) > 16 || s[0] != '+' {
		return false
	}
	if s[1] < '1' || s[1] > '9' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func NewEngine(pool *pgxpool.Pool, rater RaterInterface, numberer NumbererInterface, randSrc rand.Source) *Engine {
	if randSrc == nil {
		randSrc = rand.NewSource(time.Now().UnixNano())
	}
	return &Engine{
		pool:            pool,
		rater:           rater,
		numberer:        numberer,
		randSrc:         randSrc,
		globalMinMargin: decimal.Zero,
	}
}

func (e *Engine) SetGlobalMinMargin(m decimal.Decimal) {
	e.globalMinMargin = m
}

// Resolve returns the ordered list of eligible candidates for a given request.
// NO database queries are made during this call - all data comes from snapshot.
func (e *Engine) Resolve(ctx context.Context, req ResolveRequest) (*ResolveResult, error) {
	snap := CurrentSnapshot()
	if snap == nil {
		return nil, fmt.Errorf("routing snapshot not initialized")
	}

	result := &ResolveResult{
		Request:         req,
		SnapshotBuiltAt: snap.BuildAt,
		Candidates:      make([]Candidate, 0),
		Excluded:        make([]ExclusionReason, 0),
		RulesApplied:    make([]string, 0),
	}

	evalTime := time.Now().UTC()
	if req.Timestamp != nil && !req.Timestamp.IsZero() {
		evalTime = *req.Timestamp
	}

	// 0. Unconditional destination E.164 format check (never gated on e.numberer != nil)
	if req.DestinationE164 != "" && !isValidE164(req.DestinationE164) {
		result.Excluded = append(result.Excluded, ExclusionReason{
			Reason:     ReasonInvalidNumber,
			ReasonCode: ReasonInvalidNumber,
			Detail:     "destination number is not valid international E.164",
		})
		result.Exclusions = result.Excluded
		return result, nil
	}

	// 0b. Destination lookup (country and operator if numberer provided)
	var destCountryID uuid.UUID
	var destOperatorID *uuid.UUID
	if req.CountryID != nil && *req.CountryID != uuid.Nil {
		destCountryID = *req.CountryID
	}
	if req.OperatorID != nil {
		destOperatorID = req.OperatorID
	}
	if destCountryID == uuid.Nil && e.numberer != nil && req.DestinationE164 != "" {
		if numInfo, err := e.numberer.Lookup(req.DestinationE164); err == nil && numInfo != nil {
			destCountryID = numInfo.CountryID
			destOperatorID = numInfo.OperatorID
		}
	}

	// 1. Customer profile and policy checks
	if req.CustomerOrgID != uuid.Nil && len(snap.CustomerProfiles) > 0 {
		profile, exists := snap.CustomerProfiles[req.CustomerOrgID]
		if !exists || profile.OrgStatus == "suspended" || profile.OrgStatus == "inactive" {
			result.Excluded = append(result.Excluded, ExclusionReason{
				Reason:     ReasonCustomerSuspended,
				ReasonCode: ReasonCustomerSuspended,
				Detail:     "customer organization is suspended or inactive",
			})
			result.Exclusions = result.Excluded
			return result, nil
		}
		if len(profile.AllowedCountries) > 0 && destCountryID != uuid.Nil {
			allowed := false
			for _, cid := range profile.AllowedCountries {
				if cid == destCountryID {
					allowed = true
					break
				}
			}
			if !allowed {
				result.Excluded = append(result.Excluded, ExclusionReason{
					Reason:     ReasonDestinationNotAllowed,
					ReasonCode: ReasonDestinationNotAllowed,
					Detail:     "destination country not permitted by customer policy",
				})
				result.Exclusions = result.Excluded
				return result, nil
			}
		}
	}

	// 1b. Customer approved sender ID check
	senderIDVal := req.SenderID
	if senderIDVal == "" {
		senderIDVal = req.SenderIDValue
	}
	if senderIDVal != "" && req.CustomerOrgID != uuid.Nil && len(snap.ApprovedSenderIDs) > 0 {
		keyCountry := fmt.Sprintf("%s:%s:%s", req.CustomerOrgID, senderIDVal, destCountryID)
		keyGlobal := fmt.Sprintf("%s:%s", req.CustomerOrgID, senderIDVal)
		_, okC := snap.ApprovedSenderIDs[keyCountry]
		_, okG := snap.ApprovedSenderIDs[keyGlobal]
		if !okC && !okG {
			result.Excluded = append(result.Excluded, ExclusionReason{
				Reason:     ReasonSenderIDNotApproved,
				ReasonCode: ReasonSenderIDNotApproved,
				Detail:     fmt.Sprintf("sender ID %s is not approved for customer", senderIDVal),
			})
			result.Exclusions = result.Excluded
			return result, nil
		}
	}

	// 2. Find customer's route plan (country override > default assignment)
	plan, planExclusionReason := e.selectPlanWithReason(snap, req.CustomerOrgID, destCountryID, evalTime)
	if plan == nil {
		reason := ReasonPlanInactive
		detail := "no active route plan assigned to customer"
		if planExclusionReason != "" {
			reason = planExclusionReason
			detail = "plan or assignment excluded by effective date window"
		}
		result.Excluded = append(result.Excluded, ExclusionReason{
			Reason:     reason,
			ReasonCode: reason,
			Detail:     detail,
		})
		result.Exclusions = result.Excluded
		return result, nil
	}
	result.PlanUsed = plan.Code
	result.PlanID = &plan.ID
	result.StrategyUsed = plan.Strategy

	// 3. Apply routing rules (block → return early; force → pin; avoid/prefer → affect ordering)
	ruleAction, rulesApplied, forcedConn, preferredConn, avoidedConn, blockedReason := e.applyRules(snap, req, plan, destCountryID, destOperatorID, evalTime)
	if rulesApplied != nil {
		result.RulesApplied = rulesApplied
	}

	if ruleAction == "block" {
		result.Excluded = append(result.Excluded, ExclusionReason{
			Reason:     ReasonRuleBlock,
			ReasonCode: ReasonRuleBlock,
			Detail:     blockedReason,
		})
		result.Exclusions = result.Excluded
		return result, nil
	}

	// 4. Filter eligible entries
	var candidates []Candidate
	for _, entry := range plan.Entries {
		if entry.Status != "active" {
			result.Excluded = append(result.Excluded, ExclusionReason{
				ConnectionID: entry.ConnectionID,
				Reason:       ReasonEntryInactive,
				ReasonCode:   ReasonEntryInactive,
			})
			continue
		}
		if entry.EffectiveFrom != nil && evalTime.Before(*entry.EffectiveFrom) {
			result.Excluded = append(result.Excluded, ExclusionReason{
				ConnectionID: entry.ConnectionID,
				Reason:       ReasonEffectiveDateNotStarted,
				ReasonCode:   ReasonEffectiveDateNotStarted,
				Detail:       "entry effective_from is in the future",
			})
			continue
		}
		if entry.EffectiveTo != nil && evalTime.After(*entry.EffectiveTo) {
			result.Excluded = append(result.Excluded, ExclusionReason{
				ConnectionID: entry.ConnectionID,
				Reason:       ReasonEffectiveDateExpired,
				ReasonCode:   ReasonEffectiveDateExpired,
				Detail:       "entry effective_to has expired",
			})
			continue
		}

		// Check kill switch
		if _, killed := snap.Overrides[entry.ConnectionID]; killed {
			result.Excluded = append(result.Excluded, ExclusionReason{
				ConnectionID: entry.ConnectionID,
				Reason:       ReasonKillSwitchActive,
				ReasonCode:   ReasonKillSwitchActive,
			})
			continue
		}

		// Check connection and provider status
		conn, ok := snap.Connections[entry.ConnectionID]
		if !ok {
			result.Excluded = append(result.Excluded, ExclusionReason{
				ConnectionID: entry.ConnectionID,
				Reason:       ReasonConnectionInactive,
				ReasonCode:   ReasonConnectionInactive,
				Detail:       "connection not found",
			})
			continue
		}
		if conn.ProviderStatus != "active" {
			result.Excluded = append(result.Excluded, ExclusionReason{
				ConnectionID: entry.ConnectionID,
				Reason:       ReasonProviderInactive,
				ReasonCode:   ReasonProviderInactive,
			})
			continue
		}
		if conn.ConnectionStatus != "active" {
			result.Excluded = append(result.Excluded, ExclusionReason{
				ConnectionID: entry.ConnectionID,
				Reason:       ReasonConnectionInactive,
				ReasonCode:   ReasonConnectionInactive,
			})
			continue
		}

		// Check coverage
		covStatus := conn.CoverageStatus
		if covStatus == "" && destCountryID != uuid.Nil {
			covStatus = snap.GetCoverageStatus(conn.ProviderID, destCountryID, destOperatorID)
		}
		if covStatus == "" {
			covStatus = "inactive"
		}
		if covStatus == "tested" && !entry.AllowTestedCoverage {
			result.Excluded = append(result.Excluded, ExclusionReason{
				ConnectionID: entry.ConnectionID,
				Reason:       ReasonCoverageTestedDisallowed,
				ReasonCode:   ReasonCoverageTestedDisallowed,
				Detail:       "coverage is tested but entry does not allow tested coverage",
			})
			continue
		}
		if covStatus != "active" && !(entry.AllowTestedCoverage && covStatus == "tested") {
			result.Excluded = append(result.Excluded, ExclusionReason{
				ConnectionID: entry.ConnectionID,
				Reason:       ReasonCoverageNotActive,
				ReasonCode:   ReasonCoverageNotActive,
			})
			continue
		}

		// Check scope matching
		if entry.ScopeType == "country" && entry.ScopeCountryID != nil && destCountryID != uuid.Nil && *entry.ScopeCountryID != destCountryID {
			result.Excluded = append(result.Excluded, ExclusionReason{
				ConnectionID: entry.ConnectionID,
				Reason:       ReasonScopeMismatch,
				ReasonCode:   ReasonScopeMismatch,
				Detail:       "country scope does not match destination",
			})
			continue
		}
		if entry.ScopeType == "operator" && entry.ScopeOperatorID != nil && destOperatorID != nil && *entry.ScopeOperatorID != *destOperatorID {
			result.Excluded = append(result.Excluded, ExclusionReason{
				ConnectionID: entry.ConnectionID,
				Reason:       ReasonScopeMismatch,
				ReasonCode:   ReasonScopeMismatch,
				Detail:       "operator scope does not match destination",
			})
			continue
		}
		if entry.ScopeType == "prefix" && entry.ScopePrefix != "" && !strings.HasPrefix(req.DestinationE164, entry.ScopePrefix) {
			result.Excluded = append(result.Excluded, ExclusionReason{
				ConnectionID: entry.ConnectionID,
				Reason:       ReasonScopeMismatch,
				ReasonCode:   ReasonScopeMismatch,
				Detail:       "prefix scope does not match destination",
			})
			continue
		}

		// Check allowed sender ID types
		if len(entry.AllowedSenderIDTypes) > 0 && req.SenderIDType != "" {
			allowed := false
			for _, st := range entry.AllowedSenderIDTypes {
				if strings.EqualFold(st, req.SenderIDType) {
					allowed = true
					break
				}
			}
			if !allowed {
				result.Excluded = append(result.Excluded, ExclusionReason{
					ConnectionID: entry.ConnectionID,
					Reason:       ReasonSenderIDTypeNotAllowed,
					ReasonCode:   ReasonSenderIDTypeNotAllowed,
					Detail:       fmt.Sprintf("sender ID type %s not allowed", req.SenderIDType),
				})
				continue
			}
		}

		// Check exclude list (failover / Next())
		allExcludes := append(req.ExcludeIDs, req.Exclusions...)
		excluded := false
		for _, excID := range allExcludes {
			if excID == entry.ConnectionID {
				excluded = true
				break
			}
		}
		if excluded {
			result.Excluded = append(result.Excluded, ExclusionReason{
				ConnectionID: entry.ConnectionID,
				Reason:       ReasonExcludedByCaller,
				ReasonCode:   ReasonExcludedByCaller,
			})
			continue
		}

		// Force rule: only include the forced connection
		if ruleAction == "force" && forcedConn != nil && *forcedConn != entry.ConnectionID {
			result.Excluded = append(result.Excluded, ExclusionReason{
				ConnectionID: entry.ConnectionID,
				Reason:       ReasonRuleBlock,
				ReasonCode:   ReasonRuleBlock,
				Detail:       "rule forced another connection",
			})
			continue
		}

		cand := Candidate{
			ConnectionID:            entry.ConnectionID,
			ConnectionName:          entry.ConnectionName,
			ProviderID:              conn.ProviderID,
			Priority:                entry.Priority,
			Weight:                  entry.Weight,
			Cost:                    conn.Cost,
			DestinationNumberFormat: entry.DestinationNumberFormat,
			FormattedDestination:    formatDestinationNumber(req.DestinationE164, entry.DestinationNumberFormat),
		}

		// Rates and Margin validation via Phase 5 Rating Engine (with evalTime)
		if e.rater != nil {
			// 1. Lookup Sell Rate (MUST EXIST)
			sellRate, err := e.rater.LookupSellRate(ctx, req.CustomerOrgID, destCountryID, destOperatorID, evalTime)
			if err != nil || sellRate == nil || !sellRate.Found {
				result.Excluded = append(result.Excluded, ExclusionReason{
					ConnectionID: entry.ConnectionID,
					Reason:       ReasonNoSellRate,
					ReasonCode:   ReasonNoSellRate,
					Detail:       "no active sell rate found for customer and destination",
				})
				continue
			}
			cand.EstimatedSellRate = sellRate.Price
			cand.CurrencyCode = sellRate.Currency

			// 2. Lookup Buy Rate (MUST EXIST)
			buyRate, err := e.rater.LookupBuyRate(ctx, entry.ConnectionID, destCountryID, destOperatorID, evalTime)
			if err != nil || buyRate == nil || !buyRate.Found {
				result.Excluded = append(result.Excluded, ExclusionReason{
					ConnectionID: entry.ConnectionID,
					Reason:       ReasonNoBuyRate,
					ReasonCode:   ReasonNoBuyRate,
					Detail:       "no active buy rate found for connection and destination",
				})
				continue
			}
			cand.EstimatedBuyRate = buyRate.Price
			cand.Cost = buyRate.Price

			// 3. Currency Match Check (Different currencies are not comparable, never converted)
			if !strings.EqualFold(sellRate.Currency, buyRate.Currency) {
				result.Excluded = append(result.Excluded, ExclusionReason{
					ConnectionID: entry.ConnectionID,
					Reason:       ReasonCurrencyMismatch,
					ReasonCode:   ReasonCurrencyMismatch,
					Detail:       fmt.Sprintf("currency mismatch: sell=%s, buy=%s (not comparable)", sellRate.Currency, buyRate.Currency),
				})
				continue
			}

			// 4. Margin Check (evaluating entry override vs global minimum margin)
			minMargin := e.globalMinMargin
			if entry.MinMarginOverride != nil {
				minMargin = *entry.MinMarginOverride
			}
			margin := sellRate.Price.Sub(buyRate.Price)
			if margin.LessThan(minMargin) {
				result.Excluded = append(result.Excluded, ExclusionReason{
					ConnectionID: entry.ConnectionID,
					Reason:       ReasonMarginBelowMinimum,
					ReasonCode:   ReasonMarginBelowMinimum,
					Detail:       fmt.Sprintf("margin %s below required minimum %s", margin.StringFixed(6), minMargin.StringFixed(6)),
				})
				continue
			}
		}

		candidates = append(candidates, cand)
	}

	// 4. Apply strategy ordering & rule modifications (prefer / avoid)
	candidates = e.applyStrategy(plan.Strategy, candidates, preferredConn, avoidedConn)

	// Set 1-based ranks and default reason
	for i := range candidates {
		candidates[i].Rank = i + 1
		if i == 0 {
			candidates[i].Reason = "primary"
		} else {
			candidates[i].Reason = "failover"
		}
	}

	if candidates == nil {
		candidates = make([]Candidate, 0)
	}
	if result.Excluded == nil {
		result.Excluded = make([]ExclusionReason, 0)
	}
	result.Candidates = candidates
	result.AllCandidates = candidates
	result.Exclusions = result.Excluded
	if len(candidates) > 0 {
		top := candidates[0]
		result.Candidate = &top
	}
	result.SnapshotAgeMs = time.Since(snap.BuildAt).Milliseconds()
	return result, nil
}

// Next returns the next candidate after excluding failed ones.
func (e *Engine) Next(ctx context.Context, req ResolveRequest, extraExcludes ...[]uuid.UUID) (*Candidate, error) {
	for _, ex := range extraExcludes {
		req.ExcludeIDs = append(req.ExcludeIDs, ex...)
	}
	result, err := e.Resolve(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(result.Candidates) == 0 {
		return nil, nil
	}
	c := result.Candidates[0]
	return &c, nil
}

// Simulate runs Resolve with detailed trace logging.
func (e *Engine) Simulate(ctx context.Context, req SimulateRequest) (*SimulateResult, error) {
	var custOrgID uuid.UUID
	if req.CustomerOrgID != nil {
		custOrgID = *req.CustomerOrgID
	}
	senderID := req.SenderID
	if senderID == "" {
		senderID = req.SenderIDValue
	}
	resolveReq := ResolveRequest{
		CustomerOrgID:   custOrgID,
		DestinationE164: req.DestinationE164,
		CountryID:       req.CountryID,
		OperatorID:      req.OperatorID,
		TrafficProfile:  req.TrafficProfile,
		SenderIDType:    req.SenderIDType,
		SenderID:        senderID,
		SenderIDValue:   senderID,
		Timestamp:       req.Timestamp,
	}

	snap := CurrentSnapshot()
	if snap == nil {
		return nil, fmt.Errorf("routing snapshot not initialized")
	}

	res, err := e.Resolve(ctx, resolveReq)
	if err != nil {
		return nil, err
	}

	simResult := &SimulateResult{
		ResolveResult: *res,
		Trace:         make([]string, 0),
	}

	simResult.Trace = append(simResult.Trace, fmt.Sprintf("1. Normalization: destination=%s, customer=%s", req.DestinationE164, custOrgID))
	if res.PlanUsed != "" {
		simResult.Trace = append(simResult.Trace, fmt.Sprintf("2. Plan Selection: matched active plan '%s'", res.PlanUsed))
	} else {
		simResult.Trace = append(simResult.Trace, "2. Plan Selection: NO ACTIVE PLAN FOUND")
	}

	if len(res.RulesApplied) > 0 {
		simResult.Trace = append(simResult.Trace, fmt.Sprintf("3. Routing Rules: applied %d rules [%s]", len(res.RulesApplied), strings.Join(res.RulesApplied, ", ")))
	} else {
		simResult.Trace = append(simResult.Trace, "3. Routing Rules: no rules triggered")
	}

	simResult.Trace = append(simResult.Trace, fmt.Sprintf("4. Eligibility: %d candidates eligible, %d entries excluded", len(res.Candidates), len(res.Excluded)))
	for _, exc := range res.Excluded {
		simResult.Trace = append(simResult.Trace, fmt.Sprintf("   - Excluded conn %s: reason=%s (%s)", exc.ConnectionID, exc.Reason, exc.Detail))
	}

	simResult.Trace = append(simResult.Trace, fmt.Sprintf("5. Final Ranked Candidates (%d total):", len(res.Candidates)))
	for i, c := range res.Candidates {
		simResult.Trace = append(simResult.Trace, fmt.Sprintf("   #%d: conn=%s, priority=%d, weight=%d, buyRate=%s", i+1, c.ConnectionID, c.Priority, c.Weight, c.EstimatedBuyRate.StringFixed(4)))
	}

	simResult.Trace = append(simResult.Trace, fmt.Sprintf("Snapshot built at %s (age %s)", snap.BuildAt.Format(time.RFC3339), time.Since(snap.BuildAt).Round(time.Millisecond)))

	return simResult, nil
}

func (e *Engine) selectPlanWithReason(snap *RoutingSnapshot, customerOrgID uuid.UUID, countryID uuid.UUID, evalTime time.Time) (*RoutePlan, string) {
	if evalTime.IsZero() {
		evalTime = time.Now().UTC()
	}

	var dateReason string
	checkPlanDates := func(p *RoutePlan) bool {
		if p.EffectiveFrom != nil && evalTime.Before(*p.EffectiveFrom) {
			dateReason = ReasonEffectiveDateNotStarted
			return false
		}
		if p.EffectiveTo != nil && evalTime.After(*p.EffectiveTo) {
			dateReason = ReasonEffectiveDateExpired
			return false
		}
		return true
	}

	// 1. Look for country-specific assignment first
	if countryID != uuid.Nil {
		for _, a := range snap.Assignments {
			if a.CustomerOrgID == customerOrgID && a.CountryID != nil && *a.CountryID == countryID && a.Status == "active" {
				if a.EffectiveFrom != nil && evalTime.Before(*a.EffectiveFrom) {
					dateReason = ReasonEffectiveDateNotStarted
					continue
				}
				if a.EffectiveTo != nil && evalTime.After(*a.EffectiveTo) {
					dateReason = ReasonEffectiveDateExpired
					continue
				}
				for i := range snap.Plans {
					if snap.Plans[i].ID == a.PlanID && snap.Plans[i].Status == PlanActive {
						if checkPlanDates(&snap.Plans[i]) {
							return &snap.Plans[i], ""
						}
					}
				}
			}
		}
	}

	// 2. Look for default assignment (country_id IS NULL)
	for _, a := range snap.Assignments {
		if a.CustomerOrgID == customerOrgID && a.CountryID == nil && a.Status == "active" {
			if a.EffectiveFrom != nil && evalTime.Before(*a.EffectiveFrom) {
				dateReason = ReasonEffectiveDateNotStarted
				continue
			}
			if a.EffectiveTo != nil && evalTime.After(*a.EffectiveTo) {
				dateReason = ReasonEffectiveDateExpired
				continue
			}
			for i := range snap.Plans {
				if snap.Plans[i].ID == a.PlanID && snap.Plans[i].Status == PlanActive {
					if checkPlanDates(&snap.Plans[i]) {
						return &snap.Plans[i], ""
					}
				}
			}
		}
	}

	return nil, dateReason
}

func (e *Engine) applyRules(
	snap *RoutingSnapshot,
	req ResolveRequest,
	plan *RoutePlan,
	countryID uuid.UUID,
	operatorID *uuid.UUID,
	evalTime time.Time,
) (actionType string, rulesApplied []string, forcedConn *uuid.UUID, preferredConn *uuid.UUID, avoidedConn *uuid.UUID, blockedReason string) {
	if evalTime.IsZero() {
		evalTime = time.Now().UTC()
	}

	for _, rule := range snap.Rules {
		if rule.Status != "active" {
			continue
		}
		if rule.EffectiveFrom != nil && evalTime.Before(*rule.EffectiveFrom) {
			continue
		}
		if rule.EffectiveTo != nil && evalTime.After(*rule.EffectiveTo) {
			continue
		}

		// Evaluate conditions
		if !matchConditions(rule.Conditions, req, countryID, operatorID) {
			continue
		}

		rulesApplied = append(rulesApplied, rule.Name)

		switch rule.ActionType {
		case "block":
			actionType = "block"
			blockedReason = rule.ActionBlockReason
			if blockedReason == "" {
				blockedReason = fmt.Sprintf("blocked by rule: %s", rule.Name)
			}
			return
		case "force":
			actionType = "force"
			forcedConn = rule.ActionConnectionID
		case "prefer":
			preferredConn = rule.ActionConnectionID
		case "avoid":
			avoidedConn = rule.ActionConnectionID
		}
	}

	return
}

func matchConditions(conds map[string]interface{}, req ResolveRequest, countryID uuid.UUID, operatorID *uuid.UUID) bool {
	if len(conds) == 0 {
		return true
	}

	if val, ok := conds["customer_org_id"]; ok && val != nil {
		if valStr, ok := val.(string); ok && valStr != "" {
			if id, err := uuid.Parse(valStr); err == nil && id != req.CustomerOrgID {
				return false
			}
		}
	}

	if val, ok := conds["country_id"]; ok && val != nil {
		if valStr, ok := val.(string); ok && valStr != "" {
			if id, err := uuid.Parse(valStr); err == nil && id != countryID {
				return false
			}
		}
	}

	if val, ok := conds["operator_id"]; ok && val != nil && operatorID != nil {
		if valStr, ok := val.(string); ok && valStr != "" {
			if id, err := uuid.Parse(valStr); err == nil && id != *operatorID {
				return false
			}
		}
	}

	if val, ok := conds["prefix"]; ok && val != nil {
		if prefix, ok := val.(string); ok && prefix != "" {
			if !strings.HasPrefix(req.DestinationE164, prefix) {
				return false
			}
		}
	}

	if val, ok := conds["sender_id_type"]; ok && val != nil {
		if sit, ok := val.(string); ok && sit != "" {
			if !strings.EqualFold(sit, req.SenderIDType) {
				return false
			}
		}
	}

	if val, ok := conds["traffic_profile"]; ok && val != nil {
		if tp, ok := val.(string); ok && tp != "" {
			if !strings.EqualFold(tp, req.TrafficProfile) {
				return false
			}
		}
	}

	if val, ok := conds["sender_id"]; ok && val != nil {
		if sid, ok := val.(string); ok && sid != "" {
			if sid != req.SenderID {
				return false
			}
		}
	}

	return true
}

func (e *Engine) applyStrategy(
	strategy Strategy,
	candidates []Candidate,
	preferredConn *uuid.UUID,
	avoidedConn *uuid.UUID,
) []Candidate {
	if len(candidates) <= 1 {
		return candidates
	}

	switch strategy {
	case StrategyPriority:
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].Priority != candidates[j].Priority {
				return candidates[i].Priority < candidates[j].Priority
			}
			return candidates[i].ConnectionID.String() < candidates[j].ConnectionID.String()
		})

	case StrategyLeastCost:
		// Sort by cost or estimated buy rate ascending using exact decimal arithmetic
		sort.SliceStable(candidates, func(i, j int) bool {
			costI := candidates[i].Cost
			if costI.IsZero() {
				costI = candidates[i].EstimatedBuyRate
			}
			costJ := candidates[j].Cost
			if costJ.IsZero() {
				costJ = candidates[j].EstimatedBuyRate
			}
			cmp := costI.Cmp(costJ)
			if cmp != 0 {
				return cmp < 0
			}
			if candidates[i].Priority != candidates[j].Priority {
				return candidates[i].Priority < candidates[j].Priority
			}
			return candidates[i].ConnectionID.String() < candidates[j].ConnectionID.String()
		})

	case StrategyWeighted:
		candidates = e.weightedShuffle(candidates)
	}

	// Apply rule adjustments (prefer -> move to front, avoid -> move to end)
	if preferredConn != nil {
		for i, c := range candidates {
			if c.ConnectionID == *preferredConn {
				pref := candidates[i]
				candidates = append(candidates[:i], candidates[i+1:]...)
				candidates = append([]Candidate{pref}, candidates...)
				break
			}
		}
	}
	if avoidedConn != nil {
		for i, c := range candidates {
			if c.ConnectionID == *avoidedConn {
				avoid := candidates[i]
				candidates = append(candidates[:i], candidates[i+1:]...)
				candidates = append(candidates, avoid)
				break
			}
		}
	}

	return candidates
}

func (e *Engine) weightedShuffle(candidates []Candidate) []Candidate {
	if len(candidates) <= 1 {
		return candidates
	}

	e.randMu.Lock()
	defer e.randMu.Unlock()

	rng := rand.New(e.randSrc)
	remaining := make([]Candidate, len(candidates))
	copy(remaining, candidates)

	result := make([]Candidate, 0, len(candidates))

	for len(remaining) > 0 {
		totalWeight := 0
		for _, c := range remaining {
			w := c.Weight
			if w < 1 {
				w = 1
			}
			totalWeight += w
		}

		pick := rng.Intn(totalWeight)
		cum := 0
		chosenIdx := 0
		for i, c := range remaining {
			w := c.Weight
			if w < 1 {
				w = 1
			}
			cum += w
			if pick < cum {
				chosenIdx = i
				break
			}
		}

		result = append(result, remaining[chosenIdx])
		remaining = append(remaining[:chosenIdx], remaining[chosenIdx+1:]...)
	}

	return result
}

func formatDestinationNumber(e164 string, format string) string {
	if e164 == "" {
		return ""
	}
	cleanE164 := e164
	if !strings.HasPrefix(cleanE164, "+") {
		cleanE164 = "+" + cleanE164
	}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "e164_digits", "international_no_plus":
		return strings.TrimPrefix(cleanE164, "+")
	case "national":
		if num, err := phonenumbers.Parse(cleanE164, ""); err == nil && num != nil && num.NationalNumber != nil {
			return strconv.FormatUint(*num.NationalNumber, 10)
		}
		return strings.TrimPrefix(cleanE164, "+")
	case "e164_plus", "e164":
		fallthrough
	default:
		return cleanE164
	}
}
