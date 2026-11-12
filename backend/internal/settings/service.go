package settings

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

type Service struct {
	repo     *Repository
	auditSvc *audit.Service
	logger   *slog.Logger
}

func NewService(repo *Repository, auditSvc *audit.Service, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		repo:     repo,
		auditSvc: auditSvc,
		logger:   logger.With(slog.String("component", "settings_service")),
	}
}

// ListSettings returns all settings in the registry, with secrets securely masked.
func (s *Service) ListSettings(ctx context.Context, tc *tenant.Context, category string) ([]*Setting, error) {
	items, err := s.repo.ListSettings(ctx, category)
	if err != nil {
		return nil, err
	}

	masked := make([]*Setting, len(items))
	for i, item := range items {
		masked[i] = MaskSecret(item)
	}
	return masked, nil
}

// GetSetting returns a single setting by key, with secrets securely masked.
func (s *Service) GetSetting(ctx context.Context, tc *tenant.Context, key string) (*Setting, error) {
	item, err := s.repo.GetSetting(ctx, key)
	if err != nil {
		return nil, err
	}
	return MaskSecret(item), nil
}

// UpdateSetting validates and stores an updated setting value.
func (s *Service) UpdateSetting(ctx context.Context, tc *tenant.Context, key string, rawValue any) (*Setting, error) {
	if !tc.IsPlatformAdmin {
		return nil, ErrUnauthorizedTenant
	}

	current, err := s.repo.GetSetting(ctx, key)
	if err != nil {
		return nil, err
	}

	coerced, err := ValidateAndCoerce(rawValue, current.ValueType)
	if err != nil {
		return nil, err
	}

	current.Value = coerced
	current.UpdatedByUserID = &tc.ActorUserID
	current.UpdatedAt = time.Now().UTC()

	if err := s.repo.UpsertSetting(ctx, current); err != nil {
		return nil, fmt.Errorf("upsert setting: %w", err)
	}

	s.logger.Info("system setting updated",
		slog.String("key", key),
		slog.String("updated_by", tc.ActorUserID.String()),
	)

	return MaskSecret(current), nil
}

// ResetSetting resets a setting to factory default.
func (s *Service) ResetSetting(ctx context.Context, tc *tenant.Context, key string) error {
	if !tc.IsPlatformAdmin {
		return ErrUnauthorizedTenant
	}
	return s.repo.ResetSetting(ctx, key)
}

// ==================== Typed Runtime Accessors ====================

func (s *Service) GetTypedString(ctx context.Context, key, fallback string) string {
	item, err := s.repo.GetSetting(ctx, key)
	if err != nil {
		return fallback
	}
	str, ok := item.Value.(string)
	if !ok {
		return fallback
	}
	return str
}

func (s *Service) GetTypedInt(ctx context.Context, key string, fallback int) int {
	item, err := s.repo.GetSetting(ctx, key)
	if err != nil {
		return fallback
	}
	switch v := item.Value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return fallback
	}
}

func (s *Service) GetTypedBool(ctx context.Context, key string, fallback bool) bool {
	item, err := s.repo.GetSetting(ctx, key)
	if err != nil {
		return fallback
	}
	b, ok := item.Value.(bool)
	if !ok {
		return fallback
	}
	return b
}
