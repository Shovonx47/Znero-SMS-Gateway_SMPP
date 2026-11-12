package smpp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/smpp/pdu"
)

// ErrAmbiguousOutcome indicates submit_sm was written onto the carrier socket
// but the session/connection terminated before submit_sm_resp arrived.
// Callers MUST treat this as SendStatusUnknown and NEVER auto-resend.
var ErrAmbiguousOutcome = errors.New("submit_sm written to wire; connection closed before submit_sm_resp")

// DLRHandler is called when a deliver_sm PDU is received from the upstream carrier.
type DLRHandler func(ctx context.Context, connectionID uuid.UUID, deliver *pdu.DeliverSM) error

// SessionConfig configures a single SMPP session.
type SessionConfig struct {
	ConnectionID   uuid.UUID
	SystemID       string
	Password       string
	SystemType     string
	BindType       string // "transceiver", "transmitter", "receiver"
	WindowSize     int
	EnquireLinkSec int
	DLRHandler     DLRHandler
}

// SessionStats represents live session operational metrics.
type SessionStats struct {
	ConnectionID   uuid.UUID
	Bound          bool
	InFlight       int
	CurrentTPS     float64
	SuccessCount   uint64
	FailureCount   uint64
	LastErrorCode  string
	ReconnectCount uint64
}

// Session manages a single bound SMPP connection with sliding window flow control.
type Session struct {
	cfg        SessionConfig
	conn       net.Conn
	window     chan struct{} // sliding window semaphore
	seqCounter uint32

	mu           sync.RWMutex
	inFlight     map[uint32]chan *pdu.SubmitSMResp
	bound        bool
	closed       bool
	closeCh      chan struct{}
	lastActive   time.Time
	successCount uint64
	failureCount uint64
	lastErrCode  string
	reconnects   uint64

	// TPS calculation
	tpsWindowStart time.Time
	tpsCount       uint64
	currentTPS     float64
}

// NewSession creates an unbound Session over an existing network connection.
func NewSession(cfg SessionConfig, conn net.Conn) *Session {
	if cfg.WindowSize <= 0 {
		cfg.WindowSize = 10
	}
	if cfg.EnquireLinkSec <= 0 {
		cfg.EnquireLinkSec = 30
	}
	if cfg.BindType == "" {
		cfg.BindType = "transceiver"
	}

	s := &Session{
		cfg:            cfg,
		conn:           conn,
		window:         make(chan struct{}, cfg.WindowSize),
		inFlight:       make(map[uint32]chan *pdu.SubmitSMResp),
		closeCh:        make(chan struct{}),
		lastActive:     time.Now(),
		tpsWindowStart: time.Now(),
	}

	// Fill window semaphore
	for i := 0; i < cfg.WindowSize; i++ {
		s.window <- struct{}{}
	}

	return s
}

// NextSeq returns an incrementing sequence number in the valid SMPP range [1, 0x7FFFFFFF].
func (s *Session) NextSeq() uint32 {
	for {
		val := atomic.AddUint32(&s.seqCounter, 1)
		// Mask out the sign bit (SMPP sequence number is 1 to 2^31 - 1)
		seq := val & 0x7FFFFFFF
		if seq != 0 {
			return seq
		}
	}
}

// Bind sends the bind PDU and waits for bind_resp.
func (s *Session) Bind(ctx context.Context) error {
	var cmdID uint32
	switch s.cfg.BindType {
	case "transmitter":
		cmdID = pdu.CmdBindTransmitter
	case "receiver":
		cmdID = pdu.CmdBindReceiver
	default:
		cmdID = pdu.CmdBindTransceiver
	}

	seqNum := s.NextSeq()
	bindPDU := pdu.NewBind(cmdID, seqNum, s.cfg.SystemID, s.cfg.Password, s.cfg.SystemType)

	// Set deadline on connection for initial bind
	if deadline, ok := ctx.Deadline(); ok {
		_ = s.conn.SetDeadline(deadline)
	} else {
		_ = s.conn.SetDeadline(time.Now().Add(10 * time.Second))
	}

	if err := pdu.WritePDU(s.conn, bindPDU); err != nil {
		return fmt.Errorf("write bind pdu: %w", err)
	}

	resp, err := pdu.ReadPDU(s.conn)
	if err != nil {
		return fmt.Errorf("read bind resp: %w", err)
	}

	// Clear connection deadline for normal operation
	_ = s.conn.SetDeadline(time.Time{})

	hdr := resp.GetHeader()
	if hdr.CommandStatus != pdu.StatusROK {
		s.mu.Lock()
		s.lastErrCode = StatusString(hdr.CommandStatus)
		s.failureCount++
		s.mu.Unlock()
		return fmt.Errorf("bind rejected with status %s (0x%08x)", StatusString(hdr.CommandStatus), hdr.CommandStatus)
	}

	s.mu.Lock()
	s.bound = true
	s.mu.Unlock()

	// Start reader loop and heartbeat loop
	go s.readLoop()
	go s.heartbeatLoop()

	return nil
}

// Submit writes a submit_sm PDU adhering to the sliding window and waits for submit_sm_resp.
func (s *Session) Submit(ctx context.Context, sm *pdu.SubmitSM) (*pdu.SubmitSMResp, error) {
	s.mu.RLock()
	if !s.bound || s.closed {
		s.mu.RUnlock()
		return nil, fmt.Errorf("session is not bound or closed")
	}
	s.mu.RUnlock()

	// 1. Acquire sliding window token
	select {
	case <-s.window:
		// token acquired
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closeCh:
		return nil, fmt.Errorf("session closed while waiting for sliding window")
	}

	defer func() {
		// Return window token
		select {
		case s.window <- struct{}{}:
		default:
		}
	}()

	// 2. Assign sequence number and register in-flight waiter
	seqNum := s.NextSeq()
	sm.Header.SequenceNumber = seqNum

	respCh := make(chan *pdu.SubmitSMResp, 1)

	s.mu.Lock()
	s.inFlight[seqNum] = respCh
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.inFlight, seqNum)
		s.mu.Unlock()
	}()

	// 3. Write PDU to wire
	if err := pdu.WritePDU(s.conn, sm); err != nil {
		s.recordFailure(err.Error())
		return nil, fmt.Errorf("write submit_sm: %w", err)
	}

	// 4. Wait for response or context timeout
	select {
	case resp, ok := <-respCh:
		if !ok || resp == nil {
			s.recordFailure("connection reset during wait")
			return nil, fmt.Errorf("%w: connection closed while waiting for submit_sm_resp", ErrAmbiguousOutcome)
		}
		if resp.Header.CommandStatus == pdu.StatusROK {
			s.recordSuccess()
		} else {
			s.recordFailure(StatusString(resp.Header.CommandStatus))
		}
		return resp, nil

	case <-ctx.Done():
		// TIMEOUT: PDU was written to the carrier, but response was not received!
		// MUST NOT resend or fail over without confirmation.
		s.recordFailure("timeout_waiting_for_resp")
		return nil, ctx.Err()

	case <-s.closeCh:
		s.recordFailure("session_closed")
		return nil, fmt.Errorf("%w: session closed while waiting for submit_sm_resp", ErrAmbiguousOutcome)
	}
}

// Close gracefully unbinds and terminates the session.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.bound = false
	close(s.closeCh)

	// Wake up all pending in-flight submitters
	for seq, ch := range s.inFlight {
		close(ch)
		delete(s.inFlight, seq)
	}
	s.mu.Unlock()

	// Try unbinding cleanly
	_ = s.conn.SetDeadline(time.Now().Add(2 * time.Second))
	_ = pdu.WritePDU(s.conn, pdu.NewUnbind(s.NextSeq()))

	return s.conn.Close()
}

// Stats returns a snapshot of session metrics.
func (s *Session) Stats() SessionStats {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Calculate TPS
	now := time.Now()
	elapsed := now.Sub(s.tpsWindowStart).Seconds()
	if elapsed >= 1.0 {
		s.currentTPS = float64(s.tpsCount) / elapsed
		s.tpsWindowStart = now
		s.tpsCount = 0
	}

	return SessionStats{
		ConnectionID:   s.cfg.ConnectionID,
		Bound:          s.bound,
		InFlight:       len(s.inFlight),
		CurrentTPS:     s.currentTPS,
		SuccessCount:   s.successCount,
		FailureCount:   s.failureCount,
		LastErrorCode:  s.lastErrCode,
		ReconnectCount: s.reconnects,
	}
}

func (s *Session) recordSuccess() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.successCount++
	s.tpsCount++
	s.lastActive = time.Now()
}

func (s *Session) recordFailure(errCode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failureCount++
	s.lastErrCode = errCode
	s.lastActive = time.Now()
}

func (s *Session) readLoop() {
	for {
		p, err := pdu.ReadPDU(s.conn)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				s.recordFailure(err.Error())
			}
			_ = s.Close()
			return
		}

		s.mu.Lock()
		s.lastActive = time.Now()
		s.mu.Unlock()

		hdr := p.GetHeader()

		switch hdr.CommandID {
		case pdu.CmdSubmitSMResp:
			resp, ok := p.(*pdu.SubmitSMResp)
			if ok {
				s.mu.RLock()
				ch, found := s.inFlight[hdr.SequenceNumber]
				s.mu.RUnlock()
				if found && ch != nil {
					select {
					case ch <- resp:
					default:
					}
				}
			}

		case pdu.CmdEnquireLink:
			// Automatically answer heartbeats from the SMSC
			_ = pdu.WritePDU(s.conn, pdu.NewEnquireLinkResp(pdu.StatusROK, hdr.SequenceNumber))

		case pdu.CmdEnquireLinkResp:
			// Heartbeat acknowledged

		case pdu.CmdDeliverSM:
			deliver, ok := p.(*pdu.DeliverSM)
			if ok {
				// Persist raw DLR receipt BEFORE sending deliver_sm_resp
				respStatus := pdu.StatusROK
				if s.cfg.DLRHandler != nil {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					if err := s.cfg.DLRHandler(ctx, s.cfg.ConnectionID, deliver); err != nil {
						respStatus = pdu.StatusRSysErr
					}
					cancel()
				}
				_ = pdu.WritePDU(s.conn, pdu.NewDeliverSMResp(respStatus, hdr.SequenceNumber))
			}

		case pdu.CmdUnbind:
			_ = pdu.WritePDU(s.conn, pdu.NewUnbindResp(pdu.StatusROK, hdr.SequenceNumber))
			_ = s.Close()
			return

		case pdu.CmdGenericNack:
			s.mu.RLock()
			ch, found := s.inFlight[hdr.SequenceNumber]
			s.mu.RUnlock()
			if found && ch != nil {
				// Map generic nack to submit_sm_resp with error
				select {
				case ch <- pdu.NewSubmitSMResp(hdr.CommandStatus, hdr.SequenceNumber, ""):
				default:
				}
			}
		}
	}
}

func (s *Session) heartbeatLoop() {
	ticker := time.NewTicker(time.Duration(s.cfg.EnquireLinkSec) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.mu.RLock()
			if !s.bound || s.closed {
				s.mu.RUnlock()
				return
			}
			lastAct := s.lastActive
			s.mu.RUnlock()

			// Inactivity watchdog: if no activity for 3x heartbeat interval, close session
			if time.Since(lastAct) > time.Duration(s.cfg.EnquireLinkSec*3)*time.Second {
				s.recordFailure("inactivity_watchdog_timeout")
				_ = s.Close()
				return
			}

			// Send EnquireLink
			seq := s.NextSeq()
			_ = pdu.WritePDU(s.conn, pdu.NewEnquireLink(seq))

		case <-s.closeCh:
			return
		}
	}
}
