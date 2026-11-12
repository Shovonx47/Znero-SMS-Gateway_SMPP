package analytics

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const AnalyticsAdvisoryLockID = int64(8899001)

// WorkerConfig holds configuration for the background rollup worker.
type WorkerConfig struct {
	PollInterval time.Duration
	BatchSize    int
	InstanceID   string
}

// DefaultWorkerConfig returns production defaults for WorkerConfig.
func DefaultWorkerConfig() WorkerConfig {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "worker"
	}
	return WorkerConfig{
		PollInterval: 1 * time.Second,
		BatchSize:    1000,
		InstanceID:   fmt.Sprintf("%s-%s", hostname, uuid.New().String()[:8]),
	}
}

// Worker executes continuous aggregation of event streams into 15-minute rollups.
type Worker struct {
	pool    *pgxpool.Pool
	repo    *Repository
	cfg     WorkerConfig
	logger  *slog.Logger
	stopCh  chan struct{}
	doneCh  chan struct{}
	mu      sync.Mutex
	running bool
}

// NewWorker constructs a new background rollup worker.
func NewWorker(pool *pgxpool.Pool, repo *Repository, cfg WorkerConfig, logger *slog.Logger) *Worker {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 1 * time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 1000
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{
		pool:   pool,
		repo:   repo,
		cfg:    cfg,
		logger: logger.With(slog.String("component", "analytics_worker")),
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Start begins processing events in a background goroutine.
func (w *Worker) Start(ctx context.Context) {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return
	}
	w.running = true
	w.stopCh = make(chan struct{})
	w.doneCh = make(chan struct{})
	w.mu.Unlock()

	go w.run(ctx)
}

// Stop signals the worker to drain and shutdown gracefully.
func (w *Worker) Stop() {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return
	}
	w.running = false
	close(w.stopCh)
	w.mu.Unlock()

	<-w.doneCh
}

func (w *Worker) run(ctx context.Context) {
	defer close(w.doneCh)

	hostname, _ := os.Hostname()
	w.logger.Info("starting analytics worker", slog.String("instance_id", w.cfg.InstanceID))

	// Acquire dedicated connection for PostgreSQL session advisory lock
	conn, err := w.pool.Acquire(ctx)
	if err != nil {
		w.logger.Error("failed to acquire db connection for advisory lock", slog.String("error", err.Error()))
		return
	}
	defer conn.Release()

	// Try acquiring session advisory lock
	var acquired bool
	for {
		err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", AnalyticsAdvisoryLockID).Scan(&acquired)
		if err == nil && acquired {
			break
		}

		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-time.After(3 * time.Second):
			w.logger.Debug("standby analytics worker awaiting leader lock")
		}
	}

	defer func() {
		// Release advisory lock on exit
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", AnalyticsAdvisoryLockID)
		w.logger.Info("released analytics advisory lock")
	}()

	w.logger.Info("acquired analytics leader advisory lock; running rollup loop")

	heartbeatTicker := time.NewTicker(30 * time.Second)
	defer heartbeatTicker.Stop()

	// Initial heartbeat
	_ = w.repo.RecordHeartbeat(ctx, ServiceHeartbeat{
		Role:        "analytics",
		InstanceID:  w.cfg.InstanceID,
		Hostname:    hostname,
		LastSeenAt:  time.Now().UTC(),
		MetricsJSON: map[string]interface{}{"status": "leader"},
	})

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("analytics worker context canceled")
			return
		case <-w.stopCh:
			w.logger.Info("analytics worker stop requested")
			return
		case <-heartbeatTicker.C:
			_ = w.repo.RecordHeartbeat(ctx, ServiceHeartbeat{
				Role:        "analytics",
				InstanceID:  w.cfg.InstanceID,
				Hostname:    hostname,
				LastSeenAt:  time.Now().UTC(),
				MetricsJSON: map[string]interface{}{"status": "leader"},
			})
		default:
			// Process event batch
			n, err := w.repo.ProcessMessageEventsBatch(ctx, w.cfg.BatchSize)
			if err != nil {
				w.logger.Error("error processing event batch", slog.String("error", err.Error()))
				select {
				case <-ctx.Done():
					return
				case <-w.stopCh:
					return
				case <-time.After(w.cfg.PollInterval):
				}
				continue
			}

			if n > 0 {
				w.logger.Debug("processed event batch", slog.Int("count", n))
				// Backlog exists, continue processing immediately
				continue
			}

			// No events processed, wait poll interval
			select {
			case <-ctx.Done():
				return
			case <-w.stopCh:
				return
			case <-time.After(w.cfg.PollInterval):
			}
		}
	}
}
