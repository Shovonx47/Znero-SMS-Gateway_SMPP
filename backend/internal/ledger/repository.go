package ledger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

type Repository interface {
	EnsureWalletForOrg(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, currency, accountType string) (*Wallet, error)
	GetWalletByID(ctx context.Context, id uuid.UUID) (*Wallet, error)
	GetWalletByIDForUpdate(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*Wallet, error)
	GetWalletByOrgID(ctx context.Context, orgID uuid.UUID) (*Wallet, error)
	ListWallets(ctx context.Context, tc *tenant.Context, search, status string, page, pageSize int) ([]Wallet, int, error)
	UpdateWalletStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string, expectedVersion int) error
	UpdateWalletCachedBalance(ctx context.Context, tx pgx.Tx, id uuid.UUID, newBalance decimal.Decimal, expectedVersion int) error

	GetSystemAccount(ctx context.Context, tx pgx.Tx, code string) (*LedgerAccount, error)
	GetAccountByWalletID(ctx context.Context, tx pgx.Tx, walletID uuid.UUID) (*LedgerAccount, error)
	CreateAccountForWallet(ctx context.Context, tx pgx.Tx, walletID uuid.UUID, orgName, currency string) (*LedgerAccount, error)

	GetTransactionByIdempotency(ctx context.Context, tx pgx.Tx, tenantOrgID *uuid.UUID, txType, idempotencyKey string) (*LedgerTransaction, error)
	GetTransactionByID(ctx context.Context, id uuid.UUID) (*LedgerTransaction, error)
	CreateTransaction(ctx context.Context, tx pgx.Tx, t LedgerTransaction) (*LedgerTransaction, error)
	CreateEntries(ctx context.Context, tx pgx.Tx, entries []LedgerEntry) error
	ListTransactions(ctx context.Context, tc *tenant.Context, filter TransactionFilter) ([]LedgerTransaction, int, error)

	CreateHold(ctx context.Context, tx pgx.Tx, h Hold) (*Hold, error)
	GetHoldByID(ctx context.Context, id uuid.UUID) (*Hold, error)
	GetHoldByIDForUpdate(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*Hold, error)
	GetHoldByIdempotency(ctx context.Context, tx pgx.Tx, walletID uuid.UUID, idempotencyKey string) (*Hold, error)
	UpdateHoldStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string, capturedAmount decimal.Decimal, expectedVersion int) error
	GetActiveHoldsSum(ctx context.Context, walletID uuid.UUID) (decimal.Decimal, error)
	ListHoldsByWallet(ctx context.Context, walletID uuid.UUID, status string) ([]Hold, error)
	ExpireActiveHolds(ctx context.Context, now time.Time) (int64, error)

	GetWalletStatement(ctx context.Context, filter StatementFilter) ([]StatementItem, int, error)
	ComputeWalletBalanceFromEntries(ctx context.Context, walletID uuid.UUID) (decimal.Decimal, int, error)
	ListAllWalletsForReconciliation(ctx context.Context) ([]Wallet, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (r *pgRepository) dbOrPool(tx pgx.Tx) queryRower {
	if tx != nil {
		return tx
	}
	return r.pool
}

func (r *pgRepository) EnsureWalletForOrg(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, currency, accountType string) (*Wallet, error) {
	if currency == "" {
		currency = "USD"
	}
	if accountType == "" {
		accountType = "prepaid"
	}

	query := `
		INSERT INTO wallets (organization_id, currency, status, account_type, cached_balance, version)
		VALUES ($1, $2, 'active', $3, 0, 1)
		ON CONFLICT (organization_id) DO UPDATE
		SET updated_at = now()
		RETURNING id, organization_id, currency, status, account_type, low_balance_threshold, cached_balance, version, created_at, updated_at
	`
	var w Wallet
	var err error
	if tx != nil {
		err = tx.QueryRow(ctx, query, orgID, currency, accountType).Scan(
			&w.ID, &w.OrganizationID, &w.Currency, &w.Status, &w.AccountType,
			&w.LowBalanceThreshold, &w.CachedBalance, &w.Version, &w.CreatedAt, &w.UpdatedAt,
		)
	} else {
		err = r.pool.QueryRow(ctx, query, orgID, currency, accountType).Scan(
			&w.ID, &w.OrganizationID, &w.Currency, &w.Status, &w.AccountType,
			&w.LowBalanceThreshold, &w.CachedBalance, &w.Version, &w.CreatedAt, &w.UpdatedAt,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("ensure wallet: %w", err)
	}

	// Ensure ledger account exists for this wallet
	_, err = r.CreateAccountForWallet(ctx, tx, w.ID, "", w.Currency)
	if err != nil {
		return nil, fmt.Errorf("ensure wallet account: %w", err)
	}

	return &w, nil
}

func (r *pgRepository) GetWalletByID(ctx context.Context, id uuid.UUID) (*Wallet, error) {
	query := `
		SELECT w.id, w.organization_id, o.name, o.type, w.currency, w.status, w.account_type,
		       w.low_balance_threshold, w.cached_balance, COALESCE(cp.credit_limit, 0),
		       w.version, w.created_at, w.updated_at
		FROM wallets w
		JOIN organizations o ON o.id = w.organization_id
		LEFT JOIN customer_profiles cp ON cp.organization_id = w.organization_id
		WHERE w.id = $1
	`
	var w Wallet
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&w.ID, &w.OrganizationID, &w.OrganizationName, &w.OrganizationType, &w.Currency, &w.Status, &w.AccountType,
		&w.LowBalanceThreshold, &w.CachedBalance, &w.CreditLimit,
		&w.Version, &w.CreatedAt, &w.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrWalletNotFound
		}
		return nil, fmt.Errorf("get wallet by id: %w", err)
	}

	holdsSum, err := r.GetActiveHoldsSum(ctx, w.ID)
	if err != nil {
		return nil, err
	}
	w.ActiveHolds = holdsSum

	// Available balance calculation:
	// For prepaid: cached_balance - active_holds
	// For postpaid: cached_balance - active_holds + credit_limit
	avail := w.CachedBalance.Sub(w.ActiveHolds)
	if w.AccountType == "postpaid" {
		avail = avail.Add(w.CreditLimit)
	}
	w.AvailableBalance = avail

	return &w, nil
}

func (r *pgRepository) GetWalletByIDForUpdate(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*Wallet, error) {
	query := `
		SELECT w.id, w.organization_id, o.name, o.type, w.currency, w.status, w.account_type,
		       w.low_balance_threshold, w.cached_balance, COALESCE(cp.credit_limit, 0),
		       w.version, w.created_at, w.updated_at
		FROM wallets w
		JOIN organizations o ON o.id = w.organization_id
		LEFT JOIN customer_profiles cp ON cp.organization_id = w.organization_id
		WHERE w.id = $1
		FOR UPDATE OF w
	`
	var w Wallet
	err := tx.QueryRow(ctx, query, id).Scan(
		&w.ID, &w.OrganizationID, &w.OrganizationName, &w.OrganizationType, &w.Currency, &w.Status, &w.AccountType,
		&w.LowBalanceThreshold, &w.CachedBalance, &w.CreditLimit,
		&w.Version, &w.CreatedAt, &w.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrWalletNotFound
		}
		return nil, fmt.Errorf("get wallet for update: %w", err)
	}

	// Active holds sum inside transaction
	var holdsSum decimal.Decimal
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount - captured_amount), 0)
		FROM holds
		WHERE wallet_id = $1 AND status = 'active'
	`, w.ID).Scan(&holdsSum)
	if err != nil {
		return nil, fmt.Errorf("get holds for update: %w", err)
	}
	w.ActiveHolds = holdsSum

	avail := w.CachedBalance.Sub(w.ActiveHolds)
	if w.AccountType == "postpaid" {
		avail = avail.Add(w.CreditLimit)
	}
	w.AvailableBalance = avail

	return &w, nil
}

func (r *pgRepository) GetWalletByOrgID(ctx context.Context, orgID uuid.UUID) (*Wallet, error) {
	query := `
		SELECT w.id, w.organization_id, o.name, o.type, w.currency, w.status, w.account_type,
		       w.low_balance_threshold, w.cached_balance, COALESCE(cp.credit_limit, 0),
		       w.version, w.created_at, w.updated_at
		FROM wallets w
		JOIN organizations o ON o.id = w.organization_id
		LEFT JOIN customer_profiles cp ON cp.organization_id = w.organization_id
		WHERE w.organization_id = $1
	`
	var w Wallet
	err := r.pool.QueryRow(ctx, query, orgID).Scan(
		&w.ID, &w.OrganizationID, &w.OrganizationName, &w.OrganizationType, &w.Currency, &w.Status, &w.AccountType,
		&w.LowBalanceThreshold, &w.CachedBalance, &w.CreditLimit,
		&w.Version, &w.CreatedAt, &w.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrWalletNotFound
		}
		return nil, fmt.Errorf("get wallet by org id: %w", err)
	}

	holdsSum, err := r.GetActiveHoldsSum(ctx, w.ID)
	if err != nil {
		return nil, err
	}
	w.ActiveHolds = holdsSum

	avail := w.CachedBalance.Sub(w.ActiveHolds)
	if w.AccountType == "postpaid" {
		avail = avail.Add(w.CreditLimit)
	}
	w.AvailableBalance = avail

	return &w, nil
}

func (r *pgRepository) ListWallets(ctx context.Context, tc *tenant.Context, search, status string, page, pageSize int) ([]Wallet, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	var conditions []string
	var args []any
	argIdx := 1

	// Tenancy filter:
	// Platform sees all.
	// Reseller sees own wallet + direct customer wallets (materialized path match).
	// Customer sees own wallet only.
	if !tc.IsPlatformAdmin && !tc.IsPlatformOrg() {
		conditions = append(conditions, fmt.Sprintf("o.path LIKE $%d || '%%'", argIdx))
		args = append(args, tc.ActorOrgPath)
		argIdx++
	}

	if status != "" {
		conditions = append(conditions, fmt.Sprintf("w.status = $%d", argIdx))
		args = append(args, status)
		argIdx++
	}

	if search != "" {
		conditions = append(conditions, fmt.Sprintf("(o.name ILIKE $%d OR w.currency ILIKE $%d)", argIdx, argIdx))
		args = append(args, "%"+search+"%")
		argIdx++
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	countQuery := fmt.Sprintf(`
		SELECT count(*)
		FROM wallets w
		JOIN organizations o ON o.id = w.organization_id
		%s
	`, whereClause)

	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count wallets: %w", err)
	}

	selectQuery := fmt.Sprintf(`
		SELECT w.id, w.organization_id, o.name, o.type, w.currency, w.status, w.account_type,
		       w.low_balance_threshold, w.cached_balance, COALESCE(cp.credit_limit, 0),
		       COALESCE(h.active_holds, 0) as active_holds,
		       w.version, w.created_at, w.updated_at
		FROM wallets w
		JOIN organizations o ON o.id = w.organization_id
		LEFT JOIN customer_profiles cp ON cp.organization_id = w.organization_id
		LEFT JOIN (
			SELECT wallet_id, SUM(amount - captured_amount) as active_holds
			FROM holds
			WHERE status = 'active'
			GROUP BY wallet_id
		) h ON h.wallet_id = w.id
		%s
		ORDER BY o.name ASC
		LIMIT $%d OFFSET $%d
	`, whereClause, argIdx, argIdx+1)

	args = append(args, pageSize, offset)
	rows, err := r.pool.Query(ctx, selectQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list wallets: %w", err)
	}
	defer rows.Close()

	var wallets []Wallet
	for rows.Next() {
		var w Wallet
		if err := rows.Scan(
			&w.ID, &w.OrganizationID, &w.OrganizationName, &w.OrganizationType, &w.Currency, &w.Status, &w.AccountType,
			&w.LowBalanceThreshold, &w.CachedBalance, &w.CreditLimit, &w.ActiveHolds,
			&w.Version, &w.CreatedAt, &w.UpdatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan wallet: %w", err)
		}
		avail := w.CachedBalance.Sub(w.ActiveHolds)
		if w.AccountType == "postpaid" {
			avail = avail.Add(w.CreditLimit)
		}
		w.AvailableBalance = avail
		wallets = append(wallets, w)
	}

	return wallets, total, nil
}

func (r *pgRepository) UpdateWalletStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string, expectedVersion int) error {
	query := `
		UPDATE wallets
		SET status = $1, version = version + 1, updated_at = now()
		WHERE id = $2 AND version = $3
	`
	var tag any
	var err error
	if tx != nil {
		tag, err = tx.Exec(ctx, query, status, id, expectedVersion)
	} else {
		tag, err = r.pool.Exec(ctx, query, status, id, expectedVersion)
	}
	if err != nil {
		return fmt.Errorf("update wallet status: %w", err)
	}
	if ct, ok := tag.(interface{ RowsAffected() int64 }); ok && ct.RowsAffected() == 0 {
		return errors.New("optimistic lock conflict or wallet not found")
	}
	return nil
}

func (r *pgRepository) UpdateWalletCachedBalance(ctx context.Context, tx pgx.Tx, id uuid.UUID, newBalance decimal.Decimal, expectedVersion int) error {
	query := `
		UPDATE wallets
		SET cached_balance = $1, version = version + 1, updated_at = now()
		WHERE id = $2 AND version = $3
	`
	var tag any
	var err error
	if tx != nil {
		tag, err = tx.Exec(ctx, query, newBalance, id, expectedVersion)
	} else {
		tag, err = r.pool.Exec(ctx, query, newBalance, id, expectedVersion)
	}
	if err != nil {
		return fmt.Errorf("update wallet cached balance: %w", err)
	}
	if ct, ok := tag.(interface{ RowsAffected() int64 }); ok && ct.RowsAffected() == 0 {
		return errors.New("optimistic lock conflict or wallet not found")
	}
	return nil
}

func (r *pgRepository) GetSystemAccount(ctx context.Context, tx pgx.Tx, code string) (*LedgerAccount, error) {
	query := `
		SELECT id, code, name, type, currency, wallet_id, created_at
		FROM ledger_accounts
		WHERE code = $1
	`
	var a LedgerAccount
	err := r.dbOrPool(tx).QueryRow(ctx, query, code).Scan(
		&a.ID, &a.Code, &a.Name, &a.Type, &a.Currency, &a.WalletID, &a.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("system account %q not found", code)
		}
		return nil, fmt.Errorf("get system account: %w", err)
	}
	return &a, nil
}

func (r *pgRepository) GetAccountByWalletID(ctx context.Context, tx pgx.Tx, walletID uuid.UUID) (*LedgerAccount, error) {
	query := `
		SELECT id, code, name, type, currency, wallet_id, created_at
		FROM ledger_accounts
		WHERE wallet_id = $1
	`
	var a LedgerAccount
	err := r.dbOrPool(tx).QueryRow(ctx, query, walletID).Scan(
		&a.ID, &a.Code, &a.Name, &a.Type, &a.Currency, &a.WalletID, &a.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("ledger account for wallet %s not found", walletID)
		}
		return nil, fmt.Errorf("get account by wallet id: %w", err)
	}
	return &a, nil
}

func (r *pgRepository) CreateAccountForWallet(ctx context.Context, tx pgx.Tx, walletID uuid.UUID, orgName, currency string) (*LedgerAccount, error) {
	existing, err := r.GetAccountByWalletID(ctx, tx, walletID)
	if err == nil && existing != nil {
		return existing, nil
	}

	code := fmt.Sprintf("WALLET_%s", strings.ReplaceAll(walletID.String(), "-", "_"))
	name := fmt.Sprintf("Wallet Account %s (%s)", walletID.String()[:8], currency)
	if orgName != "" {
		name = fmt.Sprintf("Wallet Account - %s (%s)", orgName, currency)
	}

	query := `
		INSERT INTO ledger_accounts (code, name, type, currency, wallet_id)
		VALUES ($1, $2, 'wallet', $3, $4)
		ON CONFLICT (wallet_id) DO NOTHING
		RETURNING id, code, name, type, currency, wallet_id, created_at
	`
	var a LedgerAccount
	exec := r.dbOrPool(tx)
	err = exec.QueryRow(ctx, query, code, name, currency, walletID).Scan(
		&a.ID, &a.Code, &a.Name, &a.Type, &a.Currency, &a.WalletID, &a.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return r.GetAccountByWalletID(ctx, tx, walletID)
		}
		return nil, fmt.Errorf("create wallet ledger account: %w", err)
	}
	return &a, nil
}

func (r *pgRepository) GetTransactionByIdempotency(ctx context.Context, tx pgx.Tx, tenantOrgID *uuid.UUID, txType, idempotencyKey string) (*LedgerTransaction, error) {
	query := `
		SELECT id, type, reference_type, reference_id, idempotency_key, description, reason,
		       reversal_of_transaction_id, tenant_org_id, actor_user_id, actor_org_id, created_at
		FROM ledger_transactions
		WHERE type = $1 AND idempotency_key = $2
	`
	var args []any
	args = append(args, txType, idempotencyKey)
	if tenantOrgID != nil {
		query += " AND tenant_org_id = $3"
		args = append(args, *tenantOrgID)
	} else {
		query += " AND tenant_org_id IS NULL"
	}

	var lt LedgerTransaction
	err := r.dbOrPool(tx).QueryRow(ctx, query, args...).Scan(
		&lt.ID, &lt.Type, &lt.ReferenceType, &lt.ReferenceID, &lt.IdempotencyKey, &lt.Description,
		&lt.Reason, &lt.ReversalOfTransactionID, &lt.TenantOrgID, &lt.ActorUserID, &lt.ActorOrgID, &lt.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get transaction by idempotency: %w", err)
	}

	entries, err := r.getEntriesForTransaction(ctx, tx, lt.ID)
	if err != nil {
		return nil, err
	}
	lt.Entries = entries

	return &lt, nil
}

func (r *pgRepository) GetTransactionByID(ctx context.Context, id uuid.UUID) (*LedgerTransaction, error) {
	query := `
		SELECT id, type, reference_type, reference_id, idempotency_key, description, reason,
		       reversal_of_transaction_id, tenant_org_id, actor_user_id, actor_org_id, created_at
		FROM ledger_transactions
		WHERE id = $1
	`
	var tx LedgerTransaction
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&tx.ID, &tx.Type, &tx.ReferenceType, &tx.ReferenceID, &tx.IdempotencyKey, &tx.Description,
		&tx.Reason, &tx.ReversalOfTransactionID, &tx.TenantOrgID, &tx.ActorUserID, &tx.ActorOrgID, &tx.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTransactionNotFound
		}
		return nil, fmt.Errorf("get transaction by id: %w", err)
	}

	entries, err := r.getEntriesForTransaction(ctx, nil, tx.ID)
	if err != nil {
		return nil, err
	}
	tx.Entries = entries

	return &tx, nil
}

func (r *pgRepository) getEntriesForTransaction(ctx context.Context, tx pgx.Tx, txID uuid.UUID) ([]LedgerEntry, error) {
	query := `
		SELECT e.id, e.transaction_id, e.account_id, a.code, a.name, e.direction, e.amount, e.currency, e.created_at
		FROM ledger_entries e
		JOIN ledger_accounts a ON a.id = e.account_id
		WHERE e.transaction_id = $1
		ORDER BY e.direction DESC, e.id ASC
	`
	rows, err := r.dbOrPool(tx).Query(ctx, query, txID)
	if err != nil {
		return nil, fmt.Errorf("query transaction entries: %w", err)
	}
	defer rows.Close()

	var entries []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		if err := rows.Scan(
			&e.ID, &e.TransactionID, &e.AccountID, &e.AccountCode, &e.AccountName,
			&e.Direction, &e.Amount, &e.Currency, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func (r *pgRepository) CreateTransaction(ctx context.Context, tx pgx.Tx, t LedgerTransaction) (*LedgerTransaction, error) {
	query := `
		INSERT INTO ledger_transactions (
			type, reference_type, reference_id, idempotency_key, description, reason,
			reversal_of_transaction_id, tenant_org_id, actor_user_id, actor_org_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id, type, reference_type, reference_id, idempotency_key, description, reason,
		          reversal_of_transaction_id, tenant_org_id, actor_user_id, actor_org_id, created_at
	`
	var out LedgerTransaction
	err := tx.QueryRow(ctx, query,
		t.Type, t.ReferenceType, t.ReferenceID, t.IdempotencyKey, t.Description, t.Reason,
		t.ReversalOfTransactionID, t.TenantOrgID, t.ActorUserID, t.ActorOrgID,
	).Scan(
		&out.ID, &out.Type, &out.ReferenceType, &out.ReferenceID, &out.IdempotencyKey, &out.Description,
		&out.Reason, &out.ReversalOfTransactionID, &out.TenantOrgID, &out.ActorUserID, &out.ActorOrgID, &out.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create transaction: %w", err)
	}
	return &out, nil
}

func (r *pgRepository) CreateEntries(ctx context.Context, tx pgx.Tx, entries []LedgerEntry) error {
	for _, e := range entries {
		query := `
			INSERT INTO ledger_entries (transaction_id, account_id, direction, amount, currency)
			VALUES ($1, $2, $3, $4, $5)
		`
		_, err := tx.Exec(ctx, query, e.TransactionID, e.AccountID, e.Direction, e.Amount, e.Currency)
		if err != nil {
			return fmt.Errorf("create ledger entry: %w", err)
		}
	}
	return nil
}

func (r *pgRepository) ListTransactions(ctx context.Context, tc *tenant.Context, filter TransactionFilter) ([]LedgerTransaction, int, error) {
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PageSize < 1 || filter.PageSize > 100 {
		filter.PageSize = 20
	}
	offset := (filter.Page - 1) * filter.PageSize

	var conditions []string
	var args []any
	argIdx := 1

	// Scoping
	if !tc.IsPlatformAdmin && !tc.IsPlatformOrg() {
		// Non-platform actor can only see transactions touching their accessible organizations
		conditions = append(conditions, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM ledger_entries le
			JOIN ledger_accounts la ON la.id = le.account_id
			JOIN wallets w ON w.id = la.wallet_id
			JOIN organizations o ON o.id = w.organization_id
			WHERE le.transaction_id = lt.id AND o.path LIKE $%d || '%%'
		)`, argIdx))
		args = append(args, tc.ActorOrgPath)
		argIdx++
	}

	if filter.WalletID != nil {
		conditions = append(conditions, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM ledger_entries le
			JOIN ledger_accounts la ON la.id = le.account_id
			WHERE le.transaction_id = lt.id AND la.wallet_id = $%d
		)`, argIdx))
		args = append(args, *filter.WalletID)
		argIdx++
	}

	if filter.TransactionType != nil && *filter.TransactionType != "" {
		conditions = append(conditions, fmt.Sprintf("lt.type = $%d", argIdx))
		args = append(args, *filter.TransactionType)
		argIdx++
	}

	if filter.StartDate != nil {
		conditions = append(conditions, fmt.Sprintf("lt.created_at >= $%d", argIdx))
		args = append(args, *filter.StartDate)
		argIdx++
	}

	if filter.EndDate != nil {
		conditions = append(conditions, fmt.Sprintf("lt.created_at <= $%d", argIdx))
		args = append(args, *filter.EndDate)
		argIdx++
	}

	if filter.Search != "" {
		conditions = append(conditions, fmt.Sprintf("(lt.description ILIKE $%d OR COALESCE(lt.reason, '') ILIKE $%d)", argIdx, argIdx))
		args = append(args, "%"+filter.Search+"%")
		argIdx++
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	countQuery := fmt.Sprintf("SELECT count(*) FROM ledger_transactions lt %s", whereClause)
	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count transactions: %w", err)
	}

	selectQuery := fmt.Sprintf(`
		SELECT lt.id, lt.type, lt.reference_type, lt.reference_id, lt.idempotency_key, lt.description, lt.reason,
		       lt.reversal_of_transaction_id, lt.tenant_org_id, lt.actor_user_id, lt.actor_org_id, lt.created_at
		FROM ledger_transactions lt
		%s
		ORDER BY lt.created_at DESC
		LIMIT $%d OFFSET $%d
	`, whereClause, argIdx, argIdx+1)

	args = append(args, filter.PageSize, offset)
	rows, err := r.pool.Query(ctx, selectQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list transactions: %w", err)
	}
	defer rows.Close()

	var txs []LedgerTransaction
	for rows.Next() {
		var tx LedgerTransaction
		if err := rows.Scan(
			&tx.ID, &tx.Type, &tx.ReferenceType, &tx.ReferenceID, &tx.IdempotencyKey, &tx.Description,
			&tx.Reason, &tx.ReversalOfTransactionID, &tx.TenantOrgID, &tx.ActorUserID, &tx.ActorOrgID, &tx.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan transaction: %w", err)
		}
		entries, err := r.getEntriesForTransaction(ctx, nil, tx.ID)
		if err != nil {
			return nil, 0, err
		}
		tx.Entries = entries
		txs = append(txs, tx)
	}

	return txs, total, nil
}

func (r *pgRepository) CreateHold(ctx context.Context, tx pgx.Tx, h Hold) (*Hold, error) {
	query := `
		INSERT INTO holds (wallet_id, amount, currency, status, expires_at, reference_type, reference_id, idempotency_key, captured_amount, version)
		VALUES ($1, $2, $3, 'active', $4, $5, $6, $7, 0, 1)
		RETURNING id, wallet_id, amount, currency, status, expires_at, reference_type, reference_id, idempotency_key, captured_amount, version, created_at, updated_at
	`
	var out Hold
	err := tx.QueryRow(ctx, query,
		h.WalletID, h.Amount, h.Currency, h.ExpiresAt, h.ReferenceType, h.ReferenceID, h.IdempotencyKey,
	).Scan(
		&out.ID, &out.WalletID, &out.Amount, &out.Currency, &out.Status, &out.ExpiresAt,
		&out.ReferenceType, &out.ReferenceID, &out.IdempotencyKey, &out.CapturedAmount,
		&out.Version, &out.CreatedAt, &out.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create hold: %w", err)
	}
	return &out, nil
}

func (r *pgRepository) GetHoldByID(ctx context.Context, id uuid.UUID) (*Hold, error) {
	query := `
		SELECT id, wallet_id, amount, currency, status, expires_at, reference_type, reference_id, idempotency_key, captured_amount, version, created_at, updated_at
		FROM holds
		WHERE id = $1
	`
	var h Hold
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&h.ID, &h.WalletID, &h.Amount, &h.Currency, &h.Status, &h.ExpiresAt,
		&h.ReferenceType, &h.ReferenceID, &h.IdempotencyKey, &h.CapturedAmount,
		&h.Version, &h.CreatedAt, &h.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrHoldNotFound
		}
		return nil, fmt.Errorf("get hold: %w", err)
	}
	return &h, nil
}

func (r *pgRepository) GetHoldByIDForUpdate(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*Hold, error) {
	query := `
		SELECT id, wallet_id, amount, currency, status, expires_at, reference_type, reference_id, idempotency_key, captured_amount, version, created_at, updated_at
		FROM holds
		WHERE id = $1
		FOR UPDATE
	`
	var h Hold
	err := tx.QueryRow(ctx, query, id).Scan(
		&h.ID, &h.WalletID, &h.Amount, &h.Currency, &h.Status, &h.ExpiresAt,
		&h.ReferenceType, &h.ReferenceID, &h.IdempotencyKey, &h.CapturedAmount,
		&h.Version, &h.CreatedAt, &h.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrHoldNotFound
		}
		return nil, fmt.Errorf("get hold for update: %w", err)
	}
	return &h, nil
}

func (r *pgRepository) GetHoldByIdempotency(ctx context.Context, tx pgx.Tx, walletID uuid.UUID, idempotencyKey string) (*Hold, error) {
	query := `
		SELECT id, wallet_id, amount, currency, status, expires_at, reference_type, reference_id, idempotency_key, captured_amount, version, created_at, updated_at
		FROM holds
		WHERE wallet_id = $1 AND idempotency_key = $2
	`
	var h Hold
	err := r.dbOrPool(tx).QueryRow(ctx, query, walletID, idempotencyKey).Scan(
		&h.ID, &h.WalletID, &h.Amount, &h.Currency, &h.Status, &h.ExpiresAt,
		&h.ReferenceType, &h.ReferenceID, &h.IdempotencyKey, &h.CapturedAmount,
		&h.Version, &h.CreatedAt, &h.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get hold by idempotency: %w", err)
	}
	return &h, nil
}

func (r *pgRepository) UpdateHoldStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string, capturedAmount decimal.Decimal, expectedVersion int) error {
	query := `
		UPDATE holds
		SET status = $1, captured_amount = $2, version = version + 1, updated_at = now()
		WHERE id = $3 AND version = $4
	`
	tag, err := tx.Exec(ctx, query, status, capturedAmount, id, expectedVersion)
	if err != nil {
		return fmt.Errorf("update hold status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("optimistic lock conflict or hold not found")
	}
	return nil
}

func (r *pgRepository) GetActiveHoldsSum(ctx context.Context, walletID uuid.UUID) (decimal.Decimal, error) {
	query := `
		SELECT COALESCE(SUM(amount - captured_amount), 0)
		FROM holds
		WHERE wallet_id = $1 AND status = 'active'
	`
	var sum decimal.Decimal
	err := r.pool.QueryRow(ctx, query, walletID).Scan(&sum)
	if err != nil {
		return decimal.Zero, fmt.Errorf("get active holds sum: %w", err)
	}
	return sum, nil
}

func (r *pgRepository) ListHoldsByWallet(ctx context.Context, walletID uuid.UUID, status string) ([]Hold, error) {
	query := `
		SELECT id, wallet_id, amount, currency, status, expires_at, reference_type, reference_id, idempotency_key, captured_amount, version, created_at, updated_at
		FROM holds
		WHERE wallet_id = $1
	`
	var args []any
	args = append(args, walletID)
	if status != "" {
		query += " AND status = $2"
		args = append(args, status)
	}
	query += " ORDER BY created_at DESC"

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list holds: %w", err)
	}
	defer rows.Close()

	var holds []Hold
	for rows.Next() {
		var h Hold
		if err := rows.Scan(
			&h.ID, &h.WalletID, &h.Amount, &h.Currency, &h.Status, &h.ExpiresAt,
			&h.ReferenceType, &h.ReferenceID, &h.IdempotencyKey, &h.CapturedAmount,
			&h.Version, &h.CreatedAt, &h.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan hold: %w", err)
		}
		holds = append(holds, h)
	}
	return holds, nil
}

func (r *pgRepository) ExpireActiveHolds(ctx context.Context, now time.Time) (int64, error) {
	query := `
		UPDATE holds
		SET status = 'expired', updated_at = now(), version = version + 1
		WHERE status = 'active' AND expires_at < $1
	`
	tag, err := r.pool.Exec(ctx, query, now)
	if err != nil {
		return 0, fmt.Errorf("expire active holds: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (r *pgRepository) GetWalletStatement(ctx context.Context, filter StatementFilter) ([]StatementItem, int, error) {
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PageSize < 1 || filter.PageSize > 1000 {
		filter.PageSize = 50
	}
	offset := (filter.Page - 1) * filter.PageSize

	// Get wallet account id
	acc, err := r.GetAccountByWalletID(ctx, nil, filter.WalletID)
	if err != nil {
		return nil, 0, err
	}

	var conditions []string
	var args []any
	args = append(args, acc.ID)
	argIdx := 2

	if filter.StartDate != nil {
		conditions = append(conditions, fmt.Sprintf("le.created_at >= $%d", argIdx))
		args = append(args, *filter.StartDate)
		argIdx++
	}

	if filter.EndDate != nil {
		conditions = append(conditions, fmt.Sprintf("le.created_at <= $%d", argIdx))
		args = append(args, *filter.EndDate)
		argIdx++
	}

	whereExtra := ""
	if len(conditions) > 0 {
		whereExtra = " AND " + strings.Join(conditions, " AND ")
	}

	countQuery := fmt.Sprintf(`
		SELECT count(*)
		FROM ledger_entries le
		WHERE le.account_id = $1 %s
	`, whereExtra)

	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count statement items: %w", err)
	}

	// Running balance query using Window function over the wallet's account:
	// For wallet (liability) accounts:
	// Credits increase balance (+), Debits decrease balance (-)
	statementQuery := fmt.Sprintf(`
		WITH wallet_entries AS (
			SELECT le.id as entry_id,
			       le.transaction_id,
			       lt.type as transaction_type,
			       lt.description,
			       lt.reason,
			       lt.reference_type,
			       lt.reference_id,
			       CASE WHEN le.direction = 'debit' THEN le.amount ELSE 0 END as debit,
			       CASE WHEN le.direction = 'credit' THEN le.amount ELSE 0 END as credit,
			       CASE WHEN le.direction = 'credit' THEN le.amount ELSE -le.amount END as net_change,
			       SUM(CASE WHEN le.direction = 'credit' THEN le.amount ELSE -le.amount END)
			           OVER (PARTITION BY le.account_id ORDER BY le.created_at ASC, le.id ASC) as running_balance,
			       le.currency,
			       le.created_at
			FROM ledger_entries le
			JOIN ledger_transactions lt ON lt.id = le.transaction_id
			WHERE le.account_id = $1
		)
		SELECT entry_id, transaction_id, transaction_type, description, reason, reference_type, reference_id,
		       debit, credit, net_change, running_balance, currency, created_at
		FROM wallet_entries le
		WHERE 1=1 %s
		ORDER BY created_at DESC, entry_id DESC
		LIMIT $%d OFFSET $%d
	`, whereExtra, argIdx, argIdx+1)

	args = append(args, filter.PageSize, offset)
	rows, err := r.pool.Query(ctx, statementQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("query statement: %w", err)
	}
	defer rows.Close()

	var items []StatementItem
	for rows.Next() {
		var it StatementItem
		if err := rows.Scan(
			&it.EntryID, &it.TransactionID, &it.TransactionType, &it.Description, &it.Reason,
			&it.ReferenceType, &it.ReferenceID, &it.Debit, &it.Credit, &it.NetChange,
			&it.RunningBalance, &it.Currency, &it.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan statement item: %w", err)
		}
		items = append(items, it)
	}

	return items, total, nil
}

func (r *pgRepository) ComputeWalletBalanceFromEntries(ctx context.Context, walletID uuid.UUID) (decimal.Decimal, int, error) {
	acc, err := r.GetAccountByWalletID(ctx, nil, walletID)
	if err != nil {
		return decimal.Zero, 0, err
	}

	query := `
		SELECT COALESCE(SUM(CASE WHEN direction = 'credit' THEN amount ELSE -amount END), 0) as balance,
		       count(*) as count
		FROM ledger_entries
		WHERE account_id = $1
	`
	var balance decimal.Decimal
	var count int
	if err := r.pool.QueryRow(ctx, query, acc.ID).Scan(&balance, &count); err != nil {
		return decimal.Zero, 0, fmt.Errorf("compute balance from entries: %w", err)
	}

	return balance, count, nil
}

func (r *pgRepository) ListAllWalletsForReconciliation(ctx context.Context) ([]Wallet, error) {
	query := `
		SELECT w.id, w.organization_id, o.name, o.type, w.currency, w.status, w.account_type,
		       w.low_balance_threshold, w.cached_balance, COALESCE(cp.credit_limit, 0),
		       w.version, w.created_at, w.updated_at
		FROM wallets w
		JOIN organizations o ON o.id = w.organization_id
		LEFT JOIN customer_profiles cp ON cp.organization_id = w.organization_id
		ORDER BY o.name ASC
	`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list all wallets: %w", err)
	}
	defer rows.Close()

	var wallets []Wallet
	for rows.Next() {
		var w Wallet
		if err := rows.Scan(
			&w.ID, &w.OrganizationID, &w.OrganizationName, &w.OrganizationType, &w.Currency, &w.Status, &w.AccountType,
			&w.LowBalanceThreshold, &w.CachedBalance, &w.CreditLimit,
			&w.Version, &w.CreatedAt, &w.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan wallet: %w", err)
		}
		wallets = append(wallets, w)
	}
	return wallets, nil
}
