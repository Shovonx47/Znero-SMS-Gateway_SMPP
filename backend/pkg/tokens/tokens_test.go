package tokens_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/znero-international/znero/backend/pkg/tokens"
)

func TestTokenManager(t *testing.T) {
	secret := "a-very-long-secret-key-that-is-at-least-32-bytes-long"
	tm, err := tokens.NewTokenManager(secret, 15*time.Minute)
	if err != nil {
		t.Fatalf("NewTokenManager failed: %v", err)
	}

	userID := uuid.New()
	orgID := uuid.New()
	sessionID := uuid.New()
	perms := []string{"org.organization.view", "user.account.create"}

	signed, err := tm.GenerateAccessToken(userID, orgID, sessionID, "reseller", "/platform_id/reseller_id/", false, perms)
	if err != nil {
		t.Fatalf("GenerateAccessToken failed: %v", err)
	}

	claims, err := tm.ValidateAccessToken(signed)
	if err != nil {
		t.Fatalf("ValidateAccessToken failed: %v", err)
	}

	if claims.UserID != userID || claims.OrgID != orgID || claims.SessionID != sessionID {
		t.Fatalf("Claims mismatch: got %+v", claims)
	}
	if len(claims.Permissions) != 2 || claims.Permissions[0] != perms[0] {
		t.Fatalf("Permissions mismatch: got %+v", claims.Permissions)
	}
}

func TestOpaqueTokens(t *testing.T) {
	tok1, err := tokens.GenerateOpaqueToken()
	if err != nil {
		t.Fatalf("GenerateOpaqueToken failed: %v", err)
	}
	tok2, _ := tokens.GenerateOpaqueToken()
	if tok1 == tok2 || len(tok1) != 64 {
		t.Fatalf("Invalid opaque tokens: %q vs %q", tok1, tok2)
	}

	h1 := tokens.HashToken(tok1)
	h2 := tokens.HashToken(tok1)
	if h1 != h2 || len(h1) != 64 {
		t.Fatalf("HashToken must be deterministic 64-char hex")
	}
}
