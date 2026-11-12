package customeredge

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// CustomerLimiter tracks token-bucket rate limits per customer organization.
// NOTE: Token buckets are tracked in-process per application node. In multi-server clustered
// deployments, a centralized high-throughput store (e.g. Redis) is required to aggregate
// cluster-wide limits.
type CustomerLimiter struct {
	mu       sync.Mutex
	limiters map[uuid.UUID]*tokenBucket
}

type tokenBucket struct {
	mu         sync.Mutex
	rate       float64
	capacity   float64
	tokens     float64
	lastRefill time.Time
}

// NewCustomerLimiter initializes the edge rate limiter registry.
func NewCustomerLimiter() *CustomerLimiter {
	return &CustomerLimiter{
		limiters: make(map[uuid.UUID]*tokenBucket),
	}
}

// AllowN checks whether the customer has enough tokens for N submissions.
// If allowed, tokens are deducted and true is returned.
// If rejected, false is returned along with suggested Retry-After seconds.
func (cl *CustomerLimiter) AllowN(customerOrgID uuid.UUID, maxTPS int, count int) (bool, int) {
	if maxTPS <= 0 {
		maxTPS = 10 // Default fallback TPS
	}

	cl.mu.Lock()
	tb, exists := cl.limiters[customerOrgID]
	if !exists {
		tb = &tokenBucket{
			rate:       float64(maxTPS),
			capacity:   float64(maxTPS),
			tokens:     float64(maxTPS),
			lastRefill: time.Now(),
		}
		cl.limiters[customerOrgID] = tb
	}
	cl.mu.Unlock()

	tb.mu.Lock()
	defer tb.mu.Unlock()

	newLimit := float64(maxTPS)
	if tb.rate != newLimit || tb.capacity != newLimit {
		tb.rate = newLimit
		tb.capacity = newLimit
		if tb.tokens > tb.capacity {
			tb.tokens = tb.capacity
		}
	}

	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tb.tokens += elapsed * tb.rate
	if tb.tokens > tb.capacity {
		tb.tokens = tb.capacity
	}
	tb.lastRefill = now

	needed := float64(count)
	if tb.tokens >= needed {
		tb.tokens -= needed
		return true, 0
	}

	// Calculate wait time
	missing := needed - tb.tokens
	waitSec := int(missing/tb.rate) + 1
	if waitSec < 1 {
		waitSec = 1
	}
	return false, waitSec
}
