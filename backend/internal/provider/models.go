package provider

import (
	"context"
	"errors"
	"strings"
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
	HTTPAuthType             string               `json:"http_auth_type"` // none | basic | bearer | header_token | api_key_header | query_token | api_key_query
	HTTPCredentials          secrets.SecretStatus `json:"http_credentials"`
	HTTPCredentialsEncrypted string               `json:"-"`
	HTTPCredentialsChangedAt *time.Time           `json:"-"`
	HTTPFormatNotes          string               `json:"http_format_notes"`
	HTTPDLRCallbackNotes     string               `json:"http_dlr_callback_notes"`
	HTTPConfig               HTTPConnectorConfig  `json:"http_config"`
	HTTPDLRMode              string               `json:"http_dlr_mode"` // none | callback | polling
	HTTPDLRSecret            secrets.SecretStatus `json:"http_dlr_secret"`
	HTTPDLRSecretEncrypted   string               `json:"-"`
	HTTPDLRSecretChangedAt   *time.Time           `json:"-"`
	HTTPDLRCallbackPath      string               `json:"http_dlr_callback_path,omitempty"`
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
	Name                 string               `json:"name"`
	Protocol             string               `json:"protocol"`
	RoleNotes            string               `json:"role_notes"`
	Status               string               `json:"status"`
	MaxTPS               int                  `json:"max_tps"`
	SMPPHost             string               `json:"smpp_host"`
	SMPPPort             *int                 `json:"smpp_port"`
	SMPPSystemID         string               `json:"smpp_system_id"`
	SMPPPassword         *string              `json:"smpp_password,omitempty"`
	SMPPSystemType       string               `json:"smpp_system_type"`
	SMPPBindType         string               `json:"smpp_bind_type"`
	SMPPTLS              *bool                `json:"smpp_tls"`
	SMPPWindowSize       *int                 `json:"smpp_window_size"`
	SMPPEnquireLinkSec   *int                 `json:"smpp_enquire_link_sec"`
	SMPPSourceTON        *int                 `json:"smpp_source_ton"`
	SMPPSourceNPI        *int                 `json:"smpp_source_npi"`
	SMPPDestTON          *int                 `json:"smpp_dest_ton"`
	SMPPDestNPI          *int                 `json:"smpp_dest_npi"`
	SMPPCharset          string               `json:"smpp_charset"`
	IPAllowlistNotes     string               `json:"ip_allowlist_notes"`
	HTTPBaseURL          string               `json:"http_base_url"`
	HTTPAuthType         string               `json:"http_auth_type"`
	HTTPCredentials      *string              `json:"http_credentials,omitempty"`
	HTTPFormatNotes      string               `json:"http_format_notes"`
	HTTPDLRCallbackNotes string               `json:"http_dlr_callback_notes"`
	HTTPConfig           *HTTPConnectorConfig `json:"http_config,omitempty"`
	HTTPDLRMode          string               `json:"http_dlr_mode,omitempty"`
	HTTPDLRSecret        *string              `json:"http_dlr_secret,omitempty"`
	Reason               string               `json:"reason"`
}

type UpdateConnectionRequest struct {
	Name                 *string              `json:"name"`
	Protocol             *string              `json:"protocol"`
	RoleNotes            *string              `json:"role_notes"`
	Status               *string              `json:"status"`
	Archived             *bool                `json:"archived"`
	MaxTPS               *int                 `json:"max_tps"`
	SMPPHost             *string              `json:"smpp_host"`
	SMPPPort             *int                 `json:"smpp_port"`
	SMPPSystemID         *string              `json:"smpp_system_id"`
	SMPPSystemType       *string              `json:"smpp_system_type"`
	SMPPBindType         *string              `json:"smpp_bind_type"`
	SMPPTLS              *bool                `json:"smpp_tls"`
	SMPPWindowSize       *int                 `json:"smpp_window_size"`
	SMPPEnquireLinkSec   *int                 `json:"smpp_enquire_link_sec"`
	SMPPSourceTON        *int                 `json:"smpp_source_ton"`
	SMPPSourceNPI        *int                 `json:"smpp_source_npi"`
	SMPPDestTON          *int                 `json:"smpp_dest_ton"`
	SMPPDestNPI          *int                 `json:"smpp_dest_npi"`
	SMPPCharset          *string              `json:"smpp_charset"`
	IPAllowlistNotes     *string              `json:"ip_allowlist_notes"`
	HTTPBaseURL          *string              `json:"http_base_url"`
	HTTPAuthType         *string              `json:"http_auth_type"`
	HTTPFormatNotes      *string              `json:"http_format_notes"`
	HTTPDLRCallbackNotes *string              `json:"http_dlr_callback_notes"`
	HTTPConfig           *HTTPConnectorConfig `json:"http_config,omitempty"`
	HTTPDLRMode          *string              `json:"http_dlr_mode,omitempty"`
	Reason               string               `json:"reason"`
}

type UpdateConnectionSecretRequest struct {
	SMPPPassword    *string `json:"smpp_password,omitempty"`
	HTTPCredentials *string `json:"http_credentials,omitempty"`
	HTTPDLRSecret   *string `json:"http_dlr_secret,omitempty"`
	Reason          string  `json:"reason"`
}

// HTTPConnectorConfig holds template, auth header/query naming, response parsing,
// and DLR (callback or polling) rules for an HTTP provider connection.
type HTTPConnectorConfig struct {
	Method           string            `json:"method,omitempty"`
	URLTemplate      string            `json:"url_template,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
	AuthHeaderName   string            `json:"auth_header_name,omitempty"`
	AuthQueryParam   string            `json:"auth_query_param,omitempty"`
	BodyFormat       string            `json:"body_format,omitempty"` // json | form | none
	BodyTemplate     string            `json:"body_template,omitempty"`
	RequestTimeoutMs int               `json:"request_timeout_ms,omitempty"`
	MaxResponseBytes int               `json:"max_response_bytes,omitempty"`

	SuccessHTTPMin      int      `json:"success_http_min,omitempty"`
	SuccessHTTPMax      int      `json:"success_http_max,omitempty"`
	SuccessJSONPath     string   `json:"success_json_path,omitempty"`
	SuccessEquals       string   `json:"success_equals,omitempty"`
	ProviderMsgIDPath   string   `json:"provider_msg_id_path,omitempty"`
	ProviderMsgIDRegex  string   `json:"provider_msg_id_regex,omitempty"`
	ErrorCodePath       string   `json:"error_code_path,omitempty"`
	ErrorMsgPath        string   `json:"error_msg_path,omitempty"`
	PermanentHTTPCodes  []int    `json:"permanent_http_codes,omitempty"`
	PermanentErrorCodes []string `json:"permanent_error_codes,omitempty"`
	ThrottleHTTPCodes   []int    `json:"throttle_http_codes,omitempty"`
	ThrottleErrorCodes  []string `json:"throttle_error_codes,omitempty"`

	CallbackSignatureHeader string            `json:"callback_signature_header,omitempty"`
	CallbackSignatureAlgo   string            `json:"callback_signature_algo,omitempty"` // hmac_sha256 | shared_secret | none
	CallbackMsgIDPath       string            `json:"callback_msg_id_path,omitempty"`
	CallbackStatusPath      string            `json:"callback_status_path,omitempty"`
	CallbackErrorCodePath   string            `json:"callback_error_code_path,omitempty"`
	CallbackStatusMap       map[string]string `json:"callback_status_map,omitempty"`

	PollMethod        string            `json:"poll_method,omitempty"` // GET | POST
	PollURLTemplate   string            `json:"poll_url_template,omitempty"`
	PollIntervalSec   int               `json:"poll_interval_sec,omitempty"`
	PollStatusPath    string            `json:"poll_status_path,omitempty"`
	PollErrorCodePath string            `json:"poll_error_code_path,omitempty"`
	PollStatusMap     map[string]string `json:"poll_status_map,omitempty"`
}

// Normalized returns a copy of HTTPConnectorConfig with safe defaults populated.
func (c HTTPConnectorConfig) Normalized() HTTPConnectorConfig {
	out := c
	if out.Method == "" {
		out.Method = "POST"
	} else {
		out.Method = strings.ToUpper(strings.TrimSpace(out.Method))
	}
	if out.BodyFormat == "" {
		if out.Method == "GET" {
			out.BodyFormat = "none"
		} else {
			out.BodyFormat = "json"
		}
	}
	if out.BodyTemplate == "" {
		switch out.BodyFormat {
		case "json":
			out.BodyTemplate = `{"to":"{{destination}}","from":"{{sender}}","text":"{{text}}","message_id":"{{message_id}}","encoding":"{{encoding}}"}`
		case "form":
			out.BodyTemplate = `to={{destination}}&from={{sender}}&text={{text}}&message_id={{message_id}}&encoding={{encoding}}`
		}
	}
	if out.RequestTimeoutMs <= 0 {
		out.RequestTimeoutMs = 8000
	}
	if out.MaxResponseBytes <= 0 {
		out.MaxResponseBytes = 65536
	}
	if out.SuccessHTTPMin <= 0 {
		out.SuccessHTTPMin = 200
	}
	if out.SuccessHTTPMax <= 0 {
		out.SuccessHTTPMax = 299
	}
	if out.ProviderMsgIDPath == "" && out.ProviderMsgIDRegex == "" {
		out.ProviderMsgIDPath = "message_id"
	}
	if out.ErrorCodePath == "" {
		out.ErrorCodePath = "error"
	}
	if out.ErrorMsgPath == "" {
		out.ErrorMsgPath = "message"
	}
	if len(out.PermanentHTTPCodes) == 0 {
		out.PermanentHTTPCodes = []int{400, 401, 403, 404, 422}
	}
	if len(out.ThrottleHTTPCodes) == 0 {
		out.ThrottleHTTPCodes = []int{429}
	}
	if out.CallbackSignatureHeader == "" {
		out.CallbackSignatureHeader = "X-Provider-Signature"
	}
	if out.CallbackSignatureAlgo == "" {
		out.CallbackSignatureAlgo = "hmac_sha256"
	}
	if out.CallbackMsgIDPath == "" {
		out.CallbackMsgIDPath = "message_id"
	}
	if out.CallbackStatusPath == "" {
		out.CallbackStatusPath = "status"
	}
	if out.CallbackErrorCodePath == "" {
		out.CallbackErrorCodePath = "error_code"
	}
	if out.PollMethod == "" {
		out.PollMethod = "GET"
	} else {
		out.PollMethod = strings.ToUpper(strings.TrimSpace(out.PollMethod))
	}
	if out.PollIntervalSec <= 0 {
		out.PollIntervalSec = 30
	}
	if out.PollStatusPath == "" {
		out.PollStatusPath = "status"
	}
	if out.PollErrorCodePath == "" {
		out.PollErrorCodePath = "error_code"
	}
	return out
}

// ProviderDLRCallbackPath returns the canonical inbound webhook callback path for an HTTP connection.
func ProviderDLRCallbackPath(connID uuid.UUID) string {
	return "/api/v1/provider-callbacks/dlr/" + connID.String()
}

type TestSendRequest struct {
	Destination  string `json:"destination"`
	SenderID     string `json:"sender_id"`
	Text         string `json:"text"`
	Encoding     string `json:"encoding"`
	SimulateMode string `json:"simulate_mode,omitempty"` // "" | "accept" | "reject" | "throttle" | "server_err" (development only)
}

type TestSendResult struct {
	Success           bool   `json:"success"`
	Status            string `json:"status"`
	HTTPStatusCode    int    `json:"http_status_code,omitempty"`
	ProviderMessageID string `json:"provider_message_id,omitempty"`
	ErrorCode         string `json:"error_code,omitempty"`
	ErrorMessage      string `json:"error_message,omitempty"`
	LatencyMs         int64  `json:"latency_ms"`
	Method            string `json:"method"`
	RedactedURL       string `json:"redacted_url"`
	RedactedBody      string `json:"redacted_body,omitempty"`
	ResponseSnippet   string `json:"response_snippet,omitempty"`
	Simulated         bool   `json:"simulated"`
}

// HTTPTestSender executes a test send against a connection's HTTP template (or dev simulation).
type HTTPTestSender interface {
	TestConnection(ctx context.Context, conn *ProviderConnection, req TestSendRequest) (*TestSendResult, error)
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
