package messaging

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// SendStatus classifies the outcome returned by an upstream carrier protocol adapter.
type SendStatus string

const (
	SendStatusAccepted        SendStatus = "accepted"
	SendStatusRetryableReject SendStatus = "retryable_reject"
	SendStatusFatalReject     SendStatus = "fatal_reject"
	SendStatusUnknown         SendStatus = "unknown"
)

// SendAttempt contains the parameters supplied to an egress sender.
type SendAttempt struct {
	MessageID      uuid.UUID
	ConnectionID   uuid.UUID
	Destination    string
	SenderID       string
	SenderIDType   string
	Text           string
	UDH            []byte
	PartNumber     int
	TotalParts     int
	Encoding       string
	TrafficProfile string
	Timeout        time.Duration
}

// SendResult encapsulates the upstream response details.
type SendResult struct {
	Status            SendStatus
	ProviderMessageID string
	ErrorCode         string
	ErrorMessage      string
	BuyCost           decimal.Decimal
	Latency           time.Duration
}

// Sender is the upstream carrier transport interface (implemented by FakeSender in Chunk 1 and SMPP in Chunk 2).
type Sender interface {
	Send(ctx context.Context, attempt SendAttempt) (SendResult, error)
}

// FakeSenderOutcomeConfig defines mock behavior for a particular connection or default.
type FakeSenderOutcomeConfig struct {
	Status            SendStatus
	ProviderMessageID string
	ErrorCode         string
	ErrorMessage      string
	BuyCost           decimal.Decimal
	Latency           time.Duration
	SimulateError     error
}

// FakeSender provides a deterministic, thread-safe mock carrier client for testing and development.
type FakeSender struct {
	mu             sync.RWMutex
	defaultOutcome FakeSenderOutcomeConfig
	routeOutcomes  map[uuid.UUID]FakeSenderOutcomeConfig // keyed by ConnectionID
	destOutcomes   map[string]FakeSenderOutcomeConfig    // keyed by Destination
	attempts       []SendAttempt
}

// NewFakeSender creates a FakeSender with default success outcome.
func NewFakeSender() *FakeSender {
	return &FakeSender{
		defaultOutcome: FakeSenderOutcomeConfig{
			Status:   SendStatusAccepted,
			BuyCost:  decimal.NewFromFloat(0.0020),
			Latency:  5 * time.Millisecond,
		},
		routeOutcomes: make(map[uuid.UUID]FakeSenderOutcomeConfig),
		destOutcomes:  make(map[string]FakeSenderOutcomeConfig),
		attempts:      make([]SendAttempt, 0),
	}
}

// ConfigureConnection sets an explicit outcome for a specific provider connection.
func (f *FakeSender) ConfigureConnection(connID uuid.UUID, outcome FakeSenderOutcomeConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routeOutcomes[connID] = outcome
}

// ConfigureDestination sets an explicit outcome for a specific destination number.
func (f *FakeSender) ConfigureDestination(dest string, outcome FakeSenderOutcomeConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destOutcomes[dest] = outcome
}

// SetDefaultOutcome updates the fallback outcome for unspecified connections.
func (f *FakeSender) SetDefaultOutcome(outcome FakeSenderOutcomeConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.defaultOutcome = outcome
}

// GetRecordedAttempts returns a snapshot copy of all attempts processed by the fake sender.
func (f *FakeSender) GetRecordedAttempts() []SendAttempt {
	f.mu.RLock()
	defer f.mu.RUnlock()
	res := make([]SendAttempt, len(f.attempts))
	copy(res, f.attempts)
	return res
}

// ClearAttempts resets the recorded attempts log.
func (f *FakeSender) ClearAttempts() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts = make([]SendAttempt, 0)
}

// Send simulates upstream transmission, respecting configured outcomes and latencies.
func (f *FakeSender) Send(ctx context.Context, attempt SendAttempt) (SendResult, error) {
	f.mu.Lock()
	f.attempts = append(f.attempts, attempt)
	outcome := f.defaultOutcome
	if destCfg, ok := f.destOutcomes[attempt.Destination]; ok {
		outcome = destCfg
	} else if routeCfg, ok := f.routeOutcomes[attempt.ConnectionID]; ok {
		outcome = routeCfg
	}
	f.mu.Unlock()

	if outcome.Latency > 0 {
		select {
		case <-time.After(outcome.Latency):
		case <-ctx.Done():
			return SendResult{
				Status:       SendStatusUnknown,
				ErrorCode:    "TIMEOUT",
				ErrorMessage: "context deadline exceeded waiting for carrier response",
				Latency:      outcome.Latency,
			}, ctx.Err()
		}
	}

	if outcome.SimulateError != nil {
		return SendResult{
			Status:       SendStatusUnknown,
			ErrorCode:    "IO_ERROR",
			ErrorMessage: outcome.SimulateError.Error(),
		}, outcome.SimulateError
	}

	pMsgID := outcome.ProviderMessageID
	if pMsgID == "" && outcome.Status == SendStatusAccepted {
		pMsgID = fmt.Sprintf("smsc_%s", uuid.NewString()[:12])
	}

	return SendResult{
		Status:            outcome.Status,
		ProviderMessageID: pMsgID,
		ErrorCode:         outcome.ErrorCode,
		ErrorMessage:      outcome.ErrorMessage,
		BuyCost:           outcome.BuyCost,
		Latency:           outcome.Latency,
	}, nil
}

