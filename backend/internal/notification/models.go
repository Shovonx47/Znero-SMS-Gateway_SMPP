package notification

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Categories
const (
	CategorySecurity   = "security"
	CategoryBilling    = "billing"
	CategoryOperations = "operations"
	CategorySystem     = "system"
)

// Severities
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Event types
const (
	EventBalanceLow             = "balance_low"
	EventCreditLimitApproaching = "credit_limit_approaching"
	EventInvoiceGenerated       = "invoice_generated"
	EventPaymentReceived        = "payment_received"
	EventRateChangeScheduled    = "rate_change_scheduled"
	EventAlertFiring            = "alert_firing"
	EventIncidentOpened         = "incident_opened"
	EventIncidentResolved       = "incident_resolved"
	EventSecurityLoginNewIP     = "security_login_new_ip"
	EventSecurityMFAChanged     = "security_mfa_changed"
	EventExportReady            = "export_ready"
	EventSystemAnnouncement     = "system_announcement"
)

var (
	ErrNotificationNotFound = errors.New("notification not found")
	ErrUnauthorizedScope    = errors.New("unauthorized notification scope")
	ErrInvalidPreferences   = errors.New("invalid notification preferences")
)

type Notification struct {
	ID             uuid.UUID  `json:"id"`
	OrganizationID *uuid.UUID `json:"organization_id,omitempty"`
	UserID         *uuid.UUID `json:"user_id,omitempty"`
	Category       string     `json:"category"`
	Severity       string     `json:"severity"`
	Title          string     `json:"title"`
	Body           string     `json:"body"`
	LinkURL        *string    `json:"link_url,omitempty"`
	ReadAt         *time.Time `json:"read_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

type CategoryPreference struct {
	Email   bool `json:"email"`
	InApp   bool `json:"in_app"`
	Webhook bool `json:"webhook"`
}

type NotificationPreferences struct {
	ID             uuid.UUID                     `json:"id"`
	UserID         uuid.UUID                     `json:"user_id"`
	EmailEnabled   bool                          `json:"email_enabled"`
	WebhookEnabled bool                          `json:"webhook_enabled"`
	WebhookURL     *string                       `json:"webhook_url,omitempty"`
	Categories     map[string]CategoryPreference `json:"categories"`
	CreatedAt      time.Time                     `json:"created_at"`
	UpdatedAt      time.Time                     `json:"updated_at"`
}

func DefaultPreferences(userID uuid.UUID) *NotificationPreferences {
	return &NotificationPreferences{
		ID:             uuid.New(),
		UserID:         userID,
		EmailEnabled:   true,
		WebhookEnabled: false,
		Categories: map[string]CategoryPreference{
			CategorySecurity:   {Email: true, InApp: true, Webhook: true},
			CategoryBilling:    {Email: true, InApp: true, Webhook: true},
			CategoryOperations: {Email: true, InApp: true, Webhook: true},
			CategorySystem:     {Email: false, InApp: true, Webhook: false},
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
}

type ListFilter struct {
	Category string
	Severity string
	IsRead   *bool
	Limit    int
	Offset   int
}

type DispatchEvent struct {
	OrganizationID *uuid.UUID
	UserID         *uuid.UUID
	Category       string
	Severity       string
	Title          string
	Body           string
	LinkURL        *string
	EventType      string
	DedupKey       string
}

type UpdatePreferencesRequest struct {
	EmailEnabled   *bool                         `json:"email_enabled,omitempty"`
	WebhookEnabled *bool                         `json:"webhook_enabled,omitempty"`
	WebhookURL     *string                       `json:"webhook_url,omitempty"`
	Categories     map[string]CategoryPreference `json:"categories,omitempty"`
}
