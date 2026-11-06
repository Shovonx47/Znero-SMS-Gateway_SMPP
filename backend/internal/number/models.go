package number

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type LookupRequest struct {
	Number            string `json:"number"`
	Direction         string `json:"direction,omitempty"`           // inbound, outbound (default: outbound)
	DefaultCountryISO string `json:"default_country_iso,omitempty"` // fallback country hint e.g. "US", "GB"
}

type BulkLookupRequest struct {
	Numbers           []string `json:"numbers"`
	Direction         string   `json:"direction,omitempty"`
	DefaultCountryISO string   `json:"default_country_iso,omitempty"`
}

type CountryInfo struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	ISO2        string    `json:"iso2"`
	CallingCode string    `json:"calling_code"`
}

type OperatorInfo struct {
	ID        *uuid.UUID `json:"id,omitempty"`
	Name      string     `json:"name"`
	BrandName string     `json:"brand_name,omitempty"`
	MCC       string     `json:"mcc,omitempty"`
	MNC       string     `json:"mnc,omitempty"`
}

type AppliedRule struct {
	RuleID    uuid.UUID `json:"rule_id"`
	RuleName  string    `json:"rule_name"`
	Operation string    `json:"operation"`
	Before    string    `json:"before"`
	After     string    `json:"after"`
}

type NumberLookupResult struct {
	OriginalNumber string        `json:"original_number"`
	NormalizedE164 *string       `json:"normalized_e164"`
	IsValid        bool          `json:"is_valid"`
	NumberType     string        `json:"number_type"` // mobile, fixed, voip, other, unknown
	Country        *CountryInfo  `json:"country,omitempty"`
	Operator       *OperatorInfo `json:"operator,omitempty"`
	MatchedPrefix  string        `json:"matched_prefix,omitempty"`
	AppliedRules   []AppliedRule `json:"applied_rules,omitempty"`
	Error          string        `json:"error,omitempty"`
}

type BulkLookupResult struct {
	Results   []NumberLookupResult `json:"results"`
	Total     int                  `json:"total"`
	Valid     int                  `json:"valid"`
	Invalid   int                  `json:"invalid"`
	ProcessMs int64                `json:"process_ms"`
}

type TransformRule struct {
	ID            uuid.UUID         `json:"id"`
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	Operation     string            `json:"operation"` // add_prefix, remove_prefix, replace_prefix, add_country_code, remove_country_code
	MatchPattern  string            `json:"match_pattern"`
	Params        map[string]string `json:"params"`
	Scope         string            `json:"scope"` // global, country, operator
	CountryID     *uuid.UUID        `json:"country_id,omitempty"`
	CountryName   string            `json:"country_name,omitempty"`
	CountryISO2   string            `json:"country_iso2,omitempty"`
	OperatorID    *uuid.UUID        `json:"operator_id,omitempty"`
	OperatorName  string            `json:"operator_name,omitempty"`
	Direction     string            `json:"direction"` // inbound, outbound, both
	Priority      int               `json:"priority"`  // lower number = higher precedence
	ActiveFrom    *time.Time        `json:"active_from,omitempty"`
	ActiveTo      *time.Time        `json:"active_to,omitempty"`
	Status        string            `json:"status"` // active, inactive
	Version       int               `json:"version"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	CreatedBy     *uuid.UUID        `json:"created_by,omitempty"`
	UpdatedBy     *uuid.UUID        `json:"updated_by,omitempty"`
}

type CreateTransformRuleRequest struct {
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Operation    string            `json:"operation"`
	MatchPattern string            `json:"match_pattern"`
	Params       map[string]string `json:"params"`
	Scope        string            `json:"scope"`
	CountryID    *uuid.UUID        `json:"country_id,omitempty"`
	OperatorID   *uuid.UUID        `json:"operator_id,omitempty"`
	Direction    string            `json:"direction"`
	Priority     int               `json:"priority"`
	ActiveFrom   *time.Time        `json:"active_from,omitempty"`
	ActiveTo     *time.Time        `json:"active_to,omitempty"`
	Reason       string            `json:"reason"`
}

type UpdateTransformRuleRequest struct {
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Operation    string            `json:"operation"`
	MatchPattern string            `json:"match_pattern"`
	Params       map[string]string `json:"params"`
	Scope        string            `json:"scope"`
	CountryID    *uuid.UUID        `json:"country_id,omitempty"`
	OperatorID   *uuid.UUID        `json:"operator_id,omitempty"`
	Direction    string            `json:"direction"`
	Priority     int               `json:"priority"`
	ActiveFrom   *time.Time        `json:"active_from,omitempty"`
	ActiveTo     *time.Time        `json:"active_to,omitempty"`
	Status       string            `json:"status"`
	Reason       string            `json:"reason"`
}

type RuleFilter struct {
	Scope     string
	Status    string
	CountryID *uuid.UUID
	Page      int
	PageSize  int
}

type RuleListResult struct {
	Items      []TransformRule `json:"items"`
	TotalCount int             `json:"total_count"`
	Page       int             `json:"page"`
	PageSize   int             `json:"page_size"`
}

type RulePreviewRequest struct {
	Rule   TransformRule `json:"rule"`
	Number string        `json:"number"`
}

type RulePreviewResult struct {
	Original    string `json:"original"`
	Transformed string `json:"transformed"`
	Applied     bool   `json:"applied"`
}

type ChainTestRequest struct {
	Number     string     `json:"number"`
	Direction  string     `json:"direction"`
	CountryID  *uuid.UUID `json:"country_id,omitempty"`
	OperatorID *uuid.UUID `json:"operator_id,omitempty"`
}

type ChainStep struct {
	RuleID    uuid.UUID `json:"rule_id"`
	RuleName  string    `json:"rule_name"`
	Operation string    `json:"operation"`
	Before    string    `json:"before"`
	After     string    `json:"after"`
}

type ChainTestResult struct {
	Original string      `json:"original"`
	Final    string      `json:"final"`
	Steps    []ChainStep `json:"steps"`
}

type RuleHistoryEntry struct {
	ID          uuid.UUID       `json:"id"`
	RuleID      uuid.UUID       `json:"rule_id"`
	Version     int             `json:"version"`
	Snapshot    json.RawMessage `json:"snapshot"`
	ActorUserID *uuid.UUID      `json:"actor_user_id,omitempty"`
	ChangedAt   time.Time       `json:"changed_at"`
	Reason      string          `json:"reason"`
}
