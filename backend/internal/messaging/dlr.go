package messaging

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/smpp/pdu"
)

var (
	dlrIDRegex      = regexp.MustCompile(`(?i)\bid:([^ \t\r\n;]+)`)
	dlrSubRegex     = regexp.MustCompile(`(?i)\bsub:([^ \t\r\n;]+)`)
	dlrDlvrdRegex   = regexp.MustCompile(`(?i)\bdlvrd:([^ \t\r\n;]+)`)
	dlrSubmitDateRe = regexp.MustCompile(`(?i)\bsubmit date:([^ \t\r\n;]+)`)
	dlrDoneDateRe   = regexp.MustCompile(`(?i)\bdone date:([^ \t\r\n;]+)`)
	dlrStatRegex    = regexp.MustCompile(`(?i)\bstat:([^ \t\r\n;]+)`)
	dlrErrRegex     = regexp.MustCompile(`(?i)\berr:([^ \t\r\n;]+)`)
)

// DLRReceipt contains parsed attributes from a delivery receipt.
type DLRReceipt struct {
	ProviderMessageID string
	SubCount          string
	DlvrdCount        string
	SubmitDate        string
	DoneDate          string
	Stat              string
	ErrCode           string
	MessageState      *byte
	RawReceipt        string
}

// ParseDLRReceipt parses both the text receipt format (Appendix B) and receipted_message_id/message_state TLVs.
func ParseDLRReceipt(shortMsg []byte, tlvs []pdu.TLV) (*DLRReceipt, error) {
	receipt := &DLRReceipt{
		RawReceipt: string(shortMsg),
	}

	// 1. Parse TLVs if present
	for _, tlv := range tlvs {
		if tlv.Tag == pdu.TagReceiptedMessageId {
			// Zero-terminated string or raw bytes
			val := string(tlv.Value)
			val = strings.TrimRight(val, "\x00")
			if val != "" {
				receipt.ProviderMessageID = val
			}
		} else if tlv.Tag == pdu.TagMessageState && len(tlv.Value) >= 1 {
			st := tlv.Value[0]
			receipt.MessageState = &st
		}
	}

	// 2. Parse text receipt body
	text := receipt.RawReceipt
	if m := dlrIDRegex.FindStringSubmatch(text); len(m) > 1 && receipt.ProviderMessageID == "" {
		receipt.ProviderMessageID = strings.TrimSpace(m[1])
	}
	if m := dlrSubRegex.FindStringSubmatch(text); len(m) > 1 {
		receipt.SubCount = strings.TrimSpace(m[1])
	}
	if m := dlrDlvrdRegex.FindStringSubmatch(text); len(m) > 1 {
		receipt.DlvrdCount = strings.TrimSpace(m[1])
	}
	if m := dlrSubmitDateRe.FindStringSubmatch(text); len(m) > 1 {
		receipt.SubmitDate = strings.TrimSpace(m[1])
	}
	if m := dlrDoneDateRe.FindStringSubmatch(text); len(m) > 1 {
		receipt.DoneDate = strings.TrimSpace(m[1])
	}
	if m := dlrStatRegex.FindStringSubmatch(text); len(m) > 1 {
		receipt.Stat = strings.ToUpper(strings.TrimSpace(m[1]))
	}
	if m := dlrErrRegex.FindStringSubmatch(text); len(m) > 1 {
		receipt.ErrCode = strings.TrimSpace(m[1])
	}

	// Map TLV message state if stat wasn't in text
	if receipt.Stat == "" && receipt.MessageState != nil {
		switch *receipt.MessageState {
		case 2:
			receipt.Stat = "DELIVRD"
		case 3:
			receipt.Stat = "EXPIRED"
		case 5:
			receipt.Stat = "UNDELIV"
		case 8:
			receipt.Stat = "REJECTD"
		case 6:
			receipt.Stat = "ACCEPTD"
		default:
			receipt.Stat = "UNKNOWN"
		}
	}

	if receipt.ProviderMessageID == "" {
		return receipt, fmt.Errorf("could not extract provider_message_id from DLR (text or TLV)")
	}

	return receipt, nil
}

// CandidateProviderMessageIDs returns variants for format normalization (hex vs decimal, leading zeros).
func CandidateProviderMessageIDs(rawID string) []string {
	var candidates []string
	clean := strings.TrimSpace(rawID)
	if clean == "" {
		return candidates
	}

	// 1. Exact match
	candidates = append(candidates, clean)

	// 2. Stripped leading zeros
	stripped := strings.TrimLeft(clean, "0")
	if stripped != "" && stripped != clean {
		candidates = append(candidates, stripped)
	}

	// 3. Decimal to hex
	if decVal, err := strconv.ParseUint(clean, 10, 64); err == nil {
		hexStr := fmt.Sprintf("%X", decVal)
		candidates = append(candidates, hexStr)
		candidates = append(candidates, strings.ToLower(hexStr))
	}

	// 4. Hex to decimal
	hexClean := strings.TrimPrefix(strings.TrimPrefix(clean, "0x"), "0X")
	if hexVal, err := strconv.ParseUint(hexClean, 16, 64); err == nil {
		decStr := fmt.Sprintf("%d", hexVal)
		candidates = append(candidates, decStr)
	}

	// 5. Hex decode raw bytes if it was ASCII hex representation
	if len(hexClean)%2 == 0 && len(hexClean) > 2 {
		if b, err := hex.DecodeString(hexClean); err == nil && len(b) > 0 {
			candidates = append(candidates, string(b))
		}
	}

	return candidates
}

// DLRProcessor coordinates delivery receipt parsing, matching, settlement, and lifecycle events.
type DLRProcessor struct {
	repo           Repository
	ledgerSvc      *ledger.Service
	billingTrig    string // "on_delivery" (default) or "on_provider_accept"
	logger         *slog.Logger
	inboundHandler *InboundHandler
}

// NewDLRProcessor creates a new DLRProcessor.
func NewDLRProcessor(repo Repository, ledgerSvc *ledger.Service, billingTrig string, logger *slog.Logger) *DLRProcessor {
	if logger == nil {
		logger = slog.Default()
	}
	if billingTrig == "" {
		billingTrig = "on_delivery"
	}
	return &DLRProcessor{
		repo:        repo,
		ledgerSvc:   ledgerSvc,
		billingTrig: billingTrig,
		logger:      logger,
	}
}

// SetInboundHandler registers the MO inbound SMS handler.
func (p *DLRProcessor) SetInboundHandler(h *InboundHandler) {
	p.inboundHandler = h
}

// ProcessDeliverSM handles an inbound deliver_sm PDU from an upstream SMPP connection.
func (p *DLRProcessor) ProcessDeliverSM(ctx context.Context, connectionID uuid.UUID, deliver *pdu.DeliverSM) error {
	// If bit 2 (0x04) of esm_class is NOT set, this is an Inbound (MO) SMS, not a DLR receipt!
	if deliver.ESMClass&0x04 == 0 {
		if p.inboundHandler != nil {
			return p.inboundHandler.ProcessInboundSM(ctx, connectionID, deliver)
		}
		p.logger.Info("received MO deliver_sm with no inbound handler configured", "from", deliver.SourceAddr, "to", deliver.DestAddr)
		return nil
	}

	receipt, err := ParseDLRReceipt(deliver.ShortMessage, deliver.TLVs)
	rawText := string(deliver.ShortMessage)

	if err != nil {
		p.logger.Warn("could not parse DLR receipt, storing raw in unmatched buffer",
			"conn_id", connectionID,
			"raw", rawText,
			"err", err,
		)
		// Store raw DLR durably
		_ = p.repo.RecordRawDLR(ctx, nil, MessageDLR{
			ConnectionID:      &connectionID,
			ProviderMessageID: "UNKNOWN",
			Status:            "unparsed",
			RawReceipt:        &rawText,
			ReceivedAt:        time.Now().UTC(),
		})
		if insErr := p.repo.InsertUnmatchedDLR(ctx, nil, UnmatchedDLR{
			ConnectionID:      &connectionID,
			ProviderMessageID: "UNKNOWN",
			Status:            "unparsed",
			RawReceipt:        &rawText,
			MatchStatus:       "unmatched",
		}); insErr != nil {
			p.logger.Error("insert unparsed unmatched DLR failed", "conn_id", connectionID, "err", insErr)
		}
		return nil
	}

	// 1-4: Match, store raw receipt, buffer if unmatched, and apply state transition.
	return p.ProcessParsedDLR(ctx, connectionID, receipt)
}

// ProcessParsedDLR processes a pre-parsed delivery receipt (from SMPP DeliverSM, HTTP DLR callback, or HTTP DLR poller),
// persists the raw DLR row, buffers unmatched receipts, and drives settlement/customer notifications.
func (p *DLRProcessor) ProcessParsedDLR(ctx context.Context, connectionID uuid.UUID, receipt *DLRReceipt) error {
	if receipt == nil || strings.TrimSpace(receipt.ProviderMessageID) == "" {
		rawText := ""
		if receipt != nil {
			rawText = receipt.RawReceipt
		}
		_ = p.repo.RecordRawDLR(ctx, nil, MessageDLR{
			ConnectionID:      &connectionID,
			ProviderMessageID: "UNKNOWN",
			Status:            "unparsed",
			RawReceipt:        &rawText,
			ReceivedAt:        time.Now().UTC(),
		})
		if insErr := p.repo.InsertUnmatchedDLR(ctx, nil, UnmatchedDLR{
			ConnectionID:      &connectionID,
			ProviderMessageID: "UNKNOWN",
			Status:            "unparsed",
			RawReceipt:        &rawText,
			MatchStatus:       "unmatched",
		}); insErr != nil {
			p.logger.Error("insert unparsed unmatched DLR failed", "conn_id", connectionID, "err", insErr)
		}
		return fmt.Errorf("missing provider_message_id in DLR receipt")
	}

	// 1. Try finding message via provider lookups using candidate normalizations.
	// Immediate DLRs can race slightly ahead of provider_lookups persistence; retry briefly before buffering unmatched.
	candidates := CandidateProviderMessageIDs(receipt.ProviderMessageID)
	var matchedMsgID *uuid.UUID
	var matchedCreatedAt *time.Time

	for attempt := 0; attempt < 8 && matchedMsgID == nil; attempt++ {
		for _, cand := range candidates {
			msgID, createdAt, lookupErr := p.repo.GetProviderLookup(ctx, connectionID, cand)
			if lookupErr == nil && msgID != nil {
				matchedMsgID = msgID
				matchedCreatedAt = createdAt
				break
			}
		}
		if matchedMsgID != nil || !waitDLRRetryDelay(ctx, 15*time.Millisecond) {
			break
		}
	}

	// 2. Defer raw DLR persistence into the atomic state-transition tx on matched valid paths
	// to halve PostgreSQL WAL fsync commits per DLR; fall back to standalone durable write if unmatched/conflict/invalid.
	rawDLR := MessageDLR{
		MessageID:         matchedMsgID,
		MessageCreatedAt:  matchedCreatedAt,
		ConnectionID:      &connectionID,
		ProviderMessageID: receipt.ProviderMessageID,
		Status:            receipt.Stat,
		ErrorCode:         &receipt.ErrCode,
		RawReceipt:        &receipt.RawReceipt,
		ReceivedAt:        time.Now().UTC(),
	}

	// 3. If unmatched, store raw DLR durably and buffer for retry window
	if matchedMsgID == nil {
		if err := p.repo.RecordRawDLR(ctx, nil, rawDLR); err != nil {
			p.logger.Error("durable storage of raw DLR failed", "err", err)
		}
		p.logger.Info("DLR provider message ID unmatched; buffering in unmatched_dlrs",
			"conn_id", connectionID,
			"prov_msg_id", receipt.ProviderMessageID,
			"stat", receipt.Stat,
		)
		if insErr := p.repo.InsertUnmatchedDLR(ctx, nil, UnmatchedDLR{
			ConnectionID:      &connectionID,
			ProviderMessageID: receipt.ProviderMessageID,
			Status:            receipt.Stat,
			ErrorCode:         &receipt.ErrCode,
			RawReceipt:        &receipt.RawReceipt,
			MatchStatus:       "unmatched",
		}); insErr != nil {
			p.logger.Error("insert unmatched DLR failed", "conn_id", connectionID, "prov_msg_id", receipt.ProviderMessageID, "err", insErr)
		}
		return nil
	}

	// 4. Message matched: apply state transition, raw DLR storage, and settlement atomically
	return p.applyDLRTransitionWithRaw(ctx, connectionID, *matchedMsgID, *matchedCreatedAt, receipt, &rawDLR)
}

func waitDLRRetryDelay(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (p *DLRProcessor) applyDLRTransition(
	ctx context.Context,
	connID uuid.UUID,
	msgID uuid.UUID,
	createdAt time.Time,
	receipt *DLRReceipt,
) error {
	return p.applyDLRTransitionWithRaw(ctx, connID, msgID, createdAt, receipt, nil)
}

func (p *DLRProcessor) applyDLRTransitionWithRaw(
	ctx context.Context,
	connID uuid.UUID,
	msgID uuid.UUID,
	createdAt time.Time,
	receipt *DLRReceipt,
	rawDLR *MessageDLR,
) error {
	saveRawStandalone := func() {
		if rawDLR != nil {
			if rErr := p.repo.RecordRawDLR(ctx, nil, *rawDLR); rErr != nil {
				p.logger.Error("durable storage of raw DLR failed", "err", rErr)
			}
		}
	}

	msg, err := p.repo.GetMessageHeader(ctx, msgID, createdAt)
	if err != nil {
		saveRawStandalone()
		return fmt.Errorf("fetch matched message %s: %w", msgID, err)
	}
	if msg == nil {
		saveRawStandalone()
		return fmt.Errorf("message %s not found", msgID)
	}

	// Handle immediate-DLR carriers racing ahead of Dispatcher's StatusAccepted->StatusSubmitted commit:
	// Wait briefly for in-flight dispatch tx to commit; if still pre-submitted, buffer into unmatched_dlrs for Reaper.
	for attempt := 0; attempt < 8 && (msg.Status == StatusAccepted || msg.Status == StatusQueued || msg.Status == StatusSubmitting); attempt++ {
		if !waitDLRRetryDelay(ctx, 15*time.Millisecond) {
			break
		}
		if refreshed, rErr := p.repo.GetMessageHeader(ctx, msgID, createdAt); rErr == nil && refreshed != nil {
			msg = refreshed
		}
	}

	// Determine mapped status: check connection overrides first, then fallback to standard map
	mappedStatus := p.resolveMappedStatus(ctx, connID, receipt.Stat, receipt.ErrCode)

	// MONOTONIC STATE GUARANTEE: Final states can never be overwritten
	switch msg.Status {
	case StatusDelivered, StatusUndelivered, StatusExpired, StatusFailed, StatusRejected:
		saveRawStandalone()
		if msg.Status != mappedStatus {
			p.logger.Warn("conflicting DLR received for already finalized message; retaining final state",
				"msg_id", msgID,
				"current_status", msg.Status,
				"dlr_mapped_status", mappedStatus,
				"receipt_stat", receipt.Stat,
			)
			// Record settlement exception flag for auditing
			details, _ := json.Marshal(map[string]interface{}{
				"current_status":    msg.Status,
				"conflicting_stat":  receipt.Stat,
				"conflicting_error": receipt.ErrCode,
				"mapped_status":     mappedStatus,
				"raw_receipt":       receipt.RawReceipt,
			})
			_ = p.repo.RecordSettlementException(ctx, SettlementException{
				MessageID:        msgID,
				MessageCreatedAt: createdAt,
				CustomerOrgID:    &msg.CustomerOrgID,
				HoldID:           msg.HoldID,
				ExceptionType:    "dlr_conflict",
				Severity:         "warning",
				Details:          details,
			})
		}
		// Monotonic: do not overwrite
		return nil
	}

	// Check valid transition from current state (e.g. submitted -> delivered, unknown -> delivered)
	if !validTransitions[msg.Status][mappedStatus] {
		saveRawStandalone()
		if msg.Status == StatusAccepted || msg.Status == StatusQueued || msg.Status == StatusSubmitting {
			p.logger.Info("DLR arrived before dispatcher finished committing submitted state; buffering for reaper retry",
				"msg_id", msgID,
				"from", msg.Status,
				"to", mappedStatus,
			)
			_ = p.repo.InsertUnmatchedDLR(ctx, nil, UnmatchedDLR{
				ConnectionID:      &connID,
				ProviderMessageID: receipt.ProviderMessageID,
				Status:            receipt.Stat,
				ErrorCode:         &receipt.ErrCode,
				RawReceipt:        &receipt.RawReceipt,
				MatchStatus:       "unmatched",
			})
			return nil
		}
		p.logger.Warn("invalid state transition attempted by DLR",
			"msg_id", msgID,
			"from", msg.Status,
			"to", mappedStatus,
		)
		return nil
	}

	// Execute raw DLR storage, state transition & settlement inside one atomic transaction
	tx, err := p.repo.(*pgRepository).pool.Begin(ctx)
	if err != nil {
		saveRawStandalone()
		return fmt.Errorf("begin DLR transition tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if rawDLR != nil {
		if err := p.repo.RecordRawDLR(ctx, tx, *rawDLR); err != nil {
			p.logger.Error("durable storage of raw DLR in tx failed", "err", err)
		}
	}

	reason := fmt.Sprintf("DLR receipt: stat=%s err=%s", receipt.Stat, receipt.ErrCode)
	billingMode := msg.BillingModeSnapshot
	if billingMode == "" {
		billingMode = p.billingTrig
	}

	switch mappedStatus {
	case StatusDelivered:
		// Capture hold on delivery if billing_mode == on_delivery (or fallback if somehow uncaptured for on_provider_accept)
		if msg.HoldID != nil {
			idempotencyKey := fmt.Sprintf("capture-%s", msg.ID.String())
			_, capErr := p.ledgerSvc.CaptureTx(ctx, tx, *msg.HoldID, msg.SellPrice, &idempotencyKey)
			if capErr != nil && !(billingMode == "on_provider_accept" && errors.Is(capErr, ledger.ErrHoldNotActive)) {
				p.logger.Error("ledger CaptureTx on delivery failed", "msg_id", msgID, "err", capErr)
				details, _ := json.Marshal(map[string]interface{}{
					"error":        capErr.Error(),
					"amount":       msg.SellPrice.String(),
					"hold_id":      msg.HoldID.String(),
					"billing_mode": billingMode,
				})
				_ = p.repo.RecordSettlementExceptionTx(ctx, tx, SettlementException{
					MessageID:        msgID,
					MessageCreatedAt: createdAt,
					CustomerOrgID:    &msg.CustomerOrgID,
					HoldID:           msg.HoldID,
					ExceptionType:    "capture_failed",
					Severity:         "critical",
					Details:          details,
				})
			}
		}

		if err := p.repo.UpdateMessageStatus(ctx, tx,
			msgID, createdAt, msg.Status, StatusDelivered,
			&reason, nil, nil, nil,
		); err != nil {
			if errors.Is(err, ErrInvalidStateTransition) {
				if latest, lErr := p.repo.GetMessageHeader(ctx, msgID, createdAt); lErr == nil && latest != nil && IsTerminalStatus(latest.Status) {
					p.logger.Info("concurrent finalization of message during DLR processing; retaining final state", "msg_id", msgID, "status", latest.Status)
					return nil
				}
			}
			return fmt.Errorf("update message status to delivered: %w", err)
		}

	case StatusUndelivered, StatusFailed, StatusExpired:
		if msg.HoldID != nil {
			if billingMode == "on_provider_accept" {
				// Message was already accepted & captured; check customer's failed_after_accept_policy snapshot.
				if msg.FailedAfterAcceptPolicySnapshot == "refund" {
					refundKey := fmt.Sprintf("refund-%s", msg.ID.String())
					_, refErr := p.ledgerSvc.RefundCaptureTx(ctx, tx, *msg.HoldID, msg.SellPrice, &refundKey, reason)
					if errors.Is(refErr, ledger.ErrHoldNotActive) {
						// Edge case: hold wasn't captured yet, release cleanly
						_ = p.ledgerSvc.ReleaseTx(ctx, tx, *msg.HoldID)
					} else if refErr != nil {
						p.logger.Error("ledger RefundCaptureTx failed", "msg_id", msgID, "err", refErr)
						details, _ := json.Marshal(map[string]interface{}{
							"error":   refErr.Error(),
							"amount":  msg.SellPrice.String(),
							"hold_id": msg.HoldID.String(),
						})
						_ = p.repo.RecordSettlementExceptionTx(ctx, tx, SettlementException{
							MessageID:        msgID,
							MessageCreatedAt: createdAt,
							CustomerOrgID:    &msg.CustomerOrgID,
							HoldID:           msg.HoldID,
							ExceptionType:    "refund_failed",
							Severity:         "critical",
							Details:          details,
						})
					}
				}
				// Default 'keep_charge': retain the charge taken on provider acceptance.
			} else {
				// on_delivery: release active hold on terminal non-delivery
				_ = p.ledgerSvc.ReleaseTx(ctx, tx, *msg.HoldID)
			}
		}
		if err := p.repo.UpdateMessageStatus(ctx, tx,
			msgID, createdAt, msg.Status, mappedStatus,
			&reason, nil, nil, nil,
		); err != nil {
			if errors.Is(err, ErrInvalidStateTransition) {
				if latest, lErr := p.repo.GetMessageHeader(ctx, msgID, createdAt); lErr == nil && latest != nil && IsTerminalStatus(latest.Status) {
					p.logger.Info("concurrent finalization of message during DLR processing; retaining final state", "msg_id", msgID, "status", latest.Status)
					return nil
				}
			}
			return fmt.Errorf("update message status to %s: %w", mappedStatus, err)
		}

	default:
		// If mapped to unknown or other
		if err := p.repo.UpdateMessageStatus(ctx, tx,
			msgID, createdAt, msg.Status, mappedStatus,
			&reason, nil, nil, nil,
		); err != nil {
			return fmt.Errorf("update message status: %w", err)
		}
	}

	// Record event
	evtDetails, _ := json.Marshal(map[string]interface{}{
		"provider_message_id": receipt.ProviderMessageID,
		"stat":                receipt.Stat,
		"err":                 receipt.ErrCode,
		"raw_receipt":         receipt.RawReceipt,
	})
	_ = p.repo.RecordEvent(ctx, tx, MessageEvent{
		MessageID:        msgID,
		MessageCreatedAt: createdAt,
		EventType:        "dlr_received",
		FromStatus:       &msg.Status,
		ToStatus:         mappedStatus,
		ActorType:        "provider",
		Details:          evtDetails,
		CreatedAt:        time.Now().UTC(),
	})

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit DLR transition: %w", err)
	}

	p.logger.Debug("message status transitioned via DLR",
		"msg_id", msgID,
		"from", msg.Status,
		"to", mappedStatus,
		"stat", receipt.Stat,
	)

	return nil
}

func (p *DLRProcessor) resolveMappedStatus(ctx context.Context, connID uuid.UUID, stat, errCode string) string {
	// 1. Check custom per-connection overrides
	overrides, err := p.repo.GetDLRMappingsForConnection(ctx, connID)
	if err == nil {
		for _, o := range overrides {
			if strings.EqualFold(o.UpstreamStat, stat) {
				if o.UpstreamErr == nil || *o.UpstreamErr == "" || strings.EqualFold(*o.UpstreamErr, errCode) {
					return o.MappedStatus
				}
			}
		}
	}

	// 2. Standard SMPP 3.4 mapping
	switch stat {
	case "DELIVRD":
		return StatusDelivered
	case "UNDELIV":
		return StatusUndelivered
	case "EXPIRED":
		return StatusExpired
	case "REJECTD":
		return StatusFailed
	case "ACCEPTD":
		return StatusSubmitted
	case "UNKNOWN":
		return StatusUnknown
	default:
		return StatusUnknown
	}
}
