package tenant_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

func TestTenantContextScoping(t *testing.T) {
	platformID := uuid.New()
	resellerID := uuid.New()
	customerID := uuid.New()

	platformPath := "/" + platformID.String() + "/"
	resellerPath := platformPath + resellerID.String() + "/"
	customerPath := resellerPath + customerID.String() + "/"

	otherResellerID := uuid.New()
	otherResellerPath := platformPath + otherResellerID.String() + "/"

	// 1. Platform admin can see everything
	platformCtx := &tenant.Context{
		ActorUserID:     uuid.New(),
		ActorOrgID:      platformID,
		ActorOrgType:    "platform",
		ActorOrgPath:    platformPath,
		IsPlatformAdmin: true,
	}
	if !platformCtx.CanAccessOrg(platformPath) || !platformCtx.CanAccessOrg(resellerPath) || !platformCtx.CanAccessOrg(otherResellerPath) {
		t.Fatal("Platform admin should access all paths")
	}

	// 2. Reseller can see itself and customer, but NOT other reseller or platform root
	resellerCtx := &tenant.Context{
		ActorUserID:     uuid.New(),
		ActorOrgID:      resellerID,
		ActorOrgType:    "reseller",
		ActorOrgPath:    resellerPath,
		IsPlatformAdmin: false,
	}
	if !resellerCtx.CanAccessOrg(resellerPath) {
		t.Fatal("Reseller should access self")
	}
	if !resellerCtx.CanAccessOrg(customerPath) {
		t.Fatal("Reseller should access child customer")
	}
	if resellerCtx.CanAccessOrg(platformPath) {
		t.Fatal("Reseller must NOT access parent platform")
	}
	if resellerCtx.CanAccessOrg(otherResellerPath) {
		t.Fatal("Reseller must NOT access sibling reseller")
	}

	// 3. Context propagation
	ctx := tenant.WithContext(context.Background(), resellerCtx)
	extracted, err := tenant.FromContext(ctx)
	if err != nil || extracted.ActorOrgID != resellerID {
		t.Fatalf("Context roundtrip failed: %v", err)
	}
}

func TestTenantContext_PermissionChecksAndCustomerIsolation(t *testing.T) {
	custID := uuid.New()
	custCtx := &tenant.Context{
		ActorUserID:  uuid.New(),
		ActorOrgID:   custID,
		ActorOrgType: "customer",
		Permissions: map[string]bool{
			"customer.apikey.view": true,
			"wallet.wallet.view":   true,
		},
	}

	// Permission checks
	if !custCtx.HasPermission("customer.apikey.view") {
		t.Error("expected HasPermission to return true for customer.apikey.view")
	}
	if custCtx.HasPermission("provider.provider.view") {
		t.Error("expected HasPermission to return false for unheld permission")
	}
	if custCtx.IsPlatformOrg() {
		t.Error("customer must not be platform org")
	}

	// Missing context error
	_, err := tenant.FromContext(context.Background())
	if err == nil {
		t.Error("expected error when context lacks tenant")
	}
}
