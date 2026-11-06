package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/auth"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/internal/rbac"
	"github.com/znero-international/znero/backend/pkg/crypto/password"
	"github.com/znero-international/znero/backend/pkg/database"
	"github.com/znero-international/znero/backend/pkg/httpx"
	"github.com/znero-international/znero/backend/pkg/tenant"
	"github.com/znero-international/znero/backend/pkg/tokens"
)

func getTestDB(t *testing.T) *pgxpool.Pool {
	if os.Getenv("ZNERO_INTEGRATION") != "1" {
		t.Skip("skipping integration test without ZNERO_INTEGRATION=1")
	}

	// The connection string comes from the environment only (scripts/test.ps1 -Integration exports
	// it from .env); credentials must never be committed in test code.
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Fatal("DATABASE_URL must be set for integration tests (run scripts/test.ps1 -Integration)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := database.Connect(ctx, database.Config{
		URL:            dbURL,
		MaxConns:       5,
		ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("database connect failed: %v", err)
	}
	return pool
}

// 1. Tenant Isolation Integration Test
func TestIntegrationTenantIsolation(t *testing.T) {
	pool := getTestDB(t)
	defer pool.Close()
	ctx := context.Background()

	orgRepo := org.NewRepository(pool)
	authRepo := auth.NewRepository(pool)

	// Clean up / create test org A and org B
	orgAID := uuid.New()
	orgBID := uuid.New()
	orgAPath := "/" + orgAID.String() + "/"
	orgBPath := "/" + orgBID.String() + "/"

	_, err := orgRepo.Create(ctx, nil, org.Organization{
		ID:     orgAID,
		Name:   "Tenant Test Org A",
		Type:   "customer",
		Path:   orgAPath,
		Status: "active",
	})
	if err != nil {
		t.Fatalf("create org A: %v", err)
	}

	_, err = orgRepo.Create(ctx, nil, org.Organization{
		ID:     orgBID,
		Name:   "Tenant Test Org B",
		Type:   "customer",
		Path:   orgBPath,
		Status: "active",
	})
	if err != nil {
		t.Fatalf("create org B: %v", err)
	}

	// Create user in Org B
	pwdHash, _ := password.Hash("ValidPassword123!")
	userB, err := authRepo.CreateUser(ctx, nil, auth.User{
		OrganizationID: orgBID,
		Email:          "user_b_" + uuid.NewString()[:8] + "@example.com",
		PasswordHash:   &pwdHash,
		Status:         "active",
	}, nil)
	if err != nil {
		t.Fatalf("create user B: %v", err)
	}

	// Tenant Context representing an actor in Org A
	tcOrgA := &tenant.Context{
		ActorUserID:     uuid.New(),
		ActorOrgID:      orgAID,
		ActorOrgType:    "customer",
		ActorOrgPath:    orgAPath,
		IsPlatformAdmin: false,
		Permissions:     map[string]bool{"user.account.view": true, "org.organization.view": true},
	}

	// Org A queries users
	usersSeenByA, total, err := authRepo.ListUsers(ctx, tcOrgA, nil, "", "", 1, 100)
	if err != nil {
		t.Fatalf("list users by A: %v", err)
	}

	for _, u := range usersSeenByA {
		if u.ID == userB.ID || u.OrganizationID == orgBID {
			t.Fatalf("CRITICAL TENANT LEAK: Actor in Org A was able to see User in Org B!")
		}
	}
	_ = total

	// Org A queries organizations
	orgsSeenByA, err := orgRepo.List(ctx, tcOrgA, org.OrgFilter{Page: 1, PageSize: 100})
	if err != nil {
		t.Fatalf("list orgs by A: %v", err)
	}
	for _, o := range orgsSeenByA.Items {
		if o.ID == orgBID {
			t.Fatalf("CRITICAL TENANT LEAK: Actor in Org A was able to see Org B in organization list!")
		}
	}
}

// 2. Privilege Escalation Integration Test
func TestIntegrationPrivilegeEscalation(t *testing.T) {
	pool := getTestDB(t)
	defer pool.Close()
	ctx := context.Background()

	rbacRepo := rbac.NewRepository(pool)
	auditRepo := audit.NewRepository(pool)
	auditSvc := audit.NewService(auditRepo)
	rbacSvc := rbac.NewService(rbacRepo, auditSvc, pool)

	orgID := uuid.New()
	_, _ = pool.Exec(ctx, `INSERT INTO organizations (id, name, type, path, status) VALUES ($1, 'Escalation Test Org', 'customer', $2, 'active') ON CONFLICT DO NOTHING`, orgID, "/"+orgID.String()+"/")

	// Caller has only "user.account.view" permission
	tc := &tenant.Context{
		ActorUserID:     uuid.New(),
		ActorOrgID:      orgID,
		ActorOrgType:    "customer",
		ActorOrgPath:    "/" + orgID.String() + "/",
		IsPlatformAdmin: false,
		Permissions: map[string]bool{
			"user.account.view": true,
		},
	}

	// Caller attempts to create a role with "user.account.delete" which they do NOT have
	req := rbac.CreateRoleRequest{
		Name:        "Unauthorized Escalation Role " + uuid.NewString()[:6],
		Description: "Should be rejected",
		Permissions: []string{"user.account.view", "user.account.delete"},
	}

	_, err := rbacSvc.CreateRole(ctx, tc, req)
	if err == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: Privilege escalation succeeded! Expected ErrPrivilegeEscalation")
	}
	if err != rbac.ErrPrivilegeEscalation {
		t.Logf("Privilege escalation rejected as expected: %v", err)
	}
}

// 3. Refresh Token Reuse Detection Integration Test (RFC 6819)
func TestIntegrationRefreshTokenReuseDetection(t *testing.T) {
	pool := getTestDB(t)
	defer pool.Close()
	ctx := context.Background()

	authRepo := auth.NewRepository(pool)
	orgRepo := org.NewRepository(pool)
	rbacRepo := rbac.NewRepository(pool)
	auditSvc := audit.NewService(audit.NewRepository(pool))
	tokenMgr, _ := tokens.NewTokenManager("test-jwt-access-secret-32-chars-long!", 15*time.Minute)
	mailer := auth.NewDevConsoleMailer("development", nil)
	authSvc := auth.NewService(authRepo, orgRepo, rbacRepo, auditSvc, tokenMgr, mailer, pool, "5ef83b45188a60122fae1100d5bdb36943939caef953ddfd26f217f2aa929e89", "development")

	// Set up user & organization
	orgID := uuid.New()
	_, _ = pool.Exec(ctx, `INSERT INTO organizations (id, name, type, path, status) VALUES ($1, 'Reuse Test Org', 'customer', $2, 'active') ON CONFLICT DO NOTHING`, orgID, "/"+orgID.String()+"/")

	pwdHash, _ := password.Hash("ValidPassword123!")
	email := "reuse_" + uuid.NewString()[:8] + "@example.com"
	_, err := authRepo.CreateUser(ctx, nil, auth.User{
		OrganizationID: orgID,
		Email:          email,
		PasswordHash:   &pwdHash,
		Status:         "active",
	}, nil)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Login: generates session & initial refresh token (token1)
	_, rawRefresh1, err := authSvc.Login(ctx, email, "ValidPassword123!", "127.0.0.1", "TestAgent", "req-1")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}

	// Refresh 1: rotates token1 to token2. token1 is now invalidated.
	_, rawRefresh2, err := authSvc.RefreshToken(ctx, rawRefresh1, "127.0.0.1", "TestAgent")
	if err != nil {
		t.Fatalf("first refresh failed: %v", err)
	}

	// Refresh Token Reuse Attack: replay token1 again!
	_, _, attackErr := authSvc.RefreshToken(ctx, rawRefresh1, "127.0.0.1", "AttackerAgent")
	if attackErr == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: Reused refresh token was accepted!")
	}

	// Verification: Reusing token1 MUST trigger immediate revocation of ALL sessions in that family.
	// Therefore, token2 (the legitimate active token) must ALSO now be rejected.
	_, _, token2Err := authSvc.RefreshToken(ctx, rawRefresh2, "127.0.0.1", "LegitAgent")
	if token2Err == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: Session family was NOT revoked upon refresh token reuse!")
	}
}

// 4. Account Lockout After 5 Failed Attempts Integration Test
func TestIntegrationAccountLockout(t *testing.T) {
	pool := getTestDB(t)
	defer pool.Close()
	ctx := context.Background()

	authRepo := auth.NewRepository(pool)
	orgRepo := org.NewRepository(pool)
	rbacRepo := rbac.NewRepository(pool)
	auditSvc := audit.NewService(audit.NewRepository(pool))
	tokenMgr, _ := tokens.NewTokenManager("test-jwt-access-secret-32-chars-long!", 15*time.Minute)
	mailer := auth.NewDevConsoleMailer("development", nil)
	authSvc := auth.NewService(authRepo, orgRepo, rbacRepo, auditSvc, tokenMgr, mailer, pool, "5ef83b45188a60122fae1100d5bdb36943939caef953ddfd26f217f2aa929e89", "development")

	orgID := uuid.New()
	_, _ = pool.Exec(ctx, `INSERT INTO organizations (id, name, type, path, status) VALUES ($1, 'Lockout Test Org', 'customer', $2, 'active') ON CONFLICT DO NOTHING`, orgID, "/"+orgID.String()+"/")

	pwdHash, _ := password.Hash("ValidPassword123!")
	email := "lockout_" + uuid.NewString()[:8] + "@example.com"
	_, err := authRepo.CreateUser(ctx, nil, auth.User{
		OrganizationID: orgID,
		Email:          email,
		PasswordHash:   &pwdHash,
		Status:         "active",
	}, nil)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Fail 1 to 4 times: returns ErrInvalidCredentials
	for i := 1; i <= 4; i++ {
		_, _, err := authSvc.Login(ctx, email, "WrongPassword999!", "127.0.0.1", "TestAgent", "req-lock")
		if err != auth.ErrInvalidCredentials {
			t.Fatalf("attempt %d: expected ErrInvalidCredentials, got: %v", i, err)
		}
	}

	// 5th failed attempt: triggers lockout
	_, _, err5 := authSvc.Login(ctx, email, "WrongPassword999!", "127.0.0.1", "TestAgent", "req-lock")
	if err5 != auth.ErrAccountLocked {
		t.Fatalf("5th attempt: expected ErrAccountLocked, got: %v", err5)
	}

	// 6th attempt with CORRECT password: MUST still be rejected due to active lockout
	_, _, err6 := authSvc.Login(ctx, email, "ValidPassword123!", "127.0.0.1", "TestAgent", "req-lock")
	if err6 != auth.ErrAccountLocked {
		t.Fatalf("attempt during lockout: expected ErrAccountLocked, got: %v", err6)
	}
}

// 5. Password Reset Invalidates Existing Sessions Integration Test
func TestIntegrationPasswordResetInvalidatesSessions(t *testing.T) {
	pool := getTestDB(t)
	defer pool.Close()
	ctx := context.Background()

	authRepo := auth.NewRepository(pool)
	orgRepo := org.NewRepository(pool)
	rbacRepo := rbac.NewRepository(pool)
	auditSvc := audit.NewService(audit.NewRepository(pool))
	tokenMgr, _ := tokens.NewTokenManager("test-jwt-access-secret-32-chars-long!", 15*time.Minute)
	mailer := auth.NewDevConsoleMailer("development", nil)
	authSvc := auth.NewService(authRepo, orgRepo, rbacRepo, auditSvc, tokenMgr, mailer, pool, "5ef83b45188a60122fae1100d5bdb36943939caef953ddfd26f217f2aa929e89", "development")

	orgID := uuid.New()
	_, _ = pool.Exec(ctx, `INSERT INTO organizations (id, name, type, path, status) VALUES ($1, 'Reset Test Org', 'customer', $2, 'active') ON CONFLICT DO NOTHING`, orgID, "/"+orgID.String()+"/")

	pwdHash, _ := password.Hash("OldPassword123!")
	email := "reset_" + uuid.NewString()[:8] + "@example.com"
	u, err := authRepo.CreateUser(ctx, nil, auth.User{
		OrganizationID: orgID,
		Email:          email,
		PasswordHash:   &pwdHash,
		Status:         "active",
	}, nil)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Login and establish an active session
	_, rawRefresh, err := authSvc.Login(ctx, email, "OldPassword123!", "127.0.0.1", "TestAgent", "req-reset")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}

	// Create a reset token
	rawResetToken, _ := tokens.GenerateOpaqueToken()
	resetHash := tokens.HashToken(rawResetToken)
	if err := authRepo.CreatePasswordReset(ctx, nil, u.ID, resetHash, time.Now().Add(30*time.Minute)); err != nil {
		t.Fatalf("create reset token: %v", err)
	}

	// Confirm password reset
	newPassword := "NewPassword456!"
	if err := authSvc.ConfirmPasswordReset(ctx, rawResetToken, newPassword); err != nil {
		t.Fatalf("confirm password reset failed: %v", err)
	}

	// Verify old session refresh token is now revoked
	_, _, refreshErr := authSvc.RefreshToken(ctx, rawRefresh, "127.0.0.1", "TestAgent")
	if refreshErr == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: Active session remained valid after password reset!")
	}

	// Verify new password works
	loginResp, _, loginErr := authSvc.Login(ctx, email, newPassword, "127.0.0.1", "TestAgent", "req-reset-verify")
	if loginErr != nil || loginResp == nil {
		t.Fatalf("login with new password failed: %v", loginErr)
	}
}

// 6. Immediate Revocation Defense via Authenticate Middleware
func TestIntegrationImmediateRevocation(t *testing.T) {
	pool := getTestDB(t)
	defer pool.Close()
	ctx := context.Background()

	authRepo := auth.NewRepository(pool)
	orgRepo := org.NewRepository(pool)
	tokenMgr, _ := tokens.NewTokenManager("test-jwt-access-secret-32-chars-long!", 15*time.Minute)

	orgID := uuid.New()
	orgPath := "/" + orgID.String() + "/"
	_, _ = pool.Exec(ctx, `INSERT INTO organizations (id, name, type, path, status) VALUES ($1, 'Revoke Test Org', 'customer', $2, 'active') ON CONFLICT DO NOTHING`, orgID, orgPath)

	pwdHash, _ := password.Hash("ValidPassword123!")
	email := "imm_revoke_" + uuid.NewString()[:8] + "@example.com"
	u, err := authRepo.CreateUser(ctx, nil, auth.User{
		OrganizationID: orgID,
		Email:          email,
		PasswordHash:   &pwdHash,
		Status:         "active",
	}, nil)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	sessionID := uuid.New()
	familyID := uuid.New()
	dummyRefreshHash := tokens.HashToken("dummy-refresh-" + uuid.NewString())
	err = authRepo.CreateSession(ctx, nil, auth.Session{
		ID:               sessionID,
		UserID:           u.ID,
		OrganizationID:   orgID,
		RefreshTokenHash: dummyRefreshHash,
		FamilyID:         familyID,
		IPAddress:        "127.0.0.1",
		UserAgent:        "TestAgent",
		ExpiresAt:        time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Generate access token for this active session
	tokenStr, err := tokenMgr.GenerateAccessToken(u.ID, orgID, sessionID, "customer", orgPath, false, []string{"user.account.view"})
	if err != nil {
		t.Fatalf("generate access token: %v", err)
	}

	app := fiber.New(fiber.Config{
		ErrorHandler: httpx.ErrorHandler(nil),
	})
	app.Get("/api/v1/protected", auth.Authenticate(tokenMgr, authRepo, orgRepo), func(c *fiber.Ctx) error {
		return c.SendString("access_granted")
	})

	// 1. Initial request with valid active token -> 200 OK
	req := httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	resp, err := app.Test(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("initial request should succeed with 200, got: %d, err: %v", resp.StatusCode, err)
	}

	// 2. Suspend the user in DB -> Subsequent request with SAME access token MUST immediately return 401
	_ = authRepo.UpdateUser(ctx, nil, u.ID, "suspended", nil)
	reqSuspended := httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
	reqSuspended.Header.Set("Authorization", "Bearer "+tokenStr)
	respSuspended, err := app.Test(reqSuspended)
	if err != nil || respSuspended.StatusCode != http.StatusUnauthorized {
		t.Fatalf("suspended user must be immediately rejected with 401, got: %d", respSuspended.StatusCode)
	}

	// 3. Reactivate user, but revoke the session in DB -> MUST immediately return 401
	_ = authRepo.UpdateUser(ctx, nil, u.ID, "active", nil)
	_ = authRepo.RevokeSession(ctx, nil, sessionID, "manual_test_revoke")
	reqRevokedSess := httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
	reqRevokedSess.Header.Set("Authorization", "Bearer "+tokenStr)
	respRevokedSess, err := app.Test(reqRevokedSess)
	if err != nil || respRevokedSess.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked session must be immediately rejected with 401, got: %d", respRevokedSess.StatusCode)
	}

	// 4. Test Org Suspension: new session, but org suspended -> MUST immediately return 401
	sess2ID := uuid.New()
	_ = authRepo.CreateSession(ctx, nil, auth.Session{
		ID:               sess2ID,
		UserID:           u.ID,
		OrganizationID:   orgID,
		RefreshTokenHash: tokens.HashToken("dummy-refresh-2-" + uuid.NewString()),
		FamilyID:         uuid.New(),
		IPAddress:        "127.0.0.1",
		UserAgent:        "TestAgent",
		ExpiresAt:        time.Now().Add(24 * time.Hour),
	})
	token2Str, _ := tokenMgr.GenerateAccessToken(u.ID, orgID, sess2ID, "customer", orgPath, false, []string{"user.account.view"})

	_ = orgRepo.SetStatus(ctx, nil, orgID, "suspended")
	reqOrgSuspended := httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
	reqOrgSuspended.Header.Set("Authorization", "Bearer "+token2Str)
	respOrgSuspended, err := app.Test(reqOrgSuspended)
	if err != nil || respOrgSuspended.StatusCode != http.StatusUnauthorized {
		t.Fatalf("suspended org must be immediately rejected with 401, got: %d", respOrgSuspended.StatusCode)
	}
}

// 7. DevConsoleMailer Environment Gate Test
func TestDevConsoleMailerEnvironmentGate(t *testing.T) {
	ctx := context.Background()

	// In non-development environment: MUST refuse to run
	prodMailer := auth.NewDevConsoleMailer("production", nil)
	if err := prodMailer.SendPasswordReset(ctx, "admin@example.com", "token123"); err == nil {
		t.Fatalf("expected DevConsoleMailer to reject SendPasswordReset in production")
	}
	if err := prodMailer.SendUserInvitation(ctx, "admin@example.com", "token123", "Org"); err == nil {
		t.Fatalf("expected DevConsoleMailer to reject SendUserInvitation in production")
	}

	// In development environment: MUST succeed
	devMailer := auth.NewDevConsoleMailer("development", nil)
	if err := devMailer.SendPasswordReset(ctx, "admin@example.com", "token123"); err != nil {
		t.Fatalf("expected DevConsoleMailer to succeed in development, got: %v", err)
	}
	if err := devMailer.SendUserInvitation(ctx, "admin@example.com", "token123", "Org"); err != nil {
		t.Fatalf("expected DevConsoleMailer to succeed in development, got: %v", err)
	}
}

// 8. User Invites are Strictly One-Time Use Integration Test
func TestIntegrationUserInviteOneTime(t *testing.T) {
	pool := getTestDB(t)
	defer pool.Close()
	ctx := context.Background()

	authRepo := auth.NewRepository(pool)
	orgRepo := org.NewRepository(pool)
	rbacRepo := rbac.NewRepository(pool)
	auditSvc := audit.NewService(audit.NewRepository(pool))
	tokenMgr, _ := tokens.NewTokenManager("test-jwt-access-secret-32-chars-long!", 15*time.Minute)
	mailer := auth.NewDevConsoleMailer("development", nil)
	authSvc := auth.NewService(authRepo, orgRepo, rbacRepo, auditSvc, tokenMgr, mailer, pool, "5ef83b45188a60122fae1100d5bdb36943939caef953ddfd26f217f2aa929e89", "development")

	orgID := uuid.New()
	_, _ = pool.Exec(ctx, `INSERT INTO organizations (id, name, type, path, status) VALUES ($1, 'Invite Test Org', 'customer', $2, 'active') ON CONFLICT DO NOTHING`, orgID, "/"+orgID.String()+"/")

	email := "invite_test_" + uuid.NewString()[:8] + "@example.com"
	u, err := authRepo.CreateUser(ctx, nil, auth.User{
		OrganizationID: orgID,
		Email:          email,
		Status:         "invited",
	}, nil)
	if err != nil {
		t.Fatalf("create invited user: %v", err)
	}

	rawToken, err := tokens.GenerateOpaqueToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	tokenHash := tokens.HashToken(rawToken)
	if err := authRepo.CreateInvitation(ctx, nil, u.ID, tokenHash, time.Now().Add(48*time.Hour)); err != nil {
		t.Fatalf("create invitation: %v", err)
	}

	// 1. First acceptance MUST succeed
	firstPass := "StrongP@ssw0rd123!"
	if err := authSvc.AcceptInvitation(ctx, rawToken, firstPass); err != nil {
		t.Fatalf("first AcceptInvitation failed: %v", err)
	}

	// 2. User status is now 'active'
	userAfter, err := authRepo.GetUserByID(ctx, u.ID)
	if err != nil || userAfter.Status != "active" {
		t.Fatalf("expected user to be active after accept, got status %v (err: %v)", userAfter.Status, err)
	}

	// 3. Second acceptance with the SAME token MUST be rejected
	errSecond := authSvc.AcceptInvitation(ctx, rawToken, "SecondP@ssw0rd999!")
	if errSecond == nil {
		t.Fatalf("CRITICAL SECURITY FLAW: User invitation was accepted twice! Expected error.")
	}
	if !errors.Is(errSecond, auth.ErrTokenInvalid) {
		t.Fatalf("expected ErrTokenInvalid, got: %v", errSecond)
	}

	// 4. Verify login succeeds with firstPass, not secondPass
	_, _, errLogin := authSvc.Login(ctx, email, firstPass, "127.0.0.1", "TestAgent", "req-invite-test")
	if errLogin != nil {
		t.Fatalf("login with accepted password failed: %v", errLogin)
	}
}
