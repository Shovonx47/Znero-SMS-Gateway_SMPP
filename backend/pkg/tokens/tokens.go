package tokens

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

var (
	ErrInvalidToken = errors.New("invalid or expired token")
)

type AccessClaims struct {
	UserID          uuid.UUID `json:"sub"`
	OrgID           uuid.UUID `json:"org_id"`
	OrgType         string    `json:"org_type"`
	OrgPath         string    `json:"org_path"`
	IsPlatformAdmin bool      `json:"is_platform_admin"`
	SessionID       uuid.UUID `json:"session_id"`
	Permissions     []string  `json:"permissions"`
	jwt.RegisteredClaims
}

type TokenManager struct {
	accessSecret []byte
	accessExpiry time.Duration
}

func NewTokenManager(accessSecret string, accessExpiry time.Duration) (*TokenManager, error) {
	if len(accessSecret) < 32 {
		return nil, errors.New("JWT access secret must be at least 32 characters")
	}
	if accessExpiry <= 0 {
		accessExpiry = 15 * time.Minute
	}
	return &TokenManager{
		accessSecret: []byte(accessSecret),
		accessExpiry: accessExpiry,
	}, nil
}

// GenerateAccessToken signs an HMAC-SHA256 JWT with claims.
func (m *TokenManager) GenerateAccessToken(userID, orgID, sessionID uuid.UUID, orgType, orgPath string, isPlatformAdmin bool, permissions []string) (string, error) {
	now := time.Now()
	claims := AccessClaims{
		UserID:          userID,
		OrgID:           orgID,
		OrgType:         orgType,
		OrgPath:         orgPath,
		IsPlatformAdmin: isPlatformAdmin,
		SessionID:       sessionID,
		Permissions:     permissions,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(m.accessExpiry)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    "znero-auth",
			Subject:   userID.String(),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(m.accessSecret)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return signed, nil
}

// ValidateAccessToken parses and validates the access token.
func (m *TokenManager) ValidateAccessToken(tokenStr string) (*AccessClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &AccessClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return m.accessSecret, nil
	})

	if err != nil || !token.Valid {
		return nil, ErrInvalidToken
	}

	claims, ok := token.Claims.(*AccessClaims)
	if !ok {
		return nil, ErrInvalidToken
	}

	return claims, nil
}

// GenerateOpaqueToken generates a cryptographically secure random token (32 bytes = 64 hex characters).
func GenerateOpaqueToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// HashToken produces the SHA-256 hex string used for database lookup and storage.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
