package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/config"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/internal/rbac"
	"github.com/znero-international/znero/backend/pkg/crypto/encrypt"
	"github.com/znero-international/znero/backend/pkg/crypto/password"
	"github.com/znero-international/znero/backend/pkg/crypto/totp"
	"github.com/znero-international/znero/backend/pkg/database"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "\n[BOOTSTRAP ERROR] %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	emailFlag := flag.String("email", "", "Super Admin email address")
	pwdFlag := flag.String("password", "", "Super Admin password (min 12 characters)")
	nonInteractive := flag.Bool("non-interactive", false, "Fail if email/password not provided via flags/env")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := database.Connect(ctx, database.Config{
		URL:            cfg.Database.URL,
		MaxConns:       5,
		ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	rbacRepo := rbac.NewRepository(pool)
	orgRepo := org.NewRepository(pool)
	auditSvc := audit.NewService(audit.NewRepository(pool))

	// 1. Check if a Super Admin already exists
	var existingCount int
	err = pool.QueryRow(ctx, `
		SELECT count(u.id)
		FROM users u
		JOIN user_roles ur ON u.id = ur.user_id
		JOIN roles r ON ur.role_id = r.id
		JOIN organizations o ON u.organization_id = o.id
		WHERE o.type = 'platform' AND r.name = 'Super Admin'
	`).Scan(&existingCount)
	if err != nil {
		return fmt.Errorf("check existing super admin: %w", err)
	}
	if existingCount > 0 {
		fmt.Println("[INFO] Super Admin already exists. Bootstrap aborted to prevent hijacking.")
		return nil
	}

	// 2. Ensure Platform Root Organization exists
	platformOrg, err := orgRepo.GetPlatformOrg(ctx)
	if err != nil || platformOrg == nil {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin tx for platform org: %w", err)
		}
		defer tx.Rollback(ctx)

		pID := uuid.New()
		path := fmt.Sprintf("/%s/", pID.String())
		query := `INSERT INTO organizations (id, name, type, parent_id, path, status) VALUES ($1, $2, 'platform', NULL, $3, 'active') RETURNING id, name, type, path, status, created_at, updated_at`
		var newOrg org.Organization
		if err := tx.QueryRow(ctx, query, pID, "Znero International Platform", path).Scan(
			&newOrg.ID, &newOrg.Name, &newOrg.Type, &newOrg.Path, &newOrg.Status, &newOrg.CreatedAt, &newOrg.UpdatedAt,
		); err != nil {
			return fmt.Errorf("insert platform org: %w", err)
		}

		if err := rbacRepo.SeedSystemRoles(ctx, tx, newOrg.ID, "platform"); err != nil {
			return fmt.Errorf("seed platform roles: %w", err)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit platform org: %w", err)
		}
		platformOrg = &newOrg
		fmt.Printf("[OK] Created platform organization: %s (%s)\n", platformOrg.Name, platformOrg.ID)
	} else {
		// Ensure system roles exist for existing platform org
		var roleCount int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM roles WHERE organization_id = $1`, platformOrg.ID).Scan(&roleCount)
		if roleCount == 0 {
			tx, err := pool.Begin(ctx)
			if err == nil {
				_ = rbacRepo.SeedSystemRoles(ctx, tx, platformOrg.ID, "platform")
				_ = tx.Commit(ctx)
			}
		}
	}

	// 3. Resolve Super Admin Role ID
	var superAdminRoleID uuid.UUID
	err = pool.QueryRow(ctx, `
		SELECT id FROM roles WHERE organization_id = $1 AND name = 'Super Admin'
	`, platformOrg.ID).Scan(&superAdminRoleID)
	if err != nil {
		return fmt.Errorf("resolve Super Admin role: %w", err)
	}

	// 4. Resolve Email and Password
	email := strings.TrimSpace(*emailFlag)
	if email == "" {
		email = strings.TrimSpace(os.Getenv("BOOTSTRAP_ADMIN_EMAIL"))
	}
	if email == "" {
		if *nonInteractive {
			return fmt.Errorf("email required in non-interactive mode")
		}
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("Enter Super Admin email: ")
		input, _ := reader.ReadString('\n')
		email = strings.TrimSpace(input)
	}
	if email == "" || !strings.Contains(email, "@") {
		return fmt.Errorf("invalid email address %q", email)
	}

	passwordStr := *pwdFlag
	if passwordStr == "" {
		passwordStr = os.Getenv("BOOTSTRAP_ADMIN_PASSWORD")
	}
	if passwordStr == "" {
		if *nonInteractive {
			return fmt.Errorf("password required in non-interactive mode")
		}
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("Enter Super Admin password (min 12 chars): ")
		input, _ := reader.ReadString('\n')
		passwordStr = strings.TrimSpace(input)
	}

	if err := password.ValidateStrength(passwordStr); err != nil {
		return fmt.Errorf("password validation failed: %w", err)
	}

	pwdHash, err := password.Hash(passwordStr)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	// 5. Generate TOTP enrollment secret and recovery codes
	totpSecret, err := totp.GenerateSecret()
	if err != nil {
		return fmt.Errorf("generate totp secret: %w", err)
	}

	encryptedSecret, err := encrypt.EncryptAESGCM(totpSecret, cfg.Auth.MFAEncryptionKey)
	if err != nil {
		return fmt.Errorf("encrypt mfa secret: %w", err)
	}

	recoveryCodes, err := totp.GenerateRecoveryCodes(10)
	if err != nil {
		return fmt.Errorf("generate recovery codes: %w", err)
	}

	// 6. In Transaction: Create User, Assign Super Admin Role, Store Recovery Codes, Audit Log
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	userID := uuid.New()
	now := time.Now()
	queryUser := `
		INSERT INTO users (
			id, organization_id, email, password_hash, status,
			mfa_enabled, mfa_secret_encrypted, mfa_enrolled_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, 'active', true, $5, $6, $7, $7)
	`
	if _, err := tx.Exec(ctx, queryUser, userID, platformOrg.ID, email, pwdHash, encryptedSecret, now, now); err != nil {
		return fmt.Errorf("insert super admin user: %w", err)
	}

	queryUserRole := `INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2)`
	if _, err := tx.Exec(ctx, queryUserRole, userID, superAdminRoleID); err != nil {
		return fmt.Errorf("assign super admin role: %w", err)
	}

	for _, rc := range recoveryCodes {
		if _, err := tx.Exec(ctx, `INSERT INTO user_recovery_codes (user_id, code_hash) VALUES ($1, $2)`, userID, rc.CodeHash); err != nil {
			return fmt.Errorf("save recovery code: %w", err)
		}
	}

	if err := auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &userID,
		ActorOrgID:  &platformOrg.ID,
		TargetOrgID: &platformOrg.ID,
		Action:      "system.bootstrap.create_admin",
		Module:      "system",
		EntityType:  "user",
		EntityID:    userID.String(),
		Reason:      "Initial platform Super Admin bootstrapped via CLI",
	}); err != nil {
		return fmt.Errorf("record audit log: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit super admin bootstrap: %w", err)
	}

	totpURI := totp.GenerateURI("Znero International", email, totpSecret)

	fmt.Println()
	fmt.Println("================================================================================")
	fmt.Println("                SUPER ADMIN BOOTSTRAP COMPLETED SUCCESSFULLY                   ")
	fmt.Println("================================================================================")
	fmt.Printf("Email:           %s\n", email)
	fmt.Printf("User ID:         %s\n", userID)
	fmt.Printf("Platform Org ID: %s\n", platformOrg.ID)
	fmt.Printf("Role:            Super Admin\n")
	fmt.Println("--------------------------------------------------------------------------------")
	fmt.Println("MFA ENROLLMENT DETAILS (SAVE IMMEDIATELY):")
	fmt.Printf("TOTP Secret:     %s\n", totpSecret)
	fmt.Printf("OTPAuth URI:     %s\n", totpURI)
	fmt.Println("--------------------------------------------------------------------------------")
	fmt.Println("SINGLE-USE RECOVERY CODES:")
	for i, rc := range recoveryCodes {
		fmt.Printf("  [%2d] %s\n", i+1, rc.Code)
	}
	fmt.Println("================================================================================")
	fmt.Println("Keep these recovery codes in a secure vault. They will not be displayed again.")
	fmt.Println()

	return nil
}
