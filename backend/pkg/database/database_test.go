package database_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/znero-international/znero/backend/pkg/database"
)

func TestConnectErrorNeverContainsPassword(t *testing.T) {
	_, err := database.Connect(context.Background(), database.Config{
		URL:            "postgres://someone:SuperSecret123@127.0.0.1:1/nodb?sslmode=disable",
		MaxConns:       2,
		ConnectTimeout: 500 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if strings.Contains(err.Error(), "SuperSecret123") {
		t.Errorf("error leaks the password: %v", err)
	}
}

func TestConnectRejectsGarbageURL(t *testing.T) {
	_, err := database.Connect(context.Background(), database.Config{URL: "%%%not a url", MaxConns: 1, ConnectTimeout: time.Second})
	if err == nil {
		t.Fatal("expected an error")
	}
}

// Integration test against the real znero_dev database (needs migrations applied).
func TestIntegrationPoolRespectsContext(t *testing.T) {
	if os.Getenv("ZNERO_INTEGRATION") != "1" {
		t.Skip("set ZNERO_INTEGRATION=1 to run against the real PostgreSQL")
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Fatal("DATABASE_URL must be set (scripts/test.ps1 -Integration loads .env)")
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, database.Config{
		URL: url, MaxConns: 3, MinConns: 0,
		MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute,
		HealthCheckPeriod: time.Minute, ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	var user string
	var super bool
	if err := pool.QueryRow(ctx, `SELECT current_user, (SELECT rolsuper FROM pg_roles WHERE rolname = current_user)`).Scan(&user, &super); err != nil {
		t.Fatal(err)
	}
	if super {
		t.Errorf("the application must not connect as a superuser (connected as %s)", user)
	}

	// Context propagation: a cancelled/expired context must abort a running query.
	tctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = pool.Exec(tctx, `SELECT pg_sleep(5)`)
	if err == nil {
		t.Fatal("pg_sleep(5) should have been cancelled by the 200ms context")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("query was not cancelled promptly (%v)", d)
	}
}
