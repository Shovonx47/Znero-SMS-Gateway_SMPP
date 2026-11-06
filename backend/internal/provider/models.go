package provider

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/secrets"
)

var (
	ErrProviderNotFound        = errors.New("provider not found")
	ErrProviderCodeExists      = errors.New("provider with this code already exists")
	ErrConnectionNotFound      = errors.New("provider connection not found")
	ErrConnectionNameExists    = errors.New("connection with this name already exists for this provider")
	ErrCoverageNotFound        = errors.New("provider coverage not found")
	ErrCoverageExists          = errors.New("coverage already exists for this provider, country, and operator")
	ErrValidation              = errors.New("validation failed")
	ErrPlatformOnlyProvider    = errors.New("providers, connections, and coverage are platform-only resources")
	ErrPermissionDenied        = errors.New("permission denied")
	ErrOperatorCountryMismatch = errors.New("selected operator does not belong to the specified country")
)

type Provider struct {
	ID                uuid.UUID  `json:"id"`
	Name              string     `json:"name"`
	Code              string     `json:"code"`
	ProviderType      string     `json:"provider_type"` // smpp | http | other
	LegalEntityName   string     `json:"legal_entity_name"`
	ContactTechnical  string     `json:"contact_technical"`
	ContactCommercial string     `json:"contact_commercial"`
	ContactNOC        string     `json:"contact_noc"`
	CountryID         *uuid.UUID `json:"country_id,omitempty"`
	CountryISO2       *string    `json:"country_iso2,omitempty"`
	CountryName       *string    `json:"country_name,omitempty"`
	Timezone          string     `json:"timezone"`
	CurrencyCode      string     `json:"currency_code"`
	Notes             string     `json:"notes"`
	Status            string     `json:"status"` // draft | active | suspended | inactive
	Archived          bool       `json:"archived"`
	Version           int        `json:"version"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	CreatedBy         *uuid.UUID `json:"created_by,omitempty"`
	UpdatedBy         *uuid.UUID `json:"updated_by,omitempty"`
}

type ProviderHistory struct {
	ID          uuid.UUID      `json:"id"`
	ProviderID  uuid.UUID      `json:"provider_id"`
	Version     int            `json:"version"`
	Snapshot    map[string]any `json:"snapshot"`
	ActorUserID *uuid.UUID     `json:"actor_user_id,omitempty"`
	ChangedAt   time.Time      `json:"changed_at"`
	Reason      string         `json:"reason"`
}

// ProviderConnection stores configuration only; encrypted secret columns are never serialized to JSON.
type ProviderConnection struct {
	ID                       uuid.UUID            `json:"id"`
	ProviderID               uuid.UUID            `json:"provider_id"`
	Name                     string               `json:"name"`
	Protocol                 string               `json:"protocol"` // smpp | http
	RoleNotes                string               `json:"role_notes"`
	Status                   string               `json:"status"` // draft | active | disabled
	Archived                 bool                 `json:"archived"`
	MaxTPS                   int                  `json:"max_tps"`
	SMPPHost                 string               `json:"smpp_host"`
	SMPPPort                 int                  `json:"smpp_port"`
	SMPPSystemID             string               `json:"smpp_system_id"`
	SMPPPassword             secrets.SecretStatus `json:"smpp_password"`
	SMPPPasswordEncrypted    string               `json:"-"`
	SMPPPasswordChangedAt    *time.Time           `json:"-"`
	SMPPSystemType           string               `json:"smpp_system_type"`
	SMPPBindType             string               `json:"smpp_bind_type"` // transceiver | transmitter | receiver
	SMPPTLS                  bool                 `json:"smpp_tls"`
	SMPPWindowSize           int                  `json:"smpp_window_size"`
	SMPPEnquireLinkSec       int                  `json:"smpp_enquire_link_sec"`
	SMPPSourceTON            int                  `json:"smpp_source_ton"`
	SMPPSourceNPI            int                  `json:"smpp_source_npi"`
	SMPPDestTON              int                  `json:"smpp_dest_ton"`
	SMPPDestNPI              int                  `json:"smpp_dest_npi"`
	SMPPCharset              string               `json:"smpp_charset"`
	IPAllowlistNotes         string               `json:"ip_allowlist_notes"`
	HTTPBaseURL              string               `json:"http_base_url"`
	HTTPAuthType             string               `json:"http_auth_type"` // none | basic | header_token | query_token
	HTTPCredentials          secrets.SecretStatus `json:"http_credentials"`
	HTTPCredentialsEncrypted string               `json:"-"`
	HTTPCredentialsChangedAt *time.Time           `json:"-"`
	HTTPFormatNotes          string               `json:"http_format_notes"`
	HTTPDLRCallbackNotes     string               `json:"http_dlr_callback_notes"`
	Version                  int                  `json:"version"`
	CreatedAt                time.Time            `json:"created_at"`
	UpdatedAt                time.Time            `json:"updated_at"`
	CreatedBy                *uuid.UUID           `json:"created_by,omitempty"`
	UpdatedBy                *uuid.UUID           `json:"updated_by,omitempty"`
}

type ProviderConnectionHistory struct {
	ID           uuid.UUID      `json:"id"`
	ConnectionID uuid.UUID      `json:"connection_id"`
	Version      int            `json:"version"`
	Snapshot     map[string]any `json:"snapshot"`
	ActorUserID  *uuid.UUID     `json:"actor_user_id,omitempty"`
	ChangedAt    time.Time      `json:"changed_at"`
	Reason       string         `json:"reason"`
}

type ProviderCoverage struct {
	ID            uuid.UUID  `json:"id"`
	ProviderID    uuid.UUID  `json:"provider_id"`
	ProviderCode  string     `json:"provider_code,omitempty"`
	ProviderName  string     `json:"provider_name,omitempty"`
	CountryID     uuid.UUID  `json:"country_id"`
	CountryISO2   string     `json:"country_iso2,omitempty"`
	CountryName   string     `json:"country_name,omitempty"`
	OperatorID    *uuid.UUID `json:"operator_id,omitempty"`
	OperatorName  *string    `json:"operator_name,omitempty"`
	OperatorMCC   *string    `json:"operator_mcc,omitempty"`
	OperatorMNC   *string    `json:"operator_mnc,omitempty"`
	Status        string     `json:"status"` // declared | tested | active | suspended
	Notes         string     `json:"notes"`
	EffectiveFrom *time.Time `json:"effective_from,omitempty"`
	EffectiveTo   *time.Time `json:"effective_to,omitempty"`
	DataSource    string     `json:"data_source"`
	Archived      bool       `json:"archived"`
	Version       int        `json:"version"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	CreatedBy     *uuid.UUID `json:"created_by,omitempty"`
	UpdatedBy     *uuid.UUID `json:"updated_by,omitempty"`
}

type ProviderCoverageHistory struct {
	ID          uuid.UUID      `json:"id"`
	CoverageID  uuid.UUID      `json:"coverage_id"`
	Version     int            `json:"version"`
	Snapshot    map[string]any `json:"snapshot"`
	ActorUserID *uuid.UUID     `json:"actor_user_id,omitempty"`
	ChangedAt   time.Time      `json:"changed_at"`
	Reason      string         `json:"reason"`
}

type CreateProviderRequest struct {
	Name              string     `json:"name"`
	Code              string     `json:"code"`
	ProviderType      string     `json:"provider_type"`
	LegalEntityName   string     `json:"legal_entity_name"`
	ContactTechnical  string     `json:"contact_technical"`
	ContactCommercial string     `json:"contact_commercial"`
	ContactNOC        string     `json:"contact_noc"`
	CountryID         *uuid.UUID `json:"country_id"`
	Timezone          string     `json:"timezone"`
	CurrencyCode      string     `json:"currency_code"`
	Notes             string     `json:"notes"`
	Status            string     `json:"status"`
	Reason            string     `json:"reason"`
}

type UpdateProviderRequest struct {
	Name              *string    `json:"name"`
	ProviderType      *string    `json:"provider_type"`
	LegalEntityName   *string    `json:"legal_entity_name"`
	ContactTechnical  *string    `json:"contact_technical"`
	ContactCommercial *string    `json:"contact_commercial"`
	ContactNOC        *string    `json:"contact_noc"`
	CountryID         *uuid.UUID `json:"country_id"`
	Timezone          *string    `json:"timezone"`
	CurrencyCode      *string    `json:"currency_code"`
	Notes             *string    `json:"notes"`
	Status            *string    `json:"status"`
	Archived          *bool      `json:"archived"`
	Reason            string     `json:"reason"`
}

type ProviderListFilter struct {
	Search          string
	ProviderType    string
	CountryID       *uuid.UUID
	Status          string
	IncludeArchived bool
	SortBy          string
	SortDir         string
	Page            int
	Limit           int
}

type CreateConnectionRequest struct {
	Name                 string  `json:"name"`
	Protocol             string  `json:"protocol"`
	RoleNotes            string  `json:"role_notes"`
	Status               string  `json:"status"`
	MaxTPS               int     `json:"max_tps"`
	SMPPHost             string  `json:"smpp_host"`
	SMPPPort             *int    `json:"smpp_port"`
	SMPPSystemID         string  `json:"smpp_system_id"`
	SMPPPassword         *string `json:"smpp_password,omitempty"`
	SMPPSystemType       string  `json:"smpp_system_type"`
	SMPPBindType         string  `json:"smpp_bind_type"`
	SMPPTLS              *bool   `json:"smpp_tls"`
	SMPPWindowSize       *int    `json:"smpp_window_size"`
	SMPPEnquireLinkSec   *int    `json:"smpp_enquire_link_sec"`
	SMPPSourceTON        *int    `json:"smpp_source_ton"`
	SMPPSourceNPI        *int    `json:"smpp_source_npi"`
	SMPPDestTON          *int    `json:"smpp_dest_ton"`
	SMPPDestNPI          *int    `json:"smpp_dest_npi"`
	SMPPCharset          string  `json:"smpp_charset"`
	IPAllowlistNotes     string  `json:"ip_allowlist_notes"`
	HTTPBaseURL          string  `json:"http_base_url"`
	HTTPAuthType         string  `json:"http_auth_type"`
	HTTPCredentials      *string `json:"http_credentials,omitempty"`
	HTTPFormatNotes      string  `json:"http_format_notes"`
	HTTPDLRCallbackNotes string  `json:"http_dlr_callback_notes"`
	Reason               string  `json:"reason"`
}

type UpdateConnectionRequest struct {
	Name                 *string `json:"name"`
	Protocol             *string `json:"protocol"`
	RoleNotes            *string `json:"role_notes"`
	Status               *string `json:"status"`
	Archived             *bool   `json:"archived"`
	MaxTPS               *int    `json:"max_tps"`
	SMPPHost             *string `json:"smpp_host"`
	SMPPPort             *int    `json:"smpp_port"`
	SMPPSystemID         *string `json:"smpp_system_id"`
	SMPPSystemType       *string `json:"smpp_system_type"`
	SMPPBindType         *string `json:"smpp_bind_type"`
	SMPPTLS              *bool   `json:"smpp_tls"`
	SMPPWindowSize       *int    `json:"smpp_window_size"`
	SMPPEnquireLinkSec   *int    `json:"smpp_enquire_link_sec"`
	SMPPSourceTON        *int    `json:"smpp_source_ton"`
	SMPPSourceNPI        *int    `json:"smpp_source_npi"`
	SMPPDestTON          *int    `json:"smpp_dest_ton"`
	SMPPDestNPI          *int    `json:"smpp_dest_npi"`
	SMPPCharset          *string `json:"smpp_charset"`
	IPAllowlistNotes     *string `json:"ip_allowlist_notes"`
	HTTPBaseURL          *string `json:"http_base_url"`
	HTTPAuthType         *string `json:"http_auth_type"`
	HTTPFormatNotes      *string `json:"http_format_notes"`
	HTTPDLRCallbackNotes *string `json:"http_dlr_callback_notes"`
	Reason               string  `json:"reason"`
}

type UpdateConnectionSecretRequest struct {
	SMPPPassword    *string `json:"smpp_password,omitempty"`
	HTTPCredentials *string `json:"http_credentials,omitempty"`
	Reason          string  `json:"reason"`
}

type UpdateConnectionStatusRequest struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type CreateCoverageRequest struct {
	ProviderID    uuid.UUID  `json:"provider_id"`
	CountryID     uuid.UUID  `json:"country_id"`
	OperatorID    *uuid.UUID `json:"operator_id"`
	Status        string     `json:"status"`
	Notes         string     `json:"notes"`
	EffectiveFrom *time.Time `json:"effective_from"`
	EffectiveTo   *time.Time `json:"effective_to"`
	DataSource    string     `json:"data_source"`
	Reason        string     `json:"reason"`
}

type UpdateCoverageRequest struct {
	Status        *string    `json:"status"`
	Notes         *string    `json:"notes"`
	EffectiveFrom *time.Time `json:"effective_from"`
	EffectiveTo   *time.Time `json:"effective_to"`
	DataSource    *string    `json:"data_source"`
	Archived      *bool      `json:"archived"`
	Reason        string     `json:"reason"`
}

type CoverageListFilter struct {
	ProviderID      *uuid.UUID
	CountryID       *uuid.UUID
	OperatorID      *uuid.UUID
	Status          string
	Search          string
	IncludeArchived bool
	Page            int
	Limit           int
}

type WriteResult struct {
	Provider      *Provider           `json:"provider,omitempty"`
	Connection    *ProviderConnection `json:"connection,omitempty"`
	Coverage      *ProviderCoverage   `json:"coverage,omitempty"`
	PendingChange any                 `json:"pending_change,omitempty"`
}
