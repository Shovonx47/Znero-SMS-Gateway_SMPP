package messaging

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Message Status Constants
const (
	StatusAccepted    = "accepted"
	StatusQueued      = "queued"
	StatusSubmitting  = "submitting"
	StatusSubmitted   = "submitted" // Carrier/Provider Accepted
	StatusDelivered   = "delivered"
	StatusUndelivered = "undelivered"
	StatusExpired     = "expired"
	StatusFailed      = "failed"
	StatusUnknown     = "unknown"
	StatusRejected    = "rejected"
)

// Standard Messaging Rejection & Failure Reason Codes
const (
	ReasonMissingSellRate            = "missing_sell_rate"
	ReasonNoRouteAvailable           = "no_route_available"
	ReasonInsufficientFunds          = "insufficient_funds"
	ReasonInvalidDestination         = "invalid_destination"
	ReasonInvalidSenderID            = "invalid_sender_id"
	ReasonMaxSegmentsExceeded        = "max_segments_exceeded"
	ReasonCustomerSuspended          = "customer_suspended"
	ReasonDestinationBarred          = "destination_barred"
	ReasonWalletNotFound             = "wallet_not_found"
	ReasonDuplicateClientReference   = "duplicate_client_reference"
	ReasonKilledAtDispatch           = "killed_at_dispatch"
	ReasonMaxAttemptsExhausted       = "max_attempts_exhausted"
	ReasonPermanentCarrierReject     = "permanent_carrier_reject"
	ReasonAmbiguousCarrierOutcome    = "ambiguous_carrier_outcome"
	ReasonClientCancelled            = "client_cancelled"
)

// Allowed State Machine Transitions
var validTransitions = map[string]map[string]bool{
	StatusAccepted: {
		StatusQueued:     true,
		StatusSubmitting: true,
		StatusSubmitted:  true, // Direct outbox dispatch carrier accept
		StatusUnknown:    true, // Direct outbox dispatch ambiguous outcome
		StatusFailed:     true, // If pipeline aborts or unrecoverable outbox error
		StatusRejected:   true, // If pre-hold or sanity check rejects
	},
	StatusQueued: {
		StatusSubmitting: true,
		StatusSubmitted:  true,
		StatusUnknown:    true,
		StatusFailed:     true,
	},
	StatusSubmitting: {
		StatusSubmitting: true, // Failover route retry
		StatusSubmitted:  true, // Carrier accepted
		StatusUnknown:    true, // Socket drop / timeout (ambiguous)
		StatusFailed:     true, // Fatal reject or max attempts exhausted
	},
	StatusSubmitted: {
		StatusDelivered:   true, // DLR DELIVRD
		StatusUndelivered: true, // DLR UNDELIV
		StatusExpired:     true, // DLR EXPIRED
		StatusFailed:      true, // DLR REJECTD
	},
	StatusUnknown: {
		StatusDelivered:   true, // Late DLR confirms delivery
		StatusUndelivered: true, // Late DLR confirms non-delivery
		StatusFailed:      true, // Reaper resolves timeout
		StatusExpired:     true, // Reaper resolves expiration
	},
	// Terminal States: no outgoing transitions allowed
	StatusDelivered:   {},
	StatusUndelivered: {},
	StatusExpired:     {},
	StatusFailed:      {},
	StatusRejected:    {},
}

var (
	ErrInvalidStateTransition = errors.New("invalid state transition")
	ErrDuplicateClientRef     = errors.New("duplicate client reference")
	ErrMaxSegmentsExceeded    = errors.New("maximum message segments exceeded")
	ErrMissingSellRate        = errors.New("no active sell rate found for destination")
	ErrNoRouteAvailable       = errors.New("no eligible routing candidate available")
	ErrInsufficientBalance    = errors.New("insufficient balance for message hold")
	ErrInvalidDestination     = errors.New("invalid destination phone number")
	ErrSenderIDNotApproved    = errors.New("sender ID not approved for this customer")
	ErrCustomerInactive       = errors.New("customer organization is suspended or inactive")
	ErrDestinationBarred      = errors.New("destination country is not permitted by customer policy")
	ErrAmbiguousOutcome       = errors.New("carrier returned ambiguous outcome; never auto-resend")
)

// ValidateTransition verifies whether moving from fromStatus to toStatus is permitted.
func ValidateTransition(fromStatus, toStatus string) error {
	if fromStatus == toStatus {
		return nil
	}
	allowedTargets, ok := validTransitions[fromStatus]
	if !ok || !allowedTargets[toStatus] {
		return fmt.Errorf("%w: cannot transition message from %q to %q", ErrInvalidStateTransition, fromStatus, toStatus)
	}
	return nil
}

// Message represents a partitioned outbound SMS message record.
type Message struct {
	ID                   uuid.UUID        `json:"id"`
	CreatedAt            time.Time        `json:"created_at"`
	CustomerOrgID        uuid.UUID        `json:"customer_org_id"`
	ClientReference      *string          `json:"client_reference,omitempty"`
	OriginalDestination string           `json:"original_destination"`
	Destination          string           `json:"destination"`
	SenderID             string           `json:"sender_id"`
	SenderIDType         string           `json:"sender_id_type"`
	MessageType          string           `json:"message_type"`
	Encoding             string           `json:"encoding"`
	EncryptedBody        []byte           `json:"-"`
	KeyID                string           `json:"key_id"`
	BodyLength           int              `json:"body_length"`
	TotalParts           int              `json:"total_parts"`
	Status               string           `json:"status"`
	FailureReason        *string          `json:"failure_reason,omitempty"`
	HoldID               *uuid.UUID       `json:"hold_id,omitempty"`
	SellPrice            decimal.Decimal  `json:"sell_price"`
	SellCurrency         string           `json:"sell_currency"`
	EstimatedBuyCost     *decimal.Decimal `json:"estimated_buy_cost,omitempty"`
	ActualBuyCost        *decimal.Decimal `json:"actual_buy_cost,omitempty"`
	BuyCurrency          *string          `json:"buy_currency,omitempty"`
	RoutePlanID          *uuid.UUID       `json:"route_plan_id,omitempty"`
	ProviderConnectionID *uuid.UUID       `json:"provider_connection_id,omitempty"`
	ProviderMessageID    *string          `json:"provider_message_id,omitempty"`
	Attempts             int              `json:"attempts"`
	MaxAttempts          int              `json:"max_attempts"`
	ExpiresAt            time.Time        `json:"expires_at"`
	SubmittedAt          *time.Time       `json:"submitted_at,omitempty"`
	DeliveredAt          *time.Time       `json:"delivered_at,omitempty"`
	UpdatedAt            time.Time        `json:"updated_at"`
}

// MessagePart represents an individual segment of a multi-part concatenated message.
type MessagePart struct {
	ID                uuid.UUID `json:"id"`
	MessageID         uuid.UUID `json:"message_id"`
	CreatedAt         time.Time `json:"created_at"`
	PartNumber        int       `json:"part_number"`
	TotalParts        int       `json:"total_parts"`
	UDH               []byte    `json:"udh,omitempty"`
	EncryptedPartBody []byte    `json:"-"`
	KeyID             string    `json:"key_id"`
	ProviderMessageID *string   `json:"provider_message_id,omitempty"`
	Status            string    `json:"status"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// MessageOutbox represents a row in the transactional outbox table.
type MessageOutbox struct {
	ID               uuid.UUID       `json:"id"`
	MessageID        uuid.UUID       `json:"message_id"`
	MessageCreatedAt time.Time       `json:"message_created_at"`
	CustomerOrgID    uuid.UUID       `json:"customer_org_id"`
	Payload          json.RawMessage `json:"payload"`
	Status           string          `json:"status"` // pending|sending|published|failed
	Attempts         int             `json:"attempts"`
	NextAttemptAt    time.Time       `json:"next_attempt_at"`
	LockedUntil      *time.Time      `json:"locked_until,omitempty"`
	LockedBy         *string         `json:"locked_by,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// MessageAttempt records an individual transmission attempt to an upstream provider.
type MessageAttempt struct {
	ID                 uuid.UUID        `json:"id"`
	MessageID          uuid.UUID        `json:"message_id"`
	MessageCreatedAt   time.Time        `json:"message_created_at"`
	AttemptNumber      int              `json:"attempt_number"`
	ConnectionID       uuid.UUID        `json:"connection_id"`
	Status             string           `json:"status"` // submitting|accepted|retryable_reject|fatal_reject|unknown
	ProviderMessageID  *string          `json:"provider_message_id,omitempty"`
	ErrorCode          *string          `json:"error_code,omitempty"`
	ErrorMessage       *string          `json:"error_message,omitempty"`
	BuyCost            *decimal.Decimal `json:"buy_cost,omitempty"`
	BuyCurrency        *string          `json:"buy_currency,omitempty"`
	LatencyMs          *int             `json:"latency_ms,omitempty"`
	StartedAt          time.Time        `json:"started_at"`
	FinishedAt         *time.Time       `json:"finished_at,omitempty"`
}

// MessageEvent represents an append-only lifecycle transition event.
type MessageEvent struct {
	ID               uuid.UUID       `json:"id"`
	MessageID        uuid.UUID       `json:"message_id"`
	MessageCreatedAt time.Time       `json:"message_created_at"`
	EventType        string          `json:"event_type"`
	FromStatus       *string         `json:"from_status,omitempty"`
	ToStatus         string          `json:"to_status"`
	ActorType        string          `json:"actor_type"`
	Details          json.RawMessage `json:"details,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
}

// MessageDLR represents an append-only DLR status report receipt.
type MessageDLR struct {
	ID                 uuid.UUID  `json:"id"`
	MessageID          *uuid.UUID `json:"message_id,omitempty"`
	MessageCreatedAt   *time.Time `json:"message_created_at,omitempty"`
	ConnectionID       *uuid.UUID `json:"connection_id,omitempty"`
	ProviderMessageID  string     `json:"provider_message_id"`
	Status             string     `json:"status"`
	ErrorCode          *string    `json:"error_code,omitempty"`
	RawReceipt         *string    `json:"raw_receipt,omitempty"`
	ReceivedAt         time.Time  `json:"received_at"`
}

// SubmitRequest is the client submission DTO.
type SubmitRequest struct {
	Destination     string  `json:"destination"`
	SenderID        string  `json:"sender_id"`
	Text            string  `json:"text"`
	ClientReference *string `json:"client_reference,omitempty"`
	TrafficProfile  string  `json:"traffic_profile,omitempty"` // otp|transactional|promotional
	ValiditySeconds *int    `json:"validity_seconds,omitempty"`
}

// SubmitResponse is returned to the client upon durable ingestion.
type SubmitResponse struct {
	MessageID       uuid.UUID       `json:"message_id"`
	Status          string          `json:"status"`
	Destination     string          `json:"destination"`
	SenderID        string          `json:"sender_id"`
	Parts           int             `json:"parts"`
	Currency        string          `json:"currency"`
	Price           decimal.Decimal `json:"price"`
	ClientReference *string         `json:"client_reference,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
}

// MessageView is the sanitized read view with tenant field-stripping applied.
type MessageView struct {
	ID                   uuid.UUID        `json:"id"`
	CreatedAt            time.Time        `json:"created_at"`
	CustomerOrgID        uuid.UUID        `json:"customer_org_id"`
	ClientReference      *string          `json:"client_reference,omitempty"`
	OriginalDestination string           `json:"original_destination"`
	Destination          string           `json:"destination"`
	SenderID             string           `json:"sender_id"`
	SenderIDType         string           `json:"sender_id_type"`
	Encoding             string           `json:"encoding"`
	Body                 string           `json:"body,omitempty"` // Masked unless message.content.view held
	BodyLength           int              `json:"body_length"`
	TotalParts           int              `json:"total_parts"`
	Status               string           `json:"status"`
	FailureReason        *string          `json:"failure_reason,omitempty"`
	SellPrice            decimal.Decimal  `json:"sell_price"`
	SellCurrency         string           `json:"sell_currency"`
	Attempts             int              `json:"attempts"`
	ExpiresAt            time.Time        `json:"expires_at"`
	SubmittedAt          *time.Time       `json:"submitted_at,omitempty"`
	DeliveredAt          *time.Time       `json:"delivered_at,omitempty"`

	// Platform-Only Fields (stripped for non-platform callers)
	ProviderConnectionID *uuid.UUID       `json:"provider_connection_id,omitempty"`
	ProviderMessageID    *string          `json:"provider_message_id,omitempty"`
	EstimatedBuyCost     *decimal.Decimal `json:"estimated_buy_cost,omitempty"`
	ActualBuyCost        *decimal.Decimal `json:"actual_buy_cost,omitempty"`
	BuyCurrency          *string          `json:"buy_currency,omitempty"`
	RoutePlanID          *uuid.UUID       `json:"route_plan_id,omitempty"`
	Margin               *decimal.Decimal `json:"margin,omitempty"`
}

// OutboxPayload is serialized into message_outbox.payload
type OutboxPayload struct {
	MessageID            uuid.UUID       `json:"message_id"`
	MessageCreatedAt     time.Time       `json:"message_created_at"`
	CustomerOrgID        uuid.UUID       `json:"customer_org_id"`
	Destination          string          `json:"destination"`
	SenderID             string          `json:"sender_id"`
	SenderIDType         string          `json:"sender_id_type"`
	Encoding             string          `json:"encoding"`
	TotalParts           int             `json:"total_parts"`
	SellPrice            decimal.Decimal `json:"sell_price"`
	SellCurrency         string          `json:"sell_currency"`
	CandidateConnectionIDs []uuid.UUID     `json:"candidate_connection_ids"`
	CurrentConnectionID    uuid.UUID       `json:"current_connection_id"`
	FailedConnectionIDs    []uuid.UUID     `json:"failed_connection_ids,omitempty"`
	AttemptNumber          int             `json:"attempt_number"`
	MaxAttempts          int             `json:"max_attempts"`
	Body                 string          `json:"body"`
}

