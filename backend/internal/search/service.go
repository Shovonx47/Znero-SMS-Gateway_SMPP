package search

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

type Service struct {
	repo   *Repository
	logger *slog.Logger
}

func NewService(repo *Repository, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		repo:   repo,
		logger: logger.With(slog.String("component", "search_service")),
	}
}

// Search coordinates multi-entity universal search scoped to the caller's tenancy.
func (s *Service) Search(ctx context.Context, tc *tenant.Context, query string) (*SearchResponse, error) {
	q := strings.TrimSpace(query)
	if len(q) < 2 {
		return &SearchResponse{
			Query:   q,
			Total:   0,
			Results: []*SearchResultItem{},
		}, nil
	}

	items, err := s.repo.Search(ctx, tc, q, 25)
	if err != nil {
		return nil, err
	}

	return &SearchResponse{
		Query:   q,
		Total:   len(items),
		Results: items,
	}, nil
}

// ==================== Saved Filters ====================

func (s *Service) ListSavedFilters(ctx context.Context, tc *tenant.Context, viewName string) ([]*SavedFilter, error) {
	return s.repo.ListSavedFilters(ctx, tc.ActorUserID, viewName)
}

func (s *Service) CreateSavedFilter(ctx context.Context, tc *tenant.Context, req CreateSavedFilterRequest) (*SavedFilter, error) {
	sf := &SavedFilter{
		ID:        uuid.New(),
		UserID:    tc.ActorUserID,
		ViewName:  req.ViewName,
		Name:      req.Name,
		Query:     req.Query,
		IsDefault: req.IsDefault,
		CreatedAt: time.Now().UTC(),
	}

	if err := s.repo.CreateSavedFilter(ctx, sf); err != nil {
		return nil, err
	}
	return sf, nil
}

func (s *Service) DeleteSavedFilter(ctx context.Context, tc *tenant.Context, id uuid.UUID) error {
	return s.repo.DeleteSavedFilter(ctx, id, tc.ActorUserID)
}

func (s *Service) SetDefaultFilter(ctx context.Context, tc *tenant.Context, id uuid.UUID) error {
	return s.repo.SetDefaultFilter(ctx, id, tc.ActorUserID)
}

// ==================== User Preferences ====================

func (s *Service) GetUserPreferences(ctx context.Context, tc *tenant.Context) (*UserPreference, error) {
	return s.repo.GetUserPreferences(ctx, tc.ActorUserID)
}

func (s *Service) UpdateUserPreferences(ctx context.Context, tc *tenant.Context, req UpdatePreferenceRequest) (*UserPreference, error) {
	pref, err := s.repo.GetUserPreferences(ctx, tc.ActorUserID)
	if err != nil {
		return nil, err
	}

	if req.Theme != nil {
		t := strings.ToLower(strings.TrimSpace(*req.Theme))
		if t != "system" && t != "light" && t != "dark" {
			return nil, ErrInvalidTheme
		}
		pref.Theme = t
	}

	if req.Locale != nil {
		l := strings.ToLower(strings.TrimSpace(*req.Locale))
		if l != "en" && l != "bn" {
			return nil, ErrInvalidLocale
		}
		pref.Locale = l
	}

	if req.Timezone != nil {
		pref.Timezone = strings.TrimSpace(*req.Timezone)
	}

	if req.DashboardLayout != nil {
		pref.DashboardLayout = *req.DashboardLayout
	}

	if err := s.repo.UpsertUserPreferences(ctx, pref); err != nil {
		return nil, err
	}
	return pref, nil
}

