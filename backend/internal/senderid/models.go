package senderid

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrSenderIDNotFound              = errors.New("sender ID not found")
	ErrSenderIDExists                = errors.New("sender ID already exists for this organization and country")
	ErrValidation                    = errors.New("sender ID validation failed")
	ErrCrossTenantAccess             = errors.New("access denied: target customer organization is outside your tenant scope")
	ErrPermissionDenied              = errors.New("permission denied")
	ErrNotCustomerOrg                = errors.New("sender IDs can only be registered for customer organizations")
	ErrMakerCannotApproveOwnSenderID = errors.New("submitter cannot approve their own sender ID request")
	ErrRejectReasonRequired          = errors.New("rejection reason is required when rejecting a sender ID")
)

type SenderID struct {
	ID                         uuid.UUID  `json:"id"`
	OrganizationID             uuid.UUID  `json:"organization_id"`
	OrganizationName           string     `json:"organization_name,omitempty"`
	SenderID                   string     `json:"sender_id"`
	SenderType                 string     `json:"sender_type"` // alphanumeric | numeric | shortcode
	CountryID                  *uuid.UUID `json:"country_id,omitempty"`
	CountryISO2                *string    `json:"country_iso2,omitempty"`
	CountryName                *string    `json:"country_name,omitempty"`
	CountrySenderIDPolicyNotes *string    `json:"country_sender_id_policy_notes,omitempty"`
	Status                     string     `json:"status"` // pending | approved | rejected | suspended
	ApprovalNotes              string     `json:"approval_notes"`
	EffectiveFrom              *time.Time `json:"effective_from,omitempty"`
	EffectiveTo                *time.Time `json:"effective_to,omitempty"`
	DataSource                 string     `json:"data_source"`
	Archived                   bool       `json:"archived"`
	Version                    int        `json:"version"`
	CreatedAt                  time.Time  `json:"created_at"`
	UpdatedAt                  time.Time  `json:"updated_at"`
	CreatedBy                  *uuid.UUID `json:"created_by,omitempty"`
	UpdatedBy                  *uuid.UUID `json:"updated_by,omitempty"`
}

type SenderIDHistory struct {
	ID          uuid.UUID      `json:"id"`
	SenderIDRef uuid.UUID      `json:"sender_id_ref"`
	Version     int            `json:"version"`
	Snapshot    map[string]any `json:"snapshot"`
	ActorUserID *uuid.UUID     `json:"actor_user_id,omitempty"`
	ChangedAt   time.Time      `json:"changed_at"`
	Reason      string         `json:"reason"`
}

type CreateSenderIDRequest struct {
	OrganizationID uuid.UUID  `json:"organization_id"`
	SenderID       string     `json:"sender_id"`
	SenderType     string     `json:"sender_type"`
	CountryID      *uuid.UUID `json:"country_id"`
	Status         string     `json:"status,omitempty"`
	ApprovalNotes  string     `json:"approval_notes,omitempty"`
	EffectiveFrom  *time.Time `json:"effective_from,omitempty"`
	EffectiveTo    *time.Time `json:"effective_to,omitempty"`
	DataSource     string     `json:"data_source,omitempty"`
	Reason         string     `json:"reason,omitempty"`
}

type UpdateSenderIDRequest struct {
	ApprovalNotes *string    `json:"approval_notes"`
	EffectiveFrom *time.Time `json:"effective_from"`
	EffectiveTo   *time.Time `json:"effective_to"`
	DataSource    *string    `json:"data_source"`
	Status        *string    `json:"status"`
	Archived      *bool      `json:"archived"`
	Reason        string     `json:"reason"`
}

type ReviewSenderIDRequest struct {
	Notes  string `json:"notes"`
	Reason string `json:"reason"`
}

type ListFilter struct {
	OrganizationID  *uuid.UUID
	ParentOrgID     *uuid.UUID
	CountryID       *uuid.UUID
	SenderType      string
	Status          string
	Search          string
	IncludeArchived bool
	Page            int
	Limit           int
}
