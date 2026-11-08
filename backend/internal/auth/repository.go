package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

var (
	ErrUserNotFound    = errors.New("user not found")
	ErrSessionNotFound = errors.New("session not found")
	ErrTokenInvalid    = errors.New("token invalid or already used")
)

type Session struct {
	ID               uuid.UUID  `json:"id"`
	UserID           uuid.UUID  `json:"user_id"`
	OrganizationID   uuid.UUID  `json:"organization_id"`
	RefreshTokenHash string     `json:"-"`
	FamilyID         uuid.UUID  `json:"family_id"`
	IPAddress        string     `json:"ip_address"`
	UserAgent        string     `json:"user_agent"`
	ExpiresAt        time.Time  `json:"expires_at"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	RevokedReason    *string    `json:"revoked_reason,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	LastUsedAt       time.Time  `json:"last_used_at"`
}

type Repository interface {
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	GetUserByID(ctx context.Context, id uuid.UUID) (*User, error)
	ListUsers(ctx context.Context, tc *tenant.Context, orgID *uuid.UUID, search, status string, page, pageSize int) ([]User, int, error)
	CreateUser(ctx context.Context, tx pgx.Tx, u User, roleIDs []uuid.UUID) (*User, error)
	UpdateUser(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string, roleIDs []uuid.UUID) error
	UpdatePassword(ctx context.Context, tx pgx.Tx, id uuid.UUID, passwordHash string) error
	UpdateMFA(ctx context.Context, tx pgx.Tx, id uuid.UUID, enabled bool, secretEncrypted *string) error
	IncrementFailedLogin(ctx context.Context, email string, maxAttempts int, lockDuration time.Duration) (int, bool, error)
	ResetFailedLogin(ctx context.Context, id uuid.UUID) error

	SaveRecoveryCodes(ctx context.Context, tx pgx.Tx, userID uuid.UUID, codeHashes []string) error
	VerifyAndConsumeRecoveryCode(ctx context.Context, tx pgx.Tx, userID uuid.UUID, codeHash string) (bool, error)

	CreateSession(ctx context.Context, tx pgx.Tx, s Session) error
	GetSessionByID(ctx context.Context, id uuid.UUID) (*Session, error)
	GetSessionByRefreshHash(ctx context.Context, hash string) (*Session, error)
	ListActiveSessions(ctx context.Context, userID uuid.UUID) ([]Session, error)
	RotateSessionToken(ctx context.Context, tx pgx.Tx, sessionID uuid.UUID, newHash string, newExpiry time.Time) error
	RevokeSession(ctx context.Context, tx pgx.Tx, sessionID uuid.UUID, reason string) error
	RevokeAllUserSessions(ctx context.Context, tx pgx.Tx, userID uuid.UUID, reason string) error
	RevokeFamilySessions(ctx context.Context, tx pgx.Tx, familyID uuid.UUID, reason string) error

	CreateMFAChallenge(ctx context.Context, userID uuid.UUID, tokenHash string, expiry time.Time, ip string) error
	VerifyMFAChallenge(ctx context.Context, tokenHash string) (*uuid.UUID, error)

	CreatePasswordReset(ctx context.Context, tx pgx.Tx, userID uuid.UUID, tokenHash string, expiry time.Time) error
	VerifyPasswordReset(ctx context.Context, tokenHash string) (*uuid.UUID, error)
	ConsumePasswordReset(ctx context.Context, tx pgx.Tx, tokenHash string) error

	CreateInvitation(ctx context.Context, tx pgx.Tx, userID uuid.UUID, tokenHash string, expiry time.Time) error
	VerifyInvitation(ctx context.Context, tokenHash string) (*uuid.UUID, error)
	ConsumeInvitation(ctx context.Context, tx pgx.Tx, tokenHash string) error

	RecordLoginHistory(ctx context.Context, entry LoginHistoryItem, orgID *uuid.UUID, reqID string) error
	ListLoginHistory(ctx context.Context, tc *tenant.Context, userID *uuid.UUID, page, pageSize int) ([]LoginHistoryItem, int, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

func (r *pgRepository) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	query := `
		SELECT u.id, u.organization_id, u.email, u.password_hash, u.status,
		       u.mfa_enabled, u.mfa_secret_encrypted, u.mfa_enrolled_at,
		       u.failed_login_attempts, u.locked_until, u.created_at, u.updated_at,
		       COALESCE(array_agg(r.name) FILTER (WHERE r.name IS NOT NULL), '{}') AS roles,
		       COALESCE(array_agg(r.id) FILTER (WHERE r.id IS NOT NULL), '{}') AS role_ids
		FROM users u
		LEFT JOIN user_roles ur ON u.id = ur.user_id
		LEFT JOIN roles r ON ur.role_id = r.id
		WHERE lower(u.email) = lower($1)
		GROUP BY u.id`

	var u User
	err := r.pool.QueryRow(ctx, query, strings.TrimSpace(email)).Scan(
		&u.ID, &u.OrganizationID, &u.Email, &u.PasswordHash, &u.Status,
		&u.MFAEnabled, &u.MFASecretEncrypted, &u.MFAEnrolledAt,
		&u.FailedLoginAttempts, &u.LockedUntil, &u.CreatedAt, &u.UpdatedAt,
		&u.Roles, &u.RoleIDs,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return &u, nil
}

func (r *pgRepository) GetUserByID(ctx context.Context, id uuid.UUID) (*User, error) {
	query := `
		SELECT u.id, u.organization_id, u.email, u.password_hash, u.status,
		       u.mfa_enabled, u.mfa_secret_encrypted, u.mfa_enrolled_at,
		       u.failed_login_attempts, u.locked_until, u.created_at, u.updated_at,
		       COALESCE(array_agg(r.name) FILTER (WHERE r.name IS NOT NULL), '{}') AS roles,
		       COALESCE(array_agg(r.id) FILTER (WHERE r.id IS NOT NULL), '{}') AS role_ids
		FROM users u
		LEFT JOIN user_roles ur ON u.id = ur.user_id
		LEFT JOIN roles r ON ur.role_id = r.id
		WHERE u.id = $1
		GROUP BY u.id`

	var u User
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&u.ID, &u.OrganizationID, &u.Email, &u.PasswordHash, &u.Status,
		&u.MFAEnabled, &u.MFASecretEncrypted, &u.MFAEnrolledAt,
		&u.FailedLoginAttempts, &u.LockedUntil, &u.CreatedAt, &u.UpdatedAt,
		&u.Roles, &u.RoleIDs,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return &u, nil
}

func (r *pgRepository) ListUsers(ctx context.Context, tc *tenant.Context, orgID *uuid.UUID, search, status string, page, pageSize int) ([]User, int, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 25
	}

	var where []string
	var args []any
	argIdx := 1

	// Mandatory Tenant Boundary:
	if !tc.IsPlatformAdmin {
		where = append(where, fmt.Sprintf(`u.organization_id IN (
			SELECT id FROM organizations WHERE path LIKE $%d || '%%'
		)`, argIdx))
		args = append(args, tc.ActorOrgPath)
		argIdx++
	}

	if orgID != nil {
		where = append(where, fmt.Sprintf("u.organization_id = $%d", argIdx))
		args = append(args, *orgID)
		argIdx++
	}
	if status != "" {
		where = append(where, fmt.Sprintf("u.status = $%d", argIdx))
		args = append(args, status)
		argIdx++
	}
	if search != "" {
		where = append(where, fmt.Sprintf("u.email ILIKE $%d", argIdx))
		args = append(args, "%"+search+"%")
		argIdx++
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM users u %s", whereClause)
	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * pageSize
	query := fmt.Sprintf(`
		SELECT u.id, u.organization_id, u.email, u.status, u.mfa_enabled,
		       u.failed_login_attempts, u.locked_until, u.created_at, u.updated_at,
		       COALESCE(array_agg(r.name) FILTER (WHERE r.name IS NOT NULL), '{}') AS roles,
		       COALESCE(array_agg(r.id) FILTER (WHERE r.id IS NOT NULL), '{}') AS role_ids
		FROM users u
		LEFT JOIN user_roles ur ON u.id = ur.user_id
		LEFT JOIN roles r ON ur.role_id = r.id
		%s
		GROUP BY u.id
		ORDER BY u.created_at DESC
		LIMIT $%d OFFSET $%d`, whereClause, argIdx, argIdx+1)

	args = append(args, pageSize, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(
			&u.ID, &u.OrganizationID, &u.Email, &u.Status, &u.MFAEnabled,
			&u.FailedLoginAttempts, &u.LockedUntil, &u.CreatedAt, &u.UpdatedAt,
			&u.Roles, &u.RoleIDs,
		); err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}

	return users, total, nil
}

func (r *pgRepository) CreateUser(ctx context.Context, tx pgx.Tx, u User, roleIDs []uuid.UUID) (*User, error) {
	if tx == nil {
		newTx, err := r.pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer func() { _ = newTx.Rollback(ctx) }()
		created, err := r.CreateUser(ctx, newTx, u, roleIDs)
		if err != nil {
			return nil, err
		}
		if err := newTx.Commit(ctx); err != nil {
			return nil, err
		}
		return created, nil
	}

	query := `
		INSERT INTO users (
			id, organization_id, email, password_hash, status, mfa_enabled, mfa_secret_encrypted, created_at, updated_at
		) VALUES (
			COALESCE(NULLIF($1, '00000000-0000-0000-0000-000000000000'::uuid), gen_random_uuid()),
			$2, lower($3), $4, $5, $6, $7, now(), now()
		)
		RETURNING id, organization_id, email, status, mfa_enabled, created_at, updated_at`

	var created User
	err := tx.QueryRow(ctx, query, u.ID, u.OrganizationID, u.Email, u.PasswordHash, u.Status, u.MFAEnabled, u.MFASecretEncrypted).Scan(
		&created.ID, &created.OrganizationID, &created.Email, &created.Status, &created.MFAEnabled, &created.CreatedAt, &created.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}

	for _, rid := range roleIDs {
		_, err := tx.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, created.ID, rid)
		if err != nil {
			return nil, fmt.Errorf("assign user role: %w", err)
		}
	}
	created.RoleIDs = roleIDs
	return &created, nil
}

func (r *pgRepository) UpdateUser(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string, roleIDs []uuid.UUID) error {
	if tx == nil {
		newTx, err := r.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = newTx.Rollback(ctx) }()
		if err := r.UpdateUser(ctx, newTx, id, status, roleIDs); err != nil {
			return err
		}
		return newTx.Commit(ctx)
	}

	query := `UPDATE users SET status = $1, updated_at = now() WHERE id = $2`
	if _, err := tx.Exec(ctx, query, status, id); err != nil {
		return err
	}

	if roleIDs != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1`, id); err != nil {
			return err
		}
		for _, rid := range roleIDs {
			if _, err := tx.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2)`, id, rid); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *pgRepository) UpdatePassword(ctx context.Context, tx pgx.Tx, id uuid.UUID, passwordHash string) error {
	query := `UPDATE users SET password_hash = $1, failed_login_attempts = 0, locked_until = NULL, updated_at = now() WHERE id = $2`
	if tx != nil {
		_, err := tx.Exec(ctx, query, passwordHash, id)
		return err
	}
	_, err := r.pool.Exec(ctx, query, passwordHash, id)
	return err
}

func (r *pgRepository) UpdateMFA(ctx context.Context, tx pgx.Tx, id uuid.UUID, enabled bool, secretEncrypted *string) error {
	var enrolledAt *time.Time
	if enabled {
		now := time.Now()
		enrolledAt = &now
	}
	query := `UPDATE users SET mfa_enabled = $1, mfa_secret_encrypted = $2, mfa_enrolled_at = $3, updated_at = now() WHERE id = $4`
	if tx != nil {
		_, err := tx.Exec(ctx, query, enabled, secretEncrypted, enrolledAt, id)
		return err
	}
	_, err := r.pool.Exec(ctx, query, enabled, secretEncrypted, enrolledAt, id)
	return err
}

func (r *pgRepository) IncrementFailedLogin(ctx context.Context, email string, maxAttempts int, lockDuration time.Duration) (int, bool, error) {
	query := `
		UPDATE users
		SET failed_login_attempts = failed_login_attempts + 1,
		    locked_until = CASE
		        WHEN failed_login_attempts + 1 >= $2 THEN now() + $3::interval
		        ELSE locked_until
		    END
		WHERE lower(email) = lower($1)
		RETURNING failed_login_attempts, (locked_until IS NOT NULL AND locked_until > now())`

	var attempts int
	var isLocked bool
	err := r.pool.QueryRow(ctx, query, strings.TrimSpace(email), maxAttempts, fmt.Sprintf("%d seconds", int(lockDuration.Seconds()))).Scan(&attempts, &isLocked)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil // generic response, no enumeration
		}
		return 0, false, err
	}
	return attempts, isLocked, nil
}

func (r *pgRepository) ResetFailedLogin(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `UPDATE users SET failed_login_attempts = 0, locked_until = NULL WHERE id = $1`, id)
	return err
}

func (r *pgRepository) SaveRecoveryCodes(ctx context.Context, tx pgx.Tx, userID uuid.UUID, codeHashes []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM user_recovery_codes WHERE user_id = $1`, userID); err != nil {
		return err
	}
	for _, hash := range codeHashes {
		if _, err := tx.Exec(ctx, `INSERT INTO user_recovery_codes (user_id, code_hash) VALUES ($1, $2)`, userID, hash); err != nil {
			return err
		}
	}
	return nil
}

func (r *pgRepository) VerifyAndConsumeRecoveryCode(ctx context.Context, tx pgx.Tx, userID uuid.UUID, codeHash string) (bool, error) {
	query := `
		UPDATE user_recovery_codes
		SET used_at = now()
		WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL
		RETURNING id`

	var id uuid.UUID
	err := tx.QueryRow(ctx, query, userID, codeHash).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (r *pgRepository) CreateSession(ctx context.Context, tx pgx.Tx, s Session) error {
	query := `
		INSERT INTO sessions (
			id, user_id, organization_id, refresh_token_hash, family_id,
			ip_address, user_agent, expires_at, created_at, last_used_at
		) VALUES (
			COALESCE(NULLIF($1, '00000000-0000-0000-0000-000000000000'::uuid), gen_random_uuid()),
			$2, $3, $4, $5, $6, $7, $8, now(), now()
		)`

	var err error
	if tx != nil {
		_, err = tx.Exec(ctx, query, s.ID, s.UserID, s.OrganizationID, s.RefreshTokenHash, s.FamilyID, s.IPAddress, s.UserAgent, s.ExpiresAt)
	} else {
		_, err = r.pool.Exec(ctx, query, s.ID, s.UserID, s.OrganizationID, s.RefreshTokenHash, s.FamilyID, s.IPAddress, s.UserAgent, s.ExpiresAt)
	}
	return err
}

func (r *pgRepository) GetSessionByID(ctx context.Context, id uuid.UUID) (*Session, error) {
	query := `
		SELECT id, user_id, organization_id, refresh_token_hash, family_id,
		       ip_address, user_agent, expires_at, revoked_at, revoked_reason, created_at, last_used_at
		FROM sessions
		WHERE id = $1`

	var s Session
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&s.ID, &s.UserID, &s.OrganizationID, &s.RefreshTokenHash, &s.FamilyID,
		&s.IPAddress, &s.UserAgent, &s.ExpiresAt, &s.RevokedAt, &s.RevokedReason,
		&s.CreatedAt, &s.LastUsedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}
	return &s, nil
}

func (r *pgRepository) GetSessionByRefreshHash(ctx context.Context, hash string) (*Session, error) {
	query := `
		SELECT id, user_id, organization_id, refresh_token_hash, family_id,
		       ip_address, user_agent, expires_at, revoked_at, revoked_reason, created_at, last_used_at
		FROM sessions
		WHERE refresh_token_hash = $1`

	var s Session
	err := r.pool.QueryRow(ctx, query, hash).Scan(
		&s.ID, &s.UserID, &s.OrganizationID, &s.RefreshTokenHash, &s.FamilyID,
		&s.IPAddress, &s.UserAgent, &s.ExpiresAt, &s.RevokedAt, &s.RevokedReason,
		&s.CreatedAt, &s.LastUsedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}
	return &s, nil
}

func (r *pgRepository) ListActiveSessions(ctx context.Context, userID uuid.UUID) ([]Session, error) {
	query := `
		SELECT id, user_id, organization_id, refresh_token_hash, family_id,
		       ip_address, user_agent, expires_at, revoked_at, revoked_reason, created_at, last_used_at
		FROM sessions
		WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()
		ORDER BY last_used_at DESC`

	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(
			&s.ID, &s.UserID, &s.OrganizationID, &s.RefreshTokenHash, &s.FamilyID,
			&s.IPAddress, &s.UserAgent, &s.ExpiresAt, &s.RevokedAt, &s.RevokedReason,
			&s.CreatedAt, &s.LastUsedAt,
		); err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}

func (r *pgRepository) RotateSessionToken(ctx context.Context, tx pgx.Tx, sessionID uuid.UUID, newHash string, newExpiry time.Time) error {
	query := `UPDATE sessions SET refresh_token_hash = $1, expires_at = $2, last_used_at = now() WHERE id = $3 AND revoked_at IS NULL`
	if tx != nil {
		tag, err := tx.Exec(ctx, query, newHash, newExpiry, sessionID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrSessionNotFound
		}
		return nil
	}
	tag, err := r.pool.Exec(ctx, query, newHash, newExpiry, sessionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionNotFound
	}
	return nil
}

func (r *pgRepository) RevokeSession(ctx context.Context, tx pgx.Tx, sessionID uuid.UUID, reason string) error {
	query := `UPDATE sessions SET revoked_at = now(), revoked_reason = $1 WHERE id = $2 AND revoked_at IS NULL`
	var err error
	if tx != nil {
		_, err = tx.Exec(ctx, query, reason, sessionID)
	} else {
		_, err = r.pool.Exec(ctx, query, reason, sessionID)
	}
	return err
}

func (r *pgRepository) RevokeAllUserSessions(ctx context.Context, tx pgx.Tx, userID uuid.UUID, reason string) error {
	query := `UPDATE sessions SET revoked_at = now(), revoked_reason = $1 WHERE user_id = $2 AND revoked_at IS NULL`
	var err error
	if tx != nil {
		_, err = tx.Exec(ctx, query, reason, userID)
	} else {
		_, err = r.pool.Exec(ctx, query, reason, userID)
	}
	return err
}

func (r *pgRepository) RevokeFamilySessions(ctx context.Context, tx pgx.Tx, familyID uuid.UUID, reason string) error {
	query := `UPDATE sessions SET revoked_at = now(), revoked_reason = $1 WHERE family_id = $2 AND revoked_at IS NULL`
	var err error
	if tx != nil {
		_, err = tx.Exec(ctx, query, reason, familyID)
	} else {
		_, err = r.pool.Exec(ctx, query, reason, familyID)
	}
	return err
}

func (r *pgRepository) CreateMFAChallenge(ctx context.Context, userID uuid.UUID, tokenHash string, expiry time.Time, ip string) error {
	query := `INSERT INTO mfa_challenges (user_id, challenge_token_hash, expires_at, ip_address) VALUES ($1, $2, $3, $4)`
	_, err := r.pool.Exec(ctx, query, userID, tokenHash, expiry, ip)
	return err
}

func (r *pgRepository) VerifyMFAChallenge(ctx context.Context, tokenHash string) (*uuid.UUID, error) {
	query := `
		UPDATE mfa_challenges
		SET used_at = now()
		WHERE challenge_token_hash = $1 AND used_at IS NULL AND expires_at > now()
		RETURNING user_id`

	var uid uuid.UUID
	err := r.pool.QueryRow(ctx, query, tokenHash).Scan(&uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTokenInvalid
		}
		return nil, err
	}
	return &uid, nil
}

func (r *pgRepository) CreatePasswordReset(ctx context.Context, tx pgx.Tx, userID uuid.UUID, tokenHash string, expiry time.Time) error {
	query := `INSERT INTO password_resets (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`
	if tx != nil {
		_, err := tx.Exec(ctx, query, userID, tokenHash, expiry)
		return err
	}
	_, err := r.pool.Exec(ctx, query, userID, tokenHash, expiry)
	return err
}

func (r *pgRepository) VerifyPasswordReset(ctx context.Context, tokenHash string) (*uuid.UUID, error) {
	query := `SELECT user_id FROM password_resets WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()`
	var uid uuid.UUID
	err := r.pool.QueryRow(ctx, query, tokenHash).Scan(&uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTokenInvalid
		}
		return nil, err
	}
	return &uid, nil
}

func (r *pgRepository) ConsumePasswordReset(ctx context.Context, tx pgx.Tx, tokenHash string) error {
	query := `UPDATE password_resets SET used_at = now() WHERE token_hash = $1 AND used_at IS NULL`
	if tx != nil {
		tag, err := tx.Exec(ctx, query, tokenHash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrTokenInvalid
		}
		return nil
	}
	tag, err := r.pool.Exec(ctx, query, tokenHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTokenInvalid
	}
	return nil
}

func (r *pgRepository) CreateInvitation(ctx context.Context, tx pgx.Tx, userID uuid.UUID, tokenHash string, expiry time.Time) error {
	query := `INSERT INTO user_invitations (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`
	if tx != nil {
		_, err := tx.Exec(ctx, query, userID, tokenHash, expiry)
		return err
	}
	_, err := r.pool.Exec(ctx, query, userID, tokenHash, expiry)
	return err
}

func (r *pgRepository) VerifyInvitation(ctx context.Context, tokenHash string) (*uuid.UUID, error) {
	query := `SELECT user_id FROM user_invitations WHERE token_hash = $1 AND accepted_at IS NULL AND expires_at > now()`
	var uid uuid.UUID
	err := r.pool.QueryRow(ctx, query, tokenHash).Scan(&uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTokenInvalid
		}
		return nil, err
	}
	return &uid, nil
}

func (r *pgRepository) ConsumeInvitation(ctx context.Context, tx pgx.Tx, tokenHash string) error {
	query := `UPDATE user_invitations SET accepted_at = now() WHERE token_hash = $1 AND accepted_at IS NULL`
	if tx != nil {
		tag, err := tx.Exec(ctx, query, tokenHash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrTokenInvalid
		}
		return nil
	}
	tag, err := r.pool.Exec(ctx, query, tokenHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTokenInvalid
	}
	return nil
}

func (r *pgRepository) RecordLoginHistory(ctx context.Context, e LoginHistoryItem, orgID *uuid.UUID, reqID string) error {
	query := `
		INSERT INTO login_history (
			email, success, reason, ip_address, user_agent, request_id, organization_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7)`

	_, err := r.pool.Exec(ctx, query, e.Email, e.Success, e.Reason, e.IPAddress, e.UserAgent, reqID, orgID)
	return err
}

func (r *pgRepository) ListLoginHistory(ctx context.Context, tc *tenant.Context, userID *uuid.UUID, page, pageSize int) ([]LoginHistoryItem, int, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 25
	}

	var where []string
	var args []any
	argIdx := 1

	if !tc.IsPlatformAdmin {
		where = append(where, fmt.Sprintf(`organization_id IN (
			SELECT id FROM organizations WHERE path LIKE $%d || '%%'
		)`, argIdx))
		args = append(args, tc.ActorOrgPath)
		argIdx++
	}

	if userID != nil {
		where = append(where, fmt.Sprintf("user_id = $%d", argIdx))
		args = append(args, *userID)
		argIdx++
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	var total int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM login_history %s", whereClause)
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * pageSize
	query := fmt.Sprintf(`
		SELECT id, email, success, reason, ip_address, user_agent, created_at
		FROM login_history
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d`, whereClause, argIdx, argIdx+1)

	args = append(args, pageSize, offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var items []LoginHistoryItem
	for rows.Next() {
		var item LoginHistoryItem
		if err := rows.Scan(
			&item.ID, &item.Email, &item.Success, &item.Reason,
			&item.IPAddress, &item.UserAgent, &item.CreatedAt,
		); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}

	return items, total, nil
}
