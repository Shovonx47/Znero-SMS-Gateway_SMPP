package smppserver

import (
	"sync"
	"time"
)

// bindFailureRecord tracks failed bind attempts for a single IP+system_id key.
type bindFailureRecord struct {
	count       int
	lockoutUntl time.Time
}

// BindLimiter tracks per-(IP, system_id) SMPP bind failures and enforces lockouts
// to prevent brute-force password attacks while isolating each credential prefix so
// an attacker cannot lock out a legitimate customer from a different IP.
type BindLimiter struct {
	mu       sync.Mutex
	failures map[string]*bindFailureRecord
}

// NewBindLimiter returns a new BindLimiter.
func NewBindLimiter() *BindLimiter {
	return &BindLimiter{
		failures: make(map[string]*bindFailureRecord),
	}
}

// key computes the lockout key: IP + "|" + system_id.
func (l *BindLimiter) key(remoteIP, systemID string) string {
	return remoteIP + "|" + systemID
}

// IsLockedOut returns true if the IP+system_id pair is currently locked out.
func (l *BindLimiter) IsLockedOut(remoteIP, systemID string) bool {
	k := l.key(remoteIP, systemID)
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, exists := l.failures[k]
	if !exists || rec.lockoutUntl.IsZero() {
		return false
	}
	if time.Now().Before(rec.lockoutUntl) {
		return true
	}
	// If the 5-minute (>=10 failures) lockout expired, clear completely; after the 30s (5-failure)
	// backoff, keep count so continued brute-force attempts escalate to 5 minutes at 10 failures.
	if rec.count >= 10 {
		delete(l.failures, k)
	} else {
		rec.lockoutUntl = time.Time{}
	}
	return false
}

// RecordFailure increments the failure counter for the IP+system_id pair.
// Thresholds: 5 failures → 30s backoff; 10 failures → 5-minute lockout.
func (l *BindLimiter) RecordFailure(remoteIP, systemID string) {
	k := l.key(remoteIP, systemID)
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, exists := l.failures[k]
	if !exists {
		rec = &bindFailureRecord{count: 1}
		l.failures[k] = rec
		return
	}
	rec.count++
	switch {
	case rec.count >= 10:
		rec.lockoutUntl = time.Now().Add(5 * time.Minute)
	case rec.count >= 5:
		rec.lockoutUntl = time.Now().Add(30 * time.Second)
	}
}

// RecordSuccess resets the failure counter for the IP+system_id pair on a successful bind.
func (l *BindLimiter) RecordSuccess(remoteIP, systemID string) {
	k := l.key(remoteIP, systemID)
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, k)
}
