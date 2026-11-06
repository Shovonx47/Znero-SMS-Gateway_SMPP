package prefix

import (
	"time"

	"github.com/google/uuid"
)

type NumberPrefix struct {
	ID            uuid.UUID  `json:"id"`
	Prefix        string     `json:"prefix"`
	CountryID     uuid.UUID  `json:"country_id"`
	CountryName   string     `json:"country_name,omitempty"`
	CountryISO2   string     `json:"country_iso2,omitempty"`
	OperatorID    *uuid.UUID `json:"operator_id,omitempty"`
	OperatorName  string     `json:"operator_name,omitempty"`
	NumberType    string     `json:"number_type"` // mobile, fixed, other
	Priority      int        `json:"priority"`
	EffectiveFrom *time.Time `json:"effective_from,omitempty"`
	EffectiveTo   *time.Time `json:"effective_to,omitempty"`
	DataSource    string     `json:"data_source"`
	Status        string     `json:"status"` // active, inactive, archived
	Version       int        `json:"version"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	CreatedBy     *uuid.UUID `json:"created_by,omitempty"`
	UpdatedBy     *uuid.UUID `json:"updated_by,omitempty"`
}

type HistoryEntry struct {
	ID          uuid.UUID    `json:"id"`
	PrefixID    uuid.UUID    `json:"prefix_id"`
	Version     int          `json:"version"`
	Snapshot    NumberPrefix `json:"snapshot"`
	ActorUserID *uuid.UUID   `json:"actor_user_id,omitempty"`
	ChangedAt   time.Time    `json:"changed_at"`
	Reason      string       `json:"reason"`
}

type CreatePrefixRequest struct {
	Prefix        string     `json:"prefix"`
	CountryID     uuid.UUID  `json:"country_id"`
	OperatorID    *uuid.UUID `json:"operator_id,omitempty"`
	NumberType    string     `json:"number_type"`
	Priority      int        `json:"priority"`
	EffectiveFrom *time.Time `json:"effective_from,omitempty"`
	EffectiveTo   *time.Time `json:"effective_to,omitempty"`
	DataSource    string     `json:"data_source"`
	Reason        string     `json:"reason"`
}

type UpdatePrefixRequest struct {
	Prefix        string     `json:"prefix"`
	OperatorID    *uuid.UUID `json:"operator_id,omitempty"`
	NumberType    string     `json:"number_type"`
	Priority      int        `json:"priority"`
	EffectiveFrom *time.Time `json:"effective_from,omitempty"`
	EffectiveTo   *time.Time `json:"effective_to,omitempty"`
	DataSource    string     `json:"data_source"`
	Reason        string     `json:"reason"`
}

type RollbackRequest struct {
	Version int    `json:"version"`
	Reason  string `json:"reason"`
}

type ActionReasonRequest struct {
	Reason string `json:"reason"`
}

type Filter struct {
	Search     string
	CountryID  *uuid.UUID
	OperatorID *uuid.UUID
	NumberType string
	Status     string
	Page       int
	PageSize   int
	SortBy     string
	SortDir    string
	ActiveOnly bool
}

type ListResult struct {
	Items      []NumberPrefix `json:"items"`
	TotalCount int            `json:"total_count"`
	Page       int            `json:"page"`
	PageSize   int            `json:"page_size"`
}
