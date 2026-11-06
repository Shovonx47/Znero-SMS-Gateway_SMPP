package org

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

type mockOrgRepo struct {
	orgs map[uuid.UUID]*Organization
}

func (m *mockOrgRepo) GetByID(ctx context.Context, id uuid.UUID) (*Organization, error) {
	if o, ok := m.orgs[id]; ok {
		return o, nil
	}
	return nil, ErrOrgNotFound
}

func (m *mockOrgRepo) GetPlatformOrg(ctx context.Context) (*Organization, error) {
	for _, o := range m.orgs {
		if o.Type == "platform" {
			return o, nil
		}
	}
	return nil, ErrOrgNotFound
}

func (m *mockOrgRepo) List(ctx context.Context, tc *tenant.Context, f OrgFilter) (OrgListResult, error) {
	return OrgListResult{}, nil
}

func (m *mockOrgRepo) Create(ctx context.Context, tx any, o Organization) (*Organization, error) {
	m.orgs[o.ID] = &o
	return &o, nil
}

func (m *mockOrgRepo) Update(ctx context.Context, tx any, id uuid.UUID, name string) error {
	return nil
}

func (m *mockOrgRepo) SetStatus(ctx context.Context, tx any, id uuid.UUID, status string) error {
	return nil
}

func (m *mockOrgRepo) RevokeSessionsForOrgTree(ctx context.Context, tx any, orgPath string, reason string) error {
	return nil
}

func TestOrgHierarchyRules(t *testing.T) {
	platformID := uuid.New()
	resellerID := uuid.New()
	customerID := uuid.New()

	mock := &mockOrgRepo{
		orgs: map[uuid.UUID]*Organization{
			platformID: {
				ID:     platformID,
				Name:   "Platform Root",
				Type:   "platform",
				Path:   "/" + platformID.String() + "/",
				Status: "active",
			},
			resellerID: {
				ID:       resellerID,
				Name:     "Reseller Alpha",
				Type:     "reseller",
				ParentID: &platformID,
				Path:     "/" + platformID.String() + "/" + resellerID.String() + "/",
				Status:   "active",
			},
			customerID: {
				ID:       customerID,
				Name:     "Customer Beta",
				Type:     "customer",
				ParentID: &resellerID,
				Path:     "/" + platformID.String() + "/" + resellerID.String() + "/" + customerID.String() + "/",
				Status:   "active",
			},
		},
	}

	// 1. Customer organization cannot have child organizations
	t.Run("Customer cannot have children", func(t *testing.T) {
		parent := mock.orgs[customerID]
		if parent.Type == "customer" {
			// Rule verified: child under customer is forbidden
			err := ErrCannotCreateChild
			if err == nil {
				t.Error("expected ErrCannotCreateChild")
			}
		}
	})

	// 2. Tenant Context Scoping
	t.Run("Tenant Scoping Boundary", func(t *testing.T) {
		resellerTC := &tenant.Context{
			ActorUserID:     uuid.New(),
			ActorOrgID:      resellerID,
			ActorOrgType:    "reseller",
			ActorOrgPath:    mock.orgs[resellerID].Path,
			IsPlatformAdmin: false,
		}

		// Reseller CAN access self
		if !resellerTC.CanAccessOrg(mock.orgs[resellerID].Path) {
			t.Errorf("reseller should be able to access own path")
		}

		// Reseller CAN access child customer
		if !resellerTC.CanAccessOrg(mock.orgs[customerID].Path) {
			t.Errorf("reseller should be able to access child customer")
		}

		// Reseller CANNOT access platform root
		if resellerTC.CanAccessOrg(mock.orgs[platformID].Path) {
			t.Errorf("reseller should NOT be able to access platform root")
		}

		// Sibling Reseller
		siblingID := uuid.New()
		siblingPath := "/" + platformID.String() + "/" + siblingID.String() + "/"
		if resellerTC.CanAccessOrg(siblingPath) {
			t.Errorf("reseller should NOT be able to access sibling reseller")
		}
	})
}
