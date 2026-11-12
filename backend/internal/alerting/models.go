package alerting

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Severities
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
	SeverityMajor    = "major"
	SeverityMinor    = "minor"
)

// Alert statuses
const (
	AlertStatusFiring   = "firing"
	AlertStatusResolved = "resolved"
	AlertStatusSilenced = "silenced"
)

// Incident statuses
const (
	IncidentStatusOpen          = "open"
	IncidentStatusInvestigating = "investigating"
	IncidentStatusIdentified    = "identified"
	IncidentStatusMonitoring    = "monitoring"
	IncidentStatusResolved      = "resolved"
)

// Condition Types
const (
	ConditionHighFailureRate     = "delivery_failure_rate"
	ConditionOutboxBacklogAge    = "outbox_oldest_age_sec"
	ConditionReconciliationDisc  = "ledger_discrepancy_count"
	ConditionCarrierConnDown     = "provider_conn_health"
	ConditionSettlementException = "settlement_error_count"
	ConditionLowMargin           = "margin_percentage"
)

var (
	ErrRuleNotFound       = errors.New("alert rule not found")
	ErrAlertNotFound      = errors.New("alert not found")
	ErrIncidentNotFound   = errors.New("incident not found")
	ErrInvalidTransition  = errors.New("invalid incident state transition")
	ErrUnauthorizedTenant = errors.New("alerting and incident management is platform restricted")
)

type AlertRule struct {
	ID              uuid.UUID              `json:"id"`
	Name            string                 `json:"name"`
	ConditionType   string                 `json:"condition_type"`
	Threshold       float64                `json:"threshold"`
	DurationSeconds int                    `json:"duration_seconds"`
	Severity        string                 `json:"severity"`
	Scope           string                 `json:"scope"`
	Enabled         bool                   `json:"enabled"`
	Params          map[string]interface{} `json:"params"`
	CreatedAt       time.Time              `json:"created_at"`
	UpdatedAt       time.Time              `json:"updated_at"`
}

type Alert struct {
	ID          uuid.UUID              `json:"id"`
	RuleID      *uuid.UUID             `json:"rule_id,omitempty"`
	Status      string                 `json:"status"`
	Severity    string                 `json:"severity"`
	Summary     string                 `json:"summary"`
	Description string                 `json:"description"`
	Labels      map[string]interface{} `json:"labels"`
	StartedAt   time.Time              `json:"started_at"`
	ResolvedAt  *time.Time             `json:"resolved_at,omitempty"`
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
}

type Incident struct {
	ID         uuid.UUID  `json:"id"`
	Title      string     `json:"title"`
	Severity   string     `json:"severity"`
	Status     string     `json:"status"`
	LeadUserID *uuid.UUID `json:"lead_user_id,omitempty"`
	RootCause  *string    `json:"root_cause,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

type IncidentTimeline struct {
	ID           uuid.UUID  `json:"id"`
	IncidentID   uuid.UUID  `json:"incident_id"`
	AuthorUserID *uuid.UUID `json:"author_user_id,omitempty"`
	EntryType    string     `json:"entry_type"`
	Content      string     `json:"content"`
	CreatedAt    time.Time  `json:"created_at"`
}

type IncidentDetail struct {
	Incident
	Timeline []*IncidentTimeline `json:"timeline"`
}

type CreateRuleRequest struct {
	Name            string                 `json:"name"`
	ConditionType   string                 `json:"condition_type"`
	Threshold       float64                `json:"threshold"`
	DurationSeconds int                    `json:"duration_seconds"`
	Severity        string                 `json:"severity"`
	Scope           string                 `json:"scope"`
	Enabled         bool                   `json:"enabled"`
	Params          map[string]interface{} `json:"params"`
}

type DeclareIncidentRequest struct {
	Title       string     `json:"title"`
	Severity    string     `json:"severity"`
	LeadUserID  *uuid.UUID `json:"lead_user_id,omitempty"`
	InitialNote string     `json:"initial_note,omitempty"`
}

type TransitionIncidentRequest struct {
	Status    string  `json:"status"`
	Note      string  `json:"note"`
	RootCause *string `json:"root_cause,omitempty"`
}

type AddTimelineRequest struct {
	EntryType string `json:"entry_type"`
	Content   string `json:"content"`
}

type SilenceAlertRequest struct {
	DurationSeconds int    `json:"duration_seconds"`
	Reason          string `json:"reason"`
}

// ValidateIncidentTransition validates state machine moves.
func ValidateIncidentTransition(currentStatus, newStatus string) error {
	if currentStatus == newStatus {
		return nil
	}

	validTransitions := map[string]map[string]bool{
		IncidentStatusOpen: {
			IncidentStatusInvestigating: true,
		},
		IncidentStatusInvestigating: {
			IncidentStatusIdentified: true,
			IncidentStatusMonitoring: true,
		},
		IncidentStatusIdentified: {
			IncidentStatusMonitoring:    true,
			IncidentStatusInvestigating: true,
		},
		IncidentStatusMonitoring: {
			IncidentStatusResolved:      true,
			IncidentStatusInvestigating: true,
		},
		IncidentStatusResolved: {
			IncidentStatusInvestigating: true, // Reopen
		},
	}

	allowed, found := validTransitions[currentStatus]
	if !found || !allowed[newStatus] {
		return fmt.Errorf("%w: cannot transition from %q to %q", ErrInvalidTransition, currentStatus, newStatus)
	}
	return nil
}
