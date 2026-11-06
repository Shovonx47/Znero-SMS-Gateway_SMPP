package audit

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
)

var sensitiveKeys = map[string]struct{}{
	"password": {}, "password_hash": {}, "token": {}, "token_hash": {},
	"secret": {}, "mfa_secret": {}, "mfa_secret_encrypted": {}, "recovery_code": {},
	"code_hash": {}, "refresh_token": {}, "refresh_token_hash": {},
}

// Entry is the audit log model.
type Entry struct {
	ID          uuid.UUID       `json:"id"`
	Timestamp   time.Time       `json:"timestamp"`
	ActorUserID *uuid.UUID      `json:"actor_user_id,omitempty"`
	ActorOrgID  *uuid.UUID      `json:"actor_org_id,omitempty"`
	TargetOrgID *uuid.UUID      `json:"target_org_id,omitempty"`
	Action      string          `json:"action"`
	Module      string          `json:"module"`
	EntityType  string          `json:"entity_type"`
	EntityID    string          `json:"entity_id"`
	BeforeValue json.RawMessage `json:"before_value,omitempty"`
	AfterValue  json.RawMessage `json:"after_value,omitempty"`
	IPAddress   string          `json:"ip_address"`
	UserAgent   string          `json:"user_agent"`
	RequestID   string          `json:"request_id"`
	Reason      string          `json:"reason,omitempty"`
}

type Filter struct {
	StartDate  *time.Time
	EndDate    *time.Time
	ActorID    *uuid.UUID
	TargetOrg  *uuid.UUID
	Module     string
	Action     string
	EntityType string
	Search     string
	Page       int
	PageSize   int
}

type ListResult struct {
	Items      []Entry `json:"items"`
	TotalCount int     `json:"total_count"`
	Page       int     `json:"page"`
	PageSize   int     `json:"page_size"`
}

// RedactValue recursively removes any sensitive fields from arbitrary JSON or struct data.
func RedactValue(val any) json.RawMessage {
	if val == nil {
		return nil
	}

	bytes, err := json.Marshal(val)
	if err != nil {
		return nil
	}

	var parsed any
	if err := json.Unmarshal(bytes, &parsed); err != nil {
		return bytes
	}

	redacted := sanitizeRecursive(parsed)
	out, err := json.Marshal(redacted)
	if err != nil {
		return bytes
	}
	return out
}

func isSensitiveKey(k string) bool {
	lower := strings.ToLower(k)
	if _, isSecret := sensitiveKeys[lower]; isSecret {
		return true
	}
	for s := range sensitiveKeys {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

func sanitizeRecursive(val any) any {
	switch v := val.(type) {
	case map[string]any:
		cleaned := make(map[string]any, len(v))
		for k, item := range v {
			if isSensitiveKey(k) {
				cleaned[k] = "[REDACTED]"
			} else {
				cleaned[k] = sanitizeRecursive(item)
			}
		}
		return cleaned
	case []any:
		cleaned := make([]any, len(v))
		for i, item := range v {
			cleaned[i] = sanitizeRecursive(item)
		}
		return cleaned
	default:
		return val
	}
}
