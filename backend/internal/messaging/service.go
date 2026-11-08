package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/routing"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

var e164Regex = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)

// Service manages the submission, lifecycle, field-stripping, and persistence of SMS messages.
type Service struct {
	pool          *pgxpool.Pool
	repo          Repository
	config        Config
	routingEngine *routing.Engine
	ledgerSvc     *ledger.Service
	keyring       *secrets.KeyRing
	auditSvc      *audit.Service
	sender        Sender
	logger        *slog.Logger
}

// NewService constructs and initializes a messaging Service.
func NewService(
	pool *pgxpool.Pool,
	repo Repository,
	cfg Config,
	routingEngine *routing.Engine,
	ledgerSvc *ledger.Service,
	keyring *secrets.KeyRing,
	auditSvc *audit.Service,
	sender Sender,
	logger *slog.Logger,
) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("messaging service config validation failed: %w", err)
	}
	if pool == nil {
		return nil, errors.New("database pool is required")
	}
	if repo == nil {
		return nil, errors.New("messaging repository is required")
	}
	if routingEngine == nil {
		return nil, errors.New("routing engine is required")
	}
	if ledgerSvc == nil {
		return nil, errors.New("ledger service is required")
	}
	if keyring == nil {
		return nil, errors.New("secrets keyring is required")
	}
	if sender == nil {
		sender = NewFakeSender()
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &Service{
		pool:          pool,
		repo:          repo,
		config:        cfg,
		routingEngine: routingEngine,
		ledgerSvc:     ledgerSvc,
		keyring:       keyring,
		auditSvc:      auditSvc,
		sender:        sender,
		logger:        logger,
	}, nil
}

// MaskPhone masks all but the country code prefix and last 4 digits for secure logging.
func MaskPhone(dest string) string {
	dest = strings.TrimSpace(dest)
	if len(dest) <= 6 {
		return "****"
	}
	// e.g. +12025550199 -> +1202***0199
	prefixLen := 4
	if strings.HasPrefix(dest, "+") && len(dest) >= 9 {
		prefixLen = 5
	} else if len(dest) < 10 {
		prefixLen = 2
	}
	return dest[:prefixLen] + "***" + dest[len(dest)-4:]
}

// Submit processes an outbound SMS submission end-to-end:
// 1. Validation & normalization
// 2. Global idempotency check via unpartitioned lookup
// 3. Segment calculation (GSM-7 / UCS-2)
// 4. Routing resolution (customer checks, sell rate, candidate ordering)
// 5. Total pricing
// 6. Pre-funding balance hold via PlaceHoldTx sharing the message insertion transaction
// 7. Atomic write: messages, parts, outbox, idempotency lookup, first event
// 8. Synchronous acknowledgment
func (s *Service) Submit(ctx context.Context, tc *tenant.Context, req SubmitRequest) (*SubmitResponse, error) {
	if tc == nil {
		return nil, tenant.ErrNoTenantContext
	}
	if !tc.HasPermission("message.send") {
		return nil, tenant.ErrUnauthorizedTenant
	}

	rawDest := strings.TrimSpace(req.Destination)
	if rawDest == "" {
		return nil, fmt.Errorf("%w: destination is required", ErrInvalidDestination)
	}
	senderID := strings.TrimSpace(req.SenderID)
	if senderID == "" {
		return nil, fmt.Errorf("%w: sender_id is required", ErrSenderIDNotApproved)
	}
	bodyText := req.Text
	if len(bodyText) == 0 {
		return nil, errors.New("message text cannot be empty")
	}

	// 1. Normalize destination to E.164
	cleanDest := rawDest
	if !strings.HasPrefix(cleanDest, "+") {
		cleanDest = "+" + cleanDest
	}
	if !e164Regex.MatchString(cleanDest) {
		s.logger.Warn("message submission rejected: invalid destination", "dest", MaskPhone(rawDest))
		return nil, fmt.Errorf("%w: %s is not valid international E.164", ErrInvalidDestination, MaskPhone(rawDest))
	}

	// 2. Global Idempotency Check across month boundaries
	var clientRef *string
	if req.ClientReference != nil && strings.TrimSpace(*req.ClientReference) != "" {
		trimmedRef := strings.TrimSpace(*req.ClientReference)
		clientRef = &trimmedRef
		existingID, existingCreatedAt, err := s.repo.CheckIdempotency(ctx, tc.ActorOrgID, *clientRef)
		if err != nil {
			return nil, fmt.Errorf("check idempotency: %w", err)
		}
		if existingID != nil && existingCreatedAt != nil {
			existingMsg, _, err := s.repo.GetMessage(ctx, *existingID, *existingCreatedAt)
			if err == nil && existingMsg != nil {
				return &SubmitResponse{
					MessageID:       existingMsg.ID,
					Status:          existingMsg.Status,
					Destination:     existingMsg.Destination,
					SenderID:        existingMsg.SenderID,
					Parts:           existingMsg.TotalParts,
					Currency:        existingMsg.SellCurrency,
					Price:           existingMsg.SellPrice,
					ClientReference: existingMsg.ClientReference,
					CreatedAt:       existingMsg.CreatedAt,
				}, nil
			}
		}
	}

	// 3. Segment and Encoding Calculation
	encoding, totalParts, bodyLength, err := CalculateSegments(bodyText, s.config.MaxSegmentsLimit)
	if err != nil {
		s.logger.Warn("message submission rejected: segment calculation failed", "dest", MaskPhone(cleanDest), "err", err)
		return nil, err
	}

	// 4. Routing Engine Resolution (Customer Policy, Sender ID, Rates, Candidates)
	now := time.Now().UTC()
	resolveReq := routing.ResolveRequest{
		CustomerOrgID:   tc.ActorOrgID,
		DestinationE164: cleanDest,
		SenderID:        senderID,
		SenderIDValue:   senderID,
		TrafficProfile:  req.TrafficProfile,
		Timestamp:       &now,
	}

	decision, err := s.routingEngine.Resolve(ctx, resolveReq)
	if err != nil {
		s.logger.Error("routing resolution error", "dest", MaskPhone(cleanDest), "err", err)
		return nil, fmt.Errorf("routing resolution failed: %w", err)
	}

	// Pre-Hold Exclusion Handling: Store REJECTED message without placing hold
	if len(decision.Candidates) == 0 {
		reasonCode := ReasonNoRouteAvailable
		var reasonDetail string
		if len(decision.Excluded) > 0 {
			firstEx := decision.Excluded[0]
			reasonDetail = firstEx.Detail
			switch firstEx.ReasonCode {
			case routing.ReasonNoSellRate:
				reasonCode = ReasonMissingSellRate
			case routing.ReasonSenderIDNotApproved:
				reasonCode = ReasonInvalidSenderID
			case routing.ReasonCustomerSuspended:
				reasonCode = ReasonCustomerSuspended
			case routing.ReasonDestinationNotAllowed:
				reasonCode = ReasonDestinationBarred
			case routing.ReasonInvalidNumber:
				reasonCode = ReasonInvalidDestination
			default:
				reasonCode = ReasonNoRouteAvailable
			}
		}

		s.logger.Warn("message submission rejected by routing policy",
			"dest", MaskPhone(cleanDest),
			"reason", reasonCode,
			"detail", reasonDetail,
		)

		_ = s.storeRejectedMessage(ctx, tc.ActorOrgID, clientRef, rawDest, cleanDest, senderID, encoding, bodyLength, totalParts, reasonCode)
		return nil, fmt.Errorf("%w: %s (%s)", ErrNoRouteAvailable, reasonCode, reasonDetail)
	}

	// 5. Total Pricing Calculation (unit_price * segments)
	primaryCandidate := decision.Candidates[0]
	unitSellPrice := primaryCandidate.EstimatedSellRate
	totalSellPrice := unitSellPrice.Mul(decimal.NewFromInt(int64(totalParts)))
	currency := primaryCandidate.CurrencyCode
	if currency == "" {
		currency = "USD"
	}

	// 6. Pre-funding Hold via PlaceHoldTx sharing the SAME database transaction
	wallet, err := s.ledgerSvc.GetWalletByOrgID(ctx, tc, tc.ActorOrgID)
	if err != nil {
		s.logger.Error("customer wallet lookup failed", "org", tc.ActorOrgID, "err", err)
		return nil, fmt.Errorf("%w: customer wallet not found", errors.New(ReasonWalletNotFound))
	}

	msgID := uuid.New()
	ttl := s.config.MessageTTL
	if req.ValiditySeconds != nil && *req.ValiditySeconds > 0 {
		customTTL := time.Duration(*req.ValiditySeconds) * time.Second
		if customTTL < ttl {
			ttl = customTTL
		}
	}
	expiresAt := now.Add(ttl)
	holdExpiry := now.Add(s.config.HoldExpiry)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin message transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	refType := "message"
	idempKey := fmt.Sprintf("hold:msg:%s", msgID.String())
	hold, err := s.ledgerSvc.PlaceHoldTx(ctx, tx, wallet.ID, totalSellPrice, currency, &refType, &msgID, &idempKey, holdExpiry)
	if err != nil {
		s.logger.Warn("message pre-funding balance hold failed",
			"org", tc.ActorOrgID,
			"amount", totalSellPrice,
			"currency", currency,
			"err", err,
		)
		_ = s.storeRejectedMessage(ctx, tc.ActorOrgID, clientRef, rawDest, cleanDest, senderID, encoding, bodyLength, totalParts, ReasonInsufficientFunds)
		return nil, fmt.Errorf("%w: unable to reserve funds (%v)", ErrInsufficientBalance, err)
	}

	// 7. Encrypt Message Body at Rest via Keyring
	envelope, err := s.keyring.Encrypt(bodyText)
	if err != nil {
		return nil, fmt.Errorf("encrypt message body: %w", err)
	}
	keyID := s.keyring.ActiveKeyID()

	// 8. Prepare Message & Parts
	candidateIDs := make([]uuid.UUID, len(decision.Candidates))
	for i, c := range decision.Candidates {
		candidateIDs[i] = c.ConnectionID
	}

	msg := &Message{
		ID:                   msgID,
		CreatedAt:            now,
		CustomerOrgID:        tc.ActorOrgID,
		ClientReference:      clientRef,
		OriginalDestination: rawDest,
		Destination:          cleanDest,
		SenderID:             senderID,
		SenderIDType:         decision.Request.SenderIDType,
		MessageType:          "sms",
		Encoding:             encoding,
		EncryptedBody:        []byte(envelope),
		KeyID:                keyID,
		BodyLength:           bodyLength,
		TotalParts:           totalParts,
		Status:               StatusAccepted,
		HoldID:               &hold.ID,
		SellPrice:            totalSellPrice,
		SellCurrency:         currency,
		EstimatedBuyCost:     &primaryCandidate.EstimatedBuyRate,
		BuyCurrency:          &primaryCandidate.CurrencyCode,
		RoutePlanID:          decision.PlanID,
		ProviderConnectionID: &primaryCandidate.ConnectionID,
		Attempts:             0,
		MaxAttempts:          3,
		ExpiresAt:            expiresAt,
		UpdatedAt:            now,
	}

	partBodies, udhs := SplitMessageParts(bodyText, encoding)
	parts := make([]MessagePart, len(partBodies))
	for i := range partBodies {
		partEnvelope, _ := s.keyring.Encrypt(partBodies[i])
		parts[i] = MessagePart{
			ID:                uuid.New(),
			MessageID:         msgID,
			CreatedAt:         now,
			PartNumber:        i + 1,
			TotalParts:        totalParts,
			UDH:               udhs[i],
			EncryptedPartBody: []byte(partEnvelope),
			KeyID:             keyID,
			Status:            StatusAccepted,
			UpdatedAt:         now,
		}
	}

	// 9. Prepare Transactional Outbox Row
	outboxPayload := OutboxPayload{
		MessageID:              msgID,
		MessageCreatedAt:       now,
		CustomerOrgID:          tc.ActorOrgID,
		Destination:            cleanDest,
		SenderID:               senderID,
		SenderIDType:           msg.SenderIDType,
		Encoding:               encoding,
		TotalParts:             totalParts,
		SellPrice:              totalSellPrice,
		SellCurrency:           currency,
		CandidateConnectionIDs: candidateIDs,
		CurrentConnectionID:    primaryCandidate.ConnectionID,
		AttemptNumber:          1,
		MaxAttempts:            msg.MaxAttempts,
		Body:                   bodyText,
	}
	payloadJSON, _ := json.Marshal(outboxPayload)

	outbox := &MessageOutbox{
		ID:               uuid.New(),
		MessageID:        msgID,
		MessageCreatedAt: now,
		CustomerOrgID:    tc.ActorOrgID,
		Payload:          payloadJSON,
		Status:           "pending",
		Attempts:         0,
		NextAttemptAt:    now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	initialEvent := &MessageEvent{
		ID:               uuid.New(),
		MessageID:        msgID,
		MessageCreatedAt: now,
		EventType:        "submitted",
		ToStatus:         StatusAccepted,
		ActorType:        "customer",
		CreatedAt:        now,
	}

	// 10. Atomic Database Insert within Caller Transaction
	if err := s.repo.InsertMessageAtomic(ctx, tx, msg, parts, outbox, initialEvent); err != nil {
		s.logger.Error("failed atomic message insertion", "msg_id", msgID, "err", err)
		return nil, fmt.Errorf("insert message atomic: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		s.logger.Error("commit message transaction failed", "msg_id", msgID, "err", err)
		return nil, fmt.Errorf("commit message transaction: %w", err)
	}

	s.logger.Info("outbound message accepted",
		"msg_id", msgID,
		"dest", MaskPhone(cleanDest),
		"parts", totalParts,
		"price", totalSellPrice,
	)

	return &SubmitResponse{
		MessageID:       msgID,
		Status:          StatusAccepted,
		Destination:     cleanDest,
		SenderID:        senderID,
		Parts:           totalParts,
		Currency:        currency,
		Price:           totalSellPrice,
		ClientReference: clientRef,
		CreatedAt:       now,
	}, nil
}

// storeRejectedMessage records a pre-hold rejected message for audit and analytics without holding funds.
func (s *Service) storeRejectedMessage(
	ctx context.Context,
	customerOrgID uuid.UUID,
	clientRef *string,
	origDest, dest, senderID, encoding string,
	bodyLen, parts int,
	reason string,
) error {
	now := time.Now().UTC()
	msg := &Message{
		ID:                   uuid.New(),
		CreatedAt:            now,
		CustomerOrgID:        customerOrgID,
		ClientReference:      clientRef,
		OriginalDestination: origDest,
		Destination:          dest,
		SenderID:             senderID,
		MessageType:          "sms",
		Encoding:             encoding,
		EncryptedBody:        []byte{},
		KeyID:                s.keyring.ActiveKeyID(),
		BodyLength:           bodyLen,
		TotalParts:           parts,
		Status:               StatusRejected,
		FailureReason:        &reason,
		ExpiresAt:            now,
		UpdatedAt:            now,
	}
	event := &MessageEvent{
		ID:               uuid.New(),
		MessageID:        msg.ID,
		MessageCreatedAt: now,
		EventType:        "rejected",
		ToStatus:         StatusRejected,
		ActorType:        "system",
		CreatedAt:        now,
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := s.repo.InsertMessageAtomic(ctx, tx, msg, nil, nil, event); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GetMessage returns a message view with tenant isolation and field-stripping applied.
func (s *Service) GetMessage(ctx context.Context, tc *tenant.Context, id uuid.UUID, createdAt time.Time) (*MessageView, error) {
	if tc == nil {
		return nil, tenant.ErrNoTenantContext
	}
	if !tc.HasPermission("message.view") {
		return nil, tenant.ErrUnauthorizedTenant
	}

	msg, _, err := s.repo.GetMessage(ctx, id, createdAt)
	if err != nil {
		return nil, err
	}
	if msg == nil {
		return nil, errors.New("message not found")
	}

	// Tenant boundary check: platform sees all, others see only their own org
	if !tc.IsPlatformOrg() && msg.CustomerOrgID != tc.ActorOrgID {
		return nil, tenant.ErrUnauthorizedTenant
	}

	view := s.toMessageView(tc, msg)
	return view, nil
}

// ListMessages returns paginated messages with tenant isolation and field-stripping applied.
func (s *Service) ListMessages(ctx context.Context, tc *tenant.Context, status string, page, pageSize int) ([]MessageView, int, error) {
	if tc == nil {
		return nil, 0, tenant.ErrNoTenantContext
	}
	if !tc.HasPermission("message.view") {
		return nil, 0, tenant.ErrUnauthorizedTenant
	}

	var targetOrg *uuid.UUID
	if !tc.IsPlatformOrg() {
		targetOrg = &tc.ActorOrgID
	}

	msgs, total, err := s.repo.ListMessages(ctx, targetOrg, status, page, pageSize)
	if err != nil {
		return nil, 0, err
	}

	views := make([]MessageView, len(msgs))
	for i := range msgs {
		views[i] = *s.toMessageView(tc, &msgs[i])
	}

	return views, total, nil
}

// toMessageView constructs the sanitized MessageView, stripping internal commercial/carrier data
// from non-platform callers, and masking the body if message.content.view is absent.
func (s *Service) toMessageView(tc *tenant.Context, m *Message) *MessageView {
	view := &MessageView{
		ID:                   m.ID,
		CreatedAt:            m.CreatedAt,
		CustomerOrgID:        m.CustomerOrgID,
		ClientReference:      m.ClientReference,
		OriginalDestination: m.OriginalDestination,
		Destination:          m.Destination,
		SenderID:             m.SenderID,
		SenderIDType:         m.SenderIDType,
		Encoding:             m.Encoding,
		BodyLength:           m.BodyLength,
		TotalParts:           m.TotalParts,
		Status:               m.Status,
		FailureReason:        m.FailureReason,
		SellPrice:            m.SellPrice,
		SellCurrency:         m.SellCurrency,
		Attempts:             m.Attempts,
		ExpiresAt:            m.ExpiresAt,
		SubmittedAt:          m.SubmittedAt,
		DeliveredAt:          m.DeliveredAt,
	}

	// Platform caller: expose provider connection, buy costs, and margin
	if tc.IsPlatformOrg() {
		view.ProviderConnectionID = m.ProviderConnectionID
		view.ProviderMessageID = m.ProviderMessageID
		view.EstimatedBuyCost = m.EstimatedBuyCost
		view.ActualBuyCost = m.ActualBuyCost
		view.BuyCurrency = m.BuyCurrency
		view.RoutePlanID = m.RoutePlanID

		if m.ActualBuyCost != nil && !m.SellPrice.IsZero() {
			margin := m.SellPrice.Sub(*m.ActualBuyCost)
			view.Margin = &margin
		}
	}

	// Content permission: decrypt body if granted, mask if denied
	if tc.HasPermission("message.content.view") && len(m.EncryptedBody) > 0 {
		decrypted, err := s.keyring.Decrypt(string(m.EncryptedBody))
		if err == nil {
			view.Body = decrypted
		} else {
			view.Body = "[DECRYPTION FAILED]"
		}
	} else {
		view.Body = "[ENCRYPTED - PERMISSION REQUIRED]"
	}

	return view
}
