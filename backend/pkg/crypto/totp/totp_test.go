package totp_test

import (
	"strings"
	"testing"
	"time"

	"github.com/znero-international/znero/backend/pkg/crypto/totp"
)

func TestTOTPGenerateAndValidate(t *testing.T) {
	secret, err := totp.GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret failed: %v", err)
	}

	now := time.Now()
	code, err := totp.GenerateCode(secret, now)
	if err != nil {
		t.Fatalf("GenerateCode failed: %v", err)
	}

	if len(code) != 6 {
		t.Fatalf("Code must be 6 digits, got %q", code)
	}

	// Immediate validation should succeed
	if !totp.ValidateCode(secret, code, now) {
		t.Fatalf("ValidateCode failed for current time")
	}

	// Validation within 25s window should succeed
	if !totp.ValidateCode(secret, code, now.Add(25*time.Second)) {
		t.Fatalf("ValidateCode failed within allowable window")
	}

	// Invalid code should fail
	if totp.ValidateCode(secret, "000000", now) && code != "000000" {
		t.Fatalf("ValidateCode succeeded for invalid code")
	}

	// Far future timestamp should fail
	if totp.ValidateCode(secret, code, now.Add(2*time.Minute)) {
		t.Fatalf("ValidateCode succeeded outside allowable window")
	}
}

func TestURIFormat(t *testing.T) {
	uri := totp.GenerateURI("Znero", "admin@znero.io", "JBSWY3DPEHPK3PXP")
	if !strings.HasPrefix(uri, "otpauth://totp/Znero:admin@znero.io?") {
		t.Fatalf("Unexpected URI format: %s", uri)
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes, err := totp.GenerateRecoveryCodes(10)
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes failed: %v", err)
	}
	if len(codes) != 10 {
		t.Fatalf("Expected 10 recovery codes, got %d", len(codes))
	}
	for _, c := range codes {
		if len(c.Code) != 10 || len(c.CodeHash) != 64 {
			t.Fatalf("Invalid recovery code or hash length: %+v", c)
		}
		if totp.HashRecoveryCode(c.Code) != c.CodeHash {
			t.Fatalf("HashRecoveryCode does not match expected hash")
		}
	}
}
