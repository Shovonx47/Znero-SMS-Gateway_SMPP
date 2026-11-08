package customeredge

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type cachedPrincipal struct {
	principal *Principal
	cachedAt  time.Time
}

type failureRecord struct {
	count       int
	lockoutUntl time.Time
}

// EdgeAuthenticator validates customer credentials against PostgreSQL with a short-TTL cache
// and rate-limited failure protection against brute-force attacks.
type EdgeAuthenticator struct {
	pool      *pgxpool.Pool
	logger    *slog.Logger
	cacheTTL  time.Duration
	cacheMu   sync.RWMutex
	cache     map[string]cachedPrincipal // key: sha256 hash
	failMu    sync.Mutex
	failures  map[string]*failureRecord // key: IP or key prefix
	trustedIP []string
}

// NewEdgeAuthenticator creates a credential authenticator. Cache TTL is strictly capped at 10s.
func NewEdgeAuthenticator(pool *pgxpool.Pool, trustedProxies []string, logger *slog.Logger) *EdgeAuthenticator {
	if logger == nil {
		logger = slog.Default()
	}
	return &EdgeAuthenticator{
		pool:      pool,
		logger:    logger,
		cacheTTL:  10 * time.Second, // Max 10 seconds per requirements
		cache:     make(map[string]cachedPrincipal),
		failures:  make(map[string]*failureRecord),
		trustedIP: trustedProxies,
	}
}

// InvalidateKey evicts an API key from cache immediately upon revocation or rotation.
func (a *EdgeAuthenticator) InvalidateKey(keyHashHex string) {
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	delete(a.cache, keyHashHex)
}

// InvalidateOrg evicts all cached keys belonging to a customer organization upon suspension.
func (a *EdgeAuthenticator) InvalidateOrg(orgID uuid.UUID) {
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	for k, v := range a.cache {
		if v.principal != nil && v.principal.CustomerOrgID == orgID {
			delete(a.cache, k)
		}
	}
}

// AuthenticateAPIKey authenticates an API key (e.g. "znk_live_...") with constant-time comparison,
// brute-force backoff, IP allowlist verification, and active customer organization validation.
func (a *EdgeAuthenticator) AuthenticateAPIKey(ctx context.Context, rawKey string, clientIP string) (*Principal, error) {
	rawKey = strings.TrimSpace(rawKey)
	if !strings.HasPrefix(rawKey, "znk_live_") || len(rawKey) < 20 {
		a.recordFailure(clientIP)
		return nil, ErrUnauthorized
	}

	// 1. Brute-force lockout check
	if a.isLockedOut(clientIP) {
		return nil, ErrUnauthorized
	}

	// 2. Compute SHA-256 hash
	sum := sha256.Sum256([]byte(rawKey))
	keyHash := hex.EncodeToString(sum[:])

	// 3. Check memory cache (<=10s TTL)
	a.cacheMu.RLock()
	cached, ok := a.cache[keyHash]
	a.cacheMu.RUnlock()

	now := time.Now()
	if ok && now.Sub(cached.cachedAt) < a.cacheTTL {
		if cached.principal == nil || !cached.principal.IsActive {
			return nil, ErrUnauthorized
		}
		if !cached.principal.ValidateIP(clientIP) {
			return nil, ErrForbiddenIPNotAllowed
		}
		return cached.principal, nil
	}

	// 4. Query database with join to organization to ensure customer is active
	query := `
		SELECT k.id, k.organization_id, k.label, k.key_prefix, k.key_hash,
		       k.scopes, k.ip_allowlist, k.status, k.expires_at,
		       o.status, o.type, coalesce(cp.tps_limit, 50)
		FROM customer_api_keys k
		JOIN organizations o ON o.id = k.organization_id
		LEFT JOIN customer_profiles cp ON cp.organization_id = o.id
		WHERE k.key_hash = $1
	`
	var (
		keyID       uuid.UUID
		orgID       uuid.UUID
		label       string
		keyPrefix   string
		dbHash      string
		scopes      []string
		ipAllowlist []string
		keyStatus   string
		expiresAt   *time.Time
		orgStatus   string
		orgType     string
		tpsLimit    int
	)

	err := a.pool.QueryRow(ctx, query, keyHash).Scan(
		&keyID, &orgID, &label, &keyPrefix, &dbHash,
		&scopes, &ipAllowlist, &keyStatus, &expiresAt,
		&orgStatus, &orgType, &tpsLimit,
	)
	if err != nil {
		a.recordFailure(clientIP)
		return nil, ErrUnauthorized
	}

	// 5. Constant-time verification of key hash
	if subtle.ConstantTimeCompare([]byte(keyHash), []byte(dbHash)) != 1 {
		a.recordFailure(clientIP)
		return nil, ErrUnauthorized
	}

	// 6. Organization checks: only CUSTOMER organizations may send; must be active
	if orgType != "customer" {
		a.logger.Warn("edge auth rejected: non-customer org attempted public edge auth", "org_id", orgID, "type", orgType)
		return nil, ErrForbiddenSuspended
	}
	if orgStatus != "active" {
		a.logger.Warn("edge auth rejected: customer organization is suspended", "org_id", orgID, "status", orgStatus)
		return nil, ErrForbiddenSuspended
	}

	// 7. Key status and expiry checks
	if keyStatus != "active" {
		return nil, ErrUnauthorized
	}
	if expiresAt != nil && now.After(*expiresAt) {
		return nil, ErrUnauthorized
	}

	p := &Principal{
		CustomerOrgID: orgID,
		APIKeyID:      &keyID,
		Label:         label,
		KeyPrefix:     keyPrefix,
		Scopes:        scopes,
		AllowedIPs:    ipAllowlist,
		MaxTPS:        tpsLimit,
		IsActive:      true,
	}

	// 8. Cache valid principal
	a.cacheMu.Lock()
	a.cache[keyHash] = cachedPrincipal{
		principal: p,
		cachedAt:  now,
	}
	a.cacheMu.Unlock()

	// 9. IP allowlist check
	if !p.ValidateIP(clientIP) {
		return nil, ErrForbiddenIPNotAllowed
	}

	// Async update last_used_at without slowing request down
	go func(id uuid.UUID) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = a.pool.Exec(ctx, "UPDATE customer_api_keys SET last_used_at = now() WHERE id = $1", id)
	}(keyID)

	return p, nil
}

func (a *EdgeAuthenticator) isLockedOut(identifier string) bool {
	a.failMu.Lock()
	defer a.failMu.Unlock()
	rec, exists := a.failures[identifier]
	if !exists {
		return false
	}
	if time.Now().Before(rec.lockoutUntl) {
		return true
	}
	// Lockout expired: reset
	delete(a.failures, identifier)
	return false
}

func (a *EdgeAuthenticator) recordFailure(identifier string) {
	a.failMu.Lock()
	defer a.failMu.Unlock()
	rec, exists := a.failures[identifier]
	if !exists {
		rec = &failureRecord{count: 1}
		a.failures[identifier] = rec
		return
	}
	rec.count++
	if rec.count >= 10 {
		// 10 failures -> 5-minute lockout
		rec.lockoutUntl = time.Now().Add(5 * time.Minute)
	} else if rec.count >= 5 {
		// 5 failures -> 30-second backoff
		rec.lockoutUntl = time.Now().Add(30 * time.Second)
	}
}

// ExtractClientIP resolves the peer IP address from RemoteAddr, or from X-Forwarded-For
// ONLY if the peer socket is in the trusted proxy allowlist.
func (a *EdgeAuthenticator) ExtractClientIP(remoteAddr string, xForwardedFor string) string {
	remoteIP := remoteAddr
	if idx := strings.LastIndex(remoteAddr, ":"); idx != -1 {
		remoteIP = remoteAddr[:idx]
	}
	remoteIP = strings.Trim(remoteIP, "[]")

	// If remote is not in trusted proxies, socket IP is authoritative
	if !a.isTrustedProxy(remoteIP) || xForwardedFor == "" {
		return remoteIP
	}

	// Trust leftmost public IP in X-Forwarded-For
	parts := strings.Split(xForwardedFor, ",")
	clientIP := strings.TrimSpace(parts[0])
	if clientIP != "" {
		return clientIP
	}
	return remoteIP
}

func (a *EdgeAuthenticator) isTrustedProxy(ip string) bool {
	for _, trusted := range a.trustedIP {
		if strings.TrimSpace(trusted) == ip {
			return true
		}
	}
	return false
}
