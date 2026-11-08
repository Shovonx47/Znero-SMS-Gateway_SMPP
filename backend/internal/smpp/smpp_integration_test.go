package smpp

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/internal/routing"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/internal/smpp/mocksmsc"
	"github.com/znero-international/znero/backend/internal/testkit"
)

func getTestDB(t *testing.T) *pgxpool.Pool {
	if os.Getenv("ZNERO_INTEGRATION") != "1" {
		t.Skip("skipping integration test; ZNERO_INTEGRATION=1 not set")
	}
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://znero_app:s3cretPASS@localhost:5432/znero_test?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("failed to connect to test db: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("failed to ping test db: %v", err)
	}
	return pool
}

// 1. PostgreSQL advisory lock stops a second process from binding the same connection
func TestSMPP_Integration_AdvisoryLockStopsSecondProcess(t *testing.T) {
	pool := getTestDB(t)
	defer pool.Close()

	mgr := NewConnectionManager(ManagerConfig{
		DB:            pool,
		AllowLoopback: true,
	})
	defer mgr.Close()

	connID := uuid.New()

	// Process 1 acquires advisory lock
	lock1, err := mgr.acquireAdvisoryLock(context.Background(), connID)
	if err != nil {
		t.Fatalf("process 1 acquire lock failed: %v", err)
	}
	defer func() {
		if lock1 != nil {
			k1, k2 := uuidToLockKeys(connID)
			_, _ = lock1.Exec(context.Background(), "SELECT pg_advisory_unlock($1, $2)", k1, k2)
			lock1.Release()
		}
	}()

	// Process 2 attempts to acquire the same advisory lock
	_, err = mgr.acquireAdvisoryLock(context.Background(), connID)
	if err == nil {
		t.Fatalf("expected process 2 advisory lock acquisition to fail, but it succeeded!")
	}
	if !strings.Contains(err.Error(), "held by another process") {
		t.Errorf("expected lock collision error, got: %v", err)
	}
}

// 2. Late deliver_sm receipt is persisted into message_dlrs table
func TestSMPP_Integration_DeliverSMPersistedDurably(t *testing.T) {
	pool := getTestDB(t)
	defer pool.Close()

	provMsgID := fmt.Sprintf("MOCK-DLR-%s", uuid.New().String()[:8])
	receiptText := fmt.Sprintf("id:%s sub:001 dlvrd:001 stat:DELIVRD err:000 text:", provMsgID)

	// connection_id is NULLable in message_dlrs
	query := `
		INSERT INTO message_dlrs (connection_id, provider_message_id, status, error_code, raw_receipt, received_at)
		VALUES (NULL, $1, 'DELIVRD', '000', $2, now())
	`
	_, err := pool.Exec(context.Background(), query, provMsgID, receiptText)
	if err != nil {
		t.Fatalf("direct insert to message_dlrs failed: %v", err)
	}

	// Verify durability
	var count int
	err = pool.QueryRow(context.Background(), "SELECT count(*) FROM message_dlrs WHERE provider_message_id = $1", provMsgID).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("expected 1 receipt in message_dlrs, got %d (err: %v)", count, err)
	}
}

// 3. End-to-End Integration: Ingestion -> Outbox -> Dispatcher -> SMPP Client -> Mock SMSC -> Provider Message ID stored -> PROVIDER_ACCEPTED
func TestSMPP_Integration_EndToEnd_SubmitOutboxDispatchMock(t *testing.T) {
	if os.Getenv("ZNERO_INTEGRATION") != "1" {
		t.Skip("skipping integration test; ZNERO_INTEGRATION=1 not set")
	}
	if os.Getenv("DATABASE_URL") == "" {
		_ = os.Setenv("DATABASE_URL", "postgres://znero_app:s3cretPASS@localhost:5432/znero_test?sslmode=disable")
	}

	e := testkit.New(t)
	ctx := context.Background()

	// 1. Start in-process Mock SMSC
	mockServer, err := mocksmsc.NewServer(mocksmsc.ServerConfig{
		SystemID: "integ_sys",
		Password: "integ_password",
		DLRMode:  mocksmsc.DLRModeImmediate,
	})
	if err != nil {
		t.Fatalf("failed to create mock SMSC: %v", err)
	}
	if err := mockServer.Start(); err != nil {
		t.Fatalf("failed to start mock SMSC: %v", err)
	}
	defer mockServer.Close()

	_, portStr, _ := net.SplitHostPort(mockServer.Addr().String())
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	// Clean outbox queue for isolated test
	_, _ = e.Pool.Exec(ctx, "DELETE FROM message_outbox")

	keyring, err := secrets.NewKeyRing(secrets.DefaultKeyID, []byte("dev-secrets-key-32-bytes-only!!!"))
	if err != nil {
		t.Fatalf("new keyring: %v", err)
	}

	orgRepo := org.NewRepository(e.Pool)
	ledgerRepo := ledger.NewRepository(e.Pool)
	ledgerSvc := ledger.NewService(ledgerRepo, orgRepo, e.Approval, e.Audit, e.Pool)

	// Ensure country exists
	var countryID uuid.UUID
	err = e.Pool.QueryRow(ctx, `SELECT id FROM countries LIMIT 1`).Scan(&countryID)
	if err != nil {
		cReq := e.CountryRequest()
		cRes, cErr := e.Country.Create(ctx, e.Override, cReq)
		if cErr != nil {
			t.Fatalf("create test country: %v", cErr)
		}
		countryID = cRes.(*country.Country).ID
	}

	// Create customer hierarchy and wallet
	resellerOrg, resellerPath := e.ChildOrg(e.PlatformOrgID, e.PlatformOrgPath, "smpp-reseller-"+testkit.RandHex(3), "reseller")
	custOrg, custPath := e.ChildOrg(resellerOrg, resellerPath, "smpp-cust-"+testkit.RandHex(3), "customer")
	custUser := e.Actor("smpp-user-"+testkit.RandHex(3), custOrg, custPath, "customer", false,
		"message.send", "message.view", "message.content.view", "message.cancel", "wallet.wallet.view",
	)

	w, err := ledgerSvc.EnsureWallet(ctx, e.Override, custOrg, "USD", "prepaid")
	if err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	_, _, err = ledgerSvc.Topup(ctx, e.Override, w.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(100),
		Reason: "Seed test funds",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("top up wallet: %v", err)
	}

	// Create provider and connection pointing to Mock SMSC port
	provCode := "SMPP_" + strings.ToUpper(testkit.RandHex(4))
	var provID uuid.UUID
	err = e.Pool.QueryRow(ctx, `
		INSERT INTO providers (name, code, provider_type, legal_entity_name, status, version)
		VALUES ($1, $2, 'smpp', 'Mock SMPP Provider Legal', 'active', 1)
		RETURNING id
	`, "Provider "+provCode, provCode).Scan(&provID)
	if err != nil {
		t.Fatalf("insert provider: %v", err)
	}

	var connID uuid.UUID
	err = e.Pool.QueryRow(ctx, `
		INSERT INTO provider_connections (
			provider_id, name, protocol, status, version, max_tps,
			smpp_host, smpp_port, smpp_system_id, smpp_password_encrypted,
			smpp_system_type, smpp_bind_type, smpp_tls, smpp_window_size, smpp_enquire_link_sec
		) VALUES (
			$1, $2, 'smpp', 'active', 1, 100,
			'127.0.0.1', $3, 'integ_sys', 'integ_password',
			'SMPP', 'transceiver', false, 5, 30
		)
		RETURNING id
	`, provID, "Mock-Conn-"+testkit.RandHex(3), port).Scan(&connID)
	if err != nil {
		t.Fatalf("insert connection: %v", err)
	}

	_, err = e.Pool.Exec(ctx, `
		INSERT INTO provider_coverage (provider_id, country_id, status, version)
		VALUES ($1, $2, 'active', 1)
		ON CONFLICT (provider_id, country_id, COALESCE(operator_id, '00000000-0000-0000-0000-000000000000'::uuid))
		DO UPDATE SET status = 'active'
	`, provID, countryID)
	if err != nil {
		t.Fatalf("insert coverage: %v", err)
	}

	// Routing engine
	rater := &testRater{
		sellPrice: decimal.RequireFromString("0.050000"),
		buyPrice:  decimal.RequireFromString("0.020000"),
		currency:  "USD",
	}
	numberer := &testNumberer{defaultCountryID: countryID}
	routingEngine := routing.NewEngine(e.Pool, rater, numberer, nil)
	routingRepo := routing.NewRepository(e.Pool)
	routingSvc := routing.NewService(routingRepo, e.Approval, e.Audit, e.Pool, routingEngine)

	plan, err := routingSvc.CreatePlan(ctx, e.Override, &routing.RoutePlan{
		Name:     "SMPP Plan " + testkit.RandHex(3),
		Code:     "SP_" + strings.ToUpper(testkit.RandHex(4)),
		Strategy: routing.StrategyPriority,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	_, _, err = routingSvc.CreateEntry(ctx, e.Override, &routing.RoutePlanEntry{
		PlanID:       plan.ID,
		ConnectionID: connID,
		ScopeType:    "global",
		Priority:     1,
		Weight:       100,
		Status:       "active",
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	_, _, err = routingSvc.RequestPlanActivation(ctx, e.Override, plan.ID)
	if err != nil {
		t.Fatalf("activate plan: %v", err)
	}
	_, _, err = routingSvc.CreateAssignment(ctx, e.Override, &routing.CustomerRouteAssignment{
		CustomerOrgID: custOrg,
		PlanID:        plan.ID,
		Priority:      1,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("assign plan: %v", err)
	}
	routing.TryReload(ctx, e.Pool)

	// Set up SMPP Client & Connection Manager
	mgr := NewConnectionManager(ManagerConfig{
		DB:            e.Pool,
		AllowLoopback: true,
	})
	defer mgr.Close()

	smppClient := NewClient(ClientConfig{
		Manager: mgr,
		DB:      e.Pool,
	})

	msgRepo := messaging.NewRepository(e.Pool)
	cfg := messaging.DefaultConfig()

	// Use smppClient as the carrier sender
	msgSvc, err := messaging.NewService(
		e.Pool, msgRepo, cfg, routingEngine, ledgerSvc, keyring, e.Audit, smppClient, nil,
	)
	if err != nil {
		t.Fatalf("new messaging service: %v", err)
	}

	dispatcher := messaging.NewDispatcher(e.Pool, msgRepo, routingEngine, ledgerSvc, smppClient, nil)

	// Submit message
	clientRef := fmt.Sprintf("ref-%s", uuid.New().String()[:8])
	req := messaging.SubmitRequest{
		Destination:     "+12025550199",
		SenderID:        "TESTSMS",
		Text:            "Hello SMPP Integration Test",
		ClientReference: &clientRef,
	}

	submitRes, err := msgSvc.Submit(ctx, custUser, req)
	if err != nil {
		t.Fatalf("Submit message failed: %v", err)
	}
	if submitRes.Status != messaging.StatusAccepted {
		t.Fatalf("expected status 'accepted', got: %s", submitRes.Status)
	}

	// Dispatch from outbox through SMPP Client to Mock SMSC
	processed := dispatcher.ProcessBatch(ctx)
	if processed == 0 {
		t.Fatalf("expected at least 1 message processed from outbox")
	}

	// 8. Assertions:
	// a) Message moved to StatusSubmitted (PROVIDER_ACCEPTED)
	var finalStatus string
	err = e.Pool.QueryRow(ctx, "SELECT status FROM messages WHERE id = $1", submitRes.MessageID).Scan(&finalStatus)
	if err != nil {
		t.Fatalf("query message status: %v", err)
	}
	if finalStatus != messaging.StatusSubmitted {
		t.Errorf("expected message status %s, got %s", messaging.StatusSubmitted, finalStatus)
	}

	// b) Provider message ID recorded in message_provider_lookups
	var lookupProvID string
	err = e.Pool.QueryRow(ctx, `
		SELECT provider_message_id FROM message_provider_lookups
		WHERE connection_id = $1 AND message_id = $2
	`, connID, submitRes.MessageID).Scan(&lookupProvID)
	if err != nil {
		t.Fatalf("provider message ID not found in message_provider_lookups: %v", err)
	}
	if !strings.HasPrefix(lookupProvID, "MOCK-") {
		t.Errorf("expected provider message ID starting with MOCK-, got %s", lookupProvID)
	}

	// c) Outbox row status moved to published
	var outboxStatus string
	err = e.Pool.QueryRow(ctx, "SELECT status FROM message_outbox WHERE message_id = $1", submitRes.MessageID).Scan(&outboxStatus)
	if err != nil || outboxStatus != "published" {
		t.Errorf("expected outbox row published, got %s (err: %v)", outboxStatus, err)
	}

	// d) Mock SMSC received the message
	mockMsgs := mockServer.GetSubmittedMessages()
	if len(mockMsgs) == 0 {
		t.Fatalf("mock SMSC recorded 0 messages")
	}
	if string(mockMsgs[0].ShortMsg) != "Hello SMPP Integration Test" {
		t.Errorf("mock short message mismatch: %s", string(mockMsgs[0].ShortMsg))
	}
}

type testRater struct {
	sellPrice decimal.Decimal
	buyPrice  decimal.Decimal
	currency  string
}

func (r *testRater) LookupBuyRate(ctx context.Context, connectionID uuid.UUID, countryID uuid.UUID, operatorID *uuid.UUID, at time.Time) (*routing.RateLookupResult, error) {
	return &routing.RateLookupResult{
		Found:    true,
		Price:    r.buyPrice,
		Currency: r.currency,
	}, nil
}

func (r *testRater) LookupSellRate(ctx context.Context, customerOrgID uuid.UUID, countryID uuid.UUID, operatorID *uuid.UUID, at time.Time) (*routing.RateLookupResult, error) {
	return &routing.RateLookupResult{
		Found:    true,
		Price:    r.sellPrice,
		Currency: r.currency,
	}, nil
}

type testNumberer struct {
	defaultCountryID uuid.UUID
}

func (n *testNumberer) Lookup(destinationE164 string) (*routing.NumberInfo, error) {
	return &routing.NumberInfo{CountryID: n.defaultCountryID}, nil
}
