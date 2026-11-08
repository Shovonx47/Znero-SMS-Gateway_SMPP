package rating

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

var (
	ErrRateCardNotFound         = errors.New("rate card not found")
	ErrRateNotFound             = errors.New("rate not found")
	ErrAssignmentNotFound       = errors.New("rate assignment not found")
	ErrCrossTenantAccess        = errors.New("access denied: rate card is outside your organization scope")
	ErrPermissionDenied         = errors.New("permission denied")
	ErrPlatformOnlyResource     = errors.New("provider buy cards, buy rates, and margin metrics are accessible to platform users only")
	ErrResellerCardRestriction  = errors.New("resellers may only manage sell rate cards for their own customers")
	ErrInvalidCurrency          = errors.New("currency code is invalid or not registered in country master data")
	ErrInvalidStatus            = errors.New("invalid status value")
	ErrInvalidMessageType       = errors.New("invalid message type")
	ErrNegativePrice            = errors.New("rate price cannot be negative")
	ErrInvalidDateRange         = errors.New("effective_to must be after effective_from")
	ErrEffectiveDateOverlap     = errors.New("rate effective dates overlap with an existing rate for this destination")
	ErrMarginViolation          = errors.New("selling price is below cost (margin violation)")
	ErrResellerCostFloor        = errors.New("sell price cannot be lower than the reseller assigned cost rate")
	ErrBulkLookupLimitExceeded  = errors.New("bulk lookup request exceeds maximum limit of 1000 items")
	ErrRateCardArchived         = errors.New("cannot add or modify rates on an archived rate card")
	ErrUnknownCountryOrOperator = errors.New("referenced country or operator does not exist")
	ErrRateChangeAboveThreshold = errors.New("rate price change exceeds the allowed threshold")
)

// TruncateMoney truncates a decimal to 6 decimal places, never rounding up sell rates.
func TruncateMoney(d decimal.Decimal) decimal.Decimal {
	return d.Truncate(6)
}

type RateCard struct {
	ID           uuid.UUID  `json:"id"`
	Name         string     `json:"name"`
	Kind         string     `json:"kind"` // provider_buy | customer_sell
	OwnerOrgID   uuid.UUID  `json:"owner_org_id"`
	OwnerOrgName string     `json:"owner_org_name,omitempty"`
	ProviderID   *uuid.UUID `json:"provider_id,omitempty"`
	ProviderName *string    `json:"provider_name,omitempty"`
	CurrencyCode string     `json:"currency_code"`
	Channel      string     `json:"channel"` // sms
	Status       string     `json:"status"`  // draft | active | inactive | archived
	Description  string     `json:"description"`
	Archived     bool       `json:"archived"`
	Version      int        `json:"version"`
	CreatedBy    *uuid.UUID `json:"created_by,omitempty"`
	UpdatedBy    *uuid.UUID `json:"updated_by,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type RateCardListFilter struct {
	Kind         string
	OwnerOrgID   *uuid.UUID
	ProviderID   *uuid.UUID
	CurrencyCode string
	Status       string
	Search       string
	Page         int
	PageSize     int
	SortBy       string
	SortOrder    string
}

type Rate struct {
	ID             uuid.UUID       `json:"id"`
	CardID         uuid.UUID       `json:"card_id"`
	CountryID      uuid.UUID       `json:"country_id"`
	CountryISO2    string          `json:"country_iso2,omitempty"`
	CountryName    string          `json:"country_name,omitempty"`
	OperatorID     *uuid.UUID      `json:"operator_id,omitempty"`
	OperatorName   *string         `json:"operator_name,omitempty"`
	OperatorMCC    *string         `json:"operator_mcc,omitempty"`
	OperatorMNC    *string         `json:"operator_mnc,omitempty"`
	MessageType    string          `json:"message_type"` // default | transactional | promotional | otp
	Price          decimal.Decimal `json:"price"`
	MinBillingNote string          `json:"min_billing_note"`
	EffectiveFrom  time.Time       `json:"effective_from"`
	EffectiveTo    *time.Time      `json:"effective_to,omitempty"`
	Source         string          `json:"source"` // manual | import
	Status         string          `json:"status"` // active | inactive | superseded
	Version        int             `json:"version"`
	CreatedBy      *uuid.UUID      `json:"created_by,omitempty"`
	UpdatedBy      *uuid.UUID      `json:"updated_by,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`

	// Field-level protected metrics (platform only; stripped for reseller/customer)
	LowestBuyCost *decimal.Decimal `json:"lowest_buy_cost,omitempty"`
	MarginAmount  *decimal.Decimal `json:"margin_amount,omitempty"`
	MarginPercent *decimal.Decimal `json:"margin_percent,omitempty"`
}

type RateListFilter struct {
	CardID      uuid.UUID
	CountryID   *uuid.UUID
	OperatorID  *uuid.UUID
	MessageType string
	Status      string
	EffectiveAt *time.Time
	Search      string
	Page        int
	PageSize    int
}

type RateCardHistory struct {
	ID           uuid.UUID       `json:"id"`
	CardID       uuid.UUID       `json:"card_id"`
	Name         string          `json:"name"`
	Kind         string          `json:"kind"`
	OwnerOrgID   uuid.UUID       `json:"owner_org_id"`
	ProviderID   *uuid.UUID      `json:"provider_id,omitempty"`
	CurrencyCode string          `json:"currency_code"`
	Channel      string          `json:"channel"`
	Status       string          `json:"status"`
	Description  string          `json:"description"`
	Archived     bool            `json:"archived"`
	Version      int             `json:"version"`
	Snapshot     json.RawMessage `json:"snapshot"`
	ChangedBy    *uuid.UUID      `json:"changed_by,omitempty"`
	ChangedAt    time.Time       `json:"changed_at"`
	ChangeReason string          `json:"change_reason"`
}

type RateHistory struct {
	ID             uuid.UUID       `json:"id"`
	RateID         uuid.UUID       `json:"rate_id"`
	CardID         uuid.UUID       `json:"card_id"`
	CountryID      uuid.UUID       `json:"country_id"`
	OperatorID     *uuid.UUID      `json:"operator_id,omitempty"`
	MessageType    string          `json:"message_type"`
	Price          decimal.Decimal `json:"price"`
	MinBillingNote string          `json:"min_billing_note"`
	EffectiveFrom  time.Time       `json:"effective_from"`
	EffectiveTo    *time.Time      `json:"effective_to,omitempty"`
	Source         string          `json:"source"`
	Status         string          `json:"status"`
	Version        int             `json:"version"`
	Snapshot       json.RawMessage `json:"snapshot"`
	ChangedBy      *uuid.UUID      `json:"changed_by,omitempty"`
	ChangedAt      time.Time       `json:"changed_at"`
	ChangeReason   string          `json:"change_reason"`
}

type CustomerRateAssignment struct {
	ID              uuid.UUID  `json:"id"`
	CustomerOrgID   uuid.UUID  `json:"customer_org_id"`
	CustomerOrgName string     `json:"customer_org_name,omitempty"`
	CardID          uuid.UUID  `json:"card_id"`
	CardName        string     `json:"card_name,omitempty"`
	EffectiveFrom   time.Time  `json:"effective_from"`
	EffectiveTo     *time.Time `json:"effective_to,omitempty"`
	Status          string     `json:"status"`
	Version         int        `json:"version"`
	AssignedBy      *uuid.UUID `json:"assigned_by,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type CustomerRateAssignmentHistory struct {
	ID            uuid.UUID       `json:"id"`
	AssignmentID  uuid.UUID       `json:"assignment_id"`
	CustomerOrgID uuid.UUID       `json:"customer_org_id"`
	CardID        uuid.UUID       `json:"card_id"`
	EffectiveFrom time.Time       `json:"effective_from"`
	EffectiveTo   *time.Time      `json:"effective_to,omitempty"`
	Status        string          `json:"status"`
	Version       int             `json:"version"`
	Snapshot      json.RawMessage `json:"snapshot"`
	ChangedBy     *uuid.UUID      `json:"changed_by,omitempty"`
	ChangedAt     time.Time       `json:"changed_at"`
	ChangeReason  string          `json:"change_reason"`
}

type ResellerCostAssignment struct {
	ID              uuid.UUID  `json:"id"`
	ResellerOrgID   uuid.UUID  `json:"reseller_org_id"`
	ResellerOrgName string     `json:"reseller_org_name,omitempty"`
	CardID          uuid.UUID  `json:"card_id"`
	CardName        string     `json:"card_name,omitempty"`
	EffectiveFrom   time.Time  `json:"effective_from"`
	EffectiveTo     *time.Time `json:"effective_to,omitempty"`
	Status          string     `json:"status"`
	Version         int        `json:"version"`
	AssignedBy      *uuid.UUID `json:"assigned_by,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type ResellerCostAssignmentHistory struct {
	ID            uuid.UUID       `json:"id"`
	AssignmentID  uuid.UUID       `json:"assignment_id"`
	ResellerOrgID uuid.UUID       `json:"reseller_org_id"`
	CardID        uuid.UUID       `json:"card_id"`
	EffectiveFrom time.Time       `json:"effective_from"`
	EffectiveTo   *time.Time      `json:"effective_to,omitempty"`
	Status        string          `json:"status"`
	Version       int             `json:"version"`
	Snapshot      json.RawMessage `json:"snapshot"`
	ChangedBy     *uuid.UUID      `json:"changed_by,omitempty"`
	ChangedAt     time.Time       `json:"changed_at"`
	ChangeReason  string          `json:"change_reason"`
}

type RateMatch struct {
	Found         bool            `json:"found"`
	RateCardID    uuid.UUID       `json:"rate_card_id,omitempty"`
	RateCardName  string          `json:"rate_card_name,omitempty"`
	RateID        uuid.UUID       `json:"rate_id,omitempty"`
	CountryID     uuid.UUID       `json:"country_id,omitempty"`
	CountryISO2   string          `json:"country_iso2,omitempty"`
	CountryName   string          `json:"country_name,omitempty"`
	OperatorID    *uuid.UUID      `json:"operator_id,omitempty"`
	OperatorName  string          `json:"operator_name,omitempty"`
	MessageType   string          `json:"message_type,omitempty"`
	Price         decimal.Decimal `json:"price"`
	Currency      string          `json:"currency,omitempty"`
	EffectiveFrom time.Time       `json:"effective_from,omitempty"`
	EffectiveTo   *time.Time      `json:"effective_to,omitempty"`
	MatchedRule   string          `json:"matched_rule,omitempty"` // operator_exact | country_default
	E164          string          `json:"e164,omitempty"`
}

type MarginResult struct {
	Comparable      bool             `json:"comparable"`
	Status          string           `json:"status,omitempty"` // ok | violation | not_comparable | no_active_coverage
	Violation       bool             `json:"violation"`
	Reason          string           `json:"reason,omitempty"`
	SellPrice       decimal.Decimal  `json:"sell_price"`
	LowestBuyCost   decimal.Decimal  `json:"lowest_buy_cost"`
	MarginAmount    decimal.Decimal  `json:"margin_amount"`
	MarginPercent   decimal.Decimal  `json:"margin_percent"`
	Currency        string           `json:"currency"`
	ResellerCost    *decimal.Decimal `json:"reseller_cost,omitempty"`
	ActiveProviders int              `json:"active_providers"`
}

type MarginReportItem struct {
	CardID          uuid.UUID       `json:"card_id"`
	CardName        string          `json:"card_name"`
	CountryID       uuid.UUID       `json:"country_id"`
	CountryISO2     string          `json:"country_iso2"`
	CountryName     string          `json:"country_name"`
	OperatorID      *uuid.UUID      `json:"operator_id,omitempty"`
	OperatorName    *string         `json:"operator_name,omitempty"`
	MessageType     string          `json:"message_type"`
	Currency        string          `json:"currency"`
	SellPrice       decimal.Decimal `json:"sell_price"`
	LowestBuyCost   decimal.Decimal `json:"lowest_buy_cost"`
	MarginAmount    decimal.Decimal `json:"margin_amount"`
	MarginPercent   decimal.Decimal `json:"margin_percent"`
	Violation       bool            `json:"violation"`
	ActiveProviders int             `json:"active_providers"`
}

type MarginReportFilter struct {
	CardID      *uuid.UUID
	CountryID   *uuid.UUID
	OnlyDeficit bool
	Page        int
	PageSize    int
}

type BulkLookupItem struct {
	CustomerOrgID uuid.UUID  `json:"customer_org_id"`
	Number        string     `json:"number"`
	MessageType   string     `json:"message_type"`
	At            *time.Time `json:"at,omitempty"`
}

type BulkLookupResult struct {
	Item  BulkLookupItem `json:"item"`
	Match *RateMatch     `json:"match,omitempty"`
	Error string         `json:"error,omitempty"`
}
