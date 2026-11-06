package routing

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/znero-international/znero/backend/internal/number"
	"github.com/znero-international/znero/backend/internal/rating"
)

type RatingEngineAdapter struct {
	engine rating.Engine
}

func NewRatingEngineAdapter(eng rating.Engine) *RatingEngineAdapter {
	return &RatingEngineAdapter{engine: eng}
}

func (a *RatingEngineAdapter) LookupBuyRate(ctx context.Context, connectionID uuid.UUID, countryID uuid.UUID, operatorID *uuid.UUID) (*RateLookupResult, error) {
	if a.engine == nil {
		return &RateLookupResult{Found: false}, nil
	}
	snap := CurrentSnapshot()
	if snap == nil {
		return &RateLookupResult{Found: false}, nil
	}
	conn, ok := snap.Connections[connectionID]
	if !ok || conn.BuyCardID == nil {
		return &RateLookupResult{Found: false}, nil
	}

	match, err := a.engine.LookupBuyRate(ctx, *conn.BuyCardID, countryID, operatorID, "default", time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if match == nil || !match.Found {
		return &RateLookupResult{Found: false}, nil
	}
	return &RateLookupResult{
		Found:    true,
		Price:    match.Price,
		Currency: match.Currency,
	}, nil
}

func (a *RatingEngineAdapter) LookupSellRate(ctx context.Context, customerOrgID uuid.UUID, countryID uuid.UUID, operatorID *uuid.UUID) (*RateLookupResult, error) {
	if a.engine == nil {
		return &RateLookupResult{Found: false}, nil
	}
	match, err := a.engine.LookupSellRate(ctx, customerOrgID, countryID, operatorID, "default", time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if match == nil || !match.Found {
		return &RateLookupResult{Found: false}, nil
	}
	return &RateLookupResult{
		Found:    true,
		Price:    match.Price,
		Currency: match.Currency,
	}, nil
}

type NumberServiceAdapter struct {
	service *number.Service
}

func NewNumberServiceAdapter(svc *number.Service) *NumberServiceAdapter {
	return &NumberServiceAdapter{service: svc}
}

func (a *NumberServiceAdapter) Lookup(destinationE164 string) (*NumberInfo, error) {
	if a.service == nil {
		return nil, nil
	}
	res := a.service.Lookup(context.Background(), number.LookupRequest{Number: destinationE164})
	if res.Country == nil || res.Country.ID == uuid.Nil {
		return nil, nil
	}
	var opID *uuid.UUID
	if res.Operator != nil && res.Operator.ID != nil && *res.Operator.ID != uuid.Nil {
		opID = res.Operator.ID
	}
	return &NumberInfo{
		CountryID:  res.Country.ID,
		OperatorID: opID,
		Prefix:     res.MatchedPrefix,
	}, nil
}
