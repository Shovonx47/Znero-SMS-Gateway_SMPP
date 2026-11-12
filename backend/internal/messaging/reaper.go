package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/internal/ledger"
)

// NoDLRHoldPolicy defines how customer wallet holds are treated when a submitted message expires without DLR.
type NoDLRHoldPolicy string

const (
	// HoldPolicyRetainUntilResolution retains the hold until the unknown-resolution window ends,
	// then releases the hold and records a settlement exception exposure flag (default, safe).
	HoldPolicyRetainUntilResolution NoDLRHoldPolicy = "retain_until_resolution"

	// HoldPolicyImmediateRelease immediately releases the customer hold upon message TTL expiry.
	HoldPolicyImmediateRelease NoDLRHoldPolicy = "immediate_release"

	// HoldPolicyCaptureAtTTL charges the customer (captures the hold) when a provider-accepted
	// message expires with no DLR receipt, rather than releasing.  Use when the platform bears
	// the supplier cost and must recover it even without delivery confirmation.
	HoldPolicyCaptureAtTTL NoDLRHoldPolicy = "capture_at_ttl"
)

// ReaperConfig contains configurable durations for the messaging reaper worker.
type ReaperConfig struct {
	MessageTTL              time.Duration    // Default 24h
	UnknownResolutionWindow time.Duration    // Default 24h
	HoldExpiry              time.Duration    // Default 72h
	UnmatchedDLRRetryWindow time.Duration    // Default 15m
	PollInterval            time.Duration    // Default 30s
	BatchLimit              int              // Default 100
	NoDLRHoldPolicy         NoDLRHoldPolicy  // Default HoldPolicyRetainUntilResolution
	Clock                   func() time.Time // Optional clock override (defaults to time.Now().UTC)
}

// Reaper executes safety sweeps, hold releases, ambiguous outcome resolution, and DLR retries.
type Reaper struct {
	pool      *pgxpool.Pool
	repo      Repository
	ledgerSvc *ledger.Service
	dlrProc   *DLRProcessor
	cfg       ReaperConfig
	clock     func() time.Time
	logger    *slog.Logger

	mu     sync.Mutex
	stopCh chan struct{}
	doneCh chan struct{}
}

// SetClock sets a custom clock function (useful for deterministic TestClock integration tests).
func (r *Reaper) SetClock(fn func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if fn == nil {
		r.clock = func() time.Time { return time.Now().UTC() }
		return
	}
	r.clock = fn
}

func (r *Reaper) now() time.Time {
	r.mu.Lock()
	fn := r.clock
	r.mu.Unlock()
	if fn != nil {
		return fn().UTC()
	}
	return time.Now().UTC()
}

// NewReaper creates a new Reaper service.
func NewReaper(
	pool *pgxpool.Pool,
	repo Repository,
	ledgerSvc *ledger.Service,
	dlrProc *DLRProcessor,
	cfg ReaperConfig,
	logger *slog.Logger,
) *Reaper {
	if cfg.MessageTTL <= 0 {
		cfg.MessageTTL = 24 * time.Hour
	}
	if cfg.UnknownResolutionWindow <= 0 {
		cfg.UnknownResolutionWindow = 24 * time.Hour
	}
	if cfg.HoldExpiry <= 0 {
		cfg.HoldExpiry = 72 * time.Hour
	}
	if cfg.UnmatchedDLRRetryWindow <= 0 {
		cfg.UnmatchedDLRRetryWindow = 15 * time.Minute
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 30 * time.Second
	}
	if cfg.BatchLimit <= 0 {
		cfg.BatchLimit = 100
	}
	if cfg.NoDLRHoldPolicy == "" {
		cfg.NoDLRHoldPolicy = HoldPolicyRetainUntilResolution
	}
	if logger == nil {
		logger = slog.Default()
	}
	clk := cfg.Clock
	if clk == nil {
		clk = func() time.Time { return time.Now().UTC() }
	}

	return &Reaper{
		pool:      pool,
		repo:      repo,
		ledgerSvc: ledgerSvc,
		dlrProc:   dlrProc,
		cfg:       cfg,
		clock:     clk,
		logger:    logger,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

// Start launches the background reaper loop guarded by a PostgreSQL advisory lock.
func (r *Reaper) Start(ctx context.Context) {
	go func() {
		defer close(r.doneCh)
		ticker := time.NewTicker(r.cfg.PollInterval)
		defer ticker.Stop()

		r.logger.Info("messaging safety reaper started",
			"poll_interval", r.cfg.PollInterval,
			"msg_ttl", r.cfg.MessageTTL,
			"unknown_window", r.cfg.UnknownResolutionWindow,
		)

		for {
			select {
			case <-ctx.Done():
				return
			case <-r.stopCh:
				return
			case <-ticker.C:
				r.RunSweep(ctx)
			}
		}
	}()
}

// Stop gracefully signals the reaper to finish.
func (r *Reaper) Stop() {
	r.mu.Lock()
	select {
	case <-r.stopCh:
	default:
		close(r.stopCh)
	}
	r.mu.Unlock()
	<-r.doneCh
}

// RunSweep performs one iteration across all reaper safety duties under an advisory lock.
func (r *Reaper) RunSweep(ctx context.Context) {
	// 1. Acquire single-runner PostgreSQL advisory lock
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		r.logger.Warn("reaper could not acquire pool connection", "err", err)
		return
	}
	defer conn.Release()

	var acquired bool
	err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, $2)", int32(8888), int32(1001)).Scan(&acquired)
	if err != nil || !acquired {
		// Another cluster node is currently executing the reaper sweep
		return
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1, $2)", int32(8888), int32(1001))
	}()

	// 2. Execute safety checks
	r.reapStuckMessages(ctx)
	r.reapUnknownMessages(ctx)
	r.reapNoDLRMessages(ctx)
	r.retryUnmatchedDLRs(ctx)
}

func (r *Reaper) reapStuckMessages(ctx context.Context) {
	now := r.now()
	cutoff := now.Add(-r.cfg.MessageTTL)
	stuckList, err := r.repo.FindStuckMessages(ctx, cutoff, r.cfg.BatchLimit)
	if err != nil {
		r.logger.Error("reaper find stuck messages failed", "err", err)
		return
	}

	for _, msg := range stuckList {
		r.reapSingleStuck(ctx, msg)
	}
}

func (r *Reaper) reapSingleStuck(ctx context.Context, msg Message) {
	r.logger.Warn("reaping stuck message past TTL", "msg_id", msg.ID, "status", msg.Status, "created_at", msg.CreatedAt)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		r.logger.Error("reaper stuck begin tx failed", "msg_id", msg.ID, "err", err)
		return
	}
	defer tx.Rollback(ctx)

	reason := "reaped past TTL in non-final status"
	if err := r.repo.UpdateMessageStatus(ctx, tx, msg.ID, msg.CreatedAt, msg.Status, StatusFailed, &reason, nil, nil, nil); err != nil {
		r.logger.Warn("reaper stuck status update failed", "msg_id", msg.ID, "err", err)
		return
	}

	if msg.HoldID != nil {
		if err := r.ledgerSvc.ReleaseTx(ctx, tx, *msg.HoldID); err != nil && !errors.Is(err, ledger.ErrHoldNotActive) {
			r.logger.Error("reaper stuck ReleaseTx failed", "msg_id", msg.ID, "err", err)
			return
		}
	}

	evtDetails, _ := json.Marshal(map[string]interface{}{"reason": reason})
	if err := r.repo.RecordEvent(ctx, tx, MessageEvent{
		MessageID:        msg.ID,
		MessageCreatedAt: msg.CreatedAt,
		EventType:        "reaper_stuck_expired",
		FromStatus:       &msg.Status,
		ToStatus:         StatusFailed,
		ActorType:        "system_reaper",
		Details:          evtDetails,
		CreatedAt:        r.now(),
	}); err != nil {
		r.logger.Error("reaper stuck RecordEvent failed", "msg_id", msg.ID, "err", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		r.logger.Error("reaper stuck commit failed", "msg_id", msg.ID, "err", err)
	}
}

func (r *Reaper) reapUnknownMessages(ctx context.Context) {
	now := r.now()
	cutoff := now.Add(-r.cfg.UnknownResolutionWindow)
	unknownList, err := r.repo.FindUnknownMessages(ctx, cutoff, r.cfg.BatchLimit)
	if err != nil {
		r.logger.Error("reaper find unknown messages failed", "err", err)
		return
	}

	for _, msg := range unknownList {
		r.reapSingleUnknown(ctx, msg)
	}
}

func (r *Reaper) reapSingleUnknown(ctx context.Context, msg Message) {
	r.logger.Warn("reaping UNKNOWN message past unknown resolution window; releasing hold and flagging exposure",
		"msg_id", msg.ID,
		"created_at", msg.CreatedAt,
	)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		r.logger.Error("reaper unknown begin tx failed", "msg_id", msg.ID, "err", err)
		return
	}
	defer tx.Rollback(ctx)

	reason := "unknown resolution window elapsed without DLR; hold released with exposure flag"
	if err := r.repo.UpdateMessageStatus(ctx, tx, msg.ID, msg.CreatedAt, StatusUnknown, StatusFailed, &reason, nil, nil, nil); err != nil {
		r.logger.Warn("reaper unknown status update failed", "msg_id", msg.ID, "err", err)
		return
	}

	if msg.HoldID != nil {
		if err := r.ledgerSvc.ReleaseTx(ctx, tx, *msg.HoldID); err != nil && !errors.Is(err, ledger.ErrHoldNotActive) {
			r.logger.Error("reaper unknown ReleaseTx failed", "msg_id", msg.ID, "err", err)
			return
		}
	}

	details, _ := json.Marshal(map[string]interface{}{
		"message_id":         msg.ID.String(),
		"customer_org_id":    msg.CustomerOrgID.String(),
		"sell_price":         msg.SellPrice.String(),
		"estimated_buy_cost": msg.EstimatedBuyCost,
		"hold_id":            msg.HoldID,
		"note":               "Provider outcome remained ambiguous past resolution window. Customer hold released; potential supplier liability exposure.",
	})
	if err := r.repo.RecordSettlementExceptionTx(ctx, tx, SettlementException{
		MessageID:        msg.ID,
		MessageCreatedAt: msg.CreatedAt,
		CustomerOrgID:    &msg.CustomerOrgID,
		HoldID:           msg.HoldID,
		ExceptionType:    "unknown_exposure",
		Severity:         "warning",
		Details:          details,
	}); err != nil {
		r.logger.Error("reaper unknown RecordSettlementExceptionTx failed", "msg_id", msg.ID, "err", err)
		return
	}

	if err := r.repo.RecordEvent(ctx, tx, MessageEvent{
		MessageID:        msg.ID,
		MessageCreatedAt: msg.CreatedAt,
		EventType:        "unknown_window_expired",
		FromStatus:       &msg.Status,
		ToStatus:         StatusFailed,
		ActorType:        "system_reaper",
		Details:          details,
		CreatedAt:        r.now(),
	}); err != nil {
		r.logger.Error("reaper unknown RecordEvent failed", "msg_id", msg.ID, "err", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		r.logger.Error("reaper unknown commit failed", "msg_id", msg.ID, "err", err)
	}
}

func (r *Reaper) reapNoDLRMessages(ctx context.Context) {
	now := r.now()
	submittedList, err := r.repo.FindNoDLRSubmittedMessages(ctx, now, r.cfg.BatchLimit)
	if err != nil {
		r.logger.Error("reaper find submitted no-DLR messages failed", "err", err)
		return
	}

	for _, msg := range submittedList {
		r.reapSingleNoDLR(ctx, now, msg)
	}
}

func (r *Reaper) reapSingleNoDLR(ctx context.Context, now time.Time, msg Message) {
	billingMode := msg.BillingModeSnapshot
	if billingMode == "" && r.dlrProc != nil {
		billingMode = r.dlrProc.billingTrig
	}
	if billingMode == "" {
		billingMode = "on_delivery"
	}
	policy := NoDLRHoldPolicy(msg.NoDLRPolicySnapshot)
	if policy == "" {
		policy = r.cfg.NoDLRHoldPolicy
	}

	r.logger.Info("reaping submitted message without DLR past per-message wait window",
		"msg_id", msg.ID,
		"submitted_at", msg.SubmittedAt,
		"dlr_wait_hours", msg.DLRWaitHoursSnapshot,
		"billing_mode", billingMode,
		"no_dlr_policy", policy,
	)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		r.logger.Error("reaper no-DLR begin tx failed", "msg_id", msg.ID, "err", err)
		return
	}
	defer tx.Rollback(ctx)

	if billingMode == "on_provider_accept" {
		reason := fmt.Sprintf("no DLR received within customer dlr_wait_hours (%dh)", msg.DLRWaitHoursSnapshot)
		if msg.FailedAfterAcceptPolicySnapshot == "refund" && msg.HoldID != nil {
			refundKey := fmt.Sprintf("reaper-refund-%s", msg.ID.String())
			_, refErr := r.ledgerSvc.RefundCaptureTx(ctx, tx, *msg.HoldID, msg.SellPrice, &refundKey, reason)
			if errors.Is(refErr, ledger.ErrHoldNotActive) {
				if relErr := r.ledgerSvc.ReleaseTx(ctx, tx, *msg.HoldID); relErr != nil && !errors.Is(relErr, ledger.ErrHoldNotActive) {
					r.logger.Error("reaper fallback ReleaseTx failed", "msg_id", msg.ID, "err", relErr)
					return
				}
			} else if refErr != nil {
				r.logger.Error("reaper RefundCaptureTx on no-DLR expiration failed", "msg_id", msg.ID, "err", refErr)
				return
			}
			details, _ := json.Marshal(map[string]interface{}{
				"message_id":      msg.ID.String(),
				"customer_org_id": msg.CustomerOrgID.String(),
				"sell_price":      msg.SellPrice.String(),
				"hold_id":         msg.HoldID,
				"dlr_wait_hours":  msg.DLRWaitHoursSnapshot,
				"note":            "No DLR within dlr_wait_hours for on_provider_accept customer with refund policy; refunded captured hold.",
			})
			_ = r.repo.RecordSettlementExceptionTx(ctx, tx, SettlementException{
				MessageID:        msg.ID,
				MessageCreatedAt: msg.CreatedAt,
				CustomerOrgID:    &msg.CustomerOrgID,
				HoldID:           msg.HoldID,
				ExceptionType:    "on_provider_accept_nodlr_refund",
				Severity:         "info",
				Details:          details,
			})
		}
		if err := r.repo.UpdateMessageStatus(ctx, tx, msg.ID, msg.CreatedAt, StatusSubmitted, StatusExpired, &reason, nil, nil, nil); err != nil {
			r.logger.Warn("reaper no-DLR status update failed", "msg_id", msg.ID, "err", err)
			return
		}
		evtDetails, _ := json.Marshal(map[string]interface{}{
			"reason":                     reason,
			"billing_mode":               billingMode,
			"failed_after_accept_policy": msg.FailedAfterAcceptPolicySnapshot,
		})
		if err := r.repo.RecordEvent(ctx, tx, MessageEvent{
			MessageID:        msg.ID,
			MessageCreatedAt: msg.CreatedAt,
			EventType:        "no_dlr_expired",
			FromStatus:       &msg.Status,
			ToStatus:         StatusExpired,
			ActorType:        "system_reaper",
			Details:          evtDetails,
			CreatedAt:        now,
		}); err != nil {
			r.logger.Error("reaper no-DLR RecordEvent failed", "msg_id", msg.ID, "err", err)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			r.logger.Error("reaper no-DLR commit failed", "msg_id", msg.ID, "err", err)
		}
		return
	}

	targetStatus := StatusExpired
	reason := fmt.Sprintf("no DLR received within customer dlr_wait_hours (%dh); applied %s", msg.DLRWaitHoursSnapshot, policy)

	switch policy {
	case HoldPolicyCaptureAtTTL:
		if err := r.repo.UpdateMessageStatus(ctx, tx, msg.ID, msg.CreatedAt, StatusSubmitted, StatusExpired, &reason, nil, nil, nil); err != nil {
			r.logger.Warn("reaper capture_at_ttl status update failed", "msg_id", msg.ID, "err", err)
			return
		}
		if msg.HoldID != nil {
			idempKey := fmt.Sprintf("reaper-capture-at-ttl-%s", msg.ID.String())
			_, captureErr := r.ledgerSvc.CaptureTx(ctx, tx, *msg.HoldID, msg.SellPrice, &idempKey)
			if captureErr != nil {
				r.logger.Error("reaper capture_at_ttl failed; hold remains open",
					"msg_id", msg.ID, "hold_id", *msg.HoldID, "err", captureErr)
			} else {
				r.logger.Info("reaper captured hold at dlr_wait_hours (capture_at_ttl policy)",
					"msg_id", msg.ID, "hold_id", *msg.HoldID, "amount", msg.SellPrice)
				details, _ := json.Marshal(map[string]interface{}{
					"message_id":      msg.ID.String(),
					"customer_org_id": msg.CustomerOrgID.String(),
					"sell_price":      msg.SellPrice.String(),
					"hold_id":         msg.HoldID,
					"dlr_wait_hours":  msg.DLRWaitHoursSnapshot,
					"note":            "No DLR within dlr_wait_hours; hold captured per snapshotted capture_at_ttl policy.",
				})
				_ = r.repo.RecordSettlementExceptionTx(ctx, tx, SettlementException{
					MessageID:        msg.ID,
					MessageCreatedAt: msg.CreatedAt,
					CustomerOrgID:    &msg.CustomerOrgID,
					HoldID:           msg.HoldID,
					ExceptionType:    "capture_at_ttl",
					Severity:         "info",
					Details:          details,
				})
			}
		}

	case HoldPolicyImmediateRelease:
		if err := r.repo.UpdateMessageStatus(ctx, tx, msg.ID, msg.CreatedAt, StatusSubmitted, StatusExpired, &reason, nil, nil, nil); err != nil {
			r.logger.Warn("reaper immediate_release status update failed", "msg_id", msg.ID, "err", err)
			return
		}
		if msg.HoldID != nil {
			if relErr := r.ledgerSvc.ReleaseTx(ctx, tx, *msg.HoldID); relErr != nil && !errors.Is(relErr, ledger.ErrHoldNotActive) {
				r.logger.Error("reaper immediate_release ReleaseTx failed", "msg_id", msg.ID, "err", relErr)
				return
			}
			details, _ := json.Marshal(map[string]interface{}{
				"message_id":      msg.ID.String(),
				"customer_org_id": msg.CustomerOrgID.String(),
				"sell_price":      msg.SellPrice.String(),
				"hold_id":         msg.HoldID,
				"dlr_wait_hours":  msg.DLRWaitHoursSnapshot,
				"note":            "No DLR within dlr_wait_hours; hold released immediately per snapshotted immediate_release policy.",
			})
			_ = r.repo.RecordSettlementExceptionTx(ctx, tx, SettlementException{
				MessageID:        msg.ID,
				MessageCreatedAt: msg.CreatedAt,
				CustomerOrgID:    &msg.CustomerOrgID,
				HoldID:           msg.HoldID,
				ExceptionType:    "immediate_release_at_ttl",
				Severity:         "info",
				Details:          details,
			})
		}

	default:
		targetStatus = StatusUnknown
		if err := r.repo.UpdateMessageStatus(ctx, tx, msg.ID, msg.CreatedAt, StatusSubmitted, StatusUnknown, &reason, nil, nil, nil); err != nil {
			r.logger.Warn("reaper retain_until_resolution status update failed", "msg_id", msg.ID, "err", err)
			return
		}
		details, _ := json.Marshal(map[string]interface{}{
			"message_id":         msg.ID.String(),
			"customer_org_id":    msg.CustomerOrgID.String(),
			"sell_price":         msg.SellPrice.String(),
			"estimated_buy_cost": msg.EstimatedBuyCost,
			"hold_id":            msg.HoldID,
			"dlr_wait_hours":     msg.DLRWaitHoursSnapshot,
			"note":               "No DLR within dlr_wait_hours; transitioned to unknown and holding funds until unknown_resolution_window ends.",
		})
		_ = r.repo.RecordSettlementExceptionTx(ctx, tx, SettlementException{
			MessageID:        msg.ID,
			MessageCreatedAt: msg.CreatedAt,
			CustomerOrgID:    &msg.CustomerOrgID,
			HoldID:           msg.HoldID,
			ExceptionType:    "no_dlr_retained_exposure",
			Severity:         "warning",
			Details:          details,
		})
	}

	evtDetails, _ := json.Marshal(map[string]interface{}{"reason": reason, "no_dlr_policy": policy})
	if err := r.repo.RecordEvent(ctx, tx, MessageEvent{
		MessageID:        msg.ID,
		MessageCreatedAt: msg.CreatedAt,
		EventType:        "no_dlr_wait_elapsed",
		FromStatus:       &msg.Status,
		ToStatus:         targetStatus,
		ActorType:        "system_reaper",
		Details:          evtDetails,
		CreatedAt:        now,
	}); err != nil {
		r.logger.Error("reaper no-DLR RecordEvent failed", "msg_id", msg.ID, "err", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		r.logger.Error("reaper no-DLR commit failed", "msg_id", msg.ID, "err", err)
	}
}

func (r *Reaper) retryUnmatchedDLRs(ctx context.Context) {
	unmatched, err := r.repo.FindPendingUnmatchedDLRs(ctx, r.cfg.BatchLimit)
	if err != nil {
		r.logger.Error("reaper find pending unmatched DLRs failed", "err", err)
		return
	}

	retryCutoff := r.now().Add(-r.cfg.UnmatchedDLRRetryWindow)

	for _, item := range unmatched {
		// Check candidate IDs
		candidates := CandidateProviderMessageIDs(item.ProviderMessageID)
		var matchedMsgID *uuid.UUID
		var matchedCreatedAt *time.Time

		if item.ConnectionID != nil {
			for _, cand := range candidates {
				msgID, createdAt, lookupErr := r.repo.GetProviderLookup(ctx, *item.ConnectionID, cand)
				if lookupErr == nil && msgID != nil {
					matchedMsgID = msgID
					matchedCreatedAt = createdAt
					break
				}
			}
		}

		if matchedMsgID != nil {
			// Found match! Parse raw SMPP receipt or fall back to structured status/error_code (e.g. HTTP callbacks).
			var pduReceipt *DLRReceipt
			if item.RawReceipt != nil {
				pduReceipt, _ = ParseDLRReceipt([]byte(*item.RawReceipt), nil)
			}
			if (pduReceipt == nil || strings.TrimSpace(pduReceipt.Stat) == "") && item.Status != "" {
				var errCode, rawRcpt string
				if item.ErrorCode != nil {
					errCode = *item.ErrorCode
				}
				if item.RawReceipt != nil {
					rawRcpt = *item.RawReceipt
				}
				pduReceipt = &DLRReceipt{
					ProviderMessageID: item.ProviderMessageID,
					Stat:              item.Status,
					ErrCode:           errCode,
					RawReceipt:        rawRcpt,
				}
			}
			if pduReceipt != nil && item.ConnectionID != nil {
				if err := r.dlrProc.applyDLRTransition(ctx, *item.ConnectionID, *matchedMsgID, *matchedCreatedAt, pduReceipt); err != nil {
					r.logger.Warn("unmatched DLR transition failed on retry", "unmatched_id", item.ID, "err", err)
					continue
				}
			}
			_ = r.repo.UpdateUnmatchedDLRStatus(ctx, item.ID, "matched")
			r.logger.Info("unmatched DLR resolved on retry", "unmatched_id", item.ID, "msg_id", *matchedMsgID)
		} else if item.ReceivedAt.Before(retryCutoff) {
			// Window expired without match: flag unmatched
			_ = r.repo.UpdateUnmatchedDLRStatus(ctx, item.ID, "flagged_unmatched")
			r.logger.Warn("DLR flagged as unmatched after retry window expired",
				"unmatched_id", item.ID,
				"prov_msg_id", item.ProviderMessageID,
				"received_at", item.ReceivedAt,
			)
		}
	}
}
