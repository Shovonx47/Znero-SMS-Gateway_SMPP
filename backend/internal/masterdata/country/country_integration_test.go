package country_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

// These tests exercise the REAL country service, approval engine, repositories and audit trail
// against the real PostgreSQL (skipped unless ZNERO_INTEGRATION=1).

func mustCountry(t *testing.T, v any, err error) *country.Country {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c, ok := v.(*country.Country)
	if !ok {
		t.Fatalf("expected *country.Country (direct write), got %T", v)
	}
	return c
}

func mustChangeRequest(t *testing.T, v any, err error) *approval.ChangeRequest {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cr, ok := v.(*approval.ChangeRequest)
	if !ok {
		t.Fatalf("expected *approval.ChangeRequest (maker-checker path), got %T", v)
	}
	return cr
}

func TestResellerAndCustomerCannotWrite(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	for _, tt := range []struct {
		name string
		tc   *tenant.Context
	}{{"reseller", e.Reseller}, {"customer", e.Customer}} {
		name, tc := tt.name, tt.tc
		if _, err := e.Country.Create(ctx, tc, e.CountryRequest()); !errors.Is(err, country.ErrPlatformOnlyMasterData) {
			t.Errorf("%s create: expected ErrPlatformOnlyMasterData, got %v", name, err)
		}
		c := mustCountry(t, mustDirect(t, e), nil)
		if _, err := e.Country.SetStatus(ctx, tc, c.ID, "inactive", "x"); !errors.Is(err, country.ErrPlatformOnlyMasterData) {
			t.Errorf("%s status change: expected ErrPlatformOnlyMasterData, got %v", name, err)
		}
		if _, err := e.Country.Rollback(ctx, tc, c.ID, 1, "x"); !errors.Is(err, country.ErrPlatformOnlyMasterData) {
			t.Errorf("%s rollback: expected ErrPlatformOnlyMasterData, got %v", name, err)
		}
		if _, err := e.Country.GetHistory(ctx, tc, c.ID); !errors.Is(err, country.ErrPlatformOnlyMasterData) {
			t.Errorf("%s history: expected ErrPlatformOnlyMasterData, got %v", name, err)
		}
	}
}

func mustDirect(t *testing.T, e *testkit.Env) any {
	t.Helper()
	v, err := e.Country.Create(context.Background(), e.Override, e.CountryRequest())
	if err != nil {
		t.Fatalf("override create: %v", err)
	}
	return v
}

func TestResellerSeesOnlyActiveRecords(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	c := mustCountry(t, mustDirect(t, e), nil)
	if _, err := e.Country.SetStatus(ctx, e.Override, c.ID, "inactive", "test deactivate"); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if _, err := e.Country.Get(ctx, e.Reseller, c.ID); !errors.Is(err, country.ErrCountryNotFound) {
		t.Errorf("reseller must not see inactive country, got %v", err)
	}
	if _, err := e.Country.Get(ctx, e.Customer, c.ID); !errors.Is(err, country.ErrCountryNotFound) {
		t.Errorf("customer must not see inactive country, got %v", err)
	}
	res, err := e.Country.List(ctx, e.Reseller, country.Filter{Search: c.ISO2, Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range res.Items {
		if it.ID == c.ID {
			t.Error("reseller list returned an inactive country")
		}
	}
	if got, err := e.Country.Get(ctx, e.Maker, c.ID); err != nil || got.Status != "inactive" {
		t.Errorf("platform user must see inactive country, got %v / %v", got, err)
	}
}

func TestMakerWithoutOverrideCreatesPendingChangeAndCheckerApproves(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	req := e.CountryRequest()

	cr := crOf(t)(e.Country.Create(ctx, e.Maker, req))
	if cr.Status != "pending" || cr.MakerUserID != e.Maker.ActorUserID || cr.EntityType != "country" || cr.Operation != "create" {
		t.Fatalf("unexpected change request: %+v", cr)
	}
	if existing, _ := e.CountryRepo.GetByISO2(ctx, req.ISO2); existing != nil {
		t.Fatal("country must NOT exist before approval")
	}

	// The maker can never approve (or reject) their own change. A plain maker lacks the approve
	// permission altogether; a maker who DOES hold it is refused by the maker != checker rule.
	if err := e.Approval.Approve(ctx, e.Maker, cr.ID, "self approve"); !errors.Is(err, approval.ErrNotAuthorizedToApprove) {
		t.Fatalf("maker without approve permission: expected ErrNotAuthorizedToApprove, got %v", err)
	}
	makerWithApprove := testkit.WithPermissions(e.Maker, "country.country.approve")
	if err := e.Approval.Approve(ctx, makerWithApprove, cr.ID, "self approve"); !errors.Is(err, approval.ErrMakerCannotApproveOwnChange) {
		t.Fatalf("maker holding approve permission must still be refused, got %v", err)
	}
	if err := e.Approval.Reject(ctx, makerWithApprove, cr.ID, "self reject"); !errors.Is(err, approval.ErrMakerCannotApproveOwnChange) {
		t.Fatalf("maker self-reject: expected ErrMakerCannotApproveOwnChange, got %v", err)
	}
	if existing, _ := e.CountryRepo.GetByISO2(ctx, req.ISO2); existing != nil {
		t.Fatal("refused approvals must not create the country")
	}

	// A different user with the approve permission approves; the change appears.
	if err := e.Approval.Approve(ctx, e.Checker, cr.ID, "looks right"); err != nil {
		t.Fatalf("checker approve: %v", err)
	}
	created, err := e.CountryRepo.GetByISO2(ctx, req.ISO2)
	if err != nil || created == nil {
		t.Fatalf("country must exist after approval: %v", err)
	}
	if created.VerificationStatus != "unverified" || created.Version != 1 {
		t.Errorf("new country must be unverified v1, got %q v%d", created.VerificationStatus, created.Version)
	}

	got, err := e.Approval.Get(ctx, cr.ID)
	if err != nil || got.Status != "approved" || got.CheckerUserID == nil || *got.CheckerUserID != e.Checker.ActorUserID {
		t.Fatalf("change request must be approved by the checker: %+v / %v", got, err)
	}
	// Version history records the checker as the actor of the applied change.
	hist, err := e.Country.GetHistory(ctx, e.Maker, created.ID)
	if err != nil || len(hist) != 1 {
		t.Fatalf("expected exactly 1 history row, got %d / %v", len(hist), err)
	}
	if hist[0].ActorUserID == nil || *hist[0].ActorUserID != e.Checker.ActorUserID {
		t.Errorf("history actor must be the checker, got %v", hist[0].ActorUserID)
	}
	// Approving twice is refused.
	if err := e.Approval.Approve(ctx, e.Checker, cr.ID, "again"); !errors.Is(err, approval.ErrInvalidStatus) {
		t.Errorf("second approval must fail with ErrInvalidStatus, got %v", err)
	}
	// Audit: submit and approve are both audited, with maker and checker identified.
	rows := e.AuditRows("change_request", cr.ID.String())
	var sawSubmit, sawApprove bool
	for _, r := range rows {
		if r.Action == "country.change_request.submit" && r.ActorUserID != nil && *r.ActorUserID == e.Maker.ActorUserID {
			sawSubmit = true
		}
		if r.Action == "country.change_request.approve" && r.ActorUserID != nil && *r.ActorUserID == e.Checker.ActorUserID {
			sawApprove = true
		}
	}
	if !sawSubmit || !sawApprove {
		t.Errorf("audit trail incomplete: submit=%v approve=%v rows=%d", sawSubmit, sawApprove, len(rows))
	}
}

// crOf / cOf adapt a (any, error) service result into the expected concrete type.
func crOf(t *testing.T) func(any, error) *approval.ChangeRequest {
	return func(v any, err error) *approval.ChangeRequest { return mustChangeRequest(t, v, err) }
}

func cOf(t *testing.T) func(any, error) *country.Country {
	return func(v any, err error) *country.Country { return mustCountry(t, v, err) }
}

func TestCheckerNeedsTheEntitySpecificApprovePermission(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	cr := crOf(t)(e.Country.Create(ctx, e.Maker, e.CountryRequest()))

	// Holds only the OPERATOR approve permission: must not review a COUNTRY change.
	wrong := testkit.WithPermissions(e.Checker)
	delete(wrong.Permissions, "country.country.approve")
	if err := e.Approval.Approve(ctx, wrong, cr.ID, ""); !errors.Is(err, approval.ErrNotAuthorizedToApprove) {
		t.Errorf("checker without country.country.approve: expected ErrNotAuthorizedToApprove, got %v", err)
	}
	// A reseller can never review, even if (wrongly) holding the permission.
	reseller := testkit.WithPermissions(e.Reseller, "country.country.approve")
	if err := e.Approval.Approve(ctx, reseller, cr.ID, ""); !errors.Is(err, approval.ErrNotAuthorizedToApprove) {
		t.Errorf("reseller approve: expected ErrNotAuthorizedToApprove, got %v", err)
	}
	if err := e.Approval.Reject(ctx, reseller, cr.ID, "no"); !errors.Is(err, approval.ErrNotAuthorizedToApprove) {
		t.Errorf("reseller reject: expected ErrNotAuthorizedToApprove, got %v", err)
	}
}

func TestRejectRequiresReasonAndKeepsHistory(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	req := e.CountryRequest()
	cr := crOf(t)(e.Country.Create(ctx, e.Maker, req))

	if err := e.Approval.Reject(ctx, e.Checker, cr.ID, "   "); !errors.Is(err, approval.ErrEmptyRejectReason) {
		t.Fatalf("empty reason: expected ErrEmptyRejectReason, got %v", err)
	}
	if err := e.Approval.Reject(ctx, e.Checker, cr.ID, "duplicate of an existing proposal"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	got, err := e.Approval.Get(ctx, cr.ID)
	if err != nil || got.Status != "rejected" || got.RejectReason != "duplicate of an existing proposal" {
		t.Fatalf("rejected change request must keep its reason: %+v / %v", got, err)
	}
	if existing, _ := e.CountryRepo.GetByISO2(ctx, req.ISO2); existing != nil {
		t.Error("rejected change must not create the country")
	}
	if err := e.Approval.Approve(ctx, e.Checker, cr.ID, ""); !errors.Is(err, approval.ErrInvalidStatus) {
		t.Errorf("a rejected request cannot be approved later, got %v", err)
	}
}

func TestVersioningAndRollbackNeverLoseHistory(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	c := mustCountry(t, mustDirect(t, e), nil) // v1

	upd := country.UpdateCountryRequest{Name: c.Name + " v2", CallingCode: "998", Reason: "rename"}
	c2 := cOf(t)(e.Country.Update(ctx, e.Override, c.ID, upd)) // v2
	upd.Name = c.Name + " v3"
	c3 := cOf(t)(e.Country.Update(ctx, e.Override, c.ID, upd)) // v3
	if c2.Version != 2 || c3.Version != 3 {
		t.Fatalf("versions must increment: got %d, %d", c2.Version, c3.Version)
	}

	rolled := cOf(t)(e.Country.Rollback(ctx, e.Override, c.ID, 1, "undo renames")) // v4
	if rolled.Version != 4 {
		t.Fatalf("rollback must create a NEW version (4), got %d", rolled.Version)
	}
	if rolled.Name != c.Name || rolled.CallingCode != c.CallingCode {
		t.Errorf("rollback must restore v1 content, got %q / %q", rolled.Name, rolled.CallingCode)
	}
	versions := e.HistoryVersions("countries_history", "country_id", c.ID)
	if len(versions) != 4 {
		t.Fatalf("history must hold all 4 versions, got %v", versions)
	}
	for i, v := range versions {
		if v != i+1 {
			t.Fatalf("history versions must be 1..4 in order, got %v", versions)
		}
	}
	// A maker's rollback is only a pending request: nothing changes until approved.
	cr := crOf(t)(e.Country.Rollback(ctx, e.Maker, c.ID, 2, "maker rollback"))
	if got, _ := e.CountryRepo.GetByID(ctx, c.ID); got.Version != 4 {
		t.Errorf("pending rollback must not change the entity, version=%d", got.Version)
	}
	if err := e.Approval.Approve(ctx, e.Checker, cr.ID, "ok"); err != nil {
		t.Fatalf("approve rollback: %v", err)
	}
	if got, _ := e.CountryRepo.GetByID(ctx, c.ID); got.Version != 5 || got.Name != c.Name+" v2" {
		t.Errorf("approved rollback must create v5 with v2 content, got v%d %q", got.Version, got.Name)
	}
	if v := e.HistoryVersions("countries_history", "country_id", c.ID); len(v) != 5 {
		t.Errorf("history must keep every version (5), got %v", v)
	}
}

func TestEveryDirectWriteIsAuditedInTheSameTransaction(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	c := mustCountry(t, mustDirect(t, e), nil)
	if _, err := e.Country.SetStatus(ctx, e.Override, c.ID, "inactive", "audit check"); err != nil {
		t.Fatal(err)
	}
	rows := e.AuditRows("country", c.ID.String())
	actions := map[string]bool{}
	for _, r := range rows {
		actions[r.Action] = true
		if r.ActorUserID == nil || *r.ActorUserID != e.Override.ActorUserID {
			t.Errorf("audit row %s has wrong actor %v", r.Action, r.ActorUserID)
		}
		for _, banned := range []string{"password", "secret", "token", "api_key"} {
			if strings.Contains(strings.ToLower(r.Before+r.After), banned) {
				t.Errorf("audit row %s contains forbidden term %q", r.Action, banned)
			}
		}
	}
	if !actions["country.country.create"] || !actions["country.country.deactivate"] {
		t.Errorf("expected create and deactivate audit rows, got %v", actions)
	}
}

func TestInvalidCountryCodesRejected(t *testing.T) {
	e := testkit.New(t)
	req := e.CountryRequest()
	req.ISO2 = "X"
	if _, err := e.Country.Create(context.Background(), e.Override, req); !errors.Is(err, country.ErrInvalidCountryCodes) {
		t.Errorf("expected ErrInvalidCountryCodes, got %v", err)
	}
}
