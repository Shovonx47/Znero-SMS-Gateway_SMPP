package smppserver_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/znero-international/znero/backend/internal/customeredge"
	"github.com/znero-international/znero/backend/internal/ledger"
	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/internal/routing"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/internal/smpp/pdu"
	"github.com/znero-international/znero/backend/internal/smppserver"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/pkg/crypto/password"
)

type srvRater struct{ sellPrice, buyPrice decimal.Decimal }

func (r *srvRater) LookupBuyRate(_ context.Context, _, _ uuid.UUID, _ *uuid.UUID, _ time.Time) (*routing.RateLookupResult, error) {
	return &routing.RateLookupResult{Found: true, Price: r.buyPrice, Currency: "USD"}, nil
}
func (r *srvRater) LookupSellRate(_ context.Context, _, _ uuid.UUID, _ *uuid.UUID, _ time.Time) (*routing.RateLookupResult, error) {
	return &routing.RateLookupResult{Found: true, Price: r.sellPrice, Currency: "USD"}, nil
}

type srvNumberer struct{ countryID uuid.UUID }

func (n *srvNumberer) Lookup(_ string) (*routing.NumberInfo, error) {
	return &routing.NumberInfo{CountryID: n.countryID}, nil
}

func TestSMPPServer_BindLockout_Auth_SubmitSM_UDHReject_And_DLRDelivery(t *testing.T) {
	e := testkit.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. BindLimiter composite IP|systemID isolation check
	bl := smppserver.NewBindLimiter()
	for i := 0; i < 6; i++ {
		bl.RecordFailure("10.8.8.1", "victim_sys")
	}
	if !bl.IsLockedOut("10.8.8.1", "victim_sys") {
		t.Fatalf("expected 10.8.8.1|victim_sys locked out")
	}
	if bl.IsLockedOut("10.8.8.1", "other_sys") || bl.IsLockedOut("10.8.8.2", "victim_sys") {
		t.Fatalf("lockout bled across IP or systemID boundary!")
	}

	lc, logger := testkit.NewLogCapture()
	kr, err := secrets.NewKeyRingFromEnv("development", testkit.RandHex(32), "v1", "")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}

	var countryID uuid.UUID
	if err := e.Pool.QueryRow(ctx, `SELECT id FROM countries LIMIT 1`).Scan(&countryID); err != nil {
		cRes, cErr := e.Country.Create(ctx, e.Override, e.CountryRequest())
		if cErr != nil {
			t.Fatalf("create country: %v", cErr)
		}
		countryID = cRes.(*country.Country).ID
	}

	// Setup customer, profile, wallet, routing, SMPP account
	custOrgID, _ := e.ChildOrg(e.PlatformOrgID, e.PlatformOrgPath, "smpp-srv-cust-"+testkit.RandHex(4), "customer")

	_, _ = e.Pool.Exec(ctx, `
		INSERT INTO customer_profiles (organization_id, account_type, billing_currency, default_max_tps, billing_mode, dlr_wait_hours, no_dlr_policy, failed_after_accept_policy, version)
		VALUES ($1, 'prepaid', 'USD', 50, 'on_delivery', 24, 'retain_until_resolution', 'keep_charge', 1)`, custOrgID)

	ledgerSvc := ledger.NewService(ledger.NewRepository(e.Pool), org.NewRepository(e.Pool), e.Approval, e.Audit, e.Pool)
	w, err := ledgerSvc.EnsureWallet(ctx, e.Override, custOrgID, "USD", "prepaid")
	if err != nil {
		t.Fatalf("ensure wallet: %v", err)
	}
	_, _, _ = ledgerSvc.Topup(ctx, e.Override, w.ID, ledger.WalletTopupRequest{
		Amount: decimal.NewFromInt(100), Reason: "smppserver test topup", IdempotencyKey: "s-topup-" + custOrgID.String(),
	}, "127.0.0.1", "test")

	var provID, connID uuid.UUID
	_ = e.Pool.QueryRow(ctx, `INSERT INTO providers (code, name, provider_type, status, version) VALUES ($1, 'SMPP Test Prov', 'smpp', 'active', 1) RETURNING id`, "SP_"+testkit.RandHex(4)).Scan(&provID)
	_ = e.Pool.QueryRow(ctx, `INSERT INTO provider_connections (provider_id, name, protocol, status, version, max_tps) VALUES ($1, $2, 'smpp', 'active', 1, 100) RETURNING id`, provID, "SP-Conn-"+testkit.RandHex(4)).Scan(&connID)
	_, _ = e.Pool.Exec(ctx, `
		INSERT INTO provider_coverage (provider_id, country_id, status, version) VALUES ($1, $2, 'active', 1)
		ON CONFLICT (provider_id, country_id, COALESCE(operator_id, '00000000-0000-0000-0000-000000000000'::uuid)) DO UPDATE SET status = 'active'`,
		provID, countryID)

	routingEngine := routing.NewEngine(e.Pool, &srvRater{sellPrice: decimal.RequireFromString("0.050000"), buyPrice: decimal.RequireFromString("0.020000")}, &srvNumberer{countryID: countryID}, nil)
	routingSvc := routing.NewService(routing.NewRepository(e.Pool), e.Approval, e.Audit, e.Pool, routingEngine)
	plan, _ := routingSvc.CreatePlan(ctx, e.Override, &routing.RoutePlan{Name: "SMPP Plan " + testkit.RandHex(3), Code: "SP_" + strings.ToUpper(testkit.RandHex(4)), Strategy: routing.StrategyPriority})
	_, _, _ = routingSvc.CreateEntry(ctx, e.Override, &routing.RoutePlanEntry{PlanID: plan.ID, ConnectionID: connID, ScopeType: "global", Priority: 1, Weight: 100, Status: "active"})
	_, _, _ = routingSvc.RequestPlanActivation(ctx, e.Override, plan.ID)
	_, _, _ = routingSvc.CreateAssignment(ctx, e.Override, &routing.CustomerRouteAssignment{CustomerOrgID: custOrgID, PlanID: plan.ID, Priority: 1, Status: "active"})
	routing.TryReload(ctx, e.Pool)

	sysID := "esme_" + testkit.RandHex(4)
	plainPass := "SMPPPass_" + testkit.RandHex(12)
	pwHash, err := password.Hash(plainPass)
	if err != nil {
		t.Fatalf("hash pw: %v", err)
	}
	var acctID uuid.UUID
	if err := e.Pool.QueryRow(ctx, `
		INSERT INTO customer_smpp_accounts (organization_id, system_id, password_hash, allowed_bind_types, max_binds, max_tps, status)
		VALUES ($1, $2, $3, ARRAY['transceiver','transmitter','receiver'], 4, 50, 'active')
		RETURNING id`, custOrgID, sysID, pwHash,
	).Scan(&acctID); err != nil {
		t.Fatalf("insert smpp account: %v", err)
	}

	msgRepo := messaging.NewRepository(e.Pool)
	msgSvc, err := messaging.NewService(e.Pool, msgRepo, messaging.DefaultConfig(), routingEngine, ledgerSvc, kr, e.Audit, messaging.NewFakeSender(), logger)
	if err != nil {
		t.Fatalf("messaging service: %v", err)
	}

	srv := smppserver.NewServer(smppserver.ServerConfig{
		Addr:            "127.0.0.1:0",
		SystemID:        "ZNERO_TEST",
		IdleTimeout:     30 * time.Second,
		EnquireInterval: 10 * time.Second,
		DefaultWindow:   16,
	}, e.Pool, msgSvc, msgRepo, customeredge.NewCustomerLimiter(), logger)
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("start smpp server: %v", err)
	}
	defer srv.Stop()

	// 2. Wrong password bind rejected
	badConn, err := net.DialTimeout("tcp", srv.Addr(), 3*time.Second)
	if err != nil {
		t.Fatalf("dial bad bind: %v", err)
	}
	_ = pdu.WritePDU(badConn, pdu.NewBind(pdu.CmdBindTransceiver, 1, sysID, "wrong-password", "SMPP"))
	badRespP, err := pdu.ReadPDU(badConn)
	_ = badConn.Close()
	if err != nil || badRespP.GetHeader().CommandStatus == pdu.StatusROK {
		t.Fatalf("expected non-ROK bind rejection on wrong password, got %v err=%v", badRespP, err)
	}

	// 3. Valid bind_transceiver succeeds
	conn, err := net.DialTimeout("tcp", srv.Addr(), 3*time.Second)
	if err != nil {
		t.Fatalf("dial smppserver: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))

	if err := pdu.WritePDU(conn, pdu.NewBind(pdu.CmdBindTransceiver, 2, sysID, plainPass, "SMPP")); err != nil {
		t.Fatalf("write bind: %v", err)
	}
	bindRespP, err := pdu.ReadPDU(conn)
	if err != nil || bindRespP.GetHeader().CommandStatus != pdu.StatusROK {
		t.Fatalf("expected bind ESME_ROK, got %+v err=%v", bindRespP, err)
	}

	// 4. Customer-split UDH part (ESMClass & 0x40 != 0) MUST be rejected
	udhSub := pdu.NewSubmitSM(3)
	udhSub.SourceAddr = "ZNERO"
	udhSub.DestAddr = "+12025550177"
	udhSub.ESMClass = 0x40
	udhSub.ShortMessage = []byte{0x05, 0x00, 0x03, 0x01, 0x02, 0x01, 'H', 'i'}
	_ = pdu.WritePDU(conn, udhSub)
	udhResp, err := pdu.ReadPDU(conn)
	if err != nil || udhResp.GetHeader().CommandStatus == pdu.StatusROK {
		t.Fatalf("expected customer-split UDH part to be rejected, got status=%d err=%v", udhResp.GetHeader().CommandStatus, err)
	}

	// 5. Standard SubmitSM succeeds -> verifies BUG-011 IngressChannel="smpp" & IngressAccountID
	secretSMSBody := "SMPP_SECRET_OTP_9944"
	sub := pdu.NewSubmitSM(4)
	sub.SourceAddr = "ZNERO"
	sub.DestAddr = "+12025550166"
	sub.RegisteredDelivery = 1
	sub.ShortMessage = []byte(secretSMSBody)
	if err := pdu.WritePDU(conn, sub); err != nil {
		t.Fatalf("write submit_sm: %v", err)
	}
	subRespP, err := pdu.ReadPDU(conn)
	if err != nil {
		t.Fatalf("read submit_sm_resp: %v", err)
	}
	subResp, ok := subRespP.(*pdu.SubmitSMResp)
	if !ok || subResp.Header.CommandStatus != pdu.StatusROK || subResp.MessageID == "" {
		t.Fatalf("submit_sm failed: %+v", subRespP)
	}
	msgUUID, err := uuid.Parse(subResp.MessageID)
	if err != nil {
		t.Fatalf("parse submit_sm_resp message_id %q: %v", subResp.MessageID, err)
	}

	// Verify DB persisted ingress_channel='smpp' and ingress_account_id=acctID and dlr_requested=true
	var gotChannel string
	var gotAcctID uuid.UUID
	var gotDLR bool
	if err := e.Pool.QueryRow(ctx, `SELECT ingress_channel, ingress_account_id, dlr_requested FROM messages WHERE id = $1`, msgUUID).Scan(&gotChannel, &gotAcctID, &gotDLR); err != nil {
		t.Fatalf("query message %s: %v", msgUUID, err)
	}
	if gotChannel != "smpp" || gotAcctID != acctID || !gotDLR {
		t.Fatalf("BUG-011 regression: channel=%q acct=%s dlr=%v", gotChannel, gotAcctID, gotDLR)
	}

	// 6. Queue customer DLR via smpp_dlr_queue & verify DLRDispatcher pushes real deliver_sm over TCP!
	_, _ = e.Pool.Exec(ctx, `DELETE FROM smpp_dlr_queue WHERE account_id <> $1`, acctID)
	if err := msgRepo.InsertSMPPDLRQueue(ctx, nil, messaging.SMPPDLRQueueItem{
		ID:          uuid.New(),
		AccountID:   acctID,
		MessageID:   msgUUID,
		MessageRef:  subResp.MessageID,
		Destination: "+12025550166",
		SenderID:    "ZNERO",
		Status:      "delivered",
		ErrorCode:   "000",
		QueueStatus: "pending",
		Attempts:    0,
		ExpiresAt:   time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("insert smpp dlr queue: %v", err)
	}

	dlrDisp := smppserver.NewDLRDispatcher(smppserver.DLRDispatcherConfig{
		BatchSize:     20,
		LeaseDuration: 10 * time.Second,
	}, e.Pool, msgRepo, srv.Tracker(), logger)

	doneDeliver := make(chan *pdu.DeliverSM, 1)
	go func() {
		dlrDisp.ProcessBatch(ctx)
	}()

	p, err := pdu.ReadPDU(conn)
	if err != nil {
		t.Fatalf("read deliver_sm from customer DLR dispatcher: %v", err)
	}
	deliv, ok := p.(*pdu.DeliverSM)
	if !ok {
		t.Fatalf("expected DeliverSM PDU, got %T", p)
	}
	doneDeliver <- deliv
	_ = pdu.WritePDU(conn, pdu.NewDeliverSMResp(pdu.StatusROK, deliv.Header.SequenceNumber))

	dl := <-doneDeliver
	if dl.ESMClass&0x04 == 0 || !strings.Contains(string(dl.ShortMessage), "stat:DELIVRD") {
		t.Fatalf("unexpected customer deliver_sm DLR payload: esm=%x msg=%q", dl.ESMClass, string(dl.ShortMessage))
	}

	lc.ScanForLeaks(t)
	logs := lc.Output()
	if strings.Contains(logs, plainPass) || strings.Contains(logs, secretSMSBody) || strings.Contains(logs, "+12025550166") {
		t.Fatalf("SECURITY: SMPP password, body, or full destination leaked into logs")
	}
}
