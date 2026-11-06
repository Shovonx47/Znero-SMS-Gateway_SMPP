// Package diagnostics provides development-only Kafka diagnostics: publishing a test message and
// a consumer handler that logs what it receives. It owns no database tables, so it has no
// repository layer; the Kafka publisher is injected through the Publisher interface.
package diagnostics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/znero-international/znero/backend/pkg/kafkaclient"
)

const (
	maxMessageRunes = 200
	maxLoggedBytes  = 512
)

// ErrPublishFailed is returned when the message could not be delivered to Kafka.
var ErrPublishFailed = errors.New("could not publish to kafka")

// ValidationError describes why the supplied message was rejected (safe to show to the client).
type ValidationError struct{ Reason string }

func (e *ValidationError) Error() string { return e.Reason }

// Publisher is implemented by *kafkaclient.Client.
type Publisher interface {
	Publish(ctx context.Context, topic string, key, value []byte) error
}

// TestMessage is the JSON document written to the test topic.
type TestMessage struct {
	ID      string    `json:"id"`
	Message string    `json:"message"`
	SentAt  time.Time `json:"sent_at"`
}

type Service struct {
	pub     Publisher
	topic   string
	timeout time.Duration
	logger  *slog.Logger
	now     func() time.Time
}

func NewService(pub Publisher, topic string, timeout time.Duration, logger *slog.Logger) *Service {
	return &Service{pub: pub, topic: topic, timeout: timeout, logger: logger, now: time.Now}
}

// Topic returns the test topic name.
func (s *Service) Topic() string { return s.topic }

// validateMessage allow-lists printable text of 1..200 characters.
func validateMessage(msg string) error {
	n := utf8.RuneCountInString(msg)
	if n == 0 {
		return &ValidationError{Reason: "message must not be empty"}
	}
	if n > maxMessageRunes {
		return &ValidationError{Reason: fmt.Sprintf("message must be at most %d characters", maxMessageRunes)}
	}
	if !utf8.ValidString(msg) {
		return &ValidationError{Reason: "message must be valid UTF-8"}
	}
	for _, r := range msg {
		if unicode.IsControl(r) {
			return &ValidationError{Reason: "message must not contain control characters"}
		}
	}
	return nil
}

func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// PublishTestMessage validates msg and publishes it to the test topic.
func (s *Service) PublishTestMessage(ctx context.Context, msg string) (TestMessage, error) {
	if err := validateMessage(msg); err != nil {
		return TestMessage{}, err
	}
	id, err := newID()
	if err != nil {
		return TestMessage{}, fmt.Errorf("generate id: %w", err)
	}
	tm := TestMessage{ID: id, Message: msg, SentAt: s.now().UTC()}
	payload, err := json.Marshal(tm)
	if err != nil {
		return TestMessage{}, fmt.Errorf("encode message: %w", err)
	}

	pctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := s.pub.Publish(pctx, s.topic, []byte(tm.ID), payload); err != nil {
		s.logger.WarnContext(ctx, "test message publish failed", slog.String("topic", s.topic), slog.String("error", err.Error()))
		return TestMessage{}, ErrPublishFailed
	}
	s.logger.InfoContext(ctx, "test message published", slog.String("topic", s.topic), slog.String("id", tm.ID))
	return tm, nil
}

// LogMessage is the consumer handler: it logs every received record. The payload is truncated and
// logged as a structured attribute (never interpolated into the message), which prevents log injection.
func (s *Service) LogMessage(ctx context.Context, m kafkaclient.Message) error {
	value := m.Value
	truncated := false
	if len(value) > maxLoggedBytes {
		value = value[:maxLoggedBytes]
		truncated = true
	}
	s.logger.InfoContext(ctx, "kafka message received",
		slog.String("topic", m.Topic),
		slog.Int("partition", int(m.Partition)),
		slog.Int64("offset", m.Offset),
		slog.String("key", string(m.Key)),
		slog.String("value", string(value)),
		slog.Bool("truncated", truncated),
	)
	return nil
}
