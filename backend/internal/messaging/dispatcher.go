package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/routing"
)

// RateLimiter is a thread-safe token-bucket rate limiter.
type RateLimiter struct {
	mu         sync.Mutex
	rate       float64
	capacity   float64
	tokens     float64
	lastUpdate time.Time
}

func NewRateLimiter(tps float64) *RateLimiter {
	if tps <= 0 {
		tps = 1000000
	}
	return &RateLimiter{
		rate:       tps,
		capacity:   tps + 1,
		tokens:     tps + 1,
		lastUpdate: time.Now(),
	}
}

func (l *RateLimiter) Wait(ctx context.Context) error {
	for {
		l.mu.Lock()
		now := time.Now()
		elapsed := now.Sub(l.lastUpdate).Seconds()
		l.tokens += elapsed * l.rate
		if l.tokens > l.capacity {
			l.tokens = l.capacity
		}
		l.lastUpdate = now

		if l.tokens >= 1.0 {
			l.tokens -= 1.0
			l.mu.Unlock()
			return nil
		}

		needed := 1.0 - l.tokens
		sleepTime := time.Duration((needed / l.rate) * float64(time.Second))
		l.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleepTime):
		}
	}
}

// Dispatcher processes messages from message_outbox and dispatches them via the Sender interface.
type Dispatcher struct {
	pool          *pgxpool.Pool
	repo          Repository
	routingEngine *routing.Engine
	ledgerSvc     *ledger.Service
	sender        Sender
	logger        *slog.Logger

	workerID     string
	lockDuration time.Duration
	pollInterval time.Duration
	batchSize    int

	mu       sync.Mutex
	limiters map[uuid.UUID]*RateLimiter

	stopCh chan struct{}
	doneCh chan struct{}
}

// NewDispatcher creates an asynchronous outbox dispatcher.
func NewDispatcher(
	pool *pgxpool.Pool,
	repo Repository,
	routingEngine *routing.Engine,
	ledgerSvc *ledger.Service,
	sender Sender,
	logger *slog.Logger,
) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{
		pool:          pool,
		repo:          repo,
		routingEngine: routingEngine,
		ledgerSvc:     ledgerSvc,
		sender:        sender,
		logger:        logger,
		workerID:      fmt.Sprintf("worker-%s", uuid.NewString()[:8]),
		lockDuration:  30 * time.Second,
		pollInterval:  50 * time.Millisecond,
		batchSize:     20,
		limiters:      make(map[uuid.UUID]*RateLimiter),
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
	}
}

// SetConnectionRateLimit sets an in-memory TPS rate limit for a specific connection.
func (d *Dispatcher) SetConnectionRateLimit(connID uuid.UUID, tps float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.limiters[connID] = NewRateLimiter(tps)
}

// Start launches the background outbox polling loop.
func (d *Dispatcher) Start(ctx context.Context) {
	go func() {
		defer close(d.doneCh)
		ticker := time.NewTicker(d.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-d.stopCh:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = d.ProcessBatch(ctx)
			}
		}
	}()
}

// Stop gracefully signals the dispatcher loop to terminate.
func (d *Dispatcher) Stop() {
	close(d.stopCh)
	<-d.doneCh
}

// ProcessBatch fetches a batch of pending outbox rows and processes them synchronously.
func (d *Dispatcher) ProcessBatch(ctx context.Context) int {
	items, err := d.repo.FetchOutboxPending(ctx, d.batchSize, d.workerID, d.lockDuration)
	if err != nil {
		d.logger.Error("fetch outbox pending failed", "err", err)
		return 0
	}

	for _, item := range items {
		d.DispatchOne(ctx, item)
	}

	return len(items)
}

// DispatchOne processes a claimed outbox item.
func (d *Dispatcher) DispatchOne(ctx context.Context, item MessageOutbox) {
	var payload OutboxPayload
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		d.logger.Error("unmarshal outbox payload failed", "id", item.ID, "err", err)
		return
	}

	// 1. Dynamic Routing Check at Dispatch Time (Kill Switch & Route Freshness)
	failedConnIDs := payload.FailedConnectionIDs
	if failedConnIDs == nil {
		failedConnIDs = make([]uuid.UUID, 0)
	}

	resolveReq := routing.ResolveRequest{
		CustomerOrgID:   payload.CustomerOrgID,
		DestinationE164: payload.Destination,
		SenderID:        payload.SenderID,
		SenderIDValue:   payload.SenderID,
		TrafficProfile:  payload.Encoding,
	}

	// Dynamic Next() evaluation to ensure connection was not kill-switched while queued
	cand, err := d.routingEngine.Next(ctx, resolveReq, failedConnIDs)
	if err != nil || cand == nil {
		d.logger.Warn("dispatch aborted: kill switch active or no route at dispatch time",
			"msg_id", payload.MessageID,
			"dest", MaskPhone(payload.Destination),
		)
		d.finalizeFailed(ctx, item.ID, payload, ReasonKilledAtDispatch)
		return
	}
	currentConnID := cand.ConnectionID

	// 2. Per-connection rate limiting
	d.mu.Lock()
	limiter, exists := d.limiters[currentConnID]
	d.mu.Unlock()
	if exists && limiter != nil {
		_ = limiter.Wait(ctx)
	}

	// 3. Dispatch to Upstream Sender
	attemptStart := time.Now().UTC()
	attemptReq := SendAttempt{
		MessageID:      payload.MessageID,
		ConnectionID:   currentConnID,
		Destination:    payload.Destination,
		SenderID:       payload.SenderID,
		SenderIDType:   payload.SenderIDType,
		Text:           payload.Body,
		PartNumber:     1,
		TotalParts:     payload.TotalParts,
		Encoding:       payload.Encoding,
		TrafficProfile: payload.Encoding,
		Timeout:        10 * time.Second,
	}

	result, sendErr := d.sender.Send(ctx, attemptReq)
	if sendErr != nil && result.Status == "" {
		result.Status = SendStatusUnknown
		result.ErrorMessage = sendErr.Error()
	}
	attemptFinish := time.Now().UTC()
	latencyMs := int(attemptFinish.Sub(attemptStart).Milliseconds())

	attemptRecord := MessageAttempt{
		ID:                uuid.New(),
		MessageID:         payload.MessageID,
		MessageCreatedAt:  payload.MessageCreatedAt,
		AttemptNumber:     payload.AttemptNumber,
		ConnectionID:      currentConnID,
		Status:            string(result.Status),
		ProviderMessageID: &result.ProviderMessageID,
		ErrorCode:         &result.ErrorCode,
		ErrorMessage:      &result.ErrorMessage,
		BuyCost:           &result.BuyCost,
		BuyCurrency:       &payload.SellCurrency,
		LatencyMs:         &latencyMs,
		StartedAt:         attemptStart,
		FinishedAt:        &attemptFinish,
	}

	// 4. Handle Sender Response Outcomes
	switch result.Status {
	case SendStatusAccepted:
		d.handleAccepted(ctx, item.ID, payload, currentConnID, result, attemptRecord)

	case SendStatusRetryableReject:
		d.handleRetryableReject(ctx, item.ID, payload, currentConnID, result, attemptRecord)

	case SendStatusFatalReject:
		d.handleFatalReject(ctx, item.ID, payload, currentConnID, result, attemptRecord)

	case SendStatusUnknown:
		d.handleAmbiguousUnknown(ctx, item.ID, payload, currentConnID, result, attemptRecord)

	default:
		d.handleAmbiguousUnknown(ctx, item.ID, payload, currentConnID, result, attemptRecord)
	}
}

// handleAccepted transitions message to submitted (PROVIDER_ACCEPTED) and saves provider lookup.
func (d *Dispatcher) handleAccepted(
	ctx context.Context,
	outboxID uuid.UUID,
	payload OutboxPayload,
	connID uuid.UUID,
	res SendResult,
	attempt MessageAttempt,
) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		d.logger.Error("begin tx for accepted message failed", "err", err)
		return
	}
	defer tx.Rollback(ctx)

	// Record attempt
	if err := d.repo.RecordAttempt(ctx, tx, attempt); err != nil {
		d.logger.Error("record attempt failed", "err", err)
		return
	}

	// Update message status to submitted
	reason := "provider accepted submission"
	if err := d.repo.UpdateMessageStatus(ctx, tx,
		payload.MessageID, payload.MessageCreatedAt,
		StatusAccepted, StatusSubmitted,
		&reason, &res.BuyCost, &connID, &res.ProviderMessageID,
	); err != nil {
		// Try from queued or submitting if already transitioned
		_ = d.repo.UpdateMessageStatus(ctx, tx,
			payload.MessageID, payload.MessageCreatedAt,
			StatusSubmitting, StatusSubmitted,
			&reason, &res.BuyCost, &connID, &res.ProviderMessageID,
		)
	}

	// Write unpartitioned provider lookup row
	if res.ProviderMessageID != "" {
		_ = d.repo.RecordProviderLookup(ctx, tx, connID, res.ProviderMessageID, payload.MessageID, payload.MessageCreatedAt)
	}

	// Mark outbox row published
	_ = d.repo.UpdateOutboxStatus(ctx, tx, outboxID, "published", nil)

	if err := tx.Commit(ctx); err != nil {
		d.logger.Error("commit accepted transition failed", "err", err)
		return
	}

	d.logger.Info("message accepted by upstream carrier",
		"msg_id", payload.MessageID,
		"conn_id", connID,
		"provider_msg_id", res.ProviderMessageID,
	)
}

// handleRetryableReject performs failover route switching via Next() if attempts remain.
func (d *Dispatcher) handleRetryableReject(
	ctx context.Context,
	outboxID uuid.UUID,
	payload OutboxPayload,
	connID uuid.UUID,
	res SendResult,
	attempt MessageAttempt,
) {
	// Step 0d Multi-part safety: a retryable reject after some parts were already accepted must NOT fail over
	// to another route (duplicate-part risk). Transition to UNKNOWN so hold is retained and exposure audited.
	if payload.TotalParts > 1 && payload.PartsAccepted > 0 {
		d.logger.Error("multipart message had partial parts accepted; failover prohibited to prevent duplicate delivery",
			"msg_id", payload.MessageID,
			"parts_accepted", payload.PartsAccepted,
			"total_parts", payload.TotalParts,
		)
		d.handleAmbiguousUnknown(ctx, outboxID, payload, connID, res, attempt)
		return
	}

	d.logger.Warn("carrier returned retryable reject, attempting failover",
		"msg_id", payload.MessageID,
		"conn_id", connID,
		"err_code", res.ErrorCode,
		"attempt", payload.AttemptNumber,
	)

	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)

	_ = d.repo.RecordAttempt(ctx, tx, attempt)

	// Check if attempts exhausted
	if payload.AttemptNumber >= payload.MaxAttempts {
		d.finalizeFailedTx(ctx, tx, outboxID, payload, ReasonMaxAttemptsExhausted)
		_ = tx.Commit(ctx)
		return
	}

	// Exclude failed connection and invoke Next() for fallback route
	payload.FailedConnectionIDs = append(payload.FailedConnectionIDs, connID)
	resolveReq := routing.ResolveRequest{
		CustomerOrgID:   payload.CustomerOrgID,
		DestinationE164: payload.Destination,
		SenderID:        payload.SenderID,
		SenderIDValue:   payload.SenderID,
		TrafficProfile:  payload.Encoding,
	}

	nextCand, err := d.routingEngine.Next(ctx, resolveReq, payload.FailedConnectionIDs)
	if err != nil || nextCand == nil {
		d.finalizeFailedTx(ctx, tx, outboxID, payload, ReasonNoRouteAvailable)
		_ = tx.Commit(ctx)
		return
	}

	// Re-queue outbox for fallback candidate
	payload.CurrentConnectionID = nextCand.ConnectionID
	payload.AttemptNumber++
	newPayloadJSON, _ := json.Marshal(payload)

	now := time.Now().UTC()
	requeueSQL := `
		UPDATE message_outbox
		SET payload = $1,
		    status = 'pending',
		    locked_until = NULL,
		    locked_by = NULL,
		    next_attempt_at = $2,
		    updated_at = $3
		WHERE id = $4
	`
	_, _ = tx.Exec(ctx, requeueSQL, newPayloadJSON, now, now, outboxID)
	_ = tx.Commit(ctx)
}

// handleFatalReject terminates immediately without failover and releases funds.
func (d *Dispatcher) handleFatalReject(
	ctx context.Context,
	outboxID uuid.UUID,
	payload OutboxPayload,
	connID uuid.UUID,
	res SendResult,
	attempt MessageAttempt,
) {
	d.logger.Warn("carrier returned fatal permanent reject; failover prohibited",
		"msg_id", payload.MessageID,
		"conn_id", connID,
		"err_code", res.ErrorCode,
	)

	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)

	_ = d.repo.RecordAttempt(ctx, tx, attempt)
	d.finalizeFailedTx(ctx, tx, outboxID, payload, ReasonPermanentCarrierReject)
	_ = tx.Commit(ctx)
}

// handleAmbiguousUnknown transitions message to UNKNOWN; strictly NEVER auto-resends.
func (d *Dispatcher) handleAmbiguousUnknown(
	ctx context.Context,
	outboxID uuid.UUID,
	payload OutboxPayload,
	connID uuid.UUID,
	res SendResult,
	attempt MessageAttempt,
) {
	d.logger.Error("carrier returned ambiguous outcome; strictly retaining hold and setting to unknown",
		"msg_id", payload.MessageID,
		"conn_id", connID,
		"err_msg", res.ErrorMessage,
	)

	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)

	_ = d.repo.RecordAttempt(ctx, tx, attempt)

	reason := fmt.Sprintf("ambiguous carrier outcome: %s; delivery unconfirmed", res.ErrorMessage)
	_ = d.repo.UpdateMessageStatus(ctx, tx,
		payload.MessageID, payload.MessageCreatedAt,
		StatusAccepted, StatusUnknown,
		&reason, nil, &connID, nil,
	)

	// Hold is RETAINED. Outbox finalized so it is NEVER resent.
	_ = d.repo.UpdateOutboxStatus(ctx, tx, outboxID, "published", nil)
	_ = tx.Commit(ctx)
}

func (d *Dispatcher) finalizeFailed(ctx context.Context, outboxID uuid.UUID, payload OutboxPayload, reason string) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)

	d.finalizeFailedTx(ctx, tx, outboxID, payload, reason)
	_ = tx.Commit(ctx)
}

func (d *Dispatcher) finalizeFailedTx(ctx context.Context, tx pgx.Tx, outboxID uuid.UUID, payload OutboxPayload, reason string) {
	msg, _, err := d.repo.GetMessage(ctx, payload.MessageID, payload.MessageCreatedAt)
	if err == nil && msg != nil {
		_ = d.repo.UpdateMessageStatus(ctx, tx,
			payload.MessageID, payload.MessageCreatedAt,
			msg.Status, StatusFailed,
			&reason, nil, nil, nil,
		)
		// Release hold on terminal failure
		if msg.HoldID != nil {
			_ = d.ledgerSvc.ReleaseTx(ctx, tx, *msg.HoldID)
		}
	}
	_ = d.repo.UpdateOutboxStatus(ctx, tx, outboxID, "failed", nil)
}
