package messaging

import (
	"context"
	"encoding/json"
	"log/slog"
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
)

// ReaperConfig contains configurable durations for the messaging reaper worker.
type ReaperConfig struct {
	MessageTTL              time.Duration   // Default 24h
	UnknownResolutionWindow time.Duration   // Default 24h
	HoldExpiry              time.Duration   // Default 72h
	UnmatchedDLRRetryWindow time.Duration   // Default 15m
	PollInterval            time.Duration   // Default 30s
	BatchLimit              int             // Default 100
	NoDLRHoldPolicy         NoDLRHoldPolicy // Default HoldPolicyRetainUntilResolution
}

// Reaper executes safety sweeps, hold releases, ambiguous outcome resolution, and DLR retries.
type Reaper struct {
	pool      *pgxpool.Pool
	repo      Repository
	ledgerSvc *ledger.Service
	dlrProc   *DLRProcessor
	cfg       ReaperConfig
	logger    *slog.Logger

	mu     sync.Mutex
	stopCh chan struct{}
	doneCh chan struct{}
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

	return &Reaper{
		pool:      pool,
		repo:      repo,
		ledgerSvc: ledgerSvc,
		dlrProc:   dlrProc,
		cfg:       cfg,
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
	cutoff := time.Now().UTC().Add(-r.cfg.MessageTTL)
	stuckList, err := r.repo.FindStuckMessages(ctx, cutoff, r.cfg.BatchLimit)
	if err != nil {
		r.logger.Error("reaper find stuck messages failed", "err", err)
		return
	}

	for _, msg := range stuckList {
		r.logger.Warn("reaping stuck message past TTL", "msg_id", msg.ID, "status", msg.Status, "created_at", msg.CreatedAt)
		tx, err := r.pool.Begin(ctx)
		if err != nil {
			continue
		}

		reason := "reaped past TTL in non-final status"
		_ = r.repo.UpdateMessageStatus(ctx, tx, msg.ID, msg.CreatedAt, msg.Status, StatusFailed, &reason, nil, nil, nil)

		// Release hold atomically in same transaction
		if msg.HoldID != nil {
			_ = r.ledgerSvc.ReleaseTx(ctx, tx, *msg.HoldID)
		}

		evtDetails, _ := json.Marshal(map[string]interface{}{"reason": reason})
		_ = r.repo.RecordEvent(ctx, tx, MessageEvent{
			MessageID:        msg.ID,
			MessageCreatedAt: msg.CreatedAt,
			EventType:        "reaper_stuck_expired",
			FromStatus:       &msg.Status,
			ToStatus:         StatusFailed,
			ActorType:        "system_reaper",
			Details:          evtDetails,
			CreatedAt:        time.Now().UTC(),
		})

		_ = tx.Commit(ctx)
	}
}

func (r *Reaper) reapUnknownMessages(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-r.cfg.UnknownResolutionWindow)
	unknownList, err := r.repo.FindUnknownMessages(ctx, cutoff, r.cfg.BatchLimit)
	if err != nil {
		r.logger.Error("reaper find unknown messages failed", "err", err)
		return
	}

	for _, msg := range unknownList {
		r.logger.Warn("reaping UNKNOWN message past unknown resolution window; releasing hold and flagging exposure",
			"msg_id", msg.ID,
			"created_at", msg.CreatedAt,
		)

		tx, err := r.pool.Begin(ctx)
		if err != nil {
			continue
		}

		reason := "unknown resolution window elapsed without DLR; hold released with exposure flag"
		_ = r.repo.UpdateMessageStatus(ctx, tx, msg.ID, msg.CreatedAt, StatusUnknown, StatusFailed, &reason, nil, nil, nil)

		// Release customer hold
		if msg.HoldID != nil {
			_ = r.ledgerSvc.ReleaseTx(ctx, tx, *msg.HoldID)
		}

		// Record exposure flag in settlement_exceptions
		details, _ := json.Marshal(map[string]interface{}{
			"message_id":         msg.ID.String(),
			"customer_org_id":    msg.CustomerOrgID.String(),
			"sell_price":         msg.SellPrice.String(),
			"estimated_buy_cost": msg.EstimatedBuyCost,
			"hold_id":            msg.HoldID,
			"note":               "Provider outcome remained ambiguous past resolution window. Customer hold released; potential supplier liability exposure.",
		})
		_ = r.repo.RecordSettlementException(ctx, SettlementException{
			MessageID:        msg.ID,
			MessageCreatedAt: msg.CreatedAt,
			CustomerOrgID:    &msg.CustomerOrgID,
			HoldID:           msg.HoldID,
			ExceptionType:    "unknown_exposure",
			Severity:         "warning",
			Details:          details,
		})

		_ = r.repo.RecordEvent(ctx, tx, MessageEvent{
			MessageID:        msg.ID,
			MessageCreatedAt: msg.CreatedAt,
			EventType:        "unknown_window_expired",
			FromStatus:       &msg.Status,
			ToStatus:         StatusFailed,
			ActorType:        "system_reaper",
			Details:          details,
			CreatedAt:        time.Now().UTC(),
		})

		_ = tx.Commit(ctx)
	}
}

func (r *Reaper) reapNoDLRMessages(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-r.cfg.MessageTTL)
	submittedList, err := r.repo.FindNoDLRSubmittedMessages(ctx, cutoff, r.cfg.BatchLimit)
	if err != nil {
		r.logger.Error("reaper find submitted no-DLR messages failed", "err", err)
		return
	}

	for _, msg := range submittedList {
		r.logger.Info("reaping submitted message without DLR past message TTL -> expired",
			"msg_id", msg.ID,
			"submitted_at", msg.SubmittedAt,
		)

		tx, err := r.pool.Begin(ctx)
		if err != nil {
			continue
		}

		reason := "no DLR received from upstream within message TTL"
		_ = r.repo.UpdateMessageStatus(ctx, tx, msg.ID, msg.CreatedAt, StatusSubmitted, StatusExpired, &reason, nil, nil, nil)

		// If billing trigger is on_delivery, apply configured hold policy
		if r.dlrProc.billingTrig == "on_delivery" && msg.HoldID != nil {
			if r.cfg.NoDLRHoldPolicy == HoldPolicyImmediateRelease {
				_ = r.ledgerSvc.ReleaseTx(ctx, tx, *msg.HoldID)
			} else {
				// Default HoldPolicyRetainUntilResolution: retain until unknown-resolution window ends
				submittedAt := msg.CreatedAt
				if msg.SubmittedAt != nil {
					submittedAt = *msg.SubmittedAt
				}
				if time.Since(submittedAt) >= r.cfg.UnknownResolutionWindow {
					_ = r.ledgerSvc.ReleaseTx(ctx, tx, *msg.HoldID)

					// Record supplier liability exposure flag in settlement_exceptions
					details, _ := json.Marshal(map[string]interface{}{
						"message_id":         msg.ID.String(),
						"customer_org_id":    msg.CustomerOrgID.String(),
						"sell_price":         msg.SellPrice.String(),
						"estimated_buy_cost": msg.EstimatedBuyCost,
						"hold_id":            msg.HoldID,
						"note":               "Provider accepted message but never delivered DLR within TTL and unknown resolution window. Customer hold released; potential supplier liability exposure.",
					})
					_ = r.repo.RecordSettlementException(ctx, SettlementException{
						MessageID:        msg.ID,
						MessageCreatedAt: msg.CreatedAt,
						CustomerOrgID:    &msg.CustomerOrgID,
						HoldID:           msg.HoldID,
						ExceptionType:    "no_dlr_expired_exposure",
						Severity:         "warning",
						Details:          details,
					})
				}
			}
		}

		evtDetails, _ := json.Marshal(map[string]interface{}{"reason": reason})
		_ = r.repo.RecordEvent(ctx, tx, MessageEvent{
			MessageID:        msg.ID,
			MessageCreatedAt: msg.CreatedAt,
			EventType:        "no_dlr_expired",
			FromStatus:       &msg.Status,
			ToStatus:         StatusExpired,
			ActorType:        "system_reaper",
			Details:          evtDetails,
			CreatedAt:        time.Now().UTC(),
		})

		_ = tx.Commit(ctx)
	}
}

func (r *Reaper) retryUnmatchedDLRs(ctx context.Context) {
	unmatched, err := r.repo.FindPendingUnmatchedDLRs(ctx, r.cfg.BatchLimit)
	if err != nil {
		r.logger.Error("reaper find pending unmatched DLRs failed", "err", err)
		return
	}

	retryCutoff := time.Now().UTC().Add(-r.cfg.UnmatchedDLRRetryWindow)

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
			// Found match! Process DLR transition and mark matched
			pduReceipt, _ := ParseDLRReceipt([]byte(*item.RawReceipt), nil)
			if pduReceipt != nil && item.ConnectionID != nil {
				_ = r.dlrProc.applyDLRTransition(ctx, *item.ConnectionID, *matchedMsgID, *matchedCreatedAt, pduReceipt)
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
