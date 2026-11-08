// Command seeddev provisions or removes labeled fake development test accounts.
// Strictly forbidden from running outside APP_ENV=development.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/znero-international/znero/backend/internal/config"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/internal/rbac"
	"github.com/znero-international/znero/backend/pkg/crypto/password"
	"github.com/znero-international/znero/backend/pkg/database"
)

const devSeedDomain = "@dev.znero.test"

type DevSeedUser struct {
	Email       string
	Password    string
	OrgName     string
	OrgType     string
	ParentName  string
	RoleName    string
	Description string
}

var devAccounts = []DevSeedUser{
	{
		Email:       "dev-superadmin" + devSeedDomain,
		Password:    "DevSeed1234!#",
		OrgName:     "Znero International Platform",
		OrgType:     "platform",
		RoleName:    "Super Admin",
		Description: "[DEV FAKE] Platform Super Admin account for automated & manual local testing",
	},
	{
		Email:       "dev-maker" + devSeedDomain,
		Password:    "DevSeed1234!#",
		OrgName:     "Znero International Platform",
		OrgType:     "platform",
		RoleName:    "Master Data Editor",
		Description: "[DEV FAKE] Platform Maker account (no override permission) for dual-control testing",
	},
	{
		Email:       "dev-checker" + devSeedDomain,
		Password:    "DevSeed1234!#",
		OrgName:     "Znero International Platform",
		OrgType:     "platform",
		RoleName:    "Master Data Approver",
		Description: "[DEV FAKE] Platform Checker account for dual-control approval testing",
	},
	{
		Email:       "dev-routing-maker" + devSeedDomain,
		Password:    "DevSeed1234!#",
		OrgName:     "Znero International Platform",
		OrgType:     "platform",
		RoleName:    "Routing Maker",
		Description: "[DEV FAKE] Platform Routing Maker account (no override or approve) for routing dual-control testing",
	},
	{
		Email:       "dev-routing-checker" + devSeedDomain,
		Password:    "DevSeed1234!#",
		OrgName:     "Znero International Platform",
		OrgType:     "platform",
		RoleName:    "Routing Checker",
		Description: "[DEV FAKE] Platform Routing Checker account for routing approval and kill-switch testing",
	},
	{
		Email:       "dev-reseller-admin" + devSeedDomain,
		Password:    "DevSeed1234!#",
		OrgName:     "Apex Wholesale Telecom (Dev)",
		OrgType:     "reseller",
		ParentName:  "Znero International Platform",
		RoleName:    "Org Admin",
		Description: "[DEV FAKE] Reseller Administrator account for subtree testing",
	},
	{
		Email:       "dev-customer-admin" + devSeedDomain,
		Password:    "DevSeed1234!#",
		OrgName:     "BlueStar SMS Global (Dev)",
		OrgType:     "customer",
		ParentName:  "Apex Wholesale Telecom (Dev)",
		RoleName:    "Org Admin",
		Description: "[DEV FAKE] Customer Administrator account for leaf tenant testing",
	},
	{
		Email:       "dev-readonly" + devSeedDomain,
		Password:    "DevSeed1234!#",
		OrgName:     "BlueStar SMS Global (Dev)",
		OrgType:     "customer",
		ParentName:  "Apex Wholesale Telecom (Dev)",
		RoleName:    "Read-only",
		Description: "[DEV FAKE] Customer Viewer account for RBAC read-only testing",
	},
}

func main() {
	cleanup := flag.Bool("cleanup", false, "Remove all dev seed accounts and test tenants")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	if cfg.Env != config.EnvDevelopment {
		fmt.Fprintf(os.Stderr, "FATAL: Dev seed tool is strictly prohibited outside APP_ENV=development. Current environment: %q\n", cfg.Env)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := database.Connect(ctx, database.Config{
		URL:            cfg.Database.URL,
		MaxConns:       5,
		ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Database connection failed: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	if *cleanup {
		if err := runCleanup(ctx, pool); err != nil {
			fmt.Fprintf(os.Stderr, "Cleanup failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("[SUCCESS] Dev seed accounts purged successfully.")
		return
	}

	if err := runSeed(ctx, pool); err != nil {
		fmt.Fprintf(os.Stderr, "Seed failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("[SUCCESS] Dev seed accounts created successfully.")
}

func runCleanup(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	targetDomains := []string{
		devSeedDomain,
		"@znero.internal",
		"@apextelecom.com",
		"@bluestar-sms.com",
		"@fintechcorp.com",
	}

	for _, dom := range targetDomains {
		// Revoke sessions and auth tokens
		_, _ = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id IN (SELECT id FROM users WHERE email LIKE '%' || $1)`, dom)
		_, _ = tx.Exec(ctx, `DELETE FROM user_recovery_codes WHERE user_id IN (SELECT id FROM users WHERE email LIKE '%' || $1)`, dom)
		_, _ = tx.Exec(ctx, `DELETE FROM mfa_challenges WHERE user_id IN (SELECT id FROM users WHERE email LIKE '%' || $1)`, dom)
		_, _ = tx.Exec(ctx, `DELETE FROM password_resets WHERE user_id IN (SELECT id FROM users WHERE email LIKE '%' || $1)`, dom)
		_, _ = tx.Exec(ctx, `DELETE FROM user_invitations WHERE user_id IN (SELECT id FROM users WHERE email LIKE '%' || $1)`, dom)

		// Delete user roles
		_, _ = tx.Exec(ctx, `
			DELETE FROM user_roles WHERE user_id IN (
				SELECT id FROM users WHERE email LIKE '%' || $1
			)
		`, dom)

		// Neutralize accounts: strip passwords, revoke MFA, suspend, and scramble email
		tagPurged, err := tx.Exec(ctx, `
			UPDATE users
			SET email = 'purged-' || id || '@purged.invalid',
			    password_hash = NULL,
			    status = 'suspended',
			    mfa_enabled = false,
			    mfa_secret_encrypted = NULL,
			    locked_until = now() + INTERVAL '100 years',
			    updated_at = now()
			WHERE email LIKE '%' || $1
		`, dom)
		if err != nil {
			return fmt.Errorf("purge users for domain %s: %w", dom, err)
		}
		if tagPurged.RowsAffected() > 0 {
			fmt.Printf("[INFO] Neutralized %d dev/legacy users matching domain %q.\n", tagPurged.RowsAffected(), dom)
		}
	}

	// Deactivate and neutralize dev and legacy test organizations
	tagOrgs, err := tx.Exec(ctx, `
		UPDATE organizations
		SET status = 'suspended',
		    name = 'Purged Test Org ' || id,
		    updated_at = now()
		WHERE name LIKE '%(Dev)%'
		   OR name IN ('Apex Telecom', 'BlueStar SMS', 'Fintech Corp')
	`)
	if err != nil {
		return fmt.Errorf("suspend test organizations: %w", err)
	}
	if tagOrgs.RowsAffected() > 0 {
		fmt.Printf("[INFO] Deactivated %d dev/legacy test organizations.\n", tagOrgs.RowsAffected())
	}

	return tx.Commit(ctx)
}

func runSeed(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	orgRepo := org.NewRepository(pool)
	rbacRepo := rbac.NewRepository(pool)

	// 1. Get or verify Platform Organization
	platformOrg, err := orgRepo.GetPlatformOrg(ctx)
	if err != nil || platformOrg == nil {
		return fmt.Errorf("platform root organization not found; run bootstrap-admin first: %w", err)
	}

	if err := syncOrgSystemRoles(ctx, tx, rbacRepo, platformOrg.ID, platformOrg.Type); err != nil {
		return fmt.Errorf("refresh platform system roles: %w", err)
	}

	// Ensure Master Data Editor (Maker) and Master Data Approver (Checker) roles exist in Platform Org with Phase 3 & Phase 4 permissions
	if err := ensureMakerCheckerRoles(ctx, tx, platformOrg.ID); err != nil {
		return err
	}

	orgMap := map[string]*org.Organization{
		"Znero International Platform": platformOrg,
	}

	// 2. Provision or retrieve test organizations
	for _, acc := range devAccounts {
		if _, exists := orgMap[acc.OrgName]; exists {
			continue
		}
		parent, ok := orgMap[acc.ParentName]
		if !ok {
			return fmt.Errorf("parent org %q not found for %q", acc.ParentName, acc.OrgName)
		}

		var existingOrg org.Organization
		err := tx.QueryRow(ctx, `
			SELECT id, name, type, parent_id, path, status, created_at, updated_at
			FROM organizations WHERE name = $1
		`, acc.OrgName).Scan(
			&existingOrg.ID, &existingOrg.Name, &existingOrg.Type,
			&existingOrg.ParentID, &existingOrg.Path, &existingOrg.Status,
			&existingOrg.CreatedAt, &existingOrg.UpdatedAt,
		)
		if err == nil {
			if existingOrg.Status != "active" {
				_, _ = tx.Exec(ctx, `UPDATE organizations SET status = 'active', updated_at = now() WHERE id = $1`, existingOrg.ID)
				existingOrg.Status = "active"
			}
			if err := syncOrgSystemRoles(ctx, tx, rbacRepo, existingOrg.ID, existingOrg.Type); err != nil {
				return fmt.Errorf("refresh roles for %q: %w", existingOrg.Name, err)
			}
			orgMap[acc.OrgName] = &existingOrg
			continue
		}

		newID := uuid.New()
		newPath := fmt.Sprintf("%s%s/", parent.Path, newID.String())
		query := `
			INSERT INTO organizations (id, name, type, parent_id, path, status)
			VALUES ($1, $2, $3, $4, $5, 'active')
			RETURNING id, name, type, parent_id, path, status, created_at, updated_at
		`
		var createdOrg org.Organization
		if err := tx.QueryRow(ctx, query, newID, acc.OrgName, acc.OrgType, parent.ID, newPath).Scan(
			&createdOrg.ID, &createdOrg.Name, &createdOrg.Type,
			&createdOrg.ParentID, &createdOrg.Path, &createdOrg.Status,
			&createdOrg.CreatedAt, &createdOrg.UpdatedAt,
		); err != nil {
			return fmt.Errorf("create org %q: %w", acc.OrgName, err)
		}

		if err := rbacRepo.SeedSystemRoles(ctx, tx, createdOrg.ID, createdOrg.Type); err != nil {
			return fmt.Errorf("seed roles for %q: %w", createdOrg.Name, err)
		}

		orgMap[acc.OrgName] = &createdOrg
		fmt.Printf("[OK] Seeded organization: %s (%s)\n", createdOrg.Name, createdOrg.Type)
	}

	// 3. Provision dev seed accounts
	for _, acc := range devAccounts {
		targetOrg := orgMap[acc.OrgName]
		if targetOrg == nil {
			return fmt.Errorf("target org %q not resolved", acc.OrgName)
		}

		var roleID uuid.UUID
		err := tx.QueryRow(ctx, `
			SELECT id FROM roles WHERE organization_id = $1 AND name = $2
		`, targetOrg.ID, acc.RoleName).Scan(&roleID)
		if err != nil {
			return fmt.Errorf("role %q not found in org %q: %w", acc.RoleName, targetOrg.Name, err)
		}

		pwdHash, err := password.Hash(acc.Password)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}

		var userID uuid.UUID
		err = tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, acc.Email).Scan(&userID)
		if err != nil {
			// Create user
			userID = uuid.New()
			now := time.Now()
			_, err = tx.Exec(ctx, `
				INSERT INTO users (id, organization_id, email, password_hash, status, mfa_enabled, created_at, updated_at)
				VALUES ($1, $2, $3, $4, 'active', false, $5, $5)
			`, userID, targetOrg.ID, acc.Email, pwdHash, now)
			if err != nil {
				return fmt.Errorf("insert user %q: %w", acc.Email, err)
			}
			_, _ = tx.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, roleID)
			fmt.Printf("[OK] Created fake test user: %s (Role: %s, Org: %s)\n", acc.Email, acc.RoleName, targetOrg.Name)
		} else {
			// Update password
			_, _ = tx.Exec(ctx, `UPDATE users SET password_hash = $1, organization_id = $2, status = 'active' WHERE id = $3`, pwdHash, targetOrg.ID, userID)
			_, _ = tx.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, roleID)
			fmt.Printf("[OK] Updated fake test user: %s (Role: %s, Org: %s)\n", acc.Email, acc.RoleName, targetOrg.Name)
		}
	}

	return tx.Commit(ctx)
}

func upsertSystemRole(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, name, desc string, perms []string) error {
	var roleID uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM roles WHERE organization_id = $1 AND name = $2`, orgID, name).Scan(&roleID)
	if err != nil {
		roleID = uuid.New()
		_, err = tx.Exec(ctx, `
			INSERT INTO roles (id, organization_id, name, description, is_system, created_at, updated_at)
			VALUES ($1, $2, $3, $4, true, now(), now())
		`, roleID, orgID, name, desc)
		if err != nil {
			return fmt.Errorf("insert role %s: %w", name, err)
		}
	}
	_, _ = tx.Exec(ctx, `DELETE FROM role_permissions WHERE role_id = $1`, roleID)
	_, err = tx.Exec(ctx, `
		INSERT INTO role_permissions (role_id, permission_code)
		SELECT $1, code FROM permissions WHERE code = ANY($2)
		ON CONFLICT DO NOTHING
	`, roleID, perms)
	return err
}

func syncOrgSystemRoles(ctx context.Context, tx pgx.Tx, rbacRepo rbac.Repository, orgID uuid.UUID, orgType string) error {
	allPerms, err := rbacRepo.ListAllPermissions(ctx)
	if err != nil {
		return err
	}
	var allCodes, orgAdminCodes, staffCodes, readOnlyCodes []string
	for _, p := range allPerms {
		if orgType != "platform" && rbac.IsPlatformOnlyPermission(p.Code) {
			continue
		}
		if orgType == "customer" && rbac.IsForbiddenForCustomerOrg(p.Code) {
			continue
		}
		allCodes = append(allCodes, p.Code)
		if p.Code != "system.info.view" {
			orgAdminCodes = append(orgAdminCodes, p.Code)
		}
		if p.Action == "view" || p.Action == "invite" {
			staffCodes = append(staffCodes, p.Code)
		}
		if p.Action == "view" {
			readOnlyCodes = append(readOnlyCodes, p.Code)
		}
	}
	if orgType == "platform" {
		if err := upsertSystemRole(ctx, tx, orgID, "Super Admin", "Full platform administrative access to all organizations, users, and resources", allCodes); err != nil {
			return err
		}
	}
	if err := upsertSystemRole(ctx, tx, orgID, "Org Admin", "Full administrative access within this organization", orgAdminCodes); err != nil {
		return err
	}
	if err := upsertSystemRole(ctx, tx, orgID, "Staff", "Standard staff operations: view resources and invite users", staffCodes); err != nil {
		return err
	}
	if err := upsertSystemRole(ctx, tx, orgID, "Read-only", "Read-only access to organization resources", readOnlyCodes); err != nil {
		return err
	}
	return nil
}

func ensureMakerCheckerRoles(ctx context.Context, tx pgx.Tx, platformOrgID uuid.UUID) error {
	makerPerms := []string{
		"country.country.view", "country.country.create", "country.country.edit", "country.country.deactivate", "country.country.archive", "country.country.import",
		"operator.operator.view", "operator.operator.create", "operator.operator.edit", "operator.operator.deactivate", "operator.operator.archive", "operator.operator.import",
		"number.prefix.view", "number.prefix.create", "number.prefix.edit", "number.prefix.deactivate", "number.prefix.archive", "number.prefix.import",
		"number.lookup.use", "number.rule.view", "number.rule.create", "number.rule.edit", "number.rule.delete",
		"import.job.view", "import.job.manage", "import.job.create", "import.job.confirm", "import.job.rollback",
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
	checkerPerms := []string{
		"country.country.view", "country.country.approve",
		"operator.operator.view", "operator.operator.approve",
		"number.prefix.view", "number.prefix.approve",
		"number.lookup.use", "number.rule.view", "number.rule.approve",
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

	if err := upsertSystemRole(ctx, tx, platformOrgID, "Master Data Editor", "Maker role without override or approve permissions", makerPerms); err != nil {
		return err
	}
	if err := upsertSystemRole(ctx, tx, platformOrgID, "Master Data Approver", "Checker role with approve permissions", checkerPerms); err != nil {
		return err
	}

	routingMakerPerms := []string{
		"routing.plan.view", "routing.plan.create", "routing.plan.edit", "routing.plan.activate", "routing.plan.deactivate", "routing.plan.archive",
		"routing.entry.view", "routing.entry.create", "routing.entry.edit",
		"routing.assignment.view", "routing.assignment.create", "routing.assignment.edit",
		"routing.rule.view", "routing.rule.create", "routing.rule.edit", "routing.rule.activate",
		"routing.simulate", "routing.snapshot.view",
		"provider.provider.view", "provider.connection.view", "customer.profile.view",
		"country.country.view", "operator.operator.view",
	}
	routingCheckerPerms := []string{
		"routing.plan.view", "routing.plan.approve",
		"routing.entry.view", "routing.entry.approve",
		"routing.assignment.view", "routing.assignment.approve",
		"routing.rule.view", "routing.rule.approve", "routing.rule.activate",
		"routing.simulate", "routing.snapshot.view", "routing.killswitch",
		"provider.provider.view", "provider.connection.view", "customer.profile.view",
		"country.country.view", "operator.operator.view",
	}
	if err := upsertSystemRole(ctx, tx, platformOrgID, "Routing Maker", "Maker: creates and edits route plans, entries, customer assignments, and routing rules; changes require approval", routingMakerPerms); err != nil {
		return err
	}
	if err := upsertSystemRole(ctx, tx, platformOrgID, "Routing Checker", "Checker: reviews and approves or rejects routing change requests and manages emergency kill switch", routingCheckerPerms); err != nil {
		return err
	}
	return nil
}
