package httpprovider

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/provider"
	"github.com/znero-international/znero/backend/pkg/httpx"
)

// CallbackHandler handles inbound delivery report callbacks from HTTP providers.
type CallbackHandler struct {
	client  *Client
	dlrProc *messaging.DLRProcessor
	logger  *slog.Logger
}

// NewCallbackHandler constructs the HTTP provider DLR callback handler.
func NewCallbackHandler(client *Client, dlrProc *messaging.DLRProcessor, logger *slog.Logger) *CallbackHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &CallbackHandler{
		client:  client,
		dlrProc: dlrProc,
		logger:  logger.With("module", "httpprovider_callback"),
	}
}

// Register mounts the provider callback webhook endpoints (protected per connection via optional HMAC / shared secret).
func (h *CallbackHandler) Register(v1 fiber.Router) {
	cb := v1.Group("/provider-callbacks")
	cb.Post("/dlr/:connectionId", h.handleDLR)
	cb.Get("/dlr/:connectionId", h.handleDLR)
}

func (h *CallbackHandler) handleDLR(c *fiber.Ctx) error {
	connID, err := uuid.Parse(c.Params("connectionId"))
	if err != nil {
		return httpx.BadRequest("Invalid connection ID")
	}

	ctx := c.UserContext()
	conn, err := h.client.loadConnection(ctx, connID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || strings.Contains(err.Error(), "not found") {
			return httpx.NotFound("Provider callback endpoint not found")
		}
		return httpx.Internal(err)
	}
	if conn.dlrMode != "callback" {
		return httpx.NotFound("Callback DLR mode is not enabled on this connection")
	}

	rawBody := c.Body()
	if len(rawBody) > 65536 {
		return httpx.BadRequest("Payload exceeds maximum callback size")
	}

	norm := conn.cfg.Normalized()
	if err := h.verifyCallbackSecret(ctx, c, conn.dlrSecretEnc, norm, rawBody); err != nil {
		h.logger.Warn("rejected HTTP provider DLR callback signature",
			"conn_id", connID,
			"err", err.Error(),
		)
		return httpx.Unauthorized("Invalid provider callback signature")
	}

	providerMsgID, rawStatus, errCode := extractCallbackFields(c, norm, rawBody)
	mappedStat := MapDLRStatus(rawStatus, norm.CallbackStatusMap)

	receipt := &messaging.DLRReceipt{
		ProviderMessageID: providerMsgID,
		Stat:              mappedStat,
		ErrCode:           errCode,
		RawReceipt:        truncateRawReceipt(string(rawBody), string(c.Request().URI().QueryString())),
	}

	if err := h.dlrProc.ProcessParsedDLR(ctx, connID, receipt); err != nil {
		if strings.TrimSpace(providerMsgID) == "" {
			return httpx.BadRequest("Missing provider message identifier in callback payload")
		}
		return httpx.Internal(err)
	}

	return httpx.OK(c, fiber.Map{
		"received":            true,
		"provider_message_id": providerMsgID,
		"status":              mappedStat,
	})
}

func (h *CallbackHandler) verifyCallbackSecret(_ context.Context, c *fiber.Ctx, secretEnc string, norm provider.HTTPConnectorConfig, rawBody []byte) error {
	algo := strings.ToLower(strings.TrimSpace(norm.CallbackSignatureAlgo))
	if algo == "none" {
		return nil
	}
	// Shared secret / HMAC is optional unless configured on the connection.
	if strings.TrimSpace(secretEnc) == "" {
		return nil
	}
	if h.client.decrypter == nil {
		return fmt.Errorf("decrypter unavailable for callback secret")
	}
	plainSecret, err := h.client.decrypter.Decrypt(secretEnc)
	if err != nil {
		return fmt.Errorf("decrypt callback secret: %w", err)
	}

	sigHeader := firstNonEmptyHeader(c, norm.CallbackSignatureHeader, "X-Provider-Signature", "X-Signature", "Authorization")
	if sigHeader == "" {
		sigHeader = c.Query("signature")
	}
	if sigHeader == "" {
		return fmt.Errorf("missing callback signature header")
	}

	switch algo {
	case "shared_secret":
		provided := strings.TrimPrefix(sigHeader, "Bearer ")
		provided = strings.TrimPrefix(provided, "bearer ")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(plainSecret)) != 1 {
			return fmt.Errorf("shared secret mismatch")
		}
		return nil
	default: // hmac_sha256
		payload := rawBody
		if len(payload) == 0 {
			payload = c.Request().URI().QueryString()
		}
		mac := hmac.New(sha256.New, []byte(plainSecret))
		_, _ = mac.Write(payload)
		expectedMAC := mac.Sum(nil)

		cleanSig := strings.TrimSpace(sigHeader)
		cleanSig = strings.TrimPrefix(cleanSig, "sha256=")
		cleanSig = strings.TrimPrefix(cleanSig, "SHA256=")
		providedMAC, err := hex.DecodeString(cleanSig)
		if err != nil {
			return fmt.Errorf("malformed HMAC hex signature")
		}
		if !hmac.Equal(providedMAC, expectedMAC) {
			return fmt.Errorf("HMAC signature mismatch")
		}
		return nil
	}
}

func firstNonEmptyHeader(c *fiber.Ctx, candidates ...string) string {
	for _, hdr := range candidates {
		if strings.TrimSpace(hdr) == "" {
			continue
		}
		if val := strings.TrimSpace(c.Get(hdr)); val != "" {
			return val
		}
	}
	return ""
}

func extractCallbackFields(c *fiber.Ctx, norm provider.HTTPConnectorConfig, rawBody []byte) (msgID, status, errCode string) {
	// 1. JSON body if valid JSON object
	if len(rawBody) > 0 && json.Valid(rawBody) {
		msgID, _ = ExtractJSONPath(rawBody, norm.CallbackMsgIDPath)
		status, _ = ExtractJSONPath(rawBody, norm.CallbackStatusPath)
		errCode, _ = ExtractJSONPath(rawBody, norm.CallbackErrorCodePath)
	}

	// 2. Form-urlencoded fallback if any field missing
	ct := strings.ToLower(c.Get("Content-Type"))
	if len(rawBody) > 0 && (strings.Contains(ct, "application/x-www-form-urlencoded") || (!json.Valid(rawBody) && strings.Contains(string(rawBody), "="))) {
		if vals, err := url.ParseQuery(string(rawBody)); err == nil {
			if msgID == "" {
				msgID = firstFormValue(vals, norm.CallbackMsgIDPath, "message_id", "id", "msg_id")
			}
			if status == "" {
				status = firstFormValue(vals, norm.CallbackStatusPath, "status", "stat", "state")
			}
			if errCode == "" {
				errCode = firstFormValue(vals, norm.CallbackErrorCodePath, "error_code", "err", "error")
			}
		}
	}

	// 3. Query params fallback (supports GET callbacks and query-augmented POSTs)
	if msgID == "" {
		msgID = firstQueryParam(c, norm.CallbackMsgIDPath, "message_id", "id", "msg_id")
	}
	if status == "" {
		status = firstQueryParam(c, norm.CallbackStatusPath, "status", "stat", "state")
	}
	if errCode == "" {
		errCode = firstQueryParam(c, norm.CallbackErrorCodePath, "error_code", "err", "error")
	}
	return strings.TrimSpace(msgID), strings.TrimSpace(status), strings.TrimSpace(errCode)
}

func firstFormValue(vals url.Values, keys ...string) string {
	for _, k := range keys {
		if k == "" {
			continue
		}
		if v := strings.TrimSpace(vals.Get(k)); v != "" {
			return v
		}
	}
	return ""
}

func firstQueryParam(c *fiber.Ctx, keys ...string) string {
	for _, k := range keys {
		if k == "" {
			continue
		}
		if v := strings.TrimSpace(c.Query(k)); v != "" {
			return v
		}
	}
	return ""
}

func truncateRawReceipt(body, query string) string {
	raw := body
	if raw == "" {
		raw = query
	}
	if len(raw) > 1024 {
		return raw[:1024]
	}
	return raw
}
