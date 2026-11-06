package rating

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

type Engine interface {
	LookupSellRate(ctx context.Context, customerOrgID uuid.UUID, countryID uuid.UUID, operatorID *uuid.UUID, msgType string, at time.Time) (*RateMatch, error)
	LookupBuyRate(ctx context.Context, cardID uuid.UUID, countryID uuid.UUID, operatorID *uuid.UUID, msgType string, at time.Time) (*RateMatch, error)
	Reload(ctx context.Context) error
}

// -----------------------------------------------------------------------------
// 1. DB-DIRECT ENGINE
// -----------------------------------------------------------------------------

type DBEngine struct {
	repo Repository
}

func NewDBEngine(repo Repository) *DBEngine {
	return &DBEngine{repo: repo}
}

func (e *DBEngine) Reload(ctx context.Context) error {
	return nil // No-op for direct DB queries
}

func (e *DBEngine) LookupSellRate(ctx context.Context, customerOrgID uuid.UUID, countryID uuid.UUID, operatorID *uuid.UUID, msgType string, at time.Time) (*RateMatch, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if msgType == "" {
		msgType = "default"
	}

	// 1. Customer's active assignment -> get sell card
	assign, err := e.repo.GetActiveCustomerAssignmentAt(ctx, customerOrgID, at)
	if err != nil {
		return nil, fmt.Errorf("db lookup customer assignment: %w", err)
	}
	if assign == nil {
		return &RateMatch{Found: false}, nil
	}

	card, err := e.repo.GetCardByID(ctx, assign.CardID)
	if err != nil || card == nil || card.Status != "active" || card.Archived {
		return &RateMatch{Found: false}, nil
	}

	// 2. On that card: operator-specific rate (exact match)
	if operatorID != nil {
		opRate, err := e.repo.GetActiveRateAt(ctx, card.ID, countryID, operatorID, msgType, at)
		if err != nil {
			return nil, fmt.Errorf("db lookup operator rate: %w", err)
		}
		if opRate != nil {
			return &RateMatch{
				Found:         true,
				RateCardID:    card.ID,
				RateCardName:  card.Name,
				RateID:        opRate.ID,
				CountryID:     countryID,
				CountryISO2:   opRate.CountryISO2,
				CountryName:   opRate.CountryName,
				OperatorID:    operatorID,
				OperatorName:  safeStr(opRate.OperatorName),
				MessageType:   opRate.MessageType,
				Price:         opRate.Price,
				Currency:      card.CurrencyCode,
				EffectiveFrom: opRate.EffectiveFrom,
				EffectiveTo:   opRate.EffectiveTo,
				MatchedRule:   "operator_exact",
			}, nil
		}
	}

	// 3. On that card: country-wide rate (operator = NULL)
	countryRate, err := e.repo.GetActiveRateAt(ctx, card.ID, countryID, nil, msgType, at)
	if err != nil {
		return nil, fmt.Errorf("db lookup country rate: %w", err)
	}
	if countryRate != nil {
		return &RateMatch{
			Found:         true,
			RateCardID:    card.ID,
			RateCardName:  card.Name,
			RateID:        countryRate.ID,
			CountryID:     countryID,
			CountryISO2:   countryRate.CountryISO2,
			CountryName:   countryRate.CountryName,
			OperatorID:    nil,
			OperatorName:  "",
			MessageType:   countryRate.MessageType,
			Price:         countryRate.Price,
			Currency:      card.CurrencyCode,
			EffectiveFrom: countryRate.EffectiveFrom,
			EffectiveTo:   countryRate.EffectiveTo,
			MatchedRule:   "country_default",
		}, nil
	}

	// 4. No rate found
	return &RateMatch{Found: false}, nil
}

func (e *DBEngine) LookupBuyRate(ctx context.Context, cardID uuid.UUID, countryID uuid.UUID, operatorID *uuid.UUID, msgType string, at time.Time) (*RateMatch, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if msgType == "" {
		msgType = "default"
	}

	card, err := e.repo.GetCardByID(ctx, cardID)
	if err != nil || card == nil || card.Status != "active" || card.Archived {
		return &RateMatch{Found: false}, nil
	}

	// 1. Operator-specific rate
	if operatorID != nil {
		opRate, err := e.repo.GetActiveRateAt(ctx, cardID, countryID, operatorID, msgType, at)
		if err != nil {
			return nil, err
		}
		if opRate != nil {
			return &RateMatch{
				Found:         true,
				RateCardID:    card.ID,
				RateCardName:  card.Name,
				RateID:        opRate.ID,
				CountryID:     countryID,
				CountryISO2:   opRate.CountryISO2,
				CountryName:   opRate.CountryName,
				OperatorID:    operatorID,
				OperatorName:  safeStr(opRate.OperatorName),
				MessageType:   opRate.MessageType,
				Price:         opRate.Price,
				Currency:      card.CurrencyCode,
				EffectiveFrom: opRate.EffectiveFrom,
				EffectiveTo:   opRate.EffectiveTo,
				MatchedRule:   "operator_exact",
			}, nil
		}
	}

	// 2. Country-wide rate
	countryRate, err := e.repo.GetActiveRateAt(ctx, cardID, countryID, nil, msgType, at)
	if err != nil {
		return nil, err
	}
	if countryRate != nil {
		return &RateMatch{
			Found:         true,
			RateCardID:    card.ID,
			RateCardName:  card.Name,
			RateID:        countryRate.ID,
			CountryID:     countryID,
			CountryISO2:   countryRate.CountryISO2,
			CountryName:   countryRate.CountryName,
			OperatorID:    nil,
			OperatorName:  "",
			MessageType:   countryRate.MessageType,
			Price:         countryRate.Price,
			Currency:      card.CurrencyCode,
			EffectiveFrom: countryRate.EffectiveFrom,
			EffectiveTo:   countryRate.EffectiveTo,
			MatchedRule:   "country_default",
		}, nil
	}

	return &RateMatch{Found: false}, nil
}

// -----------------------------------------------------------------------------
// 2. HIGH-PERFORMANCE IN-MEMORY SNAPSHOT ENGINE
// -----------------------------------------------------------------------------

type snapshot struct {
	cardsByID          map[uuid.UUID]RateCard
	assignmentsByOrg   map[uuid.UUID][]CustomerRateAssignment
	ratesByCardCountry map[string][]Rate
}

type InMemoryEngine struct {
	repo    Repository
	current atomic.Pointer[snapshot]
}

func NewInMemoryEngine(repo Repository) *InMemoryEngine {
	eng := &InMemoryEngine{repo: repo}
	emptySnap := &snapshot{
		cardsByID:          make(map[uuid.UUID]RateCard),
		assignmentsByOrg:   make(map[uuid.UUID][]CustomerRateAssignment),
		ratesByCardCountry: make(map[string][]Rate),
	}
	eng.current.Store(emptySnap)
	return eng
}

func (e *InMemoryEngine) Reload(ctx context.Context) error {
	rates, cards, assignments, err := e.repo.LoadAllActiveRates(ctx)
	if err != nil {
		return fmt.Errorf("in-memory engine reload: %w", err)
	}

	snap := &snapshot{
		cardsByID:          make(map[uuid.UUID]RateCard, len(cards)),
		assignmentsByOrg:   make(map[uuid.UUID][]CustomerRateAssignment, len(assignments)),
		ratesByCardCountry: make(map[string][]Rate, len(rates)),
	}

	for _, c := range cards {
		snap.cardsByID[c.ID] = c
	}
	for _, a := range assignments {
		snap.assignmentsByOrg[a.CustomerOrgID] = append(snap.assignmentsByOrg[a.CustomerOrgID], a)
	}
	for _, r := range rates {
		key := r.CardID.String() + ":" + r.CountryID.String()
		snap.ratesByCardCountry[key] = append(snap.ratesByCardCountry[key], r)
	}

	e.current.Store(snap)
	return nil
}

func (e *InMemoryEngine) LookupSellRate(ctx context.Context, customerOrgID uuid.UUID, countryID uuid.UUID, operatorID *uuid.UUID, msgType string, at time.Time) (*RateMatch, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if msgType == "" {
		msgType = "default"
	}

	snap := e.current.Load()
	if snap == nil {
		return &RateMatch{Found: false}, nil
	}

	// 1. Resolve customer assignment
	assignments := snap.assignmentsByOrg[customerOrgID]
	var activeCardID *uuid.UUID
	for i := range assignments {
		a := &assignments[i]
		if a.Status == "active" && !a.EffectiveFrom.After(at) && (a.EffectiveTo == nil || a.EffectiveTo.After(at)) {
			activeCardID = &a.CardID
			break
		}
	}
	if activeCardID == nil {
		return &RateMatch{Found: false}, nil
	}

	card, ok := snap.cardsByID[*activeCardID]
	if !ok || card.Status != "active" || card.Archived {
		return &RateMatch{Found: false}, nil
	}

	// 2. Search destination rates
	key := card.ID.String() + ":" + countryID.String()
	rates := snap.ratesByCardCountry[key]
	if len(rates) == 0 {
		return &RateMatch{Found: false}, nil
	}

	var countryFallback *Rate

	for i := range rates {
		r := &rates[i]
		if r.Status != "active" || r.MessageType != msgType {
			continue
		}
		if r.EffectiveFrom.After(at) || (r.EffectiveTo != nil && !r.EffectiveTo.After(at)) {
			continue
		}

		if operatorID != nil && r.OperatorID != nil && *r.OperatorID == *operatorID {
			// Operator exact match!
			return &RateMatch{
				Found:         true,
				RateCardID:    card.ID,
				RateCardName:  card.Name,
				RateID:        r.ID,
				CountryID:     countryID,
				CountryISO2:   r.CountryISO2,
				CountryName:   r.CountryName,
				OperatorID:    operatorID,
				OperatorName:  safeStr(r.OperatorName),
				MessageType:   r.MessageType,
				Price:         r.Price,
				Currency:      card.CurrencyCode,
				EffectiveFrom: r.EffectiveFrom,
				EffectiveTo:   r.EffectiveTo,
				MatchedRule:   "operator_exact",
			}, nil
		}

		if r.OperatorID == nil && countryFallback == nil {
			countryFallback = r
		}
	}

	if countryFallback != nil {
		return &RateMatch{
			Found:         true,
			RateCardID:    card.ID,
			RateCardName:  card.Name,
			RateID:        countryFallback.ID,
			CountryID:     countryID,
			CountryISO2:   countryFallback.CountryISO2,
			CountryName:   countryFallback.CountryName,
			OperatorID:    nil,
			OperatorName:  "",
			MessageType:   countryFallback.MessageType,
			Price:         countryFallback.Price,
			Currency:      card.CurrencyCode,
			EffectiveFrom: countryFallback.EffectiveFrom,
			EffectiveTo:   countryFallback.EffectiveTo,
			MatchedRule:   "country_default",
		}, nil
	}

	return &RateMatch{Found: false}, nil
}

func (e *InMemoryEngine) LookupBuyRate(ctx context.Context, cardID uuid.UUID, countryID uuid.UUID, operatorID *uuid.UUID, msgType string, at time.Time) (*RateMatch, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if msgType == "" {
		msgType = "default"
	}

	snap := e.current.Load()
	if snap == nil {
		return &RateMatch{Found: false}, nil
	}

	card, ok := snap.cardsByID[cardID]
	if !ok || card.Status != "active" || card.Archived {
		return &RateMatch{Found: false}, nil
	}

	key := cardID.String() + ":" + countryID.String()
	rates := snap.ratesByCardCountry[key]
	if len(rates) == 0 {
		return &RateMatch{Found: false}, nil
	}

	var countryFallback *Rate

	for i := range rates {
		r := &rates[i]
		if r.Status != "active" || r.MessageType != msgType {
			continue
		}
		if r.EffectiveFrom.After(at) || (r.EffectiveTo != nil && !r.EffectiveTo.After(at)) {
			continue
		}

		if operatorID != nil && r.OperatorID != nil && *r.OperatorID == *operatorID {
			return &RateMatch{
				Found:         true,
				RateCardID:    card.ID,
				RateCardName:  card.Name,
				RateID:        r.ID,
				CountryID:     countryID,
				CountryISO2:   r.CountryISO2,
				CountryName:   r.CountryName,
				OperatorID:    operatorID,
				OperatorName:  safeStr(r.OperatorName),
				MessageType:   r.MessageType,
				Price:         r.Price,
				Currency:      card.CurrencyCode,
				EffectiveFrom: r.EffectiveFrom,
				EffectiveTo:   r.EffectiveTo,
				MatchedRule:   "operator_exact",
			}, nil
		}

		if r.OperatorID == nil && countryFallback == nil {
			countryFallback = r
		}
	}

	if countryFallback != nil {
		return &RateMatch{
			Found:         true,
			RateCardID:    card.ID,
			RateCardName:  card.Name,
			RateID:        countryFallback.ID,
			CountryID:     countryID,
			CountryISO2:   countryFallback.CountryISO2,
			CountryName:   countryFallback.CountryName,
			OperatorID:    nil,
			OperatorName:  "",
			MessageType:   countryFallback.MessageType,
			Price:         countryFallback.Price,
			Currency:      card.CurrencyCode,
			EffectiveFrom: countryFallback.EffectiveFrom,
			EffectiveTo:   countryFallback.EffectiveTo,
			MatchedRule:   "country_default",
		}, nil
	}

	return &RateMatch{Found: false}, nil
}

func safeStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
