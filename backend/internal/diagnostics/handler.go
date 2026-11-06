package diagnostics

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/gofiber/fiber/v2"

	"github.com/znero-international/znero/backend/pkg/httpx"
)

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Register mounts the development-only routes. The caller must only call this when
// APP_ENV=development; in every other environment the routes do not exist (default deny).
func (h *Handler) Register(v1 fiber.Router) {
	v1.Post("/dev/kafka/test-message", h.publish)
}

type publishRequest struct {
	Message string `json:"message"`
}

type publishResponse struct {
	Topic   string      `json:"topic"`
	Message TestMessage `json:"message"`
}

// publish writes one message to the Kafka test topic.
// TODO(security): unauthenticated development tool. Phase 1 has no authentication; before any
// non-development use this must require authentication and an explicit permission.
func (h *Handler) publish(c *fiber.Ctx) error {
	var req publishRequest
	dec := json.NewDecoder(bytes.NewReader(c.Body()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return httpx.BadRequest("Request body must be JSON like {\"message\": \"hello\"}.")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return httpx.BadRequest("Request body must contain a single JSON object.")
	}

	tm, err := h.svc.PublishTestMessage(c.UserContext(), req.Message)
	if err != nil {
		var vErr *ValidationError
		switch {
		case errors.As(err, &vErr):
			return httpx.ValidationFailed("The message is invalid.", map[string]string{"message": vErr.Reason})
		case errors.Is(err, ErrPublishFailed):
			return httpx.Unavailable("Kafka is unavailable; the message was not published.")
		default:
			return httpx.Internal(err)
		}
	}
	return httpx.Accepted(c, publishResponse{Topic: h.svc.Topic(), Message: tm})
}
