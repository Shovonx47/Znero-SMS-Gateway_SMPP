package smpp

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/smpp/pdu"
)

// Client implements the messaging.Sender interface over SMPP 3.4.
type Client struct {
	manager *ConnectionManager
	db      *pgxpool.Pool
	logger  *slog.Logger
}

// ClientConfig holds dependencies for the Client.
type ClientConfig struct {
	Manager *ConnectionManager
	DB      *pgxpool.Pool
	Logger  *slog.Logger
}

// NewClient creates a new SMPP Sender client.
func NewClient(cfg ClientConfig) *Client {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Client{
		manager: cfg.Manager,
		db:      cfg.DB,
		logger:  cfg.Logger,
	}
}

// Send fulfills the messaging.Sender interface for upstream carrier submission.
func (c *Client) Send(ctx context.Context, attempt messaging.SendAttempt) (messaging.SendResult, error) {
	start := time.Now()
	maskedDest := MaskPhoneNumber(attempt.Destination)

	c.logger.Debug("initiating smpp send attempt",
		slog.String("message_id", attempt.MessageID.String()),
		slog.String("connection_id", attempt.ConnectionID.String()),
		slog.String("destination", maskedDest),
		slog.Int("part_number", attempt.PartNumber),
		slog.Int("total_parts", attempt.TotalParts),
	)

	// 1. Obtain active, bound session from connection manager
	session, err := c.manager.GetSession(ctx, attempt.ConnectionID)
	if err != nil {
		latency := time.Since(start)
		c.logger.Warn("failed to obtain smpp session",
			slog.String("connection_id", attempt.ConnectionID.String()),
			slog.String("destination", maskedDest),
			slog.String("error", err.Error()),
		)
		return messaging.SendResult{
			Status:       messaging.SendStatusRetryableReject,
			ErrorCode:    "SESSION_UNAVAILABLE",
			ErrorMessage: err.Error(),
			Latency:      latency,
		}, nil
	}

	// 2. Build SubmitSM PDU
	submitPDU := pdu.NewSubmitSM(0) // sequence number will be assigned by session
	submitPDU.DestAddr = attempt.Destination
	submitPDU.SourceAddr = attempt.SenderID
	submitPDU.RegisteredDelivery = pdu.RegDeliverySMSCDeliveryReceipt

	// Set TON / NPI defaults
	if attempt.SenderIDType == "alphanumeric" {
		submitPDU.SourceAddrTON = pdu.TONAlphanumeric
		submitPDU.SourceAddrNPI = pdu.NPIUnknown
	} else {
		submitPDU.SourceAddrTON = pdu.TONInternational
		submitPDU.SourceAddrNPI = pdu.NPIISDN
	}
	submitPDU.DestAddrTON = pdu.TONInternational
	submitPDU.DestAddrNPI = pdu.NPIISDN

	// Set Data Coding Scheme
	if strings.EqualFold(attempt.Encoding, "ucs2") || strings.EqualFold(attempt.Encoding, "utf-16") {
		submitPDU.DataCoding = pdu.DataCodingUCS2
	} else {
		submitPDU.DataCoding = pdu.DataCodingSMSCDefault // GSM-7
	}

	// Wire payload encoding and UDH concatenation
	var payloadBytes []byte
	if len(attempt.UDH) > 0 {
		submitPDU.ESMClass = pdu.ESMClassUDHIIndicator
		// Prepend UDH bytes to short message
		payloadBytes = append(payloadBytes, attempt.UDH...)
	}

	// Convert text to wire bytes
	if submitPDU.DataCoding == pdu.DataCodingUCS2 {
		payloadBytes = append(payloadBytes, encodeUCS2(attempt.Text)...)
	} else {
		payloadBytes = append(payloadBytes, []byte(attempt.Text)...)
	}

	if len(payloadBytes) > 254 {
		// Use message_payload TLV for large segments
		submitPDU.TLVs = append(submitPDU.TLVs, pdu.TLV{
			Tag:    pdu.TagMessagePayload,
			Length: uint16(len(payloadBytes)),
			Value:  payloadBytes,
		})
	} else {
		submitPDU.ShortMessage = payloadBytes
	}

	// 3. Submit PDU via session with timeout boundary
	submitCtx := ctx
	if attempt.Timeout > 0 {
		var cancel context.CancelFunc
		submitCtx, cancel = context.WithTimeout(ctx, attempt.Timeout)
		defer cancel()
	}

	resp, err := session.Submit(submitCtx, submitPDU)
	latency := time.Since(start)

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			// CRITICAL OUTCOME RULE: Timeout after PDU was written is UNKNOWN and is NEVER resent or failed over!
			c.logger.Warn("smpp submission timed out - marked UNKNOWN with no resend",
				slog.String("message_id", attempt.MessageID.String()),
				slog.String("connection_id", attempt.ConnectionID.String()),
				slog.String("destination", maskedDest),
				slog.Duration("latency", latency),
			)
			return messaging.SendResult{
				Status:       messaging.SendStatusUnknown,
				ErrorCode:    "SUBMIT_TIMEOUT_UNKNOWN",
				ErrorMessage: "Carrier submission timed out with unconfirmed outcome; hold retained pending reconciliation",
				Latency:      latency,
			}, nil
		}

		c.logger.Warn("smpp submission network error",
			slog.String("message_id", attempt.MessageID.String()),
			slog.String("connection_id", attempt.ConnectionID.String()),
			slog.String("destination", maskedDest),
			slog.String("error", err.Error()),
		)
		return messaging.SendResult{
			Status:       messaging.SendStatusRetryableReject,
			ErrorCode:    "NETWORK_ERROR",
			ErrorMessage: err.Error(),
			Latency:      latency,
		}, nil
	}

	// 4. Classify carrier response status code
	sendStatus := ClassifyStatus(resp.Header.CommandStatus)
	errCodeName := StatusString(resp.Header.CommandStatus)

	c.logger.Info("smpp submission answered by upstream carrier",
		slog.String("message_id", attempt.MessageID.String()),
		slog.String("connection_id", attempt.ConnectionID.String()),
		slog.String("destination", maskedDest),
		slog.String("status", string(sendStatus)),
		slog.String("smpp_status", errCodeName),
		slog.String("provider_message_id", resp.MessageID),
		slog.Duration("latency", latency),
	)

	// 5. If accepted, record provider message ID in lookup table
	if sendStatus == messaging.SendStatusAccepted && resp.MessageID != "" && c.db != nil {
		_ = c.recordProviderLookup(ctx, attempt.ConnectionID, resp.MessageID, attempt.MessageID)
	}

	return messaging.SendResult{
		Status:            sendStatus,
		ProviderMessageID: resp.MessageID,
		ErrorCode:         errCodeName,
		Latency:           latency,
	}, nil
}

// HandleDLR persists a raw deliver_sm receipt into message_dlrs BEFORE sending deliver_sm_resp OK.
func (c *Client) HandleDLR(ctx context.Context, connectionID uuid.UUID, deliver *pdu.DeliverSM) error {
	if c.db == nil {
		return nil
	}

	rawText := string(deliver.ShortMessage)

	// Extract provider message ID from TLV or parse from receipt text
	var providerMsgID string
	if tlv := pdu.GetTLV(deliver.TLVs, pdu.TagReceiptedMessageId); tlv != nil && len(tlv.Value) > 0 {
		providerMsgID = string(tlv.Value)
	} else {
		providerMsgID = parseReceiptField(rawText, "id:")
	}

	stat := parseReceiptField(rawText, "stat:")
	if stat == "" {
		stat = "UNKNOWN"
	}
	errCode := parseReceiptField(rawText, "err:")

	query := `
		INSERT INTO message_dlrs (connection_id, provider_message_id, status, error_code, raw_receipt, received_at)
		VALUES ($1, $2, $3, $4, $5, now())
	`
	_, err := c.db.Exec(ctx, query, connectionID, providerMsgID, stat, errCode, rawText)
	if err != nil {
		c.logger.Error("failed to persist raw deliver_sm receipt into message_dlrs",
			slog.String("connection_id", connectionID.String()),
			slog.String("provider_message_id", providerMsgID),
			slog.String("error", err.Error()),
		)
		return err
	}

	c.logger.Info("persisted raw deliver_sm receipt",
		slog.String("connection_id", connectionID.String()),
		slog.String("provider_message_id", providerMsgID),
		slog.String("dlr_status", stat),
	)
	return nil
}

func (c *Client) recordProviderLookup(ctx context.Context, connID uuid.UUID, providerMsgID string, msgID uuid.UUID) error {
	// Look up message created_at
	var createdAt time.Time
	err := c.db.QueryRow(ctx, "SELECT created_at FROM messages WHERE id = $1", msgID).Scan(&createdAt)
	if err != nil {
		return err
	}

	query := `
		INSERT INTO message_provider_lookups (connection_id, provider_message_id, message_id, message_created_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (connection_id, provider_message_id) DO NOTHING
	`
	_, err = c.db.Exec(ctx, query, connID, providerMsgID, msgID, createdAt)
	return err
}

func encodeUCS2(s string) []byte {
	runes := []rune(s)
	buf := make([]byte, len(runes)*2)
	for i, r := range runes {
		buf[i*2] = byte(r >> 8)
		buf[i*2+1] = byte(r & 0xFF)
	}
	return buf
}

func parseReceiptField(receipt, fieldKey string) string {
	idx := strings.Index(receipt, fieldKey)
	if idx == -1 {
		return ""
	}
	val := receipt[idx+len(fieldKey):]
	endIdx := strings.IndexAny(val, " \t\r\n")
	if endIdx != -1 {
		val = val[:endIdx]
	}
	return strings.TrimSpace(val)
}
