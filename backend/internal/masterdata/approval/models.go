package approval

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type ChangeRequest struct {
	ID            uuid.UUID       `json:"id"`
	EntityType    string          `json:"entity_type"` // country, operator, prefix, number_rule
	EntityID      *uuid.UUID      `json:"entity_id,omitempty"`
	Operation     string          `json:"operation"` // create, update, deactivate, activate, archive, rollback
	ProposedData  json.RawMessage `json:"proposed_data"`
	CurrentData   json.RawMessage `json:"current_data,omitempty"`
	Status        string          `json:"status"` // pending, approved, rejected, cancelled
	MakerUserID   uuid.UUID       `json:"maker_user_id"`
	MakerEmail    string          `json:"maker_email,omitempty"`
	MadeAt        time.Time       `json:"made_at"`
	CheckerUserID *uuid.UUID      `json:"checker_user_id,omitempty"`
	CheckerEmail  string          `json:"checker_email,omitempty"`
	CheckedAt     *time.Time      `json:"checked_at,omitempty"`
	RejectReason  string          `json:"reject_reason,omitempty"`
	EntityVersion *int            `json:"entity_version,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type ChangeRequestFilter struct {
	EntityType string
	Status     string
	MakerID    *uuid.UUID
	Page       int
	PageSize   int
}

type ListResult struct {
	Items      []ChangeRequest `json:"items"`
	TotalCount int             `json:"total_count"`
	Page       int             `json:"page"`
	PageSize   int             `json:"page_size"`
}

type RejectRequest struct {
	Reason string `json:"reason"`
}

type ApproveRequest struct {
	Reason string `json:"reason,omitempty"`
}
