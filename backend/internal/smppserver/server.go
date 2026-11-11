package smppserver

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/internal/customeredge"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/smpp/pdu"
)

// ServerConfig configures the customer-facing SMPP 3.4 server.
type ServerConfig struct {
	Addr            string        `json:"addr"`
	TLSEnabled      bool          `json:"tls_enabled"`
	TLSCertFile     string        `json:"tls_cert_file"`
	TLSKeyFile      string        `json:"tls_key_file"`
	SystemID        string        `json:"system_id"`
	IdleTimeout     time.Duration `json:"idle_timeout"`
	EnquireInterval time.Duration `json:"enquire_interval"`
	DefaultWindow   int           `json:"default_window"`
}

// Server is the high-performance SMPP 3.4 server accepting customer binds.
type Server struct {
	cfg        ServerConfig
	pool       *pgxpool.Pool
	msgSvc     *messaging.Service
	repo       messaging.Repository
	limiter    *customeredge.CustomerLimiter
	bindLim    *BindLimiter
	logger     *slog.Logger
	listener   net.Listener
	tracker    *SessionTracker
	mu         sync.Mutex
	running    bool
	cancelFunc context.CancelFunc
}

// NewServer creates a new customer SMPP server instance.
func NewServer(
	cfg ServerConfig,
	pool *pgxpool.Pool,
	msgSvc *messaging.Service,
	repo messaging.Repository,
	limiter *customeredge.CustomerLimiter,
	logger *slog.Logger,
) *Server {
	if cfg.Addr == "" {
		cfg.Addr = ":2776"
	}
	if cfg.SystemID == "" {
		cfg.SystemID = "ZNERO"
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 60 * time.Second
	}
	if cfg.EnquireInterval <= 0 {
		cfg.EnquireInterval = 30 * time.Second
	}
	if cfg.DefaultWindow <= 0 {
		cfg.DefaultWindow = 16
	}

	return &Server{
		cfg:     cfg,
		pool:    pool,
		msgSvc:  msgSvc,
		repo:    repo,
		limiter: limiter,
		bindLim: NewBindLimiter(),
		logger:  logger,
		tracker: NewSessionTracker(),
	}
}

// Tracker returns the live session tracker.
func (s *Server) Tracker() *SessionTracker {
	return s.tracker
}

// Start begins listening on the configured TCP or TLS address.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}

	var l net.Listener
	var err error

	if s.cfg.TLSEnabled && s.cfg.TLSCertFile != "" && s.cfg.TLSKeyFile != "" {
		cert, certErr := tls.LoadX509KeyPair(s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
		if certErr != nil {
			s.mu.Unlock()
			return fmt.Errorf("load smpp server tls cert: %w", certErr)
		}
		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		l, err = tls.Listen("tcp", s.cfg.Addr, tlsConfig)
	} else {
		l, err = net.Listen("tcp", s.cfg.Addr)
	}

	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("smpp server listen on %s: %w", s.cfg.Addr, err)
	}

	s.listener = l
	s.running = true
	serverCtx, cancel := context.WithCancel(ctx)
	s.cancelFunc = cancel
	s.mu.Unlock()

	s.logger.Info("customer smpp server started", "addr", s.cfg.Addr, "tls", s.cfg.TLSEnabled)

	go s.acceptLoop(serverCtx)
	go s.sessionMetricsLoop(serverCtx)

	return nil
}

// Stop gracefully shuts down the listener and disconnects active sessions.
func (s *Server) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	if s.cancelFunc != nil {
		s.cancelFunc()
	}
	if s.listener != nil {
		_ = s.listener.Close()
	}
	s.mu.Unlock()

	s.tracker.CloseAll("server_shutdown")
	s.logger.Info("customer smpp server stopped")
}

func (s *Server) acceptLoop(ctx context.Context) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				s.logger.Error("smpp server accept error", "error", err)
				time.Sleep(100 * time.Millisecond)
				continue
			}
		}

		session := NewClientSession(conn, s.pool, s.msgSvc, s.repo, s.limiter, s.bindLim, s.tracker, s.cfg, s.logger)
		go session.Run(ctx)
	}
}

// sessionMetricsLoop updates last_activity and in_flight counts in database.
func (s *Server) sessionMetricsLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			active := s.tracker.GetAllSessions()
			for _, sess := range active {
				if sess.IsBound() {
					_, _ = s.pool.Exec(ctx, `
						UPDATE customer_smpp_sessions
						SET in_flight = $1, messages_sent = $2, dlrs_delivered = $3, last_activity = $4
						WHERE id = $5
					`, sess.InFlight(), sess.MessagesSent(), sess.DLRsDelivered(), sess.LastActivity(), sess.ID)
				}
			}
		}
	}
}

// ForceDisconnect terminates an active session administratively.
func (s *Server) ForceDisconnect(sessionID uuid.UUID) error {
	sess := s.tracker.GetSession(sessionID)
	if sess == nil {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	sess.Close("administrative_disconnect")
	return nil
}

// SessionTracker maintains in-memory index of active client sessions.
type SessionTracker struct {
	mu           sync.RWMutex
	sessions     map[uuid.UUID]*ClientSession
	accountIndex map[uuid.UUID][]*ClientSession
}

func NewSessionTracker() *SessionTracker {
	return &SessionTracker{
		sessions:     make(map[uuid.UUID]*ClientSession),
		accountIndex: make(map[uuid.UUID][]*ClientSession),
	}
}

func (t *SessionTracker) Register(s *ClientSession) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.sessions[s.ID] = s
	t.accountIndex[s.AccountID] = append(t.accountIndex[s.AccountID], s)
}

func (t *SessionTracker) Unregister(sessionID uuid.UUID, accountID uuid.UUID) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.sessions, sessionID)
	list := t.accountIndex[accountID]
	for i, sess := range list {
		if sess.ID == sessionID {
			t.accountIndex[accountID] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(t.accountIndex[accountID]) == 0 {
		delete(t.accountIndex, accountID)
	}
}

func (t *SessionTracker) GetSession(id uuid.UUID) *ClientSession {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.sessions[id]
}

func (t *SessionTracker) GetSessionsForAccount(accountID uuid.UUID) []*ClientSession {
	t.mu.RLock()
	defer t.mu.RUnlock()
	res := make([]*ClientSession, len(t.accountIndex[accountID]))
	copy(res, t.accountIndex[accountID])
	return res
}

func (t *SessionTracker) CountActiveBinds(accountID uuid.UUID) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.accountIndex[accountID])
}

func (t *SessionTracker) GetAllSessions() []*ClientSession {
	t.mu.RLock()
	defer t.mu.RUnlock()
	res := make([]*ClientSession, 0, len(t.sessions))
	for _, s := range t.sessions {
		res = append(res, s)
	}
	return res
}

func (t *SessionTracker) GetSessionsForOrg(orgID uuid.UUID) []*ClientSession {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var res []*ClientSession
	for _, s := range t.sessions {
		s.mu.Lock()
		match := (s.CustomerOrgID == orgID)
		s.mu.Unlock()
		if match {
			res = append(res, s)
		}
	}
	return res
}

func (t *SessionTracker) CloseAll(reason string) {
	t.mu.Lock()
	sessions := make([]*ClientSession, 0, len(t.sessions))
	for _, s := range t.sessions {
		sessions = append(sessions, s)
	}
	t.mu.Unlock()

	for _, s := range sessions {
		s.Close(reason)
	}
}

// DeliverMO delivers an incoming MO deliver_sm PDU to a bound receiver/transceiver session.
func (s *Server) DeliverMO(ctx context.Context, customerOrgID uuid.UUID, sourceAddr, destAddr string, body []byte, isUCS2 bool) error {
	sessions := s.tracker.GetSessionsForOrg(customerOrgID)
	if len(sessions) == 0 {
		return nil
	}

	deliver := pdu.NewDeliverSM(0)
	deliver.ESMClass = 0x00 // Standard MO message (NOT DLR)
	deliver.SourceAddr = sourceAddr
	deliver.DestAddr = destAddr
	deliver.ShortMessage = body
	if isUCS2 {
		deliver.DataCoding = 0x08
	}

	for _, sess := range sessions {
		if sess.CanReceiveDLR() {
			return sess.SendDeliverSM(ctx, deliver)
		}
	}
	return nil
}

// GetActiveCustomerSessions returns snapshots of all currently registered client sessions.
func (s *Server) GetActiveCustomerSessions() []messaging.CustomerSessionInfo {
	sessions := s.tracker.GetAllSessions()
	res := make([]messaging.CustomerSessionInfo, 0, len(sessions))
	for _, sess := range sessions {
		res = append(res, sess.GetSnapshot())
	}
	return res
}
