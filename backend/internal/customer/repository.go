package customer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type OrgInfo struct {
	ID       uuid.UUID
	Name     string
	Type     string
	ParentID *uuid.UUID
	Path     string
}

type Repository interface {
	BeginTx(ctx context.Context) (pgx.Tx, error)
	GetOrgInfo(ctx context.Context, orgID uuid.UUID) (*OrgInfo, error)

	// Profiles
	GetProfileByID(ctx context.Context, id uuid.UUID) (*CustomerProfile, error)
	GetProfileByOrgID(ctx context.Context, orgID uuid.UUID) (*CustomerProfile, error)
	ListProfiles(ctx context.Context, filter ProfileListFilter) ([]CustomerProfile, int, error)
	CreateProfile(ctx context.Context, tx pgx.Tx, p *CustomerProfile, reason string) (*CustomerProfile, error)
	UpdateProfile(ctx context.Context, tx pgx.Tx, p *CustomerProfile, reason string) (*CustomerProfile, error)
	ListProfileHistory(ctx context.Context, profileID uuid.UUID) ([]CustomerProfileHistory, error)
	GetProfileHistoryVersion(ctx context.Context, profileID uuid.UUID, version int) (*CustomerProfileHistory, error)

	// API Keys
	GetAPIKeyByID(ctx context.Context, id uuid.UUID) (*CustomerAPIKey, error)
	ListAPIKeysByOrg(ctx context.Context, orgID uuid.UUID) ([]CustomerAPIKey, error)
	CreateAPIKey(ctx context.Context, tx pgx.Tx, k *CustomerAPIKey) (*CustomerAPIKey, error)
	RevokeAPIKey(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*CustomerAPIKey, error)

	// SMPP Accounts
	GetSMPPAccountByID(ctx context.Context, id uuid.UUID) (*CustomerSMPPAccount, error)
	ListSMPPAccountsByOrg(ctx context.Context, orgID uuid.UUID) ([]CustomerSMPPAccount, error)
	CreateSMPPAccount(ctx context.Context, tx pgx.Tx, a *CustomerSMPPAccount) (*CustomerSMPPAccount, error)
	UpdateSMPPAccount(ctx context.Context, tx pgx.Tx, a *CustomerSMPPAccount) (*CustomerSMPPAccount, error)
	ResetSMPPPassword(ctx context.Context, tx pgx.Tx, id uuid.UUID, passwordHash string) (*CustomerSMPPAccount, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

func (r *pgRepository) BeginTx(ctx context.Context) (pgx.Tx, error) {
	return r.pool.Begin(ctx)
}

func (r *pgRepository) GetOrgInfo(ctx context.Context, orgID uuid.UUID) (*OrgInfo, error) {
	var info OrgInfo
	err := r.pool.QueryRow(ctx, `SELECT id, name, type, parent_id, path FROM organizations WHERE id = $1`, orgID).
		Scan(&info.ID, &info.Name, &info.Type, &info.ParentID, &info.Path)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotCustomerOrg
		}
		return nil, err
	}
	return &info, nil
}

const profileSelectCols = `
	cp.id, cp.organization_id, o.name, o.parent_id, po.name,
	cp.account_type, cp.billing_currency, cp.timezone, cp.default_language,
	cp.technical_contact, cp.billing_contact,
	cp.credit_limit::float8, cp.default_max_tps, cp.daily_message_cap, cp.monthly_message_cap,
	cp.allowed_countries, cp.destination_policy,
	cp.billing_mode, cp.dlr_wait_hours, cp.no_dlr_policy, cp.failed_after_accept_policy,
	cp.status, cp.notes, cp.archived,
	cp.version, cp.created_at, cp.updated_at, cp.created_by, cp.updated_by
`

const profileFromJoins = `
	FROM customer_profiles cp
	JOIN organizations o ON o.id = cp.organization_id
	LEFT JOIN organizations po ON po.id = o.parent_id
`

func scanProfile(row pgx.Row) (*CustomerProfile, error) {
	var p CustomerProfile
	err := row.Scan(
		&p.ID, &p.OrganizationID, &p.OrganizationName, &p.ParentOrganizationID, &p.ParentOrganizationName,
		&p.AccountType, &p.BillingCurrency, &p.Timezone, &p.DefaultLanguage,
		&p.TechnicalContact, &p.BillingContact,
		&p.CreditLimit, &p.DefaultMaxTPS, &p.DailyMessageCap, &p.MonthlyMessageCap,
		&p.AllowedCountries, &p.DestinationPolicy,
		&p.BillingMode, &p.DLRWaitHours, &p.NoDLRPolicy, &p.FailedAfterAcceptPolicy,
		&p.Status, &p.Notes, &p.Archived,
		&p.Version, &p.CreatedAt, &p.UpdatedAt, &p.CreatedBy, &p.UpdatedBy,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrProfileNotFound
		}
		return nil, err
	}
	if p.AllowedCountries == nil {
		p.AllowedCountries = []string{}
	}
	return &p, nil
}

func (r *pgRepository) GetProfileByID(ctx context.Context, id uuid.UUID) (*CustomerProfile, error) {
	q := `SELECT ` + profileSelectCols + profileFromJoins + ` WHERE cp.id = $1`
	return scanProfile(r.pool.QueryRow(ctx, q, id))
}

func (r *pgRepository) GetProfileByOrgID(ctx context.Context, orgID uuid.UUID) (*CustomerProfile, error) {
	q := `SELECT ` + profileSelectCols + profileFromJoins + ` WHERE cp.organization_id = $1`
	return scanProfile(r.pool.QueryRow(ctx, q, orgID))
}

func (r *pgRepository) ListProfiles(ctx context.Context, filter ProfileListFilter) ([]CustomerProfile, int, error) {
	var where []string
	var args []any
	idx := 1

	if !filter.IncludeArchived {
		where = append(where, "cp.archived = false")
	}
	if filter.OrganizationID != nil {
		where = append(where, fmt.Sprintf("cp.organization_id = $%d", idx))
		args = append(args, *filter.OrganizationID)
		idx++
	}
	if filter.ParentOrgID != nil {
		where = append(where, fmt.Sprintf("o.parent_id = $%d", idx))
		args = append(args, *filter.ParentOrgID)
		idx++
	}
	if filter.Status != "" {
		where = append(where, fmt.Sprintf("cp.status = $%d", idx))
		args = append(args, filter.Status)
		idx++
	}
	if filter.AccountType != "" {
		where = append(where, fmt.Sprintf("cp.account_type = $%d", idx))
		args = append(args, filter.AccountType)
		idx++
	}
	if filter.Search != "" {
		where = append(where, fmt.Sprintf("(o.name ILIKE $%d OR cp.technical_contact ILIKE $%d OR cp.billing_contact ILIKE $%d)", idx, idx, idx))
		args = append(args, "%"+strings.TrimSpace(filter.Search)+"%")
		idx++
	}

	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx, "SELECT COUNT(*) "+profileFromJoins+" "+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count profiles: %w", err)
	}

	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	page := filter.Page
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * limit

	q := fmt.Sprintf(`
		SELECT %s %s %s
		ORDER BY o.name ASC
		LIMIT $%d OFFSET $%d`, profileSelectCols, profileFromJoins, whereSQL, idx, idx+1)
	args = append(args, limit, offset)

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list profiles: %w", err)
	}
	defer rows.Close()

	var items []CustomerProfile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *p)
	}
	return items, total, nil
}

func (r *pgRepository) CreateProfile(ctx context.Context, tx pgx.Tx, p *CustomerProfile, reason string) (*CustomerProfile, error) {
	if p.AllowedCountries == nil {
		p.AllowedCountries = []string{}
	}
	q := `
		INSERT INTO customer_profiles (
			organization_id, account_type, billing_currency, timezone, default_language,
			technical_contact, billing_contact,
			credit_limit, default_max_tps, daily_message_cap, monthly_message_cap,
			allowed_countries, destination_policy,
			billing_mode, dlr_wait_hours, no_dlr_policy, failed_after_accept_policy,
			status, notes, archived,
			version, created_by, updated_by
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7,
			$8, $9, $10, $11,
			$12, $13,
			$14, $15, $16, $17,
			$18, $19, $20,
			1, $21, $21
		)
		RETURNING id, version, created_at, updated_at`

	err := tx.QueryRow(ctx, q,
		p.OrganizationID, p.AccountType, p.BillingCurrency, p.Timezone, p.DefaultLanguage,
		p.TechnicalContact, p.BillingContact,
		p.CreditLimit, p.DefaultMaxTPS, p.DailyMessageCap, p.MonthlyMessageCap,
		p.AllowedCountries, p.DestinationPolicy,
		p.BillingMode, p.DLRWaitHours, p.NoDLRPolicy, p.FailedAfterAcceptPolicy,
		p.Status, p.Notes, p.Archived,
		p.CreatedBy,
	).Scan(&p.ID, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrProfileAlreadyExists
		}
		return nil, fmt.Errorf("insert customer profile: %w", err)
	}
	p.UpdatedBy = p.CreatedBy

	if err := r.insertProfileHistory(ctx, tx, p, p.CreatedBy, reason); err != nil {
		return nil, err
	}
	return p, nil
}

func (r *pgRepository) UpdateProfile(ctx context.Context, tx pgx.Tx, p *CustomerProfile, reason string) (*CustomerProfile, error) {
	if p.AllowedCountries == nil {
		p.AllowedCountries = []string{}
	}
	q := `
		UPDATE customer_profiles SET
			account_type = $2, billing_currency = $3, timezone = $4, default_language = $5,
			technical_contact = $6, billing_contact = $7,
			credit_limit = $8, default_max_tps = $9, daily_message_cap = $10, monthly_message_cap = $11,
			allowed_countries = $12, destination_policy = $13,
			billing_mode = $14, dlr_wait_hours = $15, no_dlr_policy = $16, failed_after_accept_policy = $17,
			status = $18, notes = $19, archived = $20,
			version = version + 1, updated_at = now(), updated_by = $21
		WHERE id = $1
		RETURNING version, updated_at`

	err := tx.QueryRow(ctx, q,
		p.ID, p.AccountType, p.BillingCurrency, p.Timezone, p.DefaultLanguage,
		p.TechnicalContact, p.BillingContact,
		p.CreditLimit, p.DefaultMaxTPS, p.DailyMessageCap, p.MonthlyMessageCap,
		p.AllowedCountries, p.DestinationPolicy,
		p.BillingMode, p.DLRWaitHours, p.NoDLRPolicy, p.FailedAfterAcceptPolicy,
		p.Status, p.Notes, p.Archived,
		p.UpdatedBy,
	).Scan(&p.Version, &p.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrProfileNotFound
		}
		return nil, fmt.Errorf("update customer profile: %w", err)
	}

	if err := r.insertProfileHistory(ctx, tx, p, p.UpdatedBy, reason); err != nil {
		return nil, err
	}
	return p, nil
}

func (r *pgRepository) insertProfileHistory(ctx context.Context, tx pgx.Tx, p *CustomerProfile, actorID *uuid.UUID, reason string) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO customer_profiles_history (profile_id, version, snapshot, actor_user_id, reason)
		VALUES ($1, $2, $3, $4, $5)`,
		p.ID, p.Version, raw, actorID, strings.TrimSpace(reason),
	)
	return err
}

func (r *pgRepository) ListProfileHistory(ctx context.Context, profileID uuid.UUID) ([]CustomerProfileHistory, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, profile_id, version, snapshot, actor_user_id, changed_at, reason
		FROM customer_profiles_history
		WHERE profile_id = $1
		ORDER BY version DESC`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CustomerProfileHistory
	for rows.Next() {
		var h CustomerProfileHistory
		var raw []byte
		if err := rows.Scan(&h.ID, &h.ProfileID, &h.Version, &raw, &h.ActorUserID, &h.ChangedAt, &h.Reason); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &h.Snapshot)
		out = append(out, h)
	}
	return out, nil
}

func (r *pgRepository) GetProfileHistoryVersion(ctx context.Context, profileID uuid.UUID, version int) (*CustomerProfileHistory, error) {
	var h CustomerProfileHistory
	var raw []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id, profile_id, version, snapshot, actor_user_id, changed_at, reason
		FROM customer_profiles_history
		WHERE profile_id = $1 AND version = $2`, profileID, version).
		Scan(&h.ID, &h.ProfileID, &h.Version, &raw, &h.ActorUserID, &h.ChangedAt, &h.Reason)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrProfileNotFound
		}
		return nil, err
	}
	_ = json.Unmarshal(raw, &h.Snapshot)
	return &h, nil
}

// ============================================================================
// Customer API Keys
// ============================================================================

const apiKeySelectCols = `
	id, organization_id, label, key_prefix, key_hash,
	scopes, ip_allowlist, status, expires_at, last_used_at,
	created_by, created_at, updated_at, revoked_at
`

func scanAPIKey(row pgx.Row) (*CustomerAPIKey, error) {
	var k CustomerAPIKey
	err := row.Scan(
		&k.ID, &k.OrganizationID, &k.Label, &k.KeyPrefix, &k.KeyHash,
		&k.Scopes, &k.IPAllowlist, &k.Status, &k.ExpiresAt, &k.LastUsedAt,
		&k.CreatedBy, &k.CreatedAt, &k.UpdatedAt, &k.RevokedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAPIKeyNotFound
		}
		return nil, err
	}
	if k.Scopes == nil {
		k.Scopes = []string{}
	}
	if k.IPAllowlist == nil {
		k.IPAllowlist = []string{}
	}
	return &k, nil
}

func (r *pgRepository) GetAPIKeyByID(ctx context.Context, id uuid.UUID) (*CustomerAPIKey, error) {
	return scanAPIKey(r.pool.QueryRow(ctx, `SELECT `+apiKeySelectCols+` FROM customer_api_keys WHERE id = $1`, id))
}

func (r *pgRepository) ListAPIKeysByOrg(ctx context.Context, orgID uuid.UUID) ([]CustomerAPIKey, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+apiKeySelectCols+` FROM customer_api_keys WHERE organization_id = $1 ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CustomerAPIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, nil
}

func (r *pgRepository) CreateAPIKey(ctx context.Context, tx pgx.Tx, k *CustomerAPIKey) (*CustomerAPIKey, error) {
	if k.Scopes == nil {
		k.Scopes = []string{}
	}
	if k.IPAllowlist == nil {
		k.IPAllowlist = []string{}
	}
	q := `
		INSERT INTO customer_api_keys (
			organization_id, label, key_prefix, key_hash,
			scopes, ip_allowlist, status, expires_at, created_by
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, 'active', $7, $8
		)
		RETURNING id, status, created_at, updated_at`

	err := tx.QueryRow(ctx, q,
		k.OrganizationID, k.Label, k.KeyPrefix, k.KeyHash,
		k.Scopes, k.IPAllowlist, k.ExpiresAt, k.CreatedBy,
	).Scan(&k.ID, &k.Status, &k.CreatedAt, &k.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert customer api key: %w", err)
	}
	return k, nil
}

func (r *pgRepository) RevokeAPIKey(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*CustomerAPIKey, error) {
	now := time.Now().UTC()
	q := `
		UPDATE customer_api_keys SET
			status = 'revoked', revoked_at = $2, updated_at = $2
		WHERE id = $1
		RETURNING ` + apiKeySelectCols
	return scanAPIKey(tx.QueryRow(ctx, q, id, now))
}

// ============================================================================
// Customer SMPP Accounts
// ============================================================================

const smppSelectCols = `
	id, organization_id, system_id, password_hash, password_changed_at,
	allowed_ips, allowed_bind_types, max_binds, max_tps, status,
	created_by, created_at, updated_at
`

func scanSMPPAccount(row pgx.Row) (*CustomerSMPPAccount, error) {
	var a CustomerSMPPAccount
	err := row.Scan(
		&a.ID, &a.OrganizationID, &a.SystemID, &a.PasswordHash, &a.PasswordChangedAt,
		&a.AllowedIPs, &a.AllowedBindTypes, &a.MaxBinds, &a.MaxTPS, &a.Status,
		&a.CreatedBy, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSMPPAccountNotFound
		}
		return nil, err
	}
	if a.AllowedIPs == nil {
		a.AllowedIPs = []string{}
	}
	if a.AllowedBindTypes == nil {
		a.AllowedBindTypes = []string{"transceiver"}
	}
	return &a, nil
}

func (r *pgRepository) GetSMPPAccountByID(ctx context.Context, id uuid.UUID) (*CustomerSMPPAccount, error) {
	return scanSMPPAccount(r.pool.QueryRow(ctx, `SELECT `+smppSelectCols+` FROM customer_smpp_accounts WHERE id = $1`, id))
}

func (r *pgRepository) ListSMPPAccountsByOrg(ctx context.Context, orgID uuid.UUID) ([]CustomerSMPPAccount, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+smppSelectCols+` FROM customer_smpp_accounts WHERE organization_id = $1 ORDER BY system_id ASC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CustomerSMPPAccount
	for rows.Next() {
		a, err := scanSMPPAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, nil
}

func (r *pgRepository) CreateSMPPAccount(ctx context.Context, tx pgx.Tx, a *CustomerSMPPAccount) (*CustomerSMPPAccount, error) {
	if a.AllowedIPs == nil {
		a.AllowedIPs = []string{}
	}
	if len(a.AllowedBindTypes) == 0 {
		a.AllowedBindTypes = []string{"transceiver"}
	}
	q := `
		INSERT INTO customer_smpp_accounts (
			organization_id, system_id, password_hash,
			allowed_ips, allowed_bind_types, max_binds, max_tps, status, created_by
		) VALUES (
			$1, $2, $3,
			$4, $5, $6, $7, $8, $9
		)
		RETURNING id, password_changed_at, created_at, updated_at`

	err := tx.QueryRow(ctx, q,
		a.OrganizationID, a.SystemID, a.PasswordHash,
		a.AllowedIPs, a.AllowedBindTypes, a.MaxBinds, a.MaxTPS, a.Status, a.CreatedBy,
	).Scan(&a.ID, &a.PasswordChangedAt, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrSMPPSystemIDExists
		}
		return nil, fmt.Errorf("insert customer smpp account: %w", err)
	}
	return a, nil
}

func (r *pgRepository) UpdateSMPPAccount(ctx context.Context, tx pgx.Tx, a *CustomerSMPPAccount) (*CustomerSMPPAccount, error) {
	if a.AllowedIPs == nil {
		a.AllowedIPs = []string{}
	}
	if len(a.AllowedBindTypes) == 0 {
		a.AllowedBindTypes = []string{"transceiver"}
	}
	q := `
		UPDATE customer_smpp_accounts SET
			allowed_ips = $2, allowed_bind_types = $3,
			max_binds = $4, max_tps = $5, status = $6,
			updated_at = now()
		WHERE id = $1
		RETURNING updated_at`

	err := tx.QueryRow(ctx, q,
		a.ID, a.AllowedIPs, a.AllowedBindTypes, a.MaxBinds, a.MaxTPS, a.Status,
	).Scan(&a.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSMPPAccountNotFound
		}
		return nil, fmt.Errorf("update customer smpp account: %w", err)
	}
	return a, nil
}

func (r *pgRepository) ResetSMPPPassword(ctx context.Context, tx pgx.Tx, id uuid.UUID, passwordHash string) (*CustomerSMPPAccount, error) {
	q := `
		UPDATE customer_smpp_accounts SET
			password_hash = $2, password_changed_at = now(), updated_at = now()
		WHERE id = $1
		RETURNING ` + smppSelectCols
	return scanSMPPAccount(tx.QueryRow(ctx, q, id, passwordHash))
}
