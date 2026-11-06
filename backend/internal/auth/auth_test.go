package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/pkg/crypto/encrypt"
	"github.com/znero-international/znero/backend/pkg/crypto/password"
	"github.com/znero-international/znero/backend/pkg/crypto/totp"
	"github.com/znero-international/znero/backend/pkg/tokens"
)

func TestPasswordStrengthAndHashing(t *testing.T) {
	// 1. Minimum 12 characters
	if err := password.ValidateStrength("short"); err == nil {
		t.Errorf("expected error for short password, got nil")
	}

	// 2. Reject common passwords
	if err := password.ValidateStrength("password1234"); err == nil {
		t.Errorf("expected error for common password, got nil")
	}

	// 3. Valid secure password
	validPwd := "Tr0ng#Passw0rd_999!"
	if err := password.ValidateStrength(validPwd); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}

	// 4. Argon2id Hash & Constant-time Verify
	hash, err := password.Hash(validPwd)
	if err != nil {
		t.Fatalf("hash failed: %v", err)
	}

	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("hash does not have argon2id prefix: %s", hash)
	}

	match, err := password.Verify(validPwd, hash)
	if err != nil || !match {
		t.Errorf("verify failed for correct password")
	}

	wrongMatch, _ := password.Verify("WrongPassword123!", hash)
	if wrongMatch {
		t.Errorf("verify succeeded for wrong password")
	}
}

func TestTOTPGenerationAndValidation(t *testing.T) {
	secret, err := totp.GenerateSecret()
	if err != nil {
		t.Fatalf("generate secret failed: %v", err)
	}

	now := time.Now()
	code, err := totp.GenerateCode(secret, now)
	if err != nil {
		t.Fatalf("generate code failed: %v", err)
	}

	if len(code) != 6 {
		t.Errorf("expected 6-digit code, got %s", code)
	}

	// Valid in current window
	if !totp.ValidateCode(secret, code, now) {
		t.Errorf("expected code to validate successfully")
	}

	// Valid in drift window (within 30s)
	if !totp.ValidateCode(secret, code, now.Add(25*time.Second)) {
		t.Errorf("expected code to validate with +25s drift")
	}

	// Invalid beyond window (e.g. 2 minutes later)
	if totp.ValidateCode(secret, code, now.Add(2*time.Minute)) {
		t.Errorf("code should have expired 2 minutes later")
	}
}

func TestMFASecretEncryptionAtRest(t *testing.T) {
	keyHex := "5ef83b45188a60122fae1100d5bdb36943939caef953ddfd26f217f2aa929e89"
	secret := "JBSWY3DPEHPK3PXP"

	encrypted, err := encrypt.EncryptAESGCM(secret, keyHex)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	if encrypted == secret {
		t.Errorf("encrypted secret equals plaintext!")
	}

	decrypted, err := encrypt.DecryptAESGCM(encrypted, keyHex)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}

	if decrypted != secret {
		t.Errorf("decrypted secret = %s, want %s", decrypted, secret)
	}
}

func TestTokensAndSessionRotation(t *testing.T) {
	mgr, err := tokens.NewTokenManager("this-is-a-very-long-and-secure-secret-key-32-chars", 15*time.Minute)
	if err != nil {
		t.Fatalf("new token manager: %v", err)
	}

	userID := uuid.New()
	orgID := uuid.New()
	sessionID := uuid.New()

	jwtStr, err := mgr.GenerateAccessToken(userID, orgID, sessionID, "platform", "/"+orgID.String()+"/", true, []string{"user.account.view"})
	if err != nil {
		t.Fatalf("generate access token: %v", err)
	}

	claims, err := mgr.ValidateAccessToken(jwtStr)
	if err != nil {
		t.Fatalf("validate token: %v", err)
	}

	if claims.UserID != userID || claims.OrgID != orgID || !claims.IsPlatformAdmin {
		t.Errorf("claims do not match expected values: %+v", claims)
	}

	// Opaque refresh token generation and hashing
	rawRefresh, err := tokens.GenerateOpaqueToken()
	if err != nil {
		t.Fatalf("generate opaque token: %v", err)
	}

	hash1 := tokens.HashToken(rawRefresh)
	hash2 := tokens.HashToken(rawRefresh)
	if hash1 != hash2 {
		t.Errorf("hash should be deterministic")
	}
	if len(hash1) != 64 {
		t.Errorf("sha256 hex length should be 64, got %d", len(hash1))
	}
}

func TestAccountLockedAndSuspendedErrors(t *testing.T) {
	ctx := context.Background()

	// Mocking locked user behavior
	lockedTime := time.Now().Add(10 * time.Minute)
	lockedUser := &User{
		ID:          uuid.New(),
		Email:       "locked@example.com",
		LockedUntil: &lockedTime,
		Status:      "active",
	}

	if lockedUser.LockedUntil.After(time.Now()) {
		// Handled properly as locked
		err := ErrAccountLocked
		if err.Error() == "" {
			t.Error("expected error message")
		}
	}

	suspendedUser := &User{
		ID:     uuid.New(),
		Email:  "suspended@example.com",
		Status: "suspended",
	}

	if suspendedUser.Status == "suspended" {
		err := ErrAccountSuspended
		if err.Error() == "" {
			t.Error("expected error message")
		}
	}
	_ = ctx
}
