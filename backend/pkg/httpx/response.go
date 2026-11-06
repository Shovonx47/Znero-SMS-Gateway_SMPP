package httpx

import (
	"log/slog"
	"net/http"

	"github.com/gofiber/fiber/v2"

	"github.com/znero-international/znero/backend/pkg/logx"
)

// Meta is attached to every response, success or error.
type Meta struct {
	RequestID string `json:"request_id"`
}

// SuccessBody is the standard success envelope.
type SuccessBody struct {
	Success bool `json:"success"`
	Data    any  `json:"data"`
	Meta    Meta `json:"meta"`
}

// ErrorDetail is the payload of the standard error envelope.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// ErrorBody is the standard error envelope.
type ErrorBody struct {
	Success bool        `json:"success"`
	Error   ErrorDetail `json:"error"`
	Meta    Meta        `json:"meta"`
}

// RequestID returns the ID assigned to the current request ("" if the middleware is not installed).
func RequestID(c *fiber.Ctx) string {
	return logx.RequestID(c.UserContext())
}

// JSON writes the standard success envelope with the given status.
func JSON(c *fiber.Ctx, status int, data any) error {
	return c.Status(status).JSON(SuccessBody{Success: true, Data: data, Meta: Meta{RequestID: RequestID(c)}})
}

// OK writes a 200 success envelope.
func OK(c *fiber.Ctx, data any) error { return JSON(c, http.StatusOK, data) }

// Accepted writes a 202 success envelope.
func Accepted(c *fiber.Ctx, data any) error { return JSON(c, http.StatusAccepted, data) }

// WriteError writes the standard error envelope for appErr.
func WriteError(c *fiber.Ctx, appErr *AppError) error {
	return c.Status(appErr.Status).JSON(ErrorBody{
		Success: false,
		Error:   ErrorDetail{Code: appErr.Code, Message: appErr.Message, Details: appErr.Details},
		Meta:    Meta{RequestID: RequestID(c)},
	})
}

// ErrorHandler is the Fiber error handler: it turns every error into the standard error format
// and logs unexpected (5xx with an internal cause) failures with full detail server-side only.
func ErrorHandler(logger *slog.Logger) fiber.ErrorHandler {
	return func(c *fiber.Ctx, err error) error {
		appErr := toAppError(err)
		if appErr.Err != nil && appErr.Status >= http.StatusInternalServerError {
			logger.ErrorContext(c.UserContext(), "request failed",
				slog.String("code", appErr.Code),
				slog.String("error", appErr.Err.Error()),
			)
		}
		return WriteError(c, appErr)
	}
}
