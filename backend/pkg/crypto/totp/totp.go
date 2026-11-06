package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	PeriodSeconds = 30
	Digits        = 6
	Modulus       = 1000000
)

// GenerateSecret creates a 20-byte cryptographically secure random base32 encoded secret.
func GenerateSecret() (string, error) {
	bytes := make([]byte, 20)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(bytes), nil
}

// GenerateURI builds the standard otpauth URI used by Google Authenticator, Authy, etc.
func GenerateURI(issuer, accountName, secret string) string {
	label := fmt.Sprintf("%s:%s", issuer, accountName)
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", fmt.Sprintf("%d", Digits))
	v.Set("period", fmt.Sprintf("%d", PeriodSeconds))

	return fmt.Sprintf("otpauth://totp/%s?%s", url.PathEscape(label), v.Encode())
}

// GenerateCode calculates the 6-digit TOTP code for a given secret at a specific Unix timestamp.
func GenerateCode(secret string, t time.Time) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", fmt.Errorf("decode base32 secret: %w", err)
	}

	counter := uint64(t.Unix() / PeriodSeconds)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)

	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	binaryCode := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	code := binaryCode % Modulus

	return fmt.Sprintf("%06d", code), nil
}

// ValidateCode verifies a 6-digit code against a base32 secret, allowing -1, 0, +1 time windows for clock drift.
func ValidateCode(secret, code string, t time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != Digits {
		return false
	}

	windows := []time.Duration{-PeriodSeconds * time.Second, 0, PeriodSeconds * time.Second}
	for _, w := range windows {
		expected, err := GenerateCode(secret, t.Add(w))
		if err == nil && hmac.Equal([]byte(expected), []byte(code)) {
			return true
		}
	}
	return false
}

// RecoveryCode holds a plaintext code and its SHA-256 hash.
type RecoveryCode struct {
	Code     string
	CodeHash string
}

// GenerateRecoveryCodes produces 10 random 10-character alphanumeric single-use recovery codes.
func GenerateRecoveryCodes(count int) ([]RecoveryCode, error) {
	if count <= 0 {
		count = 10
	}
	codes := make([]RecoveryCode, count)
	const chars = "abcdefghjkmnpqrstuvwxyz23456789" // readable, avoids ambiguous 0/O, 1/l
	for i := 0; i < count; i++ {
		b := make([]byte, 10)
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("generate recovery code: %w", err)
		}
		for j := range b {
			b[j] = chars[int(b[j])%len(chars)]
		}
		raw := string(b)
		hash := sha256.Sum256([]byte(raw))
		codes[i] = RecoveryCode{
			Code:     raw,
			CodeHash: hex.EncodeToString(hash[:]),
		}
	}
	return codes, nil
}

// HashRecoveryCode hashes a recovery code with SHA-256 for verification.
func HashRecoveryCode(code string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(code)))
	return hex.EncodeToString(h[:])
}
