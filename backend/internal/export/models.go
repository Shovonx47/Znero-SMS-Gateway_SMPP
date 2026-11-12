package export

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Supported Formats
const (
	FormatCSV  = "csv"
	FormatJSON = "json"
	FormatXLSX = "xlsx"
)

// Supported Export Types
const (
	TypeMessages        = "messages"
	TypeDeliveryReport  = "delivery_report"
	TypeFinancialReport = "financial_report"
)

// Job Statuses
const (
	StatusPending    = "pending"
	StatusProcessing = "processing"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
)

var (
	ErrJobNotFound        = errors.New("export job not found")
	ErrJobNotReady        = errors.New("export job is not yet completed")
	ErrJobExpired         = errors.New("export file has expired")
	ErrUnsupportedFormat  = errors.New("unsupported export format (supported: csv, json, xlsx)")
	ErrUnsupportedType    = errors.New("unsupported export type (supported: messages, delivery_report, financial_report)")
	ErrUnauthorizedTenant = errors.New("unauthorized export job access")
)

type ExportJob struct {
	ID             uuid.UUID              `json:"id"`
	OrganizationID uuid.UUID              `json:"organization_id"`
	UserID         uuid.UUID              `json:"user_id"`
	ExportType     string                 `json:"export_type"`
	Format         string                 `json:"format"`
	Status         string                 `json:"status"`
	FilterParams   map[string]interface{} `json:"filter_params"`
	RowCount       int64                  `json:"row_count"`
	FilePath       *string                `json:"file_path,omitempty"`
	ErrorMessage   *string                `json:"error_message,omitempty"`
	StartedAt      *time.Time             `json:"started_at,omitempty"`
	CompletedAt    *time.Time             `json:"completed_at,omitempty"`
	ExpiresAt      *time.Time             `json:"expires_at,omitempty"`
	CreatedAt      time.Time              `json:"created_at"`
}

type CreateJobRequest struct {
	ExportType   string                 `json:"export_type"`
	Format       string                 `json:"format"`
	FilterParams map[string]interface{} `json:"filter_params,omitempty"`
}

// SanitizeCSVCell neutralizes CSV / Excel formula injection attempts.
// Any cell value beginning with '=', '+', '-', '@', '\t', or '\r' is prefixed with a single quote.
func SanitizeCSVCell(val string) string {
	if len(val) == 0 {
		return val
	}
	first := val[0]
	if first == '=' || first == '+' || first == '-' || first == '@' || first == '\t' || first == '\r' {
		return "'" + val
	}
	// Also sanitize if leading whitespace hides a formula
	trimmed := strings.TrimLeft(val, " \t\r\n")
	if len(trimmed) > 0 {
		firstTrimmed := trimmed[0]
		if firstTrimmed == '=' || firstTrimmed == '+' || firstTrimmed == '-' || firstTrimmed == '@' {
			return "'" + val
		}
	}
	return val
}
