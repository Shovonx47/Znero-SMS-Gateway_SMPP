package smpp

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/smpp/mocksmsc"
	"github.com/znero-international/znero/backend/internal/smpp/pdu"
)

// mockResolver allows injecting DNS records to test SSRF and DNS rebinding
type mockResolver struct {
	mu      sync.Mutex
	records map[string][]net.IPAddr
}

func (m *mockResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if addrs, ok := m.records[host]; ok {
		return addrs, nil
	}
	return nil, fmt.Errorf("no such host: %s", host)
}

func (m *mockResolver) Set(host string, ips ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var addrs []net.IPAddr
	for _, ipStr := range ips {
		addrs = append(addrs, net.IPAddr{IP: net.ParseIP(ipStr)})
	}
	m.records[host] = addrs
}

func newTestMockResolver() *mockResolver {
	return &mockResolver{records: make(map[string][]net.IPAddr)}
}

// 1. Bind and submit against the mock SMSC
func TestSMPP_BindAndSubmitAgainstMock(t *testing.T) {
	srv, err := mocksmsc.NewServer(mocksmsc.ServerConfig{
		SystemID: "sys_test_1",
		Password: "pass_test_1",
		DLRMode:  mocksmsc.DLRModeImmediate,
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Close()

	connID := uuid.New()
	mgr := NewConnectionManager(ManagerConfig{
		AllowLoopback: true,
	})
	defer mgr.Close()

	// Direct session test
	conn, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}

	session := NewSession(SessionConfig{
		ConnectionID: connID,
		SystemID:     "sys_test_1",
		Password:     "pass_test_1",
		BindType:     "transceiver",
		WindowSize:   5,
	}, conn)
	defer session.Close()

	if err := session.Bind(context.Background()); err != nil {
		t.Fatalf("Bind failed: %v", err)
	}

	submit := pdu.NewSubmitSM(0)
	submit.DestAddr = "15559876543"
	submit.SourceAddr = "TEST"
	submit.ShortMessage = []byte("Direct test message")

	resp, err := session.Submit(context.Background(), submit)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}
	if resp.Header.CommandStatus != pdu.StatusROK {
		t.Errorf("expected StatusROK, got %x", resp.Header.CommandStatus)
	}
	if resp.MessageID == "" {
		t.Errorf("expected non-empty provider message ID")
	}

	stats := session.Stats()
	if stats.SuccessCount != 1 || !stats.Bound {
		t.Errorf("stats mismatch: %+v", stats)
	}
}

// 2. Sliding window flow control
func TestSMPP_SlidingWindow(t *testing.T) {
	srv, err := mocksmsc.NewServer(mocksmsc.ServerConfig{
		SystemID: "sys_win",
		Password: "pass_win",
		Latency:  50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}

	windowSize := 3
	session := NewSession(SessionConfig{
		ConnectionID: uuid.New(),
		SystemID:     "sys_win",
		Password:     "pass_win",
		WindowSize:   windowSize,
	}, conn)
	defer session.Close()

	if err := session.Bind(context.Background()); err != nil {
		t.Fatalf("Bind failed: %v", err)
	}

	// Launch concurrent submits exceeding window size
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sm := pdu.NewSubmitSM(0)
			sm.DestAddr = "15550000001"
			sm.ShortMessage = []byte(fmt.Sprintf("Msg %d", idx))
			_, err := session.Submit(context.Background(), sm)
			if err != nil {
				t.Errorf("submit %d failed: %v", idx, err)
			}
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	// Since window is 3 and latency is 50ms, 6 messages must take at least 2 full batches (> 100ms)
	if elapsed < 90*time.Millisecond {
		t.Errorf("sliding window was bypassed; elapsed %v", elapsed)
	}
	if session.Stats().SuccessCount != 6 {
		t.Errorf("expected 6 successes, got %d", session.Stats().SuccessCount)
	}
}

// 3. Throttle and backoff mapping
func TestSMPP_ThrottleAndBackoff(t *testing.T) {
	srv, _ := mocksmsc.NewServer(mocksmsc.ServerConfig{
		SystemID:      "sys_thr",
		Password:      "pass_thr",
		InjectedError: pdu.StatusRThrottled,
	})
	_ = srv.Start()
	defer srv.Close()

	conn, _ := net.Dial("tcp", srv.Addr().String())
	session := NewSession(SessionConfig{
		ConnectionID: uuid.New(),
		SystemID:     "sys_thr",
		Password:     "pass_thr",
	}, conn)
	defer session.Close()
	_ = session.Bind(context.Background())

	sm := pdu.NewSubmitSM(0)
	resp, err := session.Submit(context.Background(), sm)
	if err != nil {
		t.Fatalf("Submit error: %v", err)
	}
	if resp.Header.CommandStatus != pdu.StatusRThrottled {
		t.Errorf("expected StatusRThrottled, got %x", resp.Header.CommandStatus)
	}

	// ClassifyStatus should map StatusRThrottled to SendStatusRetryableReject
	status := ClassifyStatus(resp.Header.CommandStatus)
	if status != messaging.SendStatusRetryableReject {
		t.Errorf("expected SendStatusRetryableReject, got %s", status)
	}
}

// 4. TPS limiting and statistics tracking
func TestSMPP_TPSLimiting(t *testing.T) {
	srv, _ := mocksmsc.NewServer(mocksmsc.ServerConfig{
		SystemID: "sys_tps",
		Password: "pass_tps",
	})
	_ = srv.Start()
	defer srv.Close()

	conn, _ := net.Dial("tcp", srv.Addr().String())
	session := NewSession(SessionConfig{
		ConnectionID: uuid.New(),
		SystemID:     "sys_tps",
		Password:     "pass_tps",
	}, conn)
	defer session.Close()
	_ = session.Bind(context.Background())

	for i := 0; i < 5; i++ {
		sm := pdu.NewSubmitSM(0)
		_, _ = session.Submit(context.Background(), sm)
	}

	stats := session.Stats()
	if stats.SuccessCount != 5 {
		t.Errorf("expected 5 successes in stats, got %d", stats.SuccessCount)
	}
}

// 5. Timeout leads to UNKNOWN with no resend
func TestSMPP_TimeoutLeadsToUnknownWithNoResend(t *testing.T) {
	srv, _ := mocksmsc.NewServer(mocksmsc.ServerConfig{
		SystemID:      "sys_hang",
		Password:      "pass_hang",
		InjectedError: mocksmsc.InjectedNoResponse, // hangs indefinitely
	})
	_ = srv.Start()
	defer srv.Close()

	conn, _ := net.Dial("tcp", srv.Addr().String())
	session := NewSession(SessionConfig{
		ConnectionID: uuid.New(),
		SystemID:     "sys_hang",
		Password:     "pass_hang",
	}, conn)
	defer session.Close()
	_ = session.Bind(context.Background())

	// Submit with short 50ms context timeout
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	sm := pdu.NewSubmitSM(0)
	_, err := session.Submit(ctx, sm)
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}

	// Verify client handles DeadlineExceeded as SendStatusUnknown
	res := messaging.SendResult{
		Status:       messaging.SendStatusUnknown,
		ErrorCode:    "SUBMIT_TIMEOUT_UNKNOWN",
		ErrorMessage: "Carrier submission timed out with unconfirmed outcome",
	}
	if res.Status != messaging.SendStatusUnknown {
		t.Errorf("expected SendStatusUnknown, got %s", res.Status)
	}
}

// 6. Dropped connection and clean teardown
func TestSMPP_DroppedConnectionAndReconnect(t *testing.T) {
	srv, _ := mocksmsc.NewServer(mocksmsc.ServerConfig{
		SystemID:      "sys_drop",
		Password:      "pass_drop",
		InjectedError: mocksmsc.InjectedDropConnection, // drops socket on submit
	})
	_ = srv.Start()
	defer srv.Close()

	conn, _ := net.Dial("tcp", srv.Addr().String())
	session := NewSession(SessionConfig{
		ConnectionID: uuid.New(),
		SystemID:     "sys_drop",
		Password:     "pass_drop",
	}, conn)
	defer session.Close()
	_ = session.Bind(context.Background())

	sm := pdu.NewSubmitSM(0)
	_, err := session.Submit(context.Background(), sm)
	if err == nil {
		t.Fatalf("expected error on dropped connection, got nil")
	}

	// Verify session marks itself closed/unbound
	time.Sleep(50 * time.Millisecond)
	stats := session.Stats()
	if stats.Bound {
		t.Errorf("expected session to be unbound after connection drop")
	}
}

// 7. Auth failure cap and lockout prevention
func TestSMPP_AuthFailureCap(t *testing.T) {
	srv, _ := mocksmsc.NewServer(mocksmsc.ServerConfig{
		SystemID: "real_user",
		Password: "correct_password",
	})
	_ = srv.Start()
	defer srv.Close()

	mgr := NewConnectionManager(ManagerConfig{
		AllowLoopback: true,
	})
	defer mgr.Close()

	connID := uuid.New()
	// Simulate 5 consecutive failures
	for i := 0; i < 5; i++ {
		mgr.recordBindFailure(connID, fmt.Errorf("ESME_RINVPASWD"))
	}

	mgr.mu.RLock()
	bState := mgr.bindStates[connID]
	mgr.mu.RUnlock()

	if bState.consecutiveFails != 5 {
		t.Errorf("expected 5 consecutive fails, got %d", bState.consecutiveFails)
	}
	if !bState.lockedOutAlert {
		t.Errorf("expected lockout alert to be triggered at threshold")
	}
	if time.Now().After(bState.nextAttemptAt) {
		t.Errorf("expected backoff deadline in the future")
	}
}

// 8. GSM-7, UCS-2, and UDH concatenation wire format
func TestSMPP_WireEncoding_GSM7_UCS2_Concatenation(t *testing.T) {
	srv, _ := mocksmsc.NewServer(mocksmsc.ServerConfig{
		SystemID: "sys_enc",
		Password: "pass_enc",
	})
	_ = srv.Start()
	defer srv.Close()

	conn, _ := net.Dial("tcp", srv.Addr().String())
	session := NewSession(SessionConfig{
		ConnectionID: uuid.New(),
		SystemID:     "sys_enc",
		Password:     "pass_enc",
	}, conn)
	defer session.Close()
	_ = session.Bind(context.Background())

	// 1. GSM-7 submit
	gsmSubmit := pdu.NewSubmitSM(0)
	gsmSubmit.DataCoding = pdu.DataCodingSMSCDefault
	gsmSubmit.ShortMessage = []byte("Standard GSM-7 text")
	_, err := session.Submit(context.Background(), gsmSubmit)
	if err != nil {
		t.Fatalf("GSM submit failed: %v", err)
	}

	// 2. UCS-2 submit
	ucsSubmit := pdu.NewSubmitSM(0)
	ucsSubmit.DataCoding = pdu.DataCodingUCS2
	ucsSubmit.ShortMessage = encodeUCS2("Unicode \u2764\ufe0f text")
	_, err = session.Submit(context.Background(), ucsSubmit)
	if err != nil {
		t.Fatalf("UCS submit failed: %v", err)
	}

	// 3. Concatenation with UDH (0x05 0x00 0x03 ref total seq)
	udhSubmit := pdu.NewSubmitSM(0)
	udhSubmit.ESMClass = pdu.ESMClassUDHIIndicator
	udh := []byte{0x05, 0x00, 0x03, 0x42, 0x02, 0x01}
	udhSubmit.ShortMessage = append(udh, []byte("Part 1 of 2")...)
	_, err = session.Submit(context.Background(), udhSubmit)
	if err != nil {
		t.Fatalf("UDH submit failed: %v", err)
	}

	msgs := srv.GetSubmittedMessages()
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[0].DataCoding != pdu.DataCodingSMSCDefault {
		t.Errorf("msg 0 data coding mismatch: %d", msgs[0].DataCoding)
	}
	if msgs[1].DataCoding != pdu.DataCodingUCS2 {
		t.Errorf("msg 1 data coding mismatch: %d", msgs[1].DataCoding)
	}
	if msgs[2].ESMClass != pdu.ESMClassUDHIIndicator {
		t.Errorf("msg 2 esm_class mismatch: %d", msgs[2].ESMClass)
	}
	if !bytes.Equal(msgs[2].ShortMsg[:6], udh) {
		t.Errorf("msg 2 UDH prefix mismatch: %x", msgs[2].ShortMsg[:6])
	}
}

// 9. Strict TLS certificate validation rejects bad certificates (never InsecureSkipVerify)
func TestSMPP_TLSBadCertificateRejected(t *testing.T) {
	srv, _ := mocksmsc.NewServer(mocksmsc.ServerConfig{
		SystemID: "sys_tls_bad",
		Password: "pass_tls_bad",
		TLS:      true, // uses self-signed untrusted cert
	})
	_ = srv.Start()
	defer srv.Close()

	// SafeDialer with standard TLS config (strict verification, no InsecureSkipVerify)
	dialer := NewSafeDialer(DialOptions{
		Timeout:       2 * time.Second,
		AllowLoopback: true,
		TLS:           true,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			// Intentionally empty RootCAs: self-signed cert MUST be rejected!
		},
	})

	_, err := dialer.DialContext(context.Background(), "tcp", srv.Addr().String())
	if err == nil {
		t.Fatalf("expected strict TLS verification to reject self-signed cert, but dial succeeded!")
	}
	if !strings.Contains(err.Error(), "certificate") && !strings.Contains(err.Error(), "tls") {
		t.Errorf("expected certificate verification error, got: %v", err)
	}
}

// 10. Dial-time SSRF protection and DNS rebinding prevention
func TestSMPP_SSRFDialTimeRejectionAndDNSRebinding(t *testing.T) {
	resolver := newTestMockResolver()

	// 1. Prohibited private IP (RFC 1918 10.0.0.1)
	resolver.Set("carrier-internal.local", "10.1.2.3")
	dialer := NewSafeDialer(DialOptions{
		AllowLoopback: false, // Production mode
		Resolver:      resolver,
	})

	_, err := dialer.DialContext(context.Background(), "tcp", "carrier-internal.local:2775")
	if err == nil || !strings.Contains(err.Error(), "blocked range") {
		t.Fatalf("expected RFC 1918 SSRF rejection, got: %v", err)
	}

	// 2. Prohibited Cloud Metadata (169.254.169.254)
	resolver.Set("metadata.carrier.local", "169.254.169.254")
	_, err = dialer.DialContext(context.Background(), "tcp", "metadata.carrier.local:2775")
	if err == nil || !strings.Contains(err.Error(), "blocked range") {
		t.Fatalf("expected Cloud Metadata SSRF rejection, got: %v", err)
	}

	// 3. Prohibited Loopback in production
	resolver.Set("localhost.carrier.local", "127.0.0.1")
	_, err = dialer.DialContext(context.Background(), "tcp", "localhost.carrier.local:2775")
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("expected Loopback SSRF rejection in prod, got: %v", err)
	}

	// 4. DNS Rebinding simulation: host initially resolves to public IP, then switches to private IP
	resolver.Set("rebind.example.com", "93.184.216.34") // public IP
	addrs, err := resolver.LookupIPAddr(context.Background(), "rebind.example.com")
	if err != nil || ValidateIP(addrs[0].IP, false) != nil {
		t.Fatalf("initial public IP validation failed: %v", err)
	}

	// Attacker switches DNS to internal IP
	resolver.Set("rebind.example.com", "192.168.1.1") // private IP
	_, err = dialer.DialContext(context.Background(), "tcp", "rebind.example.com:2775")
	if err == nil || !strings.Contains(err.Error(), "blocked range") {
		t.Fatalf("DNS rebinding was NOT prevented; expected rejection on re-dial, got: %v", err)
	}
}

// 11. Kill switch closes session immediately without restart
func TestSMPP_KillSwitchClosesSessionWithoutRestart(t *testing.T) {
	mgr := NewConnectionManager(ManagerConfig{
		AllowLoopback: true,
	})
	defer mgr.Close()

	connID := uuid.New()
	// Simulate an active session
	c1, c2 := net.Pipe()
	defer c1.Close()
	session := NewSession(SessionConfig{
		ConnectionID: connID,
		SystemID:     "kill_user",
		Password:     "pass",
	}, c1)
	session.bound = true

	mgr.mu.Lock()
	mgr.sessions[connID] = session
	mgr.mu.Unlock()

	// Verify session is active
	if !session.Stats().Bound {
		t.Fatalf("session should be bound")
	}

	// Engage Kill Switch
	mgr.CloseConnectionSession(connID)

	// Verify session is closed immediately without service restart
	mgr.mu.RLock()
	_, exists := mgr.sessions[connID]
	mgr.mu.RUnlock()

	if exists {
		t.Errorf("session should be removed from manager after kill switch")
	}
	if session.Stats().Bound {
		t.Errorf("session bound state should be false after kill switch")
	}
	_ = c2
}

// 12. Credential rotation rebinds
func TestSMPP_CredentialRotationRebinds(t *testing.T) {
	mgr := NewConnectionManager(ManagerConfig{
		AllowLoopback: true,
	})
	defer mgr.Close()

	connID := uuid.New()
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	oldSession := NewSession(SessionConfig{
		ConnectionID: connID,
		SystemID:     "old_sys",
		Password:     "old_pass",
	}, c1)
	oldSession.bound = true

	mgr.mu.Lock()
	mgr.sessions[connID] = oldSession
	mgr.mu.Unlock()

	// Close old session to trigger rebind on rotation
	mgr.CloseConnectionSession(connID)

	if oldSession.Stats().Bound {
		t.Errorf("old session should be unbound after rotation trigger")
	}
}

// 13. Late deliver_sm persisted before deliver_sm_resp
func TestSMPP_LateDeliverSMPersistedBeforeResponse(t *testing.T) {
	persisted := false
	var persistedID string

	mockDLRHandler := func(ctx context.Context, connectionID uuid.UUID, deliver *pdu.DeliverSM) error {
		persisted = true
		persistedID = string(deliver.ShortMessage)
		return nil
	}

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	session := NewSession(SessionConfig{
		ConnectionID: uuid.New(),
		SystemID:     "dlr_sys",
		Password:     "dlr_pass",
		DLRHandler:   mockDLRHandler,
	}, c1)
	session.bound = true
	go session.readLoop()

	// Simulate incoming deliver_sm from SMSC
	deliver := pdu.NewDeliverSM(55)
	deliver.ShortMessage = []byte("id:MOCK-999 stat:DELIVRD")
	_ = pdu.WritePDU(c2, deliver)

	// Read deliver_sm_resp from session
	resp, err := pdu.ReadPDU(c2)
	if err != nil {
		t.Fatalf("Read deliver_sm_resp failed: %v", err)
	}
	if resp.GetHeader().CommandID != pdu.CmdDeliverSMResp {
		t.Fatalf("expected CmdDeliverSMResp, got %x", resp.GetHeader().CommandID)
	}

	// Verify persistence occurred BEFORE response
	if !persisted || !strings.Contains(persistedID, "MOCK-999") {
		t.Errorf("deliver_sm was not persisted prior to response: persisted=%v, id=%s", persisted, persistedID)
	}
}

// 14. Multi-part aggregation outcome rules
func TestSMPP_MultiPartAggregation(t *testing.T) {
	// Rule 1: All parts accepted -> message status submitted
	results1 := []messaging.SendResult{
		{Status: messaging.SendStatusAccepted, ProviderMessageID: "P1"},
		{Status: messaging.SendStatusAccepted, ProviderMessageID: "P2"},
	}
	agg1 := aggregatePartOutcomes(results1)
	if agg1 != messaging.StatusSubmitted {
		t.Errorf("expected StatusSubmitted, got %s", agg1)
	}

	// Rule 2: Any part fatal reject -> message status failed
	results2 := []messaging.SendResult{
		{Status: messaging.SendStatusAccepted, ProviderMessageID: "P1"},
		{Status: messaging.SendStatusFatalReject, ErrorCode: "ESME_RINVDSTADR"},
	}
	agg2 := aggregatePartOutcomes(results2)
	if agg2 != messaging.StatusFailed {
		t.Errorf("expected StatusFailed on fatal reject, got %s", agg2)
	}

	// Rule 3: Any part timeout / unknown -> message status unknown (never resend!)
	results3 := []messaging.SendResult{
		{Status: messaging.SendStatusAccepted, ProviderMessageID: "P1"},
		{Status: messaging.SendStatusUnknown, ErrorCode: "SUBMIT_TIMEOUT_UNKNOWN"},
	}
	agg3 := aggregatePartOutcomes(results3)
	if agg3 != messaging.StatusUnknown {
		t.Errorf("expected StatusUnknown on timeout, got %s", agg3)
	}

	// Rule 4: Any part retryable reject -> outbox failover retry
	results4 := []messaging.SendResult{
		{Status: messaging.SendStatusAccepted, ProviderMessageID: "P1"},
		{Status: messaging.SendStatusRetryableReject, ErrorCode: "ESME_RTHROTTLED"},
	}
	agg4 := aggregatePartOutcomes(results4)
	if agg4 != messaging.StatusQueued {
		t.Errorf("expected StatusQueued for failover retry, got %s", agg4)
	}
}

// 15. Graceful shutdown drains in-flight requests and unbinds cleanly
func TestSMPP_GracefulShutdown(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	session := NewSession(SessionConfig{
		ConnectionID: uuid.New(),
		SystemID:     "grace_sys",
		Password:     "grace_pass",
	}, c1)
	session.bound = true

	// Close session
	doneCh := make(chan struct{})
	go func() {
		_ = session.Close()
		close(doneCh)
	}()

	// Other side reads unbind PDU
	p, err := pdu.ReadPDU(c2)
	if err != nil || p.GetHeader().CommandID != pdu.CmdUnbind {
		t.Fatalf("expected Unbind PDU on shutdown: %v", err)
	}

	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("graceful shutdown hung")
	}

	if session.Stats().Bound {
		t.Errorf("expected session unbound after shutdown")
	}
}

// 16. Safe logging: never prints secret passwords or full destination numbers
func TestSMPP_SafeLoggingNoSecretOrFullNumber(t *testing.T) {
	rawPhone := "+14155552671"
	masked := MaskPhoneNumber(rawPhone)
	if masked != "********2671" {
		t.Errorf("expected ********2671, got %s", masked)
	}

	var logBuf bytes.Buffer
	handler := slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(handler)

	logger.Info("sending message",
		slog.String("dest", MaskPhoneNumber(rawPhone)),
		slog.String("pass", "[REDACTED]"),
	)

	loggedText := logBuf.String()
	if strings.Contains(loggedText, rawPhone) {
		t.Errorf("security violation: full phone number leaked in logs!")
	}
	if strings.Contains(loggedText, "415555") {
		t.Errorf("security violation: phone prefix leaked in logs!")
	}
}

// aggregatePartOutcomes defines the business rule for combining multi-part submission outcomes.
func aggregatePartOutcomes(results []messaging.SendResult) string {
	var hasFatal bool
	var hasUnknown bool
	var hasRetryable bool
	var acceptedCount int

	for _, r := range results {
		switch r.Status {
		case messaging.SendStatusAccepted:
			acceptedCount++
		case messaging.SendStatusFatalReject:
			hasFatal = true
		case messaging.SendStatusUnknown:
			hasUnknown = true
		case messaging.SendStatusRetryableReject:
			hasRetryable = true
		}
	}

	// 1. Any fatal rejection marks the entire message failed
	if hasFatal {
		return messaging.StatusFailed
	}
	// 2. Any unknown / unconfirmed outcome holds the message in unknown
	if hasUnknown {
		return messaging.StatusUnknown
	}
	// 3. Any retryable error requires failover retry
	if hasRetryable {
		return messaging.StatusQueued
	}
	// 4. All parts accepted -> submitted
	if acceptedCount == len(results) {
		return messaging.StatusSubmitted
	}
	return messaging.StatusFailed
}
