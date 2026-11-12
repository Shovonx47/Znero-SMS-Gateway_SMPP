package smppserver

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/internal/customeredge"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/smpp/pdu"
)

// DLRDispatcherConfig configures the background SMPP DLR worker.
type DLRDispatcherConfig struct {
	BatchSize     int           `json:"batch_size"`
	WorkerCount   int           `json:"worker_count"`
	PollInterval  time.Duration `json:"poll_interval"`
	LeaseDuration time.Duration `json:"lease_duration"`
	WorkerID      string        `json:"worker_id"`
}

// DLRDispatcher queries smpp_dlr_queue and delivers receipts to bound client sessions.
type DLRDispatcher struct {
	cfg     DLRDispatcherConfig
	pool    *pgxpool.Pool
	repo    messaging.Repository
	tracker *SessionTracker
	logger  *slog.Logger
	stopCh  chan struct{}
	wg      sync.WaitGroup
}

// NewDLRDispatcher creates a new DLRDispatcher worker.
func NewDLRDispatcher(
	cfg DLRDispatcherConfig,
	pool *pgxpool.Pool,
	repo messaging.Repository,
	tracker *SessionTracker,
	logger *slog.Logger,
) *DLRDispatcher {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 200
	}
	if cfg.WorkerCount <= 0 {
		cfg.WorkerCount = 32
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 50 * time.Millisecond
	}
	if cfg.LeaseDuration <= 0 {
		cfg.LeaseDuration = 10 * time.Second
	}
	if cfg.WorkerID == "" {
		cfg.WorkerID = fmt.Sprintf("smpp-dlr-%d", time.Now().UnixNano()%100000)
	}

	return &DLRDispatcher{
		cfg:     cfg,
		pool:    pool,
		repo:    repo,
		tracker: tracker,
		logger:  logger,
		stopCh:  make(chan struct{}),
	}
}

// Start spawns the background DLR polling worker.
func (d *DLRDispatcher) Start(ctx context.Context) {
	d.wg.Add(1)
	go d.run(ctx)
	d.logger.Info("smpp customer dlr dispatcher started", "worker_id", d.cfg.WorkerID)
}

// Stop shuts down the background worker.
func (d *DLRDispatcher) Stop() {
	close(d.stopCh)
	d.wg.Wait()
	d.logger.Info("smpp customer dlr dispatcher stopped")
}

func (d *DLRDispatcher) run(ctx context.Context) {
	defer d.wg.Done()
	ticker := time.NewTicker(d.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopCh:
			return
		case <-ticker.C:
			for d.processBatch(ctx) >= d.cfg.BatchSize {
				select {
				case <-ctx.Done():
					return
				case <-d.stopCh:
					return
				default:
				}
			}
		}
	}
}

// ProcessBatch claims and delivers one batch of queued customer SMPP DLRs deterministically.
func (d *DLRDispatcher) ProcessBatch(ctx context.Context) {
	d.processBatch(ctx)
}

func (d *DLRDispatcher) processBatch(ctx context.Context) int {
	items, err := d.repo.FetchPendingSMPPDLRs(ctx, d.cfg.BatchSize, d.cfg.WorkerID, d.cfg.LeaseDuration)
	if err != nil {
		d.logger.Error("failed to claim pending smpp dlrs", "error", err)
		return 0
	}
	if len(items) == 0 {
		return 0
	}

	workers := d.cfg.WorkerCount
	if workers <= 0 {
		workers = 32
	}
	if workers > len(items) {
		workers = len(items)
	}

	type itemOutcome struct {
		id     uuid.UUID
		status string // "", "delivered", "expired"
	}
	outcomes := make([]itemOutcome, len(items))
	jobs := make(chan int, len(items))
	for i := range items {
		jobs <- i
	}
	close(jobs)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				st := d.dispatchItem(ctx, items[idx])
				outcomes[idx] = itemOutcome{id: items[idx].ID, status: st}
			}
		}()
	}
	wg.Wait()

	var deliveredIDs, expiredIDs []uuid.UUID
	for _, out := range outcomes {
		switch out.status {
		case "delivered":
			deliveredIDs = append(deliveredIDs, out.id)
		case "expired":
			expiredIDs = append(expiredIDs, out.id)
		}
	}
	if len(deliveredIDs) > 0 {
		if err := d.repo.UpdateSMPPDLRQueueStatusBatch(ctx, deliveredIDs, "delivered"); err != nil {
			d.logger.Error("failed batch update smpp dlr delivered status", "count", len(deliveredIDs), "error", err)
		}
	}
	if len(expiredIDs) > 0 {
		if err := d.repo.UpdateSMPPDLRQueueStatusBatch(ctx, expiredIDs, "expired"); err != nil {
			d.logger.Error("failed batch update smpp dlr expired status", "count", len(expiredIDs), "error", err)
		}
	}
	return len(items)
}

func (d *DLRDispatcher) dispatchItem(ctx context.Context, item messaging.SMPPDLRQueueItem) string {
	// Find active receiver or transceiver session for this account
	sessions := d.tracker.GetSessionsForAccount(item.AccountID)
	var targetSession *ClientSession
	for _, sess := range sessions {
		if sess.CanReceiveDLR() {
			targetSession = sess
			break
		}
	}

	now := time.Now().UTC()

	// If no bound session is connected
	if targetSession == nil {
		if now.After(item.ExpiresAt) {
			d.logger.Info("smpp dlr expired without bound session", "message_id", item.MessageID)
			return "expired"
		}
		// Otherwise leave leased_until to expire so it retries when client connects
		return ""
	}

	// Map to standard SMPP DLR receipt format
	// id:0123456789 sub:001 dlvrd:001 submit date:YYMMDDhhmm done date:YYMMDDhhmm stat:DELIVRD err:000 text:...
	submitDate := item.CreatedAt.Format("0601021504")
	doneDate := now.Format("0601021504")
	coarseStat := strings.ToUpper(customeredge.MapInternalToCustomerStatus(item.Status).ReasonCode)
	if coarseStat == "" {
		coarseStat = "DELIVRD"
	}
	errCode := item.ErrorCode
	if errCode == "" {
		errCode = "000"
	}
	if len(errCode) > 3 {
		errCode = errCode[:3]
	}

	receiptText := fmt.Sprintf("id:%s sub:001 dlvrd:001 submit date:%s done date:%s stat:%s err:%s text:",
		item.MessageRef, submitDate, doneDate, coarseStat, errCode)

	// Map numeric SMPP message state
	var stateVal uint8 = 2 // DELIVERED
	switch item.Status {
	case "delivered":
		stateVal = 2
	case "expired":
		stateVal = 3
	case "deleted":
		stateVal = 4
	case "undelivered":
		stateVal = 5
	case "accepted":
		stateVal = 6
	case "unknown":
		stateVal = 7
	case "rejected", "failed":
		stateVal = 8
	}

	deliver := pdu.NewDeliverSM(0)
	deliver.ESMClass = 0x04 // Delivery receipt flag
	deliver.SourceAddr = item.Destination
	deliver.DestAddr = item.SenderID
	deliver.ShortMessage = []byte(receiptText)

	// Add optional TLVs for modern clients
	deliver.TLVs = []pdu.TLV{
		{
			Tag:   pdu.TagReceiptedMessageId,
			Value: append([]byte(item.MessageRef), 0x00), // C-String
		},
		{
			Tag:   pdu.TagMessageState,
			Value: []byte{stateVal},
		},
	}

	// Dispatch to client session and wait for deliver_sm_resp
	dispErr := targetSession.SendDeliverSM(ctx, deliver)
	if dispErr != nil {
		d.logger.Warn("smpp deliver_sm failed", "session_id", targetSession.ID, "error", dispErr)
		return ""
	}

	return "delivered"
}
