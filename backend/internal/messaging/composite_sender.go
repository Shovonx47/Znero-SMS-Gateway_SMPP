package messaging

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CompositeSender routes outbound SendAttempt traffic to the underlying protocol Sender
// ("smpp" or "http") based on the route connection's configured protocol in provider_connections.
type CompositeSender struct {
	pool     *pgxpool.Pool
	senders  map[string]Sender
	cacheTTL time.Duration

	mu    sync.RWMutex
	cache map[uuid.UUID]cachedProto
}

type cachedProto struct {
	protocol  string
	expiresAt time.Time
}

// NewCompositeSender creates a unified Sender dispatching to protocol-specific connectors.
func NewCompositeSender(pool *pgxpool.Pool, senders map[string]Sender, cacheTTL time.Duration) *CompositeSender {
	clean := make(map[string]Sender, len(senders))
	for k, v := range senders {
		if v != nil {
			clean[strings.ToLower(strings.TrimSpace(k))] = v
		}
	}
	return &CompositeSender{
		pool:     pool,
		senders:  clean,
		cacheTTL: cacheTTL,
		cache:    make(map[uuid.UUID]cachedProto),
	}
}

// CanSend returns true if this dispatcher node holds (or can acquire) ownership for connID.
func (c *CompositeSender) CanSend(ctx context.Context, connID uuid.UUID) bool {
	proto, err := c.lookupProtocol(ctx, connID)
	if err != nil {
		return true
	}
	sender, ok := c.senders[proto]
	if !ok {
		return true
	}
	if checker, ok := sender.(PreSendChecker); ok {
		return checker.CanSend(ctx, connID)
	}
	return true
}

// Send determines the connection's protocol and forwards the SendAttempt to the matching connector.
func (c *CompositeSender) Send(ctx context.Context, attempt SendAttempt) (SendResult, error) {
	proto, err := c.lookupProtocol(ctx, attempt.ConnectionID)
	if err != nil {
		return SendResult{
			Status:       SendStatusRetryableReject,
			ErrorCode:    "PROTOCOL_LOOKUP_ERROR",
			ErrorMessage: err.Error(),
		}, nil
	}
	sender, ok := c.senders[proto]
	if !ok {
		return SendResult{
			Status:       SendStatusFatalReject,
			ErrorCode:    "UNSUPPORTED_PROTOCOL",
			ErrorMessage: fmt.Sprintf("no egress sender registered for protocol %q", proto),
		}, nil
	}
	return sender.Send(ctx, attempt)
}

func (c *CompositeSender) lookupProtocol(ctx context.Context, connID uuid.UUID) (string, error) {
	if c.cacheTTL > 0 {
		c.mu.RLock()
		entry, ok := c.cache[connID]
		c.mu.RUnlock()
		if ok && time.Now().Before(entry.expiresAt) {
			return entry.protocol, nil
		}
	}

	var proto string
	err := c.pool.QueryRow(ctx, `SELECT protocol FROM provider_connections WHERE id = $1`, connID).Scan(&proto)
	if err != nil {
		return "", fmt.Errorf("lookup connection protocol %s: %w", connID, err)
	}
	proto = strings.ToLower(strings.TrimSpace(proto))
	if c.cacheTTL > 0 {
		c.mu.Lock()
		c.cache[connID] = cachedProto{protocol: proto, expiresAt: time.Now().Add(c.cacheTTL)}
		c.mu.Unlock()
	}
	return proto, nil
}
