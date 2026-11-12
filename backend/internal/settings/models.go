package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

// Categories
const (
	CategoryGeneral       = "general"
	CategoryMessaging     = "messaging"
	CategoryRouting       = "routing"
	CategoryBilling       = "billing"
	CategorySecurity      = "security"
	CategoryNotifications = "notifications"
)

// Value Types
const (
	TypeString  = "string"
	TypeInteger = "integer"
	TypeBoolean = "boolean"
	TypeFloat   = "float"
	TypeJSON    = "json"
	TypeSecret  = "secret"
)

var (
	ErrSettingNotFound    = errors.New("setting not found")
	ErrInvalidValue       = errors.New("invalid setting value type or schema")
	ErrUnauthorizedTenant = errors.New("settings management is platform restricted")
)

type Setting struct {
	Key             string     `json:"key"`
	Value           any        `json:"value"`
	ValueType       string     `json:"value_type"`
	Category        string     `json:"category"`
	Description     string     `json:"description"`
	IsSecret        bool       `json:"is_secret"`
	UpdatedByUserID *uuid.UUID `json:"updated_by_user_id,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type UpdateSettingRequest struct {
	Value any `json:"value"`
}

// MaskSecret returns a copy of the setting with secret value masked.
func MaskSecret(s *Setting) *Setting {
	if s == nil {
		return nil
	}
	res := *s
	if s.IsSecret {
		res.Value = "********"
	}
	return &res
}

// ValidateAndCoerce validates the incoming value against the declared value_type.
func ValidateAndCoerce(val any, valueType string) (any, error) {
	if val == nil {
		return nil, fmt.Errorf("%w: value cannot be null", ErrInvalidValue)
	}

	switch valueType {
	case TypeString, TypeSecret:
		str, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("%w: expected string, got %T", ErrInvalidValue, val)
		}
		return str, nil

	case TypeBoolean:
		b, ok := val.(bool)
		if !ok {
			return nil, fmt.Errorf("%w: expected boolean, got %T", ErrInvalidValue, val)
		}
		return b, nil

	case TypeInteger:
		switch v := val.(type) {
		case int:
			return int64(v), nil
		case int64:
			return v, nil
		case float64:
			if math.Trunc(v) != v {
				return nil, fmt.Errorf("%w: float has fractional component, expected integer", ErrInvalidValue)
			}
			return int64(v), nil
		case json.Number:
			n, err := v.Int64()
			if err != nil {
				return nil, fmt.Errorf("%w: invalid integer number: %v", ErrInvalidValue, err)
			}
			return n, nil
		default:
			return nil, fmt.Errorf("%w: expected integer, got %T", ErrInvalidValue, val)
		}

	case TypeFloat:
		switch v := val.(type) {
		case float64:
			return v, nil
		case int:
			return float64(v), nil
		case int64:
			return float64(v), nil
		case json.Number:
			f, err := v.Float64()
			if err != nil {
				return nil, fmt.Errorf("%w: invalid float number: %v", ErrInvalidValue, err)
			}
			return f, nil
		default:
			return nil, fmt.Errorf("%w: expected float, got %T", ErrInvalidValue, val)
		}

	case TypeJSON:
		switch val.(type) {
		case map[string]interface{}, []interface{}:
			return val, nil
		default:
			return nil, fmt.Errorf("%w: expected json object or array, got %T", ErrInvalidValue, val)
		}

	default:
		return val, nil
	}
}

