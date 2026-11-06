package auth

import (
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID                  uuid.UUID  `json:"id"`
	OrganizationID      uuid.UUID  `json:"organization_id"`
	Email               string     `json:"email"`
	PasswordHash        *string    `json:"-"`
	Status              string     `json:"status"` // "active", "suspended", "invited"
	MFAEnabled          bool       `json:"mfa_enabled"`
	MFASecretEncrypted  *string    `json:"-"`
	MFAEnrolledAt       *time.Time `json:"mfa_enrolled_at,omitempty"`
	FailedLoginAttempts int        `json:"failed_login_attempts"`
	LockedUntil         *time.Time `json:"locked_until,omitempty"`
	Roles               []string   `json:"roles"`
	RoleIDs             []uuid.UUID `json:"role_ids"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

type SessionInfo struct {
	ID        uuid.UUID `json:"id"`
	IPAddress string    `json:"ip_address"`
	UserAgent string    `json:"user_agent"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used_at"`
	IsCurrent bool      `json:"is_current"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginResponse struct {
	AccessToken string  `json:"access_token,omitempty"`
	ExpiresIn   int     `json:"expires_in,omitempty"` // seconds
	User        *User   `json:"user,omitempty"`
	MFARequired bool    `json:"mfa_required,omitempty"`
	MFAToken    string  `json:"mfa_challenge_token,omitempty"`
}

type MFAVerifyRequest struct {
	ChallengeToken string `json:"challenge_token"`
	Code           string `json:"code"` // 6-digit TOTP or 10-char recovery code
}

type MFASetupResponse struct {
	Secret        string   `json:"secret"`
	URI           string   `json:"uri"`
	RecoveryCodes []string `json:"recovery_codes"`
}

type MFAConfirmRequest struct {
	Secret        string   `json:"secret"`
	Code          string   `json:"code"`
	RecoveryCodes []string `json:"recovery_codes"`
}

type ResetPasswordRequest struct {
	Email string `json:"email"`
}

type ConfirmResetRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type InviteUserRequest struct {
	Email          string      `json:"email"`
	OrganizationID *uuid.UUID  `json:"organization_id,omitempty"`
	RoleIDs        []uuid.UUID `json:"role_ids"`
}

type AcceptInvitationRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

type LoginHistoryItem struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	Success   bool      `json:"success"`
	Reason    string    `json:"reason"`
	IPAddress string    `json:"ip_address"`
	UserAgent string    `json:"user_agent"`
	CreatedAt time.Time `json:"created_at"`
}
