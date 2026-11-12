package alerting

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/notification"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

type Service struct {
	repo      *Repository
	evaluator *Evaluator
	notifSvc  *notification.Service
	logger    *slog.Logger
}

func NewService(repo *Repository, evaluator *Evaluator, notifSvc *notification.Service, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		repo:      repo,
		evaluator: evaluator,
		notifSvc:  notifSvc,
		logger:    logger.With(slog.String("component", "alerting_service")),
	}
}

// EvaluateRules evaluates all active alert rules and transitions alert lifecycles.
func (s *Service) EvaluateRules(ctx context.Context) ([]*Alert, error) {
	rules, err := s.repo.ListActiveRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active rules: %w", err)
	}

	var updatedAlerts []*Alert

	for _, rule := range rules {
		breached, val, err := s.evaluator.EvaluateRule(ctx, rule)
		if err != nil {
			s.logger.Warn("failed to evaluate alert rule",
				slog.String("rule_name", rule.Name),
				slog.String("error", err.Error()),
			)
			continue
		}

		activeAlert, err := s.repo.GetActiveAlertByRule(ctx, rule.ID)
		if err != nil {
			s.logger.Warn("failed to query active alert for rule",
				slog.String("rule_id", rule.ID.String()),
				slog.String("error", err.Error()),
			)
			continue
		}

		if breached {
			if activeAlert == nil {
				// Transition to FIRING
				newAlert := &Alert{
					ID:          uuid.New(),
					RuleID:      &rule.ID,
					Status:      AlertStatusFiring,
					Severity:    rule.Severity,
					Summary:     fmt.Sprintf("Alert Firing: %s", rule.Name),
					Description: fmt.Sprintf("Rule %q breached threshold: current value %.2f (threshold %.2f)", rule.Name, val, rule.Threshold),
					Labels: map[string]interface{}{
						"condition_type": rule.ConditionType,
						"measured_value": val,
					},
					StartedAt: time.Now().UTC(),
				}

				if err := s.repo.CreateAlert(ctx, newAlert); err != nil {
					s.logger.Error("failed to create firing alert", slog.String("error", err.Error()))
					continue
				}

				updatedAlerts = append(updatedAlerts, newAlert)

				// Dispatch notification if notification service available
				if s.notifSvc != nil {
					ev := notification.DispatchEvent{
						Category:  notification.CategoryOperations,
						Severity:  rule.Severity,
						Title:     newAlert.Summary,
						Body:      newAlert.Description,
						EventType: notification.EventAlertFiring,
						DedupKey:  fmt.Sprintf("alert:%s", rule.ID),
					}
					_ = s.notifSvc.Dispatch(ctx, ev)
				}
			}
		} else {
			if activeAlert != nil && activeAlert.Status != AlertStatusResolved {
				// Condition cleared -> Transition to RESOLVED
				if err := s.repo.ResolveAlert(ctx, activeAlert.ID); err != nil {
					s.logger.Error("failed to resolve alert", slog.String("error", err.Error()))
					continue
				}

				activeAlert.Status = AlertStatusResolved
				now := time.Now().UTC()
				activeAlert.ResolvedAt = &now
				updatedAlerts = append(updatedAlerts, activeAlert)

				if s.notifSvc != nil {
					ev := notification.DispatchEvent{
						Category:  notification.CategoryOperations,
						Severity:  notification.SeverityInfo,
						Title:     fmt.Sprintf("Alert Resolved: %s", rule.Name),
						Body:      fmt.Sprintf("Condition %q has cleared (current value %.2f)", rule.Name, val),
						EventType: "alert_resolved",
						DedupKey:  fmt.Sprintf("resolve:%s", rule.ID),
					}
					_ = s.notifSvc.Dispatch(ctx, ev)
				}
			}
		}
	}

	return updatedAlerts, nil
}

// SilenceAlert mutes notifications for a firing alert.
func (s *Service) SilenceAlert(ctx context.Context, tc *tenant.Context, alertID uuid.UUID, duration time.Duration, reason string) error {
	if !tc.IsPlatformAdmin {
		return ErrUnauthorizedTenant
	}
	if duration <= 0 {
		duration = 1 * time.Hour
	}
	silenceUntil := time.Now().UTC().Add(duration)
	return s.repo.SilenceAlert(ctx, alertID, silenceUntil, reason)
}

// ==================== Incident Operations ====================

func (s *Service) DeclareIncident(ctx context.Context, tc *tenant.Context, req DeclareIncidentRequest) (*Incident, error) {
	if !tc.IsPlatformAdmin {
		return nil, ErrUnauthorizedTenant
	}

	if req.Severity == "" {
		req.Severity = SeverityMajor
	}

	inc := &Incident{
		ID:         uuid.New(),
		Title:      req.Title,
		Severity:   req.Severity,
		Status:     IncidentStatusOpen,
		LeadUserID: req.LeadUserID,
		StartedAt:  time.Now().UTC(),
	}

	if err := s.repo.CreateIncident(ctx, inc); err != nil {
		return nil, err
	}

	// Add timeline creation entry
	_ = s.repo.AddTimelineEntry(ctx, &IncidentTimeline{
		IncidentID:   inc.ID,
		AuthorUserID: &tc.ActorUserID,
		EntryType:    "state_change",
		Content:      fmt.Sprintf("Incident declared with severity %s", req.Severity),
		CreatedAt:    time.Now().UTC(),
	})

	if req.InitialNote != "" {
		_ = s.repo.AddTimelineEntry(ctx, &IncidentTimeline{
			IncidentID:   inc.ID,
			AuthorUserID: &tc.ActorUserID,
			EntryType:    "note",
			Content:      req.InitialNote,
			CreatedAt:    time.Now().UTC(),
		})
	}

	if s.notifSvc != nil {
		ev := notification.DispatchEvent{
			Category:  notification.CategoryOperations,
			Severity:  notification.SeverityCritical,
			Title:     fmt.Sprintf("[INCIDENT DECLARED] %s", inc.Title),
			Body:      fmt.Sprintf("Severity: %s, Lead: %v", inc.Severity, inc.LeadUserID),
			EventType: notification.EventIncidentOpened,
			DedupKey:  fmt.Sprintf("incident:%s", inc.ID),
		}
		_ = s.notifSvc.Dispatch(ctx, ev)
	}

	return inc, nil
}

func (s *Service) TransitionIncident(ctx context.Context, tc *tenant.Context, id uuid.UUID, newStatus string, note string, rootCause *string) error {
	if !tc.IsPlatformAdmin {
		return ErrUnauthorizedTenant
	}

	inc, err := s.repo.GetIncident(ctx, id)
	if err != nil {
		return err
	}

	if err := ValidateIncidentTransition(inc.Status, newStatus); err != nil {
		return err
	}

	var resolvedAt *time.Time
	if newStatus == IncidentStatusResolved {
		now := time.Now().UTC()
		resolvedAt = &now
	}

	if err := s.repo.UpdateIncidentStatus(ctx, id, newStatus, rootCause, resolvedAt); err != nil {
		return err
	}

	// Timeline entry
	content := fmt.Sprintf("Status changed from %s to %s", inc.Status, newStatus)
	if note != "" {
		content += fmt.Sprintf(": %s", note)
	}
	_ = s.repo.AddTimelineEntry(ctx, &IncidentTimeline{
		IncidentID:   id,
		AuthorUserID: &tc.ActorUserID,
		EntryType:    "state_change",
		Content:      content,
		CreatedAt:    time.Now().UTC(),
	})

	if newStatus == IncidentStatusResolved && s.notifSvc != nil {
		ev := notification.DispatchEvent{
			Category:  notification.CategoryOperations,
			Severity:  notification.SeverityInfo,
			Title:     fmt.Sprintf("[INCIDENT RESOLVED] %s", inc.Title),
			Body:      fmt.Sprintf("Incident %q has been marked resolved.", inc.Title),
			EventType: notification.EventIncidentResolved,
			DedupKey:  fmt.Sprintf("incident_res:%s", inc.ID),
		}
		_ = s.notifSvc.Dispatch(ctx, ev)
	}

	return nil
}

func (s *Service) AddTimelineEntry(ctx context.Context, tc *tenant.Context, incidentID uuid.UUID, entryType, content string) error {
	if !tc.IsPlatformAdmin {
		return ErrUnauthorizedTenant
	}

	// Ensure incident exists
	if _, err := s.repo.GetIncident(ctx, incidentID); err != nil {
		return err
	}

	if entryType == "" {
		entryType = "note"
	}

	return s.repo.AddTimelineEntry(ctx, &IncidentTimeline{
		IncidentID:   incidentID,
		AuthorUserID: &tc.ActorUserID,
		EntryType:    entryType,
		Content:      content,
		CreatedAt:    time.Now().UTC(),
	})
}

func (s *Service) GetIncident(ctx context.Context, tc *tenant.Context, id uuid.UUID) (*IncidentDetail, error) {
	if !tc.IsPlatformAdmin {
		return nil, ErrUnauthorizedTenant
	}

	inc, err := s.repo.GetIncident(ctx, id)
	if err != nil {
		return nil, err
	}

	timeline, err := s.repo.GetIncidentTimeline(ctx, id)
	if err != nil {
		return nil, err
	}

	return &IncidentDetail{
		Incident: *inc,
		Timeline: timeline,
	}, nil
}

func (s *Service) ListIncidents(ctx context.Context, tc *tenant.Context, status, severity string, limit, offset int) ([]*Incident, int64, error) {
	if !tc.IsPlatformAdmin {
		return nil, 0, ErrUnauthorizedTenant
	}
	return s.repo.ListIncidents(ctx, status, severity, limit, offset)
}

// ==================== Alert and Rule Management ====================

func (s *Service) ListAlerts(ctx context.Context, tc *tenant.Context, status string) ([]*Alert, error) {
	if !tc.IsPlatformAdmin {
		return nil, ErrUnauthorizedTenant
	}
	return s.repo.ListAlerts(ctx, status)
}

func (s *Service) ListRules(ctx context.Context, tc *tenant.Context) ([]*AlertRule, error) {
	if !tc.IsPlatformAdmin {
		return nil, ErrUnauthorizedTenant
	}
	return s.repo.ListRules(ctx)
}

func (s *Service) CreateRule(ctx context.Context, tc *tenant.Context, req CreateRuleRequest) (*AlertRule, error) {
	if !tc.IsPlatformAdmin {
		return nil, ErrUnauthorizedTenant
	}
	if req.Scope == "" {
		req.Scope = "platform"
	}
	if req.Severity == "" {
		req.Severity = SeverityWarning
	}
	if req.DurationSeconds <= 0 {
		req.DurationSeconds = 60
	}

	rule := &AlertRule{
		ID:              uuid.New(),
		Name:            req.Name,
		ConditionType:   req.ConditionType,
		Threshold:       req.Threshold,
		DurationSeconds: req.DurationSeconds,
		Severity:        req.Severity,
		Scope:           req.Scope,
		Enabled:         req.Enabled,
		Params:          req.Params,
	}

	if err := s.repo.CreateRule(ctx, rule); err != nil {
		return nil, err
	}
	return rule, nil
}

func (s *Service) UpdateRule(ctx context.Context, tc *tenant.Context, id uuid.UUID, req CreateRuleRequest) (*AlertRule, error) {
	if !tc.IsPlatformAdmin {
		return nil, ErrUnauthorizedTenant
	}
	rule, err := s.repo.GetRule(ctx, id)
	if err != nil {
		return nil, err
	}

	rule.Name = req.Name
	rule.ConditionType = req.ConditionType
	rule.Threshold = req.Threshold
	rule.DurationSeconds = req.DurationSeconds
	rule.Severity = req.Severity
	rule.Scope = req.Scope
	rule.Enabled = req.Enabled
	rule.Params = req.Params

	if err := s.repo.UpdateRule(ctx, rule); err != nil {
		return nil, err
	}
	return rule, nil
}

func (s *Service) DeleteRule(ctx context.Context, tc *tenant.Context, id uuid.UUID) error {
	if !tc.IsPlatformAdmin {
		return ErrUnauthorizedTenant
	}
	return s.repo.DeleteRule(ctx, id)
}
