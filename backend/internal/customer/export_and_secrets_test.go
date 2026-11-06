package customer_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/internal/customer"
	"github.com/znero-international/znero/backend/internal/testkit"
)

func TestCustomerExportsAndSecretScanning(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	svc := customer.NewService(customer.NewRepository(e.Pool), e.Audit, e.Approval)

	// Create test organizations: Reseller A -> Cust A
	resellerAOrg, resellerAPath := e.ChildOrg(e.PlatformOrgID, e.PlatformOrgPath, "export-reseller", "reseller")
	custAOrg, custAPath := e.ChildOrg(resellerAOrg, resellerAPath, "=CustFormulaOrg", "customer")

	t.Cleanup(func() {
		_, _ = e.Pool.Exec(context.Background(), `DELETE FROM customer_profiles WHERE organization_id = $1`, custAOrg)
		_, _ = e.Pool.Exec(context.Background(), `DELETE FROM customer_api_keys WHERE organization_id = $1`, custAOrg)
		_, _ = e.Pool.Exec(context.Background(), `DELETE FROM customer_smpp_accounts WHERE organization_id = $1`, custAOrg)
	})

	perms := []string{
		"customer.profile.view", "customer.profile.create", "customer.profile.edit",
		"customer.apikey.view", "customer.apikey.create", "customer.apikey.revoke", "customer.apikey.rotate",
		"customer.smppaccount.view", "customer.smppaccount.create", "customer.smppaccount.edit", "customer.smppaccount.revoke",
	}
	resellerActor := e.Actor("export-reseller-user", resellerAOrg, resellerAPath, "reseller", false, perms...)
	_ = custAPath

	// 1. Create Profile with a potentially dangerous CSV formula name
	tps := 50
	credit := 1000.0
	profRes, err := svc.CreateProfile(ctx, e.Override, customer.CreateProfileRequest{
		OrganizationID:    custAOrg,
		AccountType:       "prepaid",
		BillingCurrency:   "USD",
		Timezone:          "UTC",
		TechnicalContact:  "tech@example.com",
		CreditLimit:       &credit,
		DefaultMaxTPS:     &tps,
		AllowedCountries:  []string{"US"},
		DestinationPolicy: "allow_all",
		Status:            "active",
		Notes:             "@evilNoteFormula",
		Reason:            "Export test setup",
	}, "127.0.0.1", "test-agent")
	if err != nil || profRes.Profile == nil {
		t.Fatalf("CreateProfile failed: %v", err)
	}

	// 2. Create API Key
	apiKeyRes, err := svc.CreateAPIKey(ctx, resellerActor, customer.CreateAPIKeyRequest{
		OrganizationID: custAOrg,
		Label:          "+EvilKeyFormula",
		Scopes:         []string{"sms.send"},
	}, "127.0.0.1", "test-agent")
	if err != nil || apiKeyRes == nil {
		t.Fatalf("CreateAPIKey failed: %v", err)
	}
	rawPlaintextAPIKey := apiKeyRes.PlaintextKey
	if !strings.HasPrefix(rawPlaintextAPIKey, "znk_live_") {
		t.Fatalf("expected API key prefix znk_live_, got %s", rawPlaintextAPIKey)
	}

	// 3. Create SMPP Account
	maxBinds := 2
	maxTPS := 20
	smppRes, err := svc.CreateSMPPAccount(ctx, resellerActor, customer.CreateSMPPAccountRequest{
		OrganizationID:   custAOrg,
		SystemID:         "smpp_" + uuid.New().String()[:8],
		AllowedBindTypes: []string{"transceiver"},
		MaxBinds:         &maxBinds,
		MaxTPS:           &maxTPS,
	}, "127.0.0.1", "test-agent")
	if err != nil || smppRes == nil {
		t.Fatalf("CreateSMPPAccount failed: %v", err)
	}
	rawPlaintextSMPPPassword := smppRes.PlaintextPassword
	if rawPlaintextSMPPPassword == "" {
		t.Fatalf("expected generated plaintext password on creation")
	}

	// =========================================================================
	// Export Verification & Formula Injection Sanitization
	// =========================================================================

	// Set organisation name to start with '=' to test formula injection escaping
	_, err = e.Pool.Exec(ctx, `UPDATE organizations SET name = '=CustFormulaOrg' WHERE id = $1`, custAOrg)
	if err != nil {
		t.Fatalf("failed updating organization name: %v", err)
	}

	// Test ExportProfiles (CSV & JSON)
	csvProfiles, cType, err := svc.ExportProfiles(ctx, resellerActor, customer.ProfileListFilter{OrganizationID: &custAOrg}, "csv")
	if err != nil || cType != "text/csv" {
		t.Fatalf("ExportProfiles CSV failed: err=%v, type=%s", err, cType)
	}
	csvProfStr := string(csvProfiles)
	// Verify CSV formula sanitization: "=CustFormulaOrg" -> "'=CustFormulaOrg"
	if !strings.Contains(csvProfStr, "'=CustFormulaOrg") {
		t.Errorf("ExportProfiles CSV did not sanitize leading '=' formula cell, got: %s", csvProfStr)
	}

	jsonProfiles, jType, err := svc.ExportProfiles(ctx, resellerActor, customer.ProfileListFilter{OrganizationID: &custAOrg}, "json")
	if err != nil || jType != "application/json" {
		t.Fatalf("ExportProfiles JSON failed: err=%v, type=%s", err, jType)
	}
	var parsedProfiles []customer.CustomerProfile
	if err := json.Unmarshal(jsonProfiles, &parsedProfiles); err != nil || len(parsedProfiles) == 0 {
		t.Fatalf("failed to parse ExportProfiles JSON: %v", err)
	}

	// Test ExportAPIKeys (CSV & JSON)
	csvKeys, _, err := svc.ExportAPIKeys(ctx, resellerActor, custAOrg, "csv")
	if err != nil {
		t.Fatalf("ExportAPIKeys CSV failed: %v", err)
	}
	csvKeysStr := string(csvKeys)
	if strings.Contains(csvKeysStr, rawPlaintextAPIKey) {
		t.Fatalf("CRITICAL SECURITY LEAK: raw plaintext API key found in CSV export!")
	}
	if !strings.Contains(csvKeysStr, apiKeyRes.APIKey.KeyPrefix) {
		t.Errorf("expected KeyPrefix in API key export")
	}
	// Verify formula injection: "+EvilKeyFormula" -> "'+EvilKeyFormula"
	if !strings.Contains(csvKeysStr, "'+EvilKeyFormula") {
		t.Errorf("ExportAPIKeys CSV did not sanitize leading '+' formula cell: %s", csvKeysStr)
	}

	jsonKeys, _, err := svc.ExportAPIKeys(ctx, resellerActor, custAOrg, "json")
	if err != nil {
		t.Fatalf("ExportAPIKeys JSON failed: %v", err)
	}
	if strings.Contains(string(jsonKeys), rawPlaintextAPIKey) {
		t.Fatalf("CRITICAL SECURITY LEAK: raw plaintext API key found in JSON export!")
	}
	if strings.Contains(string(jsonKeys), "key_hash") {
		t.Fatalf("CRITICAL SECURITY LEAK: key_hash exposed in JSON export!")
	}

	// Test ExportSMPPAccounts (CSV & JSON)
	csvSMPP, _, err := svc.ExportSMPPAccounts(ctx, resellerActor, custAOrg, "csv")
	if err != nil {
		t.Fatalf("ExportSMPPAccounts CSV failed: %v", err)
	}
	csvSMPPStr := string(csvSMPP)
	if strings.Contains(csvSMPPStr, rawPlaintextSMPPPassword) {
		t.Fatalf("CRITICAL SECURITY LEAK: raw plaintext SMPP password found in CSV export!")
	}

	jsonSMPP, _, err := svc.ExportSMPPAccounts(ctx, resellerActor, custAOrg, "json")
	if err != nil {
		t.Fatalf("ExportSMPPAccounts JSON failed: %v", err)
	}
	if strings.Contains(string(jsonSMPP), rawPlaintextSMPPPassword) {
		t.Fatalf("CRITICAL SECURITY LEAK: raw plaintext SMPP password found in JSON export!")
	}
	if strings.Contains(string(jsonSMPP), "password_hash") {
		t.Fatalf("CRITICAL SECURITY LEAK: password_hash exposed in JSON export!")
	}

	// =========================================================================
	// Secret Scanning Verification across Database, Audit Logs, and History
	// =========================================================================

	// 1. Database table checks
	var dbKeyHash string
	err = e.Pool.QueryRow(ctx, `SELECT key_hash FROM customer_api_keys WHERE id = $1`, apiKeyRes.APIKey.ID).Scan(&dbKeyHash)
	if err != nil || dbKeyHash == "" {
		t.Fatalf("querying customer_api_keys: %v", err)
	}
	if dbKeyHash == rawPlaintextAPIKey {
		t.Fatalf("CRITICAL: raw API key stored plaintext in database!")
	}

	var dbPasswordHash string
	err = e.Pool.QueryRow(ctx, `SELECT password_hash FROM customer_smpp_accounts WHERE id = $1`, smppRes.Account.ID).Scan(&dbPasswordHash)
	if err != nil || dbPasswordHash == "" {
		t.Fatalf("querying customer_smpp_accounts: %v", err)
	}
	if dbPasswordHash == rawPlaintextSMPPPassword {
		t.Fatalf("CRITICAL: raw SMPP password stored plaintext in database!")
	}

	// 2. Audit log scanning: verify that NO audit row contains plaintext secrets
	rows, err := e.Pool.Query(ctx, `SELECT action, entity_id, entity_type, COALESCE(before_value::text, ''), COALESCE(after_value::text, '') FROM audit_logs WHERE entity_id IN ($1, $2, $3)`,
		profRes.Profile.ID.String(), apiKeyRes.APIKey.ID.String(), smppRes.Account.ID.String())
	if err != nil {
		t.Fatalf("querying audit_logs: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var action, entityID, entityType, beforeVal, afterVal string
		if err := rows.Scan(&action, &entityID, &entityType, &beforeVal, &afterVal); err != nil {
			t.Fatalf("scanning audit row: %v", err)
		}
		combined := beforeVal + " " + afterVal
		if strings.Contains(combined, rawPlaintextAPIKey) {
			t.Fatalf("CRITICAL SECURITY LEAK: plaintext API key found in audit_logs (action=%s)!", action)
		}
		if strings.Contains(combined, rawPlaintextSMPPPassword) {
			t.Fatalf("CRITICAL SECURITY LEAK: plaintext SMPP password found in audit_logs (action=%s)!", action)
		}
	}

	// 3. History tables scanning
	histRows, err := e.Pool.Query(ctx, `SELECT snapshot FROM customer_profiles_history WHERE profile_id = $1`, profRes.Profile.ID)
	if err != nil {
		t.Fatalf("querying customer_profiles_history: %v", err)
	}
	defer histRows.Close()

	for histRows.Next() {
		var snap json.RawMessage
		if err := histRows.Scan(&snap); err != nil {
			t.Fatalf("scanning history snapshot: %v", err)
		}
		sStr := string(snap)
		if strings.Contains(sStr, rawPlaintextAPIKey) {
			t.Fatalf("CRITICAL SECURITY LEAK: plaintext API key found in profile history snapshot!")
		}
		if strings.Contains(sStr, rawPlaintextSMPPPassword) {
			t.Fatalf("CRITICAL SECURITY LEAK: plaintext SMPP password found in profile history snapshot!")
		}
	}
}
