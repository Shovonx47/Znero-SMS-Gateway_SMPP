package mocksmsc

import (
	"crypto/tls"
	"net"
	"testing"

	"github.com/znero-international/znero/backend/internal/smpp/pdu"
)

func TestMockSMSC_BindAndSubmitSuccess(t *testing.T) {
	srv, err := NewServer(ServerConfig{
		SystemID: "test_sys",
		Password: "test_password",
		DLRMode:  DLRModeImmediate,
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
	defer conn.Close()

	// 1. Send Bind
	bind := pdu.NewBind(pdu.CmdBindTransceiver, 1, "test_sys", "test_password", "")
	if err := pdu.WritePDU(conn, bind); err != nil {
		t.Fatalf("Write bind failed: %v", err)
	}
	resp, err := pdu.ReadPDU(conn)
	if err != nil {
		t.Fatalf("Read bind resp failed: %v", err)
	}
	if resp.GetHeader().CommandStatus != pdu.StatusROK {
		t.Fatalf("expected bind OK, got %x", resp.GetHeader().CommandStatus)
	}

	// 2. Submit SM
	submit := pdu.NewSubmitSM(2)
	submit.SourceAddr = "SENDER"
	submit.DestAddr = "15551234567"
	submit.ShortMessage = []byte("Hello Mock SMSC")
	submit.RegisteredDelivery = pdu.RegDeliverySMSCDeliveryReceipt
	if err := pdu.WritePDU(conn, submit); err != nil {
		t.Fatalf("Write submit failed: %v", err)
	}

	// 3. Read Submit SM Resp
	subResp, err := pdu.ReadPDU(conn)
	if err != nil {
		t.Fatalf("Read submit resp failed: %v", err)
	}
	if subResp.GetHeader().CommandStatus != pdu.StatusROK {
		t.Fatalf("expected submit OK, got %x", subResp.GetHeader().CommandStatus)
	}
	sr := subResp.(*pdu.SubmitSMResp)
	if sr.MessageID == "" {
		t.Fatalf("expected non-empty message ID")
	}

	// 4. Read DLR (since DLRMode is immediate and registered delivery requested)
	dlrPDU, err := pdu.ReadPDU(conn)
	if err != nil {
		t.Fatalf("Read DLR failed: %v", err)
	}
	dlr, ok := dlrPDU.(*pdu.DeliverSM)
	if !ok {
		t.Fatalf("expected DeliverSM, got %T", dlrPDU)
	}
	if dlr.DestAddr != "SENDER" {
		t.Errorf("expected DLR dest SENDER, got %s", dlr.DestAddr)
	}

	// Ack DLR
	_ = pdu.WritePDU(conn, pdu.NewDeliverSMResp(pdu.StatusROK, dlr.Header.SequenceNumber))

	// 5. Verify submitted message was stored in server memory
	msgs := srv.GetSubmittedMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 submitted message in server, got %d", len(msgs))
	}
	if msgs[0].MessageID != sr.MessageID || string(msgs[0].ShortMsg) != "Hello Mock SMSC" {
		t.Errorf("message content mismatch: %+v", msgs[0])
	}
}

func TestMockSMSC_InvalidPassword(t *testing.T) {
	srv, err := NewServer(ServerConfig{
		SystemID: "alice",
		Password: "correct_password",
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
	defer conn.Close()

	bind := pdu.NewBind(pdu.CmdBindTransceiver, 1, "alice", "wrong_password", "")
	if err := pdu.WritePDU(conn, bind); err != nil {
		t.Fatalf("Write bind failed: %v", err)
	}
	resp, err := pdu.ReadPDU(conn)
	if err != nil {
		t.Fatalf("Read bind resp failed: %v", err)
	}
	if resp.GetHeader().CommandStatus != pdu.StatusRInvPaswd {
		t.Fatalf("expected ESME_RINVPASWD (0x0e), got 0x%x", resp.GetHeader().CommandStatus)
	}
}

func TestMockSMSC_SingleSessionEnforcement(t *testing.T) {
	srv, err := NewServer(ServerConfig{
		SystemID:             "bob",
		Password:             "pass123",
		EnforceSingleSession: true,
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Close()

	// First session binds successfully
	c1, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatalf("Dial 1 failed: %v", err)
	}
	defer c1.Close()

	bind1 := pdu.NewBind(pdu.CmdBindTransmitter, 1, "bob", "pass123", "")
	if err := pdu.WritePDU(c1, bind1); err != nil {
		t.Fatalf("Write bind 1 failed: %v", err)
	}
	resp1, err := pdu.ReadPDU(c1)
	if err != nil || resp1.GetHeader().CommandStatus != pdu.StatusROK {
		t.Fatalf("session 1 bind failed: %v", err)
	}

	// Second session attempts to bind while first is active
	c2, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatalf("Dial 2 failed: %v", err)
	}
	defer c2.Close()

	bind2 := pdu.NewBind(pdu.CmdBindTransmitter, 2, "bob", "pass123", "")
	if err := pdu.WritePDU(c2, bind2); err != nil {
		t.Fatalf("Write bind 2 failed: %v", err)
	}
	resp2, err := pdu.ReadPDU(c2)
	if err != nil {
		t.Fatalf("Read bind 2 resp failed: %v", err)
	}
	if resp2.GetHeader().CommandStatus != pdu.StatusRAlyBnd {
		t.Fatalf("expected ESME_RALYBND (0x05), got 0x%x", resp2.GetHeader().CommandStatus)
	}
}

func TestMockSMSC_InjectedErrors(t *testing.T) {
	srv, err := NewServer(ServerConfig{
		SystemID: "carrier",
		Password: "pass",
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
	defer conn.Close()

	_ = pdu.WritePDU(conn, pdu.NewBind(pdu.CmdBindTransceiver, 1, "carrier", "pass", ""))
	_, _ = pdu.ReadPDU(conn)

	// Test 1: Throttled
	srv.SetInjectedError(pdu.StatusRThrottled)
	_ = pdu.WritePDU(conn, pdu.NewSubmitSM(10))
	resp, _ := pdu.ReadPDU(conn)
	if resp.GetHeader().CommandStatus != pdu.StatusRThrottled {
		t.Errorf("expected StatusRThrottled, got %x", resp.GetHeader().CommandStatus)
	}

	// Test 2: Queue Full
	srv.SetInjectedError(pdu.StatusRMsgQFul)
	_ = pdu.WritePDU(conn, pdu.NewSubmitSM(11))
	resp, _ = pdu.ReadPDU(conn)
	if resp.GetHeader().CommandStatus != pdu.StatusRMsgQFul {
		t.Errorf("expected StatusRMsgQFul, got %x", resp.GetHeader().CommandStatus)
	}

	// Test 3: Invalid Destination
	srv.SetInjectedError(pdu.StatusRInvDstAdr)
	_ = pdu.WritePDU(conn, pdu.NewSubmitSM(12))
	resp, _ = pdu.ReadPDU(conn)
	if resp.GetHeader().CommandStatus != pdu.StatusRInvDstAdr {
		t.Errorf("expected StatusRInvDstAdr, got %x", resp.GetHeader().CommandStatus)
	}
}

func TestMockSMSC_TLS(t *testing.T) {
	srv, err := NewServer(ServerConfig{
		SystemID: "tls_user",
		Password: "tls_pass",
		TLS:      true,
	})
	if err != nil {
		t.Fatalf("NewServer TLS failed: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start TLS failed: %v", err)
	}
	defer srv.Close()

	tlsConfig := &tls.Config{
		InsecureSkipVerify: true, // test-only client dial to self-signed mock
	}
	conn, err := tls.Dial("tcp", srv.Addr().String(), tlsConfig)
	if err != nil {
		t.Fatalf("TLS Dial failed: %v", err)
	}
	defer conn.Close()

	_ = pdu.WritePDU(conn, pdu.NewBind(pdu.CmdBindTransceiver, 1, "tls_user", "tls_pass", ""))
	resp, err := pdu.ReadPDU(conn)
	if err != nil || resp.GetHeader().CommandStatus != pdu.StatusROK {
		t.Fatalf("TLS bind failed: %v", err)
	}
}

func TestMockSMSC_DLRBeforeSubmitResp(t *testing.T) {
	srv, err := NewServer(ServerConfig{
		SystemID: "dlr_user",
		Password: "secret",
		DLRMode:  DLRModeBeforeSubmitResp,
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Close()

	conn, _ := net.Dial("tcp", srv.Addr().String())
	defer conn.Close()

	_ = pdu.WritePDU(conn, pdu.NewBind(pdu.CmdBindTransceiver, 1, "dlr_user", "secret", ""))
	_, _ = pdu.ReadPDU(conn)

	_ = pdu.WritePDU(conn, pdu.NewSubmitSM(2))

	// First received PDU should be DeliverSM (DLR), NOT SubmitSMResp!
	p1, err := pdu.ReadPDU(conn)
	if err != nil {
		t.Fatalf("Read p1 failed: %v", err)
	}
	if p1.GetHeader().CommandID != pdu.CmdDeliverSM {
		t.Fatalf("expected first PDU to be CmdDeliverSM, got %x", p1.GetHeader().CommandID)
	}

	// Second received PDU should be SubmitSMResp
	p2, err := pdu.ReadPDU(conn)
	if err != nil {
		t.Fatalf("Read p2 failed: %v", err)
	}
	if p2.GetHeader().CommandID != pdu.CmdSubmitSMResp {
		t.Fatalf("expected second PDU to be CmdSubmitSMResp, got %x", p2.GetHeader().CommandID)
	}
}
