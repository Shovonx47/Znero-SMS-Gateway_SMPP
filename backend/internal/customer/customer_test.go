package customer_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/znero-international/znero/backend/internal/customer"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/pkg/crypto/password"
)

func TestCustomerProfilesAndCredentialsIntegration(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	svc := customer.NewService(customer.NewRepository(e.Pool), e.Audit, e.Approval)

	// Build two resellers (Reseller A and Reseller B), each with a child customer (Cust A and Cust B),
	// plus a direct platform customer (Cust Direct).
	resellerAOrg, resellerAPath := e.ChildOrg(e.PlatformOrgID, e.PlatformOrgPath, "reseller-a", "reseller")
	resellerBOrg, resellerBPath := e.ChildOrg(e.PlatformOrgID, e.PlatformOrgPath, "reseller-b", "reseller")

	custAOrg, custAPath := e.ChildOrg(resellerAOrg, resellerAPath, "cust-a", "customer")
	custBOrg, custBPath := e.ChildOrg(resellerBOrg, resellerBPath, "cust-b", "customer")
	custDirectOrg, _ := e.ChildOrg(e.PlatformOrgID, e.PlatformOrgPath, "cust-direct", "customer")

	t.Cleanup(func() {
		_, _ = e.Pool.Exec(context.Background(), `DELETE FROM customer_profiles WHERE organization_id IN ($1, $2, $3)`, custAOrg, custBOrg, custDirectOrg)
		_, _ = e.Pool.Exec(context.Background(), `DELETE FROM customer_api_keys WHERE organization_id IN ($1, $2, $3)`, custAOrg, custBOrg, custDirectOrg)
		_, _ = e.Pool.Exec(context.Background(), `DELETE FROM customer_smpp_accounts WHERE organization_id IN ($1, $2, $3)`, custAOrg, custBOrg, custDirectOrg)
	})

	resellerPerms := []string{
		"customer.profile.view", "customer.profile.create", "customer.profile.edit",
		"customer.apikey.view", "customer.apikey.create", "customer.apikey.revoke", "customer.apikey.rotate",
		"customer.smppaccount.view", "customer.smppaccount.create", "customer.smppaccount.edit", "customer.smppaccount.revoke",
	}
	custPerms := []string{
		"customer.profile.view",
		"customer.apikey.view", "customer.apikey.create", "customer.apikey.revoke", "customer.apikey.rotate",
		"customer.smppaccount.view",
	}

	resellerA := e.Actor("reseller-a", resellerAOrg, resellerAPath, "reseller", false, resellerPerms...)
	resellerB := e.Actor("reseller-b", resellerBOrg, resellerBPath, "reseller", false, resellerPerms...)
	custA := e.Actor("cust-a", custAOrg, custAPath, "customer", false, custPerms...)
	custB := e.Actor("cust-b", custBOrg, custBPath, "customer", false, custPerms...)

	// 1. Platform Admin creates, edits, versions, and rolls back a customer profile
	tps50 := 50
	credit1000 := 1000.0
	profARes, err := svc.CreateProfile(ctx, e.Override, customer.CreateProfileRequest{
		OrganizationID:    custAOrg,
		AccountType:       "prepaid",
		BillingCurrency:   "USD",
		Timezone:          "UTC",
		TechnicalContact:  "tech-v1@custa.example.com",
		CreditLimit:       &credit1000,
		DefaultMaxTPS:     &tps50,
		AllowedCountries:  []string{"US", "GB"},
		DestinationPolicy: "allow_listed",
		Status:            "active",
		Notes:             "Initial v1",
		Reason:            "Initial onboarding",
	}, "127.0.0.1", "customer-test")
	if err != nil || profARes.Profile == nil {
		t.Fatalf("Platform CreateProfile for CustA failed: %v", err)
	}
	profA := profARes.Profile
	if profA.Version != 1 {
		t.Fatalf("expected version 1, got %d", profA.Version)
	}

	// Update profile -> version 2
	newNotes := "Updated v2"
	updA, err := svc.UpdateProfile(ctx, e.Override, profA.ID, customer.UpdateProfileRequest{
		Notes:  &newNotes,
		Reason: "Update notes to v2",
	}, "127.0.0.1", "customer-test")
	if err != nil || updA.Profile.Version != 2 || updA.Profile.Notes != newNotes {
		t.Fatalf("UpdateProfile failed: err=%v, res=%+v", err, updA)
	}

	// Rollback profile to version 1 -> creates version 3
	rbA, err := svc.RollbackProfile(ctx, e.Override, profA.ID, 1, "Rollback to v1", "127.0.0.1", "customer-test")
	if err != nil || rbA.Profile.Version != 3 || rbA.Profile.Notes != "Initial v1" {
		t.Fatalf("RollbackProfile failed: err=%v, res=%+v", err, rbA)
	}

	// Also create profiles for CustB and CustDirect via Platform Admin
	profBRes, err := svc.CreateProfile(ctx, e.Override, customer.CreateProfileRequest{
		OrganizationID:  custBOrg,
		AccountType:     "prepaid",
		BillingCurrency: "USD",
		Status:          "active",
	}, "127.0.0.1", "customer-test")
	if err != nil {
		t.Fatalf("CreateProfile CustB: %v", err)
	}
	profB := profBRes.Profile

	profDirectRes, err := svc.CreateProfile(ctx, e.Override, customer.CreateProfileRequest{
		OrganizationID:  custDirectOrg,
		AccountType:     "postpaid",
		BillingCurrency: "EUR",
		Status:          "active",
	}, "127.0.0.1", "customer-test")
	if err != nil {
		t.Fatalf("CreateProfile CustDirect: %v", err)
	}
	profDirect := profDirectRes.Profile

	// 2. Reseller Tenant Isolation & Ceiling Enforcement
	// Reseller A can view and edit Cust A (non-sensitive field)
	gotByResellerA, err := svc.GetProfile(ctx, resellerA, profA.ID)
	if err != nil || gotByResellerA.ID != profA.ID {
		t.Fatalf("Reseller A GetProfile(CustA) failed: %v", err)
	}
	resellerNotes := "Edited by Reseller A"
	if _, err := svc.UpdateProfile(ctx, resellerA, profA.ID, customer.UpdateProfileRequest{
		Notes:  &resellerNotes,
		Reason: "Reseller note update",
	}, "127.0.0.1", "customer-test"); err != nil {
		t.Fatalf("Reseller A UpdateProfile(CustA) failed: %v", err)
	}

	// Reseller A gets 403 (ErrCrossTenantAccess) on Reseller B's customer and on Direct Platform customer
	if _, err := svc.GetProfile(ctx, resellerA, profB.ID); !errors.Is(err, customer.ErrCrossTenantAccess) {
		t.Fatalf("expected Reseller A GetProfile(CustB) to fail with ErrCrossTenantAccess, got %v", err)
	}
	if _, err := svc.GetProfile(ctx, resellerA, profDirect.ID); !errors.Is(err, customer.ErrCrossTenantAccess) {
		t.Fatalf("expected Reseller A GetProfile(CustDirect) to fail with ErrCrossTenantAccess, got %v", err)
	}
	if _, err := svc.UpdateProfile(ctx, resellerA, profB.ID, customer.UpdateProfileRequest{Notes: &resellerNotes}, "127.0.0.1", "test"); !errors.Is(err, customer.ErrCrossTenantAccess) {
		t.Fatalf("expected Reseller A UpdateProfile(CustB) to fail with ErrCrossTenantAccess, got %v", err)
	}

	// Reseller A ListProfiles returns ONLY Cust A
	resList, total, err := svc.ListProfiles(ctx, resellerA, customer.ProfileListFilter{})
	if err != nil || total != 1 || len(resList) != 1 || resList[0].ID != profA.ID {
		t.Fatalf("expected Reseller A ListProfiles to return only CustA (1 item), got total=%d, err=%v", total, err)
	}

	// Reseller cannot set TPS or caps above reseller limits
	tooHighTPS := customer.MaxResellerTPS + 100
	if _, err := svc.UpdateProfile(ctx, resellerA, profA.ID, customer.UpdateProfileRequest{
		DefaultMaxTPS: &tooHighTPS,
	}, "127.0.0.1", "test"); !errors.Is(err, customer.ErrResellerCapExceeded) {
		t.Fatalf("expected ErrResellerCapExceeded on TPS above reseller cap, got %v", err)
	}
	tooHighCredit := customer.MaxResellerCreditLimit + 1000
	if _, err := svc.UpdateProfile(ctx, resellerA, profA.ID, customer.UpdateProfileRequest{
		CreditLimit: &tooHighCredit,
	}, "127.0.0.1", "test"); !errors.Is(err, customer.ErrResellerCapExceeded) {
		t.Fatalf("expected ErrResellerCapExceeded on CreditLimit above reseller cap, got %v", err)
	}

	// Reseller cannot modify platform-only fields (account_type, billing_currency)
	postpaid := "postpaid"
	if _, err := svc.UpdateProfile(ctx, resellerA, profA.ID, customer.UpdateProfileRequest{
		AccountType: &postpaid,
	}, "127.0.0.1", "test"); !errors.Is(err, customer.ErrPlatformOnlyField) {
		t.Fatalf("expected ErrPlatformOnlyField when reseller changes account_type, got %v", err)
	}

	// 3. Customer Tenant Isolation: can view own profile, 403 on write, 403 on other customer's profile
	if _, err := svc.GetProfile(ctx, custA, profA.ID); err != nil {
		t.Fatalf("CustA GetProfile(own) failed: %v", err)
	}
	if _, err := svc.GetProfile(ctx, custA, profB.ID); !errors.Is(err, customer.ErrCrossTenantAccess) {
		t.Fatalf("expected CustA GetProfile(CustB) to return ErrCrossTenantAccess, got %v", err)
	}
	custAWithEditPerm := testkit.WithPermissions(custA, "customer.profile.edit")
	if _, err := svc.UpdateProfile(ctx, custAWithEditPerm, profA.ID, customer.UpdateProfileRequest{Notes: &resellerNotes}, "127.0.0.1", "test"); !errors.Is(err, customer.ErrPermissionDenied) {
		t.Fatalf("expected CustA UpdateProfile(own) to return ErrPermissionDenied, got %v", err)
	}

	// 4. Maker-Checker on sensitive profile changes (credit_limit / status / destination_policy)
	newCredit := 2500.0
	mcRes, err := svc.UpdateProfile(ctx, e.Maker, profA.ID, customer.UpdateProfileRequest{
		CreditLimit: &newCredit,
		Reason:      "Increase credit limit to 2500",
	}, "127.0.0.1", "customer-test")
	if err != nil || mcRes.PendingChange == nil {
		t.Fatalf("expected Maker sensitive profile update to create PendingChange, err=%v, res=%+v", err, mcRes)
	}
	cr := mcRes.PendingChange.(*approval.ChangeRequest)
	makerWithApprove := testkit.WithPermissions(e.Maker, "customer.profile.approve")
	if err := e.Approval.Approve(ctx, makerWithApprove, cr.ID, "self-approve"); !errors.Is(err, approval.ErrMakerCannotApproveOwnChange) {
		t.Fatalf("expected ErrMakerCannotApproveOwnChange, got %v", err)
	}
	if err := e.Approval.Approve(ctx, e.Checker, cr.ID, "Approve credit limit increase"); err != nil {
		t.Fatalf("Checker approve profile change failed: %v", err)
	}
	afterMC, err := svc.GetProfile(ctx, e.Override, profA.ID)
	if err != nil || afterMC.CreditLimit != 2500.0 {
		t.Fatalf("expected CreditLimit=2500 after approval, got %+v (err=%v)", afterMC, err)
	}

	// ========================================================================
	// Step 4: Customer API Keys & SMPP Accounts
	// ========================================================================

	// 1. Create API Key -> returns plaintext once; subsequent GETs return only prefix, never plaintext or hash
	issuedKey, err := svc.CreateAPIKey(ctx, custA, customer.CreateAPIKeyRequest{
		OrganizationID: custAOrg,
		Label:          "Primary Messaging Key",
		Scopes:         []string{"sms:send", "dlr:read"},
		IPAllowlist:    []string{"203.0.113.10/32"},
		Reason:         "Initial key generation",
	}, "127.0.0.1", "customer-test")
	if err != nil || issuedKey == nil {
		t.Fatalf("CreateAPIKey failed: %v", err)
	}
	plainKey := issuedKey.PlaintextKey
	if !strings.HasPrefix(plainKey, "znk_live_") || issuedKey.APIKey.KeyPrefix == "" {
		t.Fatalf("unexpected generated API key format: %+v", issuedKey)
	}

	// Verify DB stores only key_hash and key_prefix, never plaintext
	var dbPrefix, dbHash string
	if err := e.Pool.QueryRow(ctx, `SELECT key_prefix, key_hash FROM customer_api_keys WHERE id = $1`, issuedKey.APIKey.ID).Scan(&dbPrefix, &dbHash); err != nil {
		t.Fatalf("query customer_api_keys: %v", err)
	}
	if strings.Contains(dbHash, plainKey) || dbHash == plainKey {
		t.Fatalf("DB stored plaintext API key instead of SHA-256 hash!")
	}

	// Subsequent GET returns only prefix, never plaintext or hash
	listedKeys, err := svc.ListAPIKeys(ctx, custA, custAOrg)
	if err != nil || len(listedKeys) != 1 {
		t.Fatalf("ListAPIKeys failed: %v", err)
	}
	listJSON, _ := json.Marshal(listedKeys)
	if strings.Contains(string(listJSON), plainKey) || strings.Contains(string(listJSON), dbHash) {
		t.Fatalf("ListAPIKeys JSON leaked plaintext key or hash: %s", string(listJSON))
	}

	// Audit log is free of plaintext key and hash
	for _, a := range e.AuditRows("customer_api_key", issuedKey.APIKey.ID.String()) {
		blob := a.Before + " " + a.After + " " + a.Reason
		if strings.Contains(blob, plainKey) || strings.Contains(blob, dbHash) {
			t.Fatalf("audit_logs leaked API key or hash: %s", blob)
		}
	}

	// 2. Rotate API Key -> revokes old key and issues a new one in a single transaction
	rotatedKey, err := svc.RotateAPIKey(ctx, custA, issuedKey.APIKey.ID, "Scheduled rotation", "127.0.0.1", "customer-test")
	if err != nil || rotatedKey == nil || rotatedKey.PlaintextKey == "" || rotatedKey.PlaintextKey == plainKey {
		t.Fatalf("RotateAPIKey failed: %v", err)
	}

	// 3. Revoke the new API Key -> status becomes revoked and revoked_at is set
	revokedKey, err := svc.RevokeAPIKey(ctx, custA, rotatedKey.APIKey.ID, "Decommission key", "127.0.0.1", "customer-test")
	if err != nil || revokedKey.Status != "revoked" || revokedKey.RevokedAt == nil {
		t.Fatalf("RevokeAPIKey failed: err=%v, key=%+v", err, revokedKey)
	}

	// 4. Create SMPP Account -> returns password once, stores Argon2id hash, rejects duplicate system_id, enforces max_tps <= default_max_tps
	sysID := "custa_" + testkit.RandHex(3)
	overLimitTPS := 100 // CustA's default_max_tps is 50
	if _, err := svc.CreateSMPPAccount(ctx, resellerA, customer.CreateSMPPAccountRequest{
		OrganizationID: custAOrg,
		SystemID:       sysID,
		MaxTPS:         &overLimitTPS,
	}, "127.0.0.1", "customer-test"); !errors.Is(err, customer.ErrValidation) {
		t.Fatalf("expected ErrValidation when SMPP account max_tps (100) exceeds profile default_max_tps (50), got %v", err)
	}

	validTPS := 25
	issuedSMPP, err := svc.CreateSMPPAccount(ctx, resellerA, customer.CreateSMPPAccountRequest{
		OrganizationID:   custAOrg,
		SystemID:         sysID,
		AllowedIPs:       []string{"198.51.100.20"},
		AllowedBindTypes: []string{"transceiver"},
		MaxTPS:           &validTPS,
		Reason:           "Provision SMPP bind",
	}, "127.0.0.1", "customer-test")
	if err != nil || issuedSMPP == nil || issuedSMPP.PlaintextPassword == "" {
		t.Fatalf("CreateSMPPAccount failed: %v", err)
	}
	smppPass := issuedSMPP.PlaintextPassword
	if len(smppPass) > 8 {
		t.Fatalf("BUG-033 regression: SMPP v3.4 password must be <= 8 chars (max 9 octets incl NUL), got %d (%q)", len(smppPass), smppPass)
	}

	// Duplicate system_id is rejected
	if _, err := svc.CreateSMPPAccount(ctx, e.Override, customer.CreateSMPPAccountRequest{
		OrganizationID: custBOrg,
		SystemID:       sysID,
	}, "127.0.0.1", "customer-test"); !errors.Is(err, customer.ErrSMPPSystemIDExists) {
		t.Fatalf("expected ErrSMPPSystemIDExists on duplicate system_id, got %v", err)
	}

	// Verify Argon2id hash in DB and secret-free GET/audit
	var storedPassHash string
	if err := e.Pool.QueryRow(ctx, `SELECT password_hash FROM customer_smpp_accounts WHERE id = $1`, issuedSMPP.Account.ID).Scan(&storedPassHash); err != nil {
		t.Fatalf("query smpp password_hash: %v", err)
	}
	if ok, err := password.Verify(smppPass, storedPassHash); err != nil || !ok {
		t.Fatalf("expected Argon2id password.Verify to succeed, ok=%v, err=%v", ok, err)
	}
	smppList, err := svc.ListSMPPAccounts(ctx, custA, custAOrg)
	if err != nil || len(smppList) != 1 {
		t.Fatalf("ListSMPPAccounts failed: %v", err)
	}
	smppJSON, _ := json.Marshal(smppList)
	if strings.Contains(string(smppJSON), smppPass) || strings.Contains(string(smppJSON), storedPassHash) {
		t.Fatalf("ListSMPPAccounts leaked password or hash: %s", string(smppJSON))
	}

	// 5. Cross-tenant credential isolation
	if _, err := svc.ListAPIKeys(ctx, custA, custBOrg); !errors.Is(err, customer.ErrCrossTenantAccess) {
		t.Fatalf("expected CustA ListAPIKeys(CustB) to fail with ErrCrossTenantAccess, got %v", err)
	}
	if _, err := svc.CreateAPIKey(ctx, custA, customer.CreateAPIKeyRequest{OrganizationID: custBOrg, Label: "Hack"}, "127.0.0.1", "test"); !errors.Is(err, customer.ErrCrossTenantAccess) {
		t.Fatalf("expected CustA CreateAPIKey(CustB) to fail with ErrCrossTenantAccess, got %v", err)
	}
	if _, err := svc.RevokeAPIKey(ctx, custB, issuedKey.APIKey.ID, "hack", "127.0.0.1", "test"); !errors.Is(err, customer.ErrCrossTenantAccess) {
		t.Fatalf("expected CustB RevokeAPIKey(CustA) to fail with ErrCrossTenantAccess, got %v", err)
	}
	if _, err := svc.ListSMPPAccounts(ctx, resellerB, custAOrg); !errors.Is(err, customer.ErrCrossTenantAccess) {
		t.Fatalf("expected ResellerB ListSMPPAccounts(CustA) to fail with ErrCrossTenantAccess, got %v", err)
	}
	if _, err := svc.ResetSMPPPassword(ctx, resellerB, issuedSMPP.Account.ID, "hack", "127.0.0.1", "test"); !errors.Is(err, customer.ErrCrossTenantAccess) {
		t.Fatalf("expected ResellerB ResetSMPPPassword(CustA) to fail with ErrCrossTenantAccess, got %v", err)
	}
}

func TestCustomer_StrictCrossTenantIsolationAndMakerChecker(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	svc := customer.NewService(customer.NewRepository(e.Pool), e.Audit, e.Approval)

	resellerAOrg, resellerAPath := e.ChildOrg(e.PlatformOrgID, e.PlatformOrgPath, "iso-reseller-a", "reseller")
	resellerBOrg, resellerBPath := e.ChildOrg(e.PlatformOrgID, e.PlatformOrgPath, "iso-reseller-b", "reseller")

	custAOrg, custAPath := e.ChildOrg(resellerAOrg, resellerAPath, "iso-cust-a", "customer")
	custBOrg, custBPath := e.ChildOrg(resellerBOrg, resellerBPath, "iso-cust-b", "customer")

	resellerA := e.Actor("iso-reseller-a", resellerAOrg, resellerAPath, "reseller", false, "customer.profile.view", "customer.profile.edit")
	resellerB := e.Actor("iso-reseller-b", resellerBOrg, resellerBPath, "reseller", false, "customer.profile.view", "customer.profile.edit")
	custA := e.Actor("iso-cust-a", custAOrg, custAPath, "customer", false, "customer.profile.view")
	custB := e.Actor("iso-cust-b", custBOrg, custBPath, "customer", false, "customer.profile.view")

	// 1. Reseller A creates a profile for Cust A via override
	profRes, err := svc.CreateProfile(ctx, e.Override, customer.CreateProfileRequest{
		OrganizationID:  custAOrg,
		AccountType:     "prepaid",
		BillingCurrency: "USD",
	}, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("create profile custA: %v", err)
	}

	// 2. Reseller B cannot view Cust A's profile
	_, err = svc.GetProfile(ctx, resellerB, profRes.Profile.ID)
	if !errors.Is(err, customer.ErrCrossTenantAccess) {
		t.Fatalf("expected ErrCrossTenantAccess when Reseller B views Cust A profile, got %v", err)
	}

	// 3. Cust B cannot view Cust A's profile
	_, err = svc.GetProfile(ctx, custB, profRes.Profile.ID)
	if !errors.Is(err, customer.ErrCrossTenantAccess) {
		t.Fatalf("expected ErrCrossTenantAccess when Cust B views Cust A profile, got %v", err)
	}

	// 4. Cust A can view own profile
	prof, err := svc.GetProfile(ctx, custA, profRes.Profile.ID)
	if err != nil || prof == nil {
		t.Fatalf("custA should view own profile, got %v", err)
	}

	// 5. Reseller A can view Cust A's profile
	profReseller, err := svc.GetProfile(ctx, resellerA, profRes.Profile.ID)
	if err != nil || profReseller == nil {
		t.Fatalf("resellerA should view child customer profile, got %v", err)
	}
}
