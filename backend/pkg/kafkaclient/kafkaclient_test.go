package kafkaclient_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/znero-international/znero/backend/pkg/kafkaclient"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Kafka being down must never panic or hang the caller: operations fail fast with an error.
func TestOperationsFailFastWhenKafkaIsDown(t *testing.T) {
	cfg := kafkaclient.Config{
		Brokers:        []string{"127.0.0.1:1"}, // nothing listens on port 1
		ClientID:       "test",
		DialTimeout:    300 * time.Millisecond,
		RequestTimeout: 800 * time.Millisecond,
	}
	c, err := kafkaclient.New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("creating a client must not require a reachable broker: %v", err)
	}
	defer c.Close()

	t.Run("ping", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		start := time.Now()
		if err := c.Ping(ctx); err == nil {
			t.Fatal("Ping should fail when no broker is reachable")
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("Ping took %v, expected to honour the 1s context", d)
		}
	})

	t.Run("publish", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		start := time.Now()
		if err := c.Publish(ctx, "t", []byte("k"), []byte("v")); err == nil {
			t.Fatal("Publish should fail when no broker is reachable")
		}
		if d := time.Since(start); d > 4*time.Second {
			t.Errorf("Publish took %v", d)
		}
	})
}

func TestConsumerRunKeepsRetryingAndStopsOnCancel(t *testing.T) {
	cfg := kafkaclient.Config{
		Brokers: []string{"127.0.0.1:1"}, ClientID: "test",
		DialTimeout: 200 * time.Millisecond, RequestTimeout: 500 * time.Millisecond,
	}
	c, err := kafkaclient.NewConsumer(cfg, "g", "t", func(context.Context, kafkaclient.Message) error { return nil }, discardLogger())
	if err != nil {
		t.Fatalf("NewConsumer must not need a reachable broker: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()

	select {
	case <-done:
		t.Fatal("Run must keep running while Kafka is down, it exited early")
	case <-time.After(1500 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after the context was cancelled")
	}
}

// Integration test against a real broker. Run with ZNERO_INTEGRATION=1 and Kafka running
// (scripts/test.ps1 -Integration does this).
func TestIntegrationPublishAndConsumeRoundTrip(t *testing.T) {
	if os.Getenv("ZNERO_INTEGRATION") != "1" {
		t.Skip("set ZNERO_INTEGRATION=1 to run against a real Kafka broker")
	}
	broker := os.Getenv("KAFKA_BROKERS")
	if broker == "" || strings.HasPrefix(broker, "localhost:") {
		broker = strings.Replace(broker, "localhost:", "127.0.0.1:", 1)
		if broker == "" {
			broker = "127.0.0.1:9092"
		}
	}
	cfg := kafkaclient.Config{
		Brokers: []string{broker}, ClientID: "znero-it",
		DialTimeout: 3 * time.Second, RequestTimeout: 10 * time.Second,
	}
	topic := fmt.Sprintf("znero.it.%d", time.Now().UnixNano())

	prod, err := kafkaclient.New(cfg, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer prod.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := prod.Ping(ctx); err != nil {
		t.Fatalf("Kafka not reachable, failing integration test: %v", err)
	}
	if err := prod.EnsureTopic(ctx, topic, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := prod.EnsureTopic(ctx, topic, 1, 1); err != nil {
		t.Fatalf("EnsureTopic must be idempotent: %v", err)
	}

	var mu sync.Mutex
	var got []kafkaclient.Message
	received := make(chan struct{}, 1)
	cons, err := kafkaclient.NewConsumer(cfg, "znero-it-"+topic, topic, func(_ context.Context, m kafkaclient.Message) error {
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
		select {
		case received <- struct{}{}:
		default:
		}
		return nil
	}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { cons.Run(runCtx); close(done) }()
	defer func() { stop(); <-done; cons.Close() }()

	if err := prod.Publish(ctx, topic, []byte("key-1"), []byte("round-trip-payload")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
	case <-ctx.Done():
		t.Fatal("message was not consumed in time")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || string(got[0].Value) != "round-trip-payload" || string(got[0].Key) != "key-1" || got[0].Topic != topic {
		t.Errorf("unexpected messages: %+v", got)
	}
}
