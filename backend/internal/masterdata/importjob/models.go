package importjob

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type ImportJob struct {
	ID           uuid.UUID       `json:"id"`
	EntityType   string          `json:"entity_type"` // country, operator, prefix
	FileName     string          `json:"file_name"`
	FileSize     int64           `json:"file_size"`
	ContentType  string          `json:"content_type"`
	StoredPath   string          `json:"stored_path"`
	Status       string          `json:"status"` // pending, parsing, preview_ready, confirming, completed, failed, rolled_back
	TotalRows    int             `json:"total_rows"`
	ValidRows    int             `json:"valid_rows"`
	InvalidRows  int             `json:"invalid_rows"`
	ImportedRows int             `json:"imported_rows"`
	SkippedRows  int             `json:"skipped_rows"`
	AllowPartial bool            `json:"allow_partial"`
	ErrorSummary json.RawMessage `json:"error_summary"`
	ActorUserID  uuid.UUID       `json:"actor_user_id"`
	ActorEmail   string          `json:"actor_email,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	CompletedAt  *time.Time      `json:"completed_at,omitempty"`
}

type RowError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type ImportJobRow struct {
	ID              uuid.UUID       `json:"id"`
	JobID           uuid.UUID       `json:"job_id"`
	RowNumber       int             `json:"row_number"`
	RawData         json.RawMessage `json:"raw_data"`
	Status          string          `json:"status"` // valid, invalid, duplicate, conflict, imported, skipped
	Errors          []RowError      `json:"errors"`
	CreatedEntityID *uuid.UUID      `json:"created_entity_id,omitempty"`
}

type Filter struct {
	EntityType string
	Status     string
	Page       int
	PageSize   int
}

type ListResult struct {
	Items      []ImportJob `json:"items"`
	TotalCount int         `json:"total_count"`
	Page       int         `json:"page"`
	PageSize   int         `json:"page_size"`
}

type RowListResult struct {
	Items      []ImportJobRow `json:"items"`
	TotalCount int            `json:"total_count"`
	Page       int            `json:"page"`
	PageSize   int            `json:"page_size"`
}

type ConfirmRequest struct {
	AllowPartial bool `json:"allow_partial"`
}
