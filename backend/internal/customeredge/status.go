package customeredge

// Customer-facing status strings
const (
	CustomerStatusQueued    = "queued"
	CustomerStatusSent      = "sent"
	CustomerStatusDelivered = "delivered"
	CustomerStatusFailed    = "failed"
	CustomerStatusExpired   = "expired"
)

// Coarse reason codes for customer-facing outputs
const (
	ReasonCodeDelivered   = "DELIVRD"
	ReasonCodeUndelivered = "UNDELIV"
	ReasonCodeExpired     = "EXPIRED"
	ReasonCodeDeleted     = "DELETED"
	ReasonCodeUnknown     = "UNKNOWN"
	ReasonCodeRejected    = "REJECTD"
	ReasonCodeAccepted    = "ACCEPTD"
)

// CustomerStatusInfo bundles customer-facing status representation.
type CustomerStatusInfo struct {
	Status     string `json:"status"`      // "queued", "sent", "delivered", "failed", "expired"
	ReasonCode string `json:"reason_code"` // Coarse standard code (e.g. DELIVRD, UNDELIV, EXPIRED, REJECTD)
	Message    string `json:"message"`     // Customer-safe description
}

// MapInternalToCustomerStatus converts internal messaging states into customer-safe public states.
// Under NO circumstances are provider error strings, buy rates, or internal routing states leaked.
func MapInternalToCustomerStatus(internalStatus string) CustomerStatusInfo {
	switch internalStatus {
	case "accepted", "queued", "submitting":
		return CustomerStatusInfo{
			Status:     CustomerStatusQueued,
			ReasonCode: ReasonCodeAccepted,
			Message:    "Message accepted for processing",
		}

	case "submitted", "unknown":
		// Ambiguous or submitted outcomes appear as "sent" to the customer
		return CustomerStatusInfo{
			Status:     CustomerStatusSent,
			ReasonCode: ReasonCodeAccepted,
			Message:    "Message transmitted upstream to operator network",
		}

	case "delivered":
		return CustomerStatusInfo{
			Status:     CustomerStatusDelivered,
			ReasonCode: ReasonCodeDelivered,
			Message:    "Message delivered successfully to destination handset",
		}

	case "undelivered":
		return CustomerStatusInfo{
			Status:     CustomerStatusFailed,
			ReasonCode: ReasonCodeUndelivered,
			Message:    "Message delivery failed upstream",
		}

	case "expired":
		return CustomerStatusInfo{
			Status:     CustomerStatusExpired,
			ReasonCode: ReasonCodeExpired,
			Message:    "Message validity period expired before delivery confirmation",
		}

	case "failed", "rejected":
		return CustomerStatusInfo{
			Status:     CustomerStatusFailed,
			ReasonCode: ReasonCodeRejected,
			Message:    "Message was rejected or could not be delivered",
		}

	default:
		return CustomerStatusInfo{
			Status:     CustomerStatusQueued,
			ReasonCode: ReasonCodeAccepted,
			Message:    "Message accepted for delivery",
		}
	}
}
