package customeredge

import (
	"errors"
	"net/http"

	"github.com/znero-international/znero/backend/internal/smpp/pdu"
)

// PublicEdgeError is the structured public error representation returned by HTTP APIs
// and mapped into SMPP command_status.
type PublicEdgeError struct {
	HTTPStatus int    `json:"-"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	SMPPStatus uint32 `json:"-"`
	RetryAfter int    `json:"-"` // Non-zero for 429 rate limit
}

func (e *PublicEdgeError) Error() string {
	return e.Message
}

// Pre-defined canonical edge errors
var (
	ErrUnauthorized = &PublicEdgeError{
		HTTPStatus: http.StatusUnauthorized,
		Code:       "unauthorized",
		Message:    "Invalid or revoked API key",
		SMPPStatus: pdu.StatusRInvPaswd,
	}

	ErrForbiddenSuspended = &PublicEdgeError{
		HTTPStatus: http.StatusForbidden,
		Code:       "account_suspended",
		Message:    "Account is suspended or inactive",
		SMPPStatus: pdu.StatusRInvBndSts,
	}

	ErrForbiddenIPNotAllowed = &PublicEdgeError{
		HTTPStatus: http.StatusForbidden,
		Code:       "ip_not_allowed",
		Message:    "Client IP address is not permitted for this credential",
		SMPPStatus: pdu.StatusRBindFail,
	}

	ErrMissingScope = &PublicEdgeError{
		HTTPStatus: http.StatusForbidden,
		Code:       "insufficient_scope",
		Message:    "API key does not possess the required permission scope",
		SMPPStatus: pdu.StatusRInvSysID,
	}

	ErrInsufficientFunds = &PublicEdgeError{
		HTTPStatus: http.StatusPaymentRequired,
		Code:       "insufficient_funds",
		Message:    "Insufficient prepaid balance or credit limit exceeded",
		SMPPStatus: pdu.StatusRSysErr,
	}

	ErrReferenceConflict = &PublicEdgeError{
		HTTPStatus: http.StatusConflict,
		Code:       "reference_conflict",
		Message:    "Client reference has already been used with different message parameters",
		SMPPStatus: pdu.StatusRAlyBnd,
	}

	ErrPayloadTooLarge = &PublicEdgeError{
		HTTPStatus: http.StatusRequestEntityTooLarge,
		Code:       "payload_too_large",
		Message:    "Request payload exceeds allowed limit",
		SMPPStatus: pdu.StatusRInvMsgLen,
	}

	ErrRateLimitExceeded = &PublicEdgeError{
		HTTPStatus: http.StatusTooManyRequests,
		Code:       "rate_limit_exceeded",
		Message:    "Throughput limit exceeded; please slow down requests",
		SMPPStatus: pdu.StatusRThrottled,
		RetryAfter: 1,
	}

	ErrDailyCapExceeded = &PublicEdgeError{
		HTTPStatus: http.StatusUnprocessableEntity,
		Code:       "daily_cap_exceeded",
		Message:    "Daily message submission quota exceeded",
		SMPPStatus: pdu.StatusRThrottled,
	}

	ErrMonthlyCapExceeded = &PublicEdgeError{
		HTTPStatus: http.StatusUnprocessableEntity,
		Code:       "monthly_cap_exceeded",
		Message:    "Monthly message submission quota exceeded",
		SMPPStatus: pdu.StatusRThrottled,
	}

	ErrInvalidDestination = &PublicEdgeError{
		HTTPStatus: http.StatusUnprocessableEntity,
		Code:       "invalid_destination",
		Message:    "Destination address must be a valid international E.164 phone number",
		SMPPStatus: pdu.StatusRInvDstAdr,
	}

	ErrSenderIDRejected = &PublicEdgeError{
		HTTPStatus: http.StatusUnprocessableEntity,
		Code:       "sender_id_not_approved",
		Message:    "The specified sender ID is not approved or permitted for this account",
		SMPPStatus: pdu.StatusRInvSrcAdr,
	}

	ErrDestinationBarred = &PublicEdgeError{
		HTTPStatus: http.StatusUnprocessableEntity,
		Code:       "destination_barred",
		Message:    "Destination country or prefix is blocked by commercial policy",
		SMPPStatus: pdu.StatusRInvDstAdr,
	}

	ErrNoRouteAvailable = &PublicEdgeError{
		HTTPStatus: http.StatusUnprocessableEntity,
		Code:       "route_unavailable",
		Message:    "No active route or rate available for the specified destination",
		SMPPStatus: pdu.StatusRSysErr,
	}

	ErrMessageNotFound = &PublicEdgeError{
		HTTPStatus: http.StatusNotFound,
		Code:       "message_not_found",
		Message:    "The requested message does not exist or does not belong to this account",
		SMPPStatus: pdu.StatusRInvMsgID,
	}

	ErrServiceUnavailable = &PublicEdgeError{
		HTTPStatus: http.StatusServiceUnavailable,
		Code:       "service_unavailable",
		Message:    "System temporarily unavailable; please retry shortly",
		SMPPStatus: pdu.StatusRSysErr,
		RetryAfter: 5,
	}

	ErrValidation = &PublicEdgeError{
		HTTPStatus: http.StatusBadRequest,
		Code:       "validation_error",
		Message:    "Request parameter validation failed",
		SMPPStatus: pdu.StatusRInvMsgLen,
	}
)

// MapSubmitError converts internal messaging domain errors into safe PublicEdgeError representations.
func MapSubmitError(err error) *PublicEdgeError {
	if err == nil {
		return nil
	}

	var pubErr *PublicEdgeError
	if errors.As(err, &pubErr) {
		return pubErr
	}

	errStr := err.Error()

	switch {
	case errors.Is(err, ErrInvalidDestination) || contains(errStr, "invalid destination") || contains(errStr, "E.164"):
		return ErrInvalidDestination
	case contains(errStr, "insufficient funds") || contains(errStr, "insufficient balance") || contains(errStr, "overdraft"):
		return ErrInsufficientFunds
	case contains(errStr, "sender_id") || contains(errStr, "sender ID"):
		return ErrSenderIDRejected
	case contains(errStr, "barred") || contains(errStr, "blocked"):
		return ErrDestinationBarred
	case contains(errStr, "daily quota") || contains(errStr, "daily cap"):
		return ErrDailyCapExceeded
	case contains(errStr, "monthly quota") || contains(errStr, "monthly cap"):
		return ErrMonthlyCapExceeded
	case contains(errStr, "conflict") || contains(errStr, "fingerprint mismatch"):
		return ErrReferenceConflict
	case contains(errStr, "no route") || contains(errStr, "sell rate"):
		return ErrNoRouteAvailable
	case contains(errStr, "rate limit") || contains(errStr, "throttle"):
		return ErrRateLimitExceeded
	case contains(errStr, "database") || contains(errStr, "connection refused") || contains(errStr, "pool closed"):
		return ErrServiceUnavailable
	default:
		return &PublicEdgeError{
			HTTPStatus: http.StatusBadRequest,
			Code:       "submission_rejected",
			Message:    "Message submission could not be processed",
			SMPPStatus: pdu.StatusRSysErr,
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || len(needle) > 0 && (needle == haystack || findSubstr(haystack, needle)))
}

func findSubstr(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}
