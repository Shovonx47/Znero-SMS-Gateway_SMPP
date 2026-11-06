package routing

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type Strategy string

const (
	StrategyPriority  Strategy = "priority"
	StrategyLeastCost Strategy = "least_cost"
	StrategyWeighted  Strategy = "weighted"
)

type PlanStatus string

const (
	PlanDraft       PlanStatus = "draft"
	PlanActive      PlanStatus = "active"
	PlanDeactivated PlanStatus = "deactivated"
	PlanArchived    PlanStatus = "archived"
)

type RoutePlan struct {
	ID          uuid.UUID        `json:"id"`
	Name        string           `json:"name"`
	Code        string           `json:"code"`
	Description string           `json:"description"`
	Strategy    Strategy         `json:"strategy"`
	Status      PlanStatus       `json:"status"`
	Notes       string           `json:"notes"`
	CreatedBy   uuid.UUID        `json:"created_by"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	Version     int              `json:"version"`
	Entries     []RoutePlanEntry `json:"entries,omitempty"`
}

type RoutePlanHistory struct {
	ID         uuid.UUID       `json:"id"`
	PlanID     uuid.UUID       `json:"plan_id"`
	ChangedBy  uuid.UUID       `json:"changed_by"`
	ChangedAt  time.Time       `json:"changed_at"`
	ChangeType string          `json:"change_type"`
	Snapshot   json.RawMessage `json:"snapshot"`
}

type RoutePlanEntry struct {
	ID                      uuid.UUID        `json:"id"`
	PlanID                  uuid.UUID        `json:"plan_id"`
	ConnectionID            uuid.UUID        `json:"connection_id"`
	ConnectionName          string           `json:"connection_name,omitempty"`
	ProviderName            string           `json:"provider_name,omitempty"`
	ScopeType               string           `json:"scope_type"` // global|country|operator|prefix
	ScopeCountryID          *uuid.UUID       `json:"scope_country_id,omitempty"`
	ScopeOperatorID         *uuid.UUID       `json:"scope_operator_id,omitempty"`
	ScopePrefix             string           `json:"scope_prefix,omitempty"`
	Priority                int              `json:"priority"`
	Weight                  int              `json:"weight"`
	MinMarginOverride       *decimal.Decimal `json:"min_margin_override,omitempty"`
	AllowedSenderIDTypes    []string         `json:"allowed_sender_id_types"`
	DestinationNumberFormat string           `json:"destination_number_format"`
	AllowTestedCoverage     bool             `json:"allow_tested_coverage"`
	Status                  string           `json:"status"` // active|disabled
	Version                 int              `json:"version"`
	CreatedAt               time.Time        `json:"created_at"`
	UpdatedAt               time.Time        `json:"updated_at"`
}

type RoutePlanEntryHistory struct {
	ID         uuid.UUID       `json:"id"`
	EntryID    uuid.UUID       `json:"entry_id"`
	ChangedBy  uuid.UUID       `json:"changed_by"`
	ChangedAt  time.Time       `json:"changed_at"`
	ChangeType string          `json:"change_type"`
	Snapshot   json.RawMessage `json:"snapshot"`
}

type CustomerRouteAssignment struct {
	ID            uuid.UUID  `json:"id"`
	CustomerOrgID uuid.UUID  `json:"customer_org_id"`
	CustomerName  string     `json:"customer_name,omitempty"`
	PlanID        uuid.UUID  `json:"plan_id"`
	PlanName      string     `json:"plan_name,omitempty"`
	CountryID     *uuid.UUID `json:"country_id,omitempty"`
	CountryName   string     `json:"country_name,omitempty"`
	Priority      int        `json:"priority"`
	EffectiveFrom *time.Time `json:"effective_from,omitempty"`
	EffectiveTo   *time.Time `json:"effective_to,omitempty"`
	Status        string     `json:"status"` // active|disabled
	Version       int        `json:"version"`
	CreatedBy     uuid.UUID  `json:"created_by"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type CustomerRouteAssignmentHistory struct {
	ID           uuid.UUID       `json:"id"`
	AssignmentID uuid.UUID       `json:"assignment_id"`
	ChangedBy    uuid.UUID       `json:"changed_by"`
	ChangedAt    time.Time       `json:"changed_at"`
	ChangeType   string          `json:"change_type"`
	Snapshot     json.RawMessage `json:"snapshot"`
}

type RoutingRule struct {
	ID                 uuid.UUID              `json:"id"`
	Name               string                 `json:"name"`
	Priority           int                    `json:"priority"`
	Status             string                 `json:"status"` // active|disabled
	EffectiveFrom      *time.Time             `json:"effective_from,omitempty"`
	EffectiveTo        *time.Time             `json:"effective_to,omitempty"`
	Conditions         map[string]interface{} `json:"conditions"`
	ActionType         string                 `json:"action_type"` // block|force|avoid|prefer
	ActionConnectionID *uuid.UUID             `json:"action_connection_id,omitempty"`
	ActionBlockReason  string                 `json:"action_block_reason,omitempty"`
	Version            int                    `json:"version"`
	CreatedBy          uuid.UUID              `json:"created_by"`
	CreatedAt          time.Time              `json:"created_at"`
	UpdatedAt          time.Time              `json:"updated_at"`
}

type RoutingRuleHistory struct {
	ID         uuid.UUID       `json:"id"`
	RuleID     uuid.UUID       `json:"rule_id"`
	ChangedBy  uuid.UUID       `json:"changed_by"`
	ChangedAt  time.Time       `json:"changed_at"`
	ChangeType string          `json:"change_type"`
	Snapshot   json.RawMessage `json:"snapshot"`
}

type ConnectionOverride struct {
	ID             uuid.UUID `json:"id"`
	ConnectionID   uuid.UUID `json:"connection_id"`
	ConnectionName string    `json:"connection_name,omitempty"`
	State          string    `json:"state"` // "disabled"
	Reason         string    `json:"reason"`
	SetBy          uuid.UUID `json:"set_by"`
	SetAt          time.Time `json:"set_at"`
}

// Standard reason codes for exclusion and tracing
const (
	ReasonPlanInactive             = "PLAN_INACTIVE"
	ReasonEntryInactive            = "ENTRY_INACTIVE"
	ReasonConnectionInactive       = "CONNECTION_INACTIVE"
	ReasonProviderInactive         = "PROVIDER_INACTIVE"
	ReasonKillSwitchActive         = "KILL_SWITCH_ACTIVE"
	ReasonEffectiveDateNotStarted  = "EFFECTIVE_DATE_NOT_STARTED"
	ReasonEffectiveDateExpired     = "EFFECTIVE_DATE_EXPIRED"
	ReasonCoverageNotActive        = "COVERAGE_NOT_ACTIVE"
	ReasonCoverageTestedDisallowed = "COVERAGE_TESTED_DISALLOWED"
	ReasonNoBuyRate                = "NO_BUY_RATE"
	ReasonNoSellRate               = "NO_SELL_RATE"
	ReasonMarginBelowMinimum       = "MARGIN_BELOW_MINIMUM"
	ReasonSenderIDTypeNotAllowed   = "SENDER_ID_TYPE_NOT_ALLOWED"
	ReasonSenderIDNotApproved      = "SENDER_ID_NOT_APPROVED"
	ReasonDestinationNotAllowed    = "DESTINATION_NOT_ALLOWED"
	ReasonCustomerSuspended        = "CUSTOMER_SUSPENDED"
	ReasonInvalidNumber            = "INVALID_NUMBER"
	ReasonCurrencyMismatch         = "CURRENCY_MISMATCH"
	ReasonExcludedByCaller         = "EXCLUDED_BY_CALLER"
	ReasonRuleBlock                = "RULE_BLOCK"
	ReasonScopeMismatch            = "SCOPE_MISMATCH"
)

// Snapshot types for in-memory engine
type SnapshotConnection struct {
	ID               uuid.UUID       `json:"id"`
	ProviderID       uuid.UUID       `json:"provider_id"`
	BuyCardID        *uuid.UUID      `json:"buy_card_id,omitempty"`
	ProviderStatus   string          `json:"provider_status"`   // active|inactive|suspended
	ConnectionStatus string          `json:"connection_status"` // active|disabled|draft
	CoverageStatus   string          `json:"coverage_status"`   // active|tested|inactive
	CountryID        uuid.UUID       `json:"country_id,omitempty"`
	OperatorID       *uuid.UUID      `json:"operator_id,omitempty"`
	KillSwitched     bool            `json:"kill_switched"`
	Cost             decimal.Decimal `json:"cost,omitempty"`
}

type SnapshotCustomerProfile struct {
	OrgID              uuid.UUID   `json:"org_id"`
	OrgStatus          string      `json:"org_status"` // active|suspended
	AccountType        string      `json:"account_type"`
	AllowedSenderTypes []string    `json:"allowed_sender_types"`
	AllowedCountries   []uuid.UUID `json:"allowed_countries,omitempty"`
}

type SnapshotSenderID struct {
	ID            uuid.UUID  `json:"id"`
	CustomerOrgID uuid.UUID  `json:"customer_org_id"`
	SenderID      string     `json:"sender_id"`
	Type          string     `json:"type"` // alphanumeric|numeric|shortcode
	CountryID     *uuid.UUID `json:"country_id,omitempty"`
	Status        string     `json:"status"` // pending|approved|rejected
}

type SnapshotRate struct {
	ConnectionID uuid.UUID       `json:"connection_id"`
	BuyRate      decimal.Decimal `json:"buy_rate"`
	SellRate     decimal.Decimal `json:"sell_rate"`
	CurrencyCode string          `json:"currency_code"`
}

type RoutingSnapshot struct {
	BuildAt          time.Time                              `json:"build_at"`
	Plans            []RoutePlan                            `json:"plans"`
	Assignments      []CustomerRouteAssignment              `json:"assignments"`
	Rules            []RoutingRule                          `json:"rules"`
	Overrides        map[uuid.UUID]ConnectionOverride       `json:"overrides"`         // keyed by connection_id
	Connections      map[uuid.UUID]SnapshotConnection       `json:"connections"`       // keyed by connection_id
	Coverage         map[string]string                      `json:"coverage"`          // keyed by provider_id:country_id(:operator_id)
	CustomerProfiles map[uuid.UUID]SnapshotCustomerProfile  `json:"customer_profiles"` // keyed by org_id
	ApprovedSenderIDs map[string]SnapshotSenderID           `json:"approved_sender_ids"`// keyed by org_id:sender_id(:country_id)
}

// Request/Response types
type ResolveRequest struct {
	CustomerOrgID   uuid.UUID   `json:"customer_org_id"`
	DestinationE164 string      `json:"destination_e164"` // optional: if provided, numberer resolves country/operator
	CountryID       *uuid.UUID  `json:"country_id,omitempty"`
	OperatorID      *uuid.UUID  `json:"operator_id,omitempty"`
	TrafficProfile  string      `json:"traffic_profile,omitempty"` // otp|transactional|promotional
	SenderIDType    string      `json:"sender_id_type"`            // numeric|alphanumeric
	SenderIDValue   string      `json:"sender_id_value,omitempty"`
	SenderID        string      `json:"sender_id,omitempty"`
	ExcludeIDs      []uuid.UUID `json:"exclude_ids,omitempty"`     // for Next() / failover
	Exclusions      []uuid.UUID `json:"exclusions,omitempty"`     // alias for ExcludeIDs
}

type ExclusionReason struct {
	ConnectionID uuid.UUID `json:"connection_id,omitempty"`
	Reason       string    `json:"reason"`
	ReasonCode   string    `json:"reason_code,omitempty"`
	Detail       string    `json:"detail,omitempty"`
}

type Candidate struct {
	ConnectionID      uuid.UUID       `json:"connection_id"`
	ConnectionName    string          `json:"connection_name,omitempty"`
	ProviderID        uuid.UUID       `json:"provider_id,omitempty"`
	Priority          int             `json:"priority"`
	Weight            int             `json:"weight"`
	Cost              decimal.Decimal `json:"cost,omitempty"`
	Rank              int             `json:"rank,omitempty"`
	Reason            string          `json:"reason,omitempty"`
	EstimatedBuyRate  decimal.Decimal `json:"estimated_buy_rate"`
	EstimatedSellRate decimal.Decimal `json:"estimated_sell_rate"`
	CurrencyCode      string          `json:"currency_code"`
}

type ResolveResult struct {
	Request         ResolveRequest    `json:"request"`
	Candidate       *Candidate        `json:"candidate,omitempty"` // top-ranked candidate
	Candidates      []Candidate       `json:"candidates"`          // ordered, eligible
	AllCandidates   []Candidate       `json:"all_candidates,omitempty"`
	Excluded        []ExclusionReason `json:"excluded"` // all checked but excluded, with reasons
	Exclusions      []ExclusionReason `json:"exclusions,omitempty"`
	RulesApplied    []string          `json:"rules_applied"` // rule names that fired
	PlanUsed        string            `json:"plan_used"`     // plan code
	PlanID          *uuid.UUID        `json:"plan_id,omitempty"`
	StrategyUsed    Strategy          `json:"strategy_used,omitempty"`
	SnapshotBuiltAt time.Time         `json:"snapshot_built_at"`
	SnapshotAgeMs   int64             `json:"snapshot_age_ms"`
}

type SimulateRequest struct {
	CustomerOrgID     *uuid.UUID `json:"customer_org_id,omitempty"`
	DestinationE164   string     `json:"destination_e164"`
	DestinationNumber string     `json:"destination_number,omitempty"`
	CountryID         *uuid.UUID `json:"country_id,omitempty"`
	OperatorID        *uuid.UUID `json:"operator_id,omitempty"`
	TrafficProfile    string     `json:"traffic_profile,omitempty"`
	SenderIDType      string     `json:"sender_id_type"`
	SenderIDValue     string     `json:"sender_id_value,omitempty"`
	SenderID          string     `json:"sender_id,omitempty"`
}

type SimulateResult struct {
	ResolveResult
	Trace []string `json:"trace"` // human-readable pipeline steps
}

// Interfaces for engine integration
type NumberInfo struct {
	CountryID  uuid.UUID
	OperatorID *uuid.UUID
	Prefix     string
}

type NumbererInterface interface {
	Lookup(destinationE164 string) (*NumberInfo, error)
}

type RateLookupResult struct {
	Found    bool
	Price    decimal.Decimal
	Currency string
}

type RaterInterface interface {
	LookupBuyRate(ctx context.Context, connectionID uuid.UUID, countryID uuid.UUID, operatorID *uuid.UUID) (*RateLookupResult, error)
	LookupSellRate(ctx context.Context, customerOrgID uuid.UUID, countryID uuid.UUID, operatorID *uuid.UUID) (*RateLookupResult, error)
}
