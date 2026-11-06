package encrypt_test

import (
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/znero-international/znero/backend/pkg/crypto/encrypt"
)

func TestEncryptDecryptAESGCM(t *testing.T) {
	keyBytes := make([]byte, 32)
	_, _ = rand.Read(keyBytes)
	keyHex := hex.EncodeToString(keyBytes)

	original := "JBSWY3DPEHPK3PXP" // Example base32 secret
	encrypted, err := encrypt.EncryptAESGCM(original, keyHex)
	if err != nil {
		t.Fatalf("EncryptAESGCM failed: %v", err)
	}

	if encrypted == original {
		t.Fatal("Encrypted text must not match plaintext")
	}

	decrypted, err := encrypt.DecryptAESGCM(encrypted, keyHex)
	if err != nil {
		t.Fatalf("DecryptAESGCM failed: %v", err)
	}

	if decrypted != original {
		t.Fatalf("Decrypted %q != original %q", decrypted, original)
	}
}

func TestInvalidKey(t *testing.T) {
	_, err := encrypt.EncryptAESGCM("secret", "shortkey")
	if err == nil {
		t.Fatal("Expected error with short key")
	}
}
