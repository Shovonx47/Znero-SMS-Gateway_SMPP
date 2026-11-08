package rbac

import "strings"

// platformOnlyPrefixes are permission namespaces that exist only for the platform organization.
// Reseller and customer organizations must never receive them, not even through the "Org Admin"
// system template, and a role in a non-platform organization can never be granted them.
var platformOnlyPrefixes = []string{
	"provider.",      // upstream suppliers, connections and coverage (Phase 4)
	"import.",        // master data / coverage import jobs
	"number.rule.",   // number transformation rules
	"number.prefix.", // only "view" is allowed for non-platform orgs (see below)
	"country.country.",
	"operator.operator.",
	"rate.buycard.",  // provider buy rate cards (Phase 5)
	"rate.buyrate.",  // provider buy rates (Phase 5)
	"rate.margin.",   // margin analysis & override (Phase 5)
	"routing.",       // routing plans, entries, rules, simulator, kill switch (Phase 7)
}

// readOnlyForNonPlatform lists the namespaces where non-platform organizations may hold the
// "view" action (read-only access to active master data) but nothing else.
var readOnlyForNonPlatform = map[string]bool{
	"country.country.":   true,
	"operator.operator.": true,
	"number.prefix.":     true,
	"number.rule.":       true,
}

// platformOnlyExact lists exact permission codes that only the platform organization may hold.
var platformOnlyExact = map[string]bool{
	"customer.profile.approve":   true,
	"customer.profile.override":  true,
	"customer.senderid.approve":  true,
	"customer.senderid.override": true,
	"rate.sellcard.approve":      true,
	"rate.sellcard.override":     true,
	"rate.sellrate.approve":      true,
	"rate.sellrate.override":     true,
	"rate.assignment.create":     true,
	"rate.assignment.update":     true,
	"rate.assignment.approve":    true,
	"rate.assignment.override":   true,
	"wallet.override":            true,
	"wallet.approve":             true,
	"billing.invoice.void":       true,
	"billing.invoice.creditnote": true,
	"ledger.reconcile":           true,
}

// customerForbiddenCodes lists permissions that a customer organization (orgType == "customer")
// must not receive in its system roles (customers have read-only access to their own commercial
// profile and SMPP accounts, and can create/view sender IDs and manage their own API keys).
var customerForbiddenCodes = map[string]bool{
	"customer.profile.create":     true,
	"customer.profile.edit":       true,
	"customer.profile.approve":    true,
	"customer.profile.override":   true,
	"customer.smppaccount.create": true,
	"customer.smppaccount.edit":   true,
	"customer.smppaccount.revoke": true,
	"customer.senderid.edit":      true,
	"customer.senderid.approve":   true,
	"customer.senderid.override":  true,
	"customer.senderid.import":    true,
	"rate.sellcard.create":        true,
	"rate.sellcard.update":        true,
	"rate.sellcard.archive":       true,
	"rate.sellrate.create":        true,
	"rate.sellrate.update":        true,
	"rate.assignment.create":      true,
	"rate.assignment.update":      true,
	"rate.assignment.read":        true,
	"rate.import.upload":          true,
	"rate.import.confirm":         true,
	"wallet.topup.create":         true,
	"wallet.adjustment.create":    true,
	"wallet.freeze":               true,
	"billing.invoice.create":      true,
	"billing.invoice.issue":       true,
	"billing.payment.create":      true,
}

// IsPlatformOnlyPermission reports whether code may be held only by platform-organization roles.
func IsPlatformOnlyPermission(code string) bool {
	if platformOnlyExact[code] {
		return true
	}
	for _, prefix := range platformOnlyPrefixes {
		if !strings.HasPrefix(code, prefix) {
			continue
		}
		if readOnlyForNonPlatform[prefix] && strings.HasSuffix(code, ".view") {
			return false
		}
		return true
	}
	return false
}

// IsForbiddenForCustomerOrg reports whether code is forbidden for customer-type organizations.
func IsForbiddenForCustomerOrg(code string) bool {
	if IsPlatformOnlyPermission(code) {
		return true
	}
	return customerForbiddenCodes[code]
}

// platformMasterDataEditorCodes is the maker template: edit master data, providers, and customer
// profiles/credentials/sender IDs, but NOT override or approve, so sensitive changes become pending
// change requests.
var platformMasterDataEditorCodes = []string{
	"country.country.view", "country.country.create", "country.country.edit", "country.country.deactivate", "country.country.archive", "country.country.import",
	"operator.operator.view", "operator.operator.create", "operator.operator.edit", "operator.operator.deactivate", "operator.operator.archive", "operator.operator.import",
	"number.prefix.view", "number.prefix.create", "number.prefix.edit", "number.prefix.deactivate", "number.prefix.archive", "number.prefix.import",
	"number.rule.view", "number.rule.create", "number.rule.edit", "number.rule.delete",
	"number.lookup.use", "import.job.view", "import.job.manage",
	"provider.provider.view", "provider.provider.create", "provider.provider.edit", "provider.provider.status", "provider.provider.archive",
	"provider.connection.view", "provider.connection.create", "provider.connection.edit", "provider.connection.secret.update", "provider.connection.status", "provider.connection.archive",
	"provider.coverage.view", "provider.coverage.create", "provider.coverage.edit", "provider.coverage.status", "provider.coverage.archive", "provider.coverage.import",
	"customer.profile.view", "customer.profile.create", "customer.profile.edit",
	"customer.apikey.view", "customer.apikey.create", "customer.apikey.revoke", "customer.apikey.rotate",
	"customer.smppaccount.view", "customer.smppaccount.create", "customer.smppaccount.edit", "customer.smppaccount.revoke",
	"customer.senderid.view", "customer.senderid.create", "customer.senderid.edit", "customer.senderid.import",
	"rate.buycard.create", "rate.buycard.read", "rate.buycard.update", "rate.buycard.archive",
	"rate.buyrate.create", "rate.buyrate.read", "rate.buyrate.update",
	"rate.sellcard.create", "rate.sellcard.read", "rate.sellcard.update", "rate.sellcard.archive",
	"rate.sellrate.create", "rate.sellrate.read", "rate.sellrate.update",
	"rate.assignment.create", "rate.assignment.read", "rate.assignment.update",
	"rate.margin.view", "rate.import.upload", "rate.import.confirm",
	"wallet.wallet.view", "wallet.topup.create", "wallet.adjustment.create",
	"billing.invoice.create", "billing.invoice.view", "billing.payment.create", "ledger.view",
}

// platformMasterDataApproverCodes is the checker template: review change requests, read-only otherwise.
var platformMasterDataApproverCodes = []string{
	"country.country.view", "country.country.approve",
	"operator.operator.view", "operator.operator.approve",
	"number.prefix.view", "number.prefix.approve",
	"number.rule.view", "number.rule.approve",
	"import.job.view", "import.job.approve",
	"provider.provider.view", "provider.provider.approve",
	"provider.connection.view", "provider.connection.approve",
	"provider.coverage.view", "provider.coverage.approve",
	"customer.profile.view", "customer.profile.approve",
	"customer.apikey.view", "customer.smppaccount.view",
	"customer.senderid.view", "customer.senderid.approve",
	"rate.buycard.read", "rate.buycard.approve",
	"rate.buyrate.read", "rate.buyrate.approve",
	"rate.sellcard.read", "rate.sellcard.approve",
	"rate.sellrate.read", "rate.sellrate.approve",
	"rate.assignment.read", "rate.assignment.approve",
	"rate.margin.view",
	"wallet.wallet.view", "wallet.approve", "billing.invoice.view", "billing.payment.view", "ledger.view",
}

// platformRoutingMakerCodes is the dedicated Routing Maker template: drafts and edits route plans,
// entries, customer assignments, and routing rules. Strictly holds neither *.approve nor
// routing.override / routing.entry.override.
var platformRoutingMakerCodes = []string{
	"routing.plan.view", "routing.plan.create", "routing.plan.edit", "routing.plan.activate", "routing.plan.deactivate", "routing.plan.archive",
	"routing.entry.view", "routing.entry.create", "routing.entry.edit",
	"routing.assignment.view", "routing.assignment.create", "routing.assignment.edit",
	"routing.rule.view", "routing.rule.create", "routing.rule.edit", "routing.rule.activate",
	"routing.simulate", "routing.snapshot.view",
	"provider.provider.view", "provider.connection.view", "customer.profile.view",
	"country.country.view", "operator.operator.view",
}

// platformRoutingCheckerCodes is the dedicated Routing Checker template: approves or rejects routing
// change requests and manages kill-switch overrides. Strictly holds neither routing.override nor
// routing.entry.override.
var platformRoutingCheckerCodes = []string{
	"routing.plan.view", "routing.plan.approve",
	"routing.entry.view", "routing.entry.approve",
	"routing.assignment.view", "routing.assignment.approve",
	"routing.rule.view", "routing.rule.approve", "routing.rule.activate",
	"routing.simulate", "routing.snapshot.view", "routing.killswitch",
	"provider.provider.view", "provider.connection.view", "customer.profile.view",
	"country.country.view", "operator.operator.view",
}
