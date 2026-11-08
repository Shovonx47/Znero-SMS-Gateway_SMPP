package messaging

import (
	"fmt"
	"time"
)

// Config defines configurable timing parameters, constraints, and partitions for the messaging engine.
type Config struct {
	// MessageTTL is the maximum lifetime of an uncompleted message before expiration.
	MessageTTL time.Duration

	// UnknownResolutionWindow is the period an ambiguous message remains in 'unknown' awaiting late resolution.
	UnknownResolutionWindow time.Duration

	// HoldExpiry is the lifetime of a customer balance reservation before ledger expiry.
	HoldExpiry time.Duration

	// MaxSegmentsLimit is the maximum number of multi-part segments allowed per submission.
	MaxSegmentsLimit int

	// PartitionPrecreateMonths is the number of upcoming monthly partitions to keep pre-created.
	PartitionPrecreateMonths int
}

// DefaultConfig provides the production default parameters.
func DefaultConfig() Config {
	return Config{
		MessageTTL:               24 * time.Hour,
		UnknownResolutionWindow: 24 * time.Hour,
		HoldExpiry:               72 * time.Hour,
		MaxSegmentsLimit:         10,
		PartitionPrecreateMonths: 3,
	}
}

// Validate enforces all startup invariants, strictly refusing invalid configurations.
func (c Config) Validate() error {
	if c.MessageTTL <= 0 {
		return fmt.Errorf("message_ttl must be positive, got %v", c.MessageTTL)
	}
	if c.UnknownResolutionWindow <= 0 {
		return fmt.Errorf("unknown_resolution_window must be positive, got %v", c.UnknownResolutionWindow)
	}
	if c.HoldExpiry <= 0 {
		return fmt.Errorf("hold_expiry must be positive, got %v", c.HoldExpiry)
	}
	if c.MaxSegmentsLimit <= 0 {
		return fmt.Errorf("max_segments_limit must be positive, got %d", c.MaxSegmentsLimit)
	}

	// Architectural Invariant (Amendment 4):
	// hold_expiry MUST be strictly greater than message_ttl + unknown_resolution_window
	minimumRequiredHold := c.MessageTTL + c.UnknownResolutionWindow
	if c.HoldExpiry <= minimumRequiredHold {
		return fmt.Errorf(
			"invalid hold configuration: hold_expiry (%v) must exceed message_ttl (%v) + unknown_resolution_window (%v) (minimum required: > %v)",
			c.HoldExpiry, c.MessageTTL, c.UnknownResolutionWindow, minimumRequiredHold,
		)
	}

	return nil
}

