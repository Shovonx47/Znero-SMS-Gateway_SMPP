// Package secrets provides AES-256-GCM envelope encryption with versioned key rotation,
// write-only secret metadata helpers (ensuring neither plaintext nor ciphertext ever
// appears in API responses, version snapshots, or audit logs), and cryptographically
// random customer API key generation.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const (
	DefaultKeyID = "v1"
	KeySize      = 32 // AES-256 requires 32 bytes
	apiKeyPrefix = "znk_live_"
)

var (
	ErrMissingKey       = errors.New("secrets encryption key is required")
	ErrInvalidKeyLength = errors.New("secrets encryption key must decode to exactly 32 bytes (256 bits)")
	ErrInsecureDevKey   = errors.New("refusing to use known development encryption key outside development environment")
	ErrMalformedCipher  = errors.New("malformed encrypted secret envelope")
	ErrUnknownKeyID     = errors.New("unknown encryption key version ID")
	ErrDecryptionFailed = errors.New("secret authentication or decryption failed (tampered or wrong key)")
	ErrEmptySecretValue = errors.New("secret value cannot be empty")
)

// knownDevKeys lists weak or sample keys that must always be rejected outside APP_ENV=development.
var knownDevKeys = map[string]bool{
	"0000000000000000000000000000000000000000000000000000000000000000": true,
	"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20": true,
	"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": true,
	"6465762d736563726574732d6b65792d33322d62797465732d6f6e6c79212121": true, // "dev-secrets-key-32-bytes-only!!!"
}

// DevFallbackKeyBase64 is used ONLY when APP_ENV=development and SECRETS_ENCRYPTION_KEY is unset.
// It is registered in knownDevKeys so non-development environments strictly reject it.
var DevFallbackKeyBase64 = base64.StdEncoding.EncodeToString([]byte("dev-secrets-key-32-bytes-only!!!"))

// KeyRing holds the active encryption key and optional historical keys for seamless key rotation.
type KeyRing struct {
	mu          sync.RWMutex
	activeKeyID string
	keys        map[string][]byte
}

// NewKeyRingFromEnv initializes a KeyRing from environment configuration.
// In non-development environments, it strictly refuses to start if rawKey is empty or a known dev key.
func NewKeyRingFromEnv(appEnv, rawKey, activeKeyID, previousKeys string) (*KeyRing, error) {
	isDev := strings.EqualFold(strings.TrimSpace(appEnv), "development")
	rawKey = strings.TrimSpace(rawKey)
	if activeKeyID = strings.TrimSpace(activeKeyID); activeKeyID == "" {
		activeKeyID = DefaultKeyID
	}

	if rawKey == "" {
		if !isDev {
			return nil, ErrMissingKey
		}
		rawKey = DevFallbackKeyBase64
	}

	keyBytes, err := DecodeKey(rawKey)
	if err != nil {
		return nil, err
	}

	if !isDev && isWeakOrKnownDevKey(keyBytes) {
		return nil, ErrInsecureDevKey
	}

	kr := &KeyRing{
		activeKeyID: activeKeyID,
		keys: map[string][]byte{
			activeKeyID: keyBytes,
		},
	}

	// Optional comma-separated list of rotated keys: "v0:<base64>,v1:<base64>"
	if strings.TrimSpace(previousKeys) != "" {
		for _, pair := range strings.Split(previousKeys, ",") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			parts := strings.SplitN(pair, ":", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid previous key entry %q: expected <key_id>:<base64_or_hex>", pair)
			}
			kID := strings.TrimSpace(parts[0])
			kBytes, err := DecodeKey(strings.TrimSpace(parts[1]))
			if err != nil {
				return nil, fmt.Errorf("decode previous key %q: %w", kID, err)
			}
			kr.keys[kID] = kBytes
		}
	}

	return kr, nil
}

// NewKeyRing constructs a KeyRing directly from a 32-byte key and version ID.
func NewKeyRing(keyID string, key []byte) (*KeyRing, error) {
	if len(key) != KeySize {
		return nil, ErrInvalidKeyLength
	}
	if keyID = strings.TrimSpace(keyID); keyID == "" {
		keyID = DefaultKeyID
	}
	cp := make([]byte, KeySize)
	copy(cp, key)
	return &KeyRing{
		activeKeyID: keyID,
		keys:        map[string][]byte{keyID: cp},
	}, nil
}

// Rotate adds a new 32-byte key and promotes it to the active encryption key while keeping
// previous keys in the ring so existing ciphertexts still decrypt.
func (kr *KeyRing) Rotate(newKeyID string, newKey []byte) error {
	if len(newKey) != KeySize {
		return ErrInvalidKeyLength
	}
	newKeyID = strings.TrimSpace(newKeyID)
	if newKeyID == "" {
		return errors.New("key ID cannot be empty")
	}
	cp := make([]byte, KeySize)
	copy(cp, newKey)

	kr.mu.Lock()
	defer kr.mu.Unlock()
	kr.keys[newKeyID] = cp
	kr.activeKeyID = newKeyID
	return nil
}

// ActiveKeyID returns the currently active key version identifier.
func (kr *KeyRing) ActiveKeyID() string {
	kr.mu.RLock()
	defer kr.mu.RUnlock()
	return kr.activeKeyID
}

// DecodeKey accepts a 32-byte key encoded as standard base64, raw base64, URL base64, or 64-char hex.
func DecodeKey(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, ErrMissingKey
	}
	for _, dec := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if b, err := dec.DecodeString(encoded); err == nil && len(b) == KeySize {
			return b, nil
		}
	}
	if len(encoded) == 64 {
		if b, err := hex.DecodeString(encoded); err == nil && len(b) == KeySize {
			return b, nil
		}
	}
	return nil, ErrInvalidKeyLength
}

func isWeakOrKnownDevKey(key []byte) bool {
	if len(key) != KeySize {
		return true
	}
	hexStr := strings.ToLower(hex.EncodeToString(key))
	if knownDevKeys[hexStr] {
		return true
	}
	// Reject keys where all 32 bytes are identical
	allSame := true
	for i := 1; i < len(key); i++ {
		if key[i] != key[0] {
			allSame = false
			break
		}
	}
	return allSame
}

// Encrypt encrypts plaintext with AES-256-GCM using a fresh 12-byte random nonce and returns
// a versioned envelope string "<key_id>:<base64(nonce || ciphertext || tag)>".
// The key ID is bound as Additional Authenticated Data (AAD) so tampering with the version prefix fails GCM auth.
func (kr *KeyRing) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", ErrEmptySecretValue
	}

	kr.mu.RLock()
	keyID := kr.activeKeyID
	key := kr.keys[keyID]
	kr.mu.RUnlock()

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), []byte(keyID))
	return keyID + ":" + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt parses a "<key_id>:<base64(nonce || ciphertext || tag)>" envelope, looks up key_id in the
// KeyRing, and authenticates + decrypts the payload.
func (kr *KeyRing) Decrypt(envelope string) (string, error) {
	parts := strings.SplitN(strings.TrimSpace(envelope), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ErrMalformedCipher
	}
	keyID := parts[0]

	kr.mu.RLock()
	key, ok := kr.keys[keyID]
	kr.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownKeyID, keyID)
	}

	raw, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ErrMalformedCipher
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(raw) < nonceSize+gcm.Overhead() {
		return "", ErrMalformedCipher
	}

	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte(keyID))
	if err != nil {
		return "", ErrDecryptionFailed
	}
	return string(plaintext), nil
}

// ExtractKeyID returns the key version ID from a stored envelope without decrypting it.
func ExtractKeyID(envelope string) string {
	parts := strings.SplitN(strings.TrimSpace(envelope), ":", 2)
	if len(parts) == 2 {
		return parts[0]
	}
	return ""
}

// SecretStatus is the safe, write-only metadata representation returned by APIs and stored in
// version snapshots. It NEVER contains the plaintext or encrypted secret.
type SecretStatus struct {
	Set         bool       `json:"set"`
	LastChanged *time.Time `json:"last_changed,omitempty"`
}

// NewSecretStatus builds a SecretStatus from whether an encrypted value is present and its change timestamp.
func NewSecretStatus(encrypted string, changedAt *time.Time) SecretStatus {
	if strings.TrimSpace(encrypted) == "" {
		return SecretStatus{Set: false}
	}
	return SecretStatus{
		Set:         true,
		LastChanged: changedAt,
	}
}

// AuditSecretChangeMarker returns a JSON-safe marker for audit logs when a secret is created or rotated.
func AuditSecretChangeMarker(field string, changedAt time.Time) json.RawMessage {
	payload := map[string]any{
		field:          "secret changed",
		"set":          true,
		"last_changed": changedAt.UTC().Format(time.RFC3339),
	}
	b, _ := json.Marshal(payload)
	return b
}

// GeneratedAPIKey holds a newly generated customer API key. PlaintextKey is returned to the caller
// exactly once and is never persisted; only Prefix and KeyHash are stored in the database.
type GeneratedAPIKey struct {
	PlaintextKey string
	Prefix       string
	KeyHash      string
}

// GenerateAPIKey creates a high-entropy API key ("znk_live_<48 hex chars>"), extracts its
// identification prefix ("znk_live_<first 8 hex chars>"), and computes its SHA-256 hex digest.
func GenerateAPIKey() (GeneratedAPIKey, error) {
	randomBytes := make([]byte, 24) // 192 bits of entropy
	if _, err := io.ReadFull(rand.Reader, randomBytes); err != nil {
		return GeneratedAPIKey{}, fmt.Errorf("generate api key entropy: %w", err)
	}
	randHex := hex.EncodeToString(randomBytes)
	fullKey := apiKeyPrefix + randHex
	prefix := apiKeyPrefix + randHex[:8]
	hash := HashAPIKey(fullKey)
	return GeneratedAPIKey{
		PlaintextKey: fullKey,
		Prefix:       prefix,
		KeyHash:      hash,
	}, nil
}

// HashAPIKey computes the SHA-256 hex digest of an API key.
func HashAPIKey(plaintextKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(plaintextKey)))
	return hex.EncodeToString(sum[:])
}

// VerifyAPIKey compares a candidate plaintext API key against a stored hex SHA-256 hash in constant time.
func VerifyAPIKey(plaintextKey, expectedHashHex string) bool {
	if strings.TrimSpace(plaintextKey) == "" || strings.TrimSpace(expectedHashHex) == "" {
		return false
	}
	actual := HashAPIKey(plaintextKey)
	return subtle.ConstantTimeCompare([]byte(actual), []byte(strings.ToLower(strings.TrimSpace(expectedHashHex)))) == 1
}

// GenerateRandomPassword creates a high-entropy URL-safe password (for webhook signing
// secrets and general credentials stored encrypted or hashed).
func GenerateRandomPassword(byteLen int) (string, error) {
	if byteLen < 16 {
		byteLen = 18
	}
	b := make([]byte, byteLen)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

const smppPasswordAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// GenerateSMPPPassword creates a cryptographically random 8-character alphanumeric
// password compliant with SMPP v3.4 section 4.1.1 (C-Octet String max 9 octets incl. NUL).
func GenerateSMPPPassword() (string, error) {
	const pwLen = 8
	const maxValid = 248 // 256 - (256 % 62), unbiased rejection sampling threshold
	out := make([]byte, 0, pwLen)
	buf := make([]byte, 16)
	for len(out) < pwLen {
		if _, err := io.ReadFull(rand.Reader, buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) >= maxValid {
				continue
			}
			out = append(out, smppPasswordAlphabet[int(b)%len(smppPasswordAlphabet)])
			if len(out) == pwLen {
				break
			}
		}
	}
	return string(out), nil
}
