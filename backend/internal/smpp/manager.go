package smpp

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Decrypter decrypts stored ciphertext into plaintext.
type Decrypter interface {
	Decrypt(envelope string) (string, error)
}

// ConnectionConfig holds fetched database configuration for a provider connection.
type ConnectionConfig struct {
	ID                     uuid.UUID
	ProviderID             uuid.UUID
	Name                   string
	Protocol               string
	Status                 string
	Archived               bool
	MaxTPS                 int
	SMPPHost               string
	SMPPPort               int
	SMPPSystemID           string
	SMPPPasswordEncrypted  string
	SMPPPasswordChangedAt  *time.Time
	SMPPSystemType         string
	SMPPBindType           string
	SMPPTLS                bool
	SMPPWindowSize         int
	SMPPEnquireLinkSec     int
	SMPPSourceTON          int
	SMPPSourceNPI          int
	SMPPDestTON            int
	SMPPDestNPI            int
	SMPPCharset            string
	Version                int
	KillSwitched           bool
	KillSwitchReason       string
}

// ManagerConfig holds dependencies for the SMPP ConnectionManager.
type ManagerConfig struct {
	DB            *pgxpool.Pool
	Decrypter     Decrypter
	AllowLoopback bool // true when APP_ENV=development
	Resolver      Resolver
	TLSConfig     *tls.Config
	Logger        *slog.Logger
	DLRHandler    DLRHandler
}

// bindState tracks failure counts and backoff per connection to prevent carrier lockouts.
type bindState struct {
	consecutiveFails int
	nextAttemptAt    time.Time
	lockedOutAlert   bool
}

// ConnectionManager maintains and multiplexes active SMPP sessions across provider connections.
type ConnectionManager struct {
	cfg        ManagerConfig
	dialer     *SafeDialer
	mu         sync.RWMutex
	sessions   map[uuid.UUID]*Session
	lockConns  map[uuid.UUID]*pgxpool.Conn // holds the PostgreSQL advisory lock per connection
	bindStates map[uuid.UUID]*bindState
	closed     bool
}

// NewConnectionManager creates a new ConnectionManager.
func NewConnectionManager(cfg ManagerConfig) *ConnectionManager {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	dialer := NewSafeDialer(DialOptions{
		Timeout:       10 * time.Second,
		AllowLoopback: cfg.AllowLoopback,
		Resolver:      cfg.Resolver,
		TLS:           false, // TLS is decided per-connection below
	})

	return &ConnectionManager{
		cfg:        cfg,
		dialer:     dialer,
		sessions:   make(map[uuid.UUID]*Session),
		lockConns:  make(map[uuid.UUID]*pgxpool.Conn),
		bindStates: make(map[uuid.UUID]*bindState),
	}
}

// GetSession returns an active bound SMPP session for the connection, establishing or refreshing it if needed.
func (m *ConnectionManager) GetSession(ctx context.Context, connID uuid.UUID) (*Session, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, fmt.Errorf("connection manager is closed")
	}

	// 1. Fetch connection configuration and verify eligibility
	connCfg, err := m.fetchConnectionConfig(ctx, connID)
	if err != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("fetch connection config %s: %w", connID, err)
	}

	// 2. Kill-switch and active status checks
	if connCfg.KillSwitched {
		// Close existing session immediately without restarting
		m.closeSessionLocked(connID)
		m.mu.Unlock()
		return nil, fmt.Errorf("connection %s is disabled by kill switch: %s", connID, connCfg.KillSwitchReason)
	}

	if connCfg.Status != "active" || connCfg.Archived {
		m.closeSessionLocked(connID)
		m.mu.Unlock()
		return nil, fmt.Errorf("connection %s is not active (status=%s, archived=%v)", connID, connCfg.Status, connCfg.Archived)
	}

	// 3. Check existing session
	existingSession, exists := m.sessions[connID]
	if exists && existingSession != nil {
		stats := existingSession.Stats()
		if stats.Bound {
			m.mu.Unlock()
			return existingSession, nil
		}
		// Session exists but is unbound / dead: clean it up
		m.closeSessionLocked(connID)
	}

	// 4. Check repeated bind failure backoff cap to prevent carrier lockout
	bState, hasState := m.bindStates[connID]
	if !hasState {
		bState = &bindState{}
		m.bindStates[connID] = bState
	}

	if time.Now().Before(bState.nextAttemptAt) {
		m.mu.Unlock()
		return nil, fmt.Errorf("connection %s is in backoff until %s due to %d repeated bind failures",
			connID, bState.nextAttemptAt.Format(time.RFC3339), bState.consecutiveFails)
	}

	// 5. Acquire PostgreSQL advisory lock so only ONE cluster node binds this connection
	if m.cfg.DB != nil {
		if _, hasLock := m.lockConns[connID]; !hasLock {
			lockConn, err := m.acquireAdvisoryLock(ctx, connID)
			if err != nil {
				m.mu.Unlock()
				return nil, fmt.Errorf("acquire advisory lock for %s: %w", connID, err)
			}
			m.lockConns[connID] = lockConn
		}
	}

	m.mu.Unlock()

	// 6. Decrypt credentials in memory
	var passwordPlain string
	if m.cfg.Decrypter != nil && connCfg.SMPPPasswordEncrypted != "" {
		decrypted, err := m.cfg.Decrypter.Decrypt(connCfg.SMPPPasswordEncrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypt smpp password for %s: %w", connID, err)
		}
		passwordPlain = decrypted
	} else {
		passwordPlain = connCfg.SMPPPasswordEncrypted
	}

	// 7. Dial upstream carrier using SSRF-protected dialer
	dialOpts := DialOptions{
		Timeout:       10 * time.Second,
		AllowLoopback: m.cfg.AllowLoopback,
		Resolver:      m.cfg.Resolver,
		TLS:           connCfg.SMPPTLS,
		TLSConfig:     m.cfg.TLSConfig,
	}
	dialer := NewSafeDialer(dialOpts)

	targetAddr := fmt.Sprintf("%s:%d", connCfg.SMPPHost, connCfg.SMPPPort)
	conn, err := dialer.DialContext(ctx, "tcp", targetAddr)
	if err != nil {
		m.recordBindFailure(connID, err)
		return nil, fmt.Errorf("dial upstream carrier %s (%s): %w", connCfg.Name, targetAddr, err)
	}

	// 8. Construct and Bind session
	sessionCfg := SessionConfig{
		ConnectionID:   connID,
		SystemID:       connCfg.SMPPSystemID,
		Password:       passwordPlain,
		SystemType:     connCfg.SMPPSystemType,
		BindType:       connCfg.SMPPBindType,
		WindowSize:     connCfg.SMPPWindowSize,
		EnquireLinkSec: connCfg.SMPPEnquireLinkSec,
		DLRHandler:     m.cfg.DLRHandler,
	}

	session := NewSession(sessionCfg, conn)

	bindCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := session.Bind(bindCtx); err != nil {
		_ = session.Close()
		m.recordBindFailure(connID, err)
		return nil, fmt.Errorf("bind upstream carrier %s (%s): %w", connCfg.Name, targetAddr, err)
	}

	// 9. Successfully bound: reset backoff state and store session
	m.mu.Lock()
	m.bindStates[connID] = &bindState{} // reset failures
	m.sessions[connID] = session
	m.mu.Unlock()

	m.cfg.Logger.Info("smpp session bound successfully",
		slog.String("connection_id", connID.String()),
		slog.String("connection_name", connCfg.Name),
		slog.String("system_id", connCfg.SMPPSystemID),
		slog.String("bind_type", connCfg.SMPPBindType),
		slog.Bool("tls", connCfg.SMPPTLS),
	)

	return session, nil
}

// CloseConnectionSession closes the active session for a specific connection.
func (m *ConnectionManager) CloseConnectionSession(connID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeSessionLocked(connID)
}

// Close shuts down all sessions and releases advisory locks.
func (m *ConnectionManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true

	for connID := range m.sessions {
		m.closeSessionLocked(connID)
	}
	return nil
}

func (m *ConnectionManager) closeSessionLocked(connID uuid.UUID) {
	if s, exists := m.sessions[connID]; exists && s != nil {
		_ = s.Close()
		delete(m.sessions, connID)
	}
	if lockConn, exists := m.lockConns[connID]; exists && lockConn != nil {
		k1, k2 := uuidToLockKeys(connID)
		_, _ = lockConn.Exec(context.Background(), "SELECT pg_advisory_unlock($1, $2)", k1, k2)
		lockConn.Release()
		delete(m.lockConns, connID)
	}
}

func (m *ConnectionManager) recordBindFailure(connID uuid.UUID, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	bState, exists := m.bindStates[connID]
	if !exists {
		bState = &bindState{}
		m.bindStates[connID] = bState
	}

	bState.consecutiveFails++

	// Exponential backoff: min(2^fails * 1s, 60s) + jitter
	backoffSec := 1 << bState.consecutiveFails
	if backoffSec > 60 {
		backoffSec = 60
	}
	jitter := time.Duration(rand.Intn(1000)) * time.Millisecond
	bState.nextAttemptAt = time.Now().Add(time.Duration(backoffSec)*time.Second + jitter)

	// Alert on lockout threshold
	if bState.consecutiveFails >= 5 && !bState.lockedOutAlert {
		bState.lockedOutAlert = true
		m.cfg.Logger.Error("CRITICAL ALERT: SMPP Repeated bind failure cap reached for connection. Backing off to prevent carrier lockout.",
			slog.String("connection_id", connID.String()),
			slog.Int("consecutive_failures", bState.consecutiveFails),
			slog.String("next_attempt_at", bState.nextAttemptAt.Format(time.RFC3339)),
			slog.String("error", err.Error()),
		)
	} else {
		m.cfg.Logger.Warn("smpp bind failed, backing off",
			slog.String("connection_id", connID.String()),
			slog.Int("consecutive_failures", bState.consecutiveFails),
			slog.String("next_attempt_at", bState.nextAttemptAt.Format(time.RFC3339)),
			slog.String("error", err.Error()),
		)
	}
}

func (m *ConnectionManager) fetchConnectionConfig(ctx context.Context, connID uuid.UUID) (*ConnectionConfig, error) {
	if m.cfg.DB == nil {
		// Mock / test default config
		return &ConnectionConfig{
			ID:                 connID,
			Name:               "Test Connection",
			Protocol:           "smpp",
			Status:             "active",
			SMPPHost:           "127.0.0.1",
			SMPPPort:           2775,
			SMPPSystemID:       "test_sys",
			SMPPSystemType:     "SMPP",
			SMPPBindType:       "transceiver",
			SMPPTLS:            false,
			SMPPWindowSize:     10,
			SMPPEnquireLinkSec: 30,
			MaxTPS:             50,
		}, nil
	}

	query := `
		SELECT 
			c.id, c.provider_id, c.name, c.protocol, c.status, c.archived, c.max_tps,
			c.smpp_host, c.smpp_port, c.smpp_system_id, c.smpp_password_encrypted,
			c.smpp_password_changed_at, c.smpp_system_type, c.smpp_bind_type, c.smpp_tls,
			c.smpp_window_size, c.smpp_enquire_link_sec, c.smpp_source_ton, c.smpp_source_npi,
			c.smpp_dest_ton, c.smpp_dest_npi, c.smpp_charset, c.version,
			COALESCE(o.state = 'disabled', false) AS kill_switched,
			COALESCE(o.reason, '') AS kill_switch_reason
		FROM provider_connections c
		LEFT JOIN provider_connection_overrides o ON o.connection_id = c.id
		WHERE c.id = $1
	`

	var cfg ConnectionConfig
	err := m.cfg.DB.QueryRow(ctx, query, connID).Scan(
		&cfg.ID, &cfg.ProviderID, &cfg.Name, &cfg.Protocol, &cfg.Status, &cfg.Archived, &cfg.MaxTPS,
		&cfg.SMPPHost, &cfg.SMPPPort, &cfg.SMPPSystemID, &cfg.SMPPPasswordEncrypted,
		&cfg.SMPPPasswordChangedAt, &cfg.SMPPSystemType, &cfg.SMPPBindType, &cfg.SMPPTLS,
		&cfg.SMPPWindowSize, &cfg.SMPPEnquireLinkSec, &cfg.SMPPSourceTON, &cfg.SMPPSourceNPI,
		&cfg.SMPPDestTON, &cfg.SMPPDestNPI, &cfg.SMPPCharset, &cfg.Version,
		&cfg.KillSwitched, &cfg.KillSwitchReason,
	)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (m *ConnectionManager) acquireAdvisoryLock(ctx context.Context, connID uuid.UUID) (*pgxpool.Conn, error) {
	conn, err := m.cfg.DB.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection from pool: %w", err)
	}

	k1, k2 := uuidToLockKeys(connID)
	var acquired bool
	err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, $2)", k1, k2).Scan(&acquired)
	if err != nil {
		conn.Release()
		return nil, fmt.Errorf("query pg_try_advisory_lock: %w", err)
	}

	if !acquired {
		conn.Release()
		return nil, fmt.Errorf("advisory lock for connection %s is held by another process/cluster node", connID)
	}

	return conn, nil
}

func uuidToLockKeys(u uuid.UUID) (int32, int32) {
	k1 := int32(binary.BigEndian.Uint32(u[0:4]))
	k2 := int32(binary.BigEndian.Uint32(u[4:8]))
	return k1, k2
}
