package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/internal/smpp"
)

// Config holds runtime options for the customer webhook dispatcher.
type Config struct {
	WorkerCount   int
	PollInterval  time.Duration
	BatchSize     int
	Timeout       time.Duration
	MaxPayloadLen int64
	AllowLoopback bool
}

// DefaultConfig provides sensible production defaults for the webhook dispatcher.
func DefaultConfig(allowLoopback bool) Config {
	return Config{
		WorkerCount:   10,
		PollInterval:  1 * time.Second,
		BatchSize:     50,
		Timeout:       8 * time.Second,
		MaxPayloadLen: 4096, // 4KB response snippet cap
		AllowLoopback: allowLoopback,
	}
}

// Dispatcher polls for pending customer webhook deliveries, executes SSRF-safe HTTP POST requests
// with HMAC-SHA256 signatures, and records execution attempts and auto-disable status.
type Dispatcher struct {
	repo       messaging.Repository
	keyring    *secrets.KeyRing
	cfg        Config
	httpClient *http.Client
	workerID   string
	logger     *slog.Logger
	stopChan   chan struct{}
	wg         sync.WaitGroup
}

// NewDispatcher initializes a webhook Dispatcher.
func NewDispatcher(repo messaging.Repository, keyring *secrets.KeyRing, cfg Config, logger *slog.Logger) *Dispatcher {
	if cfg.WorkerCount <= 0 {
		cfg.WorkerCount = 10
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 1 * time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 50
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 8 * time.Second
	}
	if cfg.MaxPayloadLen <= 0 {
		cfg.MaxPayloadLen = 4096
	}
	if logger == nil {
		logger = slog.Default()
	}

	dialer := smpp.NewSafeDialer(smpp.DialOptions{
		Timeout:       cfg.Timeout,
		AllowLoopback: cfg.AllowLoopback,
	})

	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: cfg.Timeout,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   cfg.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // Strictly forbid redirects to prevent SSRF bypass
		},
	}

	return &Dispatcher{
		repo:       repo,
		keyring:    keyring,
		cfg:        cfg,
		httpClient: client,
		workerID:   fmt.Sprintf("wh-worker-%s", uuid.New().String()[:8]),
		logger:     logger.With("module", "webhook_dispatcher"),
		stopChan:   make(chan struct{}),
	}
}

// Start begins the background webhook polling and dispatch loop.
func (d *Dispatcher) Start(ctx context.Context) {
	d.wg.Add(1)
	go d.pollLoop(ctx)
}

// Stop signals the dispatcher to cease polling and waits for in-flight deliveries to complete.
func (d *Dispatcher) Stop() {
	close(d.stopChan)
	d.wg.Wait()
}

func (d *Dispatcher) pollLoop(ctx context.Context) {
	defer d.wg.Done()
	ticker := time.NewTicker(d.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopChan:
			return
		case <-ticker.C:
			d.dispatchPending(ctx)
		}
	}
}

func (d *Dispatcher) dispatchPending(ctx context.Context) {
	leaseDuration := 30 * time.Second
	deliveries, err := d.repo.FetchPendingWebhookDeliveries(ctx, d.cfg.BatchSize, d.workerID, leaseDuration)
	if err != nil {
		d.logger.Error("failed to fetch pending webhook deliveries", "err", err)
		return
	}
	if len(deliveries) == 0 {
		return
	}

	sem := make(chan struct{}, d.cfg.WorkerCount)
	var batchWg sync.WaitGroup

	for _, deliv := range deliveries {
		sem <- struct{}{}
		batchWg.Add(1)

		go func(item messaging.CustomerWebhookDelivery) {
			defer func() {
				<-sem
				batchWg.Done()
			}()
			d.processDelivery(ctx, item)
		}(deliv)
	}

	batchWg.Wait()
}

func (d *Dispatcher) processDelivery(ctx context.Context, item messaging.CustomerWebhookDelivery) {
	reqCtx, cancel := context.WithTimeout(ctx, d.cfg.Timeout)
	defer cancel()

	wh, err := d.repo.GetWebhook(reqCtx, item.WebhookID, item.CustomerOrgID)
	if err != nil || wh == nil {
		d.recordFailure(reqCtx, item, nil, 0, "webhook_not_found", "Webhook not found or deleted")
		return
	}

	if wh.Status != "active" {
		d.recordFailure(reqCtx, item, nil, 0, "webhook_inactive", fmt.Sprintf("Webhook is %s", wh.Status))
		return
	}

	var secret string
	if d.keyring != nil && wh.SecretEncrypted != "" {
		dec, err := d.keyring.Decrypt(wh.SecretEncrypted)
		if err != nil {
			d.logger.Error("failed to decrypt webhook secret", "webhook_id", wh.ID, "err", err)
			d.recordFailure(reqCtx, item, nil, 0, "crypto_error", "Failed to decrypt signing secret")
			return
		}
		secret = dec
	}

	ts := time.Now().Unix()
	signedPayload := fmt.Sprintf("%d.%s", ts, string(item.Payload))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	sig := hex.EncodeToString(mac.Sum(nil))
	sigHeader := fmt.Sprintf("t=%d,v1=%s", ts, sig)

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, wh.URL, bytes.NewReader(item.Payload))
	if err != nil {
		d.recordFailure(reqCtx, item, nil, 0, "invalid_request", err.Error())
		return
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "Znero-Webhook-Dispatcher/1.0")
	httpReq.Header.Set("X-Znero-Delivery-ID", item.ID.String())
	httpReq.Header.Set("X-Znero-Event", item.EventType)
	httpReq.Header.Set("X-Znero-Signature", sigHeader)

	start := time.Now()
	resp, err := d.httpClient.Do(httpReq)
	latencyMs := int(time.Since(start).Milliseconds())

	if err != nil {
		errClass := "network_error"
		if errors.Is(err, context.DeadlineExceeded) {
			errClass = "timeout"
		} else if strings.Contains(err.Error(), "ssrf") || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "loopback") {
			errClass = "ssrf_blocked"
		}
		d.recordFailure(reqCtx, item, nil, latencyMs, errClass, err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()

	snippetBytes, _ := io.ReadAll(io.LimitReader(resp.Body, d.cfg.MaxPayloadLen))
	snippet := string(snippetBytes)
	statusCode := resp.StatusCode

	if statusCode >= 200 && statusCode < 300 {
		// Success
		attempt := messaging.CustomerWebhookAttempt{
			ID:              uuid.New(),
			DeliveryID:      item.ID,
			AttemptNumber:   item.Attempts,
			ResponseCode:    &statusCode,
			LatencyMs:       latencyMs,
			ResponseSnippet: &snippet,
			CreatedAt:       time.Now().UTC(),
		}
		if err := d.repo.RecordWebhookAttempt(reqCtx, attempt, "success", nil); err != nil {
			d.logger.Error("failed to record successful webhook attempt", "delivery_id", item.ID, "err", err)
		}
		return
	}

	// Non-2xx response
	errClass := "http_error"
	if statusCode >= 400 && statusCode < 500 {
		errClass = "client_error"
	} else if statusCode >= 500 {
		errClass = "server_error"
	}
	d.recordFailure(reqCtx, item, &statusCode, latencyMs, errClass, snippet)
}

func (d *Dispatcher) recordFailure(
	ctx context.Context,
	item messaging.CustomerWebhookDelivery,
	code *int,
	latencyMs int,
	errClass string,
	snippet string,
) {
	if len(snippet) > int(d.cfg.MaxPayloadLen) {
		snippet = snippet[:d.cfg.MaxPayloadLen]
	}

	attempt := messaging.CustomerWebhookAttempt{
		ID:              uuid.New(),
		DeliveryID:      item.ID,
		AttemptNumber:   item.Attempts,
		ResponseCode:    code,
		LatencyMs:       latencyMs,
		ResponseSnippet: &snippet,
		ErrorClass:      &errClass,
		CreatedAt:       time.Now().UTC(),
	}

	var nextStatus string
	var nextAttempt *time.Time

	if item.Attempts < item.MaxAttempts {
		nextStatus = "retry"
		// Exponential backoff: 30s, 60s, 120s, 240s... capped at 15m
		backoffSeconds := (1 << (item.Attempts - 1)) * 30
		if backoffSeconds > 900 {
			backoffSeconds = 900
		}
		next := time.Now().UTC().Add(time.Duration(backoffSeconds) * time.Second)
		nextAttempt = &next
	} else {
		nextStatus = "failed"
		nextAttempt = nil
	}

	if err := d.repo.RecordWebhookAttempt(ctx, attempt, nextStatus, nextAttempt); err != nil {
		d.logger.Error("failed to record failed webhook attempt", "delivery_id", item.ID, "err", err)
	}
}
