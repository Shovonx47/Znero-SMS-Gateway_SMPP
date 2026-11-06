package prefix_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/masterdata/prefix"
	"github.com/znero-international/znero/backend/internal/testkit"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

// Real prefix service + approval engine + PostgreSQL (skipped unless ZNERO_INTEGRATION=1).

func newCountry(t *testing.T, e *testkit.Env) *country.Country {
	t.Helper()
	v, err := e.Country.Create(context.Background(), e.Override, e.CountryRequest())
	if err != nil {
		t.Fatalf("create fixture country: %v", err)
	}
	return v.(*country.Country)
}

func prefixRequest(c *country.Country, p string) prefix.CreatePrefixRequest {
	return prefix.CreatePrefixRequest{Prefix: p, CountryID: c.ID, NumberType: "mobile", Priority: 100, DataSource: "ITKIT-TEST-FIXTURE"}
}

func newPrefix(t *testing.T, e *testkit.Env, c *country.Country, p string) *prefix.NumberPrefix {
	t.Helper()
	v, err := e.Prefix.Create(context.Background(), e.Override, prefixRequest(c, p))
	if err != nil {
		t.Fatalf("create fixture prefix: %v", err)
	}
	return v.(*prefix.NumberPrefix)
}

func TestResellerAndCustomerAreReadOnly(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	c := newCountry(t, e)
	np := newPrefix(t, e, c, "9991")

	for _, tt := range []struct {
		name string
		tc   *tenant.Context
	}{{"reseller", e.Reseller}, {"customer", e.Customer}} {
		if _, err := e.Prefix.Create(ctx, tt.tc, prefixRequest(c, "9992")); !errors.Is(err, prefix.ErrPlatformOnlyMasterData) {
			t.Errorf("%s create: expected ErrPlatformOnlyMasterData, got %v", tt.name, err)
		}
		if _, err := e.Prefix.SetStatus(ctx, tt.tc, np.ID, "inactive", "x"); !errors.Is(err, prefix.ErrPlatformOnlyMasterData) {
			t.Errorf("%s status: expected ErrPlatformOnlyMasterData, got %v", tt.name, err)
		}
		if _, err := e.Prefix.Rollback(ctx, tt.tc, np.ID, 1, "x"); !errors.Is(err, prefix.ErrPlatformOnlyMasterData) {
			t.Errorf("%s rollback: expected ErrPlatformOnlyMasterData, got %v", tt.name, err)
		}
		if _, err := e.Prefix.GetHistory(ctx, tt.tc, np.ID); !errors.Is(err, prefix.ErrPlatformOnlyMasterData) {
			t.Errorf("%s history: expected ErrPlatformOnlyMasterData, got %v", tt.name, err)
		}
		if _, err := e.Prefix.Get(ctx, tt.tc, np.ID); err != nil {
			t.Errorf("%s must read an active prefix: %v", tt.name, err)
		}
	}
}

func TestMakerCannotApproveOwnPrefixChange(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	c := newCountry(t, e)

	v, err := e.Prefix.Create(ctx, e.Maker, prefixRequest(c, "9993"))
	if err != nil {
		t.Fatal(err)
	}
	cr, ok := v.(*approval.ChangeRequest)
	if !ok || cr.EntityType != "prefix" || cr.Status != "pending" {
		t.Fatalf("maker must get a pending prefix change request, got %T", v)
	}
	selfApprover := testkit.WithPermissions(e.Maker, "number.prefix.approve")
	if err := e.Approval.Approve(ctx, selfApprover, cr.ID, ""); !errors.Is(err, approval.ErrMakerCannotApproveOwnChange) {
		t.Fatalf("expected ErrMakerCannotApproveOwnChange, got %v", err)
	}
	if err := e.Approval.Reject(ctx, e.Checker, cr.ID, ""); !errors.Is(err, approval.ErrEmptyRejectReason) {
		t.Fatalf("expected ErrEmptyRejectReason, got %v", err)
	}
	if err := e.Approval.Approve(ctx, e.Checker, cr.ID, "ok"); err != nil {
		t.Fatalf("checker approve: %v", err)
	}
	res, err := e.Prefix.List(ctx, e.Checker, prefix.Filter{Search: "9993", Page: 1, PageSize: 10})
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("approved prefix must appear in the list: %v / %d items", err, len(res.Items))
	}
	if h := e.HistoryVersions("number_prefixes_history", "prefix_id", res.Items[0].ID); len(h) != 1 {
		t.Errorf("expected 1 history version, got %v", h)
	}
}

func TestPrefixVersioningRollbackAndAudit(t *testing.T) {
	e := testkit.New(t)
	ctx := context.Background()
	c := newCountry(t, e)
	np := newPrefix(t, e, c, "9994") // v1

	if _, err := e.Prefix.SetStatus(ctx, e.Override, np.ID, "inactive", "pause"); err != nil { // v2
		t.Fatal(err)
	}
	rb, err := e.Prefix.Rollback(ctx, e.Override, np.ID, 1, "undo pause") // v3
	if err != nil {
		t.Fatal(err)
	}
	got := rb.(*prefix.NumberPrefix)
	if got.Version != 3 || got.Status != "active" {
		t.Fatalf("rollback must create v3 restoring active status, got v%d %q", got.Version, got.Status)
	}
	if h := e.HistoryVersions("number_prefixes_history", "prefix_id", np.ID); len(h) != 3 {
		t.Fatalf("history must keep every version, got %v", h)
	}
	for _, r := range e.AuditRows("prefix", np.ID.String()) {
		for _, banned := range []string{"password", "secret", "token", "api_key"} {
			if strings.Contains(strings.ToLower(r.Before+r.After), banned) {
				t.Errorf("audit row %s contains %q", r.Action, banned)
			}
		}
	}
}
