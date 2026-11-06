package senderid_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/senderid"
	"github.com/znero-international/znero/backend/internal/testkit"
)

func TestValidateSenderIDFormat(t *testing.T) {
	cases := []struct {
		name       string
		senderID   string
		senderType string
		wantErr    bool
	}{
		{"valid alphanumeric", "ZNERO", "alphanumeric", false},
		{"valid alphanumeric with hyphen and space", "Bank-Alert", "alphanumeric", false},
		{"alphanumeric too short", "ZN", "alphanumeric", true},
		{"alphanumeric too long", "ABCDEFGHIJKL", "alphanumeric", true},
		{"alphanumeric digits only rejected", "123456", "alphanumeric", true},
		{"alphanumeric control char rejected", "ZNERO\n1", "alphanumeric", true},
		{"alphanumeric special char rejected", "ZNERO@1", "alphanumeric", true},
		{"valid numeric with plus", "+14155550100", "numeric", false},
		{"valid numeric digits", "8801700000000", "numeric", false},
		{"numeric with letters rejected", "+1415ABC", "numeric", true},
		{"numeric too short", "12", "numeric", true},
		{"valid shortcode", "16234", "shortcode", false},
		{"shortcode with plus rejected", "+16234", "shortcode", true},
		{"shortcode too long", "123456789", "shortcode", true},
		{"invalid sender type", "ZNERO", "unknown", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := senderid.ValidateSenderIDFormat(tc.senderID, tc.senderType)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for (%q, %q), got nil", tc.senderID, tc.senderType)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for (%q, %q): %v", tc.senderID, tc.senderType, err)
			}
		})
	}
}

func TestSenderIDRegistryIntegration(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()

	countrySvc := country.NewService(country.NewRepository(e.Pool), e.Approval, e.Audit, e.Pool)
	senderSvc := senderid.NewService(senderid.NewRepository(e.Pool), e.Audit, e.Approval)

	resellerAOrg, resellerAPath := e.ChildOrg(e.PlatformOrgID, e.PlatformOrgPath, "sid-reseller-a", "reseller")
	resellerBOrg, resellerBPath := e.ChildOrg(e.PlatformOrgID, e.PlatformOrgPath, "sid-reseller-b", "reseller")
	custAOrg, custAPath := e.ChildOrg(resellerAOrg, resellerAPath, "sid-cust-a", "customer")
	custBOrg, custBPath := e.ChildOrg(resellerBOrg, resellerBPath, "sid-cust-b", "customer")

	resellerPerms := []string{"customer.senderid.view", "customer.senderid.create", "customer.senderid.edit"}
	custPerms := []string{"customer.senderid.view", "customer.senderid.create"}

	resellerA := e.Actor("sid-res-a", resellerAOrg, resellerAPath, "reseller", false, resellerPerms...)
	resellerB := e.Actor("sid-res-b", resellerBOrg, resellerBPath, "reseller", false, resellerPerms...)
	custA := e.Actor("sid-cust-a", custAOrg, custAPath, "customer", false, custPerms...)
	custB := e.Actor("sid-cust-b", custBOrg, custBPath, "customer", false, custPerms...)

	suffix := fmt.Sprintf("%02d", time.Now().UnixNano()%90+10)
	creq := e.CountryRequest()
	creq.SenderIDPolicyNotes = "Pre-registration required; alphanumeric max 11 chars."

	cRes, err := countrySvc.Create(ctx, e.Override, creq)
	if err != nil {
		t.Fatalf("create country: %v", err)
	}
	createdCountry := cRes.(*country.Country)
	countryID := createdCountry.ID

	t.Cleanup(func() {
		_, _ = e.Pool.Exec(ctx, `DELETE FROM sender_ids WHERE organization_id IN ($1, $2)`, custAOrg, custBOrg)
		_, _ = e.Pool.Exec(ctx, `DELETE FROM countries WHERE id = $1`, countryID)
	})

	// 1. Customer A submits a Sender ID request for itself -> starts as pending
	sidName := "ZNR" + suffix
	created, err := senderSvc.Create(ctx, custA, senderid.CreateSenderIDRequest{
		OrganizationID: custAOrg,
		SenderID:       sidName,
		SenderType:     "alphanumeric",
		CountryID:      &countryID,
		DataSource:     "Customer Portal",
		Reason:         "Transactional OTP alerts",
	}, "127.0.0.1", "go-test")
	if err != nil {
		t.Fatalf("customer A create sender ID: %v", err)
	}
	if created.Status != "pending" {
		t.Fatalf("expected status pending, got %q", created.Status)
	}
	if created.CountrySenderIDPolicyNotes == nil || *created.CountrySenderIDPolicyNotes == "" {
		t.Fatalf("expected country_sender_id_policy_notes to be populated on response")
	}

	// 2. Duplicate submission for same (org, sender_id, country) is rejected
	if _, err := senderSvc.Create(ctx, custA, senderid.CreateSenderIDRequest{
		OrganizationID: custAOrg,
		SenderID:       sidName,
		SenderType:     "alphanumeric",
		CountryID:      &countryID,
	}, "127.0.0.1", "go-test"); !errors.Is(err, senderid.ErrSenderIDExists) {
		t.Fatalf("expected ErrSenderIDExists on duplicate, got %v", err)
	}

	// 3. Cross-tenant isolation:
	// Customer A cannot create or view for Customer B
	if _, err := senderSvc.Create(ctx, custA, senderid.CreateSenderIDRequest{
		OrganizationID: custBOrg,
		SenderID:       "OTHER" + suffix,
		SenderType:     "alphanumeric",
		CountryID:      &countryID,
	}, "127.0.0.1", "go-test"); !errors.Is(err, senderid.ErrCrossTenantAccess) {
		t.Fatalf("expected ErrCrossTenantAccess when customer A creates for customer B, got %v", err)
	}
	if _, err := senderSvc.Get(ctx, custB, created.ID); !errors.Is(err, senderid.ErrCrossTenantAccess) {
		t.Fatalf("expected ErrCrossTenantAccess when customer B reads customer A sender ID, got %v", err)
	}
	// Reseller A can read Customer A's sender ID, Reseller B cannot
	if _, err := senderSvc.Get(ctx, resellerA, created.ID); err != nil {
		t.Fatalf("reseller A should view child customer A sender ID: %v", err)
	}
	if _, err := senderSvc.Get(ctx, resellerB, created.ID); !errors.Is(err, senderid.ErrCrossTenantAccess) {
		t.Fatalf("expected ErrCrossTenantAccess when reseller B reads reseller A child sender ID, got %v", err)
	}

	// 4. Reseller A submits a Sender ID for child Customer A
	resSid, err := senderSvc.Create(ctx, resellerA, senderid.CreateSenderIDRequest{
		OrganizationID: custAOrg,
		SenderID:       "16" + suffix,
		SenderType:     "shortcode",
		CountryID:      &countryID,
		Reason:         "Reseller submitted shortcode",
	}, "127.0.0.1", "go-test")
	if err != nil {
		t.Fatalf("reseller A create sender ID for child: %v", err)
	}

	// 5. Maker-Checker on Sender ID approval:
	// A platform operator with both create and approve permissions (no override) submits a sender ID, and cannot self-approve it
	makerApprover := e.Actor("sid-maker-approver", e.PlatformOrgID, e.PlatformOrgPath, "platform", false,
		"customer.senderid.view", "customer.senderid.create", "customer.senderid.approve")
	makerSid, err := senderSvc.Create(ctx, makerApprover, senderid.CreateSenderIDRequest{
		OrganizationID: custAOrg,
		SenderID:       "MKR" + suffix,
		SenderType:     "alphanumeric",
		CountryID:      &countryID,
		Reason:         "Maker submitted sender ID",
	}, "127.0.0.1", "go-test")
	if err != nil {
		t.Fatalf("maker create sender ID: %v", err)
	}
	if _, err := senderSvc.Approve(ctx, makerApprover, makerSid.ID, senderid.ReviewSenderIDRequest{
		Notes:  "Self approve attempt",
		Reason: "Self approve attempt",
	}, "127.0.0.1", "go-test"); !errors.Is(err, senderid.ErrMakerCannotApproveOwnSenderID) {
		t.Fatalf("expected ErrMakerCannotApproveOwnSenderID, got %v", err)
	}

	// Reseller and Customer cannot approve
	if _, err := senderSvc.Approve(ctx, resellerA, created.ID, senderid.ReviewSenderIDRequest{
		Reason: "Reseller approve attempt",
	}, "127.0.0.1", "go-test"); !errors.Is(err, senderid.ErrPermissionDenied) {
		t.Fatalf("expected ErrPermissionDenied when reseller approves, got %v", err)
	}

	// Checker approves Maker's sender ID
	approved, err := senderSvc.Approve(ctx, e.Checker, makerSid.ID, senderid.ReviewSenderIDRequest{
		Notes:  "Verified letter of authorization",
		Reason: "LOA verified",
	}, "127.0.0.1", "go-test")
	if err != nil {
		t.Fatalf("checker approve sender ID: %v", err)
	}
	if approved.Status != "approved" || approved.Version != 2 {
		t.Fatalf("expected status=approved version=2, got status=%q version=%d", approved.Status, approved.Version)
	}

	// 6. Reject requires a non-empty reason
	if _, err := senderSvc.Reject(ctx, e.Checker, resSid.ID, senderid.ReviewSenderIDRequest{
		Notes:  "   ",
		Reason: "",
	}, "127.0.0.1", "go-test"); !errors.Is(err, senderid.ErrRejectReasonRequired) {
		t.Fatalf("expected ErrRejectReasonRequired on empty reject reason, got %v", err)
	}
	rejected, err := senderSvc.Reject(ctx, e.Checker, resSid.ID, senderid.ReviewSenderIDRequest{
		Reason: "Missing operator NOC registration document",
	}, "127.0.0.1", "go-test")
	if err != nil {
		t.Fatalf("checker reject sender ID: %v", err)
	}
	if rejected.Status != "rejected" {
		t.Fatalf("expected status=rejected, got %q", rejected.Status)
	}

	// 7. Versioning & Rollback creates a new version (version 3)
	rolledBack, err := senderSvc.Rollback(ctx, e.Override, approved.ID, 1, "Rollback to pending for re-verification", "127.0.0.1", "go-test")
	if err != nil {
		t.Fatalf("rollback sender ID: %v", err)
	}
	if rolledBack.Version != 3 || rolledBack.Status != "pending" {
		t.Fatalf("expected version=3 status=pending after rollback, got version=%d status=%q", rolledBack.Version, rolledBack.Status)
	}
	hist, err := senderSvc.ListHistory(ctx, e.Override, approved.ID)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(hist) != 3 {
		t.Fatalf("expected 3 history rows, got %d", len(hist))
	}
}
