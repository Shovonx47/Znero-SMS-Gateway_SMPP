package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemory      = 64 * 1024 // 64 MB
	argonIterations  = 3
	argonParallelism = 2
	argonKeyLength   = 32
	argonSaltLength  = 16
	MinPasswordLength = 12
)

var (
	ErrPasswordTooShort = errors.New("password must be at least 12 characters")
	ErrPasswordCommon   = errors.New("password is too common; choose a more secure password")
	ErrInvalidHash      = errors.New("invalid password hash format")
)

// Common passwords blacklist (case-insensitive substring/match).
var commonPasswords = map[string]struct{}{
	"password1234": {}, "password12345": {}, "123456789012": {}, "qwertyuiop12": {},
	"administrator": {}, "admin12345678": {}, "welcome123456": {}, "changeme1234": {},
	"letmein123456": {}, "iloveyou12345": {}, "zneroplatform": {}, "zneroadmin12": {},
	"superadmin123": {}, "passwordpassword": {}, "company123456": {},
}

// ValidateStrength ensures the password is at least 12 characters and not in the common password list.
func ValidateStrength(pwd string) error {
	trimmed := strings.TrimSpace(pwd)
	if len(trimmed) < MinPasswordLength {
		return ErrPasswordTooShort
	}
	lower := strings.ToLower(trimmed)
	if _, common := commonPasswords[lower]; common {
		return ErrPasswordCommon
	}
	return nil
}

// Hash creates an Argon2id hash with a cryptographically secure random salt.
// Format: $argon2id$v=19$m=65536,t=3,p=2$<salt-b64>$<hash-b64>
func Hash(pwd string) (string, error) {
	if err := ValidateStrength(pwd); err != nil {
		return "", err
	}
	salt := make([]byte, argonSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}

	hash := argon2.IDKey([]byte(pwd), salt, argonIterations, argonMemory, argonParallelism, argonKeyLength)
	saltB64 := base64.RawStdEncoding.EncodeToString(salt)
	hashB64 := base64.RawStdEncoding.EncodeToString(hash)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonIterations, argonParallelism, saltB64, hashB64), nil
}

// Verify checks a password against an Argon2id hash using constant-time comparison.
func Verify(pwd, encodedHash string) (bool, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrInvalidHash
	}

	var memory uint32
	var iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrInvalidHash
	}

	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, ErrInvalidHash
	}

	computed := argon2.IDKey([]byte(pwd), salt, iterations, memory, parallelism, uint32(len(expectedHash)))
	if subtle.ConstantTimeCompare(expectedHash, computed) == 1 {
		return true, nil
	}
	return false, nil
}
