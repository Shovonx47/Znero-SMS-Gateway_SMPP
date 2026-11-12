package httpprovider

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/provider"
)

// DLRPoller periodically polls active HTTP connections configured with http_dlr_mode='polling'
// for pending submitted messages whose final delivery state has not yet arrived.
type DLRPoller struct {
	pool    *pgxpool.Pool
	client  *Client
	dlrProc *messaging.DLRProcessor
	logger  *slog.Logger
	clock   func() time.Time

	tickInterval time.Duration
	batchSize    int
	maxWorkers   int

	mu         sync.Mutex
	lastPolled map[uuid.UUID]time.Time
	stopCh     chan struct{}
	wg         sync.WaitGroup
}

// PollerConfig configures the HTTP DLR polling worker.
type PollerConfig struct {
	TickInterval time.Duration
	BatchSize    int
	MaxWorkers   int
	Clock        func() time.Time
}

// NewDLRPoller constructs an HTTP DLR poller.
func NewDLRPoller(pool *pgxpool.Pool, client *Client, dlrProc *messaging.DLRProcessor, cfg PollerConfig, logger *slog.Logger) *DLRPoller {
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = 5 * time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = 5
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &DLRPoller{
		pool:         pool,
		client:       client,
		dlrProc:      dlrProc,
		logger:       logger.With("module", "httpprovider_poller"),
		clock:        cfg.Clock,
		tickInterval: cfg.TickInterval,
		batchSize:    cfg.BatchSize,
		maxWorkers:   cfg.MaxWorkers,
		lastPolled:   make(map[uuid.UUID]time.Time),
		stopCh:       make(chan struct{}),
	}
}

// Start runs the background polling ticker until Stop is called or ctx is canceled.
func (p *DLRPoller) Start(ctx context.Context) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		t := time.NewTicker(p.tickInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.stopCh:
				return
			case <-t.C:
				if err := p.PollOnce(ctx, false); err != nil && ctx.Err() == nil {
					p.logger.Warn("http dlr poll cycle error", "err", err)
				}
			}
		}
	}()
}

// Stop stops the poller cleanly and waits for in-flight requests to drain.
func (p *DLRPoller) Stop() {
	select {
	case <-p.stopCh:
		return
	default:
		close(p.stopCh)
	}
	p.wg.Wait()
}

type pollingConn struct {
	id       uuid.UUID
	authType string
	credEnc  string
	cfg      provider.HTTPConnectorConfig
}

type pendingPollMsg struct {
	msgID         uuid.UUID
	createdAt     time.Time
	providerMsgID string
}

// PollOnce checks all active HTTP connections configured for polling. When force is true,
// interval gating per connection is bypassed (useful in deterministic tests).
func (p *DLRPoller) PollOnce(ctx context.Context, force bool) error {
	rows, err := p.pool.Query(ctx, `
		SELECT id, http_auth_type, http_credentials_encrypted, http_config
		FROM provider_connections
		WHERE protocol = 'http'
		  AND status = 'active'
		  AND archived = false
		  AND http_dlr_mode = 'polling'
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var conns []pollingConn
	for rows.Next() {
		var pc pollingConn
		var cfgRaw []byte
		if scanErr := rows.Scan(&pc.id, &pc.authType, &pc.credEnc, &cfgRaw); scanErr != nil {
			return scanErr
		}
		if len(cfgRaw) > 0 {
			_ = json.Unmarshal(cfgRaw, &pc.cfg)
		}
		norm := pc.cfg.Normalized()
		if strings.TrimSpace(norm.PollURLTemplate) == "" {
			continue
		}
		pc.cfg = norm
		conns = append(conns, pc)
	}

	now := p.clock()
	for _, conn := range conns {
		if !force {
			p.mu.Lock()
			last := p.lastPolled[conn.id]
			p.mu.Unlock()
			if !last.IsZero() && now.Sub(last) < time.Duration(conn.cfg.PollIntervalSec)*time.Second {
				continue
			}
		}
		if err := p.pollConnection(ctx, conn, now); err != nil && ctx.Err() == nil {
			p.logger.Warn("poll connection failed", "conn_id", conn.id, "err", err)
		}
		p.mu.Lock()
		p.lastPolled[conn.id] = now
		p.mu.Unlock()
	}
	return nil
}

func (p *DLRPoller) pollConnection(ctx context.Context, conn pollingConn, now time.Time) error {
	// Partition-pruned query scanning at most the last 8 days of submitted HTTP messages.
	since := now.Add(-8 * 24 * time.Hour)
	rows, err := p.pool.Query(ctx, `
		SELECT id, created_at, provider_message_id
		FROM messages
		WHERE provider_connection_id = $1
		  AND status = 'submitted'
		  AND provider_message_id IS NOT NULL
		  AND provider_message_id <> ''
		  AND created_at >= $2
		ORDER BY created_at ASC
		LIMIT $3
	`, conn.id, since, p.batchSize)
	if err != nil {
		return err
	}
	defer rows.Close()

	var targets []pendingPollMsg
	for rows.Next() {
		var m pendingPollMsg
		if scanErr := rows.Scan(&m.msgID, &m.createdAt, &m.providerMsgID); scanErr != nil {
			return scanErr
		}
		targets = append(targets, m)
	}
	if len(targets) == 0 {
		return nil
	}

	plainCred := ""
	if conn.credEnc != "" && p.client.decrypter != nil {
		dec, err := p.client.decrypter.Decrypt(conn.credEnc)
		if err != nil {
			return err
		}
		plainCred = dec
	}

	timeout := time.Duration(conn.cfg.RequestTimeoutMs) * time.Millisecond
	httpClient := p.client.buildHTTPClient(timeout, p.client.allowLoopback)

	sem := make(chan struct{}, p.maxWorkers)
	var wg sync.WaitGroup
loop:
	for _, target := range targets {
		select {
		case <-ctx.Done():
			break loop
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(m pendingPollMsg) {
			defer wg.Done()
			defer func() { <-sem }()
			p.pollSingleMessage(ctx, conn, plainCred, httpClient, timeout, m)
		}(target)
	}
	wg.Wait()
	return nil
}

func (p *DLRPoller) pollSingleMessage(
	ctx context.Context,
	conn pollingConn,
	plainCred string,
	httpClient *http.Client,
	timeout time.Duration,
	m pendingPollMsg,
) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, _, err := RenderPollRequest(reqCtx, conn.cfg, conn.authType, plainCred, m.providerMsgID)
	if err != nil {
		return
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return
	}

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, int64(conn.cfg.MaxResponseBytes)+1))
	if err != nil {
		return
	}
	if len(rawBody) > conn.cfg.MaxResponseBytes {
		rawBody = rawBody[:conn.cfg.MaxResponseBytes]
	}

	rawStat, _ := ExtractJSONPath(rawBody, conn.cfg.PollStatusPath)
	errCode, _ := ExtractJSONPath(rawBody, conn.cfg.PollErrorCodePath)
	if strings.TrimSpace(rawStat) == "" {
		return
	}

	mappedStat := MapDLRStatus(rawStat, conn.cfg.PollStatusMap)
	if mappedStat != "DELIVRD" && mappedStat != "UNDELIV" && mappedStat != "EXPIRED" && mappedStat != "REJECTD" {
		// Non-terminal intermediate state (e.g. ACCEPTD / queued); wait for next poll.
		return
	}

	rawStr := string(rawBody)
	if len(rawStr) > 1024 {
		rawStr = rawStr[:1024]
	}
	_ = p.dlrProc.ProcessParsedDLR(ctx, conn.id, &messaging.DLRReceipt{
		ProviderMessageID: m.providerMsgID,
		Stat:              mappedStat,
		ErrCode:           errCode,
		RawReceipt:        rawStr,
	})
}
