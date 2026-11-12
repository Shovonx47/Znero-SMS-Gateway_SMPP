package customeredge

import (
	"strings"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

// Standard public API key scopes
const (
	ScopeSMSSend        = "sms:send"
	ScopeSMSRead        = "sms:read"
	ScopeSMSReadContent = "sms:read_content"
	ScopeAccountRead    = "account:read"
	ScopeInboundRead    = "inbound:read"
)

// Principal represents an authenticated public edge caller (API Key or SMPP account).
// Public edge callers are NOT JWT user identities; they represent a narrow credential identity.
type Principal struct {
	CustomerOrgID   uuid.UUID
	CustomerOrgPath string
	AccountID       *uuid.UUID // Set if SMPP account
	APIKeyID        *uuid.UUID // Set if HTTP API key
	Label           string
	KeyPrefix       string
	Scopes          []string
	AllowedIPs      []string
	MaxTPS          int
	IsActive        bool
}

// HasScope checks whether the principal possesses the requested permission scope.
func (p *Principal) HasScope(requiredScope string) bool {
	if p == nil {
		return false
	}
	for _, s := range p.Scopes {
		if strings.EqualFold(s, requiredScope) || s == "*" {
			return true
		}
	}
	return false
}

// ToTenantContext constructs a synthetic tenant context representing the authenticated customer organization.
func (p *Principal) ToTenantContext() *tenant.Context {
	if p == nil {
		return nil
	}
	perms := make(map[string]bool)
	for _, s := range p.Scopes {
		switch s {
		case ScopeSMSSend, "*":
			perms["message.send"] = true
			if s == "*" {
				perms["message.view"] = true
				perms["wallet.wallet.view"] = true
				perms["inbound.view"] = true
			}
		case ScopeSMSRead:
			perms["message.view"] = true
		case ScopeSMSReadContent:
			perms["message.content.view"] = true
		case ScopeAccountRead:
			perms["balance.view"] = true
			perms["wallet.wallet.view"] = true
		case ScopeInboundRead:
			perms["inbound.view"] = true
		}
	}
	return &tenant.Context{
		ActorUserID:  uuid.Nil,
		ActorOrgID:   p.CustomerOrgID,
		ActorOrgType: "customer",
		ActorOrgPath: p.CustomerOrgPath,
		Permissions:  perms,
	}
}

// ValidateIP checks whether the client IP is allowed by the credential's allowlist (if configured).
func (p *Principal) ValidateIP(clientIP string) bool {
	if p == nil {
		return false
	}
	if len(p.AllowedIPs) == 0 {
		return true // No restriction configured
	}
	clientIP = strings.TrimSpace(clientIP)
	for _, allowed := range p.AllowedIPs {
		if strings.TrimSpace(allowed) == clientIP {
			return true
		}
	}
	return false
}
