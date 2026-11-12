package analytics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/znero-international/znero/backend/pkg/tenant"
)

var (
	ErrUnauthorizedScope = errors.New("unauthorized analytics scope for current tenant")
	ErrPlatformOnly      = errors.New("this resource is restricted to platform administrators")
)

// Service provides high-level business logic, caching, and tenant authorization for analytics.
type Service struct {
	repo       *Repository
	logger     *slog.Logger
	cacheMu    sync.RWMutex
	liveCached *NOCDashboard
	liveExpiry time.Time
}

func NewService(repo *Repository, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		repo:   repo,
		logger: logger.With(slog.String("component", "analytics_service")),
	}
}

// sanitizeFilter enforces tenant isolation boundaries based on caller's identity.
func (s *Service) sanitizeFilter(filter *DashboardFilter, tc *tenant.Context) error {
	if tc == nil {
		return ErrUnauthorizedScope
	}

	if filter.StartTime.IsZero() {
		filter.StartTime = time.Now().UTC().Add(-24 * time.Hour)
	}
	if filter.EndTime.IsZero() {
		filter.EndTime = time.Now().UTC()
	}
	if filter.Timezone == "" {
		filter.Timezone = "UTC"
	}

	// Default granularity
	if filter.Granularity == "" {
		duration := filter.EndTime.Sub(filter.StartTime)
		if duration <= 2*time.Hour {
			filter.Granularity = "15m"
		} else if duration <= 7*24*time.Hour {
			filter.Granularity = "1h"
		} else {
			filter.Granularity = "1d"
		}
	}

	switch tc.ActorOrgType {
	case "platform":
		// Platform can view any org or provider
		return nil

	case "reseller":
		// Reseller can only view its own customer tree
		filter.OrgID = &tc.ActorOrgID
		filter.ProviderID = nil
		filter.ConnectionID = nil
		return nil

	case "customer":
		// Customer can only view itself
		filter.OrgID = &tc.ActorOrgID
		filter.CustomerID = &tc.ActorOrgID
		filter.ProviderID = nil
		filter.ConnectionID = nil
		return nil

	default:
		return ErrUnauthorizedScope
	}
}

// GetExecutiveDashboard returns high-level KPI summary and trends.
func (s *Service) GetExecutiveDashboard(ctx context.Context, filter DashboardFilter, tc *tenant.Context) (*ExecutiveDashboard, error) {
	if err := s.sanitizeFilter(&filter, tc); err != nil {
		return nil, err
	}

	dash, err := s.repo.QueryExecutiveDashboard(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("executive dashboard: %w", err)
	}

	// Strip platform-only cost/margin for non-platform users
	if tc.ActorOrgType != "platform" {
		dash.CostByCur = nil
		dash.MarginByCur = nil
		for i := range dash.TimeSeries {
			dash.TimeSeries[i].Cost = nil
			dash.TimeSeries[i].Margin = nil
			dash.TimeSeries[i].MarginPercent = nil
		}
	}

	return dash, nil
}

// GetNOCDashboard returns NOC statistics. Strictly platform-only.
func (s *Service) GetNOCDashboard(ctx context.Context, filter DashboardFilter, tc *tenant.Context) (*NOCDashboard, error) {
	if tc == nil || tc.ActorOrgType != "platform" {
		return nil, ErrPlatformOnly
	}
	if err := s.sanitizeFilter(&filter, tc); err != nil {
		return nil, err
	}

	return s.repo.QueryNOCDashboard(ctx, filter)
}

// GetLiveNOC returns 2-second cached live NOC operational data for real-time dashboards.
func (s *Service) GetLiveNOC(ctx context.Context, tc *tenant.Context) (*NOCDashboard, error) {
	if tc == nil || tc.ActorOrgType != "platform" {
		return nil, ErrPlatformOnly
	}

	s.cacheMu.RLock()
	if s.liveCached != nil && time.Now().Before(s.liveExpiry) {
		cached := s.liveCached
		s.cacheMu.RUnlock()
		return cached, nil
	}
	s.cacheMu.RUnlock()

	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	// Double check under write lock
	if s.liveCached != nil && time.Now().Before(s.liveExpiry) {
		return s.liveCached, nil
	}

	filter := DashboardFilter{
		StartTime:   time.Now().UTC().Add(-1 * time.Hour),
		EndTime:     time.Now().UTC(),
		Granularity: "15m",
	}

	live, err := s.repo.QueryNOCDashboard(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("query live noc: %w", err)
	}

	s.liveCached = live
	s.liveExpiry = time.Now().Add(2 * time.Second)

	return live, nil
}

// GetResellerDashboard returns reseller-scoped analytics.
func (s *Service) GetResellerDashboard(ctx context.Context, filter DashboardFilter, tc *tenant.Context) (*ResellerDashboard, error) {
	if tc == nil || (tc.ActorOrgType != "reseller" && tc.ActorOrgType != "platform") {
		return nil, ErrUnauthorizedScope
	}
	if err := s.sanitizeFilter(&filter, tc); err != nil {
		return nil, err
	}

	return s.repo.QueryResellerDashboard(ctx, filter)
}

// GetCustomerDashboard returns customer-scoped self-service dashboard data.
func (s *Service) GetCustomerDashboard(ctx context.Context, filter DashboardFilter, tc *tenant.Context) (*CustomerDashboard, error) {
	if tc == nil {
		return nil, ErrUnauthorizedScope
	}
	if err := s.sanitizeFilter(&filter, tc); err != nil {
		return nil, err
	}

	return s.repo.QueryCustomerDashboard(ctx, filter)
}

// ListHeartbeats retrieves active service heartbeats.
func (s *Service) ListHeartbeats(ctx context.Context, tc *tenant.Context) ([]ServiceHeartbeat, error) {
	if tc == nil || tc.ActorOrgType != "platform" {
		return nil, ErrPlatformOnly
	}
	return s.repo.ListHeartbeats(ctx)
}

// RebuildRollups triggers an event replay from the requested timestamp. Platform admin only.
func (s *Service) RebuildRollups(ctx context.Context, fromTime time.Time, tc *tenant.Context) error {
	if tc == nil || tc.ActorOrgType != "platform" {
		return ErrPlatformOnly
	}
	s.logger.Warn("rebuilding analytics rollups", slog.Time("from", fromTime), slog.String("user_id", tc.ActorUserID.String()))
	return s.repo.RebuildRollups(ctx, fromTime)
}

// RecordHeartbeat logs a service heartbeat.
func (s *Service) RecordHeartbeat(ctx context.Context, hb ServiceHeartbeat) error {
	return s.repo.RecordHeartbeat(ctx, hb)
}
