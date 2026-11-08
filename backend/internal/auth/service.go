package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/org"
	"github.com/znero-international/znero/backend/internal/rbac"
	"github.com/znero-international/znero/backend/pkg/crypto/encrypt"
	"github.com/znero-international/znero/backend/pkg/crypto/password"
	"github.com/znero-international/znero/backend/pkg/crypto/totp"
	"github.com/znero-international/znero/backend/pkg/tenant"
	"github.com/znero-international/znero/backend/pkg/tokens"
)

var (
	ErrInvalidCredentials      = errors.New("invalid email or password")
	ErrAccountLocked           = errors.New("account is temporarily locked due to multiple failed login attempts; please try again in 15 minutes")
	ErrAccountSuspended        = errors.New("account is suspended")
	ErrOrgSuspended            = errors.New("organization is suspended")
	ErrInvalidMFACode          = errors.New("invalid authentication code or recovery code")
	ErrInvalidChallenge        = errors.New("invalid or expired authentication challenge")
	ErrMFANotEnabled           = errors.New("MFA is not enabled for this user")
	ErrCannotSuspendSelf       = errors.New("you cannot suspend your own account")
	ErrPrivilegeEscalation     = errors.New("privilege escalation: cannot grant permissions you do not possess")
	ErrEmailAlreadyExists      = errors.New("user with this email already exists")
	ErrSessionExpiredOrRevoked = errors.New("session has expired or been revoked")
	ErrCurrentPasswordInvalid  = errors.New("current password is incorrect")
)

// Precomputed Argon2id dummy hash for constant-time comparison when email is not found.
const dummyArgon2idHash = "$argon2id$v=19$m=65536,t=3,p=4$dGVzdHNhbHQxMjM0NTY3OA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

type Service struct {
	repo      Repository
	orgRepo   org.Repository
	rbacRepo  rbac.Repository
	auditSvc  *audit.Service
	tokenMgr  *tokens.TokenManager
	mailer    Mailer
	pool      *pgxpool.Pool
	mfaKeyHex string
	env       string
}

func NewService(
	repo Repository,
	orgRepo org.Repository,
	rbacRepo rbac.Repository,
	auditSvc *audit.Service,
	tokenMgr *tokens.TokenManager,
	mailer Mailer,
	pool *pgxpool.Pool,
	mfaKeyHex string,
	env string,
) *Service {
	return &Service{
		repo:      repo,
		orgRepo:   orgRepo,
		rbacRepo:  rbacRepo,
		auditSvc:  auditSvc,
		tokenMgr:  tokenMgr,
		mailer:    mailer,
		pool:      pool,
		mfaKeyHex: mfaKeyHex,
		env:       env,
	}
}

// Login performs authentication with Argon2id, constant-time verification,
// failed-attempt tracking, temporary lockout, and MFA challenge enforcement.
func (s *Service) Login(ctx context.Context, email, plainPassword, ip, userAgent, reqID string) (*LoginResponse, string, error) {
	email = strings.TrimSpace(email)
	u, err := s.repo.GetUserByEmail(ctx, email)
	if err != nil || u == nil {
		// Constant-time dummy verification to defeat timing attacks
		_, _ = password.Verify(plainPassword, dummyArgon2idHash)
		// Record failed attempt
		_, _, _ = s.repo.IncrementFailedLogin(ctx, email, 5, 15*time.Minute)
		_ = s.repo.RecordLoginHistory(ctx, LoginHistoryItem{
			Email:     email,
			Success:   false,
			Reason:    "invalid_credentials",
			IPAddress: ip,
			UserAgent: userAgent,
		}, nil, reqID)
		return nil, "", ErrInvalidCredentials
	}

	// Check if account is currently locked
	if u.LockedUntil != nil && u.LockedUntil.After(time.Now()) {
		_ = s.repo.RecordLoginHistory(ctx, LoginHistoryItem{
			Email:     email,
			Success:   false,
			Reason:    "account_locked",
			IPAddress: ip,
			UserAgent: userAgent,
		}, &u.OrganizationID, reqID)
		return nil, "", ErrAccountLocked
	}

	if u.Status == "suspended" {
		_ = s.repo.RecordLoginHistory(ctx, LoginHistoryItem{
			Email:     email,
			Success:   false,
			Reason:    "account_suspended",
			IPAddress: ip,
			UserAgent: userAgent,
		}, &u.OrganizationID, reqID)
		return nil, "", ErrAccountSuspended
	}

	match := false
	if u.PasswordHash != nil {
		match, _ = password.Verify(plainPassword, *u.PasswordHash)
	}
	if !match {
		attempts, isLocked, _ := s.repo.IncrementFailedLogin(ctx, email, 5, 15*time.Minute)
		reason := "invalid_credentials"
		if isLocked || attempts >= 5 {
			reason = "account_locked"
		}
		_ = s.repo.RecordLoginHistory(ctx, LoginHistoryItem{
			Email:     email,
			Success:   false,
			Reason:    reason,
			IPAddress: ip,
			UserAgent: userAgent,
		}, &u.OrganizationID, reqID)
		if isLocked {
			return nil, "", ErrAccountLocked
		}
		return nil, "", ErrInvalidCredentials
	}

	// Verify organization status
	orgRecord, err := s.orgRepo.GetByID(ctx, u.OrganizationID)
	if err != nil || orgRecord.Status == "suspended" {
		_ = s.repo.RecordLoginHistory(ctx, LoginHistoryItem{
			Email:     email,
			Success:   false,
			Reason:    "org_suspended",
			IPAddress: ip,
			UserAgent: userAgent,
		}, &u.OrganizationID, reqID)
		return nil, "", ErrOrgSuspended
	}

	// Reset failed attempts on valid credentials
	_ = s.repo.ResetFailedLogin(ctx, u.ID)

	// Determine if MFA is required:
	// 1. If MFA is enrolled (mfa_enabled = true)
	// 2. Mandatory for platform Super Admins
	isPlatformAdmin := orgRecord.Type == "platform" && containsRole(u.Roles, "Super Admin")
	requiresMFA := u.MFAEnabled || isPlatformAdmin

	if requiresMFA && u.MFASecretEncrypted != nil {
		challengeToken, err := tokens.GenerateOpaqueToken()
		if err != nil {
			return nil, "", fmt.Errorf("generate challenge: %w", err)
		}
		tokenHash := tokens.HashToken(challengeToken)
		expiry := time.Now().Add(5 * time.Minute)

		if err := s.repo.CreateMFAChallenge(ctx, u.ID, tokenHash, expiry, ip); err != nil {
			return nil, "", fmt.Errorf("create challenge: %w", err)
		}

		_ = s.repo.RecordLoginHistory(ctx, LoginHistoryItem{
			Email:     email,
			Success:   false,
			Reason:    "mfa_challenge_issued",
			IPAddress: ip,
			UserAgent: userAgent,
		}, &u.OrganizationID, reqID)

		return &LoginResponse{
			MFARequired: true,
			MFAToken:    challengeToken,
		}, "", nil
	}

	// Successful login without MFA
	resp, rawRefresh, err := s.createSessionAndTokens(ctx, u, orgRecord, isPlatformAdmin, ip, userAgent)
	if err != nil {
		return nil, "", err
	}

	_ = s.repo.RecordLoginHistory(ctx, LoginHistoryItem{
		Email:     email,
		Success:   true,
		Reason:    "password",
		IPAddress: ip,
		UserAgent: userAgent,
	}, &u.OrganizationID, reqID)

	return resp, rawRefresh, nil
}

// VerifyMFA validates the MFA challenge using either TOTP or a single-use recovery code.
func (s *Service) VerifyMFA(ctx context.Context, challengeToken, code, ip, userAgent, reqID string) (*LoginResponse, string, error) {
	tokenHash := tokens.HashToken(challengeToken)
	userID, err := s.repo.VerifyMFAChallenge(ctx, tokenHash)
	if err != nil || userID == nil {
		return nil, "", ErrInvalidChallenge
	}

	u, err := s.repo.GetUserByID(ctx, *userID)
	if err != nil || u == nil {
		return nil, "", ErrUserNotFound
	}
	if u.Status == "suspended" {
		return nil, "", ErrAccountSuspended
	}

	orgRecord, err := s.orgRepo.GetByID(ctx, u.OrganizationID)
	if err != nil || orgRecord.Status == "suspended" {
		return nil, "", ErrOrgSuspended
	}

	code = strings.TrimSpace(code)
	valid := false

	// Try TOTP first if secret is configured
	if u.MFASecretEncrypted != nil && *u.MFASecretEncrypted != "" {
		decryptedSecret, err := encrypt.DecryptAESGCM(*u.MFASecretEncrypted, s.mfaKeyHex)
		if err == nil && totp.ValidateCode(decryptedSecret, code, time.Now()) {
			valid = true
		}
	}

	// If TOTP fails, check recovery codes
	if !valid {
		tx, err := s.pool.Begin(ctx)
		if err == nil {
			recoveryHash := tokens.HashToken(code)
			consumed, err := s.repo.VerifyAndConsumeRecoveryCode(ctx, tx, u.ID, recoveryHash)
			if err == nil && consumed {
				_ = tx.Commit(ctx)
				valid = true
			} else {
				_ = tx.Rollback(ctx)
			}
		}
	}

	if !valid {
		_ = s.repo.RecordLoginHistory(ctx, LoginHistoryItem{
			Email:     u.Email,
			Success:   false,
			Reason:    "mfa_failed",
			IPAddress: ip,
			UserAgent: userAgent,
		}, &u.OrganizationID, reqID)
		return nil, "", ErrInvalidMFACode
	}

	isPlatformAdmin := orgRecord.Type == "platform" && containsRole(u.Roles, "Super Admin")
	resp, rawRefresh, err := s.createSessionAndTokens(ctx, u, orgRecord, isPlatformAdmin, ip, userAgent)
	if err != nil {
		return nil, "", err
	}

	_ = s.repo.RecordLoginHistory(ctx, LoginHistoryItem{
		Email:     u.Email,
		Success:   true,
		Reason:    "mfa_verified",
		IPAddress: ip,
		UserAgent: userAgent,
	}, &u.OrganizationID, reqID)

	return resp, rawRefresh, nil
}

// SetupMFA generates a new TOTP secret and 10 recovery codes.
// The secret is NOT committed to the database until confirmed with ConfirmMFA.
func (s *Service) SetupMFA(ctx context.Context, tc *tenant.Context) (*MFASetupResponse, error) {
	u, err := s.repo.GetUserByID(ctx, tc.ActorUserID)
	if err != nil || u == nil {
		return nil, ErrUserNotFound
	}

	secret, err := totp.GenerateSecret()
	if err != nil {
		return nil, fmt.Errorf("generate totp secret: %w", err)
	}

	uri := totp.GenerateURI("Znero International", u.Email, secret)
	recoveryCodes, err := totp.GenerateRecoveryCodes(10)
	if err != nil {
		return nil, fmt.Errorf("generate recovery codes: %w", err)
	}

	var codeStrings []string
	for _, rc := range recoveryCodes {
		codeStrings = append(codeStrings, rc.Code)
	}

	return &MFASetupResponse{
		Secret:        secret,
		URI:           uri,
		RecoveryCodes: codeStrings,
	}, nil
}

// ConfirmMFA verifies the enrollment code against the secret, encrypts the secret with AES-256-GCM,
// stores hashed recovery codes, activates MFA on the user, and audits the change.
func (s *Service) ConfirmMFA(ctx context.Context, tc *tenant.Context, req MFAConfirmRequest) error {
	secret := strings.TrimSpace(req.Secret)
	code := strings.TrimSpace(req.Code)

	if !totp.ValidateCode(secret, code, time.Now()) {
		return ErrInvalidMFACode
	}

	encryptedSecret, err := encrypt.EncryptAESGCM(secret, s.mfaKeyHex)
	if err != nil {
		return fmt.Errorf("encrypt mfa secret: %w", err)
	}

	var codeHashes []string
	for _, rc := range req.RecoveryCodes {
		rc = strings.TrimSpace(rc)
		if rc != "" {
			codeHashes = append(codeHashes, tokens.HashToken(rc))
		}
	}
	if len(codeHashes) != 10 {
		return errors.New("exactly 10 recovery codes are required for confirmation")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := s.repo.UpdateMFA(ctx, tx, tc.ActorUserID, true, &encryptedSecret); err != nil {
		return fmt.Errorf("update mfa: %w", err)
	}

	if err := s.repo.SaveRecoveryCodes(ctx, tx, tc.ActorUserID, codeHashes); err != nil {
		return fmt.Errorf("save recovery codes: %w", err)
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		TargetOrgID: &tc.ActorOrgID,
		Action:      "auth.mfa.enable",
		Module:      "auth",
		EntityType:  "user",
		EntityID:    tc.ActorUserID.String(),
		Reason:      "User enrolled and confirmed TOTP MFA",
	}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	return tx.Commit(ctx)
}

// RefreshToken implements RFC 6819 rotating refresh tokens and reuse detection.
func (s *Service) RefreshToken(ctx context.Context, rawRefreshToken, ip, userAgent string) (*LoginResponse, string, error) {
	tokenHash := tokens.HashToken(rawRefreshToken)
	sess, err := s.repo.GetSessionByRefreshHash(ctx, tokenHash)
	if err != nil || sess == nil {
		return nil, "", ErrSessionExpiredOrRevoked
	}

	// Reuse detection: If a refresh token for a revoked session is presented,
	// revoke ALL sessions in that family immediately.
	if sess.RevokedAt != nil {
		_ = s.repo.RevokeFamilySessions(ctx, nil, sess.FamilyID, "reuse_detected")
		return nil, "", ErrSessionExpiredOrRevoked
	}

	if sess.ExpiresAt.Before(time.Now()) {
		_ = s.repo.RevokeSession(ctx, nil, sess.ID, "expired")
		return nil, "", ErrSessionExpiredOrRevoked
	}

	u, err := s.repo.GetUserByID(ctx, sess.UserID)
	if err != nil || u == nil || u.Status != "active" {
		_ = s.repo.RevokeSession(ctx, nil, sess.ID, "user_inactive")
		return nil, "", ErrAccountSuspended
	}

	orgRecord, err := s.orgRepo.GetByID(ctx, sess.OrganizationID)
	if err != nil || orgRecord.Status != "active" {
		_ = s.repo.RevokeSession(ctx, nil, sess.ID, "org_inactive")
		return nil, "", ErrOrgSuspended
	}

	// Rotate token: invalidate old session as rotated and issue new session in same family
	newRawRefresh, err := tokens.GenerateOpaqueToken()
	if err != nil {
		return nil, "", fmt.Errorf("generate refresh: %w", err)
	}
	newHash := tokens.HashToken(newRawRefresh)
	newExpiry := time.Now().Add(7 * 24 * time.Hour)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Mark old session as rotated
	if err := s.repo.RevokeSession(ctx, tx, sess.ID, "rotated"); err != nil {
		return nil, "", fmt.Errorf("rotate token: %w", err)
	}

	// Create new session in same family
	newSessionID := uuid.New()
	newSess := Session{
		ID:               newSessionID,
		UserID:           sess.UserID,
		OrganizationID:   sess.OrganizationID,
		RefreshTokenHash: newHash,
		FamilyID:         sess.FamilyID,
		IPAddress:        ip,
		UserAgent:        userAgent,
		ExpiresAt:        newExpiry,
	}
	if err := s.repo.CreateSession(ctx, tx, newSess); err != nil {
		return nil, "", fmt.Errorf("create new rotated session: %w", err)
	}

	perms, err := s.rbacRepo.GetUserPermissions(ctx, u.ID)
	if err != nil {
		return nil, "", fmt.Errorf("get permissions: %w", err)
	}

	isPlatformAdmin := orgRecord.Type == "platform" && containsRole(u.Roles, "Super Admin")
	accessJWT, err := s.tokenMgr.GenerateAccessToken(
		u.ID,
		orgRecord.ID,
		newSessionID,
		orgRecord.Type,
		orgRecord.Path,
		isPlatformAdmin,
		perms,
	)
	if err != nil {
		return nil, "", fmt.Errorf("generate access token: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, "", fmt.Errorf("commit tx: %w", err)
	}

	return &LoginResponse{
		AccessToken: accessJWT,
		ExpiresIn:   900,
		User:        u,
	}, newRawRefresh, nil
}

// Logout revokes the session associated with the provided refresh token.
func (s *Service) Logout(ctx context.Context, rawRefreshToken string) error {
	tokenHash := tokens.HashToken(rawRefreshToken)
	sess, err := s.repo.GetSessionByRefreshHash(ctx, tokenHash)
	if err != nil || sess == nil {
		return nil
	}
	return s.repo.RevokeSession(ctx, nil, sess.ID, "user_logout")
}

// LogoutAll revokes all active sessions for a user.
func (s *Service) LogoutAll(ctx context.Context, userID uuid.UUID) error {
	return s.repo.RevokeAllUserSessions(ctx, nil, userID, "user_logout_all")
}

// ListSessions returns all active sessions for the user.
func (s *Service) ListSessions(ctx context.Context, userID, currentSessionID uuid.UUID) ([]SessionInfo, error) {
	sessions, err := s.repo.ListActiveSessions(ctx, userID)
	if err != nil {
		return nil, err
	}

	var res []SessionInfo
	for _, sess := range sessions {
		res = append(res, SessionInfo{
			ID:        sess.ID,
			IPAddress: sess.IPAddress,
			UserAgent: sess.UserAgent,
			CreatedAt: sess.CreatedAt,
			LastUsed:  sess.LastUsedAt,
			IsCurrent: sess.ID == currentSessionID,
		})
	}
	return res, nil
}

// RevokeSession revokes a specific session for the user.
func (s *Service) RevokeSession(ctx context.Context, userID, sessionID uuid.UUID) error {
	sessions, err := s.repo.ListActiveSessions(ctx, userID)
	if err != nil {
		return err
	}
	belongs := false
	for _, sess := range sessions {
		if sess.ID == sessionID {
			belongs = true
			break
		}
	}
	if !belongs {
		return ErrSessionNotFound
	}
	return s.repo.RevokeSession(ctx, nil, sessionID, "user_manual_revoke")
}

// RequestPasswordReset creates a single-use token and dispatches an email via Mailer.
// Generic response prevents user enumeration.
func (s *Service) RequestPasswordReset(ctx context.Context, email, ip, userAgent string) error {
	email = strings.TrimSpace(email)
	u, err := s.repo.GetUserByEmail(ctx, email)
	if err != nil || u == nil || u.Status != "active" {
		return nil // generic response: no user enumeration
	}

	rawToken, err := tokens.GenerateOpaqueToken()
	if err != nil {
		return fmt.Errorf("generate reset token: %w", err)
	}
	tokenHash := tokens.HashToken(rawToken)
	expiry := time.Now().Add(30 * time.Minute)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := s.repo.CreatePasswordReset(ctx, tx, u.ID, tokenHash, expiry); err != nil {
		return fmt.Errorf("create password reset: %w", err)
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &u.ID,
		ActorOrgID:  &u.OrganizationID,
		TargetOrgID: &u.OrganizationID,
		Action:      "auth.password.reset_request",
		Module:      "auth",
		EntityType:  "user",
		EntityID:    u.ID.String(),
		Reason:      "Password reset requested",
	}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	_ = s.mailer.SendPasswordReset(ctx, email, rawToken)
	return nil
}

// ConfirmPasswordReset validates the token, hashes the new password with Argon2id,
// updates the user, and revokes all active sessions.
func (s *Service) ConfirmPasswordReset(ctx context.Context, rawToken, newPassword string) error {
	if err := password.ValidateStrength(newPassword); err != nil {
		return err
	}

	tokenHash := tokens.HashToken(rawToken)
	userID, err := s.repo.VerifyPasswordReset(ctx, tokenHash)
	if err != nil || userID == nil {
		return ErrTokenInvalid
	}

	u, err := s.repo.GetUserByID(ctx, *userID)
	if err != nil || u == nil {
		return ErrUserNotFound
	}

	pwdHash, err := password.Hash(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := s.repo.UpdatePassword(ctx, tx, u.ID, pwdHash); err != nil {
		return fmt.Errorf("update password: %w", err)
	}

	if err := s.repo.ConsumePasswordReset(ctx, tx, tokenHash); err != nil {
		return fmt.Errorf("consume token: %w", err)
	}

	if err := s.repo.RevokeAllUserSessions(ctx, tx, u.ID, "password_reset"); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &u.ID,
		ActorOrgID:  &u.OrganizationID,
		TargetOrgID: &u.OrganizationID,
		Action:      "auth.password.reset_confirm",
		Module:      "auth",
		EntityType:  "user",
		EntityID:    u.ID.String(),
		Reason:      "Password reset confirmed; all sessions revoked",
	}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	return tx.Commit(ctx)
}

// ChangePassword allows an authenticated user to change their password after verifying the current one.
func (s *Service) ChangePassword(ctx context.Context, tc *tenant.Context, currentPassword, newPassword string) error {
	if err := password.ValidateStrength(newPassword); err != nil {
		return err
	}

	u, err := s.repo.GetUserByID(ctx, tc.ActorUserID)
	if err != nil || u == nil {
		return ErrUserNotFound
	}

	match := false
	if u.PasswordHash != nil {
		match, _ = password.Verify(currentPassword, *u.PasswordHash)
	}
	if !match {
		return ErrCurrentPasswordInvalid
	}

	pwdHash, err := password.Hash(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := s.repo.UpdatePassword(ctx, tx, u.ID, pwdHash); err != nil {
		return fmt.Errorf("update password: %w", err)
	}

	// Revoke other sessions (except the current one)
	if err := s.repo.RevokeAllUserSessions(ctx, tx, u.ID, "password_change"); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		TargetOrgID: &tc.ActorOrgID,
		Action:      "auth.password.change",
		Module:      "auth",
		EntityType:  "user",
		EntityID:    u.ID.String(),
		Reason:      "Password changed by user",
	}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	return tx.Commit(ctx)
}

// InviteUser creates an invited user record and sends an invitation email.
func (s *Service) InviteUser(ctx context.Context, tc *tenant.Context, req InviteUserRequest) (*User, error) {
	email := strings.TrimSpace(req.Email)
	if email == "" || !strings.Contains(email, "@") {
		return nil, errors.New("valid email is required")
	}

	targetOrgID := tc.ActorOrgID
	if req.OrganizationID != nil {
		targetOrgID = *req.OrganizationID
	}

	targetOrg, err := s.orgRepo.GetByID(ctx, targetOrgID)
	if err != nil {
		return nil, org.ErrOrgNotFound
	}
	if !tc.CanAccessOrg(targetOrg.Path) {
		return nil, org.ErrOrgNotFound
	}

	// Prevent privilege escalation: caller cannot assign roles granting permissions caller doesn't possess
	if !tc.IsPlatformAdmin {
		for _, rid := range req.RoleIDs {
			rolePerms, err := s.rbacRepo.GetRolePermissions(ctx, rid)
			if err != nil {
				return nil, fmt.Errorf("check role permissions: %w", err)
			}
			for _, p := range rolePerms {
				if !tc.Permissions[p] {
					return nil, fmt.Errorf("%w: missing %s", ErrPrivilegeEscalation, p)
				}
			}
		}
	}

	// Check if email is already taken
	existing, _ := s.repo.GetUserByEmail(ctx, email)
	if existing != nil {
		return nil, ErrEmailAlreadyExists
	}

	rawToken, err := tokens.GenerateOpaqueToken()
	if err != nil {
		return nil, fmt.Errorf("generate invitation token: %w", err)
	}
	tokenHash := tokens.HashToken(rawToken)
	expiry := time.Now().Add(48 * time.Hour)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	newUser, err := s.repo.CreateUser(ctx, tx, User{
		OrganizationID: targetOrgID,
		Email:          email,
		Status:         "invited",
	}, req.RoleIDs)
	if err != nil {
		return nil, fmt.Errorf("create invited user: %w", err)
	}

	if err := s.repo.CreateInvitation(ctx, tx, newUser.ID, tokenHash, expiry); err != nil {
		return nil, fmt.Errorf("create invitation token: %w", err)
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		TargetOrgID: &targetOrgID,
		Action:      "user.account.invite",
		Module:      "user",
		EntityType:  "user",
		EntityID:    newUser.ID.String(),
		AfterValue:  audit.RedactValue(newUser),
		Reason:      fmt.Sprintf("Invited user %s into org %s", email, targetOrg.Name),
	}); err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	_ = s.mailer.SendUserInvitation(ctx, email, rawToken, targetOrg.Name)
	return newUser, nil
}

// AcceptInvitation consumes the invitation token, sets the password, and activates the user.
func (s *Service) AcceptInvitation(ctx context.Context, rawToken, plainPassword string) error {
	if err := password.ValidateStrength(plainPassword); err != nil {
		return err
	}

	tokenHash := tokens.HashToken(rawToken)
	userID, err := s.repo.VerifyInvitation(ctx, tokenHash)
	if err != nil || userID == nil {
		return ErrTokenInvalid
	}

	u, err := s.repo.GetUserByID(ctx, *userID)
	if err != nil || u == nil {
		return ErrUserNotFound
	}

	pwdHash, err := password.Hash(plainPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := s.repo.UpdatePassword(ctx, tx, u.ID, pwdHash); err != nil {
		return fmt.Errorf("set password: %w", err)
	}

	if err := s.repo.UpdateUser(ctx, tx, u.ID, "active", u.RoleIDs); err != nil {
		return fmt.Errorf("activate user: %w", err)
	}

	if err := s.repo.ConsumeInvitation(ctx, tx, tokenHash); err != nil {
		return fmt.Errorf("consume invitation: %w", err)
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &u.ID,
		ActorOrgID:  &u.OrganizationID,
		TargetOrgID: &u.OrganizationID,
		Action:      "user.account.accept_invite",
		Module:      "user",
		EntityType:  "user",
		EntityID:    u.ID.String(),
		Reason:      "User accepted invitation and set password",
	}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	return tx.Commit(ctx)
}

// ListUsers returns paginated users scoped to the caller's tenant context.
func (s *Service) ListUsers(ctx context.Context, tc *tenant.Context, orgID *uuid.UUID, search, status string, page, pageSize int) ([]User, int, error) {
	if orgID != nil {
		targetOrg, err := s.orgRepo.GetByID(ctx, *orgID)
		if err != nil || !tc.CanAccessOrg(targetOrg.Path) {
			return nil, 0, org.ErrOrgNotFound
		}
	}
	return s.repo.ListUsers(ctx, tc, orgID, search, status, page, pageSize)
}

// GetUser returns a user if accessible by tenant context.
func (s *Service) GetUser(ctx context.Context, tc *tenant.Context, id uuid.UUID) (*User, error) {
	u, err := s.repo.GetUserByID(ctx, id)
	if err != nil || u == nil {
		return nil, ErrUserNotFound
	}
	targetOrg, err := s.orgRepo.GetByID(ctx, u.OrganizationID)
	if err != nil || !tc.CanAccessOrg(targetOrg.Path) {
		return nil, ErrUserNotFound
	}
	return u, nil
}

// SuspendUser sets status to "suspended", revokes all active sessions immediately, and writes an audit log.
func (s *Service) SuspendUser(ctx context.Context, tc *tenant.Context, id uuid.UUID, reason string) error {
	if id == tc.ActorUserID {
		return ErrCannotSuspendSelf
	}

	u, err := s.GetUser(ctx, tc, id)
	if err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := s.repo.UpdateUser(ctx, tx, u.ID, "suspended", u.RoleIDs); err != nil {
		return fmt.Errorf("update user status: %w", err)
	}

	// Revoke sessions immediately
	if err := s.repo.RevokeAllUserSessions(ctx, tx, u.ID, "user_suspended"); err != nil {
		return fmt.Errorf("revoke user sessions: %w", err)
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		TargetOrgID: &u.OrganizationID,
		Action:      "user.account.suspend",
		Module:      "user",
		EntityType:  "user",
		EntityID:    u.ID.String(),
		BeforeValue: audit.RedactValue(u),
		AfterValue:  audit.RedactValue(map[string]any{"status": "suspended"}),
		Reason:      fmt.Sprintf("User suspended: %s", reason),
	}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	return tx.Commit(ctx)
}

// ActivateUser restores a user to "active" status and writes an audit log.
func (s *Service) ActivateUser(ctx context.Context, tc *tenant.Context, id uuid.UUID) error {
	u, err := s.GetUser(ctx, tc, id)
	if err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := s.repo.UpdateUser(ctx, tx, u.ID, "active", u.RoleIDs); err != nil {
		return fmt.Errorf("activate user: %w", err)
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		TargetOrgID: &u.OrganizationID,
		Action:      "user.account.activate",
		Module:      "user",
		EntityType:  "user",
		EntityID:    u.ID.String(),
		BeforeValue: audit.RedactValue(u),
		AfterValue:  audit.RedactValue(map[string]any{"status": "active"}),
		Reason:      "User activated",
	}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	return tx.Commit(ctx)
}

// UpdateUserRoles assigns roles to a user, checking for privilege escalation.
func (s *Service) UpdateUserRoles(ctx context.Context, tc *tenant.Context, id uuid.UUID, roleIDs []uuid.UUID) error {
	u, err := s.GetUser(ctx, tc, id)
	if err != nil {
		return err
	}

	// Prevent privilege escalation
	if !tc.IsPlatformAdmin {
		for _, rid := range roleIDs {
			rolePerms, err := s.rbacRepo.GetRolePermissions(ctx, rid)
			if err != nil {
				return fmt.Errorf("check role permissions: %w", err)
			}
			for _, p := range rolePerms {
				if !tc.Permissions[p] {
					return fmt.Errorf("%w: missing %s", ErrPrivilegeEscalation, p)
				}
			}
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := s.repo.UpdateUser(ctx, tx, u.ID, u.Status, roleIDs); err != nil {
		return fmt.Errorf("update user roles: %w", err)
	}

	if err := s.auditSvc.Record(ctx, tx, audit.Entry{
		ActorUserID: &tc.ActorUserID,
		ActorOrgID:  &tc.ActorOrgID,
		TargetOrgID: &u.OrganizationID,
		Action:      "user.account.edit_roles",
		Module:      "user",
		EntityType:  "user",
		EntityID:    u.ID.String(),
		BeforeValue: audit.RedactValue(map[string]any{"role_ids": u.RoleIDs}),
		AfterValue:  audit.RedactValue(map[string]any{"role_ids": roleIDs}),
		Reason:      "Updated user assigned roles",
	}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	return tx.Commit(ctx)
}

// ListLoginHistory returns paginated login history items scoped to the caller's tenant.
func (s *Service) ListLoginHistory(ctx context.Context, tc *tenant.Context, userID *uuid.UUID, page, pageSize int) ([]LoginHistoryItem, int, error) {
	if userID != nil {
		targetUser, err := s.GetUser(ctx, tc, *userID)
		if err != nil {
			return nil, 0, err
		}
		return s.repo.ListLoginHistory(ctx, tc, &targetUser.ID, page, pageSize)
	}
	return s.repo.ListLoginHistory(ctx, tc, nil, page, pageSize)
}

func (s *Service) createSessionAndTokens(
	ctx context.Context,
	u *User,
	orgRecord *org.Organization,
	isPlatformAdmin bool,
	ip, userAgent string,
) (*LoginResponse, string, error) {
	rawRefresh, err := tokens.GenerateOpaqueToken()
	if err != nil {
		return nil, "", fmt.Errorf("generate refresh: %w", err)
	}
	refreshHash := tokens.HashToken(rawRefresh)

	sessionID := uuid.New()
	familyID := uuid.New()
	expiresAt := time.Now().Add(7 * 24 * time.Hour)

	if err := s.repo.CreateSession(ctx, nil, Session{
		ID:               sessionID,
		UserID:           u.ID,
		OrganizationID:   orgRecord.ID,
		RefreshTokenHash: refreshHash,
		FamilyID:         familyID,
		IPAddress:        ip,
		UserAgent:        userAgent,
		ExpiresAt:        expiresAt,
	}); err != nil {
		return nil, "", fmt.Errorf("create session: %w", err)
	}

	perms, err := s.rbacRepo.GetUserPermissions(ctx, u.ID)
	if err != nil {
		return nil, "", fmt.Errorf("get user permissions: %w", err)
	}

	accessJWT, err := s.tokenMgr.GenerateAccessToken(
		u.ID,
		orgRecord.ID,
		sessionID,
		orgRecord.Type,
		orgRecord.Path,
		isPlatformAdmin,
		perms,
	)
	if err != nil {
		return nil, "", fmt.Errorf("generate access token: %w", err)
	}

	return &LoginResponse{
		AccessToken: accessJWT,
		ExpiresIn:   900,
		User:        u,
	}, rawRefresh, nil
}

func containsRole(roles []string, name string) bool {
	for _, r := range roles {
		if r == name {
			return true
		}
	}
	return false
}
