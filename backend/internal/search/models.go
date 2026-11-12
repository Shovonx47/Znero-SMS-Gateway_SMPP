package search

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrFilterNotFound = errors.New("saved filter not found")
	ErrInvalidLocale  = errors.New("unsupported locale (supported: en, bn)")
	ErrInvalidTheme   = errors.New("unsupported theme (supported: system, light, dark)")
)

type SearchResultItem struct {
	Type     string                 `json:"type"` // "message", "customer", "provider", "incident", "alert"
	ID       string                 `json:"id"`
	Title    string                 `json:"title"`
	Subtitle string                 `json:"subtitle"`
	URL      string                 `json:"url"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

type SearchResponse struct {
	Query   string              `json:"query"`
	Total   int                 `json:"total"`
	Results []*SearchResultItem `json:"results"`
}

type SavedFilter struct {
	ID        uuid.UUID              `json:"id"`
	UserID    uuid.UUID              `json:"user_id"`
	ViewName  string                 `json:"view_name"`
	Name      string                 `json:"name"`
	Query     map[string]interface{} `json:"query"`
	IsDefault bool                   `json:"is_default"`
	CreatedAt time.Time              `json:"created_at"`
}

type CreateSavedFilterRequest struct {
	ViewName  string                 `json:"view_name"`
	Name      string                 `json:"name"`
	Query     map[string]interface{} `json:"query"`
	IsDefault bool                   `json:"is_default"`
}

type UserPreference struct {
	UserID          uuid.UUID              `json:"user_id"`
	Theme           string                 `json:"theme"`
	Locale          string                 `json:"locale"`
	Timezone        string                 `json:"timezone"`
	DashboardLayout map[string]interface{} `json:"dashboard_layout"`
	UpdatedAt       time.Time              `json:"updated_at"`
}

type UpdatePreferenceRequest struct {
	Theme           *string                 `json:"theme,omitempty"`
	Locale          *string                 `json:"locale,omitempty"`
	Timezone        *string                 `json:"timezone,omitempty"`
	DashboardLayout *map[string]interface{} `json:"dashboard_layout,omitempty"`
}

