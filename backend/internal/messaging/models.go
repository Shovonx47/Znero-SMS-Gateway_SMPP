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
	ReasonMissingSellRate          = "missing_sell_rate"
	ReasonNoRouteAvailable         = "no_route_available"
	ReasonInsufficientFunds        = "insufficient_funds"
	ReasonInvalidDestination       = "invalid_destination"
	ReasonInvalidSenderID          = "invalid_sender_id"
	ReasonMaxSegmentsExceeded      = "max_segments_exceeded"
	ReasonCustomerSuspended        = "customer_suspended"
	ReasonDestinationBarred        = "destination_barred"
	ReasonWalletNotFound           = "wallet_not_found"
	ReasonDuplicateClientReference = "duplicate_client_reference"
	ReasonKilledAtDispatch         = "killed_at_dispatch"
	ReasonMaxAttemptsExhausted     = "max_attempts_exhausted"
	ReasonPermanentCarrierReject   = "permanent_carrier_reject"
	ReasonAmbiguousCarrierOutcome  = "ambiguous_carrier_outcome"
	ReasonClientCancelled          = "client_cancelled"
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

// CanTransition checks whether a transition from status `from` to status `to` is allowed.
func CanTransition(from, to string) bool {
	targets, ok := validTransitions[from]
	if !ok {
		return false
	}
	return targets[to]
}

// IsTerminalStatus returns true if status cannot transition further.
func IsTerminalStatus(status string) bool {
	switch status {
	case StatusDelivered, StatusUndelivered, StatusExpired, StatusFailed, StatusRejected:
		return true
	default:
		return false
	}
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
	OriginalDestination  string           `json:"original_destination"`
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
	RequestFingerprint   *string          `json:"request_fingerprint,omitempty"`
	IngressChannel       string           `json:"ingress_channel"`
	IngressAccountID     *uuid.UUID       `json:"ingress_account_id,omitempty"`
	DLRRequested         bool             `json:"dlr_requested"`
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
	ID                uuid.UUID        `json:"id"`
	MessageID         uuid.UUID        `json:"message_id"`
	MessageCreatedAt  time.Time        `json:"message_created_at"`
	AttemptNumber     int              `json:"attempt_number"`
	ConnectionID      uuid.UUID        `json:"connection_id"`
	Status            string           `json:"status"` // submitting|accepted|retryable_reject|fatal_reject|unknown
	ProviderMessageID *string          `json:"provider_message_id,omitempty"`
	ErrorCode         *string          `json:"error_code,omitempty"`
	ErrorMessage      *string          `json:"error_message,omitempty"`
	BuyCost           *decimal.Decimal `json:"buy_cost,omitempty"`
	BuyCurrency       *string          `json:"buy_currency,omitempty"`
	LatencyMs         *int             `json:"latency_ms,omitempty"`
	StartedAt         time.Time        `json:"started_at"`
	FinishedAt        *time.Time       `json:"finished_at,omitempty"`
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
	ID                uuid.UUID  `json:"id"`
	MessageID         *uuid.UUID `json:"message_id,omitempty"`
	MessageCreatedAt  *time.Time `json:"message_created_at,omitempty"`
	ConnectionID      *uuid.UUID `json:"connection_id,omitempty"`
	ProviderMessageID string     `json:"provider_message_id"`
	Status            string     `json:"status"`
	ErrorCode         *string    `json:"error_code,omitempty"`
	RawReceipt        *string    `json:"raw_receipt,omitempty"`
	ReceivedAt        time.Time  `json:"received_at"`
}

// SubmitRequest is the client submission DTO.
type SubmitRequest struct {
	Destination        string     `json:"destination"`
	SenderID           string     `json:"sender_id"`
	Text               string     `json:"text"`
	ClientReference    *string    `json:"client_reference,omitempty"`
	TrafficProfile     string     `json:"traffic_profile,omitempty"` // otp|transactional|promotional
	ValiditySeconds    *int       `json:"validity_seconds,omitempty"`
	IngressChannel     string     `json:"ingress_channel,omitempty"` // http|smpp|platform
	IngressAccountID   *uuid.UUID `json:"ingress_account_id,omitempty"`
	DLRRequested       bool       `json:"dlr_requested,omitempty"`
	RequestFingerprint string     `json:"request_fingerprint,omitempty"`
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
	ID                  uuid.UUID       `json:"id"`
	CreatedAt           time.Time       `json:"created_at"`
	CustomerOrgID       uuid.UUID       `json:"customer_org_id"`
	ClientReference     *string         `json:"client_reference,omitempty"`
	OriginalDestination string          `json:"original_destination"`
	Destination         string          `json:"destination"`
	SenderID            string          `json:"sender_id"`
	SenderIDType        string          `json:"sender_id_type"`
	Encoding            string          `json:"encoding"`
	Body                string          `json:"body,omitempty"` // Masked unless message.content.view held
	BodyLength          int             `json:"body_length"`
	TotalParts          int             `json:"total_parts"`
	Status              string          `json:"status"`
	FailureReason       *string         `json:"failure_reason,omitempty"`
	SellPrice           decimal.Decimal `json:"sell_price"`
	SellCurrency        string          `json:"sell_currency"`
	Attempts            int             `json:"attempts"`
	ExpiresAt           time.Time       `json:"expires_at"`
	SubmittedAt         *time.Time      `json:"submitted_at,omitempty"`
	DeliveredAt         *time.Time      `json:"delivered_at,omitempty"`

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
	MessageID              uuid.UUID       `json:"message_id"`
	MessageCreatedAt       time.Time       `json:"message_created_at"`
	CustomerOrgID          uuid.UUID       `json:"customer_org_id"`
	Destination            string          `json:"destination"`
	SenderID               string          `json:"sender_id"`
	SenderIDType           string          `json:"sender_id_type"`
	Encoding               string          `json:"encoding"`
	TotalParts             int             `json:"total_parts"`
	PartsAccepted          int             `json:"parts_accepted"`
	SellPrice              decimal.Decimal `json:"sell_price"`
	SellCurrency           string          `json:"sell_currency"`
	CandidateConnectionIDs []uuid.UUID     `json:"candidate_connection_ids"`
	CurrentConnectionID    uuid.UUID       `json:"current_connection_id"`
	FailedConnectionIDs    []uuid.UUID     `json:"failed_connection_ids,omitempty"`
	AttemptNumber          int             `json:"attempt_number"`
	MaxAttempts            int             `json:"max_attempts"`
	Body                   string          `json:"body"`
}

// DLRMapping represents a custom status override for an upstream SMSC.
type DLRMapping struct {
	ID           uuid.UUID  `json:"id"`
	ConnectionID uuid.UUID  `json:"connection_id"`
	UpstreamStat string     `json:"upstream_stat"`
	UpstreamErr  *string    `json:"upstream_err,omitempty"`
	MappedStatus string     `json:"mapped_status"`
	Description  *string    `json:"description,omitempty"`
	Version      int        `json:"version"`
	CreatedBy    *uuid.UUID `json:"created_by,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// UnmatchedDLR represents a DLR awaiting provider message ID resolution.
type UnmatchedDLR struct {
	ID                uuid.UUID  `json:"id"`
	ConnectionID      *uuid.UUID `json:"connection_id,omitempty"`
	ProviderMessageID string     `json:"provider_message_id"`
	Status            string     `json:"status"`
	ErrorCode         *string    `json:"error_code,omitempty"`
	RawReceipt        *string    `json:"raw_receipt,omitempty"`
	MatchStatus       string     `json:"match_status"` // unmatched|matched|flagged_unmatched
	RetryCount        int        `json:"retry_count"`
	LastRetryAt       *time.Time `json:"last_retry_at,omitempty"`
	ReceivedAt        time.Time  `json:"received_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// SettlementException represents a financial or reconciliation discrepancy.
type SettlementException struct {
	ID               uuid.UUID       `json:"id"`
	MessageID        uuid.UUID       `json:"message_id"`
	MessageCreatedAt time.Time       `json:"message_created_at"`
	CustomerOrgID    *uuid.UUID      `json:"customer_org_id,omitempty"`
	HoldID           *uuid.UUID      `json:"hold_id,omitempty"`
	ExceptionType    string          `json:"exception_type"`
	Severity         string          `json:"severity"` // info|warning|critical
	Details          json.RawMessage `json:"details"`
	Resolved         bool            `json:"resolved"`
	ResolvedBy       *uuid.UUID      `json:"resolved_by,omitempty"`
	ResolvedAt       *time.Time      `json:"resolved_at,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
}

// ReconciliationRun records the execution summary of a ledger-messaging check.
type ReconciliationRun struct {
	ID               uuid.UUID       `json:"id"`
	StartedAt        time.Time       `json:"started_at"`
	CompletedAt      *time.Time      `json:"completed_at,omitempty"`
	MessagesChecked  int             `json:"messages_checked"`
	DiscrepancyCount int             `json:"discrepancy_count"`
	SummaryJSON      json.RawMessage `json:"summary_json"`
	InitiatedBy      *uuid.UUID      `json:"initiated_by,omitempty"`
}

// ReconciliationReport holds the detailed output of an on-demand reconciliation.
type ReconciliationReport struct {
	MessagesChecked    int                      `json:"messages_checked"`
	TerminalCount      int                      `json:"terminal_count"`
	InFlightCount      int                      `json:"in_flight_count"`
	UnresolvedUnknown  int                      `json:"unresolved_unknown"`
	UnmatchedDLRsCount int                      `json:"unmatched_dlrs_count"`
	Discrepancies      []map[string]interface{} `json:"discrepancies"`
	DiscrepancyCount   int                      `json:"discrepancy_count"`
	LedgerMatches      bool                     `json:"ledger_matches"`
}

// QueueBacklogStats provides live metrics on the transactional outbox.
type QueueBacklogStats struct {
	PendingCount      int        `json:"pending_count"`
	SendingCount      int        `json:"sending_count"`
	OldestPendingTime *time.Time `json:"oldest_pending_time,omitempty"`
	OldestAgeSeconds  float64    `json:"oldest_age_seconds"`
}

// MessageDetailView contains full journey details for a single message.
type MessageDetailView struct {
	Message  MessageView      `json:"message"`
	Parts    []MessagePart    `json:"parts"`
	Attempts []MessageAttempt `json:"attempts"`
	Events   []MessageEvent   `json:"events"`
	DLRs     []MessageDLR     `json:"dlrs"`
	HoldInfo *HoldSummary     `json:"hold_info,omitempty"`
}

// HoldSummary summarizes the ledger hold associated with a message.
type HoldSummary struct {
	HoldID       uuid.UUID        `json:"hold_id"`
	WalletID     uuid.UUID        `json:"wallet_id"`
	OriginalHold decimal.Decimal  `json:"original_hold"`
	Captured     *decimal.Decimal `json:"captured,omitempty"`
	Status       string           `json:"status"` // active|captured|released|expired
	ExpiresAt    time.Time        `json:"expires_at"`
}

// TestSendRequest is the platform operator test dispatch DTO.
type TestSendRequest struct {
	CustomerOrgID   uuid.UUID `json:"customer_org_id"`
	Destination     string    `json:"destination"`
	SenderID        string    `json:"sender_id"`
	Text            string    `json:"text"`
	Encoding        string    `json:"encoding,omitempty"`
	ValiditySeconds *int      `json:"validity_seconds,omitempty"`
}

// MessageLocator stores unpartitioned locator entries for high-speed single-index lookups.
type MessageLocator struct {
	MessageID     uuid.UUID `json:"message_id"`
	CreatedAt     time.Time `json:"created_at"`
	CustomerOrgID uuid.UUID `json:"customer_org_id"`
}

// SMPPDLRQueueItem represents an outbound deliver_sm receipt queued for a customer SMPP session.
type SMPPDLRQueueItem struct {
	ID              uuid.UUID  `json:"id"`
	AccountID       uuid.UUID  `json:"account_id"`
	MessageID       uuid.UUID  `json:"message_id"`
	MessageRef      string     `json:"message_ref"`
	ClientReference *string    `json:"client_reference,omitempty"`
	Destination     string     `json:"destination"`
	SenderID        string     `json:"sender_id"`
	Status          string     `json:"status"`
	ErrorCode       string     `json:"error_code"`
	Payload         []byte     `json:"payload"`
	Attempts        int        `json:"attempts"`
	NextAttemptAt   time.Time  `json:"next_attempt_at"`
	LeasedUntil     *time.Time `json:"leased_until,omitempty"`
	LeasedBy        *string    `json:"leased_by,omitempty"`
	QueueStatus     string     `json:"queue_status"`
	ExpiresAt       time.Time  `json:"expires_at"`
	CreatedAt       time.Time  `json:"created_at"`
	DeliveredAt     *time.Time `json:"delivered_at,omitempty"`
}

// CustomerWebhook represents a customer's registered HTTP webhook endpoint.
type CustomerWebhook struct {
	ID                  uuid.UUID  `json:"id"`
	CustomerOrgID       uuid.UUID  `json:"customer_org_id"`
	URL                 string     `json:"url"`
	SecretEncrypted     string     `json:"-"`
	SecretKeyID         string     `json:"-"`
	Events              []string   `json:"events"`
	Status              string     `json:"status"` // active, disabled, disabled_auto
	ConsecutiveFailures int        `json:"consecutive_failures"`
	Version             int        `json:"version"`
	CreatedBy           *uuid.UUID `json:"created_by,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// CustomerWebhookDelivery represents an individual event queued for webhook delivery.
type CustomerWebhookDelivery struct {
	ID            uuid.UUID  `json:"id"`
	WebhookID     uuid.UUID  `json:"webhook_id"`
	CustomerOrgID uuid.UUID  `json:"customer_org_id"`
	EventID       string     `json:"event_id"`
	EventType     string     `json:"event_type"`
	Payload       []byte     `json:"payload"`
	Status        string     `json:"status"` // pending, success, failed
	Attempts      int        `json:"attempts"`
	MaxAttempts   int        `json:"max_attempts"`
	NextAttemptAt time.Time  `json:"next_attempt_at"`
	LeasedUntil   *time.Time `json:"leased_until,omitempty"`
	LeasedBy      *string    `json:"leased_by,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	DeliveredAt   *time.Time `json:"delivered_at,omitempty"`
}

// CustomerWebhookAttempt logs each HTTP dispatch attempt for a webhook delivery.
type CustomerWebhookAttempt struct {
	ID              uuid.UUID `json:"id"`
	DeliveryID      uuid.UUID `json:"delivery_id"`
	AttemptNumber   int       `json:"attempt_number"`
	ResponseCode    *int      `json:"response_code,omitempty"`
	LatencyMs       int       `json:"latency_ms"`
	ResponseSnippet *string   `json:"response_snippet,omitempty"`
	ErrorClass      *string   `json:"error_class,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// InboundNumber represents a platform-managed virtual number or shortcode assigned to a customer.
type InboundNumber struct {
	ID                   uuid.UUID  `json:"id"`
	NumberRaw            string     `json:"number_raw"`
	NumberE164           string     `json:"number_e164"`
	CountryID            *uuid.UUID `json:"country_id,omitempty"`
	ProviderConnectionID *uuid.UUID `json:"provider_connection_id,omitempty"`
	CustomerOrgID        *uuid.UUID `json:"customer_org_id,omitempty"`
	Type                 string     `json:"type"`   // longcode, shortcode
	Status               string     `json:"status"` // active, inactive
	Capabilities         []string   `json:"capabilities"`
	Version              int        `json:"version"`
	CreatedBy            *uuid.UUID `json:"created_by,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

// InboundMessage represents an incoming MO SMS received from an upstream carrier.
type InboundMessage struct {
	ID                   uuid.UUID  `json:"id"`
	CreatedAt            time.Time  `json:"created_at"`
	SourceRaw            string     `json:"source_raw"`
	SourceE164           string     `json:"source_e164"`
	DestinationRaw       string     `json:"destination_raw"`
	DestinationE164      string     `json:"destination_e164"`
	InboundNumberID      *uuid.UUID `json:"inbound_number_id,omitempty"`
	CustomerOrgID        *uuid.UUID `json:"customer_org_id,omitempty"`
	ProviderConnectionID *uuid.UUID `json:"provider_connection_id,omitempty"`
	ProviderMessageID    *string    `json:"provider_message_id,omitempty"`
	EncryptedBody        []byte     `json:"-"`
	KeyID                string     `json:"key_id"`
	Encoding             string     `json:"encoding"`
	ConcatRef            *int       `json:"concat_ref,omitempty"`
	ConcatPart           *int       `json:"concat_part,omitempty"`
	ConcatTotal          *int       `json:"concat_total,omitempty"`
	DedupeHash           string     `json:"dedupe_hash"`
	Status               string     `json:"status"` // received, routed, unrouted, delivered
}

// FinalStateParams supplies parameters for the single atomic final state transition function.
type FinalStateParams struct {
	MessageID        uuid.UUID
	MessageCreatedAt time.Time
	FromStatus       string
	ToStatus         string // StatusDelivered, StatusUndelivered, StatusFailed, StatusExpired, StatusRejected
	Reason           string
	ProviderMsgID    *string
	ConnectionID     *uuid.UUID
	BuyCost          *decimal.Decimal
	Details          map[string]interface{}
}
