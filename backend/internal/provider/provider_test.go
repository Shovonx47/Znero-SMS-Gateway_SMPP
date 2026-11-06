package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/masterdata/operator"
	"github.com/znero-international/znero/backend/internal/provider"
	"github.com/znero-international/znero/backend/internal/secrets"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

func newTestKeyRing(t *testing.T) *secrets.KeyRing {
	t.Helper()
	rawKey := testkit.RandHex(32)
	kr, err := secrets.NewKeyRingFromEnv("development", rawKey, "v1", "")
	if err != nil {
		t.Fatalf("new keyring: %v", err)
	}
	return kr
}

func TestSSRFAndConnectionValidation(t *testing.T) {
	forbiddenHosts := []string{
		"127.0.0.1",
		"169.254.169.254",
		"10.0.0.1",
		"172.16.0.5",
		"192.168.1.1",
		"localhost",
		"::1",
		"fe80::1",
		"metadata.google.internal",
	}
	for _, h := range forbiddenHosts {
		if err := provider.ValidateHostSSRF(h, "production", "active"); err == nil {
			t.Errorf("expected SSRF validation to reject host %q in production, got nil", h)
		}
		if err := provider.ValidateHostSSRF(h, "development", "active"); err == nil {
			t.Errorf("expected SSRF validation to reject host %q in active connection, got nil", h)
		}
		if err := provider.ValidateHTTPBaseURL("https://"+h+"/v1/sms", "http", "production", "active"); err == nil {
			t.Errorf("expected HTTP Base URL SSRF validation to reject %q, got nil", h)
		}
	}

	// Cloud metadata 169.254.169.254 must be rejected even in development draft
	if err := provider.ValidateHostSSRF("169.254.169.254", "development", "draft"); err == nil {
		t.Fatalf("expected 169.254.169.254 to be rejected even in development draft")
	}

	// Active HTTP connections must use https://
	if err := provider.ValidateHTTPBaseURL("http://smpp-gw.example.com/sms", "http", "development", "active"); err == nil {
		t.Fatalf("expected http:// to be rejected for active connection")
	}

	// Valid fictional domain (non-resolving or external)
	if err := provider.ValidateHostSSRF("smpp-alpha.example.com", "production", "active"); err != nil {
		t.Fatalf("expected smpp-alpha.example.com to be accepted: %v", err)
	}
	if err := provider.ValidateHTTPBaseURL("https://api-alpha.example.com/v1/messages", "http", "production", "active"); err != nil {
		t.Fatalf("expected https://api-alpha.example.com/v1/messages to be accepted: %v", err)
	}

	// Hostname resolving to private/loopback IP via DNS (e.g. localtest.me -> 127.0.0.1)
	if err := provider.ValidateHostSSRF("localtest.me", "production", "active"); err == nil {
		t.Fatalf("expected hostname resolving to 127.0.0.1 (localtest.me) to be rejected by SSRF DNS resolution")
	}
	if err := provider.ValidateHostSSRF("sub.localtest.me", "production", "active"); err == nil {
		t.Fatalf("expected sub.localtest.me resolving to 127.0.0.1 to be rejected by SSRF DNS resolution")
	}
}

func TestProviderConnectionCoverageIntegration(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	kr := newTestKeyRing(t)
	svc := provider.NewService(provider.NewRepository(e.Pool), e.Audit, e.Approval, kr, "development")

	// Create a test country and operator via service layer for coverage tests
	creq := e.CountryRequest()
	cRes, err := e.Country.Create(ctx, e.Override, creq)
	if err != nil {
		t.Fatalf("create country: %v", err)
	}
	countryID := cRes.(*country.Country).ID

	opRes, err := e.Operator.Create(ctx, e.Override, operator.CreateOperatorRequest{
		Name:       "DEV SAMPLE TestOp " + testkit.RandHex(3),
		CountryID:  countryID,
		MCC:        "999",
		MNC:        "01",
		DataSource: "DEV-SAMPLE-FICTIONAL",
	})
	if err != nil {
		t.Fatalf("create operator: %v", err)
	}
	opRow := opRes.(*operator.Operator)

	// 1. Reseller and Customer get 403 on EVERY provider, connection, and coverage operation (read & write)
	resellerWithPerms := testkit.WithPermissions(e.Reseller,
		"provider.provider.view", "provider.provider.create", "provider.connection.view", "provider.coverage.view")
	customerWithPerms := testkit.WithPermissions(e.Customer,
		"provider.provider.view", "provider.provider.create", "provider.connection.view", "provider.coverage.view")

	for _, actor := range []*tenant.Context{resellerWithPerms, customerWithPerms} {
		if _, _, err := svc.ListProviders(ctx, actor, provider.ProviderListFilter{}); !errors.Is(err, provider.ErrPlatformOnlyProvider) {
			t.Fatalf("%s ListProviders: expected ErrPlatformOnlyProvider, got %v", actor.ActorOrgType, err)
		}
		if _, err := svc.GetProvider(ctx, actor, uuid.New()); !errors.Is(err, provider.ErrPlatformOnlyProvider) {
			t.Fatalf("%s GetProvider: expected ErrPlatformOnlyProvider, got %v", actor.ActorOrgType, err)
		}
		if _, err := svc.CreateProvider(ctx, actor, provider.CreateProviderRequest{
			Code: "RESELLER_TRY",
			Name: "Should Fail",
		}, "127.0.0.1", "test"); !errors.Is(err, provider.ErrPlatformOnlyProvider) {
			t.Fatalf("%s CreateProvider: expected ErrPlatformOnlyProvider, got %v", actor.ActorOrgType, err)
		}
		if _, err := svc.UpdateProvider(ctx, actor, uuid.New(), provider.UpdateProviderRequest{}, "127.0.0.1", "test"); !errors.Is(err, provider.ErrPlatformOnlyProvider) {
			t.Fatalf("%s UpdateProvider: expected ErrPlatformOnlyProvider, got %v", actor.ActorOrgType, err)
		}
		if _, err := svc.ListConnections(ctx, actor, uuid.New()); !errors.Is(err, provider.ErrPlatformOnlyProvider) {
			t.Fatalf("%s ListConnections: expected ErrPlatformOnlyProvider, got %v", actor.ActorOrgType, err)
		}
		if _, err := svc.CreateConnection(ctx, actor, uuid.New(), provider.CreateConnectionRequest{}, "127.0.0.1", "test"); !errors.Is(err, provider.ErrPlatformOnlyProvider) {
			t.Fatalf("%s CreateConnection: expected ErrPlatformOnlyProvider, got %v", actor.ActorOrgType, err)
		}
		if _, _, err := svc.ListCoverage(ctx, actor, provider.CoverageListFilter{}); !errors.Is(err, provider.ErrPlatformOnlyProvider) {
			t.Fatalf("%s ListCoverage: expected ErrPlatformOnlyProvider, got %v", actor.ActorOrgType, err)
		}
		if _, err := svc.CreateCoverage(ctx, actor, provider.CreateCoverageRequest{}, "127.0.0.1", "test"); !errors.Is(err, provider.ErrPlatformOnlyProvider) {
			t.Fatalf("%s CreateCoverage: expected ErrPlatformOnlyProvider, got %v", actor.ActorOrgType, err)
		}
	}

	// 2. Platform Admin creates, edits, versions, and rolls back a Provider
	code := "PRV_" + strings.ToUpper(testkit.RandHex(4))
	t.Cleanup(func() {
		_, _ = e.Pool.Exec(context.Background(), `DELETE FROM providers WHERE code = $1`, code)
	})

	createRes, err := svc.CreateProvider(ctx, e.Override, provider.CreateProviderRequest{
		Code:            code,
		Name:            "DEV SAMPLE Alpha v1",
		LegalEntityName: "DEV SAMPLE Alpha Carrier Ltd",
		ProviderType:    "smpp",
		CountryID:       &countryID,
		CurrencyCode:    "USD",
		Timezone:        "UTC",
		Status:          "active",
		Reason:          "Initial onboarding",
	}, "127.0.0.1", "provider-test")
	if err != nil || createRes.Provider == nil {
		t.Fatalf("CreateProvider failed: %v", err)
	}
	prv := createRes.Provider
	if prv.Version != 1 {
		t.Fatalf("expected version 1, got %d", prv.Version)
	}

	// Duplicate code is rejected
	if _, err := svc.CreateProvider(ctx, e.Override, provider.CreateProviderRequest{
		Code: code,
		Name: "Duplicate Ltd",
	}, "127.0.0.1", "provider-test"); !errors.Is(err, provider.ErrProviderCodeExists) {
		t.Fatalf("expected ErrProviderCodeExists, got %v", err)
	}

	// Update provider -> version 2
	newDisplay := "DEV SAMPLE Alpha v2"
	updRes, err := svc.UpdateProvider(ctx, e.Override, prv.ID, provider.UpdateProviderRequest{
		Name:   &newDisplay,
		Reason: "Rename provider name",
	}, "127.0.0.1", "provider-test")
	if err != nil || updRes.Provider.Version != 2 || updRes.Provider.Name != newDisplay {
		t.Fatalf("UpdateProvider failed: err=%v, res=%+v", err, updRes)
	}

	// Rollback provider to version 1 -> creates version 3
	rbRes, err := svc.RollbackProvider(ctx, e.Override, prv.ID, 1, "Revert to v1", "127.0.0.1", "provider-test")
	if err != nil || rbRes.Provider.Version != 3 || rbRes.Provider.Name != "DEV SAMPLE Alpha v1" {
		t.Fatalf("RollbackProvider failed: err=%v, res=%+v", err, rbRes)
	}
	if got := len(e.HistoryVersions("providers_history", "provider_id", prv.ID)); got != 3 {
		t.Fatalf("expected 3 history versions for provider, got %d", got)
	}

	// 3. Create Connection with secret password & verify SSRF + Secret-Free Guarantee
	port := 2775
	if _, err := svc.CreateConnection(ctx, e.Override, prv.ID, provider.CreateConnectionRequest{
		Name:     "prod-ssrf-bad",
		Protocol: "smpp",
		SMPPHost: "127.0.0.1",
		SMPPPort: &port,
		MaxTPS:   100,
		Status:   "active",
	}, "127.0.0.1", "provider-test"); err == nil {
		t.Fatalf("expected SSRF rejection for 127.0.0.1 in connection")
	}

	plainSecret := "ConnSecret_" + testkit.RandHex(12)
	connRes, err := svc.CreateConnection(ctx, e.Override, prv.ID, provider.CreateConnectionRequest{
		Name:         "alpha-smpp-1",
		Protocol:     "smpp",
		SMPPHost:     "smpp-alpha.example.com",
		SMPPPort:     &port,
		SMPPSystemID: "dev_sample_sys",
		SMPPPassword: &plainSecret,
		MaxTPS:       200,
		Status:       "draft",
		Reason:       "Initial sandbox connection",
	}, "127.0.0.1", "provider-test")
	if err != nil || connRes.Connection == nil {
		t.Fatalf("CreateConnection failed: %v", err)
	}
	conn := connRes.Connection

	// Verify encrypted in DB and decryptable only via KeyRing
	var dbCipher string
	if err := e.Pool.QueryRow(ctx, `SELECT smpp_password_encrypted FROM provider_connections WHERE id = $1`, conn.ID).Scan(&dbCipher); err != nil {
		t.Fatalf("query encrypted password: %v", err)
	}
	if dbCipher == "" || strings.Contains(dbCipher, plainSecret) {
		t.Fatalf("expected non-empty encrypted password not containing plaintext")
	}
	decrypted, err := kr.Decrypt(dbCipher)
	if err != nil || decrypted != plainSecret {
		t.Fatalf("expected KeyRing.Decrypt to recover plaintext, err=%v", err)
	}

	// Secret-free guarantee on GET response
	fetchedConn, err := svc.GetConnection(ctx, e.Override, conn.ID)
	if err != nil {
		t.Fatalf("GetConnection: %v", err)
	}
	connJSON, _ := json.Marshal(fetchedConn)
	if strings.Contains(string(connJSON), plainSecret) || strings.Contains(string(connJSON), dbCipher) {
		t.Fatalf("GET connection JSON leaked secret or ciphertext: %s", string(connJSON))
	}
	if !fetchedConn.SMPPPassword.Set || fetchedConn.SMPPPassword.LastChanged == nil {
		t.Fatalf("expected SecretStatus{Set: true, LastChanged: non-nil}, got %+v", fetchedConn.SMPPPassword)
	}

	// Secret-free guarantee on provider_connections_history.snapshot
	connHist, err := svc.ListConnectionHistory(ctx, e.Override, conn.ID)
	if err != nil || len(connHist) == 0 {
		t.Fatalf("ListConnectionHistory: %v", err)
	}
	histJSON, _ := json.Marshal(connHist)
	if strings.Contains(string(histJSON), plainSecret) || strings.Contains(string(histJSON), dbCipher) {
		t.Fatalf("provider_connections_history leaked secret or ciphertext: %s", string(histJSON))
	}

	// Secret-free guarantee on audit_logs
	for _, a := range e.AuditRows("provider_connection", conn.ID.String()) {
		blob := a.Before + " " + a.After + " " + a.Reason
		if strings.Contains(blob, plainSecret) || strings.Contains(blob, dbCipher) {
			t.Fatalf("audit_logs leaked secret or ciphertext: %s", blob)
		}
	}

	// 4. Maker-Checker on activating a connection & updating secret
	statusRes, err := svc.ChangeConnectionStatus(ctx, e.Maker, conn.ID, provider.UpdateConnectionStatusRequest{
		Status: "active",
		Reason: "Ready for active traffic",
	}, "127.0.0.1", "provider-test")
	if err != nil || statusRes.PendingChange == nil {
		t.Fatalf("expected Maker activating connection to produce PendingChange, err=%v, res=%+v", err, statusRes)
	}
	cr := statusRes.PendingChange.(*approval.ChangeRequest)

	// Maker cannot self-approve even if given approve permission
	makerWithApprove := testkit.WithPermissions(e.Maker, "provider.connection.approve")
	if err := e.Approval.Approve(ctx, makerWithApprove, cr.ID, "self-approve attempt"); !errors.Is(err, approval.ErrMakerCannotApproveOwnChange) {
		t.Fatalf("expected ErrMakerCannotApproveOwnChange, got %v", err)
	}

	// Checker approves -> connection becomes active (version 2)
	if err := e.Approval.Approve(ctx, e.Checker, cr.ID, "Approved connection activation"); err != nil {
		t.Fatalf("Checker approve failed: %v", err)
	}
	afterApprove, err := svc.GetConnection(ctx, e.Override, conn.ID)
	if err != nil || afterApprove.Status != "active" || afterApprove.Version != 2 {
		t.Fatalf("expected active connection at version 2, got %+v (err=%v)", afterApprove, err)
	}

	// Maker updates connection secret -> goes to pending change without leaking plaintext or ciphertext
	rotatedSecret := "RotatedSecret_" + testkit.RandHex(12)
	secRes, err := svc.UpdateConnectionSecret(ctx, e.Maker, conn.ID, provider.UpdateConnectionSecretRequest{
		SMPPPassword: &rotatedSecret,
		Reason:       "Scheduled credential rotation",
	}, "127.0.0.1", "provider-test")
	if err != nil || secRes.PendingChange == nil {
		t.Fatalf("expected Maker secret update to create PendingChange, err=%v", err)
	}
	secCR := secRes.PendingChange.(*approval.ChangeRequest)
	if strings.Contains(string(secCR.ProposedData), rotatedSecret) {
		t.Fatalf("change_request ProposedData leaked plaintext secret!")
	}
	if err := e.Approval.Approve(ctx, e.Checker, secCR.ID, "Approve secret rotation"); err != nil {
		t.Fatalf("Checker approve secret rotation failed: %v", err)
	}
	var rotatedCipher string
	_ = e.Pool.QueryRow(ctx, `SELECT smpp_password_encrypted FROM provider_connections WHERE id = $1`, conn.ID).Scan(&rotatedCipher)
	if dec, err := kr.Decrypt(rotatedCipher); err != nil || dec != rotatedSecret {
		t.Fatalf("expected rotated secret to be active after approval, err=%v", err)
	}

	// 5. Coverage creation, duplicate rejection, operator-country validation, versioning & rollback
	covRes, err := svc.CreateCoverage(ctx, e.Override, provider.CreateCoverageRequest{
		ProviderID: prv.ID,
		CountryID:  countryID,
		OperatorID: &opRow.ID,
		Status:     "declared",
		DataSource: "DEV-SAMPLE-FICTIONAL",
		Notes:      "Initial coverage v1",
		Reason:     "Initial coverage declaration",
	}, "127.0.0.1", "provider-test")
	if err != nil || covRes.Coverage == nil {
		t.Fatalf("CreateCoverage failed: %v", err)
	}
	cov := covRes.Coverage

	// Duplicate coverage row is rejected
	if _, err := svc.CreateCoverage(ctx, e.Override, provider.CreateCoverageRequest{
		ProviderID: prv.ID,
		CountryID:  countryID,
		OperatorID: &opRow.ID,
		Status:     "declared",
	}, "127.0.0.1", "provider-test"); !errors.Is(err, provider.ErrCoverageExists) {
		t.Fatalf("expected ErrCoverageExists on duplicate coverage, got %v", err)
	}

	// Update coverage & rollback
	newNotes := "Updated coverage v2"
	covUpd, err := svc.UpdateCoverage(ctx, e.Override, cov.ID, provider.UpdateCoverageRequest{
		Notes:  &newNotes,
		Reason: "Update notes",
	}, "127.0.0.1", "provider-test")
	if err != nil || covUpd.Coverage.Version != 2 || covUpd.Coverage.Notes != newNotes {
		t.Fatalf("UpdateCoverage failed: err=%v, res=%+v", err, covUpd)
	}

	covRB, err := svc.RollbackCoverage(ctx, e.Override, cov.ID, 1, "Rollback coverage to v1", "127.0.0.1", "provider-test")
	if err != nil || covRB.Coverage.Version != 3 || covRB.Coverage.Notes != "Initial coverage v1" {
		t.Fatalf("RollbackCoverage failed: err=%v, res=%+v", err, covRB)
	}
	_ = uuid.Nil
}
