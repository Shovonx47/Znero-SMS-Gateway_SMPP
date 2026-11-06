package country

import (
	"time"

	"github.com/google/uuid"
)

type Country struct {
	ID                      uuid.UUID  `json:"id"`
	Name                    string     `json:"name"`
	ISO2                    string     `json:"iso2"`
	ISO3                    string     `json:"iso3"`
	NumericCode             string     `json:"numeric_code"`
	CallingCode             string     `json:"calling_code"`
	Continent               string     `json:"continent"`
	Region                  string     `json:"region"`
	CurrencyCode            string     `json:"currency_code"`
	Timezones               []string   `json:"timezones"`
	NumberingPlanNotes      string     `json:"numbering_plan_notes"`
	MinNationalLength       int        `json:"min_national_length"`
	MaxNationalLength       int        `json:"max_national_length"`
	SenderIDPolicyNotes     string     `json:"sender_id_policy_notes"`
	DNDPolicyNotes          string     `json:"dnd_policy_notes"`
	ConsentRequirementNotes string     `json:"consent_requirement_notes"`
	DataSource              string     `json:"data_source"`
	VerificationDate        *time.Time `json:"verification_date,omitempty"`
	VerificationStatus      string     `json:"verification_status"` // unverified, verified
	Status                  string     `json:"status"`              // active, inactive, archived
	Version                 int        `json:"version"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
	CreatedBy               *uuid.UUID `json:"created_by,omitempty"`
	UpdatedBy               *uuid.UUID `json:"updated_by,omitempty"`
}

type HistoryEntry struct {
	ID          uuid.UUID  `json:"id"`
	CountryID   uuid.UUID  `json:"country_id"`
	Version     int        `json:"version"`
	Snapshot    Country    `json:"snapshot"`
	ActorUserID *uuid.UUID `json:"actor_user_id,omitempty"`
	ChangedAt   time.Time  `json:"changed_at"`
	Reason      string     `json:"reason"`
}

type CreateCountryRequest struct {
	Name                    string   `json:"name"`
	ISO2                    string   `json:"iso2"`
	ISO3                    string   `json:"iso3"`
	NumericCode             string   `json:"numeric_code"`
	CallingCode             string   `json:"calling_code"`
	Continent               string   `json:"continent"`
	Region                  string   `json:"region"`
	CurrencyCode            string   `json:"currency_code"`
	Timezones               []string `json:"timezones"`
	NumberingPlanNotes      string   `json:"numbering_plan_notes"`
	MinNationalLength       int      `json:"min_national_length"`
	MaxNationalLength       int      `json:"max_national_length"`
	SenderIDPolicyNotes     string   `json:"sender_id_policy_notes"`
	DNDPolicyNotes          string   `json:"dnd_policy_notes"`
	ConsentRequirementNotes string   `json:"consent_requirement_notes"`
	DataSource              string   `json:"data_source"`
	Reason                  string   `json:"reason"`
}

type UpdateCountryRequest struct {
	Name                    string   `json:"name"`
	CallingCode             string   `json:"calling_code"`
	Continent               string   `json:"continent"`
	Region                  string   `json:"region"`
	CurrencyCode            string   `json:"currency_code"`
	Timezones               []string `json:"timezones"`
	NumberingPlanNotes      string   `json:"numbering_plan_notes"`
	MinNationalLength       int      `json:"min_national_length"`
	MaxNationalLength       int      `json:"max_national_length"`
	SenderIDPolicyNotes     string   `json:"sender_id_policy_notes"`
	DNDPolicyNotes          string   `json:"dnd_policy_notes"`
	ConsentRequirementNotes string   `json:"consent_requirement_notes"`
	DataSource              string   `json:"data_source"`
	VerificationStatus      string   `json:"verification_status"`
	Reason                  string   `json:"reason"`
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
	Status             string
	VerificationStatus string
	Continent          string
	CallingCode        string
	Page               int
	PageSize           int
	SortBy             string
	SortDir            string
	ActiveOnly         bool // Used for reseller/customer
}

type ListResult struct {
	Items      []Country `json:"items"`
	TotalCount int       `json:"total_count"`
	Page       int       `json:"page"`
	PageSize   int       `json:"page_size"`
}
