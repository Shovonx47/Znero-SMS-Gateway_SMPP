package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/znero-international/znero/backend/internal/ledger"
)

// Repository specifies the persistence interface for messaging operations.
// BulkInsertItem groups one message and its associated records for a single-batch TX insert.
type BulkInsertItem struct {
	Msg    *Message
	Parts  []MessagePart
	Outbox *MessageOutbox
	Event  *MessageEvent
}

type Repository interface {
	InsertMessageAtomic(ctx context.Context, tx pgx.Tx, msg *Message, parts []MessagePart, outbox *MessageOutbox, event *MessageEvent) error
	InsertMessagesBulkAtomic(ctx context.Context, tx pgx.Tx, items []BulkInsertItem) error
	CheckIdempotency(ctx context.Context, customerOrgID uuid.UUID, clientRef string) (*uuid.UUID, *time.Time, error)
	CheckIdempotencyBatch(ctx context.Context, customerOrgID uuid.UUID, refs []string) (map[string]MessageLocator, error)
	CheckIdempotencyWithFingerprint(ctx context.Context, customerOrgID uuid.UUID, clientRef string) (*uuid.UUID, *time.Time, *string, error)
	GetMessageLocator(ctx context.Context, messageID uuid.UUID) (*MessageLocator, error)
	GetMessageHeader(ctx context.Context, id uuid.UUID, createdAt time.Time) (*Message, error)
	GetMessage(ctx context.Context, id uuid.UUID, createdAt time.Time) (*Message, []MessagePart, error)
	GetMessageByID(ctx context.Context, id uuid.UUID) (*Message, []MessagePart, error)
	ListMessages(ctx context.Context, customerOrgID *uuid.UUID, status string, page, pageSize int) ([]Message, int, error)
	UpdateMessageStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, createdAt time.Time, fromStatus, toStatus string, reason *string, buyCost *decimal.Decimal, connID *uuid.UUID, providerMsgID *string) error
	RecordAttempt(ctx context.Context, tx pgx.Tx, attempt MessageAttempt) error
	RecordEvent(ctx context.Context, tx pgx.Tx, event MessageEvent) error
	RecordProviderLookup(ctx context.Context, tx pgx.Tx, connectionID uuid.UUID, providerMsgID string, messageID uuid.UUID, createdAt time.Time) error
	FetchOutboxPending(ctx context.Context, limit int, workerID string, lockDuration time.Duration) ([]MessageOutbox, error)
	ReleaseOutboxPending(ctx context.Context, outboxID uuid.UUID, delay time.Duration) error
	UpdateOutboxStatus(ctx context.Context, tx pgx.Tx, outboxID uuid.UUID, status string, nextAttemptAt *time.Time) error
	EnsureMonthlyPartitions(ctx context.Context, monthsAhead int) error
	GetProviderLookup(ctx context.Context, connectionID uuid.UUID, providerMsgID string) (*uuid.UUID, *time.Time, error)
	RecordRawDLR(ctx context.Context, tx pgx.Tx, dlr MessageDLR) error
	InsertUnmatchedDLR(ctx context.Context, tx pgx.Tx, item UnmatchedDLR) error
	GetUnmatchedDLRs(ctx context.Context, page, pageSize int, statusFilter string) ([]UnmatchedDLR, int, error)
	UpdateUnmatchedDLRStatus(ctx context.Context, id uuid.UUID, status string) error
	GetDLRMappingsForConnection(ctx context.Context, connectionID uuid.UUID) ([]DLRMapping, error)
	CreateDLRMapping(ctx context.Context, m *DLRMapping) error
	UpdateDLRMapping(ctx context.Context, m *DLRMapping) error
	DeleteDLRMapping(ctx context.Context, id uuid.UUID) error
	ListDLRMappings(ctx context.Context, connectionID *uuid.UUID) ([]DLRMapping, error)
	RecordSettlementException(ctx context.Context, exc SettlementException) error
	RecordSettlementExceptionTx(ctx context.Context, tx pgx.Tx, exc SettlementException) error
	ListSettlementExceptions(ctx context.Context, orgID *uuid.UUID, resolved *bool, page, pageSize int) ([]SettlementException, int, error)
	ResolveSettlementException(ctx context.Context, id uuid.UUID, resolvedBy uuid.UUID) error
	RecordReconciliationRun(ctx context.Context, run ReconciliationRun) error
	ListReconciliationRuns(ctx context.Context, limit int) ([]ReconciliationRun, error)
	GetQueueBacklogStats(ctx context.Context) (QueueBacklogStats, error)
	GetMessageAttempts(ctx context.Context, messageID uuid.UUID) ([]MessageAttempt, error)
	GetMessageEvents(ctx context.Context, messageID uuid.UUID) ([]MessageEvent, error)
	GetMessageDLRs(ctx context.Context, messageID uuid.UUID) ([]MessageDLR, error)
	FindStuckMessages(ctx context.Context, before time.Time, limit int) ([]Message, error)
	FindUnknownMessages(ctx context.Context, before time.Time, limit int) ([]Message, error)
	FindNoDLRSubmittedMessages(ctx context.Context, before time.Time, limit int) ([]Message, error)
	FindPendingUnmatchedDLRs(ctx context.Context, limit int) ([]UnmatchedDLR, error)
	ListMessagesAdvanced(ctx context.Context, orgID *uuid.UUID, status, destination, senderID string, connID *uuid.UUID, fromDate, toDate *time.Time, page, pageSize int) ([]Message, int, error)
	IncrementUsageCountersTx(ctx context.Context, tx pgx.Tx, customerOrgID uuid.UUID, parts int, dailyCap, monthlyCap *int64) error
	GetCustomerUsage(ctx context.Context, customerOrgID uuid.UUID) (dailyCount int64, monthlyCount int64, dailyCap int64, monthlyCap int64, tpsLimit int, err error)
	GetCustomerBillingTerms(ctx context.Context, customerOrgID uuid.UUID, defaults CustomerBillingTerms) (CustomerBillingTerms, error)
	TransitionToFinalStateTx(ctx context.Context, tx pgx.Tx, ledgerSvc *ledger.Service, billingTrig string, params FinalStateParams) error
	InsertSMPPDLRQueue(ctx context.Context, tx pgx.Tx, item SMPPDLRQueueItem) error
	FetchPendingSMPPDLRs(ctx context.Context, limit int, leasedBy string, leaseDuration time.Duration) ([]SMPPDLRQueueItem, error)
	UpdateSMPPDLRQueueStatus(ctx context.Context, id uuid.UUID, status string) error
	UpdateSMPPDLRQueueStatusBatch(ctx context.Context, ids []uuid.UUID, status string) error
	CreateWebhook(ctx context.Context, wh *CustomerWebhook) error
	GetWebhook(ctx context.Context, id uuid.UUID, customerOrgID uuid.UUID) (*CustomerWebhook, error)
	ListWebhooks(ctx context.Context, customerOrgID uuid.UUID) ([]CustomerWebhook, error)
	UpdateWebhook(ctx context.Context, wh *CustomerWebhook) error
	DeleteWebhook(ctx context.Context, id uuid.UUID, customerOrgID uuid.UUID) error
	ListWebhookDeliveries(ctx context.Context, customerOrgID uuid.UUID, webhookID *uuid.UUID, status string, page, pageSize int) ([]CustomerWebhookDelivery, int, error)
	GetWebhookDeliveryWithAttempts(ctx context.Context, deliveryID uuid.UUID, customerOrgID uuid.UUID) (*CustomerWebhookDelivery, []CustomerWebhookAttempt, error)
	FetchPendingWebhookDeliveries(ctx context.Context, limit int, leasedBy string, leaseDuration time.Duration) ([]CustomerWebhookDelivery, error)
	RecordWebhookAttempt(ctx context.Context, attempt CustomerWebhookAttempt, nextStatus string, nextAttempt *time.Time) error
	CreateInboundNumber(ctx context.Context, n *InboundNumber) error
	UpdateInboundNumber(ctx context.Context, n *InboundNumber) error
	GetInboundNumber(ctx context.Context, id uuid.UUID) (*InboundNumber, error)
	MatchInboundNumber(ctx context.Context, destinationE164 string) (*InboundNumber, error)
	ListInboundNumbers(ctx context.Context, customerOrgID *uuid.UUID, status string, page, pageSize int) ([]InboundNumber, int, error)
	RecordInboundMessage(ctx context.Context, msg *InboundMessage) error
	QueueInboundWebhook(ctx context.Context, customerOrgID uuid.UUID, payload map[string]interface{}) error
	ListInboundMessages(ctx context.Context, customerOrgID *uuid.UUID, page, pageSize int) ([]InboundMessage, int, error)
	CleanExpiredData(ctx context.Context, retentionDays int) (dlrsCleaned int64, webhooksCleaned int64, err error)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

// NewRepository returns a PostgreSQL-backed repository for messaging.
func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

// InsertMessageAtomic inserts the message, its multi-part segments, transactional outbox row,
// unpartitioned idempotency key, and initial event within one caller-provided database transaction.
func (r *pgRepository) InsertMessageAtomic(
	ctx context.Context,
	tx pgx.Tx,
	msg *Message,
	parts []MessagePart,
	outbox *MessageOutbox,
	event *MessageEvent,
) error {
	return r.InsertMessagesBulkAtomic(ctx, tx, []BulkInsertItem{{
		Msg:    msg,
		Parts:  parts,
		Outbox: outbox,
		Event:  event,
	}})
}

// InsertMessagesBulkAtomic inserts one or more messages and all related rows in a single pgx.Batch.
func (r *pgRepository) InsertMessagesBulkAtomic(ctx context.Context, tx pgx.Tx, items []BulkInsertItem) error {
	if len(items) == 0 {
		return nil
	}
	var batch pgx.Batch
	hasIdemp := make([]bool, len(items))
	for idx, it := range items {
		hasIdemp[idx] = queueSingleMessageBatch(&batch, it.Msg, it.Parts, it.Outbox, it.Event)
	}

	br := tx.SendBatch(ctx, &batch)
	defer br.Close()

	for idx, it := range items {
		if err := readSingleMessageBatch(br, it.Msg, it.Parts, it.Outbox, it.Event, hasIdemp[idx]); err != nil {
			return err
		}
	}
	if err := br.Close(); err != nil {
		return fmt.Errorf("close atomic message batch: %w", err)
	}
	return nil
}

func queueSingleMessageBatch(
	batch *pgx.Batch,
	msg *Message,
	parts []MessagePart,
	outbox *MessageOutbox,
	event *MessageEvent,
) bool {
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	if msg.ID == uuid.Nil {
		msg.ID = uuid.New()
	}

	const msgQuery = `
		INSERT INTO messages (
			id, created_at, customer_org_id, client_reference, original_destination, destination,
			sender_id, sender_id_type, message_type, encoding, encrypted_body, key_id,
			body_length, total_parts, status, failure_reason, hold_id, sell_price, sell_currency,
			estimated_buy_cost, actual_buy_cost, buy_currency, route_plan_id, provider_connection_id,
			provider_message_id, attempts, max_attempts, expires_at, updated_at,
			ingress_channel, ingress_account_id, dlr_requested,
			billing_mode_snapshot, dlr_wait_hours_snapshot, no_dlr_policy_snapshot, failed_after_accept_policy_snapshot
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11, $12,
			$13, $14, $15, $16, $17, $18, $19,
			$20, $21, $22, $23, $24,
			$25, $26, $27, $28, $29,
			$30, $31, $32,
			$33, $34, $35, $36
		)
	`
	ingressChan := msg.IngressChannel
	if ingressChan == "" {
		ingressChan = "platform"
	}
	defTerms := DefaultCustomerBillingTerms()
	if msg.BillingModeSnapshot == "" {
		msg.BillingModeSnapshot = defTerms.BillingMode
	}
	if msg.DLRWaitHoursSnapshot <= 0 {
		msg.DLRWaitHoursSnapshot = defTerms.DLRWaitHours
	}
	if msg.NoDLRPolicySnapshot == "" {
		msg.NoDLRPolicySnapshot = defTerms.NoDLRPolicy
	}
	if msg.FailedAfterAcceptPolicySnapshot == "" {
		msg.FailedAfterAcceptPolicySnapshot = defTerms.FailedAfterAcceptPolicy
	}
	batch.Queue(msgQuery,
		msg.ID, msg.CreatedAt, msg.CustomerOrgID, msg.ClientReference, msg.OriginalDestination, msg.Destination,
		msg.SenderID, msg.SenderIDType, msg.MessageType, msg.Encoding, msg.EncryptedBody, msg.KeyID,
		msg.BodyLength, msg.TotalParts, msg.Status, msg.FailureReason, msg.HoldID, msg.SellPrice, msg.SellCurrency,
		msg.EstimatedBuyCost, msg.ActualBuyCost, msg.BuyCurrency, msg.RoutePlanID, msg.ProviderConnectionID,
		msg.ProviderMessageID, msg.Attempts, msg.MaxAttempts, msg.ExpiresAt, msg.CreatedAt,
		ingressChan, msg.IngressAccountID, msg.DLRRequested,
		msg.BillingModeSnapshot, msg.DLRWaitHoursSnapshot, msg.NoDLRPolicySnapshot, msg.FailedAfterAcceptPolicySnapshot,
	)

	const locatorQuery = `
		INSERT INTO message_locator (message_id, created_at, customer_org_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (message_id) DO NOTHING
	`
	batch.Queue(locatorQuery, msg.ID, msg.CreatedAt, msg.CustomerOrgID)

	hasIdemp := msg.ClientReference != nil && *msg.ClientReference != ""
	if hasIdemp {
		const lookupQuery = `
			INSERT INTO message_idempotency_lookups (
				customer_org_id, client_reference, message_id, message_created_at, request_fingerprint, created_at
			) VALUES ($1, $2, $3, $4, $5, $6)
		`
		batch.Queue(lookupQuery, msg.CustomerOrgID, *msg.ClientReference, msg.ID, msg.CreatedAt, msg.RequestFingerprint, msg.CreatedAt)
	}

	const partQuery = `
		INSERT INTO message_parts (
			id, message_id, created_at, part_number, total_parts, udh,
			encrypted_part_body, key_id, provider_message_id, status, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`
	for i := range parts {
		if parts[i].ID == uuid.Nil {
			parts[i].ID = uuid.New()
		}
		parts[i].MessageID = msg.ID
		parts[i].CreatedAt = msg.CreatedAt
		parts[i].UpdatedAt = msg.CreatedAt
		batch.Queue(partQuery,
			parts[i].ID, parts[i].MessageID, parts[i].CreatedAt, parts[i].PartNumber, parts[i].TotalParts, parts[i].UDH,
			parts[i].EncryptedPartBody, parts[i].KeyID, parts[i].ProviderMessageID, parts[i].Status, parts[i].UpdatedAt,
		)
	}

	if outbox != nil {
		if outbox.ID == uuid.Nil {
			outbox.ID = uuid.New()
		}
		outbox.MessageID = msg.ID
		outbox.MessageCreatedAt = msg.CreatedAt
		outbox.CustomerOrgID = msg.CustomerOrgID
		outbox.CreatedAt = msg.CreatedAt
		outbox.UpdatedAt = msg.CreatedAt

		const outboxQuery = `
			INSERT INTO message_outbox (
				id, message_id, message_created_at, customer_org_id, payload,
				status, attempts, next_attempt_at, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		`
		batch.Queue(outboxQuery,
			outbox.ID, outbox.MessageID, outbox.MessageCreatedAt, outbox.CustomerOrgID, outbox.Payload,
			outbox.Status, outbox.Attempts, outbox.NextAttemptAt, outbox.CreatedAt, outbox.UpdatedAt,
		)
	}

	if event != nil {
		if event.ID == uuid.Nil {
			event.ID = uuid.New()
		}
		event.MessageID = msg.ID
		event.MessageCreatedAt = msg.CreatedAt
		event.CreatedAt = msg.CreatedAt

		const eventQuery = `
			INSERT INTO message_events (
				id, message_id, message_created_at, event_type, from_status,
				to_status, actor_type, details, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		`
		batch.Queue(eventQuery,
			event.ID, event.MessageID, event.MessageCreatedAt, event.EventType, event.FromStatus,
			event.ToStatus, event.ActorType, event.Details, event.CreatedAt,
		)
	}

	return hasIdemp
}

func readSingleMessageBatch(
	br pgx.BatchResults,
	msg *Message,
	parts []MessagePart,
	outbox *MessageOutbox,
	event *MessageEvent,
	hasIdemp bool,
) error {
	if _, err := br.Exec(); err != nil {
		return fmt.Errorf("insert message row: %w", err)
	}
	if _, err := br.Exec(); err != nil {
		return fmt.Errorf("insert message locator: %w", err)
	}
	if hasIdemp {
		if _, err := br.Exec(); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
				return fmt.Errorf("%w: client_reference %q already submitted for this customer", ErrDuplicateClientRef, *msg.ClientReference)
			}
			return fmt.Errorf("insert idempotency lookup: %w", err)
		}
	}
	for i := range parts {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("insert message part %d: %w", parts[i].PartNumber, err)
		}
	}
	if outbox != nil {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("insert outbox row: %w", err)
		}
	}
	if event != nil {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("insert initial event: %w", err)
		}
	}
	return nil
}

// CheckIdempotencyBatch resolves existing message locators for a slice of client references in one SQL query.
func (r *pgRepository) CheckIdempotencyBatch(ctx context.Context, customerOrgID uuid.UUID, refs []string) (map[string]MessageLocator, error) {
	out := make(map[string]MessageLocator)
	if len(refs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT client_reference, message_id, message_created_at
		FROM message_idempotency_lookups
		WHERE customer_org_id = $1 AND client_reference = ANY($2)
	`, customerOrgID, refs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ref string
		var loc MessageLocator
		loc.CustomerOrgID = customerOrgID
		if err := rows.Scan(&ref, &loc.MessageID, &loc.CreatedAt); err != nil {
			return nil, err
		}
		out[ref] = loc
	}
	return out, rows.Err()
}

// CheckIdempotency verifies if client_reference exists in the unpartitioned lookup table.
func (r *pgRepository) CheckIdempotency(ctx context.Context, customerOrgID uuid.UUID, clientRef string) (*uuid.UUID, *time.Time, error) {
	query := `
		SELECT message_id, message_created_at
		FROM message_idempotency_lookups
		WHERE customer_org_id = $1 AND client_reference = $2
	`
	var msgID uuid.UUID
	var createdAt time.Time
	err := r.pool.QueryRow(ctx, query, customerOrgID, clientRef).Scan(&msgID, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return &msgID, &createdAt, nil
}

// GetMessage fetches a message and its parts by composite key (id, created_at).
// GetMessageHeader fetches the message row without querying message_parts (used on hot DLR paths).
func (r *pgRepository) GetMessageHeader(ctx context.Context, id uuid.UUID, createdAt time.Time) (*Message, error) {
	query := `
		SELECT id, created_at, customer_org_id, client_reference, original_destination, destination,
		       sender_id, sender_id_type, message_type, encoding, encrypted_body, key_id,
		       body_length, total_parts, status, failure_reason, hold_id, sell_price, sell_currency,
		       estimated_buy_cost, actual_buy_cost, buy_currency, route_plan_id, provider_connection_id,
		       provider_message_id, attempts, max_attempts, expires_at, submitted_at, delivered_at, updated_at,
		       billing_mode_snapshot, dlr_wait_hours_snapshot, no_dlr_policy_snapshot, failed_after_accept_policy_snapshot
		FROM messages
		WHERE id = $1 AND created_at = $2
	`
	var m Message
	err := r.pool.QueryRow(ctx, query, id, createdAt).Scan(
		&m.ID, &m.CreatedAt, &m.CustomerOrgID, &m.ClientReference, &m.OriginalDestination, &m.Destination,
		&m.SenderID, &m.SenderIDType, &m.MessageType, &m.Encoding, &m.EncryptedBody, &m.KeyID,
		&m.BodyLength, &m.TotalParts, &m.Status, &m.FailureReason, &m.HoldID, &m.SellPrice, &m.SellCurrency,
		&m.EstimatedBuyCost, &m.ActualBuyCost, &m.BuyCurrency, &m.RoutePlanID, &m.ProviderConnectionID,
		&m.ProviderMessageID, &m.Attempts, &m.MaxAttempts, &m.ExpiresAt, &m.SubmittedAt, &m.DeliveredAt, &m.UpdatedAt,
		&m.BillingModeSnapshot, &m.DLRWaitHoursSnapshot, &m.NoDLRPolicySnapshot, &m.FailedAfterAcceptPolicySnapshot,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *pgRepository) GetMessage(ctx context.Context, id uuid.UUID, createdAt time.Time) (*Message, []MessagePart, error) {
	query := `
		SELECT id, created_at, customer_org_id, client_reference, original_destination, destination,
		       sender_id, sender_id_type, message_type, encoding, encrypted_body, key_id,
		       body_length, total_parts, status, failure_reason, hold_id, sell_price, sell_currency,
		       estimated_buy_cost, actual_buy_cost, buy_currency, route_plan_id, provider_connection_id,
		       provider_message_id, attempts, max_attempts, expires_at, submitted_at, delivered_at, updated_at,
		       billing_mode_snapshot, dlr_wait_hours_snapshot, no_dlr_policy_snapshot, failed_after_accept_policy_snapshot
		FROM messages
		WHERE id = $1 AND created_at = $2
	`
	var m Message
	err := r.pool.QueryRow(ctx, query, id, createdAt).Scan(
		&m.ID, &m.CreatedAt, &m.CustomerOrgID, &m.ClientReference, &m.OriginalDestination, &m.Destination,
		&m.SenderID, &m.SenderIDType, &m.MessageType, &m.Encoding, &m.EncryptedBody, &m.KeyID,
		&m.BodyLength, &m.TotalParts, &m.Status, &m.FailureReason, &m.HoldID, &m.SellPrice, &m.SellCurrency,
		&m.EstimatedBuyCost, &m.ActualBuyCost, &m.BuyCurrency, &m.RoutePlanID, &m.ProviderConnectionID,
		&m.ProviderMessageID, &m.Attempts, &m.MaxAttempts, &m.ExpiresAt, &m.SubmittedAt, &m.DeliveredAt, &m.UpdatedAt,
		&m.BillingModeSnapshot, &m.DLRWaitHoursSnapshot, &m.NoDLRPolicySnapshot, &m.FailedAfterAcceptPolicySnapshot,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}

	partsQuery := `
		SELECT id, message_id, created_at, part_number, total_parts, udh,
		       encrypted_part_body, key_id, provider_message_id, status, updated_at
		FROM message_parts
		WHERE message_id = $1 AND created_at = $2
		ORDER BY part_number ASC
	`
	rows, err := r.pool.Query(ctx, partsQuery, id, createdAt)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var parts []MessagePart
	for rows.Next() {
		var p MessagePart
		if err := rows.Scan(
			&p.ID, &p.MessageID, &p.CreatedAt, &p.PartNumber, &p.TotalParts, &p.UDH,
			&p.EncryptedPartBody, &p.KeyID, &p.ProviderMessageID, &p.Status, &p.UpdatedAt,
		); err != nil {
			return nil, nil, err
		}
		parts = append(parts, p)
	}

	return &m, parts, nil
}

// GetMessageByID queries messages by ID, using the unpartitioned message_locator table to prune partitions.
func (r *pgRepository) GetMessageByID(ctx context.Context, id uuid.UUID) (*Message, []MessagePart, error) {
	// 1. Try unpartitioned locator first for O(1) single-partition routing
	var createdAt time.Time
	err := r.pool.QueryRow(ctx, "SELECT created_at FROM message_locator WHERE message_id = $1", id).Scan(&createdAt)
	if err == nil {
		return r.GetMessage(ctx, id, createdAt)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, fmt.Errorf("query message locator: %w", err)
	}

	// 2. Fallback across partitions if locator has not recorded it
	query := `
		SELECT created_at
		FROM messages
		WHERE id = $1
		LIMIT 1
	`
	err = r.pool.QueryRow(ctx, query, id).Scan(&createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}

	return r.GetMessage(ctx, id, createdAt)
}

// ListMessages returns paginated messages filtered by customer org and optional status.
func (r *pgRepository) ListMessages(ctx context.Context, customerOrgID *uuid.UUID, status string, page, pageSize int) ([]Message, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	where := "WHERE 1=1"
	var args []any
	argIdx := 1

	if customerOrgID != nil {
		where += fmt.Sprintf(" AND customer_org_id = $%d", argIdx)
		args = append(args, *customerOrgID)
		argIdx++
	}
	if status != "" {
		where += fmt.Sprintf(" AND status = $%d", argIdx)
		args = append(args, status)
		argIdx++
	}

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM messages %s", where)
	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := fmt.Sprintf(`
		SELECT id, created_at, customer_org_id, client_reference, original_destination, destination,
		       sender_id, sender_id_type, message_type, encoding, key_id,
		       body_length, total_parts, status, failure_reason, hold_id, sell_price, sell_currency,
		       estimated_buy_cost, actual_buy_cost, buy_currency, route_plan_id, provider_connection_id,
		       provider_message_id, attempts, max_attempts, expires_at, submitted_at, delivered_at, updated_at,
		       billing_mode_snapshot, dlr_wait_hours_snapshot, no_dlr_policy_snapshot, failed_after_accept_policy_snapshot
		FROM messages
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d
	`, where, argIdx, argIdx+1)

	args = append(args, pageSize, offset)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(
			&m.ID, &m.CreatedAt, &m.CustomerOrgID, &m.ClientReference, &m.OriginalDestination, &m.Destination,
			&m.SenderID, &m.SenderIDType, &m.MessageType, &m.Encoding, &m.KeyID,
			&m.BodyLength, &m.TotalParts, &m.Status, &m.FailureReason, &m.HoldID, &m.SellPrice, &m.SellCurrency,
			&m.EstimatedBuyCost, &m.ActualBuyCost, &m.BuyCurrency, &m.RoutePlanID, &m.ProviderConnectionID,
			&m.ProviderMessageID, &m.Attempts, &m.MaxAttempts, &m.ExpiresAt, &m.SubmittedAt, &m.DeliveredAt, &m.UpdatedAt,
			&m.BillingModeSnapshot, &m.DLRWaitHoursSnapshot, &m.NoDLRPolicySnapshot, &m.FailedAfterAcceptPolicySnapshot,
		); err != nil {
			return nil, 0, err
		}
		msgs = append(msgs, m)
	}

	return msgs, total, nil
}

// UpdateMessageStatus transitions the message conditionally, enforcing the state machine.
func (r *pgRepository) UpdateMessageStatus(
	ctx context.Context,
	tx pgx.Tx,
	id uuid.UUID,
	createdAt time.Time,
	fromStatus, toStatus string,
	reason *string,
	buyCost *decimal.Decimal,
	connID *uuid.UUID,
	providerMsgID *string,
) error {
	if err := ValidateTransition(fromStatus, toStatus); err != nil {
		return err
	}

	now := time.Now().UTC()
	var submittedAt *time.Time
	var deliveredAt *time.Time
	if toStatus == StatusSubmitted {
		submittedAt = &now
	} else if toStatus == StatusDelivered {
		deliveredAt = &now
	}

	query := `
		UPDATE messages
		SET status = $1,
		    failure_reason = COALESCE($2, failure_reason),
		    actual_buy_cost = COALESCE($3, actual_buy_cost),
		    provider_connection_id = COALESCE($4, provider_connection_id),
		    provider_message_id = COALESCE($5, provider_message_id),
		    submitted_at = COALESCE($6, submitted_at),
		    delivered_at = COALESCE($7, delivered_at),
		    attempts = attempts + 1,
		    updated_at = $8
		WHERE id = $9 AND created_at = $10 AND status = $11
		RETURNING customer_org_id, client_reference, destination, sender_id,
		          ingress_channel, ingress_account_id, dlr_requested
	`
	var notif notifMessageInfo
	err := tx.QueryRow(ctx, query,
		toStatus, reason, buyCost, connID, providerMsgID, submittedAt, deliveredAt, now,
		id, createdAt, fromStatus,
	).Scan(
		&notif.CustomerOrgID, &notif.ClientReference, &notif.Destination, &notif.SenderID,
		&notif.IngressChannel, &notif.IngressAccountID, &notif.DLRRequested,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: message %s not in expected status %q (concurrent update or invalid transition)", ErrInvalidStateTransition, id, fromStatus)
	}
	if err != nil {
		return fmt.Errorf("update message status: %w", err)
	}
	notif.MessageID = id

	// Record lifecycle transition event
	detailsMap := map[string]any{
		"reason": reason,
	}
	if buyCost != nil {
		detailsMap["buy_cost"] = buyCost.String()
	}
	if providerMsgID != nil {
		detailsMap["provider_message_id"] = *providerMsgID
	}
	detailsJSON, _ := json.Marshal(detailsMap)

	event := MessageEvent{
		ID:               uuid.New(),
		MessageID:        id,
		MessageCreatedAt: createdAt,
		EventType:        fmt.Sprintf("status_%s", toStatus),
		FromStatus:       &fromStatus,
		ToStatus:         toStatus,
		ActorType:        "system",
		Details:          detailsJSON,
		CreatedAt:        now,
	}

	if err := r.RecordEvent(ctx, tx, event); err != nil {
		return err
	}

	if err := r.enqueueCustomerNotificationsTx(ctx, tx, notif, toStatus, reason, deliveredAt, now); err != nil {
		return err
	}
	return nil
}

// RecordAttempt stores or finalizes an upstream transmission attempt in message_attempts.
// If a pre-send 'submitting' checkpoint row exists for (message_id, attempt_number), it updates it in-place.
func (r *pgRepository) RecordAttempt(ctx context.Context, tx pgx.Tx, attempt MessageAttempt) error {
	if attempt.ID == uuid.Nil {
		attempt.ID = uuid.New()
	}
	if attempt.StartedAt.IsZero() {
		attempt.StartedAt = time.Now().UTC()
	}

	updateQuery := `
		UPDATE message_attempts
		SET connection_id = $1,
		    status = $2,
		    provider_message_id = COALESCE($3, provider_message_id),
		    error_code = COALESCE($4, error_code),
		    error_message = COALESCE($5, error_message),
		    buy_cost = COALESCE($6, buy_cost),
		    buy_currency = COALESCE($7, buy_currency),
		    latency_ms = COALESCE($8, latency_ms),
		    finished_at = COALESCE($9, now())
		WHERE message_id = $10
		  AND message_created_at = $11
		  AND attempt_number = $12
		  AND status = 'submitting'
	`
	tag, err := tx.Exec(ctx, updateQuery,
		attempt.ConnectionID, attempt.Status, attempt.ProviderMessageID,
		attempt.ErrorCode, attempt.ErrorMessage, attempt.BuyCost,
		attempt.BuyCurrency, attempt.LatencyMs, attempt.FinishedAt,
		attempt.MessageID, attempt.MessageCreatedAt, attempt.AttemptNumber,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}

	query := `
		INSERT INTO message_attempts (
			id, message_id, message_created_at, attempt_number, connection_id,
			status, provider_message_id, error_code, error_message, buy_cost,
			buy_currency, latency_ms, started_at, finished_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`
	_, err = tx.Exec(ctx, query,
		attempt.ID, attempt.MessageID, attempt.MessageCreatedAt, attempt.AttemptNumber, attempt.ConnectionID,
		attempt.Status, attempt.ProviderMessageID, attempt.ErrorCode, attempt.ErrorMessage, attempt.BuyCost,
		attempt.BuyCurrency, attempt.LatencyMs, attempt.StartedAt, attempt.FinishedAt,
	)
	return err
}

// RecordEvent appends an audit record to message_events.
func (r *pgRepository) RecordEvent(ctx context.Context, tx pgx.Tx, event MessageEvent) error {
	if event.ID == uuid.Nil {
		event.ID = uuid.New()
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}

	query := `
		INSERT INTO message_events (
			id, message_id, message_created_at, event_type, from_status,
			to_status, actor_type, details, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	_, err := tx.Exec(ctx, query,
		event.ID, event.MessageID, event.MessageCreatedAt, event.EventType, event.FromStatus,
		event.ToStatus, event.ActorType, event.Details, event.CreatedAt,
	)
	return err
}

// RecordProviderLookup writes an unpartitioned lookup row mapping (connection_id, provider_message_id) -> message.
func (r *pgRepository) RecordProviderLookup(
	ctx context.Context,
	tx pgx.Tx,
	connectionID uuid.UUID,
	providerMsgID string,
	messageID uuid.UUID,
	createdAt time.Time,
) error {
	query := `
		INSERT INTO message_provider_lookups (
			connection_id, provider_message_id, message_id, message_created_at, created_at
		) VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (connection_id, provider_message_id) DO NOTHING
	`
	_, err := tx.Exec(ctx, query, connectionID, providerMsgID, messageID, createdAt)
	return err
}

// FetchOutboxPending retrieves pending outbox rows using FOR UPDATE SKIP LOCKED.
func (r *pgRepository) FetchOutboxPending(
	ctx context.Context,
	limit int,
	workerID string,
	lockDuration time.Duration,
) ([]MessageOutbox, error) {
	if limit <= 0 {
		limit = 50
	}
	now := time.Now().UTC()
	lockUntil := now.Add(lockDuration)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	query := `
		SELECT id, message_id, message_created_at, customer_org_id, payload,
		       status, attempts, next_attempt_at, locked_until, locked_by, created_at, updated_at
		FROM message_outbox
		WHERE status IN ('pending', 'sending')
		  AND next_attempt_at <= $1
		  AND (locked_until IS NULL OR locked_until < $1)
		ORDER BY next_attempt_at ASC
		LIMIT $2
		FOR UPDATE SKIP LOCKED
	`
	rows, err := tx.Query(ctx, query, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []MessageOutbox
	for rows.Next() {
		var o MessageOutbox
		if err := rows.Scan(
			&o.ID, &o.MessageID, &o.MessageCreatedAt, &o.CustomerOrgID, &o.Payload,
			&o.Status, &o.Attempts, &o.NextAttemptAt, &o.LockedUntil, &o.LockedBy, &o.CreatedAt, &o.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, o)
	}

	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Claim rows atomically in a single roundtrip
	if len(items) > 0 {
		ids := make([]uuid.UUID, len(items))
		for i := range items {
			ids[i] = items[i].ID
			items[i].Status = "sending"
			items[i].Attempts++
		}
		claimQuery := `
			UPDATE message_outbox
			SET status = 'sending',
			    locked_until = $1,
			    locked_by = $2,
			    attempts = attempts + 1,
			    updated_at = $3
			WHERE id = ANY($4)
		`
		if _, err := tx.Exec(ctx, claimQuery, lockUntil, workerID, now, ids); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return items, nil
}

// ReleaseOutboxPending returns a claimed outbox row cleanly to 'pending' without incrementing attempt penalty
// (e.g. when a peer dispatcher process owns an exclusive SMPP carrier advisory lock).
func (r *pgRepository) ReleaseOutboxPending(ctx context.Context, outboxID uuid.UUID, delay time.Duration) error {
	now := time.Now().UTC()
	nextAt := now.Add(delay)
	_, err := r.pool.Exec(ctx, `
		UPDATE message_outbox
		SET status = 'pending',
		    locked_until = NULL,
		    locked_by = NULL,
		    attempts = GREATEST(0, attempts - 1),
		    next_attempt_at = $1,
		    updated_at = $2
		WHERE id = $3
	`, nextAt, now, outboxID)
	return err
}

// UpdateOutboxStatus updates an outbox row's terminal or retry status.
func (r *pgRepository) UpdateOutboxStatus(
	ctx context.Context,
	tx pgx.Tx,
	outboxID uuid.UUID,
	status string,
	nextAttemptAt *time.Time,
) error {
	now := time.Now().UTC()
	query := `
		UPDATE message_outbox
		SET status = $1,
		    locked_until = NULL,
		    locked_by = NULL,
		    next_attempt_at = COALESCE($2, next_attempt_at),
		    updated_at = $3
		WHERE id = $4
	`
	_, err := tx.Exec(ctx, query, status, nextAttemptAt, now, outboxID)
	return err
}

// EnsureMonthlyPartitions proactively creates the next N monthly partitions and default partition.
func (r *pgRepository) EnsureMonthlyPartitions(ctx context.Context, monthsAhead int) error {
	if monthsAhead < 1 {
		monthsAhead = 3
	}

	now := time.Now().UTC()
	startYear := now.Year()
	startMonth := now.Month()

	for i := 0; i < monthsAhead; i++ {
		monthDate := time.Date(startYear, startMonth+time.Month(i), 1, 0, 0, 0, 0, time.UTC)
		nextMonthDate := monthDate.AddDate(0, 1, 0)

		partitionSuffix := monthDate.Format("2006_01")
		fromDateStr := monthDate.Format("2006-01-02 15:04:05+00")
		toDateStr := nextMonthDate.Format("2006-01-02 15:04:05+00")

		// Create messages partition
		msgPartSQL := fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS messages_%s PARTITION OF messages
			FOR VALUES FROM ('%s') TO ('%s');
		`, partitionSuffix, fromDateStr, toDateStr)
		if _, err := r.pool.Exec(ctx, msgPartSQL); err != nil {
			return fmt.Errorf("create partition messages_%s: %w", partitionSuffix, err)
		}

		// Create message_parts partition
		partsPartSQL := fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS message_parts_%s PARTITION OF message_parts
			FOR VALUES FROM ('%s') TO ('%s');
		`, partitionSuffix, fromDateStr, toDateStr)
		if _, err := r.pool.Exec(ctx, partsPartSQL); err != nil {
			return fmt.Errorf("create partition message_parts_%s: %w", partitionSuffix, err)
		}
	}

	return nil
}

func (r *pgRepository) GetProviderLookup(ctx context.Context, connectionID uuid.UUID, providerMsgID string) (*uuid.UUID, *time.Time, error) {
	query := `
		SELECT message_id, message_created_at
		FROM message_provider_lookups
		WHERE connection_id = $1 AND provider_message_id = $2
	`
	var msgID uuid.UUID
	var createdAt time.Time
	err := r.pool.QueryRow(ctx, query, connectionID, providerMsgID).Scan(&msgID, &createdAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	return &msgID, &createdAt, nil
}

func (r *pgRepository) RecordRawDLR(ctx context.Context, tx pgx.Tx, dlr MessageDLR) error {
	if dlr.ID == uuid.Nil {
		dlr.ID = uuid.New()
	}
	if dlr.ReceivedAt.IsZero() {
		dlr.ReceivedAt = time.Now().UTC()
	}
	query := `
		INSERT INTO message_dlrs (
			id, message_id, message_created_at, connection_id, provider_message_id,
			status, error_code, raw_receipt, received_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	var err error
	if tx != nil {
		_, err = tx.Exec(ctx, query,
			dlr.ID, dlr.MessageID, dlr.MessageCreatedAt, dlr.ConnectionID,
			dlr.ProviderMessageID, dlr.Status, dlr.ErrorCode, dlr.RawReceipt, dlr.ReceivedAt,
		)
	} else {
		_, err = r.pool.Exec(ctx, query,
			dlr.ID, dlr.MessageID, dlr.MessageCreatedAt, dlr.ConnectionID,
			dlr.ProviderMessageID, dlr.Status, dlr.ErrorCode, dlr.RawReceipt, dlr.ReceivedAt,
		)
	}
	return err
}

func (r *pgRepository) InsertUnmatchedDLR(ctx context.Context, tx pgx.Tx, item UnmatchedDLR) error {
	if item.ID == uuid.Nil {
		item.ID = uuid.New()
	}
	if item.ReceivedAt.IsZero() {
		item.ReceivedAt = time.Now().UTC()
	}
	if item.MatchStatus == "" {
		item.MatchStatus = "unmatched"
	}
	query := `
		INSERT INTO unmatched_dlrs (
			id, connection_id, provider_message_id, status, error_code,
			raw_receipt, match_status, retry_count, received_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
	`
	var err error
	if tx != nil {
		_, err = tx.Exec(ctx, query,
			item.ID, item.ConnectionID, item.ProviderMessageID, item.Status, item.ErrorCode,
			item.RawReceipt, item.MatchStatus, item.RetryCount, item.ReceivedAt,
		)
	} else {
		_, err = r.pool.Exec(ctx, query,
			item.ID, item.ConnectionID, item.ProviderMessageID, item.Status, item.ErrorCode,
			item.RawReceipt, item.MatchStatus, item.RetryCount, item.ReceivedAt,
		)
	}
	return err
}

func (r *pgRepository) GetUnmatchedDLRs(ctx context.Context, page, pageSize int, statusFilter string) ([]UnmatchedDLR, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	offset := (page - 1) * pageSize

	baseWhere := "WHERE 1=1"
	var args []interface{}
	argIdx := 1
	if statusFilter != "" {
		baseWhere += fmt.Sprintf(" AND match_status = $%d", argIdx)
		args = append(args, statusFilter)
		argIdx++
	}

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM unmatched_dlrs %s", baseWhere)
	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := fmt.Sprintf(`
		SELECT id, connection_id, provider_message_id, status, error_code,
		       raw_receipt, match_status, retry_count, last_retry_at, received_at, updated_at
		FROM unmatched_dlrs
		%s
		ORDER BY received_at DESC
		LIMIT $%d OFFSET $%d
	`, baseWhere, argIdx, argIdx+1)
	args = append(args, pageSize, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var list []UnmatchedDLR
	for rows.Next() {
		var item UnmatchedDLR
		if err := rows.Scan(
			&item.ID, &item.ConnectionID, &item.ProviderMessageID, &item.Status, &item.ErrorCode,
			&item.RawReceipt, &item.MatchStatus, &item.RetryCount, &item.LastRetryAt,
			&item.ReceivedAt, &item.UpdatedAt,
		); err != nil {
			return nil, 0, err
		}
		list = append(list, item)
	}

	return list, total, nil
}

func (r *pgRepository) UpdateUnmatchedDLRStatus(ctx context.Context, id uuid.UUID, status string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE unmatched_dlrs
		SET match_status = $1, retry_count = retry_count + 1, last_retry_at = now(), updated_at = now()
		WHERE id = $2
	`, status, id)
	return err
}

func (r *pgRepository) GetDLRMappingsForConnection(ctx context.Context, connectionID uuid.UUID) ([]DLRMapping, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, connection_id, upstream_stat, upstream_err, mapped_status, description, version, created_by, created_at, updated_at
		FROM provider_connection_dlr_mappings
		WHERE connection_id = $1
	`, connectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []DLRMapping
	for rows.Next() {
		var m DLRMapping
		if err := rows.Scan(
			&m.ID, &m.ConnectionID, &m.UpstreamStat, &m.UpstreamErr, &m.MappedStatus,
			&m.Description, &m.Version, &m.CreatedBy, &m.CreatedAt, &m.UpdatedAt,
		); err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, nil
}

func (r *pgRepository) CreateDLRMapping(ctx context.Context, m *DLRMapping) error {
	if m.ID == uuid.Nil {
		m.ID = uuid.New()
	}
	m.Version = 1
	m.CreatedAt = time.Now().UTC()
	m.UpdatedAt = m.CreatedAt

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO provider_connection_dlr_mappings (
			id, connection_id, upstream_stat, upstream_err, mapped_status, description, version, created_by, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, m.ID, m.ConnectionID, m.UpstreamStat, m.UpstreamErr, m.MappedStatus, m.Description, m.Version, m.CreatedBy, m.CreatedAt, m.UpdatedAt)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO provider_connection_dlr_mappings_history (
			mapping_id, connection_id, upstream_stat, upstream_err, mapped_status, description, version, changed_by, change_type
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'create')
	`, m.ID, m.ConnectionID, m.UpstreamStat, m.UpstreamErr, m.MappedStatus, m.Description, m.Version, m.CreatedBy)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *pgRepository) UpdateDLRMapping(ctx context.Context, m *DLRMapping) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var currentVersion int
	err = tx.QueryRow(ctx, "SELECT version FROM provider_connection_dlr_mappings WHERE id = $1 FOR UPDATE", m.ID).Scan(&currentVersion)
	if err != nil {
		return err
	}

	m.Version = currentVersion + 1
	m.UpdatedAt = time.Now().UTC()

	_, err = tx.Exec(ctx, `
		UPDATE provider_connection_dlr_mappings
		SET upstream_stat = $1, upstream_err = $2, mapped_status = $3, description = $4, version = $5, updated_at = $6
		WHERE id = $7
	`, m.UpstreamStat, m.UpstreamErr, m.MappedStatus, m.Description, m.Version, m.UpdatedAt, m.ID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO provider_connection_dlr_mappings_history (
			mapping_id, connection_id, upstream_stat, upstream_err, mapped_status, description, version, changed_by, change_type
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'update')
	`, m.ID, m.ConnectionID, m.UpstreamStat, m.UpstreamErr, m.MappedStatus, m.Description, m.Version, m.CreatedBy)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *pgRepository) DeleteDLRMapping(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, "DELETE FROM provider_connection_dlr_mappings WHERE id = $1", id)
	return err
}

func (r *pgRepository) ListDLRMappings(ctx context.Context, connectionID *uuid.UUID) ([]DLRMapping, error) {
	query := `
		SELECT id, connection_id, upstream_stat, upstream_err, mapped_status, description, version, created_by, created_at, updated_at
		FROM provider_connection_dlr_mappings
	`
	var rows pgx.Rows
	var err error
	if connectionID != nil {
		query += " WHERE connection_id = $1 ORDER BY updated_at DESC"
		rows, err = r.pool.Query(ctx, query, *connectionID)
	} else {
		query += " ORDER BY updated_at DESC"
		rows, err = r.pool.Query(ctx, query)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []DLRMapping
	for rows.Next() {
		var m DLRMapping
		if err := rows.Scan(
			&m.ID, &m.ConnectionID, &m.UpstreamStat, &m.UpstreamErr, &m.MappedStatus,
			&m.Description, &m.Version, &m.CreatedBy, &m.CreatedAt, &m.UpdatedAt,
		); err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, nil
}

func (r *pgRepository) RecordSettlementException(ctx context.Context, exc SettlementException) error {
	return r.RecordSettlementExceptionTx(ctx, nil, exc)
}

func (r *pgRepository) RecordSettlementExceptionTx(ctx context.Context, tx pgx.Tx, exc SettlementException) error {
	if exc.ID == uuid.Nil {
		exc.ID = uuid.New()
	}
	if exc.CreatedAt.IsZero() {
		exc.CreatedAt = time.Now().UTC()
	}
	if exc.Severity == "" {
		exc.Severity = "warning"
	}
	if len(exc.Details) == 0 {
		exc.Details = []byte("{}")
	}

	const q = `
		INSERT INTO settlement_exceptions (
			id, message_id, message_created_at, customer_org_id, hold_id,
			exception_type, severity, details, resolved, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	if tx != nil {
		_, err := tx.Exec(ctx, q, exc.ID, exc.MessageID, exc.MessageCreatedAt, exc.CustomerOrgID, exc.HoldID,
			exc.ExceptionType, exc.Severity, exc.Details, exc.Resolved, exc.CreatedAt)
		return err
	}
	_, err := r.pool.Exec(ctx, q, exc.ID, exc.MessageID, exc.MessageCreatedAt, exc.CustomerOrgID, exc.HoldID,
		exc.ExceptionType, exc.Severity, exc.Details, exc.Resolved, exc.CreatedAt)
	return err
}

func (r *pgRepository) ListSettlementExceptions(ctx context.Context, orgID *uuid.UUID, resolved *bool, page, pageSize int) ([]SettlementException, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	offset := (page - 1) * pageSize

	baseWhere := "WHERE 1=1"
	var args []interface{}
	argIdx := 1
	if orgID != nil {
		baseWhere += fmt.Sprintf(" AND customer_org_id = $%d", argIdx)
		args = append(args, *orgID)
		argIdx++
	}
	if resolved != nil {
		baseWhere += fmt.Sprintf(" AND resolved = $%d", argIdx)
		args = append(args, *resolved)
		argIdx++
	}

	var total int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM settlement_exceptions %s", baseWhere)
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := fmt.Sprintf(`
		SELECT id, message_id, message_created_at, customer_org_id, hold_id,
		       exception_type, severity, details, resolved, resolved_by, resolved_at, created_at
		FROM settlement_exceptions
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d
	`, baseWhere, argIdx, argIdx+1)
	args = append(args, pageSize, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var list []SettlementException
	for rows.Next() {
		var exc SettlementException
		if err := rows.Scan(
			&exc.ID, &exc.MessageID, &exc.MessageCreatedAt, &exc.CustomerOrgID, &exc.HoldID,
			&exc.ExceptionType, &exc.Severity, &exc.Details, &exc.Resolved,
			&exc.ResolvedBy, &exc.ResolvedAt, &exc.CreatedAt,
		); err != nil {
			return nil, 0, err
		}
		list = append(list, exc)
	}

	return list, total, nil
}

func (r *pgRepository) ResolveSettlementException(ctx context.Context, id uuid.UUID, resolvedBy uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE settlement_exceptions
		SET resolved = true, resolved_by = $1, resolved_at = now()
		WHERE id = $2
	`, resolvedBy, id)
	return err
}

func (r *pgRepository) RecordReconciliationRun(ctx context.Context, run ReconciliationRun) error {
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}
	if len(run.SummaryJSON) == 0 {
		run.SummaryJSON = []byte("{}")
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO messaging_reconciliation_runs (
			id, started_at, completed_at, messages_checked, discrepancy_count, summary_json, initiated_by
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, run.ID, run.StartedAt, run.CompletedAt, run.MessagesChecked, run.DiscrepancyCount, run.SummaryJSON, run.InitiatedBy)
	return err
}

func (r *pgRepository) ListReconciliationRuns(ctx context.Context, limit int) ([]ReconciliationRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, started_at, completed_at, messages_checked, discrepancy_count, summary_json, initiated_by
		FROM messaging_reconciliation_runs
		ORDER BY started_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []ReconciliationRun
	for rows.Next() {
		var item ReconciliationRun
		if err := rows.Scan(
			&item.ID, &item.StartedAt, &item.CompletedAt, &item.MessagesChecked,
			&item.DiscrepancyCount, &item.SummaryJSON, &item.InitiatedBy,
		); err != nil {
			return nil, err
		}
		list = append(list, item)
	}
	return list, nil
}

func (r *pgRepository) GetQueueBacklogStats(ctx context.Context) (QueueBacklogStats, error) {
	var stats QueueBacklogStats
	err := r.pool.QueryRow(ctx, `
		SELECT 
			COUNT(*) FILTER (WHERE status = 'pending'),
			COUNT(*) FILTER (WHERE status = 'sending'),
			MIN(created_at) FILTER (WHERE status IN ('pending', 'sending'))
		FROM message_outbox
		WHERE status IN ('pending', 'sending')
	`).Scan(&stats.PendingCount, &stats.SendingCount, &stats.OldestPendingTime)
	if err != nil {
		return stats, err
	}
	if stats.OldestPendingTime != nil {
		stats.OldestAgeSeconds = time.Since(*stats.OldestPendingTime).Seconds()
	}
	return stats, nil
}

func (r *pgRepository) GetMessageAttempts(ctx context.Context, messageID uuid.UUID) ([]MessageAttempt, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, message_id, message_created_at, attempt_number, connection_id, status,
		       provider_message_id, error_code, error_message, buy_cost, buy_currency, latency_ms,
		       started_at, finished_at
		FROM message_attempts
		WHERE message_id = $1
		ORDER BY attempt_number ASC
	`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []MessageAttempt
	for rows.Next() {
		var a MessageAttempt
		if err := rows.Scan(
			&a.ID, &a.MessageID, &a.MessageCreatedAt, &a.AttemptNumber, &a.ConnectionID, &a.Status,
			&a.ProviderMessageID, &a.ErrorCode, &a.ErrorMessage, &a.BuyCost, &a.BuyCurrency, &a.LatencyMs,
			&a.StartedAt, &a.FinishedAt,
		); err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	return list, nil
}

func (r *pgRepository) GetMessageEvents(ctx context.Context, messageID uuid.UUID) ([]MessageEvent, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, message_id, message_created_at, event_type, from_status, to_status, actor_type, details, created_at
		FROM message_events
		WHERE message_id = $1
		ORDER BY created_at ASC
	`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []MessageEvent
	for rows.Next() {
		var e MessageEvent
		if err := rows.Scan(
			&e.ID, &e.MessageID, &e.MessageCreatedAt, &e.EventType, &e.FromStatus, &e.ToStatus,
			&e.ActorType, &e.Details, &e.CreatedAt,
		); err != nil {
			return nil, err
		}
		list = append(list, e)
	}
	return list, nil
}

func (r *pgRepository) GetMessageDLRs(ctx context.Context, messageID uuid.UUID) ([]MessageDLR, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, message_id, message_created_at, connection_id, provider_message_id, status, error_code, raw_receipt, received_at
		FROM message_dlrs
		WHERE message_id = $1
		ORDER BY received_at ASC
	`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []MessageDLR
	for rows.Next() {
		var d MessageDLR
		if err := rows.Scan(
			&d.ID, &d.MessageID, &d.MessageCreatedAt, &d.ConnectionID, &d.ProviderMessageID,
			&d.Status, &d.ErrorCode, &d.RawReceipt, &d.ReceivedAt,
		); err != nil {
			return nil, err
		}
		list = append(list, d)
	}
	return list, nil
}

func (r *pgRepository) FindStuckMessages(ctx context.Context, before time.Time, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, created_at, customer_org_id, client_reference, original_destination, destination,
		       sender_id, sender_id_type, message_type, encoding, body_length, total_parts,
		       status, failure_reason, hold_id, sell_price, sell_currency, estimated_buy_cost,
		       actual_buy_cost, buy_currency, route_plan_id, provider_connection_id,
		       provider_message_id, attempts, max_attempts, expires_at, submitted_at, delivered_at, updated_at,
		       billing_mode_snapshot, dlr_wait_hours_snapshot, no_dlr_policy_snapshot, failed_after_accept_policy_snapshot
		FROM messages
		WHERE status IN ('accepted', 'queued', 'submitting') AND created_at < $1
		LIMIT $2
	`, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(
			&m.ID, &m.CreatedAt, &m.CustomerOrgID, &m.ClientReference, &m.OriginalDestination, &m.Destination,
			&m.SenderID, &m.SenderIDType, &m.MessageType, &m.Encoding, &m.BodyLength, &m.TotalParts,
			&m.Status, &m.FailureReason, &m.HoldID, &m.SellPrice, &m.SellCurrency, &m.EstimatedBuyCost,
			&m.ActualBuyCost, &m.BuyCurrency, &m.RoutePlanID, &m.ProviderConnectionID,
			&m.ProviderMessageID, &m.Attempts, &m.MaxAttempts, &m.ExpiresAt, &m.SubmittedAt, &m.DeliveredAt, &m.UpdatedAt,
			&m.BillingModeSnapshot, &m.DLRWaitHoursSnapshot, &m.NoDLRPolicySnapshot, &m.FailedAfterAcceptPolicySnapshot,
		); err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, nil
}

func (r *pgRepository) FindUnknownMessages(ctx context.Context, before time.Time, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, created_at, customer_org_id, client_reference, original_destination, destination,
		       sender_id, sender_id_type, message_type, encoding, body_length, total_parts,
		       status, failure_reason, hold_id, sell_price, sell_currency, estimated_buy_cost,
		       actual_buy_cost, buy_currency, route_plan_id, provider_connection_id,
		       provider_message_id, attempts, max_attempts, expires_at, submitted_at, delivered_at, updated_at,
		       billing_mode_snapshot, dlr_wait_hours_snapshot, no_dlr_policy_snapshot, failed_after_accept_policy_snapshot
		FROM messages
		WHERE status = 'unknown' AND updated_at < $1
		LIMIT $2
	`, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(
			&m.ID, &m.CreatedAt, &m.CustomerOrgID, &m.ClientReference, &m.OriginalDestination, &m.Destination,
			&m.SenderID, &m.SenderIDType, &m.MessageType, &m.Encoding, &m.BodyLength, &m.TotalParts,
			&m.Status, &m.FailureReason, &m.HoldID, &m.SellPrice, &m.SellCurrency, &m.EstimatedBuyCost,
			&m.ActualBuyCost, &m.BuyCurrency, &m.RoutePlanID, &m.ProviderConnectionID,
			&m.ProviderMessageID, &m.Attempts, &m.MaxAttempts, &m.ExpiresAt, &m.SubmittedAt, &m.DeliveredAt, &m.UpdatedAt,
			&m.BillingModeSnapshot, &m.DLRWaitHoursSnapshot, &m.NoDLRPolicySnapshot, &m.FailedAfterAcceptPolicySnapshot,
		); err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, nil
}

// FindNoDLRSubmittedMessages finds submitted messages whose per-message snapshotted dlr_wait_hours window has elapsed asOf.
func (r *pgRepository) FindNoDLRSubmittedMessages(ctx context.Context, asOf time.Time, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, created_at, customer_org_id, client_reference, original_destination, destination,
		       sender_id, sender_id_type, message_type, encoding, body_length, total_parts,
		       status, failure_reason, hold_id, sell_price, sell_currency, estimated_buy_cost,
		       actual_buy_cost, buy_currency, route_plan_id, provider_connection_id,
		       provider_message_id, attempts, max_attempts, expires_at, submitted_at, delivered_at, updated_at,
		       billing_mode_snapshot, dlr_wait_hours_snapshot, no_dlr_policy_snapshot, failed_after_accept_policy_snapshot
		FROM messages
		WHERE status = 'submitted'
		  AND COALESCE(submitted_at, created_at) + make_interval(hours => dlr_wait_hours_snapshot) <= $1
		LIMIT $2
	`, asOf, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(
			&m.ID, &m.CreatedAt, &m.CustomerOrgID, &m.ClientReference, &m.OriginalDestination, &m.Destination,
			&m.SenderID, &m.SenderIDType, &m.MessageType, &m.Encoding, &m.BodyLength, &m.TotalParts,
			&m.Status, &m.FailureReason, &m.HoldID, &m.SellPrice, &m.SellCurrency, &m.EstimatedBuyCost,
			&m.ActualBuyCost, &m.BuyCurrency, &m.RoutePlanID, &m.ProviderConnectionID,
			&m.ProviderMessageID, &m.Attempts, &m.MaxAttempts, &m.ExpiresAt, &m.SubmittedAt, &m.DeliveredAt, &m.UpdatedAt,
			&m.BillingModeSnapshot, &m.DLRWaitHoursSnapshot, &m.NoDLRPolicySnapshot, &m.FailedAfterAcceptPolicySnapshot,
		); err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, nil
}

func (r *pgRepository) FindPendingUnmatchedDLRs(ctx context.Context, limit int) ([]UnmatchedDLR, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, connection_id, provider_message_id, status, error_code, raw_receipt, match_status, retry_count, last_retry_at, received_at, updated_at
		FROM unmatched_dlrs
		WHERE match_status = 'unmatched'
		ORDER BY received_at ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []UnmatchedDLR
	for rows.Next() {
		var item UnmatchedDLR
		if err := rows.Scan(
			&item.ID, &item.ConnectionID, &item.ProviderMessageID, &item.Status, &item.ErrorCode,
			&item.RawReceipt, &item.MatchStatus, &item.RetryCount, &item.LastRetryAt,
			&item.ReceivedAt, &item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		list = append(list, item)
	}
	return list, nil
}

func (r *pgRepository) ListMessagesAdvanced(
	ctx context.Context,
	orgID *uuid.UUID,
	status, destination, senderID string,
	connID *uuid.UUID,
	fromDate, toDate *time.Time,
	page, pageSize int,
) ([]Message, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	offset := (page - 1) * pageSize

	baseWhere := "WHERE 1=1"
	var args []interface{}
	argIdx := 1

	if orgID != nil {
		baseWhere += fmt.Sprintf(" AND customer_org_id = $%d", argIdx)
		args = append(args, *orgID)
		argIdx++
	}
	if status != "" {
		baseWhere += fmt.Sprintf(" AND status = $%d", argIdx)
		args = append(args, status)
		argIdx++
	}
	if destination != "" {
		baseWhere += fmt.Sprintf(" AND (destination LIKE $%d OR original_destination LIKE $%d)", argIdx, argIdx)
		args = append(args, "%"+destination+"%")
		argIdx++
	}
	if senderID != "" {
		baseWhere += fmt.Sprintf(" AND sender_id ILIKE $%d", argIdx)
		args = append(args, "%"+senderID+"%")
		argIdx++
	}
	if connID != nil {
		baseWhere += fmt.Sprintf(" AND provider_connection_id = $%d", argIdx)
		args = append(args, *connID)
		argIdx++
	}
	if fromDate != nil {
		baseWhere += fmt.Sprintf(" AND created_at >= $%d", argIdx)
		args = append(args, *fromDate)
		argIdx++
	}
	if toDate != nil {
		baseWhere += fmt.Sprintf(" AND created_at <= $%d", argIdx)
		args = append(args, *toDate)
		argIdx++
	}

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM messages %s", baseWhere)
	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := fmt.Sprintf(`
		SELECT id, created_at, customer_org_id, client_reference, original_destination, destination,
		       sender_id, sender_id_type, message_type, encoding, body_length, total_parts,
		       status, failure_reason, hold_id, sell_price, sell_currency, estimated_buy_cost,
		       actual_buy_cost, buy_currency, route_plan_id, provider_connection_id,
		       provider_message_id, attempts, max_attempts, expires_at, submitted_at, delivered_at, updated_at,
		       billing_mode_snapshot, dlr_wait_hours_snapshot, no_dlr_policy_snapshot, failed_after_accept_policy_snapshot
		FROM messages
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d
	`, baseWhere, argIdx, argIdx+1)
	args = append(args, pageSize, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var list []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(
			&m.ID, &m.CreatedAt, &m.CustomerOrgID, &m.ClientReference, &m.OriginalDestination, &m.Destination,
			&m.SenderID, &m.SenderIDType, &m.MessageType, &m.Encoding, &m.BodyLength, &m.TotalParts,
			&m.Status, &m.FailureReason, &m.HoldID, &m.SellPrice, &m.SellCurrency, &m.EstimatedBuyCost,
			&m.ActualBuyCost, &m.BuyCurrency, &m.RoutePlanID, &m.ProviderConnectionID,
			&m.ProviderMessageID, &m.Attempts, &m.MaxAttempts, &m.ExpiresAt, &m.SubmittedAt, &m.DeliveredAt, &m.UpdatedAt,
			&m.BillingModeSnapshot, &m.DLRWaitHoursSnapshot, &m.NoDLRPolicySnapshot, &m.FailedAfterAcceptPolicySnapshot,
		); err != nil {
			return nil, 0, err
		}
		list = append(list, m)
	}

	return list, total, nil
}

// CheckIdempotencyWithFingerprint returns the existing message and the saved request fingerprint.
func (r *pgRepository) CheckIdempotencyWithFingerprint(ctx context.Context, customerOrgID uuid.UUID, clientRef string) (*uuid.UUID, *time.Time, *string, error) {
	query := `
		SELECT message_id, message_created_at, request_fingerprint
		FROM message_idempotency_lookups
		WHERE customer_org_id = $1 AND client_reference = $2
	`
	var msgID uuid.UUID
	var createdAt time.Time
	var fingerprint *string
	err := r.pool.QueryRow(ctx, query, customerOrgID, clientRef).Scan(&msgID, &createdAt, &fingerprint)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}
	return &msgID, &createdAt, fingerprint, nil
}

// GetMessageLocator finds the message created_at partition key and customer_org_id from unpartitioned locator table.
func (r *pgRepository) GetMessageLocator(ctx context.Context, messageID uuid.UUID) (*MessageLocator, error) {
	query := `SELECT message_id, created_at, customer_org_id FROM message_locator WHERE message_id = $1`
	var loc MessageLocator
	err := r.pool.QueryRow(ctx, query, messageID).Scan(&loc.MessageID, &loc.CreatedAt, &loc.CustomerOrgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &loc, nil
}

// IncrementUsageCountersTx atomically updates daily and monthly counters and validates against caps.
func (r *pgRepository) IncrementUsageCountersTx(
	ctx context.Context,
	tx pgx.Tx,
	customerOrgID uuid.UUID,
	parts int,
	dailyCap *int64,
	monthlyCap *int64,
) error {
	now := time.Now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	if dailyCap != nil && *dailyCap > 0 {
		var count int64
		q := `
			INSERT INTO customer_usage_counters (customer_org_id, period_type, period_start, message_count, updated_at)
			VALUES ($1, 'day', $2, $3, now())
			ON CONFLICT (customer_org_id, period_type, period_start)
			DO UPDATE SET message_count = customer_usage_counters.message_count + EXCLUDED.message_count,
			              updated_at = now()
			RETURNING message_count
		`
		if err := tx.QueryRow(ctx, q, customerOrgID, dayStart, parts).Scan(&count); err != nil {
			return fmt.Errorf("increment daily usage: %w", err)
		}
		if count > *dailyCap {
			return fmt.Errorf("daily quota exceeded: %d/%d messages used", count, *dailyCap)
		}
	}

	if monthlyCap != nil && *monthlyCap > 0 {
		var count int64
		q := `
			INSERT INTO customer_usage_counters (customer_org_id, period_type, period_start, message_count, updated_at)
			VALUES ($1, 'month', $2, $3, now())
			ON CONFLICT (customer_org_id, period_type, period_start)
			DO UPDATE SET message_count = customer_usage_counters.message_count + EXCLUDED.message_count,
			              updated_at = now()
			RETURNING message_count
		`
		if err := tx.QueryRow(ctx, q, customerOrgID, monthStart, parts).Scan(&count); err != nil {
			return fmt.Errorf("increment monthly usage: %w", err)
		}
		if count > *monthlyCap {
			return fmt.Errorf("monthly quota exceeded: %d/%d messages used", count, *monthlyCap)
		}
	}

	return nil
}

// GetCustomerUsage returns current daily and monthly usage counts and configured caps.
func (r *pgRepository) GetCustomerUsage(ctx context.Context, customerOrgID uuid.UUID) (dailyCount int64, monthlyCount int64, dailyCap int64, monthlyCap int64, tpsLimit int, err error) {
	now := time.Now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	_ = r.pool.QueryRow(ctx, `
		SELECT coalesce(message_count, 0)
		FROM customer_usage_counters
		WHERE customer_org_id = $1 AND period_type = 'day' AND period_start = $2
	`, customerOrgID, dayStart).Scan(&dailyCount)

	_ = r.pool.QueryRow(ctx, `
		SELECT coalesce(message_count, 0)
		FROM customer_usage_counters
		WHERE customer_org_id = $1 AND period_type = 'month' AND period_start = $2
	`, customerOrgID, monthStart).Scan(&monthlyCount)

	var dCap, mCap *int64
	var tps *int
	row := r.pool.QueryRow(ctx, `
		SELECT daily_message_cap, monthly_message_cap, default_max_tps
		FROM customer_profiles
		WHERE organization_id = $1
	`, customerOrgID)
	if err := row.Scan(&dCap, &mCap, &tps); err == nil {
		if dCap != nil {
			dailyCap = *dCap
		}
		if mCap != nil {
			monthlyCap = *mCap
		}
		if tps != nil {
			tpsLimit = *tps
		}
	}

	return dailyCount, monthlyCount, dailyCap, monthlyCap, tpsLimit, nil
}

// GetCustomerBillingTerms reads the active per-customer billing terms from customer_profiles,
// falling back to the supplied defaults if no profile exists for the customer organization.
func (r *pgRepository) GetCustomerBillingTerms(ctx context.Context, customerOrgID uuid.UUID, defaults CustomerBillingTerms) (CustomerBillingTerms, error) {
	if defaults.BillingMode == "" {
		defaults = DefaultCustomerBillingTerms()
	}
	var out CustomerBillingTerms
	err := r.pool.QueryRow(ctx, `
		SELECT billing_mode, dlr_wait_hours, no_dlr_policy, failed_after_accept_policy
		FROM customer_profiles
		WHERE organization_id = $1
	`, customerOrgID).Scan(&out.BillingMode, &out.DLRWaitHours, &out.NoDLRPolicy, &out.FailedAfterAcceptPolicy)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return defaults, nil
		}
		return defaults, err
	}
	if out.BillingMode == "" {
		out.BillingMode = defaults.BillingMode
	}
	if out.DLRWaitHours < 1 || out.DLRWaitHours > 168 {
		out.DLRWaitHours = defaults.DLRWaitHours
	}
	if out.NoDLRPolicy == "" {
		out.NoDLRPolicy = defaults.NoDLRPolicy
	}
	if out.FailedAfterAcceptPolicy == "" {
		out.FailedAfterAcceptPolicy = defaults.FailedAfterAcceptPolicy
	}
	return out, nil
}

// TransitionToFinalStateTx executes monotonic state transition, financial settlement, audit recording,
// and atomically queues customer delivery receipts (SMPP DLR / Webhook) inside one caller transaction.
func (r *pgRepository) TransitionToFinalStateTx(
	ctx context.Context,
	tx pgx.Tx,
	ledgerSvc *ledger.Service,
	billingTrig string,
	params FinalStateParams,
) error {
	// 1. Fetch current message state FOR UPDATE targeting exact partition
	var current Message
	queryLock := `
		SELECT id, created_at, customer_org_id, client_reference, destination, sender_id,
		       status, hold_id, sell_price, sell_currency, ingress_channel, ingress_account_id, dlr_requested,
		       billing_mode_snapshot, dlr_wait_hours_snapshot, no_dlr_policy_snapshot, failed_after_accept_policy_snapshot
		FROM messages
		WHERE id = $1 AND created_at = $2
		FOR UPDATE
	`
	err := tx.QueryRow(ctx, queryLock, params.MessageID, params.MessageCreatedAt).Scan(
		&current.ID, &current.CreatedAt, &current.CustomerOrgID, &current.ClientReference,
		&current.Destination, &current.SenderID, &current.Status, &current.HoldID,
		&current.SellPrice, &current.SellCurrency, &current.IngressChannel, &current.IngressAccountID,
		&current.DLRRequested,
		&current.BillingModeSnapshot, &current.DLRWaitHoursSnapshot, &current.NoDLRPolicySnapshot, &current.FailedAfterAcceptPolicySnapshot,
	)
	if err != nil {
		return fmt.Errorf("lock message for transition: %w", err)
	}

	// Monotonic check: terminal state cannot transition again
	if IsTerminalStatus(current.Status) {
		return nil
	}

	if !CanTransition(current.Status, params.ToStatus) {
		return fmt.Errorf("invalid state transition: %s -> %s", current.Status, params.ToStatus)
	}

	now := time.Now().UTC()
	var deliveredAt *time.Time
	if params.ToStatus == StatusDelivered {
		deliveredAt = &now
	}

	// 2. Financial settlement according to immutable snapshotted billing terms
	if current.HoldID != nil && ledgerSvc != nil {
		mode := current.BillingModeSnapshot
		if mode == "" {
			if billingTrig == "submission" || billingTrig == "on_provider_accept" {
				mode = "on_provider_accept"
			} else {
				mode = "on_delivery"
			}
		}

		isOnAccept := (mode == "on_provider_accept")
		idempotencyKey := fmt.Sprintf("capture-%s", current.ID.String())

		if isOnAccept {
			switch params.ToStatus {
			case StatusSubmitted, StatusDelivered:
				_, capErr := ledgerSvc.CaptureTx(ctx, tx, *current.HoldID, current.SellPrice, &idempotencyKey)
				if capErr != nil && !errors.Is(capErr, ledger.ErrHoldNotActive) {
					return fmt.Errorf("capture hold on %s: %w", params.ToStatus, capErr)
				}
			case StatusFailed, StatusExpired, StatusUndelivered, StatusRejected:
				if params.ToStatus == StatusRejected || current.Status == StatusAccepted || current.Status == StatusQueued || current.Status == StatusSubmitting {
					// Rejected before provider acceptance: release active hold
					if relErr := ledgerSvc.ReleaseTx(ctx, tx, *current.HoldID); relErr != nil {
						return fmt.Errorf("release hold on %s: %w", params.ToStatus, relErr)
					}
				} else if current.FailedAfterAcceptPolicySnapshot == "refund" {
					refundKey := fmt.Sprintf("refund-%s", current.ID.String())
					_, refErr := ledgerSvc.RefundCaptureTx(ctx, tx, *current.HoldID, current.SellPrice, &refundKey, "Refund after failed delivery")
					if refErr != nil && !errors.Is(refErr, ledger.ErrHoldNotActive) {
						return fmt.Errorf("refund captured hold on %s: %w", params.ToStatus, refErr)
					}
				}
				// If failed_after_accept_policy == 'keep_charge', leave hold captured.
			}
		} else {
			if params.ToStatus == StatusDelivered {
				_, capErr := ledgerSvc.CaptureTx(ctx, tx, *current.HoldID, current.SellPrice, &idempotencyKey)
				if capErr != nil {
					return fmt.Errorf("capture hold on %s: %w", params.ToStatus, capErr)
				}
			} else if params.ToStatus == StatusFailed || params.ToStatus == StatusExpired || params.ToStatus == StatusUndelivered || params.ToStatus == StatusRejected {
				relErr := ledgerSvc.ReleaseTx(ctx, tx, *current.HoldID)
				if relErr != nil {
					return fmt.Errorf("release hold on %s: %w", params.ToStatus, relErr)
				}
			}
		}
	}

	// 3. Update message row
	var reasonVal *string
	if params.Reason != "" {
		reasonVal = &params.Reason
	}
	updQuery := `
		UPDATE messages
		SET status = $1,
		    failure_reason = coalesce($2, failure_reason),
		    actual_buy_cost = coalesce($3, actual_buy_cost),
		    provider_connection_id = coalesce($4, provider_connection_id),
		    provider_message_id = coalesce($5, provider_message_id),
		    delivered_at = coalesce($6, delivered_at),
		    updated_at = $7
		WHERE id = $8 AND created_at = $9
	`
	_, err = tx.Exec(ctx, updQuery,
		params.ToStatus, reasonVal, params.BuyCost, params.ConnectionID, params.ProviderMsgID,
		deliveredAt, now, params.MessageID, params.MessageCreatedAt,
	)
	if err != nil {
		return fmt.Errorf("update message status: %w", err)
	}

	// 4. Record audit event
	detailBytes, _ := json.Marshal(params.Details)
	evQuery := `
		INSERT INTO message_events (
			id, message_id, message_created_at, event_type, from_status,
			to_status, actor_type, details, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	_, err = tx.Exec(ctx, evQuery,
		uuid.New(), params.MessageID, params.MessageCreatedAt, "status_transition",
		current.Status, params.ToStatus, "system", detailBytes, now,
	)
	if err != nil {
		return fmt.Errorf("record status event: %w", err)
	}

	// 5 & 6. Customer Notification Hooks: SMPP DLR & Customer Webhooks
	notif := notifMessageInfo{
		MessageID:        params.MessageID,
		CustomerOrgID:    current.CustomerOrgID,
		ClientReference:  current.ClientReference,
		Destination:      current.Destination,
		SenderID:         current.SenderID,
		IngressChannel:   current.IngressChannel,
		IngressAccountID: current.IngressAccountID,
		DLRRequested:     current.DLRRequested,
	}
	return r.enqueueCustomerNotificationsTx(ctx, tx, notif, params.ToStatus, reasonVal, deliveredAt, now)
}

type notifMessageInfo struct {
	MessageID        uuid.UUID
	CustomerOrgID    uuid.UUID
	ClientReference  *string
	Destination      string
	SenderID         string
	IngressChannel   string
	IngressAccountID *uuid.UUID
	DLRRequested     bool
}

// enqueueCustomerNotificationsTx enqueues SMPP DLRs (for terminal outcomes) and matching customer webhooks
// atomically inside the state-transition transaction.
func (r *pgRepository) enqueueCustomerNotificationsTx(
	ctx context.Context,
	tx pgx.Tx,
	msg notifMessageInfo,
	toStatus string,
	reasonVal *string,
	deliveredAt *time.Time,
	now time.Time,
) error {
	// 1. SMPP DLR Queue (only when entering terminal state for SMPP ingress with DLR requested)
	if IsTerminalStatus(toStatus) && msg.IngressChannel == "smpp" && msg.IngressAccountID != nil && msg.DLRRequested {
		dlrPayload, _ := json.Marshal(map[string]interface{}{
			"message_id":   msg.MessageID.String(),
			"status":       toStatus,
			"reason":       reasonVal,
			"delivered_at": deliveredAt,
		})
		smppDlrQuery := `
			INSERT INTO smpp_dlr_queue (
				account_id, message_id, message_ref, client_reference,
				destination, sender_id, status, error_code, payload,
				expires_at, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
		`
		msgRef := msg.MessageID.String()
		errCode := "000"
		if toStatus != StatusDelivered {
			errCode = "045"
			if reasonVal != nil && len(*reasonVal) > 0 {
				trimmed := strings.TrimSpace(*reasonVal)
				if len(trimmed) > 16 {
					trimmed = trimmed[:16]
				}
				errCode = trimmed
			}
		}
		if _, err := tx.Exec(ctx, smppDlrQuery,
			*msg.IngressAccountID, msg.MessageID, msgRef, msg.ClientReference,
			msg.Destination, msg.SenderID, toStatus, errCode, dlrPayload,
			now.Add(24*time.Hour),
		); err != nil {
			return fmt.Errorf("enqueue smpp dlr: %w", err)
		}
	}

	// 2. Customer Webhooks matching event subscription (skip purely internal transient pre-send states)
	if toStatus == StatusSubmitting || toStatus == StatusAccepted || toStatus == StatusQueued {
		return nil
	}
	eventType := "message." + toStatus
	whQuery := `
		SELECT id, url
		FROM customer_webhooks
		WHERE customer_org_id = $1 AND status = 'active' AND events @> to_jsonb($2::text)
	`
	whRows, whErr := tx.Query(ctx, whQuery, msg.CustomerOrgID, eventType)
	if whErr != nil {
		return fmt.Errorf("query customer webhooks: %w", whErr)
	}
	type activeWH struct {
		id  uuid.UUID
		url string
	}
	var activeList []activeWH
	for whRows.Next() {
		var w activeWH
		if scanErr := whRows.Scan(&w.id, &w.url); scanErr == nil {
			activeList = append(activeList, w)
		}
	}
	whRows.Close()
	if err := whRows.Err(); err != nil {
		return fmt.Errorf("iterate customer webhooks: %w", err)
	}

	for _, wh := range activeList {
		payloadObj := map[string]interface{}{
			"event":            eventType,
			"message_id":       msg.MessageID.String(),
			"client_reference": msg.ClientReference,
			"destination":      msg.Destination,
			"sender_id":        msg.SenderID,
			"status":           toStatus,
			"reason":           reasonVal,
			"timestamp":        now.Format(time.RFC3339),
		}
		payloadJSON, _ := json.Marshal(payloadObj)
		insDeliv := `
			INSERT INTO customer_webhook_deliveries (
				id, webhook_id, customer_org_id, event_id, event_type, payload,
				status, attempts, max_attempts, next_attempt_at, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, 'pending', 0, 5, now(), now())
		`
		if _, err := tx.Exec(ctx, insDeliv, uuid.New(), wh.id, msg.CustomerOrgID, uuid.New().String(), eventType, payloadJSON); err != nil {
			return fmt.Errorf("enqueue webhook delivery: %w", err)
		}
	}

	return nil
}

// InsertSMPPDLRQueue queues an SMPP delivery receipt.
func (r *pgRepository) InsertSMPPDLRQueue(ctx context.Context, tx pgx.Tx, item SMPPDLRQueueItem) error {
	query := `
		INSERT INTO smpp_dlr_queue (
			account_id, message_id, message_ref, client_reference,
			destination, sender_id, status, error_code, payload,
			expires_at, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
	`
	var execer interface {
		Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	} = r.pool
	if tx != nil {
		execer = tx
	}
	payload := item.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	_, err := execer.Exec(ctx, query,
		item.AccountID, item.MessageID, item.MessageRef, item.ClientReference,
		item.Destination, item.SenderID, item.Status, item.ErrorCode, payload,
		item.ExpiresAt,
	)
	return err
}

// FetchPendingSMPPDLRs claims pending SMPP DLR items with a worker lease.
func (r *pgRepository) FetchPendingSMPPDLRs(ctx context.Context, limit int, leasedBy string, leaseDuration time.Duration) ([]SMPPDLRQueueItem, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	query := `
		SELECT id, account_id, message_id, message_ref, client_reference,
		       destination, sender_id, status, error_code, payload, attempts,
		       next_attempt_at, leased_until, leased_by, queue_status, expires_at, created_at, delivered_at
		FROM smpp_dlr_queue
		WHERE queue_status = 'pending'
		  AND (leased_until IS NULL OR leased_until < now())
		  AND expires_at > now()
		ORDER BY next_attempt_at ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`
	rows, err := tx.Query(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []SMPPDLRQueueItem
	var ids []uuid.UUID
	for rows.Next() {
		var it SMPPDLRQueueItem
		if err := rows.Scan(
			&it.ID, &it.AccountID, &it.MessageID, &it.MessageRef, &it.ClientReference,
			&it.Destination, &it.SenderID, &it.Status, &it.ErrorCode, &it.Payload,
			&it.Attempts, &it.NextAttemptAt, &it.LeasedUntil, &it.LeasedBy,
			&it.QueueStatus, &it.ExpiresAt, &it.CreatedAt, &it.DeliveredAt,
		); err != nil {
			return nil, err
		}
		items = append(items, it)
		ids = append(ids, it.ID)
	}
	rows.Close()

	if len(ids) > 0 {
		leaseExpiry := time.Now().UTC().Add(leaseDuration)
		_, err := tx.Exec(ctx, `
			UPDATE smpp_dlr_queue
			SET leased_by = $1, leased_until = $2, attempts = attempts + 1
			WHERE id = ANY($3)
		`, leasedBy, leaseExpiry, ids)
		if err != nil {
			return nil, err
		}
		for i := range items {
			items[i].Attempts++
		}
	}

	return items, tx.Commit(ctx)
}

// UpdateSMPPDLRQueueStatus updates the status of an SMPP DLR queue item.
func (r *pgRepository) UpdateSMPPDLRQueueStatus(ctx context.Context, id uuid.UUID, status string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE smpp_dlr_queue
		SET queue_status = $1, leased_until = NULL, leased_by = NULL,
		    delivered_at = CASE WHEN $1 = 'delivered' THEN now() ELSE delivered_at END
		WHERE id = $2
	`, status, id)
	return err
}

// UpdateSMPPDLRQueueStatusBatch updates multiple SMPP DLR queue items in one SQL roundtrip.
func (r *pgRepository) UpdateSMPPDLRQueueStatusBatch(ctx context.Context, ids []uuid.UUID, status string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE smpp_dlr_queue
		SET queue_status = $1, leased_until = NULL, leased_by = NULL,
		    delivered_at = CASE WHEN $1 = 'delivered' THEN now() ELSE delivered_at END
		WHERE id = ANY($2)
	`, status, ids)
	return err
}

// CreateWebhook persists a new customer webhook configuration.
func (r *pgRepository) CreateWebhook(ctx context.Context, wh *CustomerWebhook) error {
	if wh.ID == uuid.Nil {
		wh.ID = uuid.New()
	}
	now := time.Now().UTC()
	wh.CreatedAt = now
	wh.UpdatedAt = now
	wh.Version = 1
	if wh.Status == "" {
		wh.Status = "active"
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	eventsJSON, _ := json.Marshal(wh.Events)

	q := `
		INSERT INTO customer_webhooks (
			id, customer_org_id, url, secret_encrypted, secret_key_id, events, status,
			consecutive_failures, version, created_by, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`
	_, err = tx.Exec(ctx, q,
		wh.ID, wh.CustomerOrgID, wh.URL, wh.SecretEncrypted, wh.SecretKeyID, eventsJSON, wh.Status,
		wh.ConsecutiveFailures, wh.Version, wh.CreatedBy, wh.CreatedAt, wh.UpdatedAt,
	)
	if err != nil {
		return err
	}

	histQ := `
		INSERT INTO customer_webhooks_history (
			webhook_id, customer_org_id, url, events, status, consecutive_failures,
			version, changed_by, changed_at, change_reason
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now(), 'create')
	`
	_, err = tx.Exec(ctx, histQ,
		wh.ID, wh.CustomerOrgID, wh.URL, eventsJSON, wh.Status, wh.ConsecutiveFailures,
		wh.Version, wh.CreatedBy,
	)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// GetWebhook returns a webhook by ID and customer_org_id.
func (r *pgRepository) GetWebhook(ctx context.Context, id uuid.UUID, customerOrgID uuid.UUID) (*CustomerWebhook, error) {
	q := `
		SELECT id, customer_org_id, url, secret_encrypted, secret_key_id, events, status,
		       consecutive_failures, version, created_by, created_at, updated_at
		FROM customer_webhooks
		WHERE id = $1 AND customer_org_id = $2
	`
	var wh CustomerWebhook
	var eventsJSON []byte
	err := r.pool.QueryRow(ctx, q, id, customerOrgID).Scan(
		&wh.ID, &wh.CustomerOrgID, &wh.URL, &wh.SecretEncrypted, &wh.SecretKeyID, &eventsJSON, &wh.Status,
		&wh.ConsecutiveFailures, &wh.Version, &wh.CreatedBy, &wh.CreatedAt, &wh.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(eventsJSON, &wh.Events)
	return &wh, nil
}

// ListWebhooks returns all webhooks configured for a customer org.
func (r *pgRepository) ListWebhooks(ctx context.Context, customerOrgID uuid.UUID) ([]CustomerWebhook, error) {
	q := `
		SELECT id, customer_org_id, url, secret_encrypted, secret_key_id, events, status,
		       consecutive_failures, version, created_by, created_at, updated_at
		FROM customer_webhooks
		WHERE customer_org_id = $1
		ORDER BY created_at ASC
	`
	rows, err := r.pool.Query(ctx, q, customerOrgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []CustomerWebhook
	for rows.Next() {
		var wh CustomerWebhook
		var eventsJSON []byte
		if err := rows.Scan(
			&wh.ID, &wh.CustomerOrgID, &wh.URL, &wh.SecretEncrypted, &wh.SecretKeyID, &eventsJSON, &wh.Status,
			&wh.ConsecutiveFailures, &wh.Version, &wh.CreatedBy, &wh.CreatedAt, &wh.UpdatedAt,
		); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(eventsJSON, &wh.Events)
		list = append(list, wh)
	}
	return list, nil
}

// UpdateWebhook updates an existing webhook and writes history.
func (r *pgRepository) UpdateWebhook(ctx context.Context, wh *CustomerWebhook) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var currentVersion int
	err = tx.QueryRow(ctx, "SELECT version FROM customer_webhooks WHERE id = $1 AND customer_org_id = $2 FOR UPDATE", wh.ID, wh.CustomerOrgID).Scan(&currentVersion)
	if err != nil {
		return err
	}

	wh.Version = currentVersion + 1
	wh.UpdatedAt = time.Now().UTC()
	eventsJSON, _ := json.Marshal(wh.Events)

	q := `
		UPDATE customer_webhooks
		SET url = $1, secret_encrypted = $2, secret_key_id = $3, events = $4, status = $5,
		    consecutive_failures = $6, version = $7, updated_at = $8
		WHERE id = $9 AND customer_org_id = $10
	`
	_, err = tx.Exec(ctx, q,
		wh.URL, wh.SecretEncrypted, wh.SecretKeyID, eventsJSON, wh.Status,
		wh.ConsecutiveFailures, wh.Version, wh.UpdatedAt, wh.ID, wh.CustomerOrgID,
	)
	if err != nil {
		return err
	}

	histQ := `
		INSERT INTO customer_webhooks_history (
			webhook_id, customer_org_id, url, events, status, consecutive_failures,
			version, changed_by, changed_at, change_reason
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now(), 'update')
	`
	_, err = tx.Exec(ctx, histQ,
		wh.ID, wh.CustomerOrgID, wh.URL, eventsJSON, wh.Status, wh.ConsecutiveFailures,
		wh.Version, wh.CreatedBy,
	)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// DeleteWebhook removes a webhook configuration.
func (r *pgRepository) DeleteWebhook(ctx context.Context, id uuid.UUID, customerOrgID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, "DELETE FROM customer_webhooks WHERE id = $1 AND customer_org_id = $2", id, customerOrgID)
	return err
}

// ListWebhookDeliveries returns paginated deliveries for a customer org.
func (r *pgRepository) ListWebhookDeliveries(ctx context.Context, customerOrgID uuid.UUID, webhookID *uuid.UUID, status string, page, pageSize int) ([]CustomerWebhookDelivery, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	where := "WHERE 1=1"
	var args []any
	argIdx := 1

	if customerOrgID != uuid.Nil {
		where += fmt.Sprintf(" AND customer_org_id = $%d", argIdx)
		args = append(args, customerOrgID)
		argIdx++
	}

	if webhookID != nil {
		where += fmt.Sprintf(" AND webhook_id = $%d", argIdx)
		args = append(args, *webhookID)
		argIdx++
	}
	if status != "" {
		where += fmt.Sprintf(" AND status = $%d", argIdx)
		args = append(args, status)
		argIdx++
	}

	var total int
	if err := r.pool.QueryRow(ctx, "SELECT COUNT(*) FROM customer_webhook_deliveries "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := fmt.Sprintf(`
		SELECT id, webhook_id, customer_org_id, event_id, event_type, payload,
		       status, attempts, max_attempts, next_attempt_at, leased_until, leased_by,
		       created_at, delivered_at
		FROM customer_webhook_deliveries
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d
	`, where, argIdx, argIdx+1)
	args = append(args, pageSize, offset)

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var list []CustomerWebhookDelivery
	for rows.Next() {
		var d CustomerWebhookDelivery
		if err := rows.Scan(
			&d.ID, &d.WebhookID, &d.CustomerOrgID, &d.EventID, &d.EventType, &d.Payload,
			&d.Status, &d.Attempts, &d.MaxAttempts, &d.NextAttemptAt, &d.LeasedUntil, &d.LeasedBy,
			&d.CreatedAt, &d.DeliveredAt,
		); err != nil {
			return nil, 0, err
		}
		list = append(list, d)
	}

	return list, total, nil
}

// GetWebhookDeliveryWithAttempts returns a delivery row and its attempt history.
func (r *pgRepository) GetWebhookDeliveryWithAttempts(ctx context.Context, deliveryID uuid.UUID, customerOrgID uuid.UUID) (*CustomerWebhookDelivery, []CustomerWebhookAttempt, error) {
	q := `
		SELECT id, webhook_id, customer_org_id, event_id, event_type, payload,
		       status, attempts, max_attempts, next_attempt_at, leased_until, leased_by,
		       created_at, delivered_at
		FROM customer_webhook_deliveries
		WHERE id = $1 AND customer_org_id = $2
	`
	var d CustomerWebhookDelivery
	err := r.pool.QueryRow(ctx, q, deliveryID, customerOrgID).Scan(
		&d.ID, &d.WebhookID, &d.CustomerOrgID, &d.EventID, &d.EventType, &d.Payload,
		&d.Status, &d.Attempts, &d.MaxAttempts, &d.NextAttemptAt, &d.LeasedUntil, &d.LeasedBy,
		&d.CreatedAt, &d.DeliveredAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}

	attQ := `
		SELECT id, delivery_id, attempt_number, response_code, latency_ms,
		       response_snippet, error_class, created_at
		FROM customer_webhook_attempts
		WHERE delivery_id = $1
		ORDER BY attempt_number ASC
	`
	rows, err := r.pool.Query(ctx, attQ, deliveryID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var attempts []CustomerWebhookAttempt
	for rows.Next() {
		var a CustomerWebhookAttempt
		if err := rows.Scan(
			&a.ID, &a.DeliveryID, &a.AttemptNumber, &a.ResponseCode, &a.LatencyMs,
			&a.ResponseSnippet, &a.ErrorClass, &a.CreatedAt,
		); err != nil {
			return nil, nil, err
		}
		attempts = append(attempts, a)
	}

	return &d, attempts, nil
}

// FetchPendingWebhookDeliveries claims batches of pending webhook deliveries with a lease.
func (r *pgRepository) FetchPendingWebhookDeliveries(ctx context.Context, limit int, leasedBy string, leaseDuration time.Duration) ([]CustomerWebhookDelivery, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := `
		SELECT id, webhook_id, customer_org_id, event_id, event_type, payload,
		       status, attempts, max_attempts, next_attempt_at, leased_until, leased_by,
		       created_at, delivered_at
		FROM customer_webhook_deliveries
		WHERE status IN ('pending', 'retry')
		  AND next_attempt_at <= now()
		  AND (leased_until IS NULL OR leased_until < now())
		ORDER BY next_attempt_at ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`
	rows, err := tx.Query(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []CustomerWebhookDelivery
	var ids []uuid.UUID
	for rows.Next() {
		var d CustomerWebhookDelivery
		if err := rows.Scan(
			&d.ID, &d.WebhookID, &d.CustomerOrgID, &d.EventID, &d.EventType, &d.Payload,
			&d.Status, &d.Attempts, &d.MaxAttempts, &d.NextAttemptAt, &d.LeasedUntil, &d.LeasedBy,
			&d.CreatedAt, &d.DeliveredAt,
		); err != nil {
			return nil, err
		}
		list = append(list, d)
		ids = append(ids, d.ID)
	}
	rows.Close()

	if len(ids) > 0 {
		leaseExpiry := time.Now().UTC().Add(leaseDuration)
		_, err := tx.Exec(ctx, `
			UPDATE customer_webhook_deliveries
			SET leased_by = $1, leased_until = $2, attempts = attempts + 1
			WHERE id = ANY($3)
		`, leasedBy, leaseExpiry, ids)
		if err != nil {
			return nil, err
		}
		for i := range list {
			list[i].Attempts++
		}
	}

	return list, tx.Commit(ctx)
}

// RecordWebhookAttempt persists an attempt outcome and updates the delivery status.
func (r *pgRepository) RecordWebhookAttempt(ctx context.Context, attempt CustomerWebhookAttempt, nextStatus string, nextAttempt *time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if attempt.ID == uuid.Nil {
		attempt.ID = uuid.New()
	}
	if attempt.CreatedAt.IsZero() {
		attempt.CreatedAt = time.Now().UTC()
	}

	var deliveredAt *time.Time
	if nextStatus == "success" {
		now := time.Now().UTC()
		deliveredAt = &now
	}

	var batch pgx.Batch
	attQ := `
		INSERT INTO customer_webhook_attempts (
			id, delivery_id, attempt_number, response_code, latency_ms,
			response_snippet, error_class, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	batch.Queue(attQ,
		attempt.ID, attempt.DeliveryID, attempt.AttemptNumber, attempt.ResponseCode,
		attempt.LatencyMs, attempt.ResponseSnippet, attempt.ErrorClass, attempt.CreatedAt,
	)

	delivQ := `
		UPDATE customer_webhook_deliveries
		SET status = $1, next_attempt_at = coalesce($2, next_attempt_at), leased_until = NULL, leased_by = NULL,
		    delivered_at = coalesce($3, delivered_at)
		WHERE id = $4
	`
	batch.Queue(delivQ, nextStatus, nextAttempt, deliveredAt, attempt.DeliveryID)

	hasHookUpdate := false
	// Update consecutive failures and auto-disable status on parent webhook
	if nextStatus == "success" {
		resetQ := `
			UPDATE customer_webhooks
			SET consecutive_failures = 0, updated_at = now()
			WHERE id = (SELECT webhook_id FROM customer_webhook_deliveries WHERE id = $1)
			  AND consecutive_failures > 0
		`
		batch.Queue(resetQ, attempt.DeliveryID)
		hasHookUpdate = true
	} else if nextStatus == "failed" {
		failQ := `
			UPDATE customer_webhooks
			SET consecutive_failures = consecutive_failures + 1,
			    status = CASE WHEN consecutive_failures + 1 >= 10 THEN 'disabled_auto' ELSE status END,
			    updated_at = now()
			WHERE id = (SELECT webhook_id FROM customer_webhook_deliveries WHERE id = $1)
		`
		batch.Queue(failQ, attempt.DeliveryID)
		hasHookUpdate = true
	}

	br := tx.SendBatch(ctx, &batch)
	defer br.Close()
	if _, err := br.Exec(); err != nil {
		return err
	}
	if _, err := br.Exec(); err != nil {
		return err
	}
	if hasHookUpdate {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	if err := br.Close(); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// Inbound Numbers CRUD & matching
func (r *pgRepository) CreateInboundNumber(ctx context.Context, n *InboundNumber) error {
	if n.ID == uuid.Nil {
		n.ID = uuid.New()
	}
	now := time.Now().UTC()
	n.CreatedAt = now
	n.UpdatedAt = now
	n.Version = 1
	if n.Type == "" {
		n.Type = "longcode"
	}
	if n.Status == "" {
		n.Status = "active"
	}
	if len(n.Capabilities) == 0 {
		n.Capabilities = []string{"sms"}
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	capsJSON, _ := json.Marshal(n.Capabilities)

	q := `
		INSERT INTO inbound_numbers (
			id, number_raw, number_e164, country_id, provider_connection_id,
			customer_org_id, type, status, capabilities, version, created_by,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`
	_, err = tx.Exec(ctx, q,
		n.ID, n.NumberRaw, n.NumberE164, n.CountryID, n.ProviderConnectionID,
		n.CustomerOrgID, n.Type, n.Status, capsJSON, n.Version, n.CreatedBy,
		n.CreatedAt, n.UpdatedAt,
	)
	if err != nil {
		return err
	}

	histQ := `
		INSERT INTO inbound_numbers_history (
			inbound_number_id, number_raw, number_e164, country_id, provider_connection_id,
			customer_org_id, status, version, changed_by, changed_at, change_reason
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now(), 'create')
	`
	_, err = tx.Exec(ctx, histQ,
		n.ID, n.NumberRaw, n.NumberE164, n.CountryID, n.ProviderConnectionID,
		n.CustomerOrgID, n.Status, n.Version, n.CreatedBy,
	)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *pgRepository) UpdateInboundNumber(ctx context.Context, n *InboundNumber) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var currentVersion int
	err = tx.QueryRow(ctx, "SELECT version FROM inbound_numbers WHERE id = $1 FOR UPDATE", n.ID).Scan(&currentVersion)
	if err != nil {
		return err
	}

	n.Version = currentVersion + 1
	n.UpdatedAt = time.Now().UTC()
	capsJSON, _ := json.Marshal(n.Capabilities)

	q := `
		UPDATE inbound_numbers
		SET number_raw = $1, number_e164 = $2, country_id = $3, provider_connection_id = $4,
		    customer_org_id = $5, type = $6, status = $7, capabilities = $8, version = $9, updated_at = $10
		WHERE id = $11
	`
	_, err = tx.Exec(ctx, q,
		n.NumberRaw, n.NumberE164, n.CountryID, n.ProviderConnectionID,
		n.CustomerOrgID, n.Type, n.Status, capsJSON, n.Version, n.UpdatedAt, n.ID,
	)
	if err != nil {
		return err
	}

	histQ := `
		INSERT INTO inbound_numbers_history (
			inbound_number_id, number_raw, number_e164, country_id, provider_connection_id,
			customer_org_id, status, version, changed_by, changed_at, change_reason
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now(), 'update')
	`
	_, err = tx.Exec(ctx, histQ,
		n.ID, n.NumberRaw, n.NumberE164, n.CountryID, n.ProviderConnectionID,
		n.CustomerOrgID, n.Status, n.Version, n.CreatedBy,
	)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *pgRepository) GetInboundNumber(ctx context.Context, id uuid.UUID) (*InboundNumber, error) {
	q := `
		SELECT id, number_raw, number_e164, country_id, provider_connection_id,
		       customer_org_id, type, status, capabilities, version, created_by,
		       created_at, updated_at
		FROM inbound_numbers
		WHERE id = $1
	`
	var n InboundNumber
	var capsJSON []byte
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&n.ID, &n.NumberRaw, &n.NumberE164, &n.CountryID, &n.ProviderConnectionID,
		&n.CustomerOrgID, &n.Type, &n.Status, &capsJSON, &n.Version, &n.CreatedBy,
		&n.CreatedAt, &n.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(capsJSON, &n.Capabilities)
	return &n, nil
}

func (r *pgRepository) MatchInboundNumber(ctx context.Context, destinationE164 string) (*InboundNumber, error) {
	q := `
		SELECT id, number_raw, number_e164, country_id, provider_connection_id,
		       customer_org_id, type, status, capabilities, version, created_by,
		       created_at, updated_at
		FROM inbound_numbers
		WHERE number_e164 = $1 AND status = 'active'
	`
	var n InboundNumber
	var capsJSON []byte
	err := r.pool.QueryRow(ctx, q, destinationE164).Scan(
		&n.ID, &n.NumberRaw, &n.NumberE164, &n.CountryID, &n.ProviderConnectionID,
		&n.CustomerOrgID, &n.Type, &n.Status, &capsJSON, &n.Version, &n.CreatedBy,
		&n.CreatedAt, &n.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(capsJSON, &n.Capabilities)
	return &n, nil
}

func (r *pgRepository) ListInboundNumbers(ctx context.Context, customerOrgID *uuid.UUID, status string, page, pageSize int) ([]InboundNumber, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	where := "WHERE 1=1"
	var args []any
	argIdx := 1

	if customerOrgID != nil {
		where += fmt.Sprintf(" AND customer_org_id = $%d", argIdx)
		args = append(args, *customerOrgID)
		argIdx++
	}
	if status != "" {
		where += fmt.Sprintf(" AND status = $%d", argIdx)
		args = append(args, status)
		argIdx++
	}

	var total int
	if err := r.pool.QueryRow(ctx, "SELECT COUNT(*) FROM inbound_numbers "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := fmt.Sprintf(`
		SELECT id, number_raw, number_e164, country_id, provider_connection_id,
		       customer_org_id, type, status, capabilities, version, created_by,
		       created_at, updated_at
		FROM inbound_numbers
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d
	`, where, argIdx, argIdx+1)
	args = append(args, pageSize, offset)

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var list []InboundNumber
	for rows.Next() {
		var n InboundNumber
		var capsJSON []byte
		if err := rows.Scan(
			&n.ID, &n.NumberRaw, &n.NumberE164, &n.CountryID, &n.ProviderConnectionID,
			&n.CustomerOrgID, &n.Type, &n.Status, &capsJSON, &n.Version, &n.CreatedBy,
			&n.CreatedAt, &n.UpdatedAt,
		); err != nil {
			return nil, 0, err
		}
		_ = json.Unmarshal(capsJSON, &n.Capabilities)
		list = append(list, n)
	}

	return list, total, nil
}

// Inbound Messages persistence
func (r *pgRepository) RecordInboundMessage(ctx context.Context, msg *InboundMessage) error {
	if msg.ID == uuid.Nil {
		msg.ID = uuid.New()
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}

	query := `
		INSERT INTO inbound_messages (
			id, created_at, source_raw, source_e164, destination_raw, destination_e164,
			inbound_number_id, customer_org_id, provider_connection_id, provider_message_id,
			encrypted_body, key_id, encoding, concat_ref, concat_part, concat_total,
			dedupe_hash, status
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13, $14, $15, $16,
			$17, $18
		)
	`
	_, err := r.pool.Exec(ctx, query,
		msg.ID, msg.CreatedAt, msg.SourceRaw, msg.SourceE164, msg.DestinationRaw, msg.DestinationE164,
		msg.InboundNumberID, msg.CustomerOrgID, msg.ProviderConnectionID, msg.ProviderMessageID,
		msg.EncryptedBody, msg.KeyID, msg.Encoding, msg.ConcatRef, msg.ConcatPart, msg.ConcatTotal,
		msg.DedupeHash, msg.Status,
	)
	return err
}

func (r *pgRepository) QueueInboundWebhook(ctx context.Context, customerOrgID uuid.UUID, payload map[string]interface{}) error {
	eventType := "message.inbound"
	whQuery := `
		SELECT id
		FROM customer_webhooks
		WHERE customer_org_id = $1 AND status = 'active' AND events @> to_jsonb($2::text)
	`
	rows, err := r.pool.Query(ctx, whQuery, customerOrgID, eventType)
	if err != nil {
		return err
	}
	defer rows.Close()

	var webhookIDs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err == nil {
			webhookIDs = append(webhookIDs, id)
		}
	}

	if len(webhookIDs) == 0 {
		return nil
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	for _, whID := range webhookIDs {
		insDeliv := `
			INSERT INTO customer_webhook_deliveries (
				id, webhook_id, customer_org_id, event_id, event_type, payload,
				status, attempts, max_attempts, next_attempt_at, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, 'pending', 0, 5, now(), now())
		`
		_, _ = r.pool.Exec(ctx, insDeliv, uuid.New(), whID, customerOrgID, uuid.New().String(), eventType, payloadJSON)
	}

	return nil
}

func (r *pgRepository) ListInboundMessages(ctx context.Context, customerOrgID *uuid.UUID, page, pageSize int) ([]InboundMessage, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	where := "WHERE 1=1"
	var args []any
	argIdx := 1

	if customerOrgID != nil {
		where += fmt.Sprintf(" AND customer_org_id = $%d", argIdx)
		args = append(args, *customerOrgID)
		argIdx++
	}

	var total int
	if err := r.pool.QueryRow(ctx, "SELECT COUNT(*) FROM inbound_messages "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := fmt.Sprintf(`
		SELECT id, created_at, source_raw, source_e164, destination_raw, destination_e164,
		       inbound_number_id, customer_org_id, provider_connection_id, provider_message_id,
		       key_id, encoding, concat_ref, concat_part, concat_total, dedupe_hash, status
		FROM inbound_messages
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d
	`, where, argIdx, argIdx+1)
	args = append(args, pageSize, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var list []InboundMessage
	for rows.Next() {
		var m InboundMessage
		if err := rows.Scan(
			&m.ID, &m.CreatedAt, &m.SourceRaw, &m.SourceE164, &m.DestinationRaw, &m.DestinationE164,
			&m.InboundNumberID, &m.CustomerOrgID, &m.ProviderConnectionID, &m.ProviderMessageID,
			&m.KeyID, &m.Encoding, &m.ConcatRef, &m.ConcatPart, &m.ConcatTotal, &m.DedupeHash, &m.Status,
		); err != nil {
			return nil, 0, err
		}
		list = append(list, m)
	}
	return list, total, nil
}

// CleanExpiredData cleans terminal rows older than retentionDays. Never touches append-only tables!
func (r *pgRepository) CleanExpiredData(ctx context.Context, retentionDays int) (int64, int64, error) {
	if retentionDays <= 0 {
		retentionDays = 30
	}
	cutoff := time.Now().UTC().Add(-time.Duration(retentionDays) * 24 * time.Hour)

	// Clean delivered or expired SMPP DLR queue items
	dlrTag, err := r.pool.Exec(ctx, `
		DELETE FROM smpp_dlr_queue
		WHERE queue_status IN ('delivered', 'expired') AND created_at < $1
	`, cutoff)
	if err != nil {
		return 0, 0, fmt.Errorf("clean expired smpp dlrs: %w", err)
	}

	// Clean finalized webhook deliveries (cascades to customer_webhook_attempts)
	whTag, err := r.pool.Exec(ctx, `
		DELETE FROM customer_webhook_deliveries
		WHERE status IN ('success', 'failed') AND created_at < $1
	`, cutoff)
	if err != nil {
		return dlrTag.RowsAffected(), 0, fmt.Errorf("clean expired webhook deliveries: %w", err)
	}

	return dlrTag.RowsAffected(), whTag.RowsAffected(), nil
}
