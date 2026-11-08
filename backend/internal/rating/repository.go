package rating

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
	"github.com/shopspring/decimal"
)

type Repository interface {
	BeginTx(ctx context.Context) (pgx.Tx, error)

	// Rate Cards
	CreateCard(ctx context.Context, tx pgx.Tx, card *RateCard, reason string) (*RateCard, error)
	UpdateCard(ctx context.Context, tx pgx.Tx, card *RateCard, reason string) (*RateCard, error)
	GetCardByID(ctx context.Context, id uuid.UUID) (*RateCard, error)
	ListCards(ctx context.Context, filter RateCardListFilter) ([]RateCard, int, error)
	ListCardHistory(ctx context.Context, cardID uuid.UUID) ([]RateCardHistory, error)
	GetCardHistoryVersion(ctx context.Context, cardID uuid.UUID, version int) (*RateCardHistory, error)

	// Rates
	CreateRate(ctx context.Context, tx pgx.Tx, rate *Rate, reason string) (*Rate, error)
	CreateRatesBatch(ctx context.Context, tx pgx.Tx, rates []*Rate, reason string) ([]*Rate, error)
	ClosePreviousOpenEndedRatesBatch(ctx context.Context, tx pgx.Tx, rates []*Rate, updatedBy *uuid.UUID, reason string) error
	RollbackImportedRates(ctx context.Context, tx pgx.Tx, rateIDs []uuid.UUID, updatedBy *uuid.UUID, reason string) error
	UpdateRate(ctx context.Context, tx pgx.Tx, rate *Rate, reason string) (*Rate, error)
	ClosePreviousOpenEndedRate(ctx context.Context, tx pgx.Tx, cardID, countryID uuid.UUID, operatorID *uuid.UUID, msgType string, effectiveFrom time.Time, updatedBy *uuid.UUID) (*Rate, error)
	GetRateByID(ctx context.Context, id uuid.UUID) (*Rate, error)
	ListRates(ctx context.Context, filter RateListFilter) ([]Rate, int, error)
	ListActiveRatesByCard(ctx context.Context, cardID uuid.UUID) ([]Rate, error)
	ListRateHistory(ctx context.Context, rateID uuid.UUID) ([]RateHistory, error)
	GetRateHistoryVersion(ctx context.Context, rateID uuid.UUID, version int) (*RateHistory, error)
	GetActiveRateAt(ctx context.Context, cardID, countryID uuid.UUID, operatorID *uuid.UUID, msgType string, at time.Time) (*Rate, error)
	ListUpcomingRates(ctx context.Context, days int) ([]Rate, error)

	// Customer Rate Assignments
	CreateCustomerAssignment(ctx context.Context, tx pgx.Tx, a *CustomerRateAssignment, reason string) (*CustomerRateAssignment, error)
	UpdateCustomerAssignment(ctx context.Context, tx pgx.Tx, a *CustomerRateAssignment, reason string) (*CustomerRateAssignment, error)
	GetCustomerAssignmentByID(ctx context.Context, id uuid.UUID) (*CustomerRateAssignment, error)
	GetActiveCustomerAssignmentAt(ctx context.Context, customerOrgID uuid.UUID, at time.Time) (*CustomerRateAssignment, error)
	ListCustomerAssignments(ctx context.Context, customerOrgID *uuid.UUID) ([]CustomerRateAssignment, error)
	ListCustomerAssignmentHistory(ctx context.Context, assignmentID uuid.UUID) ([]CustomerRateAssignmentHistory, error)
	GetCustomerAssignmentHistoryVersion(ctx context.Context, assignmentID uuid.UUID, version int) (*CustomerRateAssignmentHistory, error)

	// Reseller Cost Assignments
	CreateResellerCostAssignment(ctx context.Context, tx pgx.Tx, a *ResellerCostAssignment, reason string) (*ResellerCostAssignment, error)
	UpdateResellerCostAssignment(ctx context.Context, tx pgx.Tx, a *ResellerCostAssignment, reason string) (*ResellerCostAssignment, error)
	GetResellerCostAssignmentByID(ctx context.Context, id uuid.UUID) (*ResellerCostAssignment, error)
	GetActiveResellerCostAssignmentAt(ctx context.Context, resellerOrgID uuid.UUID, at time.Time) (*ResellerCostAssignment, error)
	ListResellerCostAssignments(ctx context.Context, resellerOrgID *uuid.UUID) ([]ResellerCostAssignment, error)
	ListResellerCostAssignmentHistory(ctx context.Context, assignmentID uuid.UUID) ([]ResellerCostAssignmentHistory, error)
	GetResellerCostAssignmentHistoryVersion(ctx context.Context, assignmentID uuid.UUID, version int) (*ResellerCostAssignmentHistory, error)

	// Master Data & Intelligence Lookups
	ValidateCurrencyExists(ctx context.Context, code string) (bool, error)
	ValidateCountryAndOperator(ctx context.Context, countryID uuid.UUID, operatorID *uuid.UUID) error
	GetLowestActiveBuyRate(ctx context.Context, countryID uuid.UUID, operatorID *uuid.UUID, msgType string, at time.Time, currency string) (*decimal.Decimal, int, error)
	InspectBuyCoverageAndRates(ctx context.Context, countryID uuid.UUID, operatorID *uuid.UUID, msgType string, at time.Time, currency string) (lowestSameCcy *decimal.Decimal, sameCcyProvs int, activeCovProvs int, otherCcyProvs int, otherCurrencies []string, err error)
	ListLowestActiveBuyRatesByCurrency(ctx context.Context, currency string, at time.Time) (map[string]decimal.Decimal, error)
	GetResellerCostRateAt(ctx context.Context, resellerOrgID, countryID uuid.UUID, operatorID *uuid.UUID, msgType string, at time.Time) (*decimal.Decimal, error)
	GetResellerCostRateWithCurrencyAt(ctx context.Context, resellerOrgID, countryID uuid.UUID, operatorID *uuid.UUID, msgType string, at time.Time) (*decimal.Decimal, string, error)
	GenerateMarginReport(ctx context.Context, filter MarginReportFilter) ([]MarginReportItem, int, error)

	// Bulk in-memory snapshot loaders
	LoadAllActiveRates(ctx context.Context) ([]Rate, []RateCard, []CustomerRateAssignment, error)
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

func (r *pgRepository) q(tx pgx.Tx) interface {
	Exec(ctx context.Context, sql string, arguments ...any) (commandTag pgconn.CommandTag, err error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
} {
	if tx != nil {
		return tx
	}
	return r.pool
}

// -----------------------------------------------------------------------------
// RATE CARDS
// -----------------------------------------------------------------------------

func (r *pgRepository) CreateCard(ctx context.Context, tx pgx.Tx, card *RateCard, reason string) (*RateCard, error) {
	if card.ID == uuid.Nil {
		card.ID = uuid.New()
	}
	if card.Channel == "" {
		card.Channel = "sms"
	}
	if card.Status == "" {
		card.Status = "draft"
	}
	card.Version = 1
	now := time.Now().UTC()
	card.CreatedAt = now
	card.UpdatedAt = now

	q := r.q(tx)
	query := `
		INSERT INTO rate_cards (
			id, name, kind, owner_org_id, provider_id, currency_code,
			channel, status, description, archived, version, created_by, updated_by, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		RETURNING id, name, kind, owner_org_id, provider_id, currency_code,
		          channel, status, description, archived, version, created_by, updated_by, created_at, updated_at
	`
	var res RateCard
	err := q.QueryRow(ctx, query,
		card.ID, card.Name, card.Kind, card.OwnerOrgID, card.ProviderID, card.CurrencyCode,
		card.Channel, card.Status, card.Description, card.Archived, card.Version,
		card.CreatedBy, card.UpdatedBy, card.CreatedAt, card.UpdatedAt,
	).Scan(
		&res.ID, &res.Name, &res.Kind, &res.OwnerOrgID, &res.ProviderID, &res.CurrencyCode,
		&res.Channel, &res.Status, &res.Description, &res.Archived, &res.Version,
		&res.CreatedBy, &res.UpdatedBy, &res.CreatedAt, &res.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create rate card: %w", err)
	}

	// Record snapshot in history
	snap, _ := json.Marshal(res)
	histQuery := `
		INSERT INTO rate_cards_history (
			card_id, name, kind, owner_org_id, provider_id, currency_code,
			channel, status, description, archived, version, snapshot,
			changed_by, changed_at, change_reason
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
	`
	_, err = q.Exec(ctx, histQuery,
		res.ID, res.Name, res.Kind, res.OwnerOrgID, res.ProviderID, res.CurrencyCode,
		res.Channel, res.Status, res.Description, res.Archived, res.Version, snap,
		res.CreatedBy, now, reason,
	)
	if err != nil {
		return nil, fmt.Errorf("record rate card history: %w", err)
	}

	return &res, nil
}

func (r *pgRepository) UpdateCard(ctx context.Context, tx pgx.Tx, card *RateCard, reason string) (*RateCard, error) {
	q := r.q(tx)
	now := time.Now().UTC()
	query := `
		UPDATE rate_cards
		SET name = $1,
		    currency_code = $2,
		    status = $3,
		    description = $4,
		    archived = $5,
		    version = version + 1,
		    updated_by = $6,
		    updated_at = $7
		WHERE id = $8 AND version = $9
		RETURNING id, name, kind, owner_org_id, provider_id, currency_code,
		          channel, status, description, archived, version, created_by, updated_by, created_at, updated_at
	`
	var res RateCard
	err := q.QueryRow(ctx, query,
		card.Name, card.CurrencyCode, card.Status, card.Description, card.Archived,
		card.UpdatedBy, now, card.ID, card.Version,
	).Scan(
		&res.ID, &res.Name, &res.Kind, &res.OwnerOrgID, &res.ProviderID, &res.CurrencyCode,
		&res.Channel, &res.Status, &res.Description, &res.Archived, &res.Version,
		&res.CreatedBy, &res.UpdatedBy, &res.CreatedAt, &res.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("rate card update failed (conflict or not found)")
		}
		return nil, fmt.Errorf("update rate card: %w", err)
	}

	snap, _ := json.Marshal(res)
	histQuery := `
		INSERT INTO rate_cards_history (
			card_id, name, kind, owner_org_id, provider_id, currency_code,
			channel, status, description, archived, version, snapshot,
			changed_by, changed_at, change_reason
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
	`
	_, err = q.Exec(ctx, histQuery,
		res.ID, res.Name, res.Kind, res.OwnerOrgID, res.ProviderID, res.CurrencyCode,
		res.Channel, res.Status, res.Description, res.Archived, res.Version, snap,
		res.UpdatedBy, now, reason,
	)
	if err != nil {
		return nil, fmt.Errorf("record rate card history: %w", err)
	}

	return &res, nil
}

func (r *pgRepository) GetCardByID(ctx context.Context, id uuid.UUID) (*RateCard, error) {
	query := `
		SELECT rc.id, rc.name, rc.kind, rc.owner_org_id, COALESCE(o.name, '') AS owner_org_name,
		       rc.provider_id, p.name AS provider_name, rc.currency_code, rc.channel,
		       rc.status, rc.description, rc.archived, rc.version, rc.created_by, rc.updated_by,
		       rc.created_at, rc.updated_at
		FROM rate_cards rc
		JOIN organizations o ON o.id = rc.owner_org_id
		LEFT JOIN providers p ON p.id = rc.provider_id
		WHERE rc.id = $1
	`
	var c RateCard
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&c.ID, &c.Name, &c.Kind, &c.OwnerOrgID, &c.OwnerOrgName,
		&c.ProviderID, &c.ProviderName, &c.CurrencyCode, &c.Channel,
		&c.Status, &c.Description, &c.Archived, &c.Version, &c.CreatedBy, &c.UpdatedBy,
		&c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRateCardNotFound
		}
		return nil, fmt.Errorf("get rate card by id: %w", err)
	}
	return &c, nil
}

func (r *pgRepository) ListCards(ctx context.Context, filter RateCardListFilter) ([]RateCard, int, error) {
	var conditions []string
	var args []any
	argIdx := 1

	if filter.Kind != "" {
		conditions = append(conditions, fmt.Sprintf("rc.kind = $%d", argIdx))
		args = append(args, filter.Kind)
		argIdx++
	}
	if filter.OwnerOrgID != nil {
		conditions = append(conditions, fmt.Sprintf("rc.owner_org_id = $%d", argIdx))
		args = append(args, *filter.OwnerOrgID)
		argIdx++
	}
	if filter.ProviderID != nil {
		conditions = append(conditions, fmt.Sprintf("rc.provider_id = $%d", argIdx))
		args = append(args, *filter.ProviderID)
		argIdx++
	}
	if filter.CurrencyCode != "" {
		conditions = append(conditions, fmt.Sprintf("rc.currency_code = $%d", argIdx))
		args = append(args, strings.ToUpper(filter.CurrencyCode))
		argIdx++
	}
	if filter.Status != "" {
		conditions = append(conditions, fmt.Sprintf("rc.status = $%d", argIdx))
		args = append(args, filter.Status)
		argIdx++
	}
	if filter.Search != "" {
		conditions = append(conditions, fmt.Sprintf("(rc.name ILIKE $%d OR rc.description ILIKE $%d)", argIdx, argIdx))
		args = append(args, "%"+filter.Search+"%")
		argIdx++
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM rate_cards rc %s", whereClause)
	var total int
	err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("count rate cards: %w", err)
	}

	page := filter.Page
	if page < 1 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize < 1 || pageSize > 500 {
		pageSize = 50
	}
	offset := (page - 1) * pageSize

	sortBy := "rc.created_at"
	sortOrder := "DESC"
	if filter.SortBy == "name" {
		sortBy = "rc.name"
	} else if filter.SortBy == "currency" {
		sortBy = "rc.currency_code"
	}
	if strings.ToUpper(filter.SortOrder) == "ASC" {
		sortOrder = "ASC"
	}

	query := fmt.Sprintf(`
		SELECT rc.id, rc.name, rc.kind, rc.owner_org_id, COALESCE(o.name, '') AS owner_org_name,
		       rc.provider_id, p.name AS provider_name, rc.currency_code, rc.channel,
		       rc.status, rc.description, rc.archived, rc.version, rc.created_by, rc.updated_by,
		       rc.created_at, rc.updated_at
		FROM rate_cards rc
		JOIN organizations o ON o.id = rc.owner_org_id
		LEFT JOIN providers p ON p.id = rc.provider_id
		%s
		ORDER BY %s %s
		LIMIT $%d OFFSET $%d
	`, whereClause, sortBy, sortOrder, argIdx, argIdx+1)

	args = append(args, pageSize, offset)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list rate cards: %w", err)
	}
	defer rows.Close()

	var cards []RateCard
	for rows.Next() {
		var c RateCard
		err := rows.Scan(
			&c.ID, &c.Name, &c.Kind, &c.OwnerOrgID, &c.OwnerOrgName,
			&c.ProviderID, &c.ProviderName, &c.CurrencyCode, &c.Channel,
			&c.Status, &c.Description, &c.Archived, &c.Version, &c.CreatedBy, &c.UpdatedBy,
			&c.CreatedAt, &c.UpdatedAt,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("scan rate card row: %w", err)
		}
		cards = append(cards, c)
	}
	return cards, total, nil
}

func (r *pgRepository) ListCardHistory(ctx context.Context, cardID uuid.UUID) ([]RateCardHistory, error) {
	query := `
		SELECT id, card_id, name, kind, owner_org_id, provider_id, currency_code,
		       channel, status, description, archived, version, snapshot,
		       changed_by, changed_at, change_reason
		FROM rate_cards_history
		WHERE card_id = $1
		ORDER BY version DESC
	`
	rows, err := r.pool.Query(ctx, query, cardID)
	if err != nil {
		return nil, fmt.Errorf("list rate card history: %w", err)
	}
	defer rows.Close()

	var items []RateCardHistory
	for rows.Next() {
		var h RateCardHistory
		err := rows.Scan(
			&h.ID, &h.CardID, &h.Name, &h.Kind, &h.OwnerOrgID, &h.ProviderID, &h.CurrencyCode,
			&h.Channel, &h.Status, &h.Description, &h.Archived, &h.Version, &h.Snapshot,
			&h.ChangedBy, &h.ChangedAt, &h.ChangeReason,
		)
		if err != nil {
			return nil, fmt.Errorf("scan rate card history: %w", err)
		}
		items = append(items, h)
	}
	return items, nil
}

func (r *pgRepository) GetCardHistoryVersion(ctx context.Context, cardID uuid.UUID, version int) (*RateCardHistory, error) {
	query := `
		SELECT id, card_id, name, kind, owner_org_id, provider_id, currency_code,
		       channel, status, description, archived, version, snapshot,
		       changed_by, changed_at, change_reason
		FROM rate_cards_history
		WHERE card_id = $1 AND version = $2
	`
	var h RateCardHistory
	err := r.pool.QueryRow(ctx, query, cardID, version).Scan(
		&h.ID, &h.CardID, &h.Name, &h.Kind, &h.OwnerOrgID, &h.ProviderID, &h.CurrencyCode,
		&h.Channel, &h.Status, &h.Description, &h.Archived, &h.Version, &h.Snapshot,
		&h.ChangedBy, &h.ChangedAt, &h.ChangeReason,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("rate card history version not found")
		}
		return nil, fmt.Errorf("get rate card history version: %w", err)
	}
	return &h, nil
}

// -----------------------------------------------------------------------------
// RATES
// -----------------------------------------------------------------------------

func (r *pgRepository) CreateRate(ctx context.Context, tx pgx.Tx, rate *Rate, reason string) (*Rate, error) {
	if rate.ID == uuid.Nil {
		rate.ID = uuid.New()
	}
	if rate.MessageType == "" {
		rate.MessageType = "default"
	}
	if rate.Source == "" {
		rate.Source = "manual"
	}
	if rate.Status == "" {
		rate.Status = "active"
	}
	rate.Price = TruncateMoney(rate.Price)
	rate.Version = 1
	now := time.Now().UTC()
	rate.CreatedAt = now
	rate.UpdatedAt = now

	q := r.q(tx)
	query := `
		INSERT INTO rates (
			id, card_id, country_id, operator_id, message_type, price,
			min_billing_note, effective_from, effective_to, source, status,
			version, created_by, updated_by, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		RETURNING id, card_id, country_id, operator_id, message_type, price,
		          min_billing_note, effective_from, effective_to, source, status,
		          version, created_by, updated_by, created_at, updated_at
	`
	var res Rate
	err := q.QueryRow(ctx, query,
		rate.ID, rate.CardID, rate.CountryID, rate.OperatorID, rate.MessageType, rate.Price,
		rate.MinBillingNote, rate.EffectiveFrom, rate.EffectiveTo, rate.Source, rate.Status,
		rate.Version, rate.CreatedBy, rate.UpdatedBy, rate.CreatedAt, rate.UpdatedAt,
	).Scan(
		&res.ID, &res.CardID, &res.CountryID, &res.OperatorID, &res.MessageType, &res.Price,
		&res.MinBillingNote, &res.EffectiveFrom, &res.EffectiveTo, &res.Source, &res.Status,
		&res.Version, &res.CreatedBy, &res.UpdatedBy, &res.CreatedAt, &res.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23P01" { // exclusion_violation
			return nil, ErrEffectiveDateOverlap
		}
		return nil, fmt.Errorf("create rate: %w", err)
	}

	snap, _ := json.Marshal(res)
	histQuery := `
		INSERT INTO rates_history (
			rate_id, card_id, country_id, operator_id, message_type, price,
			min_billing_note, effective_from, effective_to, source, status,
			version, snapshot, changed_by, changed_at, change_reason
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
	`
	_, err = q.Exec(ctx, histQuery,
		res.ID, res.CardID, res.CountryID, res.OperatorID, res.MessageType, res.Price,
		res.MinBillingNote, res.EffectiveFrom, res.EffectiveTo, res.Source, res.Status,
		res.Version, snap, res.CreatedBy, now, reason,
	)
	if err != nil {
		return nil, fmt.Errorf("record rate history: %w", err)
	}

	return &res, nil
}

func (r *pgRepository) CreateRatesBatch(ctx context.Context, tx pgx.Tx, rates []*Rate, reason string) ([]*Rate, error) {
	if len(rates) == 0 {
		return nil, nil
	}
	q := r.q(tx)
	now := time.Now().UTC()

	n := len(rates)
	ids := make([]uuid.UUID, n)
	cardIDs := make([]uuid.UUID, n)
	countryIDs := make([]uuid.UUID, n)
	operatorIDs := make([]*string, n)
	msgTypes := make([]string, n)
	priceStrs := make([]string, n)
	minNotes := make([]string, n)
	effFroms := make([]time.Time, n)
	effTos := make([]*string, n)
	sources := make([]string, n)
	statuses := make([]string, n)

	var actorID *uuid.UUID
	for i, rate := range rates {
		if rate.ID == uuid.Nil {
			rate.ID = uuid.New()
		}
		if rate.MessageType == "" || rate.MessageType == "sms" {
			rate.MessageType = "default"
		}
		if rate.Source == "" {
			rate.Source = "import"
		}
		if rate.Status == "" {
			rate.Status = "active"
		}
		rate.Price = TruncateMoney(rate.Price)
		rate.EffectiveFrom = rate.EffectiveFrom.UTC()
		if rate.EffectiveTo != nil {
			utc := rate.EffectiveTo.UTC()
			rate.EffectiveTo = &utc
			s := utc.Format(time.RFC3339Nano)
			effTos[i] = &s
		}
		rate.Version = 1
		rate.CreatedAt = now
		rate.UpdatedAt = now
		if actorID == nil && rate.CreatedBy != nil {
			actorID = rate.CreatedBy
		}

		ids[i] = rate.ID
		cardIDs[i] = rate.CardID
		countryIDs[i] = rate.CountryID
		if rate.OperatorID != nil {
			s := rate.OperatorID.String()
			operatorIDs[i] = &s
		}
		msgTypes[i] = rate.MessageType
		priceStrs[i] = rate.Price.StringFixed(6)
		minNotes[i] = rate.MinBillingNote
		effFroms[i] = rate.EffectiveFrom
		sources[i] = rate.Source
		statuses[i] = rate.Status
	}

	_, err := q.Exec(ctx, `
		INSERT INTO rates (
			id, card_id, country_id, operator_id, message_type, price,
			min_billing_note, effective_from, effective_to, source, status,
			version, created_by, updated_by, created_at, updated_at
		)
		SELECT
			u.id, u.card_id, u.country_id, u.operator_id_str::uuid, u.message_type, u.price::numeric(18,6),
			u.min_billing_note, u.effective_from, u.effective_to_str::timestamptz, u.source, u.status,
			1, $12, $12, $13, $13
		FROM UNNEST(
			$1::uuid[], $2::uuid[], $3::uuid[], $4::text[], $5::text[],
			$6::text[], $7::text[], $8::timestamptz[], $9::text[],
			$10::text[], $11::text[]
		) AS u(id, card_id, country_id, operator_id_str, message_type, price, min_billing_note, effective_from, effective_to_str, source, status)
	`, ids, cardIDs, countryIDs, operatorIDs, msgTypes, priceStrs, minNotes, effFroms, effTos, sources, statuses, actorID, now)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23P01" {
			return nil, ErrEffectiveDateOverlap
		}
		return nil, fmt.Errorf("batch insert rates: %w", err)
	}

	_, err = q.Exec(ctx, `
		INSERT INTO rates_history (
			rate_id, card_id, country_id, operator_id, message_type, price,
			min_billing_note, effective_from, effective_to, source, status,
			version, snapshot, changed_by, changed_at, change_reason
		)
		SELECT
			r.id, r.card_id, r.country_id, r.operator_id, r.message_type, r.price,
			r.min_billing_note, r.effective_from, r.effective_to, r.source, r.status,
			r.version, to_jsonb(r), $2, $3, $4
		FROM rates r
		WHERE r.id = ANY($1::uuid[])
	`, ids, actorID, now, reason)
	if err != nil {
		return nil, fmt.Errorf("batch insert rate history: %w", err)
	}

	return rates, nil
}

func (r *pgRepository) ClosePreviousOpenEndedRatesBatch(ctx context.Context, tx pgx.Tx, rates []*Rate, updatedBy *uuid.UUID, reason string) error {
	// Only open-ended incoming rates (effective_to == nil) close prior open-ended rates
	var toClose []*Rate
	for _, rt := range rates {
		if rt.EffectiveTo == nil {
			toClose = append(toClose, rt)
		}
	}
	if len(toClose) == 0 {
		return nil
	}
	if reason == "" {
		reason = "auto-closed by incoming new rate"
	}
	q := r.q(tx)
	now := time.Now().UTC()

	n := len(toClose)
	cardIDs := make([]uuid.UUID, n)
	countryIDs := make([]uuid.UUID, n)
	operatorIDs := make([]*string, n)
	msgTypes := make([]string, n)
	effFroms := make([]time.Time, n)
	for i, rt := range toClose {
		cardIDs[i] = rt.CardID
		countryIDs[i] = rt.CountryID
		if rt.OperatorID != nil {
			s := rt.OperatorID.String()
			operatorIDs[i] = &s
		}
		msgType := rt.MessageType
		if msgType == "" || msgType == "sms" {
			msgType = "default"
		}
		msgTypes[i] = msgType
		effFroms[i] = rt.EffectiveFrom.UTC()
	}

	_, err := q.Exec(ctx, `
		WITH closed AS (
			UPDATE rates r
			SET effective_to = u.eff_from,
			    version = r.version + 1,
			    updated_by = $6,
			    updated_at = $7
			FROM UNNEST($1::uuid[], $2::uuid[], $3::text[], $4::text[], $5::timestamptz[])
			     AS u(card_id, country_id, operator_id_str, msg_type, eff_from)
			WHERE r.card_id = u.card_id
			  AND r.country_id = u.country_id
			  AND r.message_type = u.msg_type
			  AND ((r.operator_id IS NULL AND u.operator_id_str IS NULL) OR r.operator_id = u.operator_id_str::uuid)
			  AND r.status = 'active'
			  AND r.effective_from < u.eff_from
			  AND (r.effective_to IS NULL OR r.effective_to > u.eff_from)
			RETURNING r.*
		)
		INSERT INTO rates_history (
			rate_id, card_id, country_id, operator_id, message_type, price,
			min_billing_note, effective_from, effective_to, source, status,
			version, snapshot, changed_by, changed_at, change_reason
		)
		SELECT
			c.id, c.card_id, c.country_id, c.operator_id, c.message_type, c.price,
			c.min_billing_note, c.effective_from, c.effective_to, c.source, c.status,
			c.version, to_jsonb(c), $6, $7, $8
		FROM closed c
	`, cardIDs, countryIDs, operatorIDs, msgTypes, effFroms, updatedBy, now, reason)
	return err
}

func (r *pgRepository) RollbackImportedRates(ctx context.Context, tx pgx.Tx, rateIDs []uuid.UUID, updatedBy *uuid.UUID, reason string) error {
	if len(rateIDs) == 0 {
		return nil
	}
	q := r.q(tx)
	now := time.Now().UTC()

	const batchSize = 5000

	// 1. Deactivate ALL imported rates across chunks first
	for i := 0; i < len(rateIDs); i += batchSize {
		end := i + batchSize
		if end > len(rateIDs) {
			end = len(rateIDs)
		}
		chunk := rateIDs[i:end]

		_, err := q.Exec(ctx, `
			WITH deactivated AS (
				UPDATE rates r
				SET status = 'inactive',
				    version = r.version + 1,
				    updated_by = $2,
				    updated_at = $3
				WHERE r.id = ANY($1::uuid[])
				  AND r.status = 'active'
				RETURNING r.*
			)
			INSERT INTO rates_history (
				rate_id, card_id, country_id, operator_id, message_type, price,
				min_billing_note, effective_from, effective_to, source, status,
				version, snapshot, changed_by, changed_at, change_reason
			)
			SELECT
				d.id, d.card_id, d.country_id, d.operator_id, d.message_type, d.price,
				d.min_billing_note, d.effective_from, d.effective_to, d.source, d.status,
				d.version, to_jsonb(d), $2, $3, $4
			FROM deactivated d
		`, chunk, updatedBy, now, reason)
		if err != nil {
			return fmt.Errorf("rollback imported rates deactivate: %w", err)
		}
	}

	// 2. Re-open any predecessor rate that was auto-closed specifically by this import job
	_, err := q.Exec(ctx, `
		WITH reopened AS (
			UPDATE rates prev
			SET effective_to = NULL,
			    version = prev.version + 1,
			    updated_by = $1,
			    updated_at = $2
			FROM rates_history h
			WHERE h.change_reason = $3
			  AND h.rate_id = prev.id
			  AND prev.status = 'active'
			  AND prev.effective_to IS NOT NULL
			  AND prev.effective_to = (h.snapshot->>'effective_to')::timestamptz
			RETURNING prev.*
		)
		INSERT INTO rates_history (
			rate_id, card_id, country_id, operator_id, message_type, price,
			min_billing_note, effective_from, effective_to, source, status,
			version, snapshot, changed_by, changed_at, change_reason
		)
		SELECT
			ro.id, ro.card_id, ro.country_id, ro.operator_id, ro.message_type, ro.price,
			ro.min_billing_note, ro.effective_from, ro.effective_to, ro.source, ro.status,
			ro.version, to_jsonb(ro), $1, $2, 're-opened on rollback: ' || $3
		FROM reopened ro
	`, updatedBy, now, reason)
	if err != nil {
		return fmt.Errorf("rollback imported rates reopen predecessors: %w", err)
	}

	return nil
}

func (r *pgRepository) ListActiveRatesByCard(ctx context.Context, cardID uuid.UUID) ([]Rate, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, card_id, country_id, operator_id, message_type, price,
		       min_billing_note, effective_from, effective_to, source, status,
		       version, created_by, updated_by, created_at, updated_at
		FROM rates
		WHERE card_id = $1 AND status = 'active'
	`, cardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Rate
	for rows.Next() {
		var rt Rate
		if err := rows.Scan(
			&rt.ID, &rt.CardID, &rt.CountryID, &rt.OperatorID, &rt.MessageType, &rt.Price,
			&rt.MinBillingNote, &rt.EffectiveFrom, &rt.EffectiveTo, &rt.Source, &rt.Status,
			&rt.Version, &rt.CreatedBy, &rt.UpdatedBy, &rt.CreatedAt, &rt.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, rt)
	}
	return out, nil
}

func (r *pgRepository) ClosePreviousOpenEndedRate(
	ctx context.Context, tx pgx.Tx,
	cardID, countryID uuid.UUID, operatorID *uuid.UUID, msgType string,
	effectiveFrom time.Time, updatedBy *uuid.UUID,
) (*Rate, error) {
	q := r.q(tx)
	now := time.Now().UTC()

	var opClause string
	var args []any
	if operatorID == nil {
		opClause = "operator_id IS NULL"
		args = []any{effectiveFrom, updatedBy, now, cardID, countryID, msgType}
	} else {
		opClause = "operator_id = $7"
		args = []any{effectiveFrom, updatedBy, now, cardID, countryID, msgType, *operatorID}
	}

	query := fmt.Sprintf(`
		UPDATE rates
		SET effective_to = $1,
		    version = version + 1,
		    updated_by = $2,
		    updated_at = $3
		WHERE card_id = $4
		  AND country_id = $5
		  AND message_type = $6
		  AND %s
		  AND status = 'active'
		  AND effective_from < $1
		  AND (effective_to IS NULL OR effective_to > $1)
		RETURNING id, card_id, country_id, operator_id, message_type, price,
		          min_billing_note, effective_from, effective_to, source, status,
		          version, created_by, updated_by, created_at, updated_at
	`, opClause)

	var res Rate
	err := q.QueryRow(ctx, query, args...).Scan(
		&res.ID, &res.CardID, &res.CountryID, &res.OperatorID, &res.MessageType, &res.Price,
		&res.MinBillingNote, &res.EffectiveFrom, &res.EffectiveTo, &res.Source, &res.Status,
		&res.Version, &res.CreatedBy, &res.UpdatedBy, &res.CreatedAt, &res.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // No prior open-ended rate to close
		}
		return nil, fmt.Errorf("close previous rate: %w", err)
	}

	snap, _ := json.Marshal(res)
	histQuery := `
		INSERT INTO rates_history (
			rate_id, card_id, country_id, operator_id, message_type, price,
			min_billing_note, effective_from, effective_to, source, status,
			version, snapshot, changed_by, changed_at, change_reason
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
	`
	_, err = q.Exec(ctx, histQuery,
		res.ID, res.CardID, res.CountryID, res.OperatorID, res.MessageType, res.Price,
		res.MinBillingNote, res.EffectiveFrom, res.EffectiveTo, res.Source, res.Status,
		res.Version, snap, updatedBy, now, "auto-closed by incoming new rate",
	)
	if err != nil {
		return nil, fmt.Errorf("record rate history on close: %w", err)
	}

	return &res, nil
}

func (r *pgRepository) UpdateRate(ctx context.Context, tx pgx.Tx, rate *Rate, reason string) (*Rate, error) {
	q := r.q(tx)
	now := time.Now().UTC()
	rate.Price = TruncateMoney(rate.Price)

	query := `
		UPDATE rates
		SET price = $1,
		    min_billing_note = $2,
		    effective_to = $3,
		    status = $4,
		    version = version + 1,
		    updated_by = $5,
		    updated_at = $6
		WHERE id = $7 AND version = $8
		RETURNING id, card_id, country_id, operator_id, message_type, price,
		          min_billing_note, effective_from, effective_to, source, status,
		          version, created_by, updated_by, created_at, updated_at
	`
	var res Rate
	err := q.QueryRow(ctx, query,
		rate.Price, rate.MinBillingNote, rate.EffectiveTo, rate.Status,
		rate.UpdatedBy, now, rate.ID, rate.Version,
	).Scan(
		&res.ID, &res.CardID, &res.CountryID, &res.OperatorID, &res.MessageType, &res.Price,
		&res.MinBillingNote, &res.EffectiveFrom, &res.EffectiveTo, &res.Source, &res.Status,
		&res.Version, &res.CreatedBy, &res.UpdatedBy, &res.CreatedAt, &res.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23P01" {
			return nil, ErrEffectiveDateOverlap
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("rate update failed (conflict or not found)")
		}
		return nil, fmt.Errorf("update rate: %w", err)
	}

	snap, _ := json.Marshal(res)
	histQuery := `
		INSERT INTO rates_history (
			rate_id, card_id, country_id, operator_id, message_type, price,
			min_billing_note, effective_from, effective_to, source, status,
			version, snapshot, changed_by, changed_at, change_reason
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
	`
	_, err = q.Exec(ctx, histQuery,
		res.ID, res.CardID, res.CountryID, res.OperatorID, res.MessageType, res.Price,
		res.MinBillingNote, res.EffectiveFrom, res.EffectiveTo, res.Source, res.Status,
		res.Version, snap, res.UpdatedBy, now, reason,
	)
	if err != nil {
		return nil, fmt.Errorf("record rate history on update: %w", err)
	}

	return &res, nil
}

func (r *pgRepository) GetRateByID(ctx context.Context, id uuid.UUID) (*Rate, error) {
	query := `
		SELECT r.id, r.card_id, r.country_id, c.iso2, c.name,
		       r.operator_id, o.name, o.mcc, o.mnc,
		       r.message_type, r.price, r.min_billing_note,
		       r.effective_from, r.effective_to, r.source, r.status,
		       r.version, r.created_by, r.updated_by, r.created_at, r.updated_at
		FROM rates r
		JOIN countries c ON c.id = r.country_id
		LEFT JOIN operators o ON o.id = r.operator_id
		WHERE r.id = $1
	`
	var rt Rate
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&rt.ID, &rt.CardID, &rt.CountryID, &rt.CountryISO2, &rt.CountryName,
		&rt.OperatorID, &rt.OperatorName, &rt.OperatorMCC, &rt.OperatorMNC,
		&rt.MessageType, &rt.Price, &rt.MinBillingNote,
		&rt.EffectiveFrom, &rt.EffectiveTo, &rt.Source, &rt.Status,
		&rt.Version, &rt.CreatedBy, &rt.UpdatedBy, &rt.CreatedAt, &rt.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRateNotFound
		}
		return nil, fmt.Errorf("get rate by id: %w", err)
	}
	return &rt, nil
}

func (r *pgRepository) ListRates(ctx context.Context, filter RateListFilter) ([]Rate, int, error) {
	var conditions []string
	var args []any
	argIdx := 1

	conditions = append(conditions, fmt.Sprintf("r.card_id = $%d", argIdx))
	args = append(args, filter.CardID)
	argIdx++

	if filter.CountryID != nil {
		conditions = append(conditions, fmt.Sprintf("r.country_id = $%d", argIdx))
		args = append(args, *filter.CountryID)
	}
	if filter.OperatorID != nil {
		conditions = append(conditions, fmt.Sprintf("r.operator_id = $%d", argIdx))
		args = append(args, *filter.OperatorID)
		argIdx++
	}
	if filter.MessageType != "" {
		conditions = append(conditions, fmt.Sprintf("r.message_type = $%d", argIdx))
		args = append(args, filter.MessageType)
		argIdx++
	}
	if filter.Status != "" {
		conditions = append(conditions, fmt.Sprintf("r.status = $%d", argIdx))
		args = append(args, filter.Status)
		argIdx++
	}
	if filter.EffectiveAt != nil {
		conditions = append(conditions, fmt.Sprintf("r.effective_from <= $%d AND (r.effective_to IS NULL OR r.effective_to > $%d)", argIdx, argIdx))
		args = append(args, *filter.EffectiveAt)
		argIdx++
	}
	if filter.Search != "" {
		conditions = append(conditions, fmt.Sprintf("(c.name ILIKE $%d OR c.iso2 ILIKE $%d OR o.name ILIKE $%d)", argIdx, argIdx, argIdx))
		args = append(args, "%"+filter.Search+"%")
		argIdx++
	}

	whereClause := "WHERE " + strings.Join(conditions, " AND ")

	countQuery := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM rates r
		JOIN countries c ON c.id = r.country_id
		LEFT JOIN operators o ON o.id = r.operator_id
		%s
	`, whereClause)

	var total int
	err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("count rates: %w", err)
	}

	page := filter.Page
	if page < 1 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize < 1 || pageSize > 1000 {
		pageSize = 50
	}
	offset := (page - 1) * pageSize

	query := fmt.Sprintf(`
		SELECT r.id, r.card_id, r.country_id, c.iso2, c.name,
		       r.operator_id, o.name, o.mcc, o.mnc,
		       r.message_type, r.price, r.min_billing_note,
		       r.effective_from, r.effective_to, r.source, r.status,
		       r.version, r.created_by, r.updated_by, r.created_at, r.updated_at
		FROM rates r
		JOIN countries c ON c.id = r.country_id
		LEFT JOIN operators o ON o.id = r.operator_id
		%s
		ORDER BY c.name ASC, o.name ASC NULLS FIRST, r.effective_from DESC
		LIMIT $%d OFFSET $%d
	`, whereClause, argIdx, argIdx+1)

	args = append(args, pageSize, offset)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list rates: %w", err)
	}
	defer rows.Close()

	var list []Rate
	for rows.Next() {
		var rt Rate
		err := rows.Scan(
			&rt.ID, &rt.CardID, &rt.CountryID, &rt.CountryISO2, &rt.CountryName,
			&rt.OperatorID, &rt.OperatorName, &rt.OperatorMCC, &rt.OperatorMNC,
			&rt.MessageType, &rt.Price, &rt.MinBillingNote,
			&rt.EffectiveFrom, &rt.EffectiveTo, &rt.Source, &rt.Status,
			&rt.Version, &rt.CreatedBy, &rt.UpdatedBy, &rt.CreatedAt, &rt.UpdatedAt,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("scan rate: %w", err)
		}
		list = append(list, rt)
	}
	return list, total, nil
}

func (r *pgRepository) ListRateHistory(ctx context.Context, rateID uuid.UUID) ([]RateHistory, error) {
	query := `
		SELECT id, rate_id, card_id, country_id, operator_id, message_type,
		       price, min_billing_note, effective_from, effective_to, source,
		       status, version, snapshot, changed_by, changed_at, change_reason
		FROM rates_history
		WHERE rate_id = $1
		ORDER BY version DESC
	`
	rows, err := r.pool.Query(ctx, query, rateID)
	if err != nil {
		return nil, fmt.Errorf("list rate history: %w", err)
	}
	defer rows.Close()

	var items []RateHistory
	for rows.Next() {
		var h RateHistory
		err := rows.Scan(
			&h.ID, &h.RateID, &h.CardID, &h.CountryID, &h.OperatorID, &h.MessageType,
			&h.Price, &h.MinBillingNote, &h.EffectiveFrom, &h.EffectiveTo, &h.Source,
			&h.Status, &h.Version, &h.Snapshot, &h.ChangedBy, &h.ChangedAt, &h.ChangeReason,
		)
		if err != nil {
			return nil, fmt.Errorf("scan rate history: %w", err)
		}
		items = append(items, h)
	}
	return items, nil
}

func (r *pgRepository) GetRateHistoryVersion(ctx context.Context, rateID uuid.UUID, version int) (*RateHistory, error) {
	query := `
		SELECT id, rate_id, card_id, country_id, operator_id, message_type,
		       price, min_billing_note, effective_from, effective_to, source,
		       status, version, snapshot, changed_by, changed_at, change_reason
		FROM rates_history
		WHERE rate_id = $1 AND version = $2
	`
	var h RateHistory
	err := r.pool.QueryRow(ctx, query, rateID, version).Scan(
		&h.ID, &h.RateID, &h.CardID, &h.CountryID, &h.OperatorID, &h.MessageType,
		&h.Price, &h.MinBillingNote, &h.EffectiveFrom, &h.EffectiveTo, &h.Source,
		&h.Status, &h.Version, &h.Snapshot, &h.ChangedBy, &h.ChangedAt, &h.ChangeReason,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("rate history version not found")
		}
		return nil, fmt.Errorf("get rate history version: %w", err)
	}
	return &h, nil
}

func (r *pgRepository) GetActiveRateAt(ctx context.Context, cardID, countryID uuid.UUID, operatorID *uuid.UUID, msgType string, at time.Time) (*Rate, error) {
	var opClause string
	var args []any
	if operatorID == nil {
		opClause = "r.operator_id IS NULL"
		args = []any{cardID, countryID, msgType, at}
	} else {
		opClause = "r.operator_id = $5"
		args = []any{cardID, countryID, msgType, at, *operatorID}
	}

	query := fmt.Sprintf(`
		SELECT r.id, r.card_id, r.country_id, c.iso2, c.name,
		       r.operator_id, o.name, o.mcc, o.mnc,
		       r.message_type, r.price, r.min_billing_note,
		       r.effective_from, r.effective_to, r.source, r.status,
		       r.version, r.created_by, r.updated_by, r.created_at, r.updated_at
		FROM rates r
		JOIN countries c ON c.id = r.country_id
		LEFT JOIN operators o ON o.id = r.operator_id
		WHERE r.card_id = $1
		  AND r.country_id = $2
		  AND r.message_type = $3
		  AND r.status = 'active'
		  AND r.effective_from <= $4
		  AND (r.effective_to IS NULL OR r.effective_to > $4)
		  AND %s
		ORDER BY r.effective_from DESC
		LIMIT 1
	`, opClause)

	var rt Rate
	err := r.pool.QueryRow(ctx, query, args...).Scan(
		&rt.ID, &rt.CardID, &rt.CountryID, &rt.CountryISO2, &rt.CountryName,
		&rt.OperatorID, &rt.OperatorName, &rt.OperatorMCC, &rt.OperatorMNC,
		&rt.MessageType, &rt.Price, &rt.MinBillingNote,
		&rt.EffectiveFrom, &rt.EffectiveTo, &rt.Source, &rt.Status,
		&rt.Version, &rt.CreatedBy, &rt.UpdatedBy, &rt.CreatedAt, &rt.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get active rate at: %w", err)
	}
	return &rt, nil
}

func (r *pgRepository) ListUpcomingRates(ctx context.Context, days int) ([]Rate, error) {
	if days <= 0 {
		days = 30
	}
	now := time.Now().UTC()
	futureLimit := now.AddDate(0, 0, days)

	query := `
		SELECT r.id, r.card_id, r.country_id, c.iso2, c.name,
		       r.operator_id, o.name, o.mcc, o.mnc,
		       r.message_type, r.price, r.min_billing_note,
		       r.effective_from, r.effective_to, r.source, r.status,
		       r.version, r.created_by, r.updated_by, r.created_at, r.updated_at
		FROM rates r
		JOIN countries c ON c.id = r.country_id
		LEFT JOIN operators o ON o.id = r.operator_id
		WHERE r.status = 'active'
		  AND r.effective_from > $1
		  AND r.effective_from <= $2
		ORDER BY r.effective_from ASC, c.name ASC
	`
	rows, err := r.pool.Query(ctx, query, now, futureLimit)
	if err != nil {
		return nil, fmt.Errorf("list upcoming rates: %w", err)
	}
	defer rows.Close()

	var list []Rate
	for rows.Next() {
		var rt Rate
		err := rows.Scan(
			&rt.ID, &rt.CardID, &rt.CountryID, &rt.CountryISO2, &rt.CountryName,
			&rt.OperatorID, &rt.OperatorName, &rt.OperatorMCC, &rt.OperatorMNC,
			&rt.MessageType, &rt.Price, &rt.MinBillingNote,
			&rt.EffectiveFrom, &rt.EffectiveTo, &rt.Source, &rt.Status,
			&rt.Version, &rt.CreatedBy, &rt.UpdatedBy, &rt.CreatedAt, &rt.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan upcoming rate: %w", err)
		}
		list = append(list, rt)
	}
	return list, nil
}

// -----------------------------------------------------------------------------
// CUSTOMER ASSIGNMENTS
// -----------------------------------------------------------------------------

func (r *pgRepository) CreateCustomerAssignment(ctx context.Context, tx pgx.Tx, a *CustomerRateAssignment, reason string) (*CustomerRateAssignment, error) {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	if a.Status == "" {
		a.Status = "active"
	}
	a.Version = 1
	now := time.Now().UTC()
	a.CreatedAt = now
	a.UpdatedAt = now

	q := r.q(tx)
	query := `
		INSERT INTO customer_rate_assignments (
			id, customer_org_id, card_id, effective_from, effective_to,
			status, version, assigned_by, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id, customer_org_id, card_id, effective_from, effective_to,
		          status, version, assigned_by, created_at, updated_at
	`
	var res CustomerRateAssignment
	err := q.QueryRow(ctx, query,
		a.ID, a.CustomerOrgID, a.CardID, a.EffectiveFrom, a.EffectiveTo,
		a.Status, a.Version, a.AssignedBy, a.CreatedAt, a.UpdatedAt,
	).Scan(
		&res.ID, &res.CustomerOrgID, &res.CardID, &res.EffectiveFrom, &res.EffectiveTo,
		&res.Status, &res.Version, &res.AssignedBy, &res.CreatedAt, &res.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23P01" {
			return nil, errors.New("customer already has an active rate card assignment for this time period")
		}
		return nil, fmt.Errorf("create customer rate assignment: %w", err)
	}

	snap, _ := json.Marshal(res)
	histQuery := `
		INSERT INTO customer_rate_assignments_history (
			assignment_id, customer_org_id, card_id, effective_from, effective_to,
			status, version, snapshot, changed_by, changed_at, change_reason
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`
	_, err = q.Exec(ctx, histQuery,
		res.ID, res.CustomerOrgID, res.CardID, res.EffectiveFrom, res.EffectiveTo,
		res.Status, res.Version, snap, res.AssignedBy, now, reason,
	)
	if err != nil {
		return nil, fmt.Errorf("record customer assignment history: %w", err)
	}

	return &res, nil
}

func (r *pgRepository) UpdateCustomerAssignment(ctx context.Context, tx pgx.Tx, a *CustomerRateAssignment, reason string) (*CustomerRateAssignment, error) {
	q := r.q(tx)
	now := time.Now().UTC()

	query := `
		UPDATE customer_rate_assignments
		SET card_id = $1,
		    effective_to = $2,
		    status = $3,
		    version = version + 1,
		    assigned_by = $4,
		    updated_at = $5
		WHERE id = $6 AND version = $7
		RETURNING id, customer_org_id, card_id, effective_from, effective_to,
		          status, version, assigned_by, created_at, updated_at
	`
	var res CustomerRateAssignment
	err := q.QueryRow(ctx, query,
		a.CardID, a.EffectiveTo, a.Status, a.AssignedBy, now, a.ID, a.Version,
	).Scan(
		&res.ID, &res.CustomerOrgID, &res.CardID, &res.EffectiveFrom, &res.EffectiveTo,
		&res.Status, &res.Version, &res.AssignedBy, &res.CreatedAt, &res.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23P01" {
			return nil, errors.New("customer already has an active rate card assignment for this time period")
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("assignment update failed (conflict or not found)")
		}
		return nil, fmt.Errorf("update customer assignment: %w", err)
	}

	snap, _ := json.Marshal(res)
	histQuery := `
		INSERT INTO customer_rate_assignments_history (
			assignment_id, customer_org_id, card_id, effective_from, effective_to,
			status, version, snapshot, changed_by, changed_at, change_reason
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`
	_, err = q.Exec(ctx, histQuery,
		res.ID, res.CustomerOrgID, res.CardID, res.EffectiveFrom, res.EffectiveTo,
		res.Status, res.Version, snap, a.AssignedBy, now, reason,
	)
	if err != nil {
		return nil, fmt.Errorf("record customer assignment history: %w", err)
	}

	return &res, nil
}

func (r *pgRepository) GetCustomerAssignmentByID(ctx context.Context, id uuid.UUID) (*CustomerRateAssignment, error) {
	query := `
		SELECT ca.id, ca.customer_org_id, o.name, ca.card_id, rc.name,
		       ca.effective_from, ca.effective_to, ca.status, ca.version,
		       ca.assigned_by, ca.created_at, ca.updated_at
		FROM customer_rate_assignments ca
		JOIN organizations o ON o.id = ca.customer_org_id
		JOIN rate_cards rc ON rc.id = ca.card_id
		WHERE ca.id = $1
	`
	var a CustomerRateAssignment
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&a.ID, &a.CustomerOrgID, &a.CustomerOrgName, &a.CardID, &a.CardName,
		&a.EffectiveFrom, &a.EffectiveTo, &a.Status, &a.Version,
		&a.AssignedBy, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAssignmentNotFound
		}
		return nil, fmt.Errorf("get customer assignment by id: %w", err)
	}
	return &a, nil
}

func (r *pgRepository) GetActiveCustomerAssignmentAt(ctx context.Context, customerOrgID uuid.UUID, at time.Time) (*CustomerRateAssignment, error) {
	query := `
		SELECT ca.id, ca.customer_org_id, o.name, ca.card_id, rc.name,
		       ca.effective_from, ca.effective_to, ca.status, ca.version,
		       ca.assigned_by, ca.created_at, ca.updated_at
		FROM customer_rate_assignments ca
		JOIN organizations o ON o.id = ca.customer_org_id
		JOIN rate_cards rc ON rc.id = ca.card_id
		WHERE ca.customer_org_id = $1
		  AND ca.status = 'active'
		  AND ca.effective_from <= $2
		  AND (ca.effective_to IS NULL OR ca.effective_to > $2)
		  AND rc.status = 'active'
		  AND rc.archived = false
		ORDER BY ca.effective_from DESC
		LIMIT 1
	`
	var a CustomerRateAssignment
	err := r.pool.QueryRow(ctx, query, customerOrgID, at).Scan(
		&a.ID, &a.CustomerOrgID, &a.CustomerOrgName, &a.CardID, &a.CardName,
		&a.EffectiveFrom, &a.EffectiveTo, &a.Status, &a.Version,
		&a.AssignedBy, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get active customer assignment: %w", err)
	}
	return &a, nil
}

func (r *pgRepository) ListCustomerAssignments(ctx context.Context, customerOrgID *uuid.UUID) ([]CustomerRateAssignment, error) {
	var conditions []string
	var args []any
	if customerOrgID != nil {
		conditions = append(conditions, "ca.customer_org_id = $1")
		args = append(args, *customerOrgID)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT ca.id, ca.customer_org_id, o.name, ca.card_id, rc.name,
		       ca.effective_from, ca.effective_to, ca.status, ca.version,
		       ca.assigned_by, ca.created_at, ca.updated_at
		FROM customer_rate_assignments ca
		JOIN organizations o ON o.id = ca.customer_org_id
		JOIN rate_cards rc ON rc.id = ca.card_id
		%s
		ORDER BY ca.created_at DESC
	`, whereClause)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list customer assignments: %w", err)
	}
	defer rows.Close()

	var list []CustomerRateAssignment
	for rows.Next() {
		var a CustomerRateAssignment
		err := rows.Scan(
			&a.ID, &a.CustomerOrgID, &a.CustomerOrgName, &a.CardID, &a.CardName,
			&a.EffectiveFrom, &a.EffectiveTo, &a.Status, &a.Version,
			&a.AssignedBy, &a.CreatedAt, &a.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan customer assignment: %w", err)
		}
		list = append(list, a)
	}
	return list, nil
}

func (r *pgRepository) ListCustomerAssignmentHistory(ctx context.Context, assignmentID uuid.UUID) ([]CustomerRateAssignmentHistory, error) {
	query := `
		SELECT id, assignment_id, customer_org_id, card_id, effective_from,
		       effective_to, status, version, snapshot, changed_by, changed_at, change_reason
		FROM customer_rate_assignments_history
		WHERE assignment_id = $1
		ORDER BY version DESC
	`
	rows, err := r.pool.Query(ctx, query, assignmentID)
	if err != nil {
		return nil, fmt.Errorf("list customer assignment history: %w", err)
	}
	defer rows.Close()

	var items []CustomerRateAssignmentHistory
	for rows.Next() {
		var h CustomerRateAssignmentHistory
		err := rows.Scan(
			&h.ID, &h.AssignmentID, &h.CustomerOrgID, &h.CardID, &h.EffectiveFrom,
			&h.EffectiveTo, &h.Status, &h.Version, &h.Snapshot, &h.ChangedBy, &h.ChangedAt, &h.ChangeReason,
		)
		if err != nil {
			return nil, fmt.Errorf("scan customer assignment history: %w", err)
		}
		items = append(items, h)
	}
	return items, nil
}

func (r *pgRepository) GetCustomerAssignmentHistoryVersion(ctx context.Context, assignmentID uuid.UUID, version int) (*CustomerRateAssignmentHistory, error) {
	query := `
		SELECT id, assignment_id, customer_org_id, card_id, effective_from,
		       effective_to, status, version, snapshot, changed_by, changed_at, change_reason
		FROM customer_rate_assignments_history
		WHERE assignment_id = $1 AND version = $2
	`
	var h CustomerRateAssignmentHistory
	err := r.pool.QueryRow(ctx, query, assignmentID, version).Scan(
		&h.ID, &h.AssignmentID, &h.CustomerOrgID, &h.CardID, &h.EffectiveFrom,
		&h.EffectiveTo, &h.Status, &h.Version, &h.Snapshot, &h.ChangedBy, &h.ChangedAt, &h.ChangeReason,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("customer assignment history version not found")
		}
		return nil, fmt.Errorf("get customer assignment history version: %w", err)
	}
	return &h, nil
}

// -----------------------------------------------------------------------------
// RESELLER COST ASSIGNMENTS
// -----------------------------------------------------------------------------

func (r *pgRepository) CreateResellerCostAssignment(ctx context.Context, tx pgx.Tx, a *ResellerCostAssignment, reason string) (*ResellerCostAssignment, error) {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	if a.Status == "" {
		a.Status = "active"
	}
	a.Version = 1
	now := time.Now().UTC()
	a.CreatedAt = now
	a.UpdatedAt = now

	q := r.q(tx)
	query := `
		INSERT INTO reseller_cost_assignments (
			id, reseller_org_id, card_id, effective_from, effective_to,
			status, version, assigned_by, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id, reseller_org_id, card_id, effective_from, effective_to,
		          status, version, assigned_by, created_at, updated_at
	`
	var res ResellerCostAssignment
	err := q.QueryRow(ctx, query,
		a.ID, a.ResellerOrgID, a.CardID, a.EffectiveFrom, a.EffectiveTo,
		a.Status, a.Version, a.AssignedBy, a.CreatedAt, a.UpdatedAt,
	).Scan(
		&res.ID, &res.ResellerOrgID, &res.CardID, &res.EffectiveFrom, &res.EffectiveTo,
		&res.Status, &res.Version, &res.AssignedBy, &res.CreatedAt, &res.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23P01" {
			return nil, errors.New("reseller already has an active cost card assignment for this time period")
		}
		return nil, fmt.Errorf("create reseller cost assignment: %w", err)
	}

	snap, _ := json.Marshal(res)
	histQuery := `
		INSERT INTO reseller_cost_assignments_history (
			assignment_id, reseller_org_id, card_id, effective_from, effective_to,
			status, version, snapshot, changed_by, changed_at, change_reason
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`
	_, err = q.Exec(ctx, histQuery,
		res.ID, res.ResellerOrgID, res.CardID, res.EffectiveFrom, res.EffectiveTo,
		res.Status, res.Version, snap, res.AssignedBy, now, reason,
	)
	if err != nil {
		return nil, fmt.Errorf("record reseller cost assignment history: %w", err)
	}

	return &res, nil
}

func (r *pgRepository) UpdateResellerCostAssignment(ctx context.Context, tx pgx.Tx, a *ResellerCostAssignment, reason string) (*ResellerCostAssignment, error) {
	q := r.q(tx)
	now := time.Now().UTC()

	query := `
		UPDATE reseller_cost_assignments
		SET card_id = $1,
		    effective_to = $2,
		    status = $3,
		    version = version + 1,
		    assigned_by = $4,
		    updated_at = $5
		WHERE id = $6 AND version = $7
		RETURNING id, reseller_org_id, card_id, effective_from, effective_to,
		          status, version, assigned_by, created_at, updated_at
	`
	var res ResellerCostAssignment
	err := q.QueryRow(ctx, query,
		a.CardID, a.EffectiveTo, a.Status, a.AssignedBy, now, a.ID, a.Version,
	).Scan(
		&res.ID, &res.ResellerOrgID, &res.CardID, &res.EffectiveFrom, &res.EffectiveTo,
		&res.Status, &res.Version, &res.AssignedBy, &res.CreatedAt, &res.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23P01" {
			return nil, errors.New("reseller already has an active cost card assignment for this time period")
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("reseller assignment update failed (conflict or not found)")
		}
		return nil, fmt.Errorf("update reseller cost assignment: %w", err)
	}

	snap, _ := json.Marshal(res)
	histQuery := `
		INSERT INTO reseller_cost_assignments_history (
			assignment_id, reseller_org_id, card_id, effective_from, effective_to,
			status, version, snapshot, changed_by, changed_at, change_reason
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`
	_, err = q.Exec(ctx, histQuery,
		res.ID, res.ResellerOrgID, res.CardID, res.EffectiveFrom, res.EffectiveTo,
		res.Status, res.Version, snap, a.AssignedBy, now, reason,
	)
	if err != nil {
		return nil, fmt.Errorf("record reseller cost assignment history: %w", err)
	}

	return &res, nil
}

func (r *pgRepository) GetResellerCostAssignmentByID(ctx context.Context, id uuid.UUID) (*ResellerCostAssignment, error) {
	query := `
		SELECT ca.id, ca.reseller_org_id, o.name, ca.card_id, rc.name,
		       ca.effective_from, ca.effective_to, ca.status, ca.version,
		       ca.assigned_by, ca.created_at, ca.updated_at
		FROM reseller_cost_assignments ca
		JOIN organizations o ON o.id = ca.reseller_org_id
		JOIN rate_cards rc ON rc.id = ca.card_id
		WHERE ca.id = $1
	`
	var a ResellerCostAssignment
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&a.ID, &a.ResellerOrgID, &a.ResellerOrgName, &a.CardID, &a.CardName,
		&a.EffectiveFrom, &a.EffectiveTo, &a.Status, &a.Version,
		&a.AssignedBy, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAssignmentNotFound
		}
		return nil, fmt.Errorf("get reseller cost assignment by id: %w", err)
	}
	return &a, nil
}

func (r *pgRepository) GetActiveResellerCostAssignmentAt(ctx context.Context, resellerOrgID uuid.UUID, at time.Time) (*ResellerCostAssignment, error) {
	query := `
		SELECT ca.id, ca.reseller_org_id, o.name, ca.card_id, rc.name,
		       ca.effective_from, ca.effective_to, ca.status, ca.version,
		       ca.assigned_by, ca.created_at, ca.updated_at
		FROM reseller_cost_assignments ca
		JOIN organizations o ON o.id = ca.reseller_org_id
		JOIN rate_cards rc ON rc.id = ca.card_id
		WHERE ca.reseller_org_id = $1
		  AND ca.status = 'active'
		  AND ca.effective_from <= $2
		  AND (ca.effective_to IS NULL OR ca.effective_to > $2)
		  AND rc.status = 'active'
		  AND rc.archived = false
		ORDER BY ca.effective_from DESC
		LIMIT 1
	`
	var a ResellerCostAssignment
	err := r.pool.QueryRow(ctx, query, resellerOrgID, at).Scan(
		&a.ID, &a.ResellerOrgID, &a.ResellerOrgName, &a.CardID, &a.CardName,
		&a.EffectiveFrom, &a.EffectiveTo, &a.Status, &a.Version,
		&a.AssignedBy, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get active reseller cost assignment: %w", err)
	}
	return &a, nil
}

func (r *pgRepository) ListResellerCostAssignments(ctx context.Context, resellerOrgID *uuid.UUID) ([]ResellerCostAssignment, error) {
	var conditions []string
	var args []any
	if resellerOrgID != nil {
		conditions = append(conditions, "ca.reseller_org_id = $1")
		args = append(args, *resellerOrgID)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT ca.id, ca.reseller_org_id, o.name, ca.card_id, rc.name,
		       ca.effective_from, ca.effective_to, ca.status, ca.version,
		       ca.assigned_by, ca.created_at, ca.updated_at
		FROM reseller_cost_assignments ca
		JOIN organizations o ON o.id = ca.reseller_org_id
		JOIN rate_cards rc ON rc.id = ca.card_id
		%s
		ORDER BY ca.created_at DESC
	`, whereClause)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list reseller cost assignments: %w", err)
	}
	defer rows.Close()

	var list []ResellerCostAssignment
	for rows.Next() {
		var a ResellerCostAssignment
		err := rows.Scan(
			&a.ID, &a.ResellerOrgID, &a.ResellerOrgName, &a.CardID, &a.CardName,
			&a.EffectiveFrom, &a.EffectiveTo, &a.Status, &a.Version,
			&a.AssignedBy, &a.CreatedAt, &a.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan reseller cost assignment: %w", err)
		}
		list = append(list, a)
	}
	return list, nil
}

func (r *pgRepository) ListResellerCostAssignmentHistory(ctx context.Context, assignmentID uuid.UUID) ([]ResellerCostAssignmentHistory, error) {
	query := `
		SELECT id, assignment_id, reseller_org_id, card_id, effective_from,
		       effective_to, status, version, snapshot, changed_by, changed_at, change_reason
		FROM reseller_cost_assignments_history
		WHERE assignment_id = $1
		ORDER BY version DESC
	`
	rows, err := r.pool.Query(ctx, query, assignmentID)
	if err != nil {
		return nil, fmt.Errorf("list reseller cost assignment history: %w", err)
	}
	defer rows.Close()

	var items []ResellerCostAssignmentHistory
	for rows.Next() {
		var h ResellerCostAssignmentHistory
		err := rows.Scan(
			&h.ID, &h.AssignmentID, &h.ResellerOrgID, &h.CardID, &h.EffectiveFrom,
			&h.EffectiveTo, &h.Status, &h.Version, &h.Snapshot, &h.ChangedBy, &h.ChangedAt, &h.ChangeReason,
		)
		if err != nil {
			return nil, fmt.Errorf("scan reseller cost assignment history: %w", err)
		}
		items = append(items, h)
	}
	return items, nil
}

func (r *pgRepository) GetResellerCostAssignmentHistoryVersion(ctx context.Context, assignmentID uuid.UUID, version int) (*ResellerCostAssignmentHistory, error) {
	query := `
		SELECT id, assignment_id, reseller_org_id, card_id, effective_from,
		       effective_to, status, version, snapshot, changed_by, changed_at, change_reason
		FROM reseller_cost_assignments_history
		WHERE assignment_id = $1 AND version = $2
	`
	var h ResellerCostAssignmentHistory
	err := r.pool.QueryRow(ctx, query, assignmentID, version).Scan(
		&h.ID, &h.AssignmentID, &h.ResellerOrgID, &h.CardID, &h.EffectiveFrom,
		&h.EffectiveTo, &h.Status, &h.Version, &h.Snapshot, &h.ChangedBy, &h.ChangedAt, &h.ChangeReason,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("reseller cost assignment history version not found")
		}
		return nil, fmt.Errorf("get reseller cost assignment history version: %w", err)
	}
	return &h, nil
}

// -----------------------------------------------------------------------------
// MASTER DATA & INTELLIGENCE
// -----------------------------------------------------------------------------

func (r *pgRepository) ValidateCurrencyExists(ctx context.Context, code string) (bool, error) {
	query := `SELECT 1 FROM countries WHERE currency_code = $1 LIMIT 1`
	var val int
	err := r.pool.QueryRow(ctx, query, strings.ToUpper(code)).Scan(&val)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("validate currency: %w", err)
	}
	return true, nil
}

func (r *pgRepository) ValidateCountryAndOperator(ctx context.Context, countryID uuid.UUID, operatorID *uuid.UUID) error {
	var countryExists int
	err := r.pool.QueryRow(ctx, "SELECT 1 FROM countries WHERE id = $1 AND status != 'archived'", countryID).Scan(&countryExists)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("referenced country not found or archived")
		}
		return err
	}

	if operatorID != nil {
		var opExists int
		err := r.pool.QueryRow(ctx, "SELECT 1 FROM operators WHERE id = $1 AND country_id = $2 AND status != 'archived'", *operatorID, countryID).Scan(&opExists)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("referenced operator not found or does not belong to country")
			}
			return err
		}
	}
	return nil
}

func (r *pgRepository) GetLowestActiveBuyRate(ctx context.Context, countryID uuid.UUID, operatorID *uuid.UUID, msgType string, at time.Time, currency string) (*decimal.Decimal, int, error) {
	lowest, sameCcyProvs, _, _, _, err := r.InspectBuyCoverageAndRates(ctx, countryID, operatorID, msgType, at, currency)
	return lowest, sameCcyProvs, err
}

func (r *pgRepository) InspectBuyCoverageAndRates(
	ctx context.Context,
	countryID uuid.UUID,
	operatorID *uuid.UUID,
	msgType string,
	at time.Time,
	currency string,
) (lowestSameCcy *decimal.Decimal, sameCcyProvs int, activeCovProvs int, otherCcyProvs int, otherCurrencies []string, err error) {
	if msgType == "" || msgType == "sms" {
		msgType = "default"
	}
	var opCovClause string
	var args []any
	if operatorID == nil {
		opCovClause = "operator_id IS NULL"
		args = []any{countryID, msgType, at, strings.ToUpper(currency)}
	} else {
		opCovClause = "(operator_id = $5 OR operator_id IS NULL)"
		args = []any{countryID, msgType, at, strings.ToUpper(currency), *operatorID}
	}

	query := fmt.Sprintf(`
		WITH active_providers AS (
			SELECT DISTINCT provider_id
			FROM provider_coverage
			WHERE country_id = $1
			  AND %s
			  AND status = 'active'
			  AND archived = false
		),
		buy_rates AS (
			SELECT r.price, rc.provider_id, rc.currency_code
			FROM rates r
			JOIN rate_cards rc ON rc.id = r.card_id
			JOIN active_providers ap ON ap.provider_id = rc.provider_id
			WHERE rc.kind = 'provider_buy'
			  AND rc.status = 'active'
			  AND rc.archived = false
			  AND r.country_id = $1
			  AND %s
			  AND r.message_type = $2
			  AND r.status = 'active'
			  AND r.effective_from <= $3
			  AND (r.effective_to IS NULL OR r.effective_to > $3)
		)
		SELECT
			(SELECT MIN(price) FROM buy_rates WHERE currency_code = $4),
			(SELECT COUNT(DISTINCT provider_id) FROM buy_rates WHERE currency_code = $4),
			(SELECT COUNT(*) FROM active_providers),
			(SELECT COUNT(DISTINCT provider_id) FROM buy_rates WHERE currency_code <> $4),
			COALESCE((SELECT ARRAY_AGG(DISTINCT currency_code ORDER BY currency_code) FROM buy_rates WHERE currency_code <> $4), '{}'::text[])
	`, opCovClause, opCovClause)

	err = r.pool.QueryRow(ctx, query, args...).Scan(
		&lowestSameCcy,
		&sameCcyProvs,
		&activeCovProvs,
		&otherCcyProvs,
		&otherCurrencies,
	)
	if err != nil {
		return nil, 0, 0, 0, nil, fmt.Errorf("inspect buy coverage and rates: %w", err)
	}
	return lowestSameCcy, sameCcyProvs, activeCovProvs, otherCcyProvs, otherCurrencies, nil
}

func (r *pgRepository) ListLowestActiveBuyRatesByCurrency(ctx context.Context, currency string, at time.Time) (map[string]decimal.Decimal, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT r.country_id, r.operator_id, r.message_type, MIN(r.price)
		FROM rates r
		JOIN rate_cards rc ON rc.id = r.card_id
		JOIN provider_coverage pc ON pc.provider_id = rc.provider_id
		     AND pc.country_id = r.country_id
		     AND (pc.operator_id = r.operator_id OR pc.operator_id IS NULL)
		     AND pc.status = 'active'
		     AND pc.archived = false
		WHERE rc.kind = 'provider_buy'
		  AND rc.status = 'active'
		  AND rc.archived = false
		  AND rc.currency_code = $1
		  AND r.status = 'active'
		  AND r.effective_from <= $2
		  AND (r.effective_to IS NULL OR r.effective_to > $2)
		GROUP BY r.country_id, r.operator_id, r.message_type
	`, strings.ToUpper(currency), at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]decimal.Decimal)
	for rows.Next() {
		var countryID uuid.UUID
		var operatorID *uuid.UUID
		var msgType string
		var minPrice decimal.Decimal
		if err := rows.Scan(&countryID, &operatorID, &msgType, &minPrice); err != nil {
			return nil, err
		}
		opStr := "nil"
		if operatorID != nil {
			opStr = operatorID.String()
		}
		key := fmt.Sprintf("%s:%s:%s", countryID.String(), opStr, msgType)
		out[key] = minPrice
	}
	return out, nil
}

func (r *pgRepository) GetResellerCostRateAt(ctx context.Context, resellerOrgID, countryID uuid.UUID, operatorID *uuid.UUID, msgType string, at time.Time) (*decimal.Decimal, error) {
	cost, _, err := r.GetResellerCostRateWithCurrencyAt(ctx, resellerOrgID, countryID, operatorID, msgType, at)
	return cost, err
}

func (r *pgRepository) GetResellerCostRateWithCurrencyAt(ctx context.Context, resellerOrgID, countryID uuid.UUID, operatorID *uuid.UUID, msgType string, at time.Time) (*decimal.Decimal, string, error) {
	// Look up reseller's assigned cost card
	assignment, err := r.GetActiveResellerCostAssignmentAt(ctx, resellerOrgID, at)
	if err != nil {
		return nil, "", err
	}
	if assignment == nil {
		return nil, "", nil // No cost card assigned
	}

	costCard, err := r.GetCardByID(ctx, assignment.CardID)
	if err != nil || costCard == nil {
		return nil, "", err
	}

	// 1. Try operator-specific rate on assigned card
	if operatorID != nil {
		opRate, err := r.GetActiveRateAt(ctx, assignment.CardID, countryID, operatorID, msgType, at)
		if err != nil {
			return nil, costCard.CurrencyCode, err
		}
		if opRate != nil {
			return &opRate.Price, costCard.CurrencyCode, nil
		}
	}

	// 2. Try country-wide rate on assigned card
	countryRate, err := r.GetActiveRateAt(ctx, assignment.CardID, countryID, nil, msgType, at)
	if err != nil {
		return nil, costCard.CurrencyCode, err
	}
	if countryRate != nil {
		return &countryRate.Price, costCard.CurrencyCode, nil
	}

	return nil, costCard.CurrencyCode, nil
}

func (r *pgRepository) GenerateMarginReport(ctx context.Context, filter MarginReportFilter) ([]MarginReportItem, int, error) {
	now := time.Now().UTC()
	var conditions []string
	var args []any
	argIdx := 1

	conditions = append(conditions, "rc.kind = 'customer_sell'", "rc.status = 'active'", "rc.archived = false", "r.status = 'active'")
	conditions = append(conditions, fmt.Sprintf("r.effective_from <= $%d AND (r.effective_to IS NULL OR r.effective_to > $%d)", argIdx, argIdx))
	args = append(args, now)
	argIdx++

	if filter.CardID != nil {
		conditions = append(conditions, fmt.Sprintf("rc.id = $%d", argIdx))
		args = append(args, *filter.CardID)
		argIdx++
	}
	if filter.CountryID != nil {
		conditions = append(conditions, fmt.Sprintf("r.country_id = $%d", argIdx))
		args = append(args, *filter.CountryID)
	}

	whereClause := "WHERE " + strings.Join(conditions, " AND ")

	// Base query joining customer sell rates with lowest provider buy price
	query := fmt.Sprintf(`
		WITH buy_candidates AS (
			SELECT r_buy.country_id,
			       r_buy.operator_id,
			       r_buy.message_type,
			       rc_buy.currency_code,
			       MIN(r_buy.price) AS lowest_buy,
			       COUNT(DISTINCT rc_buy.provider_id) AS active_provs
			FROM rates r_buy
			JOIN rate_cards rc_buy ON rc_buy.id = r_buy.card_id
			JOIN provider_coverage pc ON pc.provider_id = rc_buy.provider_id 
			     AND pc.country_id = r_buy.country_id 
			     AND (pc.operator_id = r_buy.operator_id OR (pc.operator_id IS NULL AND r_buy.operator_id IS NULL))
			     AND pc.status = 'active' AND pc.archived = false
			WHERE rc_buy.kind = 'provider_buy'
			  AND rc_buy.status = 'active'
			  AND rc_buy.archived = false
			  AND r_buy.status = 'active'
			  AND r_buy.effective_from <= $1
			  AND (r_buy.effective_to IS NULL OR r_buy.effective_to > $1)
			GROUP BY r_buy.country_id, r_buy.operator_id, r_buy.message_type, rc_buy.currency_code
		)
		SELECT rc.id AS card_id, rc.name AS card_name,
		       r.country_id, c.iso2, c.name,
		       r.operator_id, o.name,
		       r.message_type, rc.currency_code,
		       r.price AS sell_price,
		       COALESCE(bc.lowest_buy, 0) AS lowest_buy_cost,
		       COALESCE(bc.active_provs, 0) AS active_providers
		FROM rates r
		JOIN rate_cards rc ON rc.id = r.card_id
		JOIN countries c ON c.id = r.country_id
		LEFT JOIN operators o ON o.id = r.operator_id
		LEFT JOIN buy_candidates bc ON bc.country_id = r.country_id
		     AND (bc.operator_id = r.operator_id OR (bc.operator_id IS NULL AND r.operator_id IS NULL))
		     AND bc.message_type = r.message_type
		     AND bc.currency_code = rc.currency_code
		%s
		ORDER BY c.name ASC, o.name ASC NULLS FIRST
	`, whereClause)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("margin report query: %w", err)
	}
	defer rows.Close()

	var allItems []MarginReportItem
	for rows.Next() {
		var item MarginReportItem
		err := rows.Scan(
			&item.CardID, &item.CardName,
			&item.CountryID, &item.CountryISO2, &item.CountryName,
			&item.OperatorID, &item.OperatorName,
			&item.MessageType, &item.Currency,
			&item.SellPrice,
			&item.LowestBuyCost,
			&item.ActiveProviders,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("scan margin report item: %w", err)
		}

		if item.LowestBuyCost.GreaterThan(decimal.Zero) {
			item.MarginAmount = item.SellPrice.Sub(item.LowestBuyCost)
			if item.SellPrice.GreaterThan(decimal.Zero) {
				item.MarginPercent = item.MarginAmount.Div(item.SellPrice).Mul(decimal.NewFromInt(100)).Truncate(2)
			}
			item.Violation = item.SellPrice.LessThan(item.LowestBuyCost)
		} else {
			item.MarginAmount = decimal.Zero
			item.MarginPercent = decimal.Zero
			item.Violation = false
		}

		if filter.OnlyDeficit && !item.Violation {
			continue
		}
		allItems = append(allItems, item)
	}

	total := len(allItems)
	page := filter.Page
	if page < 1 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize < 1 || pageSize > 500 {
		pageSize = 50
	}
	start := (page - 1) * pageSize
	if start >= total {
		return []MarginReportItem{}, total, nil
	}
	end := start + pageSize
	if end > total {
		end = total
	}

	return allItems[start:end], total, nil
}

func (r *pgRepository) LoadAllActiveRates(ctx context.Context) ([]Rate, []RateCard, []CustomerRateAssignment, error) {
	// Load active cards
	cardRows, err := r.pool.Query(ctx, `
		SELECT id, name, kind, owner_org_id, provider_id, currency_code, channel, status, description, archived, version, created_by, updated_by, created_at, updated_at
		FROM rate_cards
		WHERE status = 'active' AND archived = false
	`)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load active cards: %w", err)
	}
	defer cardRows.Close()

	var cards []RateCard
	for cardRows.Next() {
		var c RateCard
		if err := cardRows.Scan(
			&c.ID, &c.Name, &c.Kind, &c.OwnerOrgID, &c.ProviderID, &c.CurrencyCode, &c.Channel,
			&c.Status, &c.Description, &c.Archived, &c.Version, &c.CreatedBy, &c.UpdatedBy, &c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			return nil, nil, nil, err
		}
		cards = append(cards, c)
	}

	// Load active customer assignments
	assignRows, err := r.pool.Query(ctx, `
		SELECT id, customer_org_id, card_id, effective_from, effective_to, status, version, assigned_by, created_at, updated_at
		FROM customer_rate_assignments
		WHERE status = 'active'
	`)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load active assignments: %w", err)
	}
	defer assignRows.Close()

	var assignments []CustomerRateAssignment
	for assignRows.Next() {
		var a CustomerRateAssignment
		if err := assignRows.Scan(
			&a.ID, &a.CustomerOrgID, &a.CardID, &a.EffectiveFrom, &a.EffectiveTo,
			&a.Status, &a.Version, &a.AssignedBy, &a.CreatedAt, &a.UpdatedAt,
		); err != nil {
			return nil, nil, nil, err
		}
		assignments = append(assignments, a)
	}

	// Load active rates
	rateRows, err := r.pool.Query(ctx, `
		SELECT r.id, r.card_id, r.country_id, c.iso2, c.name,
		       r.operator_id, o.name, o.mcc, o.mnc,
		       r.message_type, r.price, r.min_billing_note,
		       r.effective_from, r.effective_to, r.source, r.status,
		       r.version, r.created_by, r.updated_by, r.created_at, r.updated_at
		FROM rates r
		JOIN countries c ON c.id = r.country_id
		LEFT JOIN operators o ON o.id = r.operator_id
		WHERE r.status = 'active'
	`)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load active rates: %w", err)
	}
	defer rateRows.Close()

	var rates []Rate
	for rateRows.Next() {
		var rt Rate
		if err := rateRows.Scan(
			&rt.ID, &rt.CardID, &rt.CountryID, &rt.CountryISO2, &rt.CountryName,
			&rt.OperatorID, &rt.OperatorName, &rt.OperatorMCC, &rt.OperatorMNC,
			&rt.MessageType, &rt.Price, &rt.MinBillingNote,
			&rt.EffectiveFrom, &rt.EffectiveTo, &rt.Source, &rt.Status,
			&rt.Version, &rt.CreatedBy, &rt.UpdatedBy, &rt.CreatedAt, &rt.UpdatedAt,
		); err != nil {
			return nil, nil, nil, err
		}
		rates = append(rates, rt)
	}

	return rates, cards, assignments, nil
}
