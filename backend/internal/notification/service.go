package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

type Service struct {
	repo          *Repository
	emailSender   EmailSender
	webhookSender WebhookSender
	suppressor    *StormSuppressor
	logger        *slog.Logger
}

func NewService(repo *Repository, emailSender EmailSender, webhookSender WebhookSender, suppressor *StormSuppressor, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if suppressor == nil {
		suppressor = NewStormSuppressor(5*time.Minute, 1*time.Minute, 50)
	}
	return &Service{
		repo:          repo,
		emailSender:   emailSender,
		webhookSender: webhookSender,
		suppressor:    suppressor,
		logger:        logger.With(slog.String("component", "notification_service")),
	}
}

// Dispatch processes an incoming notification event with storm protection and multi-channel routing.
func (s *Service) Dispatch(ctx context.Context, ev DispatchEvent) error {
	if ev.DedupKey == "" {
		ev.DedupKey = fmt.Sprintf("%s:%s:%s", ev.Category, ev.Severity, ev.Title)
	}

	allowed, reason := s.suppressor.CheckAndRecord(ev.UserID, ev.DedupKey)
	if !allowed {
		s.logger.Info("notification suppressed",
			slog.String("reason", reason),
			slog.String("dedup_key", ev.DedupKey),
			slog.String("title", ev.Title),
		)
		return nil
	}

	notif := &Notification{
		ID:             uuid.New(),
		OrganizationID: ev.OrganizationID,
		UserID:         ev.UserID,
		Category:       ev.Category,
		Severity:       ev.Severity,
		Title:          ev.Title,
		Body:           ev.Body,
		LinkURL:        ev.LinkURL,
		CreatedAt:      time.Now().UTC(),
	}

	if err := s.repo.Create(ctx, notif); err != nil {
		return fmt.Errorf("create in-app notification: %w", err)
	}

	if ev.UserID != nil && *ev.UserID != uuid.Nil {
		s.dispatchChannels(ctx, *ev.UserID, notif, ev.EventType)
	}

	return nil
}

func (s *Service) dispatchChannels(ctx context.Context, userID uuid.UUID, notif *Notification, eventType string) {
	pref, err := s.repo.GetPreferences(ctx, userID)
	if err != nil {
		s.logger.Warn("failed to load preferences for notification channels", slog.String("user_id", userID.String()), slog.String("error", err.Error()))
		return
	}

	catPref, ok := pref.Categories[notif.Category]
	if !ok {
		catPref = CategoryPreference{Email: true, InApp: true, Webhook: true}
	}

	// 1. Email dispatch
	if pref.EmailEnabled && catPref.Email && s.emailSender != nil {
		email, err := s.repo.GetUserEmail(ctx, userID)
		if err == nil && email != "" {
			subject := fmt.Sprintf("[Znero %s] %s", strings.ToUpper(notif.Severity), notif.Title)
			htmlBody := fmt.Sprintf("<h2>%s</h2><p>%s</p>", notif.Title, notif.Body)
			if err := s.emailSender.SendEmail(ctx, email, subject, htmlBody, notif.Body); err != nil {
				s.logger.Warn("failed to dispatch notification email",
					slog.String("to", email),
					slog.String("error", err.Error()),
				)
			}
		}
	}

	// 2. Webhook dispatch
	if pref.WebhookEnabled && catPref.Webhook && pref.WebhookURL != nil && *pref.WebhookURL != "" && s.webhookSender != nil {
		payload, err := json.Marshal(map[string]interface{}{
			"notification_id": notif.ID,
			"event_type":      eventType,
			"category":        notif.Category,
			"severity":        notif.Severity,
			"title":           notif.Title,
			"body":            notif.Body,
			"link_url":        notif.LinkURL,
			"timestamp":       notif.CreatedAt.Format(time.RFC3339),
		})
		if err == nil {
			if err := s.webhookSender.SendWebhook(ctx, *pref.WebhookURL, "whsec_default", eventType, payload); err != nil {
				s.logger.Warn("failed to dispatch notification webhook",
					slog.String("url", *pref.WebhookURL),
					slog.String("error", err.Error()),
				)
			}
		}
	}
}

// List retrieves paginated notifications scoped to the tenant context.
func (s *Service) List(ctx context.Context, tc *tenant.Context, filter ListFilter) ([]*Notification, int64, error) {
	return s.repo.List(ctx, tc.ActorUserID, tc.ActorOrgID, tc.IsPlatformAdmin, filter)
}

// GetUnreadCount returns the count of unread notifications for the caller.
func (s *Service) GetUnreadCount(ctx context.Context, tc *tenant.Context) (int64, error) {
	return s.repo.GetUnreadCount(ctx, tc.ActorUserID, tc.ActorOrgID, tc.IsPlatformAdmin)
}

// MarkAsRead marks a single notification as read.
func (s *Service) MarkAsRead(ctx context.Context, tc *tenant.Context, id uuid.UUID) error {
	return s.repo.MarkAsRead(ctx, id, tc.ActorUserID, tc.ActorOrgID, tc.IsPlatformAdmin)
}

// MarkAllAsRead marks all unread notifications as read.
func (s *Service) MarkAllAsRead(ctx context.Context, tc *tenant.Context) error {
	return s.repo.MarkAllAsRead(ctx, tc.ActorUserID, tc.ActorOrgID, tc.IsPlatformAdmin)
}

// Delete removes a notification.
func (s *Service) Delete(ctx context.Context, tc *tenant.Context, id uuid.UUID) error {
	return s.repo.Delete(ctx, id, tc.ActorUserID, tc.ActorOrgID, tc.IsPlatformAdmin)
}

// GetPreferences returns notification preferences for the caller.
func (s *Service) GetPreferences(ctx context.Context, tc *tenant.Context) (*NotificationPreferences, error) {
	return s.repo.GetPreferences(ctx, tc.ActorUserID)
}

// UpdatePreferences updates notification preferences for the caller.
func (s *Service) UpdatePreferences(ctx context.Context, tc *tenant.Context, req UpdatePreferencesRequest) (*NotificationPreferences, error) {
	pref, err := s.repo.GetPreferences(ctx, tc.ActorUserID)
	if err != nil {
		return nil, err
	}

	if req.EmailEnabled != nil {
		pref.EmailEnabled = *req.EmailEnabled
	}
	if req.WebhookEnabled != nil {
		pref.WebhookEnabled = *req.WebhookEnabled
	}
	if req.WebhookURL != nil {
		pref.WebhookURL = req.WebhookURL
	}
	if req.Categories != nil {
		pref.Categories = req.Categories
	}

	if err := s.repo.UpsertPreferences(ctx, pref); err != nil {
		return nil, err
	}
	return pref, nil
}

// TestNotification triggers a test notification across active channels for the caller.
func (s *Service) TestNotification(ctx context.Context, tc *tenant.Context) error {
	testTitle := "Test Notification from Znero System"
	testBody := fmt.Sprintf("This is a test notification generated at %s to verify multi-channel delivery.", time.Now().UTC().Format(time.RFC850))
	link := "/settings/notifications"

	ev := DispatchEvent{
		OrganizationID: &tc.ActorOrgID,
		UserID:         &tc.ActorUserID,
		Category:       CategoryOperations,
		Severity:       SeverityInfo,
		Title:          testTitle,
		Body:           testBody,
		LinkURL:        &link,
		EventType:      "test_notification",
		DedupKey:       fmt.Sprintf("test:%s:%d", tc.ActorUserID, time.Now().UnixNano()),
	}
	return s.Dispatch(ctx, ev)
}
