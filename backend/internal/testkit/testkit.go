// Package testkit builds the REAL services against the REAL local PostgreSQL for integration
// tests. Nothing here is a stub: tests written with it exercise the production service layer,
// repositories, versioning, approval engine and audit trail.
//
// Integration tests are skipped unless ZNERO_INTEGRATION=1 (scripts/test.ps1 -Integration sets it
// and exports DATABASE_URL from .env). Credentials are never hard-coded here.
//
// Fixture users/orgs are created with unique throwaway names and neutralised in t.Cleanup
// (audit rows reference them and audit_logs is append-only, so they cannot be hard-deleted).
package testkit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/masterdata/country"
	"github.com/znero-international/znero/backend/internal/masterdata/importjob"
	"github.com/znero-international/znero/backend/internal/masterdata/operator"
	"github.com/znero-international/znero/backend/internal/masterdata/prefix"
	"github.com/znero-international/znero/backend/pkg/database"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

// Env is a fully wired set of real services plus ready-made actors.
type Env struct {
	T    testing.TB
	Pool *pgxpool.Pool

	Audit        *audit.Service
	Approval     *approval.Service
	CountryRepo  country.Repository
	Country      *country.Service
	OperatorRepo operator.Repository
	Operator     *operator.Service
	PrefixRepo   prefix.Repository
	Prefix       *prefix.Service
	ImportRepo   importjob.Repository
	Import       *importjob.Service

	PlatformOrgID   uuid.UUID
	PlatformOrgPath string

	// Maker is a platform user WITHOUT any override permission (changes become change requests).
	Maker *tenant.Context
	// Checker is a different platform user holding only the approve permissions.
	Checker *tenant.Context
	// Override is a platform user holding every override permission (direct writes).
	Override *tenant.Context
	// Reseller / Customer are read-only users of non-platform organizations.
	Reseller *tenant.Context
	Customer *tenant.Context
}

// Integration returns the connection URL or skips the test when integration mode is off.
// It enforces that the target database name ends with "_test" so znero_dev is never touched.
func Integration(t testing.TB) string {
	t.Helper()
	if os.Getenv("ZNERO_INTEGRATION") != "1" {
		t.Skip("skipping integration test without ZNERO_INTEGRATION=1")
	}
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		url = os.Getenv("DATABASE_URL")
	}
	if url == "" {
		t.Fatal("TEST_DATABASE_URL or DATABASE_URL must be set for integration tests (run scripts/test.ps1 -Integration)")
	}
	assertTestDBName(t, url)
	return url
}

func assertTestDBName(t testing.TB, url string) {
	t.Helper()
	dbName := ""
	if idx := strings.LastIndex(url, "/"); idx != -1 {
		rest := url[idx+1:]
		if q := strings.Index(rest, "?"); q != -1 {
			dbName = rest[:q]
		} else {
			dbName = rest
		}
	}
	if !strings.HasSuffix(dbName, "_test") {
		t.Fatalf("SAFETY GUARD: integration tests must target a database whose name ends in '_test'. Got database name: %q. Set TEST_DATABASE_URL or DATABASE_URL to point at your 'znero_test' database.", dbName)
	}
}

// RandHex returns n random bytes hex-encoded. Use it for every fixture secret/identifier.
func RandHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// New wires the real services and creates fixture actors.
func New(t testing.TB) *Env {
	t.Helper()
	url := Integration(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := database.Connect(ctx, database.Config{URL: url, MaxConns: 25, ConnectTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("database connect failed: %v", err)
	}
	t.Cleanup(pool.Close)

	e := &Env{T: t, Pool: pool}
	e.Audit = audit.NewService(audit.NewRepository(pool))
	e.Approval = approval.NewService(approval.NewRepository(pool), e.Audit, pool)
	e.CountryRepo = country.NewRepository(pool)
	e.Country = country.NewService(e.CountryRepo, e.Approval, e.Audit, pool)
	e.OperatorRepo = operator.NewRepository(pool)
	e.Operator = operator.NewService(e.OperatorRepo, e.Approval, e.Audit, pool)
	e.PrefixRepo = prefix.NewRepository(pool)
	e.Prefix = prefix.NewService(e.PrefixRepo, e.Approval, e.Audit, pool)
	e.ImportRepo = importjob.NewRepository(pool)
	e.Import = importjob.NewService(e.ImportRepo, e.CountryRepo, e.OperatorRepo, e.PrefixRepo, e.Prefix, e.Approval, e.Audit, pool, t.TempDir())

	if err := pool.QueryRow(ctx, `SELECT id, path FROM organizations WHERE type = 'platform'`).Scan(&e.PlatformOrgID, &e.PlatformOrgPath); err != nil {
		t.Fatalf("platform organization not found (run bootstrap-admin first): %v", err)
	}

	e.Maker = e.actor("maker", e.PlatformOrgID, e.PlatformOrgPath, "platform", false, MakerPermissions())
	e.Checker = e.actor("checker", e.PlatformOrgID, e.PlatformOrgPath, "platform", false, CheckerPermissions())
	e.Override = e.actor("override", e.PlatformOrgID, e.PlatformOrgPath, "platform", false, OverridePermissions())

	resellerOrg, resellerPath := e.org("reseller", "reseller")
	e.Reseller = e.actor("reseller", resellerOrg, resellerPath, "reseller", false, ReadOnlyPermissions())
	customerOrg, customerPath := e.org("customer", "customer")
	e.Customer = e.actor("customer", customerOrg, customerPath, "customer", false, ReadOnlyPermissions())
	return e
}

func perms(codes ...string) map[string]bool {
	m := make(map[string]bool, len(codes))
	for _, c := range codes {
		m[c] = true
	}
	return m
}

// MakerPermissions mirrors the "Master Data Editor" role template (no override).
func MakerPermissions() map[string]bool {
	return perms(
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
		"wallet.wallet.view", "wallet.topup.create", "wallet.adjustment.create", "wallet.freeze",
		"billing.invoice.view", "billing.invoice.create", "billing.payment.create", "billing.payment.view",
		"ledger.view", "ledger.reconcile",
		"routing.plan.view", "routing.plan.create", "routing.plan.edit", "routing.plan.activate", "routing.plan.deactivate", "routing.plan.archive",
		"routing.entry.view", "routing.entry.create", "routing.entry.edit",
		"routing.assignment.view", "routing.assignment.create", "routing.assignment.edit",
		"routing.rule.view", "routing.rule.create", "routing.rule.edit", "routing.rule.activate",
		"routing.simulate", "routing.killswitch", "routing.snapshot.view",
	)
}

// CheckerPermissions mirrors the "Master Data Approver" role template.
func CheckerPermissions() map[string]bool {
	return perms(
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
		"wallet.wallet.view", "wallet.approve",
		"billing.invoice.view", "billing.invoice.void", "ledger.view",
		"routing.plan.view", "routing.plan.approve",
		"routing.entry.view", "routing.entry.approve",
		"routing.assignment.view", "routing.assignment.approve",
		"routing.rule.view", "routing.rule.approve",
		"routing.simulate", "routing.killswitch", "routing.snapshot.view",
	)
}

// OverridePermissions is a platform user that may bypass approval for every master data / Phase 4 entity.
func OverridePermissions() map[string]bool {
	m := MakerPermissions()
	for _, c := range []string{
		"country.country.override", "operator.operator.override", "number.prefix.override", "import.job.override",
		"provider.provider.override", "provider.connection.override", "provider.coverage.override",
		"customer.profile.override", "customer.senderid.override", "customer.senderid.approve",
		"rate.buycard.override", "rate.buycard.approve",
		"rate.buyrate.override", "rate.buyrate.approve",
		"rate.sellcard.override", "rate.sellcard.approve",
		"rate.sellrate.override", "rate.sellrate.approve",
		"rate.assignment.override", "rate.assignment.approve",
		"rate.margin.override",
		"wallet.override", "wallet.approve",
		"billing.invoice.issue", "billing.invoice.void", "billing.invoice.creditnote",
		"routing.override", "routing.entry.override",
		"routing.plan.approve", "routing.entry.approve", "routing.assignment.approve", "routing.rule.approve",
	} {
		m[c] = true
	}
	return m
}

// ReadOnlyPermissions is what a reseller/customer user may hold for master data.
func ReadOnlyPermissions() map[string]bool {
	return perms(
		"country.country.view", "operator.operator.view", "number.prefix.view",
		"wallet.wallet.view", "billing.invoice.view", "billing.payment.view", "ledger.view",
	)
}

// WithPermissions returns a copy of tc with exactly the given extra permissions added.
func WithPermissions(tc *tenant.Context, codes ...string) *tenant.Context {
	cp := *tc
	cp.Permissions = make(map[string]bool, len(tc.Permissions)+len(codes))
	for k, v := range tc.Permissions {
		cp.Permissions[k] = v
	}
	for _, c := range codes {
		cp.Permissions[c] = true
	}
	return &cp
}

func (e *Env) org(kind, orgType string) (uuid.UUID, string) {
	return e.ChildOrg(e.PlatformOrgID, e.PlatformOrgPath, kind, orgType)
}

// ChildOrg creates a throwaway organization directly under parentID/parentPath and registers cleanup.
func (e *Env) ChildOrg(parentID uuid.UUID, parentPath, kind, orgType string) (uuid.UUID, string) {
	e.T.Helper()
	id := uuid.New()
	path := parentPath + id.String() + "/"
	ctx := context.Background()
	if _, err := e.Pool.Exec(ctx, `INSERT INTO organizations (id, name, type, parent_id, path, status) VALUES ($1, $2, $3, $4, $5, 'active')`,
		id, "ITKIT "+kind+" "+id.String()[:8], orgType, parentID, path); err != nil {
		e.T.Fatalf("create fixture org: %v", err)
	}
	e.T.Cleanup(func() {
		_, _ = e.Pool.Exec(context.Background(), `UPDATE organizations SET status = 'suspended', name = 'Purged ITKIT ' || id WHERE id = $1`, id)
	})
	return id, path
}

// Actor creates a throwaway user in orgID with the given permissions.
func (e *Env) Actor(name string, orgID uuid.UUID, orgPath, orgType string, platformAdmin bool, permCodes ...string) *tenant.Context {
	return e.actor(name, orgID, orgPath, orgType, platformAdmin, perms(permCodes...))
}

func (e *Env) actor(name string, orgID uuid.UUID, orgPath, orgType string, platformAdmin bool, p map[string]bool) *tenant.Context {
	e.T.Helper()
	id := uuid.New()
	email := "itkit-" + name + "-" + RandHex(4) + "@testkit.invalid"
	if _, err := e.Pool.Exec(context.Background(), `INSERT INTO users (id, organization_id, email, status) VALUES ($1, $2, $3, 'active')`, id, orgID, email); err != nil {
		e.T.Fatalf("create fixture user: %v", err)
	}
	e.T.Cleanup(func() {
		_, _ = e.Pool.Exec(context.Background(), `UPDATE users SET status = 'suspended', password_hash = NULL, email = 'purged-' || id || '@purged.invalid' WHERE id = $1`, id)
	})
	return &tenant.Context{
		ActorUserID:     id,
		ActorOrgID:      orgID,
		ActorOrgType:    orgType,
		ActorOrgPath:    orgPath,
		IsPlatformAdmin: platformAdmin,
		Permissions:     p,
	}
}

var userAssignedAlpha2 = []string{
	"AA", "ZZ",
	"QM", "QN", "QO", "QP", "QQ", "QR", "QS", "QT", "QU", "QV", "QW", "QX", "QY", "QZ",
	"XA", "XB", "XC", "XD", "XE", "XF", "XG", "XH", "XI", "XJ", "XK", "XL", "XM",
	"XN", "XO", "XP", "XQ", "XR", "XS", "XT", "XU", "XV", "XW", "XX", "XY", "XZ",
}

// UniqueCountryCodes returns ISO codes from the user-assigned ranges (AA, ZZ, QM-QZ, XA-XZ),
// which can never collide with a real ISO 3166 country, and registers cleanup of whatever the
// test created with that ISO2.
func (e *Env) UniqueCountryCodes() (iso2, iso3, numeric string) {
	e.T.Helper()
	ctx := context.Background()
	for attempt := 0; attempt < 200; attempt++ {
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		idx := int(b[0]) % len(userAssignedAlpha2)
		iso2 = userAssignedAlpha2[idx]
		third := string(rune('A' + int(b[1])%26))
		iso3 = iso2 + third
		numeric = "9" + string(rune('0'+int(b[2])%10)) + string(rune('0'+int(b[3])%10))

		lockKey := int64(88800000) + int64(iso2[0])<<8 + int64(iso2[1])
		var locked bool
		if err := e.Pool.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockKey).Scan(&locked); err != nil || !locked {
			continue
		}

		var n int
		_ = e.Pool.QueryRow(ctx, `SELECT count(*) FROM countries WHERE iso2 = $1 OR iso3 = $2 OR numeric_code = $3`, iso2, iso3, numeric).Scan(&n)
		if n == 0 {
			e.T.Cleanup(func() {
				e.PurgeCountry(iso2)
				_, _ = e.Pool.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, lockKey)
			})
			return
		}
		_, _ = e.Pool.Exec(ctx, `SELECT pg_advisory_unlock($1)`, lockKey)
	}
	e.T.Fatal("could not find free user-assigned ISO codes")
	return
}

// PurgeCountry removes test-created master data rows (test fixtures only; the version history is
// deleted by ON DELETE CASCADE, audit rows stay because audit_logs is append-only).
func (e *Env) PurgeCountry(iso2 string) {
	ctx := context.Background()
	stmts := []string{
		`DELETE FROM rates_history WHERE country_id IN (SELECT id FROM countries WHERE iso2 = $1)`,
		`DELETE FROM rates WHERE country_id IN (SELECT id FROM countries WHERE iso2 = $1)`,
		`DELETE FROM providers WHERE country_id IN (SELECT id FROM countries WHERE iso2 = $1)`,
		`DELETE FROM provider_coverage WHERE country_id IN (SELECT id FROM countries WHERE iso2 = $1)`,
		`DELETE FROM sender_ids WHERE country_id IN (SELECT id FROM countries WHERE iso2 = $1)`,
		`DELETE FROM number_prefixes WHERE country_id IN (SELECT id FROM countries WHERE iso2 = $1)`,
		`DELETE FROM operators WHERE country_id IN (SELECT id FROM countries WHERE iso2 = $1)`,
		`DELETE FROM countries WHERE iso2 = $1`,
	}
	for _, s := range stmts {
		_, _ = e.Pool.Exec(ctx, s, strings.ToUpper(iso2))
	}
	_, _ = e.Pool.Exec(ctx, `DELETE FROM change_requests WHERE proposed_data->>'iso2' = $1`, strings.ToUpper(iso2))
}

// CountryRequest builds a valid create request for a fresh, collision-free test country.
func (e *Env) CountryRequest() country.CreateCountryRequest {
	iso2, iso3, num := e.UniqueCountryCodes()
	return country.CreateCountryRequest{
		Name:        "ITKIT Test Country " + iso2,
		ISO2:        iso2,
		ISO3:        iso3,
		NumericCode: num,
		CallingCode: "999",
		Continent:   "Testland",
		DataSource:  "ITKIT-TEST-FIXTURE",
	}
}

// AuditRows returns the audit rows (action, before, after as text) for an entity.
func (e *Env) AuditRows(entityType, entityID string) []AuditRow {
	e.T.Helper()
	rows, err := e.Pool.Query(context.Background(),
		`SELECT action, actor_user_id, coalesce(before_value::text,''), coalesce(after_value::text,''), reason FROM audit_logs WHERE entity_type = $1 AND entity_id = $2 ORDER BY timestamp`, entityType, entityID)
	if err != nil {
		e.T.Fatalf("query audit: %v", err)
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var r AuditRow
		if err := rows.Scan(&r.Action, &r.ActorUserID, &r.Before, &r.After, &r.Reason); err != nil {
			e.T.Fatalf("scan audit: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// AuditRow is one audit_logs row.
type AuditRow struct {
	Action      string
	ActorUserID *uuid.UUID
	Before      string
	After       string
	Reason      string
}

// HistoryVersions returns the version numbers recorded in <table> for the entity.
func (e *Env) HistoryVersions(table, fkColumn string, id uuid.UUID) []int {
	e.T.Helper()
	rows, err := e.Pool.Query(context.Background(), `SELECT version FROM `+table+` WHERE `+fkColumn+` = $1 ORDER BY version`, id)
	if err != nil {
		e.T.Fatalf("query history: %v", err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		_ = rows.Scan(&v)
		out = append(out, v)
	}
	return out
}
