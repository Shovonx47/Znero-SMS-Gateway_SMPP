package httpprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/provider"
	"github.com/znero-international/znero/backend/internal/smpp"
)

// Decrypter decrypts stored ciphertext credentials into plaintext.
type Decrypter interface {
	Decrypt(envelope string) (string, error)
}

// ClientConfig configures the HTTP upstream provider connector.
type ClientConfig struct {
	Pool          *pgxpool.Pool
	Decrypter     Decrypter
	AllowLoopback bool
	CacheTTL      time.Duration
	Logger        *slog.Logger
}

type cachedConn struct {
	baseURL      string
	authType     string
	credEnc      string
	dlrMode      string
	dlrSecretEnc string
	cfg          provider.HTTPConnectorConfig
	expiresAt    time.Time
}

// Client implements messaging.Sender and provider.HTTPTestSender for generic HTTP SMS providers.
type Client struct {
	pool          *pgxpool.Pool
	decrypter     Decrypter
	allowLoopback bool
	cacheTTL      time.Duration
	logger        *slog.Logger

	mu    sync.RWMutex
	cache map[uuid.UUID]cachedConn
}

// NewClient initializes an SSRF-safe HTTP provider client.
func NewClient(cfg ClientConfig) *Client {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		pool:          cfg.Pool,
		decrypter:     cfg.Decrypter,
		allowLoopback: cfg.AllowLoopback,
		cacheTTL:      cfg.CacheTTL,
		logger:        logger.With("module", "httpprovider_client"),
		cache:         make(map[uuid.UUID]cachedConn),
	}
}

// Send transmits an SMS attempt over HTTP using the connection's template and auth rules.
func (c *Client) Send(ctx context.Context, attempt messaging.SendAttempt) (messaging.SendResult, error) {
	conn, err := c.loadConnection(ctx, attempt.ConnectionID)
	if err != nil {
		return messaging.SendResult{
			Status:       messaging.SendStatusRetryableReject,
			ErrorCode:    "CONN_CONFIG_ERROR",
			ErrorMessage: err.Error(),
		}, nil
	}

	plainCred := ""
	if conn.credEnc != "" && c.decrypter != nil {
		dec, err := c.decrypter.Decrypt(conn.credEnc)
		if err != nil {
			return messaging.SendResult{
				Status:       messaging.SendStatusFatalReject,
				ErrorCode:    "CREDENTIAL_DECRYPT_FAILED",
				ErrorMessage: "failed to decrypt provider HTTP credentials",
			}, nil
		}
		plainCred = dec
	}

	execRes := c.executeSend(ctx, conn.cfg, conn.baseURL, conn.authType, plainCred, attempt, c.allowLoopback)

	c.logger.Info("http provider submission answered",
		"msg_id", attempt.MessageID,
		"conn_id", attempt.ConnectionID,
		"status", string(execRes.send.Status),
		"http_status", execRes.httpStatus,
		"provider_msg_id", execRes.send.ProviderMessageID,
		"error_code", execRes.send.ErrorCode,
		"latency_ms", execRes.send.Latency.Milliseconds(),
	)

	return execRes.send, nil
}

// TestConnection implements provider.HTTPTestSender so operators can test HTTP templates
// from the Admin UI against either the configured URL or an in-process simulated FakeHTTPProvider in dev.
func (c *Client) TestConnection(ctx context.Context, conn *provider.ProviderConnection, req provider.TestSendRequest) (*provider.TestSendResult, error) {
	if conn == nil {
		return nil, fmt.Errorf("connection is nil")
	}

	plainCred := ""
	if conn.HTTPCredentialsEncrypted != "" && c.decrypter != nil {
		dec, err := c.decrypter.Decrypt(conn.HTTPCredentialsEncrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypt http credentials: %w", err)
		}
		plainCred = dec
	}

	baseURL := conn.HTTPBaseURL
	allowLoopback := c.allowLoopback
	simulated := false

	if req.SimulateMode != "" {
		srv := newSimulatedHTTPServer(req.SimulateMode)
		defer srv.Close()
		baseURL = srv.URL
		allowLoopback = true
		simulated = true
	}

	attempt := messaging.SendAttempt{
		MessageID:    uuid.New(),
		ConnectionID: conn.ID,
		Destination:  req.Destination,
		SenderID:     req.SenderID,
		Text:         req.Text,
		Encoding:     req.Encoding,
		PartNumber:   1,
		TotalParts:   1,
	}

	execRes := c.executeSend(ctx, conn.HTTPConfig, baseURL, conn.HTTPAuthType, plainCred, attempt, allowLoopback)
	snippet := string(execRes.rawBody)
	if len(snippet) > 512 {
		snippet = snippet[:512] + "..."
	}

	method := ""
	redactedURL := ""
	redactedBody := ""
	if execRes.debug != nil {
		method = execRes.debug.Method
		redactedURL = execRes.debug.RedactedURL
		redactedBody = execRes.debug.RedactedBody
	}

	return &provider.TestSendResult{
		Success:           execRes.send.Status == messaging.SendStatusAccepted,
		Status:            string(execRes.send.Status),
		HTTPStatusCode:    execRes.httpStatus,
		ProviderMessageID: execRes.send.ProviderMessageID,
		ErrorCode:         execRes.send.ErrorCode,
		ErrorMessage:      execRes.send.ErrorMessage,
		LatencyMs:         execRes.send.Latency.Milliseconds(),
		Method:            method,
		RedactedURL:       redactedURL,
		RedactedBody:      redactedBody,
		ResponseSnippet:   snippet,
		Simulated:         simulated,
	}, nil
}

type execSendResult struct {
	send       messaging.SendResult
	httpStatus int
	rawBody    []byte
	debug      *RenderedDebug
}

func (c *Client) executeSend(
	ctx context.Context,
	cfg provider.HTTPConnectorConfig,
	baseURL string,
	authType string,
	plainCred string,
	attempt messaging.SendAttempt,
	allowLoopback bool,
) execSendResult {
	norm := cfg.Normalized()
	timeout := time.Duration(norm.RequestTimeoutMs) * time.Millisecond
	if attempt.Timeout > 0 && attempt.Timeout < timeout {
		timeout = attempt.Timeout
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var wroteRequest bool
	trace := &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				wroteRequest = true
			}
		},
	}
	reqCtx = httptrace.WithClientTrace(reqCtx, trace)

	req, debug, err := RenderRequest(reqCtx, norm, baseURL, authType, plainCred, attempt)
	if err != nil {
		return execSendResult{
			send: messaging.SendResult{
				Status:       messaging.SendStatusFatalReject,
				ErrorCode:    "TEMPLATE_RENDER_ERROR",
				ErrorMessage: err.Error(),
			},
			debug: debug,
		}
	}

	httpClient := c.buildHTTPClient(timeout, allowLoopback)
	start := time.Now()
	resp, doErr := httpClient.Do(req)
	latency := time.Since(start)
	if doErr != nil {
		// Check whether request was actually written on wire; if so, outcome is strictly UNKNOWN (ambiguous).
		status := messaging.SendStatusRetryableReject
		errCode := "CONNECT_ERROR"
		if wroteRequest || errors.Is(doErr, context.DeadlineExceeded) {
			if wroteRequest {
				status = messaging.SendStatusUnknown
				errCode = "AMBIGUOUS_TIMEOUT_OR_IO"
			}
		}
		if strings.Contains(strings.ToLower(doErr.Error()), "ssrf") || strings.Contains(doErr.Error(), "redirect") {
			status = messaging.SendStatusFatalReject
			errCode = "SSRF_OR_REDIRECT_BLOCKED"
		}
		return execSendResult{
			send: messaging.SendResult{
				Status:       status,
				ErrorCode:    errCode,
				ErrorMessage: doErr.Error(),
				Latency:      latency,
			},
			debug: debug,
		}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return execSendResult{
			httpStatus: resp.StatusCode,
			send: messaging.SendResult{
				Status:       messaging.SendStatusFatalReject,
				ErrorCode:    fmt.Sprintf("HTTP_REDIRECT_%d", resp.StatusCode),
				ErrorMessage: "redirects are forbidden on HTTP provider connections",
				Latency:      latency,
			},
			debug: debug,
		}
	}

	limited := io.LimitReader(resp.Body, int64(norm.MaxResponseBytes)+1)
	rawBody, readErr := io.ReadAll(limited)
	if readErr != nil {
		// Response read failed after request was sent -> AMBIGUOUS (NEVER resend).
		return execSendResult{
			httpStatus: resp.StatusCode,
			rawBody:    rawBody,
			send: messaging.SendResult{
				Status:       messaging.SendStatusUnknown,
				ErrorCode:    "RESPONSE_READ_ERROR",
				ErrorMessage: readErr.Error(),
				Latency:      latency,
			},
			debug: debug,
		}
	}
	if len(rawBody) > norm.MaxResponseBytes {
		rawBody = rawBody[:norm.MaxResponseBytes]
	}

	parsed := ParseSendResponse(norm, resp.StatusCode, rawBody)
	return execSendResult{
		httpStatus: resp.StatusCode,
		rawBody:    rawBody,
		debug:      debug,
		send: messaging.SendResult{
			Status:            parsed.Status,
			ProviderMessageID: parsed.ProviderMessageID,
			ErrorCode:         parsed.ErrorCode,
			ErrorMessage:      parsed.ErrorMessage,
			BuyCost:           decimal.Zero,
			Latency:           latency,
		},
	}
}

func (c *Client) buildHTTPClient(timeout time.Duration, allowLoopback bool) *http.Client {
	dialer := smpp.NewSafeDialer(smpp.DialOptions{
		Timeout:       timeout,
		AllowLoopback: allowLoopback,
	})
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: timeout,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (c *Client) loadConnection(ctx context.Context, connID uuid.UUID) (cachedConn, error) {
	if c.cacheTTL > 0 {
		c.mu.RLock()
		entry, ok := c.cache[connID]
		c.mu.RUnlock()
		if ok && time.Now().Before(entry.expiresAt) {
			return entry, nil
		}
	}

	var out cachedConn
	var cfgRaw []byte
	var status string
	var archived bool
	err := c.pool.QueryRow(ctx, `
		SELECT http_base_url, http_auth_type, http_credentials_encrypted, http_dlr_mode, http_dlr_secret_encrypted, http_config, status, archived
		FROM provider_connections
		WHERE id = $1 AND protocol = 'http'`, connID).
		Scan(&out.baseURL, &out.authType, &out.credEnc, &out.dlrMode, &out.dlrSecretEnc, &cfgRaw, &status, &archived)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return cachedConn{}, fmt.Errorf("http connection %s not found", connID)
		}
		return cachedConn{}, fmt.Errorf("load http connection %s: %w", connID, err)
	}
	if archived {
		return cachedConn{}, fmt.Errorf("http connection %s is archived", connID)
	}
	if len(cfgRaw) > 0 {
		_ = json.Unmarshal(cfgRaw, &out.cfg)
	}
	if c.cacheTTL > 0 {
		out.expiresAt = time.Now().Add(c.cacheTTL)
		c.mu.Lock()
		c.cache[connID] = out
		c.mu.Unlock()
	}
	return out, nil
}

func newSimulatedHTTPServer(mode string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(io.LimitReader(r.Body, 16384))
		w.Header().Set("Content-Type", "application/json")
		switch strings.ToLower(strings.TrimSpace(mode)) {
		case "reject":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"INVALID_DEST","permanent":true,"message":"invalid destination"}`))
		case "throttle":
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"THROTTLED","message":"rate limit exceeded"}`))
		case "server_err":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"INTERNAL","message":"upstream internal error"}`))
		default:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"message_id": "sim-" + uuid.New().String()[:12],
				"status":     "queued",
			})
		}
	}))
}
