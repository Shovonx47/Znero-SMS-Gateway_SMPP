package messaging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/internal/smpp/pdu"
)

// SMPPDeliverer delivers incoming MO messages to bound customer SMPP sessions.
type SMPPDeliverer interface {
	DeliverMO(ctx context.Context, customerOrgID uuid.UUID, sourceAddr, destAddr string, body []byte, isUCS2 bool) error
}

// InboundHandler coordinates ingestion, matching, persistence, and customer notification of MO SMS.
type InboundHandler struct {
	repo          Repository
	keyring       *secrets.KeyRing
	smppDeliverer SMPPDeliverer
	logger        *slog.Logger
}

// NewInboundHandler constructs a new InboundHandler.
func NewInboundHandler(
	repo Repository,
	keyring *secrets.KeyRing,
	smppDeliverer SMPPDeliverer,
	logger *slog.Logger,
) *InboundHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &InboundHandler{
		repo:          repo,
		keyring:       keyring,
		smppDeliverer: smppDeliverer,
		logger:        logger.With("module", "inbound_pipeline"),
	}
}

// ProcessInboundSM processes an incoming Mobile-Originated deliver_sm PDU from an upstream carrier.
func (h *InboundHandler) ProcessInboundSM(ctx context.Context, connectionID uuid.UUID, deliver *pdu.DeliverSM) error {
	// 1. Decode text
	var rawText string
	isUCS2 := deliver.DataCoding == 0x08
	if isUCS2 {
		var runes []rune
		for i := 0; i+1 < len(deliver.ShortMessage); i += 2 {
			runes = append(runes, rune(uint16(deliver.ShortMessage[i])<<8|uint16(deliver.ShortMessage[i+1])))
		}
		rawText = string(runes)
	} else if len(deliver.ShortMessage) > 0 {
		rawText = string(deliver.ShortMessage)
	} else if tlv := pdu.GetTLV(deliver.TLVs, pdu.TagMessagePayload); tlv != nil && len(tlv.Value) > 0 {
		rawText = string(tlv.Value)
	}

	// 2. Normalize numbers
	srcRaw := deliver.SourceAddr
	srcE164 := strings.TrimSpace(srcRaw)
	if srcE164 != "" && !strings.HasPrefix(srcE164, "+") {
		srcE164 = "+" + srcE164
	}

	destRaw := deliver.DestAddr
	destE164 := strings.TrimSpace(destRaw)
	if destE164 != "" && !strings.HasPrefix(destE164, "+") {
		destE164 = "+" + destE164
	}

	// 3. Match Inbound Number
	num, matchErr := h.repo.MatchInboundNumber(ctx, destE164)
	if matchErr != nil || num == nil {
		// Fallback match on raw format
		num, _ = h.repo.MatchInboundNumber(ctx, destRaw)
	}

	// 4. Encrypt message body at rest
	encBody := ""
	keyID := "v1"
	if h.keyring != nil && rawText != "" {
		if enc, err := h.keyring.Encrypt(rawText); err == nil {
			encBody = enc
			keyID = h.keyring.ActiveKeyID()
		} else {
			h.logger.Error("failed to encrypt inbound message body", "err", err)
		}
	}

	// 5. Deduplication Hash
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%s:%d", srcE164, destE164, rawText, time.Now().Unix()/60)))
	dedupeHash := hex.EncodeToString(hash[:])

	encoding := "GSM-7"
	if isUCS2 {
		encoding = "UCS-2"
	}

	status := "unmatched"
	var customerOrgID *uuid.UUID
	var inboundNumID *uuid.UUID

	if num != nil {
		inboundNumID = &num.ID
		customerOrgID = num.CustomerOrgID
		if customerOrgID != nil {
			status = "routed"
		} else {
			status = "unrouted"
		}
	}

	inboundMsg := &InboundMessage{
		ID:                   uuid.New(),
		CreatedAt:            time.Now().UTC(),
		SourceRaw:            srcRaw,
		SourceE164:           srcE164,
		DestinationRaw:       destRaw,
		DestinationE164:      destE164,
		InboundNumberID:      inboundNumID,
		CustomerOrgID:        customerOrgID,
		ProviderConnectionID: &connectionID,
		EncryptedBody:        []byte(encBody),
		KeyID:                keyID,
		Encoding:             encoding,
		DedupeHash:           dedupeHash,
		Status:               status,
	}

	if err := h.repo.RecordInboundMessage(ctx, inboundMsg); err != nil {
		h.logger.Error("failed to record inbound message", "err", err, "src", srcE164, "dest", destE164)
		return err
	}

	h.logger.Info("recorded inbound message",
		"id", inboundMsg.ID,
		"src", srcE164,
		"dest", destE164,
		"status", status,
	)

	// 6. Forward to customer if routed
	if customerOrgID != nil {
		// A. Queue Customer Webhook Delivery
		payload := map[string]interface{}{
			"event":       "message.inbound",
			"id":          inboundMsg.ID.String(),
			"from":        srcE164,
			"to":          destE164,
			"text":        rawText,
			"encoding":    encoding,
			"received_at": inboundMsg.CreatedAt.Format(time.RFC3339),
		}
		if qErr := h.repo.QueueInboundWebhook(ctx, *customerOrgID, payload); qErr != nil {
			h.logger.Warn("failed to queue inbound webhook delivery", "err", qErr, "customer_org_id", *customerOrgID)
		}

		// B. Forward to bound Customer SMPP session if connected
		if h.smppDeliverer != nil {
			if fwdErr := h.smppDeliverer.DeliverMO(ctx, *customerOrgID, srcE164, destE164, deliver.ShortMessage, isUCS2); fwdErr != nil {
				h.logger.Debug("smpp deliver mo not sent or no active bind", "customer_org_id", *customerOrgID, "err", fwdErr)
			}
		}
	}

	return nil
}
