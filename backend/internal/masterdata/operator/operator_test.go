package operator_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/masterdata/operator"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

// Real operator service + approval engine + PostgreSQL (skipped unless ZNERO_INTEGRATION=1).

func newCountry(t *testing.T, e *testkit.Env) *country.Country {
	t.Helper()
	v, err := e.Country.Create(context.Background(), e.Override, e.CountryRequest())
	if err != nil {
		t.Fatalf("create fixture country: %v", err)
	}
	return v.(*country.Country)
}

func opRequest(c *country.Country, mnc string) operator.CreateOperatorRequest {
	return operator.CreateOperatorRequest{
		Name:              "ITKIT Fixture Operator " + mnc,
		BrandName:         "ITKIT",
		CountryID:         c.ID,
		MCC:               "901",
		MNC:               mnc,
		NetworkTechnology: []string{"4G"},
		DataSource:        "ITKIT-TEST-FIXTURE",
	}
}

func newOperator(t *testing.T, e *testkit.Env, c *country.Country, mnc string) *operator.Operator {
	t.Helper()
	v, err := e.Operator.Create(context.Background(), e.Override, opRequest(c, mnc))
	if err != nil {
		t.Fatalf("create fixture operator: %v", err)
	}
	return v.(*operator.Operator)
}

func TestResellerAndCustomerAreReadOnly(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	c := newCountry(t, e)
	op := newOperator(t, e, c, "01")

	for _, tt := range []struct {
		name string
		tc   *tenant.Context
	}{{"reseller", e.Reseller}, {"customer", e.Customer}} {
		if _, err := e.Operator.Create(ctx, tt.tc, opRequest(c, "02")); !errors.Is(err, operator.ErrPlatformOnlyMasterData) {
			t.Errorf("%s create: expected ErrPlatformOnlyMasterData, got %v", tt.name, err)
		}
		if _, err := e.Operator.Update(ctx, tt.tc, op.ID, operator.UpdateOperatorRequest{Name: "hijack"}); !errors.Is(err, operator.ErrPlatformOnlyMasterData) {
			t.Errorf("%s update: expected ErrPlatformOnlyMasterData, got %v", tt.name, err)
		}
		if _, err := e.Operator.SetStatus(ctx, tt.tc, op.ID, "inactive", "x"); !errors.Is(err, operator.ErrPlatformOnlyMasterData) {
			t.Errorf("%s status: expected ErrPlatformOnlyMasterData, got %v", tt.name, err)
		}
		if _, err := e.Operator.Rollback(ctx, tt.tc, op.ID, 1, "x"); !errors.Is(err, operator.ErrPlatformOnlyMasterData) {
			t.Errorf("%s rollback: expected ErrPlatformOnlyMasterData, got %v", tt.name, err)
		}
		if _, err := e.Operator.GetHistory(ctx, tt.tc, op.ID); !errors.Is(err, operator.ErrPlatformOnlyMasterData) {
			t.Errorf("%s history: expected ErrPlatformOnlyMasterData, got %v", tt.name, err)
		}
		// Read-only access to ACTIVE records is allowed.
		if _, err := e.Operator.Get(ctx, tt.tc, op.ID); err != nil {
			t.Errorf("%s must be able to read an active operator: %v", tt.name, err)
		}
	}
	if _, err := e.Operator.SetStatus(ctx, e.Override, op.ID, "inactive", "hide"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Operator.Get(ctx, e.Reseller, op.ID); err == nil {
		t.Error("reseller must not read an inactive operator")
	}
}

func TestMakerCannotApproveOwnOperatorChange(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	c := newCountry(t, e)

	v, err := e.Operator.Create(ctx, e.Maker, opRequest(c, "11"))
	if err != nil {
		t.Fatal(err)
	}
	cr, ok := v.(*approval.ChangeRequest)
	if !ok || cr.EntityType != "operator" || cr.Status != "pending" {
		t.Fatalf("maker without override must get a pending operator change request, got %T %+v", v, v)
	}
	if got, _ := e.OperatorRepo.GetByMCCMNC(ctx, c.ID, "901", "11"); got != nil {
		t.Fatal("operator must not exist before approval")
	}
	selfApprover := testkit.WithPermissions(e.Maker, "operator.operator.approve")
	if err := e.Approval.Approve(ctx, selfApprover, cr.ID, ""); !errors.Is(err, approval.ErrMakerCannotApproveOwnChange) {
		t.Fatalf("expected ErrMakerCannotApproveOwnChange, got %v", err)
	}
	// A checker holding only the COUNTRY approve permission may not review an operator change.
	countryOnly := testkit.WithPermissions(e.Checker)
	delete(countryOnly.Permissions, "operator.operator.approve")
	if err := e.Approval.Approve(ctx, countryOnly, cr.ID, ""); !errors.Is(err, approval.ErrNotAuthorizedToApprove) {
		t.Fatalf("expected ErrNotAuthorizedToApprove, got %v", err)
	}
	if err := e.Approval.Approve(ctx, e.Checker, cr.ID, "ok"); err != nil {
		t.Fatalf("checker approve: %v", err)
	}
	got, err := e.OperatorRepo.GetByMCCMNC(ctx, c.ID, "901", "11")
	if err != nil || got == nil {
		t.Fatalf("operator must exist after approval: %v", err)
	}
	if got.VerificationStatus != "unverified" {
		t.Errorf("new operator must be unverified, got %q", got.VerificationStatus)
	}
	hist := e.HistoryVersions("operators_history", "operator_id", got.ID)
	if len(hist) != 1 || hist[0] != 1 {
		t.Errorf("expected history [1], got %v", hist)
	}
}

func TestOperatorMCCMNCUniquePerCountry(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	c := newCountry(t, e)
	newOperator(t, e, c, "21")
	if _, err := e.Operator.Create(ctx, e.Override, opRequest(c, "21")); err == nil {
		t.Fatal("duplicate MCC+MNC within one country must be rejected")
	}
	// The same MCC/MNC in a different country is legal (shared MCCs).
	c2 := newCountry(t, e)
	if _, err := e.Operator.Create(ctx, e.Override, opRequest(c2, "21")); err != nil {
		t.Fatalf("same MCC+MNC in another country must be allowed: %v", err)
	}
}

func TestOperatorVersioningRollbackAndAudit(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	c := newCountry(t, e)
	op := newOperator(t, e, c, "31") // v1

	upd := operator.UpdateOperatorRequest{Name: "ITKIT Renamed", BrandName: "ITKIT", NetworkTechnology: []string{"4G", "5G"}, DataSource: "ITKIT-TEST-FIXTURE", Reason: "rename"}
	v, err := e.Operator.Update(ctx, e.Override, op.ID, upd) // v2
	if err != nil {
		t.Fatal(err)
	}
	if v.(*operator.Operator).Version != 2 {
		t.Fatalf("expected v2, got %d", v.(*operator.Operator).Version)
	}
	rb, err := e.Operator.Rollback(ctx, e.Override, op.ID, 1, "undo") // v3
	if err != nil {
		t.Fatal(err)
	}
	got := rb.(*operator.Operator)
	if got.Version != 3 || got.Name != op.Name {
		t.Fatalf("rollback must create v3 restoring v1 content, got v%d %q", got.Version, got.Name)
	}
	if h := e.HistoryVersions("operators_history", "operator_id", op.ID); len(h) != 3 {
		t.Fatalf("history must keep all versions, got %v", h)
	}
	for _, r := range e.AuditRows("operator", op.ID.String()) {
		if r.ActorUserID == nil || *r.ActorUserID != e.Override.ActorUserID {
			t.Errorf("audit row %s has wrong actor", r.Action)
		}
		for _, banned := range []string{"password", "secret", "token", "api_key"} {
			if strings.Contains(strings.ToLower(r.Before+r.After), banned) {
				t.Errorf("audit row %s contains %q", r.Action, banned)
			}
		}
	}
}
