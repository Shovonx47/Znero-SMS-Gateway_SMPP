package operator

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Operator struct {
	ID                 uuid.UUID       `json:"id"`
	Name               string          `json:"name"`
	BrandName          string          `json:"brand_name"`
	ParentCompany      string          `json:"parent_company"`
	CountryID          uuid.UUID       `json:"country_id"`
	CountryName        string          `json:"country_name,omitempty"`
	CountryISO2        string          `json:"country_iso2,omitempty"`
	MCC                string          `json:"mcc"`
	MNC                string          `json:"mnc"`
	NetworkTechnology  []string        `json:"network_technology"` // 2G, 3G, 4G, 5G
	Capabilities       json.RawMessage `json:"capabilities"`
	DataSource         string          `json:"data_source"`
	VerificationStatus string          `json:"verification_status"` // unverified, verified
	EffectiveDate      *time.Time      `json:"effective_date,omitempty"`
	Status             string          `json:"status"` // active, inactive, archived
	Version            int             `json:"version"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	CreatedBy          *uuid.UUID      `json:"created_by,omitempty"`
	UpdatedBy          *uuid.UUID      `json:"updated_by,omitempty"`
}

type HistoryEntry struct {
	ID          uuid.UUID  `json:"id"`
	OperatorID  uuid.UUID  `json:"operator_id"`
	Version     int        `json:"version"`
	Snapshot    Operator   `json:"snapshot"`
	ActorUserID *uuid.UUID `json:"actor_user_id,omitempty"`
	ChangedAt   time.Time  `json:"changed_at"`
	Reason      string     `json:"reason"`
}

type CreateOperatorRequest struct {
	Name               string          `json:"name"`
	BrandName          string          `json:"brand_name"`
	ParentCompany      string          `json:"parent_company"`
	CountryID          uuid.UUID       `json:"country_id"`
	MCC                string          `json:"mcc"`
	MNC                string          `json:"mnc"`
	NetworkTechnology  []string        `json:"network_technology"`
	Capabilities       json.RawMessage `json:"capabilities"`
	DataSource         string          `json:"data_source"`
	EffectiveDate      *time.Time      `json:"effective_date,omitempty"`
	Reason             string          `json:"reason"`
}

type UpdateOperatorRequest struct {
	Name               string          `json:"name"`
	BrandName          string          `json:"brand_name"`
	ParentCompany      string          `json:"parent_company"`
	NetworkTechnology  []string        `json:"network_technology"`
	Capabilities       json.RawMessage `json:"capabilities"`
	DataSource         string          `json:"data_source"`
	VerificationStatus string          `json:"verification_status"`
	EffectiveDate      *time.Time      `json:"effective_date,omitempty"`
	Reason             string          `json:"reason"`
}

type RollbackRequest struct {
	Version int    `json:"version"`
	Reason  string `json:"reason"`
}

type ActionReasonRequest struct {
	Reason string `json:"reason"`
}

type Filter struct {
	Search             string
	CountryID          *uuid.UUID
	Status             string
	VerificationStatus string
	MCC                string
	MNC                string
	Technology         string
	Page               int
	PageSize           int
	SortBy             string
	SortDir            string
	ActiveOnly         bool
}

type ListResult struct {
	Items      []Operator `json:"items"`
	TotalCount int        `json:"total_count"`
	Page       int        `json:"page"`
	PageSize   int        `json:"page_size"`
}
