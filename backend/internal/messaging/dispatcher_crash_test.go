package messaging_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/messaging"
)

type spyPreSendSender struct {
	t      *testing.T
	kit    *msgKit
	calls  atomic.Int32
	sawSub atomic.Bool
}

func (s *spyPreSendSender) Send(ctx context.Context, attempt messaging.SendAttempt) (messaging.SendResult, error) {
	s.calls.Add(1)
	// BUG-035 invariant: messages row and message_attempts ('submitting') checkpoint MUST be
	// durably committed in PostgreSQL BEFORE Send ever touches the upstream network/socket.
	var msgSt, attSt string
	err := s.kit.e.Pool.QueryRow(ctx, `
		SELECT m.status, a.status
		FROM messages m
		JOIN message_attempts a ON a.message_id = m.id AND a.message_created_at = m.created_at
		WHERE m.id = $1 AND a.attempt_number = 1
	`, attempt.MessageID).Scan(&msgSt, &attSt)
	if err == nil && msgSt == messaging.StatusSubmitting && attSt == "submitting" {
		s.sawSub.Store(true)
	}
	return s.kit.fakeSender.Send(ctx, attempt)
}

func TestDispatcher_PreSendCheckpointAndCrashBlocksDuplicateSend(t *testing.T) {
	mk := newMsgKit(t)
	ctx := context.Background()

	// Part A: Verify pre-send 'submitting' checkpoint is committed BEFORE Sender.Send is invoked.
	spy := &spyPreSendSender{t: t, kit: mk}
	d1 := messaging.NewDispatcher(mk.e.Pool, mk.repo, mk.routingEngine, mk.ledgerSvc, spy, nil)

	res1, err := mk.svc.Submit(ctx, mk.custUser, messaging.SubmitRequest{
		Destination: "+12025550191",
		SenderID:    "TESTSMS",
		Text:        "Pre-send checkpoint test",
	})
	if err != nil {
		t.Fatalf("submit res1: %v", err)
	}
	if n := d1.ProcessBatch(ctx); n != 1 {
		t.Fatalf("expected 1 outbox item processed, got %d", n)
	}
	if !spy.sawSub.Load() {
		t.Fatal("expected durable messages.status='submitting' + message_attempts.status='submitting' BEFORE Sender.Send ran")
	}
	msg1, _, err := mk.repo.GetMessage(ctx, res1.MessageID, res1.CreatedAt)
	if err != nil || msg1 == nil || msg1.Status != messaging.StatusSubmitted {
		t.Fatalf("expected res1 to reach 'submitted', got %+v (err=%v)", msg1, err)
	}

	// Part B: Simulate Worker #1 crash (kill -9) right after committing pre-send checkpoint
	// (outbox lock expired while attempt 1 is still 'submitting' in DB).
	// Worker #2 restarts and reclaims the expired outbox item: MUST NOT re-send!
	spy2 := &spyPreSendSender{t: t, kit: mk}
	d2 := messaging.NewDispatcher(mk.e.Pool, mk.repo, mk.routingEngine, mk.ledgerSvc, spy2, nil)

	res2, err := mk.svc.Submit(ctx, mk.custUser, messaging.SubmitRequest{
		Destination: "+12025550192",
		SenderID:    "TESTSMS",
		Text:        "Crash recovery at-most-once test",
	})
	if err != nil {
		t.Fatalf("submit res2: %v", err)
	}

	tx, err := mk.e.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)

	reason := "simulated worker crash during dispatch"
	if err := mk.repo.UpdateMessageStatus(ctx, tx, res2.MessageID, res2.CreatedAt, messaging.StatusAccepted, messaging.StatusSubmitting, &reason, nil, &mk.provConnID1, nil); err != nil {
		t.Fatalf("set submitting: %v", err)
	}
	if err := mk.repo.RecordAttempt(ctx, tx, messaging.MessageAttempt{
		ID:               uuid.New(),
		MessageID:        res2.MessageID,
		MessageCreatedAt: res2.CreatedAt,
		AttemptNumber:    1,
		ConnectionID:     mk.provConnID1,
		Status:           "submitting",
	}); err != nil {
		t.Fatalf("insert submitting attempt: %v", err)
	}
	// Make outbox lock expired so d2 immediately reclaims it
	if _, err := tx.Exec(ctx, `UPDATE message_outbox SET locked_until = now() - interval '1 second', locked_by = 'dead-worker' WHERE message_id = $1`, res2.MessageID); err != nil {
		t.Fatalf("expire outbox lock: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit simulated crash state: %v", err)
	}

	if n := d2.ProcessBatch(ctx); n != 1 {
		t.Fatalf("expected crashed outbox row to be reclaimed cleanly (got %d)", n)
	}
	if spy2.calls.Load() != 0 {
		t.Fatalf("BUG-035 REGRESSION: crashed in-flight attempt must NEVER be re-sent on restart (got %d calls to Send)", spy2.calls.Load())
	}

	msg2, _, err := mk.repo.GetMessage(ctx, res2.MessageID, res2.CreatedAt)
	if err != nil || msg2 == nil {
		t.Fatalf("get res2: %v", err)
	}
	if msg2.Status != messaging.StatusUnknown {
		t.Fatalf("expected crashed message to transition to 'unknown', got %q", msg2.Status)
	}

	var holdStatus string
	if err := mk.e.Pool.QueryRow(ctx, `SELECT status FROM holds WHERE id = $1`, *msg2.HoldID).Scan(&holdStatus); err != nil {
		t.Fatalf("query hold: %v", err)
	}
	if holdStatus != "active" {
		t.Fatalf("expected hold to remain active on ambiguous crash outcome, got %q", holdStatus)
	}

	var excCount int
	if err := mk.e.Pool.QueryRow(ctx, `SELECT count(*) FROM settlement_exceptions WHERE message_id = $1 AND exception_type = 'crash_during_dispatch'`, res2.MessageID).Scan(&excCount); err != nil || excCount != 1 {
		t.Fatalf("expected 1 crash_during_dispatch settlement exception, got %d (err=%v)", excCount, err)
	}
}
