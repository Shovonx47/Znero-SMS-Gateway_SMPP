package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

// Repository specifies the persistence interface for messaging operations.
type Repository interface {
	InsertMessageAtomic(ctx context.Context, tx pgx.Tx, msg *Message, parts []MessagePart, outbox *MessageOutbox, event *MessageEvent) error
	CheckIdempotency(ctx context.Context, customerOrgID uuid.UUID, clientRef string) (*uuid.UUID, *time.Time, error)
	GetMessage(ctx context.Context, id uuid.UUID, createdAt time.Time) (*Message, []MessagePart, error)
	GetMessageByID(ctx context.Context, id uuid.UUID) (*Message, []MessagePart, error)
	ListMessages(ctx context.Context, customerOrgID *uuid.UUID, status string, page, pageSize int) ([]Message, int, error)
	UpdateMessageStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, createdAt time.Time, fromStatus, toStatus string, reason *string, buyCost *decimal.Decimal, connID *uuid.UUID, providerMsgID *string) error
	RecordAttempt(ctx context.Context, tx pgx.Tx, attempt MessageAttempt) error
	RecordEvent(ctx context.Context, tx pgx.Tx, event MessageEvent) error
	RecordProviderLookup(ctx context.Context, tx pgx.Tx, connectionID uuid.UUID, providerMsgID string, messageID uuid.UUID, createdAt time.Time) error
	FetchOutboxPending(ctx context.Context, limit int, workerID string, lockDuration time.Duration) ([]MessageOutbox, error)
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
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	if msg.ID == uuid.Nil {
		msg.ID = uuid.New()
	}

	// 1. Insert into partitioned messages table
	msgQuery := `
		INSERT INTO messages (
			id, created_at, customer_org_id, client_reference, original_destination, destination,
			sender_id, sender_id_type, message_type, encoding, encrypted_body, key_id,
			body_length, total_parts, status, failure_reason, hold_id, sell_price, sell_currency,
			estimated_buy_cost, actual_buy_cost, buy_currency, route_plan_id, provider_connection_id,
			provider_message_id, attempts, max_attempts, expires_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11, $12,
			$13, $14, $15, $16, $17, $18, $19,
			$20, $21, $22, $23, $24,
			$25, $26, $27, $28, $29
		)
	`
	_, err := tx.Exec(ctx, msgQuery,
		msg.ID, msg.CreatedAt, msg.CustomerOrgID, msg.ClientReference, msg.OriginalDestination, msg.Destination,
		msg.SenderID, msg.SenderIDType, msg.MessageType, msg.Encoding, msg.EncryptedBody, msg.KeyID,
		msg.BodyLength, msg.TotalParts, msg.Status, msg.FailureReason, msg.HoldID, msg.SellPrice, msg.SellCurrency,
		msg.EstimatedBuyCost, msg.ActualBuyCost, msg.BuyCurrency, msg.RoutePlanID, msg.ProviderConnectionID,
		msg.ProviderMessageID, msg.Attempts, msg.MaxAttempts, msg.ExpiresAt, msg.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert message row: %w", err)
	}

	// 2. Insert unpartitioned idempotency lookup (Amendment 2: enforces uniqueness across month boundaries)
	// (Must be inserted after messages row so fk_message_idempotency_target constraint is satisfied)
	if msg.ClientReference != nil && *msg.ClientReference != "" {
		lookupQuery := `
			INSERT INTO message_idempotency_lookups (
				customer_org_id, client_reference, message_id, message_created_at, created_at
			) VALUES ($1, $2, $3, $4, $5)
		`
		_, err := tx.Exec(ctx, lookupQuery,
			msg.CustomerOrgID, *msg.ClientReference, msg.ID, msg.CreatedAt, msg.CreatedAt,
		)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
				return fmt.Errorf("%w: client_reference %q already submitted for this customer", ErrDuplicateClientRef, *msg.ClientReference)
			}
			return fmt.Errorf("insert idempotency lookup: %w", err)
		}
	}

	// 3. Insert parts
	partQuery := `
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
		_, err := tx.Exec(ctx, partQuery,
			parts[i].ID, parts[i].MessageID, parts[i].CreatedAt, parts[i].PartNumber, parts[i].TotalParts, parts[i].UDH,
			parts[i].EncryptedPartBody, parts[i].KeyID, parts[i].ProviderMessageID, parts[i].Status, parts[i].UpdatedAt,
		)
		if err != nil {
			return fmt.Errorf("insert message part %d: %w", parts[i].PartNumber, err)
		}
	}

	// 4. Insert outbox row
	if outbox != nil {
		if outbox.ID == uuid.Nil {
			outbox.ID = uuid.New()
		}
		outbox.MessageID = msg.ID
		outbox.MessageCreatedAt = msg.CreatedAt
		outbox.CustomerOrgID = msg.CustomerOrgID
		outbox.CreatedAt = msg.CreatedAt
		outbox.UpdatedAt = msg.CreatedAt

		outboxQuery := `
			INSERT INTO message_outbox (
				id, message_id, message_created_at, customer_org_id, payload,
				status, attempts, next_attempt_at, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		`
		_, err := tx.Exec(ctx, outboxQuery,
			outbox.ID, outbox.MessageID, outbox.MessageCreatedAt, outbox.CustomerOrgID, outbox.Payload,
			outbox.Status, outbox.Attempts, outbox.NextAttemptAt, outbox.CreatedAt, outbox.UpdatedAt,
		)
		if err != nil {
			return fmt.Errorf("insert outbox row: %w", err)
		}
	}

	// 5. Insert initial event
	if event != nil {
		if event.ID == uuid.Nil {
			event.ID = uuid.New()
		}
		event.MessageID = msg.ID
		event.MessageCreatedAt = msg.CreatedAt
		event.CreatedAt = msg.CreatedAt

		eventQuery := `
			INSERT INTO message_events (
				id, message_id, message_created_at, event_type, from_status,
				to_status, actor_type, details, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		`
		_, err := tx.Exec(ctx, eventQuery,
			event.ID, event.MessageID, event.MessageCreatedAt, event.EventType, event.FromStatus,
			event.ToStatus, event.ActorType, event.Details, event.CreatedAt,
		)
		if err != nil {
			return fmt.Errorf("insert initial event: %w", err)
		}
	}

	return nil
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
func (r *pgRepository) GetMessage(ctx context.Context, id uuid.UUID, createdAt time.Time) (*Message, []MessagePart, error) {
	query := `
		SELECT id, created_at, customer_org_id, client_reference, original_destination, destination,
		       sender_id, sender_id_type, message_type, encoding, encrypted_body, key_id,
		       body_length, total_parts, status, failure_reason, hold_id, sell_price, sell_currency,
		       estimated_buy_cost, actual_buy_cost, buy_currency, route_plan_id, provider_connection_id,
		       provider_message_id, attempts, max_attempts, expires_at, submitted_at, delivered_at, updated_at
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

// GetMessageByID queries messages across partitions without knowing created_at.
func (r *pgRepository) GetMessageByID(ctx context.Context, id uuid.UUID) (*Message, []MessagePart, error) {
	query := `
		SELECT id, created_at, customer_org_id, client_reference, original_destination, destination,
		       sender_id, sender_id_type, message_type, encoding, encrypted_body, key_id,
		       body_length, total_parts, status, failure_reason, hold_id, sell_price, sell_currency,
		       estimated_buy_cost, actual_buy_cost, buy_currency, route_plan_id, provider_connection_id,
		       provider_message_id, attempts, max_attempts, expires_at, submitted_at, delivered_at, updated_at
		FROM messages
		WHERE id = $1
		LIMIT 1
	`
	var m Message
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&m.ID, &m.CreatedAt, &m.CustomerOrgID, &m.ClientReference, &m.OriginalDestination, &m.Destination,
		&m.SenderID, &m.SenderIDType, &m.MessageType, &m.Encoding, &m.EncryptedBody, &m.KeyID,
		&m.BodyLength, &m.TotalParts, &m.Status, &m.FailureReason, &m.HoldID, &m.SellPrice, &m.SellCurrency,
		&m.EstimatedBuyCost, &m.ActualBuyCost, &m.BuyCurrency, &m.RoutePlanID, &m.ProviderConnectionID,
		&m.ProviderMessageID, &m.Attempts, &m.MaxAttempts, &m.ExpiresAt, &m.SubmittedAt, &m.DeliveredAt, &m.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}

	return r.GetMessage(ctx, m.ID, m.CreatedAt)
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
		       provider_message_id, attempts, max_attempts, expires_at, submitted_at, delivered_at, updated_at
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
	`
	cmd, err := tx.Exec(ctx, query,
		toStatus, reason, buyCost, connID, providerMsgID, submittedAt, deliveredAt, now,
		id, createdAt, fromStatus,
	)
	if err != nil {
		return fmt.Errorf("update message status: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("%w: message %s not in expected status %q (concurrent update or invalid transition)", ErrInvalidStateTransition, id, fromStatus)
	}

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

	return r.RecordEvent(ctx, tx, event)
}

// RecordAttempt stores an upstream transmission attempt in message_attempts.
func (r *pgRepository) RecordAttempt(ctx context.Context, tx pgx.Tx, attempt MessageAttempt) error {
	if attempt.ID == uuid.Nil {
		attempt.ID = uuid.New()
	}
	if attempt.StartedAt.IsZero() {
		attempt.StartedAt = time.Now().UTC()
	}

	query := `
		INSERT INTO message_attempts (
			id, message_id, message_created_at, attempt_number, connection_id,
			status, provider_message_id, error_code, error_message, buy_cost,
			buy_currency, latency_ms, started_at, finished_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`
	_, err := tx.Exec(ctx, query,
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

	// Claim rows atomically
	for i := range items {
		claimQuery := `
			UPDATE message_outbox
			SET status = 'sending',
			    locked_until = $1,
			    locked_by = $2,
			    attempts = attempts + 1,
			    updated_at = $3
			WHERE id = $4
		`
		_, err := tx.Exec(ctx, claimQuery, lockUntil, workerID, now, items[i].ID)
		if err != nil {
			return nil, err
		}
		items[i].Status = "sending"
		items[i].Attempts++
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return items, nil
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

	_, err := r.pool.Exec(ctx, `
		INSERT INTO settlement_exceptions (
			id, message_id, message_created_at, customer_org_id, hold_id,
			exception_type, severity, details, resolved, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, exc.ID, exc.MessageID, exc.MessageCreatedAt, exc.CustomerOrgID, exc.HoldID,
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
		       provider_message_id, attempts, max_attempts, expires_at, submitted_at, delivered_at, updated_at
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
		       provider_message_id, attempts, max_attempts, expires_at, submitted_at, delivered_at, updated_at
		FROM messages
		WHERE status = 'unknown' AND created_at < $1
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
		); err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, nil
}

func (r *pgRepository) FindNoDLRSubmittedMessages(ctx context.Context, before time.Time, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, created_at, customer_org_id, client_reference, original_destination, destination,
		       sender_id, sender_id_type, message_type, encoding, body_length, total_parts,
		       status, failure_reason, hold_id, sell_price, sell_currency, estimated_buy_cost,
		       actual_buy_cost, buy_currency, route_plan_id, provider_connection_id,
		       provider_message_id, attempts, max_attempts, expires_at, submitted_at, delivered_at, updated_at
		FROM messages
		WHERE status = 'submitted' AND created_at < $1
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
		       provider_message_id, attempts, max_attempts, expires_at, submitted_at, delivered_at, updated_at
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
		); err != nil {
			return nil, 0, err
		}
		list = append(list, m)
	}

	return list, total, nil
}
