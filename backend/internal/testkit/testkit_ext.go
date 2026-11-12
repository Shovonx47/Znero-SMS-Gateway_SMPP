package testkit

// testkit_ext.go — Stage 2 extensions:
//   - DB name guard (must end in "_test" when ZNERO_INTEGRATION=1)
//   - Kafka test-topic lifecycle helpers
//   - Injectable clock
//   - Log capture + secret scanner
//   - In-process SMPP mock server (scriptable responses)
//   - Fake HTTP provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/smpp/pdu"
	"github.com/znero-international/znero/backend/pkg/kafkaclient"
)

// ─────────────────────────────────────────────────────────────────────────────
// 1. Database Guard
// ─────────────────────────────────────────────────────────────────────────────

// RequireIntegrationDB skips the test if ZNERO_INTEGRATION != "1" and fatals
// if the target database name does not end in "_test". This guard prevents
// any integration test from accidentally running against a production or dev DB.
func RequireIntegrationDB(t testing.TB) string {
	t.Helper()
	return Integration(t)
}

// ─────────────────────────────────────────────────────────────────────────────
// 2. Kafka Test Topic Helpers
// ─────────────────────────────────────────────────────────────────────────────

// CreateTestTopic creates a unique single-partition Kafka topic for the test and
// registers a t.Cleanup that deletes it after the test completes.
// The topic name has the form: "znero.it.<prefix>.<uuid8>"
func CreateTestTopic(t testing.TB, brokers []string, prefix string) string {
	t.Helper()
	topicName := fmt.Sprintf("znero.it.%s.%s", prefix, uuid.New().String()[:8])

	cfg := kafkaclient.Config{
		Brokers:        brokers,
		ClientID:       "testkit-topic-mgr",
		DialTimeout:    5 * time.Second,
		RequestTimeout: 10 * time.Second,
	}
	cl, err := kafkaclient.New(cfg, slog.Default())
	if err != nil {
		t.Fatalf("CreateTestTopic: kafka client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := cl.EnsureTopic(ctx, topicName, 1, 1); err != nil {
		cl.Close()
		t.Fatalf("CreateTestTopic: ensure topic %q: %v", topicName, err)
	}

	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dcancel()
		if err := cl.DeleteTopic(dctx, topicName); err != nil {
			t.Logf("CreateTestTopic cleanup: delete topic %q: %v (non-fatal)", topicName, err)
		}
		cl.Close()
	})
	return topicName
}

// ─────────────────────────────────────────────────────────────────────────────
// 3. Injectable Clock
// ─────────────────────────────────────────────────────────────────────────────

// Clock is a minimal interface for injectable time in components that accept it.
type Clock interface {
	Now() time.Time
}

// TestClock is a test-controllable clock. It starts at startTime and can be
// advanced with Advance. All methods are safe for concurrent use.
type TestClock struct {
	mu      sync.Mutex
	current time.Time
}

// NewTestClock creates a TestClock starting at startTime. If startTime is zero
// it defaults to 2024-01-01 UTC.
func NewTestClock(startTime time.Time) *TestClock {
	if startTime.IsZero() {
		startTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return &TestClock{current: startTime}
}

// Now returns the current test time.
func (c *TestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

// Advance moves the clock forward by d.
func (c *TestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(d)
}

// Set sets the clock to an absolute time.
func (c *TestClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = t
}

// ─────────────────────────────────────────────────────────────────────────────
// 4. Log Capture & Secret Scanner
// ─────────────────────────────────────────────────────────────────────────────

// sensitivePatterns is a list of patterns that must never appear in logs.
var sensitivePatterns = []string{
	"znk_live_",   // API key plaintext
	"password",    // any password field value
	"secret",      // webhook secrets
	"DevSeed1234", // dev seed password
}

// LogCapture captures structured log output during a test and scans for secrets.
type LogCapture struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

// NewLogCapture creates a LogCapture and returns an *slog.Logger writing to it.
// Call ScanForLeaks(t) at the end of your test.
func NewLogCapture() (*LogCapture, *slog.Logger) {
	lc := &LogCapture{}
	logger := slog.New(slog.NewJSONHandler(&lc.buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return lc, logger
}

// ScanForLeaks fails the test if any captured log line contains a sensitive pattern.
func (lc *LogCapture) ScanForLeaks(t testing.TB) {
	t.Helper()
	dataStr := lc.Output()

	scanner := bufio.NewScanner(strings.NewReader(dataStr))
	for scanner.Scan() {
		line := scanner.Text()
		lower := strings.ToLower(line)
		for _, pat := range sensitivePatterns {
			if strings.Contains(lower, strings.ToLower(pat)) {
				// Allow the pattern name itself (e.g. "password" key in JSON)
				// but fail if a non-empty value follows the pattern.
				t.Errorf("SECURITY: potential secret leak in log: pattern %q found in line: %s", pat, truncate(line, 200))
			}
		}
	}
}

// Output returns a safe copy of all captured structured log text.
func (lc *LogCapture) Output() string {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.buf.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ─────────────────────────────────────────────────────────────────────────────
// 5. In-Process SMPP Mock Server
// ─────────────────────────────────────────────────────────────────────────────

// SMPPResponseMode configures how the mock SMSC responds to submit_sm PDUs.
type SMPPResponseMode int

const (
	// SMPPModeAccept replies with ESME_ROK and a generated message ID.
	SMPPModeAccept SMPPResponseMode = iota
	// SMPPModeReject replies with a permanent error (ESME_RINVDSTADR).
	SMPPModeReject
	// SMPPModeThrottle replies with ESME_RTHROTTLED.
	SMPPModeThrottle
	// SMPPModeNoResponse drops the PDU without replying (simulates ambiguous outcome).
	SMPPModeNoResponse
)

// SMPPMockServer is a minimal in-process SMPP 3.4 server for testing.
// It accepts one bind, then responds to submit_sm PDUs according to Mode.
type SMPPMockServer struct {
	Addr     string
	Mode     SMPPResponseMode
	DLRDelay time.Duration // If >0 and Mode==SMPPModeAccept, send a deliver_sm after this delay

	listener net.Listener
	mu       sync.Mutex
	t        testing.TB
	connects int
	submits  []string // collected provider message IDs
}

// NewSMPPMockServer starts a mock SMPP server on a random port and registers cleanup.
func NewSMPPMockServer(t testing.TB, mode SMPPResponseMode, dlrDelay time.Duration) *SMPPMockServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("SMPPMockServer: listen: %v", err)
	}
	s := &SMPPMockServer{
		Addr:     ln.Addr().String(),
		Mode:     mode,
		DLRDelay: dlrDelay,
		listener: ln,
		t:        t,
	}
	t.Cleanup(func() { _ = ln.Close() })
	go s.acceptLoop()
	return s
}

// Submits returns the provider message IDs generated for accepted submit_sm PDUs.
func (s *SMPPMockServer) Submits() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.submits))
	copy(out, s.submits)
	return out
}

func (s *SMPPMockServer) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // listener closed
		}
		s.mu.Lock()
		s.connects++
		s.mu.Unlock()
		go s.handleConn(conn)
	}
}

func (s *SMPPMockServer) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	buf := make([]byte, 65536)

	for {
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		// Read PDU header (16 bytes)
		if _, err := io.ReadFull(conn, buf[:16]); err != nil {
			return
		}
		cmdLen := binary.BigEndian.Uint32(buf[0:4])
		cmdID := binary.BigEndian.Uint32(buf[4:8])
		seqNum := binary.BigEndian.Uint32(buf[12:16])

		if cmdLen > 65536 || cmdLen < 16 {
			return
		}
		body := make([]byte, cmdLen-16)
		if len(body) > 0 {
			if _, err := io.ReadFull(conn, body); err != nil {
				return
			}
		}

		switch cmdID {
		case pdu.CmdBindTransmitter, pdu.CmdBindReceiver, pdu.CmdBindTransceiver:
			// Accept any bind (body is "ZNERO\0" = 6 bytes -> command_length = 22)
			sysID := []byte("ZNERO\x00")
			resp := buildRespHeader(uint32(16+len(sysID)), cmdID|0x80000000, 0, seqNum)
			resp = append(resp, sysID...)
			_, _ = conn.Write(resp)

		case pdu.CmdSubmitSM:
			switch s.Mode {
			case SMPPModeAccept:
				msgID := uuid.New().String()[:16]
				s.mu.Lock()
				s.submits = append(s.submits, msgID)
				s.mu.Unlock()

				resp := buildRespHeader(17+uint32(len(msgID)), pdu.CmdSubmitSMResp, 0, seqNum)
				resp = append(resp, []byte(msgID)...)
				resp = append(resp, 0)
				_, _ = conn.Write(resp)

				if s.DLRDelay > 0 {
					go s.sendDLR(conn, msgID, s.DLRDelay)
				}
			case SMPPModeReject:
				// ESME_RINVDSTADR = 0x0000000B
				resp := buildRespHeader(16, pdu.CmdSubmitSMResp, 0x0000000B, seqNum)
				_, _ = conn.Write(resp)
			case SMPPModeThrottle:
				// ESME_RTHROTTLED = 0x00000058
				resp := buildRespHeader(16, pdu.CmdSubmitSMResp, 0x00000058, seqNum)
				_, _ = conn.Write(resp)
			case SMPPModeNoResponse:
				// Drop — do nothing
			}

		case pdu.CmdEnquireLink:
			resp := buildRespHeader(16, pdu.CmdEnquireLinkResp, 0, seqNum)
			_, _ = conn.Write(resp)

		case pdu.CmdUnbind:
			resp := buildRespHeader(16, pdu.CmdUnbindResp, 0, seqNum)
			_, _ = conn.Write(resp)
			return

		case pdu.CmdGenericNack:
			// ignore
		}
	}
}

func (s *SMPPMockServer) sendDLR(conn net.Conn, msgID string, delay time.Duration) {
	time.Sleep(delay)
	receipt := fmt.Sprintf("id:%s sub:001 dlvrd:001 submit date:2401010000 done date:2401010001 stat:DELIVRD err:000", msgID)
	body := buildDeliverSMBody(msgID, receipt)
	seqNum := uint32(999)
	hdr := buildRespHeader(uint32(16+len(body)), pdu.CmdDeliverSM, 0, seqNum)
	pkt := append(hdr, body...)
	_, _ = conn.Write(pkt)
}

func buildRespHeader(length, cmdID, status, seqNum uint32) []byte {
	b := make([]byte, 16)
	binary.BigEndian.PutUint32(b[0:], length)
	binary.BigEndian.PutUint32(b[4:], cmdID)
	binary.BigEndian.PutUint32(b[8:], status)
	binary.BigEndian.PutUint32(b[12:], seqNum)
	return b
}

func buildDeliverSMBody(srcAddr, receipt string) []byte {
	var b []byte
	b = append(b, 0)                               // service_type
	b = append(b, 0x01)                            // source_addr_ton NPI
	b = append(b, 0x01)                            // source_addr_npi
	b = append(b, []byte(srcAddr)...)              // source_addr
	b = append(b, 0)                               // source_addr null term
	b = append(b, 0x01, 0x01)                      // dest_addr_ton/npi
	b = append(b, 'D', 'E', 'S', 'T', 0)           // destination_addr
	b = append(b, 0x04)                            // esm_class: delivery receipt
	b = append(b, 0, 0, 0, 0x00, 0x00, 0, 0, 0x00) // protocol_id .. registered_delivery
	b = append(b, 0)                               // replace_if_present
	b = append(b, 0x01)                            // data_coding: GSM7
	b = append(b, 0)                               // sm_default_msg_id
	b = append(b, byte(len(receipt)))              // sm_length
	b = append(b, []byte(receipt)...)
	return b
}

// ─────────────────────────────────────────────────────────────────────────────
// 6. Fake HTTP Provider Server
// ─────────────────────────────────────────────────────────────────────────────

// HTTPProviderMode configures how the fake HTTP provider server responds.
type HTTPProviderMode int

const (
	HTTPModeAccept    HTTPProviderMode = iota // 200 + JSON {message_id: ...}
	HTTPModeReject                            // 400 permanent error
	HTTPModeThrottle                          // 429 retryable
	HTTPModeServerErr                         // 500 retryable
)

// FakeHTTPProvider is an httptest.Server acting as a fake SMS HTTP provider.
type FakeHTTPProvider struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	bodies   [][]byte
	Mode     HTTPProviderMode
}

// NewFakeHTTPProvider starts a fake provider HTTP server and registers cleanup.
func NewFakeHTTPProvider(t testing.TB, mode HTTPProviderMode) *FakeHTTPProvider {
	t.Helper()
	fp := &FakeHTTPProvider{Mode: mode}
	fp.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fp.mu.Lock()
		fp.requests = append(fp.requests, r)
		fp.bodies = append(fp.bodies, body)
		m := fp.Mode
		fp.mu.Unlock()

		switch m {
		case HTTPModeAccept:
			msgID := uuid.New().String()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"message_id": msgID, "status": "queued"})
		case HTTPModeReject:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"INVALID_DEST","permanent":true}`))
		case HTTPModeThrottle:
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"THROTTLED"}`))
		case HTTPModeServerErr:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"INTERNAL"}`))
		}
	}))
	t.Cleanup(fp.Server.Close)
	return fp
}

// RequestCount returns how many requests the fake provider has received.
func (fp *FakeHTTPProvider) RequestCount() int {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return len(fp.requests)
}

// SetMode changes the response mode mid-test.
func (fp *FakeHTTPProvider) SetMode(mode HTTPProviderMode) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.Mode = mode
}
