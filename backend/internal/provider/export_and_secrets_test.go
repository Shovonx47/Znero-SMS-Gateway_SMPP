package provider_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/provider"
	"github.com/znero-international/znero/backend/internal/testkit"
)

func TestProviderExportsAndSecretScanning(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	kr := newTestKeyRing(t)
	svc := provider.NewService(provider.NewRepository(e.Pool), e.Audit, e.Approval, kr, "development")

	// Create test provider with a formula name
	pCode := "PROV_" + strings.ToUpper(uuid.New().String()[:6])
	pRes, err := svc.CreateProvider(ctx, e.Override, provider.CreateProviderRequest{
		Code:            pCode,
		Name:            "=FormulaProvider",
		LegalEntityName: "+FormulaLegal",
		ProviderType:    "smpp",
		CurrencyCode:    "USD",
		Timezone:        "UTC",
		Status:          "active",
		Reason:          "Secret scanning and export test setup",
	}, "127.0.0.1", "test-agent")
	if err != nil || pRes.Provider == nil {
		t.Fatalf("CreateProvider failed: %v", err)
	}
	prov := pRes.Provider

	t.Cleanup(func() {
		_, _ = e.Pool.Exec(context.Background(), `DELETE FROM provider_connections WHERE provider_id = $1`, prov.ID)
		_, _ = e.Pool.Exec(context.Background(), `DELETE FROM providers WHERE id = $1`, prov.ID)
	})

	plaintextPassword := "SuperSecretSMPPPass!987654"
	port := 2775
	enquireSec := 30
	connRes, err := svc.CreateConnection(ctx, e.Override, prov.ID, provider.CreateConnectionRequest{
		Name:               "@FormulaConn",
		Protocol:           "smpp",
		SMPPHost:           "smpp-test.example.com",
		SMPPPort:           &port,
		SMPPSystemID:       "sys_prov_" + uuid.New().String()[:6],
		SMPPPassword:       &plaintextPassword,
		SMPPBindType:       "transceiver",
		MaxTPS:             100,
		SMPPEnquireLinkSec: &enquireSec,
		Status:             "active",
		Reason:             "Create connection with secret",
	}, "127.0.0.1", "test-agent")
	if err != nil || connRes.Connection == nil {
		t.Fatalf("CreateConnection failed: %v", err)
	}
	conn := connRes.Connection

	// =========================================================================
	// Export Verification & Formula Injection Sanitization
	// =========================================================================

	// Test ExportProviders (CSV & JSON)
	csvProv, _, err := svc.ExportProviders(ctx, e.Override, provider.ProviderListFilter{Search: pCode}, "csv")
	if err != nil {
		t.Fatalf("ExportProviders CSV failed: %v", err)
	}
	csvProvStr := string(csvProv)
	if !strings.Contains(csvProvStr, "'=FormulaProvider") {
		t.Errorf("ExportProviders CSV did not sanitize leading '=' formula cell: %s", csvProvStr)
	}

	// Test ExportConnections (CSV & JSON)
	csvConn, cType, err := svc.ExportConnections(ctx, e.Override, prov.ID, "csv")
	if err != nil || cType != "text/csv" {
		t.Fatalf("ExportConnections CSV failed: err=%v, type=%s", err, cType)
	}
	csvConnStr := string(csvConn)
	// Check formula escaping for connection name "@FormulaConn" -> "'@FormulaConn"
	if !strings.Contains(csvConnStr, "'@FormulaConn") {
		t.Errorf("ExportConnections CSV did not sanitize leading '@' formula cell: %s", csvConnStr)
	}
	// Assert no plaintext secret in CSV
	if strings.Contains(csvConnStr, plaintextPassword) {
		t.Fatalf("CRITICAL SECURITY LEAK: plaintext password found in connection CSV export!")
	}

	jsonConn, jType, err := svc.ExportConnections(ctx, e.Override, prov.ID, "json")
	if err != nil || jType != "application/json" {
		t.Fatalf("ExportConnections JSON failed: err=%v, type=%s", err, jType)
	}
	jsonConnStr := string(jsonConn)
	if strings.Contains(jsonConnStr, plaintextPassword) {
		t.Fatalf("CRITICAL SECURITY LEAK: plaintext password found in connection JSON export!")
	}
	if strings.Contains(jsonConnStr, "smpp_password_encrypted") {
		t.Fatalf("CRITICAL SECURITY LEAK: smpp_password_encrypted found in connection JSON export!")
	}

	var parsedConns []provider.ProviderConnection
	if err := json.Unmarshal(jsonConn, &parsedConns); err != nil || len(parsedConns) == 0 {
		t.Fatalf("failed to parse ExportConnections JSON: %v", err)
	}
	if !parsedConns[0].SMPPPassword.Set {
		t.Errorf("expected parsedConns[0].SMPPPassword.Set == true")
	}

	// =========================================================================
	// Secret Scanning across Database, Audit Logs, and History
	// =========================================================================

	// 1. Verify Database row
	var dbEncryptedPass string
	err = e.Pool.QueryRow(ctx, `SELECT smpp_password_encrypted FROM provider_connections WHERE id = $1`, conn.ID).Scan(&dbEncryptedPass)
	if err != nil || dbEncryptedPass == "" {
		t.Fatalf("querying provider_connections: %v", err)
	}
	if dbEncryptedPass == plaintextPassword {
		t.Fatalf("CRITICAL: plaintext password stored directly in provider_connections database row!")
	}

	// 2. Verify Audit Logs
	for _, a := range e.AuditRows("provider_connection", conn.ID.String()) {
		blob := a.Before + " " + a.After + " " + a.Reason
		if strings.Contains(blob, plaintextPassword) || strings.Contains(blob, dbEncryptedPass) {
			t.Fatalf("audit_logs leaked secret or ciphertext: %s", blob)
		}
	}

	// 3. Verify History Table Snapshots
	histRows, err := e.Pool.Query(ctx, `SELECT snapshot FROM provider_connections_history WHERE connection_id = $1`, conn.ID)
	if err != nil {
		t.Fatalf("querying provider_connections_history: %v", err)
	}
	defer histRows.Close()

	for histRows.Next() {
		var snap json.RawMessage
		if err := histRows.Scan(&snap); err != nil {
			t.Fatalf("scanning history snapshot: %v", err)
		}
		sStr := string(snap)
		if strings.Contains(sStr, plaintextPassword) {
			t.Fatalf("CRITICAL SECURITY LEAK: plaintext password found in provider_connections_history snapshot!")
		}
	}

	// 4. Update Connection Secret and verify rotation does not leak plaintext either
	updatedSecret := "RotatedSMPPSecret!1234567"
	updRes, err := svc.UpdateConnectionSecret(ctx, e.Override, conn.ID, provider.UpdateConnectionSecretRequest{
		SMPPPassword: &updatedSecret,
		Reason:       "Secret rotation test",
	}, "127.0.0.1", "test-agent")
	if err != nil || updRes == nil {
		t.Fatalf("UpdateConnectionSecret failed: %v", err)
	}

	// Scan audit logs again for the rotated secret
	for _, a := range e.AuditRows("provider_connection", conn.ID.String()) {
		blob := a.Before + " " + a.After + " " + a.Reason
		if strings.Contains(blob, updatedSecret) {
			t.Fatalf("CRITICAL SECURITY LEAK: rotated plaintext password found in audit_logs!")
		}
	}
}
