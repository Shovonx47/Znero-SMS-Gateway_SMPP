// Package kafkaclient is a small wrapper around franz-go providing a producer, a consumer-group
// consumer and a connectivity check. All network operations take a context and are bounded by
// timeouts; franz-go reconnects with backoff on its own, so a broker outage never crashes the caller.
package kafkaclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// Config holds connection settings shared by producers and consumers.
type Config struct {
	Brokers        []string
	ClientID       string
	DialTimeout    time.Duration
	RequestTimeout time.Duration
}

// Message is a record received by a Consumer.
type Message struct {
	Topic     string
	Partition int32
	Offset    int64
	Key       []byte
	Value     []byte
	Timestamp time.Time
}

// reconnectBackoff is the retry delay for failed requests/connections: 250ms doubling up to 5s.
func reconnectBackoff(tries int) time.Duration {
	d := 250 * time.Millisecond
	for i := 1; i < tries && d < 5*time.Second; i++ {
		d *= 2
	}
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	return d
}

// slogAdapter forwards franz-go's internal log lines (warn and error only) to slog.
type slogAdapter struct{ l *slog.Logger }

func (a slogAdapter) Level() kgo.LogLevel { return kgo.LogLevelWarn }

func (a slogAdapter) Log(level kgo.LogLevel, msg string, keyvals ...any) {
	lvl := slog.LevelWarn
	if level == kgo.LogLevelError {
		lvl = slog.LevelError
	}
	a.l.Log(context.Background(), lvl, "kafka client: "+msg, keyvals...)
}

func baseOpts(cfg Config, logger *slog.Logger) []kgo.Opt {
	// franz-go refuses delivery timeouts below 1s.
	if cfg.RequestTimeout < time.Second {
		cfg.RequestTimeout = time.Second
	}
	return []kgo.Opt{
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ClientID(cfg.ClientID),
		kgo.DialTimeout(cfg.DialTimeout),
		kgo.RetryBackoffFn(reconnectBackoff),
		kgo.RetryTimeout(cfg.RequestTimeout),
		kgo.RecordDeliveryTimeout(cfg.RequestTimeout),
		kgo.ProduceRequestTimeout(cfg.RequestTimeout),
		kgo.RequestRetries(3),
		kgo.WithLogger(slogAdapter{l: logger}),
	}
}

// Client is a producer/admin client. Creating it does not contact the brokers, so it is safe to
// create while Kafka is down.
type Client struct {
	cl *kgo.Client
}

// New creates a Client.
func New(cfg Config, logger *slog.Logger) (*Client, error) {
	cl, err := kgo.NewClient(baseOpts(cfg, logger)...)
	if err != nil {
		return nil, fmt.Errorf("create kafka client: %w", err)
	}
	return &Client{cl: cl}, nil
}

// Close releases the client's connections.
func (c *Client) Close() { c.cl.Close() }

// Ping succeeds when at least one broker answers. The caller's context bounds the wait.
func (c *Client) Ping(ctx context.Context) error {
	return c.cl.Ping(ctx)
}

// Publish synchronously produces one record and returns once the broker acknowledged it, the
// context ends, or the delivery timeout elapses.
func (c *Client) Publish(ctx context.Context, topic string, key, value []byte) error {
	rec := &kgo.Record{Topic: topic, Key: key, Value: value}
	if err := c.cl.ProduceSync(ctx, rec).FirstErr(); err != nil {
		return fmt.Errorf("publish to %q: %w", topic, err)
	}
	return nil
}

// EnsureTopic creates the topic if it does not exist yet (idempotent).
func (c *Client) EnsureTopic(ctx context.Context, topic string, partitions int32, replicas int16) error {
	req := kmsg.NewPtrCreateTopicsRequest()
	t := kmsg.NewCreateTopicsRequestTopic()
	t.Topic = topic
	t.NumPartitions = partitions
	t.ReplicationFactor = replicas
	req.Topics = append(req.Topics, t)
	req.TimeoutMillis = 10000

	resp, err := req.RequestWith(ctx, c.cl)
	if err != nil {
		return fmt.Errorf("create topic %q: %w", topic, err)
	}
	for _, rt := range resp.Topics {
		if rt.ErrorCode == 0 {
			continue
		}
		topicErr := kerr.ErrorForCode(rt.ErrorCode)
		if errors.Is(topicErr, kerr.TopicAlreadyExists) {
			continue
		}
		return fmt.Errorf("create topic %q: %w", topic, topicErr)
	}
	return nil
}

// DeleteTopic deletes the named Kafka topic. If the topic does not exist the call succeeds.
func (c *Client) DeleteTopic(ctx context.Context, topic string) error {
	req := kmsg.NewPtrDeleteTopicsRequest()
	req.TopicNames = []string{topic}
	req.TimeoutMillis = 10000

	resp, err := req.RequestWith(ctx, c.cl)
	if err != nil {
		return fmt.Errorf("delete topic %q: %w", topic, err)
	}
	for _, rt := range resp.Topics {
		if rt.ErrorCode == 0 {
			continue
		}
		topicErr := kerr.ErrorForCode(rt.ErrorCode)
		if errors.Is(topicErr, kerr.UnknownTopicOrPartition) {
			continue // already gone
		}
		return fmt.Errorf("delete topic %q: %w", topic, topicErr)
	}
	return nil
}

// Handler processes one received message. A returned error is logged; the message is still
// committed (there is no dead-letter queue yet).
type Handler func(ctx context.Context, m Message) error

// Consumer reads a topic as part of a consumer group and passes every record to a Handler.
type Consumer struct {
	cl      *kgo.Client
	topic   string
	handler Handler
	logger  *slog.Logger

	mu          sync.Mutex
	inError     bool
	lastErrorAt time.Time
}

// NewConsumer creates a consumer. It does not contact the brokers; it keeps retrying in Run.
func NewConsumer(cfg Config, group, topic string, handler Handler, logger *slog.Logger) (*Consumer, error) {
	opts := append(baseOpts(cfg, logger),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.AutoCommitMarks(),
		kgo.FetchMaxWait(2*time.Second),
	)
	cl, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("create kafka consumer: %w", err)
	}
	return &Consumer{cl: cl, topic: topic, handler: handler, logger: logger}, nil
}

// errorLogInterval limits how often a persistent outage is logged.
const errorLogInterval = 30 * time.Second

// Run blocks, delivering records to the handler, until ctx is cancelled. Broker outages are
// logged (rate-limited) and retried; they never terminate Run.
func (c *Consumer) Run(ctx context.Context) {
	c.logger.InfoContext(ctx, "kafka consumer started", slog.String("topic", c.topic))
	defer c.logger.Info("kafka consumer stopped", slog.String("topic", c.topic))

	for ctx.Err() == nil {
		fetches := c.cl.PollFetches(ctx)
		if fetches.IsClientClosed() || ctx.Err() != nil {
			return
		}

		hadError := false
		fetches.EachError(func(topic string, partition int32, err error) {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			hadError = true
			c.noteError(ctx, topic, partition, err)
		})
		if !hadError {
			c.noteRecovered(ctx)
		}

		fetches.EachRecord(func(r *kgo.Record) {
			msg := Message{Topic: r.Topic, Partition: r.Partition, Offset: r.Offset, Key: r.Key, Value: r.Value, Timestamp: r.Timestamp}
			if err := c.handler(ctx, msg); err != nil {
				c.logger.ErrorContext(ctx, "kafka message handler failed",
					slog.String("topic", r.Topic), slog.Int("partition", int(r.Partition)),
					slog.Int64("offset", r.Offset), slog.String("error", err.Error()))
			}
			c.cl.MarkCommitRecords(r)
		})

		if hadError {
			// Avoid a hot loop if the client returns errors immediately.
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

func (c *Consumer) noteError(ctx context.Context, topic string, partition int32, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inError = true
	if time.Since(c.lastErrorAt) < errorLogInterval {
		return
	}
	c.lastErrorAt = time.Now()
	c.logger.WarnContext(ctx, "kafka consumer cannot fetch (will keep retrying)",
		slog.String("topic", topic), slog.Int("partition", int(partition)), slog.String("error", err.Error()))
}

func (c *Consumer) noteRecovered(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inError {
		c.inError = false
		c.lastErrorAt = time.Time{}
		c.logger.InfoContext(ctx, "kafka consumer recovered", slog.String("topic", c.topic))
	}
}

// Close leaves the consumer group (committing marked offsets) and closes connections.
func (c *Consumer) Close() { c.cl.Close() }
