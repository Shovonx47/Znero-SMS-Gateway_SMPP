package password_test

import (
	"testing"

	"github.com/znero-international/znero/backend/pkg/crypto/password"
)

func TestValidateStrength(t *testing.T) {
	tests := []struct {
		name    string
		pwd     string
		wantErr bool
	}{
		{"too short", "short123", true},
		{"exact 11 chars", "abcdefghijk", true},
		{"common password", "password12345", true},
		{"valid strong", "SuperSecure#2026!Platform", false},
		{"valid 12 chars", "xK9#mQ2$vL8@", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := password.ValidateStrength(tt.pwd)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateStrength(%q) err = %v, wantErr %v", tt.pwd, err, tt.wantErr)
			}
		})
	}
}

func TestHashAndVerify(t *testing.T) {
	pwd := "My$ecretP@ssw0rd123"
	hash, err := password.Hash(pwd)
	if err != nil {
		t.Fatalf("Hash() unexpected err: %v", err)
	}

	ok, err := password.Verify(pwd, hash)
	if err != nil || !ok {
		t.Fatalf("Verify() expected ok=true, got ok=%v, err=%v", ok, err)
	}

	wrong, err := password.Verify("WrongPassword999#", hash)
	if err != nil || wrong {
		t.Fatalf("Verify() expected false for wrong password, got %v, err=%v", wrong, err)
	}

	// Human Hash still strictly rejects <12 char passwords, while HashRaw handles 8-char SMPP wire secrets.
	if _, err := password.Hash("Ab3dEf7H"); err == nil {
		t.Fatal("expected password.Hash to reject 8-char human password")
	}
	rawHash, err := password.HashRaw("Ab3dEf7H")
	if err != nil {
		t.Fatalf("HashRaw unexpected err: %v", err)
	}
	if ok, err := password.Verify("Ab3dEf7H", rawHash); err != nil || !ok {
		t.Fatalf("Verify(HashRaw) failed: ok=%v err=%v", ok, err)
	}
}
