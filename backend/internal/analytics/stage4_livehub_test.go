package analytics_test

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/internal/analytics"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/pkg/tokens"
)

// performRawWebSocketHandshake dials the server, performs the HTTP upgrade, and returns the net.Conn, bufio.Reader, and response.
func performRawWebSocketHandshake(t *testing.T, addr, path string, extraHeaders map[string]string) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}

	reqStr := fmt.Sprintf(
		"GET %s HTTP/1.1\r\n"+
			"Host: %s\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"+
			"Sec-WebSocket-Version: 13\r\n",
		path, addr,
	)
	for k, v := range extraHeaders {
		reqStr += fmt.Sprintf("%s: %s\r\n", k, v)
	}
	reqStr += "\r\n"

	if _, err := conn.Write([]byte(reqStr)); err != nil {
		conn.Close()
		t.Fatalf("write handshake: %v", err)
	}

	bufr := bufio.NewReader(conn)
	resp, err := http.ReadResponse(bufr, nil)
	if err != nil {
		conn.Close()
		t.Fatalf("read handshake response: %v", err)
	}

	return conn, bufr, resp
}

// readServerTextFrame reads an unmasked RFC 6455 frame from the reader.
func readServerTextFrame(t *testing.T, conn net.Conn, r io.Reader, timeout time.Duration) (byte, []byte) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))

	header := make([]byte, 2)
	if _, err := io.ReadFull(r, header); err != nil {
		t.Fatalf("read frame header: %v", err)
	}

	opcode := header[0] & 0x0F
	payloadLen := int(header[1] & 0x7F)

	if payloadLen == 126 {
		lenBuf := make([]byte, 2)
		if _, err := io.ReadFull(r, lenBuf); err != nil {
			t.Fatalf("read 16-bit len: %v", err)
		}
		payloadLen = int(binary.BigEndian.Uint16(lenBuf))
	} else if payloadLen == 127 {
		lenBuf := make([]byte, 8)
		if _, err := io.ReadFull(r, lenBuf); err != nil {
			t.Fatalf("read 64-bit len: %v", err)
		}
		payloadLen = int(binary.BigEndian.Uint64(lenBuf))
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		t.Fatalf("read payload: %v", err)
	}

	return opcode, payload
}

func TestStage4_RFC6455_FramingAndHandshakeKey(t *testing.T) {
	// 1. Accept Key derivation per RFC 6455 Section 1.3 example
	clientKey := "dGhlIHNhbXBsZSBub25jZQ=="
	wantAccept := "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	gotAccept := analytics.ComputeAcceptKey(clientKey)
	if gotAccept != wantAccept {
		t.Errorf("ComputeAcceptKey(%q) = %q, want %q", clientKey, gotAccept, wantAccept)
	}
}

func TestStage4_LiveHub_AuthTimeoutAndTenantIsolation(t *testing.T) {
	dbURL := testkit.RequireIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	tokenMgr, err := tokens.NewTokenManager(testkit.RandHex(32), 15*time.Minute)
	if err != nil {
		t.Fatalf("token manager: %v", err)
	}

	repo := analytics.NewRepository(pool, nil)
	svc := analytics.NewService(repo, nil)
	hub := analytics.NewLiveHub(tokenMgr, svc, nil)

	// Build standalone Fiber app for live hub test
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	handler := analytics.NewHandler(svc)
	handler.SetLiveHub(hub)
	handler.Register(app.Group("/api/v1"), func(c *fiber.Ctx) error { return c.Next() })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen loopback: %v", err)
	}
	defer ln.Close()

	serverAddr := ln.Addr().String()
	go func() {
		_ = app.Listener(ln)
	}()

	hub.Start(ctx)
	defer hub.Stop()

	// 1. Auth Timeout Test: Connect with no token, do not send auth message
	conn1, bufr1, resp1 := performRawWebSocketHandshake(t, serverAddr, "/api/v1/analytics/live/ws", nil)
	if resp1.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101 Switching Protocols, got %d", resp1.StatusCode)
	}

	// Verify server sends close frame 4401 after 5-second auth timeout
	opcode, payload := readServerTextFrame(t, conn1, bufr1, 7*time.Second)
	if opcode != 0x8 { // Close frame opcode
		t.Errorf("expected close frame (0x8) on auth timeout, got opcode 0x%X (payload: %s)", opcode, string(payload))
	}
	conn1.Close()

	// 2. Connect with Valid Tokens (Platform vs Customer)
	platformToken, err := tokenMgr.GenerateAccessToken(uuid.New(), uuid.New(), uuid.New(), "platform", "/p/", true, []string{"analytics.dashboard.view", "analytics.noc.view"})
	if err != nil {
		t.Fatalf("platform token: %v", err)
	}

	customerToken, err := tokenMgr.GenerateAccessToken(uuid.New(), uuid.New(), uuid.New(), "customer", "/c/", false, []string{"analytics.dashboard.view"})
	if err != nil {
		t.Fatalf("customer token: %v", err)
	}

	// Connect platform client
	connPlat, bufrPlat, respPlat := performRawWebSocketHandshake(t, serverAddr, fmt.Sprintf("/api/v1/analytics/live/ws?token=%s", platformToken), nil)
	if respPlat.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101 for platform client, got %d", respPlat.StatusCode)
	}
	defer connPlat.Close()

	// Read initial greeting
	_, greetPlat := readServerTextFrame(t, connPlat, bufrPlat, 2*time.Second)
	if !strings.Contains(string(greetPlat), `"org_type":"platform"`) {
		t.Errorf("platform greeting mismatch: %s", string(greetPlat))
	}

	// Connect customer client
	connCust, bufrCust, respCust := performRawWebSocketHandshake(t, serverAddr, fmt.Sprintf("/api/v1/analytics/live/ws?token=%s", customerToken), nil)
	if respCust.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101 for customer client, got %d", respCust.StatusCode)
	}
	defer connCust.Close()

	// Read initial greeting
	_, greetCust := readServerTextFrame(t, connCust, bufrCust, 2*time.Second)
	if !strings.Contains(string(greetCust), `"org_type":"customer"`) {
		t.Errorf("customer greeting mismatch: %s", string(greetCust))
	}

	if hub.ClientCount() != 2 {
		t.Errorf("expected 2 active clients in hub, got %d", hub.ClientCount())
	}

	// 3. Wait for 1-second broadcast tick
	_, platFrame := readServerTextFrame(t, connPlat, bufrPlat, 3*time.Second)
	_, custFrame := readServerTextFrame(t, connCust, bufrCust, 3*time.Second)

	platStr := string(platFrame)
	custStr := string(custFrame)

	// Platform frame MUST contain platform fields
	if !strings.Contains(platStr, `"ingress_tps"`) || !strings.Contains(platStr, `"outbox_depth"`) {
		t.Errorf("platform frame missing expected platform telemetry: %s", platStr)
	}

	// Customer frame MUST NOT contain platform fields
	if strings.Contains(custStr, `"outbox_depth"`) {
		t.Errorf("TENANCY LEAK: customer frame leaked outbox_depth: %s", custStr)
	}
	if strings.Contains(custStr, `"firing_alerts"`) {
		t.Errorf("TENANCY LEAK: customer frame leaked firing_alerts: %s", custStr)
	}
	if strings.Contains(custStr, `"active_connections"`) {
		t.Errorf("TENANCY LEAK: customer frame leaked active_connections: %s", custStr)
	}

	// Verify JSON validity
	var custData map[string]interface{}
	if err := json.Unmarshal(custFrame, &custData); err != nil {
		t.Errorf("customer frame invalid JSON: %v", err)
	}
	if custData["role"] != "customer" {
		t.Errorf("expected customer role, got %v", custData["role"])
	}
}
