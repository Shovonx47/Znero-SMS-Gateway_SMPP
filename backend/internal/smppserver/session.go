package smppserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/internal/customeredge"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/smpp/pdu"
	"github.com/znero-international/znero/backend/pkg/crypto/password"
)

// Session Bind State Constants
const (
	stateUnbound = iota
	stateTransmitter
	stateReceiver
	stateTransceiver
	stateClosed
)

// ClientSession manages a single SMPP TCP/TLS client connection.
type ClientSession struct {
	ID              uuid.UUID
	AccountID       uuid.UUID
	CustomerOrgID   uuid.UUID
	CustomerOrgPath string
	SystemID        string
	BindType        string
	RemoteIP        string

	conn    net.Conn
	pool    *pgxpool.Pool
	msgSvc  *messaging.Service
	repo    messaging.Repository
	limiter *customeredge.CustomerLimiter
	bindLim *BindLimiter
	tracker *SessionTracker
	cfg     ServerConfig
	logger  *slog.Logger

	mu            sync.Mutex
	writeMu       sync.Mutex
	state         int
	inFlight      int32
	messagesSent  uint64
	dlrsDelivered uint64
	lastActivity  time.Time
	maxTPS        int
	windowSize    int
	seqCounter    uint32
	closeOnce     sync.Once
	waiters       map[uint32]chan *pdu.DeliverSMResp
	waitersMu     sync.Mutex
}

// NewClientSession constructs a new ClientSession wrapper around a net.Conn.
func NewClientSession(
	conn net.Conn,
	pool *pgxpool.Pool,
	msgSvc *messaging.Service,
	repo messaging.Repository,
	limiter *customeredge.CustomerLimiter,
	bindLim *BindLimiter,
	tracker *SessionTracker,
	cfg ServerConfig,
	logger *slog.Logger,
) *ClientSession {
	remoteAddr := conn.RemoteAddr().String()
	remoteIP := remoteAddr
	if idx := strings.LastIndex(remoteAddr, ":"); idx != -1 {
		remoteIP = remoteAddr[:idx]
	}
	remoteIP = strings.Trim(remoteIP, "[]")

	return &ClientSession{
		ID:           uuid.New(),
		RemoteIP:     remoteIP,
		conn:         conn,
		pool:         pool,
		msgSvc:       msgSvc,
		repo:         repo,
		limiter:      limiter,
		bindLim:      bindLim,
		tracker:      tracker,
		cfg:          cfg,
		logger:       logger.With("session_id", uuid.New().String(), "remote_ip", remoteIP),
		state:        stateUnbound,
		windowSize:   cfg.DefaultWindow,
		lastActivity: time.Now().UTC(),
		waiters:      make(map[uint32]chan *pdu.DeliverSMResp),
	}
}

func (s *ClientSession) IsBound() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == stateTransmitter || s.state == stateReceiver || s.state == stateTransceiver
}

func (s *ClientSession) CanReceiveDLR() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == stateReceiver || s.state == stateTransceiver
}

func (s *ClientSession) InFlight() int         { return int(atomic.LoadInt32(&s.inFlight)) }
func (s *ClientSession) MessagesSent() uint64  { return atomic.LoadUint64(&s.messagesSent) }
func (s *ClientSession) DLRsDelivered() uint64 { return atomic.LoadUint64(&s.dlrsDelivered) }

func (s *ClientSession) LastActivity() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastActivity
}

func (s *ClientSession) updateActivity() {
	s.mu.Lock()
	s.lastActivity = time.Now().UTC()
	s.mu.Unlock()
}

func (s *ClientSession) nextSeq() uint32 {
	return atomic.AddUint32(&s.seqCounter, 1)
}

// Run executes the session reading loop until disconnected or context cancelled.
func (s *ClientSession) Run(ctx context.Context) {
	defer s.Close("connection_closed")

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Set read timeout for idle detection
		_ = s.conn.SetReadDeadline(time.Now().Add(s.cfg.IdleTimeout))

		p, err := pdu.ReadPDU(s.conn)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				// Send enquire_link to verify connectivity
				if s.IsBound() {
					if pingErr := s.sendEnquireLink(); pingErr != nil {
						s.logger.Warn("client ping failed; closing idle session", "error", pingErr)
						return
					}
					continue
				}
				return
			}
			s.logger.Warn("smpp read error", "error", err)
			return
		}

		s.updateActivity()

		if handleErr := s.handlePDU(ctx, p); handleErr != nil {
			s.logger.Warn("smpp pdu handling error", "error", handleErr)
			return
		}
	}
}

func (s *ClientSession) handlePDU(ctx context.Context, p pdu.PDU) error {
	switch req := p.(type) {
	case *pdu.Bind:
		return s.handleBind(ctx, req)
	case *pdu.Unbind:
		return s.handleUnbind(req)
	case *pdu.EnquireLink:
		return s.handleEnquireLink(req)
	case *pdu.EnquireLinkResp:
		return nil // Client acknowledged ping
	case *pdu.SubmitSM:
		return s.handleSubmitSM(ctx, req)
	case *pdu.DeliverSMResp:
		return s.handleDeliverSMResp(req)
	case *pdu.GenericNackPDU:
		return nil
	default:
		// Send GenericNack for unhandled or unexpected PDU
		hdr := p.GetHeader()
		nack := pdu.NewGenericNack(pdu.StatusRInvCmdID, hdr.SequenceNumber)
		return s.writePDU(nack)
	}
}

// handleBind processes bind_transmitter, bind_receiver, or bind_transceiver.
func (s *ClientSession) handleBind(ctx context.Context, bind *pdu.Bind) error {
	s.mu.Lock()
	if s.state != stateUnbound {
		s.mu.Unlock()
		resp := pdu.NewBindResp(bind.Header.CommandID|0x80000000, pdu.StatusRAlyBnd, bind.Header.SequenceNumber, s.cfg.SystemID)
		_ = s.writePDU(resp)
		return nil
	}
	s.mu.Unlock()

	bindCmd := bind.Header.CommandID
	bindType := "transceiver"
	targetState := stateTransceiver
	switch bindCmd {
	case pdu.CmdBindTransmitter:
		bindType = "transmitter"
		targetState = stateTransmitter
	case pdu.CmdBindReceiver:
		bindType = "receiver"
		targetState = stateReceiver
	case pdu.CmdBindTransceiver:
		bindType = "transceiver"
		targetState = stateTransceiver
	}

	systemID := strings.TrimSpace(bind.SystemID)
	if systemID == "" {
		resp := pdu.NewBindResp(bindCmd|0x80000000, pdu.StatusRInvSysID, bind.Header.SequenceNumber, s.cfg.SystemID)
		_ = s.writePDU(resp)
		return errors.New("empty system_id")
	}

	// Brute-force lockout check: keyed on IP + system_id so an attacker from one IP
	// cannot lock out a legitimate customer bound from a different IP.
	if s.bindLim != nil && s.bindLim.IsLockedOut(s.RemoteIP, systemID) {
		s.logger.Warn("smpp bind rejected: too many failures (lockout)", "system_id", systemID, "remote_ip", s.RemoteIP)
		resp := pdu.NewBindResp(bindCmd|0x80000000, pdu.StatusRBindFail, bind.Header.SequenceNumber, s.cfg.SystemID)
		_ = s.writePDU(resp)
		return errors.New("bind locked out due to too many failures")
	}

	// Query account and verify credentials
	q := `
		SELECT a.id, a.organization_id, a.password_hash, a.allowed_ips,
		       a.allowed_bind_types, a.max_binds, a.max_tps, a.status,
		       o.status, o.type, o.path
		FROM customer_smpp_accounts a
		JOIN organizations o ON o.id = a.organization_id
		WHERE a.system_id = $1
	`
	var (
		accountID     uuid.UUID
		orgID         uuid.UUID
		dbHash        string
		allowedIPs    []string
		allowedBinds  []string
		maxBinds      int
		maxTPS        int
		accountStatus string
		orgStatus     string
		orgType       string
		orgPath       string
	)

	err := s.pool.QueryRow(ctx, q, systemID).Scan(
		&accountID, &orgID, &dbHash, &allowedIPs,
		&allowedBinds, &maxBinds, &maxTPS, &accountStatus,
		&orgStatus, &orgType, &orgPath,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		// Record failure for this IP+system_id to throttle guessing
		if s.bindLim != nil {
			s.bindLim.RecordFailure(s.RemoteIP, systemID)
		}
		resp := pdu.NewBindResp(bindCmd|0x80000000, pdu.StatusRBindFail, bind.Header.SequenceNumber, s.cfg.SystemID)
		_ = s.writePDU(resp)
		return errors.New("smpp account not found")
	}
	if err != nil {
		resp := pdu.NewBindResp(bindCmd|0x80000000, pdu.StatusRSysErr, bind.Header.SequenceNumber, s.cfg.SystemID)
		_ = s.writePDU(resp)
		return err
	}

	// 1. Password Verification via constant-time Argon2id
	match, verifyErr := password.Verify(bind.Password, dbHash)
	if verifyErr != nil || !match {
		if s.bindLim != nil {
			s.bindLim.RecordFailure(s.RemoteIP, systemID)
		}
		s.logger.Warn("smpp bind failed: invalid credentials", "system_id", systemID)
		resp := pdu.NewBindResp(bindCmd|0x80000000, pdu.StatusRInvPaswd, bind.Header.SequenceNumber, s.cfg.SystemID)
		_ = s.writePDU(resp)
		return errors.New("invalid smpp credentials")
	}

	// 2. Organization & Account Status Check
	if orgType != "customer" || orgStatus != "active" || accountStatus != "active" {
		s.logger.Warn("smpp bind rejected: account or organization inactive", "system_id", systemID, "status", accountStatus)
		resp := pdu.NewBindResp(bindCmd|0x80000000, pdu.StatusRInvBndSts, bind.Header.SequenceNumber, s.cfg.SystemID)
		_ = s.writePDU(resp)
		return errors.New("account inactive")
	}

	// 3. IP Allowlist Validation
	if len(allowedIPs) > 0 {
		ipAllowed := false
		for _, ip := range allowedIPs {
			if strings.TrimSpace(ip) == s.RemoteIP {
				ipAllowed = true
				break
			}
		}
		if !ipAllowed {
			s.logger.Warn("smpp bind rejected: remote ip barred", "system_id", systemID, "remote_ip", s.RemoteIP)
			resp := pdu.NewBindResp(bindCmd|0x80000000, pdu.StatusRBindFail, bind.Header.SequenceNumber, s.cfg.SystemID)
			_ = s.writePDU(resp)
			return errors.New("ip not allowed")
		}
	}

	// 4. Allowed Bind Types Check
	typeAllowed := false
	for _, bt := range allowedBinds {
		if strings.EqualFold(bt, bindType) || bt == "*" {
			typeAllowed = true
			break
		}
	}
	if !typeAllowed {
		s.logger.Warn("smpp bind rejected: bind type not permitted", "system_id", systemID, "bind_type", bindType)
		resp := pdu.NewBindResp(bindCmd|0x80000000, pdu.StatusRInvBndSts, bind.Header.SequenceNumber, s.cfg.SystemID)
		_ = s.writePDU(resp)
		return errors.New("bind type not allowed")
	}

	// 5. Max Active Binds Enforcement
	currentBinds := s.tracker.CountActiveBinds(accountID)
	if currentBinds >= maxBinds {
		s.logger.Warn("smpp bind rejected: max binds reached", "system_id", systemID, "active", currentBinds, "max", maxBinds)
		resp := pdu.NewBindResp(bindCmd|0x80000000, pdu.StatusRBindFail, bind.Header.SequenceNumber, s.cfg.SystemID)
		_ = s.writePDU(resp)
		return errors.New("max binds reached")
	}

	// 6. Register Session State & Database Record
	s.mu.Lock()
	s.AccountID = accountID
	s.CustomerOrgID = orgID
	s.CustomerOrgPath = orgPath
	s.SystemID = systemID
	s.BindType = bindType
	s.state = targetState
	s.maxTPS = maxTPS
	s.mu.Unlock()

	_, err = s.pool.Exec(ctx, `
		INSERT INTO customer_smpp_sessions (
			id, account_id, customer_org_id, remote_ip, bind_type,
			system_id, bound_at, last_activity, in_flight, messages_sent, dlrs_delivered
		) VALUES ($1, $2, $3, $4, $5, $6, now(), now(), 0, 0, 0)
	`, s.ID, accountID, orgID, s.RemoteIP, bindType, systemID)
	if err != nil {
		s.logger.Error("failed to record smpp session in db", "error", err)
	}

	s.tracker.Register(s)

	// Successful bind — reset failure counter for this IP+system_id.
	if s.bindLim != nil {
		s.bindLim.RecordSuccess(s.RemoteIP, systemID)
	}

	resp := pdu.NewBindResp(bindCmd|0x80000000, pdu.StatusROK, bind.Header.SequenceNumber, s.cfg.SystemID)
	if err := s.writePDU(resp); err != nil {
		return err
	}

	s.logger.Info("smpp client successfully bound", "system_id", systemID, "bind_type", bindType)
	return nil
}

// handleSubmitSM validates PDU limits, rejects segmentation, and durably persists message.
func (s *ClientSession) handleSubmitSM(ctx context.Context, submit *pdu.SubmitSM) error {
	s.mu.Lock()
	currentState := s.state
	s.mu.Unlock()

	if currentState != stateTransmitter && currentState != stateTransceiver {
		resp := pdu.NewSubmitSMResp(pdu.StatusRInvBndSts, submit.Header.SequenceNumber, "")
		return s.writePDU(resp)
	}

	// Flow control: Window check
	if s.InFlight() >= s.windowSize {
		resp := pdu.NewSubmitSMResp(pdu.StatusRThrottled, submit.Header.SequenceNumber, "")
		return s.writePDU(resp)
	}

	atomic.AddInt32(&s.inFlight, 1)
	defer atomic.AddInt32(&s.inFlight, -1)

	// Single PDU Requirement: Reject UDH and SAR segmentation
	// UDH flag in esm_class is 0x40
	hasUDH := (submit.ESMClass & 0x40) != 0
	hasSAR := false
	for _, tlv := range submit.TLVs {
		if tlv.Tag == pdu.TagSarMsgRefNum || tlv.Tag == pdu.TagSarTotalSegments || tlv.Tag == pdu.TagSarSegmentSeqnum {
			hasSAR = true
			break
		}
	}
	if hasUDH || hasSAR {
		resp := pdu.NewSubmitSMResp(pdu.StatusRInvMsgLen, submit.Header.SequenceNumber, "")
		return s.writePDU(resp)
	}

	// Rate limiting token bucket check (keyed per SMPP account so accounts with different max_tps stay isolated)
	limitKey := s.AccountID
	if limitKey == uuid.Nil {
		limitKey = s.CustomerOrgID
	}
	allowed, _ := s.limiter.AllowN(limitKey, s.maxTPS, 1)
	if !allowed {
		resp := pdu.NewSubmitSMResp(pdu.StatusRThrottled, submit.Header.SequenceNumber, "")
		return s.writePDU(resp)
	}

	// Extract raw message payload (ShortMessage or message_payload TLV)
	rawPayload := submit.ShortMessage
	if len(rawPayload) == 0 {
		for _, tlv := range submit.TLVs {
			if tlv.Tag == pdu.TagMessagePayload {
				rawPayload = tlv.Value
				break
			}
		}
	}
	if len(rawPayload) == 0 {
		resp := pdu.NewSubmitSMResp(pdu.StatusRInvMsgLen, submit.Header.SequenceNumber, "")
		return s.writePDU(resp)
	}
	text, decStatus := decodeSubmitPayload(rawPayload, submit.DataCoding)
	if decStatus != pdu.StatusROK {
		resp := pdu.NewSubmitSMResp(decStatus, submit.Header.SequenceNumber, "")
		return s.writePDU(resp)
	}
	if text == "" {
		resp := pdu.NewSubmitSMResp(pdu.StatusRInvMsgLen, submit.Header.SequenceNumber, "")
		return s.writePDU(resp)
	}

	// Synthetic Principal for permission context
	principal := &customeredge.Principal{
		CustomerOrgID:   s.CustomerOrgID,
		CustomerOrgPath: s.CustomerOrgPath,
		AccountID:       &s.AccountID,
		Scopes:          []string{customeredge.ScopeSMSSend},
		IsActive:        true,
	}

	clientRef := fmt.Sprintf("smpp-%s-%d", s.ID.String()[:8], submit.Header.SequenceNumber)
	dlrReq := (submit.RegisteredDelivery != 0)

	subReq := messaging.SubmitRequest{
		Destination:      submit.DestAddr,
		SenderID:         submit.SourceAddr,
		Text:             text,
		ClientReference:  &clientRef,
		IngressChannel:   "smpp",
		IngressAccountID: &s.AccountID,
		DLRRequested:     dlrReq,
	}

	// Durably submit to messaging service
	subResp, err := s.msgSvc.Submit(ctx, principal.ToTenantContext(), subReq)
	if err != nil {
		mapped := customeredge.MapSubmitError(err)
		resp := pdu.NewSubmitSMResp(mapped.SMPPStatus, submit.Header.SequenceNumber, "")
		return s.writePDU(resp)
	}

	// Format 32-hex SMPP message ID
	smppMsgID := customeredge.FormatUUIDToSMPPHex(subResp.MessageID)

	atomic.AddUint64(&s.messagesSent, 1)

	// Send submit_sm_resp ONLY after database commit
	resp := pdu.NewSubmitSMResp(pdu.StatusROK, submit.Header.SequenceNumber, smppMsgID)
	return s.writePDU(resp)
}

func (s *ClientSession) handleEnquireLink(req *pdu.EnquireLink) error {
	resp := pdu.NewEnquireLinkResp(pdu.StatusROK, req.Header.SequenceNumber)
	return s.writePDU(resp)
}

func (s *ClientSession) handleUnbind(req *pdu.Unbind) error {
	resp := pdu.NewUnbindResp(pdu.StatusROK, req.Header.SequenceNumber)
	_ = s.writePDU(resp)
	s.Close("client_unbound")
	return nil
}

func (s *ClientSession) handleDeliverSMResp(resp *pdu.DeliverSMResp) error {
	s.waitersMu.Lock()
	ch, ok := s.waiters[resp.Header.SequenceNumber]
	if ok {
		delete(s.waiters, resp.Header.SequenceNumber)
		ch <- resp
	}
	s.waitersMu.Unlock()
	return nil
}

// SendDeliverSM transmits a delivery receipt to the client and awaits deliver_sm_resp.
func (s *ClientSession) SendDeliverSM(ctx context.Context, deliver *pdu.DeliverSM) error {
	if !s.CanReceiveDLR() {
		return errors.New("session is not bound as receiver or transceiver")
	}

	seq := s.nextSeq()
	deliver.Header.SequenceNumber = seq

	respChan := make(chan *pdu.DeliverSMResp, 1)
	s.waitersMu.Lock()
	s.waiters[seq] = respChan
	s.waitersMu.Unlock()

	defer func() {
		s.waitersMu.Lock()
		delete(s.waiters, seq)
		s.waitersMu.Unlock()
	}()

	if err := s.writePDU(deliver); err != nil {
		return fmt.Errorf("write deliver_sm: %w", err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case resp := <-respChan:
		if resp.Header.CommandStatus != pdu.StatusROK {
			return fmt.Errorf("client rejected deliver_sm with status: 0x%08x", resp.Header.CommandStatus)
		}
		atomic.AddUint64(&s.dlrsDelivered, 1)
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("timeout waiting for deliver_sm_resp from client")
	}
}

func (s *ClientSession) sendEnquireLink() error {
	seq := s.nextSeq()
	ping := pdu.NewEnquireLink(seq)
	return s.writePDU(ping)
}

func (s *ClientSession) writePDU(p pdu.PDU) error {
	bytes, err := p.Marshal()
	if err != nil {
		return fmt.Errorf("marshal pdu: %w", err)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_ = s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err = s.conn.Write(bytes)
	return err
}

// Close terminates the session cleanly and records close time in database.
func (s *ClientSession) Close(reason string) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.state = stateClosed
		sessionID := s.ID
		accountID := s.AccountID
		s.mu.Unlock()

		_ = s.conn.Close()
		s.tracker.Unregister(sessionID, accountID)

		if accountID != uuid.Nil {
			_, _ = s.pool.Exec(context.Background(), `
				UPDATE customer_smpp_sessions
				SET closed_at = now(), close_reason = $1
				WHERE id = $2 AND closed_at IS NULL
			`, reason, sessionID)
		}

		s.logger.Info("smpp session closed", "reason", reason)
	})
}

// GetSnapshot captures operational telemetry for the active session.
func (s *ClientSession) GetSnapshot() messaging.CustomerSessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return messaging.CustomerSessionInfo{
		ID:            s.ID,
		AccountID:     s.AccountID,
		CustomerOrgID: s.CustomerOrgID,
		SystemID:      s.SystemID,
		BindType:      s.BindType,
		RemoteIP:      s.RemoteIP,
		Bound:         s.state == stateTransmitter || s.state == stateReceiver || s.state == stateTransceiver,
		InFlight:      int(s.inFlight),
		BoundAt:       s.lastActivity,
	}
}

var gsm7DefaultTable = [128]rune{
	'@', '£', '$', '¥', 'è', 'é', 'ù', 'ì', 'ò', 'Ç', '\n', 'Ø', 'ø', '\r', 'Å', 'å',
	'Δ', '_', 'Φ', 'Γ', 'Λ', 'Ω', 'Π', 'Ψ', 'Σ', 'Θ', 'Ξ', '\x1b', 'Æ', 'æ', 'ß', 'É',
	' ', '!', '"', '#', '¤', '%', '&', '\'', '(', ')', '*', '+', ',', '-', '.', '/',
	'0', '1', '2', '3', '4', '5', '6', '7', '8', '9', ':', ';', '<', '=', '>', '?',
	'¡', 'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'I', 'J', 'K', 'L', 'M', 'N', 'O',
	'P', 'Q', 'R', 'S', 'T', 'U', 'V', 'W', 'X', 'Y', 'Z', 'Ä', 'Ö', 'Ñ', 'Ü', '§',
	'¿', 'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j', 'k', 'l', 'm', 'n', 'o',
	'p', 'q', 'r', 's', 't', 'u', 'v', 'w', 'x', 'y', 'z', 'ä', 'ö', 'ñ', 'ü', 'à',
}

var gsm7ExtTable = map[byte]rune{
	0x0A: '\f',
	0x14: '^',
	0x28: '{',
	0x29: '}',
	0x2F: '\\',
	0x3C: '[',
	0x3D: '~',
	0x3E: ']',
	0x40: '|',
	0x65: '€',
}

func decodeSubmitPayload(raw []byte, dcs uint8) (string, uint32) {
	switch dcs {
	case pdu.DataCodingUCS2:
		if len(raw)%2 != 0 {
			return "", pdu.StatusRInvMsgLen
		}
		u16 := make([]uint16, len(raw)/2)
		for i := 0; i < len(u16); i++ {
			u16[i] = uint16(raw[2*i])<<8 | uint16(raw[2*i+1])
			if u16[i] == 0 {
				return "", pdu.StatusRInvMsgLen
			}
		}
		s := string(utf16.Decode(u16))
		if !utf8.ValidString(s) {
			return "", pdu.StatusRInvDCS
		}
		return s, pdu.StatusROK

	case 0x03: // ISO-8859-1 (Latin-1)
		runes := make([]rune, len(raw))
		for i, b := range raw {
			if b == 0 {
				return "", pdu.StatusRInvMsgLen
			}
			runes[i] = rune(b)
		}
		return string(runes), pdu.StatusROK

	case pdu.DataCodingSMSCDefault, pdu.DataCodingASCII:
		hasGSMCtrl := false
		for _, b := range raw {
			if b == 0x00 || b == 0x02 || b == 0x11 || b == 0x1B || b == 0x7F {
				hasGSMCtrl = true
				break
			}
		}
		if !hasGSMCtrl && utf8.Valid(raw) {
			return string(raw), pdu.StatusROK
		}
		// Decode GSM 03.38 unpacked bytes (validates high bit clear)
		var out strings.Builder
		out.Grow(len(raw))
		for i := 0; i < len(raw); i++ {
			b := raw[i]
			if b > 0x7F {
				if utf8.Valid(raw) {
					return string(raw), pdu.StatusROK
				}
				return "", pdu.StatusRInvDCS
			}
			if b == 0x1B && i+1 < len(raw) {
				i++
				if ext, ok := gsm7ExtTable[raw[i]]; ok {
					out.WriteRune(ext)
				} else if raw[i] < 128 {
					out.WriteRune(gsm7DefaultTable[raw[i]])
				}
				continue
			}
			out.WriteRune(gsm7DefaultTable[b])
		}
		return out.String(), pdu.StatusROK

	default:
		// Binary (0x04) and undefined data coding schemes cannot be safely processed as UTF-8 text.
		return "", pdu.StatusRInvDCS
	}
}
