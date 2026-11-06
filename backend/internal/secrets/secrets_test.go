package secrets_test

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/znero-international/znero/backend/internal/secrets"
)

func randomKey(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, secrets.KeySize)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEncryptDecryptRoundTripAndRandomNonce(t *testing.T) {
	t.Parallel()

	key := randomKey(t)
	kr, err := secrets.NewKeyRing("v1", key)
	if err != nil {
		t.Fatalf("NewKeyRing: %v", err)
	}

	rawSecret := "smpp-upstream-secret-" + base64.RawURLEncoding.EncodeToString(randomKey(t)[:12])
	enc1, err := kr.Encrypt(rawSecret)
	if err != nil {
		t.Fatalf("Encrypt 1: %v", err)
	}
	enc2, err := kr.Encrypt(rawSecret)
	if err != nil {
		t.Fatalf("Encrypt 2: %v", err)
	}
	if enc1 == enc2 {
		t.Fatal("encrypting the same secret twice must produce distinct ciphertexts (random nonce per value)")
	}
	if strings.Contains(enc1, rawSecret) {
		t.Fatal("ciphertext must not contain plaintext")
	}
	if secrets.ExtractKeyID(enc1) != "v1" {
		t.Fatalf("expected key ID v1, got %q", secrets.ExtractKeyID(enc1))
	}

	dec1, err := kr.Decrypt(enc1)
	if err != nil || dec1 != rawSecret {
		t.Fatalf("Decrypt 1 mismatch: got %q, err %v", dec1, err)
	}
	dec2, err := kr.Decrypt(enc2)
	if err != nil || dec2 != rawSecret {
		t.Fatalf("Decrypt 2 mismatch: got %q, err %v", dec2, err)
	}
}

func TestTamperDetectionAndWrongKey(t *testing.T) {
	t.Parallel()

	kr1, _ := secrets.NewKeyRing("v1", randomKey(t))
	kr2, _ := secrets.NewKeyRing("v1", randomKey(t))

	rawSecret := "secret-" + base64.RawURLEncoding.EncodeToString(randomKey(t)[:12])
	enc, err := kr1.Encrypt(rawSecret)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Wrong key with same key ID fails authentication
	if _, err := kr2.Decrypt(enc); !errors.Is(err, secrets.ErrDecryptionFailed) {
		t.Fatalf("wrong key: expected ErrDecryptionFailed, got %v", err)
	}

	// 2. Flip a byte inside the base64 payload
	parts := strings.SplitN(enc, ":", 2)
	rawBytes, _ := base64.StdEncoding.DecodeString(parts[1])
	rawBytes[len(rawBytes)-3] ^= 0xff
	tampered := parts[0] + ":" + base64.StdEncoding.EncodeToString(rawBytes)
	if _, err := kr1.Decrypt(tampered); !errors.Is(err, secrets.ErrDecryptionFailed) {
		t.Fatalf("tampered payload: expected ErrDecryptionFailed, got %v", err)
	}

	// 3. Tamper with key version prefix (AAD mismatch)
	key2 := randomKey(t)
	_ = kr1.Rotate("v2", key2)
	// Swap prefix of a v2 ciphertext to v1
	encV2, _ := kr1.Encrypt(rawSecret)
	swappedAAD := "v1:" + strings.SplitN(encV2, ":", 2)[1]
	if _, err := kr1.Decrypt(swappedAAD); !errors.Is(err, secrets.ErrDecryptionFailed) {
		t.Fatalf("swapped key ID AAD: expected ErrDecryptionFailed, got %v", err)
	}
}

func TestKeyRotationKeepsHistoricalCiphertextsReadable(t *testing.T) {
	t.Parallel()

	k1 := randomKey(t)
	k2 := randomKey(t)
	kr, err := secrets.NewKeyRingFromEnv(
		"production",
		base64.StdEncoding.EncodeToString(k2),
		"v2",
		"v1:"+base64.StdEncoding.EncodeToString(k1),
	)
	if err != nil {
		t.Fatalf("NewKeyRingFromEnv with previous key: %v", err)
	}

	krV1Only, _ := secrets.NewKeyRing("v1", k1)
	oldCipher, err := krV1Only.Encrypt("legacy-secret")
	if err != nil {
		t.Fatal(err)
	}

	// Rotated keyring decrypts v1 and encrypts new secrets with v2
	decOld, err := kr.Decrypt(oldCipher)
	if err != nil || decOld != "legacy-secret" {
		t.Fatalf("failed to decrypt rotated v1 ciphertext: %v", err)
	}
	newCipher, err := kr.Encrypt("fresh-secret")
	if err != nil {
		t.Fatal(err)
	}
	if secrets.ExtractKeyID(newCipher) != "v2" {
		t.Fatalf("expected new encryption to use v2, got %q", secrets.ExtractKeyID(newCipher))
	}
}

func TestStartupRefusalOutsideDevelopment(t *testing.T) {
	t.Parallel()

	// 1. Missing key in production -> refused
	if _, err := secrets.NewKeyRingFromEnv("production", "", "v1", ""); !errors.Is(err, secrets.ErrMissingKey) {
		t.Fatalf("missing key in production: expected ErrMissingKey, got %v", err)
	}
	if _, err := secrets.NewKeyRingFromEnv("staging", "", "v1", ""); !errors.Is(err, secrets.ErrMissingKey) {
		t.Fatalf("missing key in staging: expected ErrMissingKey, got %v", err)
	}

	// 2. Known dev fallback key in production -> refused
	if _, err := secrets.NewKeyRingFromEnv("production", secrets.DevFallbackKeyBase64, "v1", ""); !errors.Is(err, secrets.ErrInsecureDevKey) {
		t.Fatalf("dev fallback key in production: expected ErrInsecureDevKey, got %v", err)
	}

	// 3. All-zeros key in production -> refused
	zeroKeyB64 := base64.StdEncoding.EncodeToString(make([]byte, 32))
	if _, err := secrets.NewKeyRingFromEnv("production", zeroKeyB64, "v1", ""); !errors.Is(err, secrets.ErrInsecureDevKey) {
		t.Fatalf("all-zero key in production: expected ErrInsecureDevKey, got %v", err)
	}

	// 4. In development, missing key uses dev fallback cleanly
	if kr, err := secrets.NewKeyRingFromEnv("development", "", "v1", ""); err != nil || kr == nil {
		t.Fatalf("development fallback should succeed, got %v", err)
	}
}

func TestWriteOnlySecretNeverLeaksPlaintextOrCiphertext(t *testing.T) {
	t.Parallel()

	kr, _ := secrets.NewKeyRing("v1", randomKey(t))
	plaintext := "rt-secret-" + base64.RawURLEncoding.EncodeToString(randomKey(t)[:16])
	ciphertext, err := kr.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	status := secrets.NewSecretStatus(ciphertext, &now)
	statusBytes, _ := json.Marshal(status)
	auditBytes := secrets.AuditSecretChangeMarker("password", now)

	for label, payload := range map[string]string{
		"api_response_and_snapshot": string(statusBytes),
		"audit_entry":               string(auditBytes),
	} {
		if strings.Contains(payload, plaintext) {
			t.Fatalf("%s leaked plaintext secret: %s", label, payload)
		}
		if strings.Contains(payload, ciphertext) {
			t.Fatalf("%s leaked encrypted ciphertext: %s", label, payload)
		}
	}
	if !status.Set || status.LastChanged == nil {
		t.Fatalf("expected status.Set=true and non-nil LastChanged, got %+v", status)
	}
	if !strings.Contains(string(auditBytes), `"secret changed"`) {
		t.Fatalf("expected audit marker to record 'secret changed', got %s", string(auditBytes))
	}
}

func TestCustomerAPIKeyGenerationAndConstantTimeVerification(t *testing.T) {
	t.Parallel()

	gen, err := secrets.GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	if !strings.HasPrefix(gen.PlaintextKey, "znk_live_") {
		t.Fatalf("unexpected key format: %q", gen.PlaintextKey)
	}
	if !strings.HasPrefix(gen.PlaintextKey, gen.Prefix) || len(gen.Prefix) >= len(gen.PlaintextKey) {
		t.Fatalf("prefix %q should be a short prefix of %q", gen.Prefix, gen.PlaintextKey)
	}
	if strings.Contains(gen.KeyHash, gen.PlaintextKey) {
		t.Fatal("key hash must not contain plaintext key")
	}
	if !secrets.VerifyAPIKey(gen.PlaintextKey, gen.KeyHash) {
		t.Fatal("VerifyAPIKey failed for valid key")
	}
	if secrets.VerifyAPIKey(gen.PlaintextKey+"x", gen.KeyHash) {
		t.Fatal("VerifyAPIKey succeeded for wrong key")
	}
}
