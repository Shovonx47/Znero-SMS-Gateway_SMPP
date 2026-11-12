package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/internal/secrets"
)

type Repository interface {
	BeginTx(ctx context.Context) (pgx.Tx, error)

	// Providers
	GetProviderByID(ctx context.Context, id uuid.UUID) (*Provider, error)
	GetProviderByCode(ctx context.Context, code string) (*Provider, error)
	ListProviders(ctx context.Context, filter ProviderListFilter) ([]Provider, int, error)
	CreateProvider(ctx context.Context, tx pgx.Tx, p *Provider, reason string) (*Provider, error)
	UpdateProvider(ctx context.Context, tx pgx.Tx, p *Provider, reason string) (*Provider, error)
	ListProviderHistory(ctx context.Context, providerID uuid.UUID) ([]ProviderHistory, error)
	GetProviderHistoryVersion(ctx context.Context, providerID uuid.UUID, version int) (*ProviderHistory, error)

	// Connections
	GetConnectionByID(ctx context.Context, id uuid.UUID) (*ProviderConnection, error)
	ListConnectionsByProvider(ctx context.Context, providerID uuid.UUID) ([]ProviderConnection, error)
	CreateConnection(ctx context.Context, tx pgx.Tx, c *ProviderConnection, reason string) (*ProviderConnection, error)
	UpdateConnection(ctx context.Context, tx pgx.Tx, c *ProviderConnection, reason string) (*ProviderConnection, error)
	ListConnectionHistory(ctx context.Context, connID uuid.UUID) ([]ProviderConnectionHistory, error)
	GetConnectionHistoryVersion(ctx context.Context, connID uuid.UUID, version int) (*ProviderConnectionHistory, error)

	// Coverage
	GetCoverageByID(ctx context.Context, id uuid.UUID) (*ProviderCoverage, error)
	ListCoverage(ctx context.Context, filter CoverageListFilter) ([]ProviderCoverage, int, error)
	CreateCoverage(ctx context.Context, tx pgx.Tx, cov *ProviderCoverage, reason string) (*ProviderCoverage, error)
	UpdateCoverage(ctx context.Context, tx pgx.Tx, cov *ProviderCoverage, reason string) (*ProviderCoverage, error)
	ListCoverageHistory(ctx context.Context, covID uuid.UUID) ([]ProviderCoverageHistory, error)
	GetCoverageHistoryVersion(ctx context.Context, covID uuid.UUID, version int) (*ProviderCoverageHistory, error)
	ValidateOperatorCountry(ctx context.Context, operatorID, countryID uuid.UUID) error
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

const providerSelectCols = `
	p.id, p.name, p.code, p.provider_type, p.legal_entity_name,
	p.contact_technical, p.contact_commercial, p.contact_noc,
	p.country_id, c.iso2, c.name,
	p.timezone, p.currency_code, p.notes, p.status, p.archived,
	p.version, p.created_at, p.updated_at, p.created_by, p.updated_by
`

func scanProvider(row pgx.Row) (*Provider, error) {
	var p Provider
	err := row.Scan(
		&p.ID, &p.Name, &p.Code, &p.ProviderType, &p.LegalEntityName,
		&p.ContactTechnical, &p.ContactCommercial, &p.ContactNOC,
		&p.CountryID, &p.CountryISO2, &p.CountryName,
		&p.Timezone, &p.CurrencyCode, &p.Notes, &p.Status, &p.Archived,
		&p.Version, &p.CreatedAt, &p.UpdatedAt, &p.CreatedBy, &p.UpdatedBy,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrProviderNotFound
		}
		return nil, err
	}
	return &p, nil
}

func (r *pgRepository) GetProviderByID(ctx context.Context, id uuid.UUID) (*Provider, error) {
	q := `SELECT ` + providerSelectCols + `
		FROM providers p
		LEFT JOIN countries c ON c.id = p.country_id
		WHERE p.id = $1`
	return scanProvider(r.pool.QueryRow(ctx, q, id))
}

func (r *pgRepository) GetProviderByCode(ctx context.Context, code string) (*Provider, error) {
	q := `SELECT ` + providerSelectCols + `
		FROM providers p
		LEFT JOIN countries c ON c.id = p.country_id
		WHERE UPPER(p.code) = UPPER($1)`
	return scanProvider(r.pool.QueryRow(ctx, q, strings.TrimSpace(code)))
}

func (r *pgRepository) ListProviders(ctx context.Context, filter ProviderListFilter) ([]Provider, int, error) {
	var where []string
	var args []any
	idx := 1

	if !filter.IncludeArchived {
		where = append(where, "p.archived = false")
	}
	if filter.Search != "" {
		where = append(where, fmt.Sprintf("(p.code ILIKE $%d OR p.name ILIKE $%d OR p.legal_entity_name ILIKE $%d)", idx, idx, idx))
		args = append(args, "%"+strings.TrimSpace(filter.Search)+"%")
		idx++
	}
	if filter.ProviderType != "" {
		where = append(where, fmt.Sprintf("p.provider_type = $%d", idx))
		args = append(args, filter.ProviderType)
		idx++
	}
	if filter.CountryID != nil {
		where = append(where, fmt.Sprintf("p.country_id = $%d", idx))
		args = append(args, *filter.CountryID)
		idx++
	}
	if filter.Status != "" {
		where = append(where, fmt.Sprintf("p.status = $%d", idx))
		args = append(args, filter.Status)
		idx++
	}

	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx, "SELECT COUNT(*) FROM providers p "+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count providers: %w", err)
	}

	sortCols := map[string]string{
		"code":          "p.code",
		"name":          "p.name",
		"provider_type": "p.provider_type",
		"status":        "p.status",
		"updated_at":    "p.updated_at",
	}
	sortCol, ok := sortCols[filter.SortBy]
	if !ok {
		sortCol = "p.name"
	}
	sortDir := "ASC"
	if strings.EqualFold(filter.SortDir, "desc") {
		sortDir = "DESC"
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
		SELECT %s
		FROM providers p
		LEFT JOIN countries c ON c.id = p.country_id
		%s
		ORDER BY %s %s, p.code ASC
		LIMIT $%d OFFSET $%d`, providerSelectCols, whereSQL, sortCol, sortDir, idx, idx+1)
	args = append(args, limit, offset)

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list providers: %w", err)
	}
	defer rows.Close()

	var items []Provider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *p)
	}
	return items, total, nil
}

func (r *pgRepository) CreateProvider(ctx context.Context, tx pgx.Tx, p *Provider, reason string) (*Provider, error) {
	q := `
		INSERT INTO providers (
			name, code, provider_type, legal_entity_name,
			contact_technical, contact_commercial, contact_noc,
			country_id, timezone, currency_code, notes, status, archived,
			version, created_by, updated_by
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7,
			$8, $9, $10, $11, $12, $13,
			1, $14, $14
		)
		RETURNING id, version, created_at, updated_at`

	err := tx.QueryRow(ctx, q,
		p.Name, p.Code, p.ProviderType, p.LegalEntityName,
		p.ContactTechnical, p.ContactCommercial, p.ContactNOC,
		p.CountryID, p.Timezone, p.CurrencyCode, p.Notes, p.Status, p.Archived,
		p.CreatedBy,
	).Scan(&p.ID, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrProviderCodeExists
		}
		return nil, fmt.Errorf("insert provider: %w", err)
	}
	p.UpdatedBy = p.CreatedBy

	if err := r.insertProviderHistory(ctx, tx, p, p.CreatedBy, reason); err != nil {
		return nil, err
	}
	return p, nil
}

func (r *pgRepository) UpdateProvider(ctx context.Context, tx pgx.Tx, p *Provider, reason string) (*Provider, error) {
	q := `
		UPDATE providers SET
			name = $2, provider_type = $3, legal_entity_name = $4,
			contact_technical = $5, contact_commercial = $6, contact_noc = $7,
			country_id = $8, timezone = $9, currency_code = $10, notes = $11, status = $12, archived = $13,
			version = version + 1, updated_at = now(), updated_by = $14
		WHERE id = $1
		RETURNING version, updated_at`

	err := tx.QueryRow(ctx, q,
		p.ID, p.Name, p.ProviderType, p.LegalEntityName,
		p.ContactTechnical, p.ContactCommercial, p.ContactNOC,
		p.CountryID, p.Timezone, p.CurrencyCode, p.Notes, p.Status, p.Archived,
		p.UpdatedBy,
	).Scan(&p.Version, &p.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrProviderNotFound
		}
		return nil, fmt.Errorf("update provider: %w", err)
	}

	if err := r.insertProviderHistory(ctx, tx, p, p.UpdatedBy, reason); err != nil {
		return nil, err
	}
	return p, nil
}

func (r *pgRepository) insertProviderHistory(ctx context.Context, tx pgx.Tx, p *Provider, actorID *uuid.UUID, reason string) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO providers_history (provider_id, version, snapshot, actor_user_id, reason)
		VALUES ($1, $2, $3, $4, $5)`,
		p.ID, p.Version, raw, actorID, strings.TrimSpace(reason),
	)
	return err
}

func (r *pgRepository) ListProviderHistory(ctx context.Context, providerID uuid.UUID) ([]ProviderHistory, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, provider_id, version, snapshot, actor_user_id, changed_at, reason
		FROM providers_history
		WHERE provider_id = $1
		ORDER BY version DESC`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ProviderHistory
	for rows.Next() {
		var h ProviderHistory
		var raw []byte
		if err := rows.Scan(&h.ID, &h.ProviderID, &h.Version, &raw, &h.ActorUserID, &h.ChangedAt, &h.Reason); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &h.Snapshot)
		out = append(out, h)
	}
	return out, nil
}

func (r *pgRepository) GetProviderHistoryVersion(ctx context.Context, providerID uuid.UUID, version int) (*ProviderHistory, error) {
	var h ProviderHistory
	var raw []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id, provider_id, version, snapshot, actor_user_id, changed_at, reason
		FROM providers_history
		WHERE provider_id = $1 AND version = $2`, providerID, version).
		Scan(&h.ID, &h.ProviderID, &h.Version, &raw, &h.ActorUserID, &h.ChangedAt, &h.Reason)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrProviderNotFound
		}
		return nil, err
	}
	_ = json.Unmarshal(raw, &h.Snapshot)
	return &h, nil
}

// ============================================================================
// Provider Connections
// ============================================================================

const connSelectCols = `
	id, provider_id, name, protocol, role_notes, status, archived, max_tps,
	smpp_host, smpp_port, smpp_system_id, smpp_password_encrypted, smpp_password_changed_at,
	smpp_system_type, smpp_bind_type, smpp_tls, smpp_window_size, smpp_enquire_link_sec,
	smpp_source_ton, smpp_source_npi, smpp_dest_ton, smpp_dest_npi, smpp_charset, ip_allowlist_notes,
	http_base_url, http_auth_type, http_credentials_encrypted, http_credentials_changed_at,
	http_format_notes, http_dlr_callback_notes,
	http_config, http_dlr_mode, http_dlr_secret_encrypted, http_dlr_secret_changed_at,
	version, created_at, updated_at, created_by, updated_by
`

func scanConnection(row pgx.Row) (*ProviderConnection, error) {
	var c ProviderConnection
	var httpCfgRaw []byte
	err := row.Scan(
		&c.ID, &c.ProviderID, &c.Name, &c.Protocol, &c.RoleNotes, &c.Status, &c.Archived, &c.MaxTPS,
		&c.SMPPHost, &c.SMPPPort, &c.SMPPSystemID, &c.SMPPPasswordEncrypted, &c.SMPPPasswordChangedAt,
		&c.SMPPSystemType, &c.SMPPBindType, &c.SMPPTLS, &c.SMPPWindowSize, &c.SMPPEnquireLinkSec,
		&c.SMPPSourceTON, &c.SMPPSourceNPI, &c.SMPPDestTON, &c.SMPPDestNPI, &c.SMPPCharset, &c.IPAllowlistNotes,
		&c.HTTPBaseURL, &c.HTTPAuthType, &c.HTTPCredentialsEncrypted, &c.HTTPCredentialsChangedAt,
		&c.HTTPFormatNotes, &c.HTTPDLRCallbackNotes,
		&httpCfgRaw, &c.HTTPDLRMode, &c.HTTPDLRSecretEncrypted, &c.HTTPDLRSecretChangedAt,
		&c.Version, &c.CreatedAt, &c.UpdatedAt, &c.CreatedBy, &c.UpdatedBy,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrConnectionNotFound
		}
		return nil, err
	}
	if len(httpCfgRaw) > 0 {
		_ = json.Unmarshal(httpCfgRaw, &c.HTTPConfig)
	}
	if c.HTTPDLRMode == "" {
		c.HTTPDLRMode = "none"
	}
	c.SMPPPassword = secrets.NewSecretStatus(c.SMPPPasswordEncrypted, c.SMPPPasswordChangedAt)
	c.HTTPCredentials = secrets.NewSecretStatus(c.HTTPCredentialsEncrypted, c.HTTPCredentialsChangedAt)
	c.HTTPDLRSecret = secrets.NewSecretStatus(c.HTTPDLRSecretEncrypted, c.HTTPDLRSecretChangedAt)
	if c.Protocol == "http" {
		c.HTTPDLRCallbackPath = ProviderDLRCallbackPath(c.ID)
	}
	return &c, nil
}

func (r *pgRepository) GetConnectionByID(ctx context.Context, id uuid.UUID) (*ProviderConnection, error) {
	q := `SELECT ` + connSelectCols + ` FROM provider_connections WHERE id = $1`
	return scanConnection(r.pool.QueryRow(ctx, q, id))
}

func (r *pgRepository) ListConnectionsByProvider(ctx context.Context, providerID uuid.UUID) ([]ProviderConnection, error) {
	q := `SELECT ` + connSelectCols + ` FROM provider_connections WHERE provider_id = $1 ORDER BY name ASC`
	rows, err := r.pool.Query(ctx, q, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ProviderConnection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, nil
}

func (r *pgRepository) CreateConnection(ctx context.Context, tx pgx.Tx, c *ProviderConnection, reason string) (*ProviderConnection, error) {
	if c.HTTPDLRMode == "" {
		c.HTTPDLRMode = "none"
	}
	httpCfgJSON, err := json.Marshal(c.HTTPConfig)
	if err != nil {
		return nil, fmt.Errorf("marshal http_config: %w", err)
	}
	q := `
		INSERT INTO provider_connections (
			provider_id, name, protocol, role_notes, status, archived, max_tps,
			smpp_host, smpp_port, smpp_system_id, smpp_password_encrypted, smpp_password_changed_at,
			smpp_system_type, smpp_bind_type, smpp_tls, smpp_window_size, smpp_enquire_link_sec,
			smpp_source_ton, smpp_source_npi, smpp_dest_ton, smpp_dest_npi, smpp_charset, ip_allowlist_notes,
			http_base_url, http_auth_type, http_credentials_encrypted, http_credentials_changed_at,
			http_format_notes, http_dlr_callback_notes,
			http_config, http_dlr_mode, http_dlr_secret_encrypted, http_dlr_secret_changed_at,
			version, created_by, updated_by
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12,
			$13, $14, $15, $16, $17,
			$18, $19, $20, $21, $22, $23,
			$24, $25, $26, $27,
			$28, $29,
			$30, $31, $32, $33,
			1, $34, $34
		)
		RETURNING id, version, created_at, updated_at`

	err = tx.QueryRow(ctx, q,
		c.ProviderID, c.Name, c.Protocol, c.RoleNotes, c.Status, c.Archived, c.MaxTPS,
		c.SMPPHost, c.SMPPPort, c.SMPPSystemID, c.SMPPPasswordEncrypted, c.SMPPPasswordChangedAt,
		c.SMPPSystemType, c.SMPPBindType, c.SMPPTLS, c.SMPPWindowSize, c.SMPPEnquireLinkSec,
		c.SMPPSourceTON, c.SMPPSourceNPI, c.SMPPDestTON, c.SMPPDestNPI, c.SMPPCharset, c.IPAllowlistNotes,
		c.HTTPBaseURL, c.HTTPAuthType, c.HTTPCredentialsEncrypted, c.HTTPCredentialsChangedAt,
		c.HTTPFormatNotes, c.HTTPDLRCallbackNotes,
		httpCfgJSON, c.HTTPDLRMode, c.HTTPDLRSecretEncrypted, c.HTTPDLRSecretChangedAt,
		c.CreatedBy,
	).Scan(&c.ID, &c.Version, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrConnectionNameExists
		}
		return nil, fmt.Errorf("insert connection: %w", err)
	}
	c.UpdatedBy = c.CreatedBy
	c.SMPPPassword = secrets.NewSecretStatus(c.SMPPPasswordEncrypted, c.SMPPPasswordChangedAt)
	c.HTTPCredentials = secrets.NewSecretStatus(c.HTTPCredentialsEncrypted, c.HTTPCredentialsChangedAt)
	c.HTTPDLRSecret = secrets.NewSecretStatus(c.HTTPDLRSecretEncrypted, c.HTTPDLRSecretChangedAt)
	if c.Protocol == "http" {
		c.HTTPDLRCallbackPath = ProviderDLRCallbackPath(c.ID)
	}

	if err := r.insertConnectionHistory(ctx, tx, c, c.CreatedBy, reason); err != nil {
		return nil, err
	}
	return c, nil
}

func (r *pgRepository) UpdateConnection(ctx context.Context, tx pgx.Tx, c *ProviderConnection, reason string) (*ProviderConnection, error) {
	if c.HTTPDLRMode == "" {
		c.HTTPDLRMode = "none"
	}
	httpCfgJSON, err := json.Marshal(c.HTTPConfig)
	if err != nil {
		return nil, fmt.Errorf("marshal http_config: %w", err)
	}
	q := `
		UPDATE provider_connections SET
			name = $2, protocol = $3, role_notes = $4, status = $5, archived = $6, max_tps = $7,
			smpp_host = $8, smpp_port = $9, smpp_system_id = $10, smpp_password_encrypted = $11, smpp_password_changed_at = $12,
			smpp_system_type = $13, smpp_bind_type = $14, smpp_tls = $15, smpp_window_size = $16, smpp_enquire_link_sec = $17,
			smpp_source_ton = $18, smpp_source_npi = $19, smpp_dest_ton = $20, smpp_dest_npi = $21, smpp_charset = $22, ip_allowlist_notes = $23,
			http_base_url = $24, http_auth_type = $25, http_credentials_encrypted = $26, http_credentials_changed_at = $27,
			http_format_notes = $28, http_dlr_callback_notes = $29,
			http_config = $30, http_dlr_mode = $31, http_dlr_secret_encrypted = $32, http_dlr_secret_changed_at = $33,
			version = version + 1, updated_at = now(), updated_by = $34
		WHERE id = $1
		RETURNING version, updated_at`

	err = tx.QueryRow(ctx, q,
		c.ID, c.Name, c.Protocol, c.RoleNotes, c.Status, c.Archived, c.MaxTPS,
		c.SMPPHost, c.SMPPPort, c.SMPPSystemID, c.SMPPPasswordEncrypted, c.SMPPPasswordChangedAt,
		c.SMPPSystemType, c.SMPPBindType, c.SMPPTLS, c.SMPPWindowSize, c.SMPPEnquireLinkSec,
		c.SMPPSourceTON, c.SMPPSourceNPI, c.SMPPDestTON, c.SMPPDestNPI, c.SMPPCharset, c.IPAllowlistNotes,
		c.HTTPBaseURL, c.HTTPAuthType, c.HTTPCredentialsEncrypted, c.HTTPCredentialsChangedAt,
		c.HTTPFormatNotes, c.HTTPDLRCallbackNotes,
		httpCfgJSON, c.HTTPDLRMode, c.HTTPDLRSecretEncrypted, c.HTTPDLRSecretChangedAt,
		c.UpdatedBy,
	).Scan(&c.Version, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrConnectionNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrConnectionNameExists
		}
		return nil, fmt.Errorf("update connection: %w", err)
	}
	c.SMPPPassword = secrets.NewSecretStatus(c.SMPPPasswordEncrypted, c.SMPPPasswordChangedAt)
	c.HTTPCredentials = secrets.NewSecretStatus(c.HTTPCredentialsEncrypted, c.HTTPCredentialsChangedAt)
	c.HTTPDLRSecret = secrets.NewSecretStatus(c.HTTPDLRSecretEncrypted, c.HTTPDLRSecretChangedAt)
	if c.Protocol == "http" {
		c.HTTPDLRCallbackPath = ProviderDLRCallbackPath(c.ID)
	} else {
		c.HTTPDLRCallbackPath = ""
	}

	if err := r.insertConnectionHistory(ctx, tx, c, c.UpdatedBy, reason); err != nil {
		return nil, err
	}
	return c, nil
}

func (r *pgRepository) insertConnectionHistory(ctx context.Context, tx pgx.Tx, c *ProviderConnection, actorID *uuid.UUID, reason string) error {
	// ProviderConnection has json:"-" on SMPPPasswordEncrypted and HTTPCredentialsEncrypted,
	// so json.Marshal(c) never includes secret ciphertexts or plaintexts.
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO provider_connections_history (connection_id, version, snapshot, actor_user_id, reason)
		VALUES ($1, $2, $3, $4, $5)`,
		c.ID, c.Version, raw, actorID, strings.TrimSpace(reason),
	)
	return err
}

func (r *pgRepository) ListConnectionHistory(ctx context.Context, connID uuid.UUID) ([]ProviderConnectionHistory, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, connection_id, version, snapshot, actor_user_id, changed_at, reason
		FROM provider_connections_history
		WHERE connection_id = $1
		ORDER BY version DESC`, connID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ProviderConnectionHistory
	for rows.Next() {
		var h ProviderConnectionHistory
		var raw []byte
		if err := rows.Scan(&h.ID, &h.ConnectionID, &h.Version, &raw, &h.ActorUserID, &h.ChangedAt, &h.Reason); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &h.Snapshot)
		out = append(out, h)
	}
	return out, nil
}

func (r *pgRepository) GetConnectionHistoryVersion(ctx context.Context, connID uuid.UUID, version int) (*ProviderConnectionHistory, error) {
	var h ProviderConnectionHistory
	var raw []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id, connection_id, version, snapshot, actor_user_id, changed_at, reason
		FROM provider_connections_history
		WHERE connection_id = $1 AND version = $2`, connID, version).
		Scan(&h.ID, &h.ConnectionID, &h.Version, &raw, &h.ActorUserID, &h.ChangedAt, &h.Reason)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrConnectionNotFound
		}
		return nil, err
	}
	_ = json.Unmarshal(raw, &h.Snapshot)
	return &h, nil
}

// ============================================================================
// Provider Coverage
// ============================================================================

const coverageSelectCols = `
	cov.id, cov.provider_id, p.code, p.name,
	cov.country_id, c.iso2, c.name,
	cov.operator_id, op.name, op.mcc, op.mnc,
	cov.status, cov.notes, cov.effective_from, cov.effective_to, cov.data_source, cov.archived,
	cov.version, cov.created_at, cov.updated_at, cov.created_by, cov.updated_by
`

const coverageFromJoins = `
	FROM provider_coverage cov
	JOIN providers p ON p.id = cov.provider_id
	JOIN countries c ON c.id = cov.country_id
	LEFT JOIN operators op ON op.id = cov.operator_id
`

func scanCoverage(row pgx.Row) (*ProviderCoverage, error) {
	var cov ProviderCoverage
	err := row.Scan(
		&cov.ID, &cov.ProviderID, &cov.ProviderCode, &cov.ProviderName,
		&cov.CountryID, &cov.CountryISO2, &cov.CountryName,
		&cov.OperatorID, &cov.OperatorName, &cov.OperatorMCC, &cov.OperatorMNC,
		&cov.Status, &cov.Notes, &cov.EffectiveFrom, &cov.EffectiveTo, &cov.DataSource, &cov.Archived,
		&cov.Version, &cov.CreatedAt, &cov.UpdatedAt, &cov.CreatedBy, &cov.UpdatedBy,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCoverageNotFound
		}
		return nil, err
	}
	return &cov, nil
}

func (r *pgRepository) GetCoverageByID(ctx context.Context, id uuid.UUID) (*ProviderCoverage, error) {
	q := `SELECT ` + coverageSelectCols + coverageFromJoins + ` WHERE cov.id = $1`
	return scanCoverage(r.pool.QueryRow(ctx, q, id))
}

func (r *pgRepository) ListCoverage(ctx context.Context, filter CoverageListFilter) ([]ProviderCoverage, int, error) {
	var where []string
	var args []any
	idx := 1

	if !filter.IncludeArchived {
		where = append(where, "cov.archived = false")
	}
	if filter.ProviderID != nil {
		where = append(where, fmt.Sprintf("cov.provider_id = $%d", idx))
		args = append(args, *filter.ProviderID)
		idx++
	}
	if filter.CountryID != nil {
		where = append(where, fmt.Sprintf("cov.country_id = $%d", idx))
		args = append(args, *filter.CountryID)
		idx++
	}
	if filter.OperatorID != nil {
		where = append(where, fmt.Sprintf("cov.operator_id = $%d", idx))
		args = append(args, *filter.OperatorID)
		idx++
	}
	if filter.Status != "" {
		where = append(where, fmt.Sprintf("cov.status = $%d", idx))
		args = append(args, filter.Status)
		idx++
	}
	if filter.Search != "" {
		where = append(where, fmt.Sprintf("(p.code ILIKE $%d OR p.name ILIKE $%d OR c.iso2 ILIKE $%d OR c.name ILIKE $%d OR COALESCE(op.name, '') ILIKE $%d)", idx, idx, idx, idx, idx))
		args = append(args, "%"+strings.TrimSpace(filter.Search)+"%")
		idx++
	}

	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx, "SELECT COUNT(*) "+coverageFromJoins+" "+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count coverage: %w", err)
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
		ORDER BY c.iso2 ASC, COALESCE(op.name, '') ASC, p.code ASC
		LIMIT $%d OFFSET $%d`, coverageSelectCols, coverageFromJoins, whereSQL, idx, idx+1)
	args = append(args, limit, offset)

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list coverage: %w", err)
	}
	defer rows.Close()

	var items []ProviderCoverage
	for rows.Next() {
		cov, err := scanCoverage(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *cov)
	}
	return items, total, nil
}

func (r *pgRepository) ValidateOperatorCountry(ctx context.Context, operatorID, countryID uuid.UUID) error {
	var opCountryID uuid.UUID
	err := r.pool.QueryRow(ctx, `SELECT country_id FROM operators WHERE id = $1`, operatorID).Scan(&opCountryID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: operator not found", ErrValidation)
		}
		return err
	}
	if opCountryID != countryID {
		return ErrOperatorCountryMismatch
	}
	return nil
}

func (r *pgRepository) CreateCoverage(ctx context.Context, tx pgx.Tx, cov *ProviderCoverage, reason string) (*ProviderCoverage, error) {
	q := `
		INSERT INTO provider_coverage (
			provider_id, country_id, operator_id,
			status, notes, effective_from, effective_to, data_source, archived,
			version, created_by, updated_by
		) VALUES (
			$1, $2, $3,
			$4, $5, $6, $7, $8, $9,
			1, $10, $10
		)
		RETURNING id, version, created_at, updated_at`

	err := tx.QueryRow(ctx, q,
		cov.ProviderID, cov.CountryID, cov.OperatorID,
		cov.Status, cov.Notes, cov.EffectiveFrom, cov.EffectiveTo, cov.DataSource, cov.Archived,
		cov.CreatedBy,
	).Scan(&cov.ID, &cov.Version, &cov.CreatedAt, &cov.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrCoverageExists
		}
		return nil, fmt.Errorf("insert coverage: %w", err)
	}
	cov.UpdatedBy = cov.CreatedBy

	if err := r.insertCoverageHistory(ctx, tx, cov, cov.CreatedBy, reason); err != nil {
		return nil, err
	}
	return cov, nil
}

func (r *pgRepository) UpdateCoverage(ctx context.Context, tx pgx.Tx, cov *ProviderCoverage, reason string) (*ProviderCoverage, error) {
	q := `
		UPDATE provider_coverage SET
			status = $2, notes = $3, effective_from = $4, effective_to = $5, data_source = $6, archived = $7,
			version = version + 1, updated_at = now(), updated_by = $8
		WHERE id = $1
		RETURNING version, updated_at`

	err := tx.QueryRow(ctx, q,
		cov.ID, cov.Status, cov.Notes, cov.EffectiveFrom, cov.EffectiveTo, cov.DataSource, cov.Archived,
		cov.UpdatedBy,
	).Scan(&cov.Version, &cov.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCoverageNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrCoverageExists
		}
		return nil, fmt.Errorf("update coverage: %w", err)
	}

	if err := r.insertCoverageHistory(ctx, tx, cov, cov.UpdatedBy, reason); err != nil {
		return nil, err
	}
	return cov, nil
}

func (r *pgRepository) insertCoverageHistory(ctx context.Context, tx pgx.Tx, cov *ProviderCoverage, actorID *uuid.UUID, reason string) error {
	raw, err := json.Marshal(cov)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO provider_coverage_history (coverage_id, version, snapshot, actor_user_id, reason)
		VALUES ($1, $2, $3, $4, $5)`,
		cov.ID, cov.Version, raw, actorID, strings.TrimSpace(reason),
	)
	return err
}

func (r *pgRepository) ListCoverageHistory(ctx context.Context, covID uuid.UUID) ([]ProviderCoverageHistory, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, coverage_id, version, snapshot, actor_user_id, changed_at, reason
		FROM provider_coverage_history
		WHERE coverage_id = $1
		ORDER BY version DESC`, covID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ProviderCoverageHistory
	for rows.Next() {
		var h ProviderCoverageHistory
		var raw []byte
		if err := rows.Scan(&h.ID, &h.CoverageID, &h.Version, &raw, &h.ActorUserID, &h.ChangedAt, &h.Reason); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &h.Snapshot)
		out = append(out, h)
	}
	return out, nil
}

func (r *pgRepository) GetCoverageHistoryVersion(ctx context.Context, covID uuid.UUID, version int) (*ProviderCoverageHistory, error) {
	var h ProviderCoverageHistory
	var raw []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id, coverage_id, version, snapshot, actor_user_id, changed_at, reason
		FROM provider_coverage_history
		WHERE coverage_id = $1 AND version = $2`, covID, version).
		Scan(&h.ID, &h.CoverageID, &h.Version, &raw, &h.ActorUserID, &h.ChangedAt, &h.Reason)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCoverageNotFound
		}
		return nil, err
	}
	_ = json.Unmarshal(raw, &h.Snapshot)
	return &h, nil
}
