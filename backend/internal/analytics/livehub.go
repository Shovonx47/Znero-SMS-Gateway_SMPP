package analytics

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/pkg/tenant"
	"github.com/znero-international/znero/backend/pkg/tokens"
)

// LiveHub coordinates WebSocket connections and telemetry fan-out.
type LiveHub struct {
	tokenMgr    *tokens.TokenManager
	svc         *Service
	logger      *slog.Logger
	clients     map[*LiveClient]bool
	clientsMu   sync.RWMutex
	stopCh      chan struct{}
	doneCh      chan struct{}
	broadcastCh chan []byte
}

func NewLiveHub(tokenMgr *tokens.TokenManager, svc *Service, logger *slog.Logger) *LiveHub {
	if logger == nil {
		logger = slog.Default()
	}
	return &LiveHub{
		tokenMgr:    tokenMgr,
		svc:         svc,
		logger:      logger.With(slog.String("component", "live_hub")),
		clients:     make(map[*LiveClient]bool),
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
		broadcastCh: make(chan []byte, 256),
	}
}

// ComputeAcceptKey calculates the RFC 6455 Sec-WebSocket-Accept handshake header.
func ComputeAcceptKey(clientKey string) string {
	h := sha1.New()
	h.Write([]byte(strings.TrimSpace(clientKey) + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// WriteTextFrame writes an unmasked text frame (RFC 6455 5.1).
func WriteTextFrame(w io.Writer, payload []byte) error {
	length := len(payload)
	var header []byte

	if length <= 125 {
		header = []byte{0x81, byte(length)}
	} else if length <= 65535 {
		header = []byte{0x81, 126, byte(length >> 8), byte(length & 0xFF)}
	} else {
		header = make([]byte, 10)
		header[0] = 0x81
		header[1] = 127
		binary.BigEndian.PutUint64(header[2:], uint64(length))
	}

	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// WriteCloseFrame sends an RFC 6455 close control frame (opcode 0x8).
func WriteCloseFrame(w io.Writer, statusCode uint16, reason string) error {
	payload := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(payload[:2], statusCode)
	copy(payload[2:], reason)

	length := len(payload)
	header := []byte{0x88, byte(length)}
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// LiveClient represents an active WebSocket connection.
type LiveClient struct {
	hub       *LiveHub
	conn      net.Conn
	tenantCtx *tenant.Context
	sendCh    chan []byte
	doneCh    chan struct{}
	closeOnce sync.Once
}

func (c *LiveClient) close() {
	c.closeOnce.Do(func() {
		close(c.doneCh)
		_ = c.conn.Close()
	})
}

// writeLoop pumps messages from sendCh to the network connection.
func (c *LiveClient) writeLoop() {
	defer c.close()

	for {
		select {
		case <-c.doneCh:
			return
		case msg, ok := <-c.sendCh:
			if !ok {
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := WriteTextFrame(c.conn, msg); err != nil {
				return
			}
		}
	}
}

// readLoop reads frames from the client and handles ping/pong or client termination.
func (c *LiveClient) readLoop() {
	defer c.close()

	buf := make([]byte, 4096)
	for {
		_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		n, err := c.conn.Read(buf)
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}

		opcode := buf[0] & 0x0F
		if opcode == 0x8 { // Close frame
			return
		} else if opcode == 0x9 { // Ping frame -> reply Pong (0xA)
			pong := []byte{0x8A, 0x00}
			_ = c.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			_, _ = c.conn.Write(pong)
		}
	}
}

// HandleWebSocket upgrades HTTP requests to RFC 6455 WebSocket.
func (h *LiveHub) HandleWebSocket(c *fiber.Ctx) error {
	connectionHdr := strings.ToLower(c.Get("Connection"))
	upgradeHdr := strings.ToLower(c.Get("Upgrade"))
	clientKey := c.Get("Sec-WebSocket-Key")

	if !strings.Contains(connectionHdr, "upgrade") || upgradeHdr != "websocket" || clientKey == "" {
		return c.Status(fiber.StatusBadRequest).SendString("Expected WebSocket Upgrade header")
	}

	// Extract token if provided at handshake
	tokenStr := c.Query("token")
	if tokenStr == "" {
		authHdr := c.Get("Authorization")
		if strings.HasPrefix(strings.ToLower(authHdr), "bearer ") {
			tokenStr = strings.TrimSpace(authHdr[7:])
		}
	}
	if tokenStr == "" {
		tokenStr = c.Get("Sec-WebSocket-Protocol")
	}

	var claims *tokens.AccessClaims
	if tokenStr != "" && h.tokenMgr != nil {
		var err error
		claims, err = h.tokenMgr.ValidateAccessToken(tokenStr)
		if err != nil {
			return c.Status(fiber.StatusUnauthorized).SendString("Invalid access token")
		}
	}

	acceptKey := ComputeAcceptKey(clientKey)

	c.Status(fiber.StatusSwitchingProtocols)
	c.Set("Upgrade", "websocket")
	c.Set("Connection", "Upgrade")
	c.Set("Sec-WebSocket-Accept", acceptKey)

	// Hijack the underlying TCP socket
	c.Context().Hijack(func(conn net.Conn) {
		h.serveConnection(conn, claims)
	})

	return nil
}

// serveConnection handles the lifecycle of a hijacked WebSocket socket.
func (h *LiveHub) serveConnection(conn net.Conn, claims *tokens.AccessClaims) {
	defer conn.Close()

	// 1. Enforce 5-second auth timeout if token was not provided at handshake
	if claims == nil {
		authBuf := make([]byte, 2048)
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := conn.Read(authBuf)
		if err != nil || n <= 2 {
			_ = WriteCloseFrame(conn, 4401, "Authentication timeout (5s)")
			return
		}

		// Parse masked RFC 6455 text frame from client
		payload, err := parseClientTextPayload(authBuf[:n])
		if err != nil {
			_ = WriteCloseFrame(conn, 4401, "Malformed auth frame")
			return
		}

		var authMsg struct {
			Type  string `json:"type"`
			Token string `json:"token"`
		}
		if err := json.Unmarshal(payload, &authMsg); err != nil || authMsg.Token == "" || h.tokenMgr == nil {
			_ = WriteCloseFrame(conn, 4401, "Missing or invalid token")
			return
		}

		claims, err = h.tokenMgr.ValidateAccessToken(authMsg.Token)
		if err != nil {
			_ = WriteCloseFrame(conn, 4403, "Invalid access token")
			return
		}
	}

	// Build verified tenant context
	tc := claimsToTenantContext(claims)
	client := &LiveClient{
		hub:       h,
		conn:      conn,
		tenantCtx: tc,
		sendCh:    make(chan []byte, 64),
		doneCh:    make(chan struct{}),
	}

	h.registerClient(client)
	defer h.unregisterClient(client)

	// Send initial greeting frame
	initMsg, _ := json.Marshal(map[string]interface{}{
		"type":      "connected",
		"org_type":  tc.ActorOrgType,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	_ = WriteTextFrame(conn, initMsg)

	go client.writeLoop()
	client.readLoop()
}

func parseClientTextPayload(frame []byte) ([]byte, error) {
	if len(frame) < 6 {
		return nil, errors.New("frame too short")
	}
	// opcode := frame[0] & 0x0F
	isMasked := (frame[1] & 0x80) != 0
	if !isMasked {
		return nil, errors.New("client frame must be masked")
	}

	payloadLen := int(frame[1] & 0x7F)
	maskOffset := 2
	if payloadLen == 126 {
		if len(frame) < 8 {
			return nil, errors.New("frame too short for 16-bit length")
		}
		payloadLen = int(binary.BigEndian.Uint16(frame[2:4]))
		maskOffset = 4
	} else if payloadLen == 127 {
		if len(frame) < 14 {
			return nil, errors.New("frame too short for 64-bit length")
		}
		payloadLen = int(binary.BigEndian.Uint64(frame[2:10]))
		maskOffset = 10
	}

	if len(frame) < maskOffset+4+payloadLen {
		return nil, errors.New("truncated payload")
	}

	maskKey := frame[maskOffset : maskOffset+4]
	data := frame[maskOffset+4 : maskOffset+4+payloadLen]

	unmasked := make([]byte, payloadLen)
	for i := 0; i < payloadLen; i++ {
		unmasked[i] = data[i] ^ maskKey[i%4]
	}
	return unmasked, nil
}

func claimsToTenantContext(claims *tokens.AccessClaims) *tenant.Context {
	if claims == nil {
		return &tenant.Context{ActorOrgType: "customer"}
	}
	perms := make(map[string]bool, len(claims.Permissions))
	for _, p := range claims.Permissions {
		perms[p] = true
	}
	return &tenant.Context{
		ActorUserID:     claims.UserID,
		ActorOrgID:      claims.OrgID,
		ActorOrgType:    claims.OrgType,
		ActorOrgPath:    claims.OrgPath,
		IsPlatformAdmin: claims.IsPlatformAdmin,
		Permissions:     perms,
	}
}

func (h *LiveHub) registerClient(c *LiveClient) {
	h.clientsMu.Lock()
	h.clients[c] = true
	h.clientsMu.Unlock()
	h.logger.Debug("registered live hub client", slog.String("org_type", c.tenantCtx.ActorOrgType))
}

func (h *LiveHub) unregisterClient(c *LiveClient) {
	h.clientsMu.Lock()
	delete(h.clients, c)
	h.clientsMu.Unlock()
	c.close()
	h.logger.Debug("unregistered live hub client", slog.String("org_type", c.tenantCtx.ActorOrgType))
}

// ClientCount returns the number of active WebSocket connections.
func (h *LiveHub) ClientCount() int {
	h.clientsMu.RLock()
	defer h.clientsMu.RUnlock()
	return len(h.clients)
}

// Start begins the broadcast loop fanning out metrics every 1 second.
func (h *LiveHub) Start(ctx context.Context) {
	go h.runBroadcastLoop(ctx)
}

// Stop shuts down the broadcast loop and closes all client connections.
func (h *LiveHub) Stop() {
	close(h.stopCh)
	h.clientsMu.Lock()
	for c := range h.clients {
		c.close()
		delete(h.clients, c)
	}
	h.clientsMu.Unlock()
	<-h.doneCh
}

func (h *LiveHub) runBroadcastLoop(ctx context.Context) {
	defer close(h.doneCh)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-h.stopCh:
			return
		case <-ticker.C:
			h.broadcastTick(ctx)
		}
	}
}

func (h *LiveHub) broadcastTick(ctx context.Context) {
	h.clientsMu.RLock()
	clientCount := len(h.clients)
	h.clientsMu.RUnlock()

	if clientCount == 0 {
		return // No active clients, zero queries
	}

	platformTc := &tenant.Context{
		ActorUserID:     uuid.Nil,
		ActorOrgType:    "platform",
		IsPlatformAdmin: true,
	}

	// Single O(1) query for all connected clients
	liveNOC, err := h.svc.GetLiveNOC(ctx, platformTc)
	if err != nil {
		h.logger.Warn("failed to fetch live noc metrics for broadcast", slog.String("error", err.Error()))
		return
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)

	// Pre-marshal role-specific payloads to avoid redundant JSON encoding
	platformPayload, _ := json.Marshal(map[string]interface{}{
		"type":                  "telemetry",
		"role":                  "platform",
		"timestamp":             nowStr,
		"ingress_tps":           liveNOC.CurrentIngressTPS,
		"egress_tps":            liveNOC.CurrentEgressTPS,
		"outbox_depth":          liveNOC.OutboxDepth,
		"outbox_oldest_age_sec": liveNOC.OutboxOldestAgeSec,
		"delivery_success_24h":  liveNOC.DeliverySuccess24h,
		"active_connections":    len(liveNOC.ActiveConnections),
		"firing_alerts":         liveNOC.FiringAlertsCount,
	})

	resellerPayload, _ := json.Marshal(map[string]interface{}{
		"type":                 "telemetry",
		"role":                 "reseller",
		"timestamp":            nowStr,
		"ingress_tps":          liveNOC.CurrentIngressTPS,
		"egress_tps":           liveNOC.CurrentEgressTPS,
		"delivery_success_24h": liveNOC.DeliverySuccess24h,
	})

	customerPayload, _ := json.Marshal(map[string]interface{}{
		"type":                 "telemetry",
		"role":                 "customer",
		"timestamp":            nowStr,
		"delivery_success_24h": liveNOC.DeliverySuccess24h,
	})

	h.clientsMu.RLock()
	defer h.clientsMu.RUnlock()

	for client := range h.clients {
		var payload []byte
		switch client.tenantCtx.ActorOrgType {
		case "platform":
			payload = platformPayload
		case "reseller":
			payload = resellerPayload
		default:
			payload = customerPayload
		}

		select {
		case client.sendCh <- payload:
		default:
			// Slow consumer: channel full, drop frame to protect memory
			h.logger.Warn("slow websocket consumer, dropping frame", slog.String("org_type", client.tenantCtx.ActorOrgType))
		}
	}
}
