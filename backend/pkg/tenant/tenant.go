package tenant

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

var (
	ErrUnauthorizedTenant = errors.New("access denied: resource is outside organization boundary")
	ErrNoTenantContext    = errors.New("no tenant context present in request")
)

type ctxKey struct{}

// Context contains verified identity and scoping metadata derived exclusively
// from the server-side authenticated session.
type Context struct {
	ActorUserID     uuid.UUID       `json:"actor_user_id"`
	ActorOrgID      uuid.UUID       `json:"actor_org_id"`
	ActorOrgType    string          `json:"actor_org_type"` // "platform", "reseller", "customer"
	ActorOrgPath    string          `json:"actor_org_path"` // e.g. "/root_id/child_id/"
	IsPlatformAdmin bool            `json:"is_platform_admin"`
	Permissions     map[string]bool `json:"permissions"`
}

// WithContext stores the tenant context in context.Context.
func WithContext(ctx context.Context, tc *Context) context.Context {
	return context.WithValue(ctx, ctxKey{}, tc)
}

// FromContext extracts the tenant context.
func FromContext(ctx context.Context) (*Context, error) {
	if ctx == nil {
		return nil, ErrNoTenantContext
	}
	tc, ok := ctx.Value(ctxKey{}).(*Context)
	if !ok || tc == nil {
		return nil, ErrNoTenantContext
	}
	return tc, nil
}

// CanAccessOrg returns true if the actor has rights to view or manage targetOrgPath.
// Platform administrators can access everything; other organizations can only
// access themselves and their descendants (materialized path prefix match).
func (tc *Context) CanAccessOrg(targetOrgPath string) bool {
	if tc.IsPlatformAdmin {
		return true
	}
	if tc.ActorOrgPath == "" || targetOrgPath == "" {
		return false
	}
	// Target path must equal or begin with the actor's path (descendants)
	return strings.HasPrefix(targetOrgPath, tc.ActorOrgPath)
}

// IsPlatformOrg reports whether the actor belongs to the (single) platform organization.
// It is deliberately distinct from IsPlatformAdmin: IsPlatformAdmin is the Super Admin
// bypass, whereas IsPlatformOrg only says "this user works for the platform". Platform-owned
// data (master data, providers) is gated on IsPlatformOrg plus the specific permission, so a
// platform user WITHOUT an override permission is a maker whose changes need a checker.
func (tc *Context) IsPlatformOrg() bool {
	return tc != nil && (tc.IsPlatformAdmin || tc.ActorOrgType == "platform")
}

// IsResellerOrg reports whether the actor belongs to a reseller organization.
func (tc *Context) IsResellerOrg() bool {
	return tc != nil && tc.ActorOrgType == "reseller"
}

// IsCustomerOrg reports whether the actor belongs to a customer organization.
func (tc *Context) IsCustomerOrg() bool {
	return tc != nil && tc.ActorOrgType == "customer"
}

// HasPermission checks whether the actor holds the specific code-defined permission.
func (tc *Context) HasPermission(code string) bool {
	if tc.IsPlatformAdmin {
		return true
	}
	return tc.Permissions[code]
}

// OrgPathFilter returns a safe SQL condition and parameter for queries filtering on organization path.
func (tc *Context) OrgPathFilter(columnName string, paramIdx int) (sql string, param any) {
	if tc.IsPlatformAdmin {
		return "1=1", nil
	}
	return fmt.Sprintf("%s LIKE $%d || '%%'", columnName, paramIdx), tc.ActorOrgPath
}

// OrgIDFilter returns a safe SQL condition when querying resources belonging directly to an organization.
func (tc *Context) DirectOrgIDFilter(columnName string, paramIdx int) (sql string, param any) {
	if tc.IsPlatformAdmin {
		return "1=1", nil
	}
	return fmt.Sprintf("%s = $%d", columnName, paramIdx), tc.ActorOrgID
}
