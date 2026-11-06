package httpx

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gofiber/fiber/v2"
)

// Stable, machine-readable error codes returned in the standard error format.
const (
	CodeBadRequest       = "bad_request"
	CodeValidationFailed = "validation_failed"
	CodeUnauthorized     = "unauthorized"
	CodeForbidden        = "forbidden"
	CodeNotFound         = "not_found"
	CodeMethodNotAllowed = "method_not_allowed"
	CodeConflict         = "conflict"
	CodePayloadTooLarge  = "payload_too_large"
	CodeRateLimited      = "rate_limited"
	CodeInternal         = "internal_error"
	CodeUnavailable      = "service_unavailable"
	CodeNotReady         = "not_ready"
)

// AppError is the only error type that is rendered to clients verbatim. Message and Details are
// public; Err is the internal cause, which is logged but never sent to the client.
type AppError struct {
	Status  int
	Code    string
	Message string
	Details any
	Err     error
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *AppError) Unwrap() error { return e.Err }

// NewError builds an AppError.
func NewError(status int, code, message string) *AppError {
	return &AppError{Status: status, Code: code, Message: message}
}

// WithDetails attaches public, structured details (e.g. per-field validation messages).
func (e *AppError) WithDetails(details any) *AppError {
	e.Details = details
	return e
}

// WithCause records the internal cause for logging. It is never serialised.
func (e *AppError) WithCause(err error) *AppError {
	e.Err = err
	return e
}

func BadRequest(message string) *AppError {
	return NewError(http.StatusBadRequest, CodeBadRequest, message)
}

func ValidationFailed(message string, details any) *AppError {
	return NewError(http.StatusUnprocessableEntity, CodeValidationFailed, message).WithDetails(details)
}

func Unauthorized(message string) *AppError {
	return NewError(http.StatusUnauthorized, CodeUnauthorized, message)
}

func Forbidden(message string) *AppError {
	return NewError(http.StatusForbidden, CodeForbidden, message)
}

func NotFound(message string) *AppError {
	return NewError(http.StatusNotFound, CodeNotFound, message)
}

func Conflict(message string) *AppError {
	return NewError(http.StatusConflict, CodeConflict, message)
}

func Unavailable(message string) *AppError {
	return NewError(http.StatusServiceUnavailable, CodeUnavailable, message)
}

// Internal wraps an unexpected failure. The client only ever sees a generic message.
func Internal(cause error) *AppError {
	return NewError(http.StatusInternalServerError, CodeInternal, "An internal error occurred.").WithCause(cause)
}

// toAppError normalises any error into an AppError with a client-safe message.
func toAppError(err error) *AppError {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr
	}
	var fiberErr *fiber.Error
	if errors.As(err, &fiberErr) {
		switch fiberErr.Code {
		case http.StatusNotFound:
			return NotFound("The requested resource was not found.")
		case http.StatusMethodNotAllowed:
			return NewError(fiberErr.Code, CodeMethodNotAllowed, "This method is not allowed for the resource.")
		case http.StatusRequestEntityTooLarge:
			return NewError(fiberErr.Code, CodePayloadTooLarge, "The request body is too large.")
		case http.StatusTooManyRequests:
			return NewError(fiberErr.Code, CodeRateLimited, "Too many requests. Please retry later.")
		case http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusUnsupportedMediaType, http.StatusRequestHeaderFieldsTooLarge:
			return NewError(fiberErr.Code, CodeBadRequest, "The request could not be processed.")
		default:
			if fiberErr.Code >= 500 {
				return Internal(err)
			}
			return NewError(fiberErr.Code, CodeBadRequest, "The request could not be processed.")
		}
	}
	return Internal(err)
}
