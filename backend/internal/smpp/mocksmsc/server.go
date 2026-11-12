package mocksmsc

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/znero-international/znero/backend/internal/smpp/pdu"
)

// DLRMode controls delivery receipt generation.
type DLRMode string

const (
	DLRModeNone             DLRMode = "none"
	DLRModeImmediate        DLRMode = "immediate"
	DLRModeDelayed          DLRMode = "delayed"
	DLRModeLost             DLRMode = "lost"
	DLRModeDuplicate        DLRMode = "duplicate"
	DLRModeBeforeSubmitResp DLRMode = "before_submit_resp"
	DLRModeFailed           DLRMode = "failed"
	DLRModeUndeliverable    DLRMode = "undeliverable"
	DLRModeExpired          DLRMode = "expired"
)

// Injected error special codes
const (
	InjectedDropConnection uint32 = 0xFFFFFFFE
	InjectedNoResponse     uint32 = 0xFFFFFFFF
)

// ServerConfig holds parameters for the mock SMSC simulator.
type ServerConfig struct {
	Addr                 string
	SystemID             string
	Password             string
	EnforceSingleSession bool
	ReplayUnackedDLRs    bool
	TLS                  bool
	InjectedError        uint32
	Latency              time.Duration
	DLRMode              DLRMode
	DLRDelay             time.Duration
}

type pendingDLR struct {
	seq   uint32
	msgID string
	src   string
	dst   string
	mode  DLRMode
}

// SubmittedMessage stores details of an ingested submit_sm.
type SubmittedMessage struct {
	MessageID   string
	SourceAddr  string
	DestAddr    string
	ShortMsg    []byte
	DataCoding  uint8
	ESMClass    uint8
	TLVs        []pdu.TLV
	SubmittedAt time.Time
}

// Server is an in-process or standalone SMPP 3.4 mock SMSC simulator.
type Server struct {
	cfg       ServerConfig
	listener  net.Listener
	tlsConfig *tls.Config

	mu            sync.RWMutex
	activeBinds   int
	submitted     []SubmittedMessage
	unackedDLRs   map[uint32]pendingDLR
	running       bool
	closeCh       chan struct{}
	msgCounter    uint64
	smscSeq       uint32
	onMessageHook func(msg SubmittedMessage)
}

// NewServer creates a new mock SMSC server.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.SystemID == "" {
		cfg.SystemID = "MOCK_SMSC_" + randomHex(4)
	}
	if cfg.Password == "" {
		cfg.Password = "MOCK_PASS_" + randomHex(4)
	}
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:0"
	}
	if cfg.DLRMode == "" {
		cfg.DLRMode = DLRModeImmediate
	}

	var tlsCfg *tls.Config
	if cfg.TLS {
		cert, err := generateSelfSignedCert()
		if err != nil {
			return nil, fmt.Errorf("generate self-signed cert: %w", err)
		}
		tlsCfg = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
	}

	return &Server{
		cfg:         cfg,
		tlsConfig:   tlsCfg,
		unackedDLRs: make(map[uint32]pendingDLR),
		closeCh:     make(chan struct{}),
		running:     true,
	}, nil
}

// Start begins listening for connections.
func (s *Server) Start() error {
	var l net.Listener
	var err error

	if s.tlsConfig != nil {
		l, err = tls.Listen("tcp", s.cfg.Addr, s.tlsConfig)
	} else {
		l, err = net.Listen("tcp", s.cfg.Addr)
	}
	if err != nil {
		return err
	}
	s.listener = l

	go s.acceptLoop()
	return nil
}

// Addr returns the listening network address.
func (s *Server) Addr() net.Addr {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.listener != nil {
		return s.listener.Addr()
	}
	return nil
}

// Close stops the server.
func (s *Server) Close() error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = false
	close(s.closeCh)
	var err error
	if s.listener != nil {
		err = s.listener.Close()
	}
	s.mu.Unlock()
	return err
}

// TLSConfig returns the server's TLS configuration (for test clients to trust).
func (s *Server) TLSConfig() *tls.Config {
	return s.tlsConfig
}

// SetInjectedError alters the error behavior dynamically during a test.
func (s *Server) SetInjectedError(errCode uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.InjectedError = errCode
}

// SetDLRMode alters DLR generation dynamically.
func (s *Server) SetDLRMode(mode DLRMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.DLRMode = mode
}

// SetLatency alters response latency dynamically.
func (s *Server) SetLatency(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Latency = d
}

// SetOnMessageHook sets a callback invoked on each submit_sm.
func (s *Server) SetOnMessageHook(h func(msg SubmittedMessage)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onMessageHook = h
}

// GetSubmittedMessages returns a copy of all received messages.
func (s *Server) GetSubmittedMessages() []SubmittedMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SubmittedMessage, len(s.submitted))
	copy(out, s.submitted)
	return out
}

// Credentials returns the configured system_id and password.
func (s *Server) Credentials() (string, string) {
	return s.cfg.SystemID, s.cfg.Password
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.closeCh:
				return
			default:
				return
			}
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()

	var isBound bool
	var bindType uint32
	defer func() {
		if isBound {
			s.mu.Lock()
			s.activeBinds--
			s.mu.Unlock()
		}
	}()

	for {
		p, err := pdu.ReadPDU(conn)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return
			}
			return
		}

		s.mu.RLock()
		injectedErr := s.cfg.InjectedError
		latency := s.cfg.Latency
		dlrMode := s.cfg.DLRMode
		dlrDelay := s.cfg.DLRDelay
		enforceSingle := s.cfg.EnforceSingleSession
		s.mu.RUnlock()

		if latency > 0 {
			time.Sleep(latency)
		}

		hdr := p.GetHeader()

		switch hdr.CommandID {
		case pdu.CmdBindTransmitter, pdu.CmdBindReceiver, pdu.CmdBindTransceiver:
			bindReq, ok := p.(*pdu.Bind)
			if !ok {
				_ = pdu.WritePDU(conn, pdu.NewGenericNack(pdu.StatusRInvCmdID, hdr.SequenceNumber))
				continue
			}

			// Injected bind failure check
			if injectedErr == pdu.StatusRBindFail {
				respCmd := getBindRespCmd(hdr.CommandID)
				_ = pdu.WritePDU(conn, pdu.NewBindResp(respCmd, pdu.StatusRBindFail, hdr.SequenceNumber, ""))
				return
			}

			// Validate credentials
			if bindReq.SystemID != s.cfg.SystemID || bindReq.Password != s.cfg.Password {
				respCmd := getBindRespCmd(hdr.CommandID)
				_ = pdu.WritePDU(conn, pdu.NewBindResp(respCmd, pdu.StatusRInvPaswd, hdr.SequenceNumber, ""))
				return
			}

			// Enforce single session check
			s.mu.Lock()
			if enforceSingle && s.activeBinds > 0 {
				s.mu.Unlock()
				respCmd := getBindRespCmd(hdr.CommandID)
				_ = pdu.WritePDU(conn, pdu.NewBindResp(respCmd, pdu.StatusRAlyBnd, hdr.SequenceNumber, ""))
				return
			}
			s.activeBinds++
			isBound = true
			bindType = hdr.CommandID
			s.mu.Unlock()

			respCmd := getBindRespCmd(hdr.CommandID)
			_ = pdu.WritePDU(conn, pdu.NewBindResp(respCmd, pdu.StatusROK, hdr.SequenceNumber, "MOCK_SMSC"))

			if s.cfg.ReplayUnackedDLRs && (hdr.CommandID == pdu.CmdBindTransceiver || hdr.CommandID == pdu.CmdBindReceiver) {
				s.replayPendingDLRs(conn)
			}

		case pdu.CmdUnbind:
			_ = pdu.WritePDU(conn, pdu.NewUnbindResp(pdu.StatusROK, hdr.SequenceNumber))
			return

		case pdu.CmdEnquireLink:
			_ = pdu.WritePDU(conn, pdu.NewEnquireLinkResp(pdu.StatusROK, hdr.SequenceNumber))

		case pdu.CmdSubmitSM:
			submitReq, ok := p.(*pdu.SubmitSM)
			if !ok {
				_ = pdu.WritePDU(conn, pdu.NewGenericNack(pdu.StatusRInvCmdID, hdr.SequenceNumber))
				continue
			}

			// Check injected error modes
			if injectedErr == InjectedDropConnection {
				conn.Close()
				return
			}
			if injectedErr == InjectedNoResponse {
				// Hang indefinitely or until client disconnects
				select {
				case <-s.closeCh:
					return
				case <-time.After(30 * time.Second):
					return
				}
			}
			if injectedErr != pdu.StatusROK && injectedErr != 0 {
				_ = pdu.WritePDU(conn, pdu.NewSubmitSMResp(injectedErr, hdr.SequenceNumber, ""))
				continue
			}

			// Success case: assign message ID
			msgNum := atomic.AddUint64(&s.msgCounter, 1)
			providerMsgID := fmt.Sprintf("MOCK-%08d", msgNum)

			submitted := SubmittedMessage{
				MessageID:   providerMsgID,
				SourceAddr:  submitReq.SourceAddr,
				DestAddr:    submitReq.DestAddr,
				ShortMsg:    submitReq.ShortMessage,
				DataCoding:  submitReq.DataCoding,
				ESMClass:    submitReq.ESMClass,
				TLVs:        submitReq.TLVs,
				SubmittedAt: time.Now().UTC(),
			}

			s.mu.Lock()
			s.submitted = append(s.submitted, submitted)
			hook := s.onMessageHook
			s.mu.Unlock()

			if hook != nil {
				hook(submitted)
			}

			// DLR Handling
			if dlrMode == DLRModeBeforeSubmitResp {
				// Send DLR BEFORE submit response
				s.sendDLR(conn, providerMsgID, submitReq.SourceAddr, submitReq.DestAddr, dlrMode)
			}

			// Send submit_sm_resp
			_ = pdu.WritePDU(conn, pdu.NewSubmitSMResp(pdu.StatusROK, hdr.SequenceNumber, providerMsgID))

			// Post-response DLRs
			if (bindType == pdu.CmdBindTransceiver || bindType == pdu.CmdBindReceiver) &&
				(submitReq.RegisteredDelivery&pdu.RegDeliverySMSCDeliveryReceipt != 0) {

				switch dlrMode {
				case DLRModeImmediate, DLRModeFailed, DLRModeUndeliverable, DLRModeExpired:
					s.sendDLR(conn, providerMsgID, submitReq.SourceAddr, submitReq.DestAddr, dlrMode)
				case DLRModeDuplicate:
					s.sendDLR(conn, providerMsgID, submitReq.SourceAddr, submitReq.DestAddr, DLRModeImmediate)
					s.sendDLR(conn, providerMsgID, submitReq.SourceAddr, submitReq.DestAddr, DLRModeImmediate)
				case DLRModeDelayed:
					go func(id, src, dst string, delay time.Duration) {
						time.Sleep(delay)
						s.sendDLR(conn, id, src, dst, DLRModeImmediate)
					}(providerMsgID, submitReq.SourceAddr, submitReq.DestAddr, dlrDelay)
				case DLRModeLost, DLRModeNone, DLRModeBeforeSubmitResp:
					// No post-submit DLR
				}
			}

		case pdu.CmdDeliverSMResp:
			if resp, ok := p.(*pdu.DeliverSMResp); ok && resp.Header.CommandStatus == pdu.StatusROK {
				s.mu.Lock()
				delete(s.unackedDLRs, hdr.SequenceNumber)
				s.mu.Unlock()
			}

		default:
			_ = pdu.WritePDU(conn, pdu.NewGenericNack(pdu.StatusRInvCmdID, hdr.SequenceNumber))
		}
	}
}

func buildDeliverSMPDU(seq uint32, msgID, src, dst string, mode DLRMode) *pdu.DeliverSM {
	dlr := pdu.NewDeliverSM(seq)
	dlr.SourceAddr = dst // DLR comes from destination back to source
	dlr.DestAddr = src
	dlr.ESMClass = 0x04 // Delivery receipt indicator

	stat := "DELIVRD"
	errCode := "000"
	var msgState byte = 2 // DELIVRD

	if mode == DLRModeFailed {
		stat = "REJECTD"
		errCode = "008"
		msgState = 8 // REJECTED
	} else if mode == DLRModeUndeliverable {
		stat = "UNDELIV"
		errCode = "005"
		msgState = 5 // UNDELIVERABLE
	} else if mode == DLRModeExpired {
		stat = "EXPIRED"
		errCode = "003"
		msgState = 3 // EXPIRED
	}

	now := time.Now().UTC()
	subDate := now.Add(-time.Second).Format("0601021504")
	doneDate := now.Format("0601021504")

	receiptText := fmt.Sprintf("id:%s sub:001 dlvrd:001 submit date:%s done date:%s stat:%s err:%s text:",
		msgID, subDate, doneDate, stat, errCode)
	dlr.ShortMessage = []byte(receiptText)

	// Attach SMPP 3.4 receipted_message_id and message_state TLVs
	dlr.TLVs = []pdu.TLV{
		{Tag: pdu.TagReceiptedMessageId, Length: uint16(len(msgID)), Value: []byte(msgID)},
		{Tag: pdu.TagMessageState, Length: 1, Value: []byte{msgState}},
	}
	return dlr
}

func (s *Server) sendDLR(w io.Writer, msgID, src, dst string, mode DLRMode) {
	seq := atomic.AddUint32(&s.smscSeq, 1)
	if s.cfg.ReplayUnackedDLRs {
		s.mu.Lock()
		s.unackedDLRs[seq] = pendingDLR{seq: seq, msgID: msgID, src: src, dst: dst, mode: mode}
		s.mu.Unlock()
	}
	_ = pdu.WritePDU(w, buildDeliverSMPDU(seq, msgID, src, dst, mode))
}

func (s *Server) replayPendingDLRs(w io.Writer) {
	s.mu.RLock()
	if len(s.unackedDLRs) == 0 {
		s.mu.RUnlock()
		return
	}
	items := make([]pendingDLR, 0, len(s.unackedDLRs))
	for _, it := range s.unackedDLRs {
		items = append(items, it)
	}
	s.mu.RUnlock()

	for _, it := range items {
		_ = pdu.WritePDU(w, buildDeliverSMPDU(it.seq, it.msgID, it.src, it.dst, it.mode))
	}
}

func getBindRespCmd(cmdID uint32) uint32 {
	switch cmdID {
	case pdu.CmdBindTransmitter:
		return pdu.CmdBindTransmitterResp
	case pdu.CmdBindReceiver:
		return pdu.CmdBindReceiverResp
	case pdu.CmdBindTransceiver:
		return pdu.CmdBindTransceiverResp
	default:
		return pdu.CmdGenericNack
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// generateSelfSignedCert creates an in-memory TLS certificate for testing.
func generateSelfSignedCert() (tls.Certificate, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			Organization: []string{"Znero Mock SMSC CA"},
			CommonName:   "127.0.0.1",
		},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:              []string{"localhost"},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})

	return tls.X509KeyPair(certPEM, keyPEM)
}
