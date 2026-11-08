package customer

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	MaxResellerTPS         = 500
	MaxResellerCreditLimit = 50000.0
	MaxResellerDailyCap    = int64(500000)
	MaxResellerMonthlyCap  = int64(10000000)

	OneTimeSecretWarning = "Store this secret securely now. It will never be shown again."
)

var (
	ErrProfileNotFound      = errors.New("customer commercial profile not found")
	ErrProfileAlreadyExists = errors.New("customer commercial profile already exists for this organization")
	ErrNotCustomerOrg       = errors.New("commercial profiles and customer credentials can only be attached to customer organizations")
	ErrCrossTenantAccess    = errors.New("access denied: target customer organization is outside your tenant scope")
	ErrPermissionDenied     = errors.New("permission denied")
	ErrPlatformOnlyField    = errors.New("reseller cannot modify platform-restricted commercial fields or reactivate suspended/closed profiles")
	ErrResellerCapExceeded  = errors.New("requested limit exceeds reseller ceiling")
	ErrValidation           = errors.New("validation failed")
	ErrAPIKeyNotFound       = errors.New("customer API key not found")
	ErrAPIKeyAlreadyRevoked = errors.New("customer API key is already revoked")
	ErrSMPPAccountNotFound  = errors.New("customer SMPP account not found")
	ErrSMPPSystemIDExists   = errors.New("system_id is already in use across the platform")
)

type CustomerProfile struct {
	ID                     uuid.UUID  `json:"id"`
	OrganizationID         uuid.UUID  `json:"organization_id"`
	OrganizationName       string     `json:"organization_name,omitempty"`
	ParentOrganizationID   *uuid.UUID `json:"parent_organization_id,omitempty"`
	ParentOrganizationName *string    `json:"parent_organization_name,omitempty"`
	AccountType            string     `json:"account_type"` // prepaid | postpaid
	BillingCurrency        string     `json:"billing_currency"`
	Timezone               string     `json:"timezone"`
	DefaultLanguage        string     `json:"default_language"`
	TechnicalContact       string     `json:"technical_contact"`
	BillingContact         string     `json:"billing_contact"`
	CreditLimit            float64    `json:"credit_limit"`
	DefaultMaxTPS          int        `json:"default_max_tps"`
	DailyMessageCap        int64      `json:"daily_message_cap"`
	MonthlyMessageCap      int64      `json:"monthly_message_cap"`
	AllowedCountries       []string   `json:"allowed_countries"`
	DestinationPolicy      string     `json:"destination_policy"` // allow_all | allow_listed | block_listed
	Status                 string     `json:"status"`             // draft | active | suspended | closed
	Notes                  string     `json:"notes"`
	Archived               bool       `json:"archived"`
	Version                int        `json:"version"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
	CreatedBy              *uuid.UUID `json:"created_by,omitempty"`
	UpdatedBy              *uuid.UUID `json:"updated_by,omitempty"`
}

type CustomerProfileHistory struct {
	ID          uuid.UUID      `json:"id"`
	ProfileID   uuid.UUID      `json:"profile_id"`
	Version     int            `json:"version"`
	Snapshot    map[string]any `json:"snapshot"`
	ActorUserID *uuid.UUID     `json:"actor_user_id,omitempty"`
	ChangedAt   time.Time      `json:"changed_at"`
	Reason      string         `json:"reason"`
}

type CreateProfileRequest struct {
	OrganizationID    uuid.UUID `json:"organization_id"`
	AccountType       string    `json:"account_type"`
	BillingCurrency   string    `json:"billing_currency"`
	Timezone          string    `json:"timezone"`
	DefaultLanguage   string    `json:"default_language"`
	TechnicalContact  string    `json:"technical_contact"`
	BillingContact    string    `json:"billing_contact"`
	CreditLimit       *float64  `json:"credit_limit"`
	DefaultMaxTPS     *int      `json:"default_max_tps"`
	DailyMessageCap   *int64    `json:"daily_message_cap"`
	MonthlyMessageCap *int64    `json:"monthly_message_cap"`
	AllowedCountries  []string  `json:"allowed_countries"`
	DestinationPolicy string    `json:"destination_policy"`
	Status            string    `json:"status"`
	Notes             string    `json:"notes"`
	Reason            string    `json:"reason"`
}

type UpdateProfileRequest struct {
	AccountType       *string   `json:"account_type"`
	BillingCurrency   *string   `json:"billing_currency"`
	Timezone          *string   `json:"timezone"`
	DefaultLanguage   *string   `json:"default_language"`
	TechnicalContact  *string   `json:"technical_contact"`
	BillingContact    *string   `json:"billing_contact"`
	CreditLimit       *float64  `json:"credit_limit"`
	DefaultMaxTPS     *int      `json:"default_max_tps"`
	DailyMessageCap   *int64    `json:"daily_message_cap"`
	MonthlyMessageCap *int64    `json:"monthly_message_cap"`
	AllowedCountries  *[]string `json:"allowed_countries"`
	DestinationPolicy *string   `json:"destination_policy"`
	Status            *string   `json:"status"`
	Notes             *string   `json:"notes"`
	Archived          *bool     `json:"archived"`
	Reason            string    `json:"reason"`
}

type ProfileListFilter struct {
	Search          string
	Status          string
	AccountType     string
	ParentOrgID     *uuid.UUID
	OrganizationID  *uuid.UUID
	IncludeArchived bool
	Page            int
	Limit           int
}

type ProfileWriteResult struct {
	Profile       *CustomerProfile `json:"profile,omitempty"`
	PendingChange any              `json:"pending_change,omitempty"`
}

// ============================================================================
// Customer API Keys
// ============================================================================

type CustomerAPIKey struct {
	ID             uuid.UUID  `json:"id"`
	OrganizationID uuid.UUID  `json:"organization_id"`
	Label          string     `json:"label"`
	KeyPrefix      string     `json:"key_prefix"`
	KeyHash        string     `json:"-"`
	Scopes         []string   `json:"scopes"`
	IPAllowlist    []string   `json:"ip_allowlist"`
	Status         string     `json:"status"` // active | revoked
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	CreatedBy      *uuid.UUID `json:"created_by,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
}

// IssuedAPIKeyResponse is returned ONLY at creation or rotation time.
type IssuedAPIKeyResponse struct {
	APIKey       CustomerAPIKey `json:"api_key"`
	PlaintextKey string         `json:"plaintext_key"`
	Warning      string         `json:"warning"`
}

type CreateAPIKeyRequest struct {
	OrganizationID uuid.UUID  `json:"organization_id"`
	Label          string     `json:"label"`
	Scopes         []string   `json:"scopes"`
	IPAllowlist    []string   `json:"ip_allowlist"`
	ExpiresAt      *time.Time `json:"expires_at"`
	Reason         string     `json:"reason"`
}

// ============================================================================
// Customer SMPP Accounts
// ============================================================================

type CustomerSMPPAccount struct {
	ID                uuid.UUID  `json:"id"`
	OrganizationID    uuid.UUID  `json:"organization_id"`
	SystemID          string     `json:"system_id"`
	PasswordHash      string     `json:"-"`
	PasswordChangedAt time.Time  `json:"password_changed_at"`
	AllowedIPs        []string   `json:"allowed_ips"`
	AllowedBindTypes  []string   `json:"allowed_bind_types"`
	MaxBinds          int        `json:"max_binds"`
	MaxTPS            int        `json:"max_tps"`
	Status            string     `json:"status"` // active | disabled | suspended
	CreatedBy         *uuid.UUID `json:"created_by,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// IssuedSMPPAccountResponse is returned ONLY when an SMPP account is created or its password is reset.
type IssuedSMPPAccountResponse struct {
	Account           CustomerSMPPAccount `json:"account"`
	PlaintextPassword string              `json:"plaintext_password"`
	Warning           string              `json:"warning"`
}

type CreateSMPPAccountRequest struct {
	OrganizationID   uuid.UUID `json:"organization_id"`
	SystemID         string    `json:"system_id"`
	AllowedIPs       []string  `json:"allowed_ips"`
	AllowedBindTypes []string  `json:"allowed_bind_types"`
	MaxBinds         *int      `json:"max_binds"`
	MaxTPS           *int      `json:"max_tps"`
	Status           string    `json:"status"`
	Reason           string    `json:"reason"`
}

type UpdateSMPPAccountRequest struct {
	AllowedIPs       *[]string `json:"allowed_ips"`
	AllowedBindTypes *[]string `json:"allowed_bind_types"`
	MaxBinds         *int      `json:"max_binds"`
	MaxTPS           *int      `json:"max_tps"`
	Status           *string   `json:"status"`
	Reason           string    `json:"reason"`
}
