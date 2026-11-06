// Command kafkatest publishes a test message to the Kafka test topic (development helper).
//
//	go run ./cmd/kafkatest publish "hello"
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/znero-international/znero/backend/internal/config"
	"github.com/znero-international/znero/backend/internal/diagnostics"
	"github.com/znero-international/znero/backend/pkg/kafkaclient"
	"github.com/znero-international/znero/backend/pkg/logx"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 1 || args[0] != "publish" {
		return fmt.Errorf("usage: kafkatest publish [message]")
	}
	message := "hello from cmd/kafkatest"
	if len(args) > 1 {
		message = strings.Join(args[1:], " ")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.IsDevelopment() {
		return fmt.Errorf("kafkatest only runs with APP_ENV=development")
	}
	logger, err := logx.New(os.Stderr, cfg.Log.Level, cfg.Log.Format)
	if err != nil {
		return err
	}

	kc, err := kafkaclient.New(kafkaclient.Config{
		Brokers: cfg.Kafka.Brokers, ClientID: cfg.Kafka.ClientID + "-kafkatest",
		DialTimeout: cfg.Kafka.DialTimeout, RequestTimeout: cfg.Kafka.RequestTimeout,
	}, logger)
	if err != nil {
		return err
	}
	defer kc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := kc.EnsureTopic(ctx, cfg.Kafka.TestTopic, 1, 1); err != nil {
		return fmt.Errorf("kafka unavailable: %w", err)
	}
	svc := diagnostics.NewService(kc, cfg.Kafka.TestTopic, cfg.Kafka.RequestTimeout, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	tm, err := svc.PublishTestMessage(ctx, message)
	if err != nil {
		return err
	}
	fmt.Printf("published to %s: id=%s message=%q\n", cfg.Kafka.TestTopic, tm.ID, tm.Message)
	return nil
}
