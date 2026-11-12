package notification

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

type userRateEntry struct {
	timestamps []time.Time
}

// StormSuppressor protects users and systems from notification cascades.
type StormSuppressor struct {
	mu           sync.Mutex
	dedupWindow  time.Duration
	rateWindow   time.Duration
	rateMaxCount int
	dedupCache   map[string]time.Time
	userRates    map[uuid.UUID]*userRateEntry
}

func NewStormSuppressor(dedupWindow, rateWindow time.Duration, rateMaxCount int) *StormSuppressor {
	if dedupWindow <= 0 {
		dedupWindow = 5 * time.Minute
	}
	if rateWindow <= 0 {
		rateWindow = 1 * time.Minute
	}
	if rateMaxCount <= 0 {
		rateMaxCount = 50
	}
	return &StormSuppressor{
		dedupWindow:  dedupWindow,
		rateWindow:   rateWindow,
		rateMaxCount: rateMaxCount,
		dedupCache:   make(map[string]time.Time),
		userRates:    make(map[uuid.UUID]*userRateEntry),
	}
}

// CheckAndRecord tests whether a notification event is allowed through.
// Returns allowed=true if it may be dispatched, or allowed=false and the suppression reason.
func (s *StormSuppressor) CheckAndRecord(userID *uuid.UUID, dedupKey string) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	s.cleanupLocked(now)

	// 1. Dedup check (5-minute window)
	if dedupKey != "" {
		if lastSeen, found := s.dedupCache[dedupKey]; found {
			if now.Sub(lastSeen) < s.dedupWindow {
				return false, fmt.Sprintf("deduplicated (seen %s ago)", now.Sub(lastSeen).Round(time.Second))
			}
		}
		s.dedupCache[dedupKey] = now
	}

	// 2. Per-user rate check (max 50/min)
	if userID != nil && *userID != uuid.Nil {
		entry, exists := s.userRates[*userID]
		if !exists {
			entry = &userRateEntry{}
			s.userRates[*userID] = entry
		}

		// Prune older than rateWindow
		cutoff := now.Add(-s.rateWindow)
		valid := make([]time.Time, 0, len(entry.timestamps))
		for _, t := range entry.timestamps {
			if t.After(cutoff) {
				valid = append(valid, t)
			}
		}
		entry.timestamps = valid

		if len(entry.timestamps) >= s.rateMaxCount {
			return false, fmt.Sprintf("rate limited (%d in last %s)", len(entry.timestamps), s.rateWindow)
		}

		entry.timestamps = append(entry.timestamps, now)
	}

	return true, ""
}

// Reset clears the suppression state.
func (s *StormSuppressor) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dedupCache = make(map[string]time.Time)
	s.userRates = make(map[uuid.UUID]*userRateEntry)
}

func (s *StormSuppressor) cleanupLocked(now time.Time) {
	// Prune dedup entries older than dedupWindow
	dedupCutoff := now.Add(-s.dedupWindow)
	for k, t := range s.dedupCache {
		if t.Before(dedupCutoff) {
			delete(s.dedupCache, k)
		}
	}

	// Prune rate entries older than rateWindow
	rateCutoff := now.Add(-s.rateWindow)
	for uid, entry := range s.userRates {
		valid := make([]time.Time, 0, len(entry.timestamps))
		for _, t := range entry.timestamps {
			if t.After(rateCutoff) {
				valid = append(valid, t)
			}
		}
		if len(valid) == 0 {
			delete(s.userRates, uid)
		} else {
			entry.timestamps = valid
		}
	}
}
