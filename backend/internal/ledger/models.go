package ledger

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

var (
	ErrWalletNotFound             = errors.New("wallet not found")
	ErrWalletFrozen               = errors.New("wallet is frozen; no debits or holds permitted")
	ErrWalletClosed               = errors.New("wallet is closed")
	ErrInsufficientFunds          = errors.New("insufficient available funds")
	ErrNegativeAmount             = errors.New("amount must be strictly positive")
	ErrCurrencyMismatch           = errors.New("currency mismatch between wallet and transaction")
	ErrUnbalancedTransaction      = errors.New("double-entry transaction entries are unbalanced")
	ErrIdempotencyConflict        = errors.New("idempotency key reused with differing parameters")
	ErrHoldNotFound               = errors.New("hold not found")
	ErrHoldNotActive              = errors.New("hold is not active")
	ErrHoldExpired                = errors.New("hold has expired")
	ErrHoldCaptureExceeded        = errors.New("captured amount exceeds original hold amount")
	ErrTransactionNotFound        = errors.New("ledger transaction not found")
	ErrTransactionAlreadyReversed = errors.New("transaction has already been reversed")
	ErrReversalNotAllowed         = errors.New("transaction cannot be reversed")
	ErrReasonRequired             = errors.New("reason is mandatory for adjustments and reversals")
	ErrMakerCannotApprove         = errors.New("maker cannot approve their own change request")
	ErrCrossTenantAccess          = errors.New("access denied: resource is outside tenant boundary")
	ErrPermissionDenied           = errors.New("permission denied")
	ErrAmountExceedsMaximum       = errors.New("amount exceeds maximum allowed bound of 1,000,000.0000")
	ErrValidation                 = errors.New("validation failed")
)

// Precision scale for monetary values (4 decimal places = 0.0001)
const MoneyScale = 4

var MaxTransactionAmount = decimal.NewFromInt(1_000_000)

// Wallet represents a tenant organization's commercial wallet.
type Wallet struct {
	ID                  uuid.UUID       `json:"id"`
	OrganizationID      uuid.UUID       `json:"organization_id"`
	OrganizationName    string          `json:"organization_name,omitempty"`
	OrganizationType    string          `json:"organization_type,omitempty"`
	Currency            string          `json:"currency"`
	Status              string          `json:"status"`       // active | frozen | closed
	AccountType         string          `json:"account_type"` // prepaid | postpaid
	LowBalanceThreshold decimal.Decimal `json:"low_balance_threshold"`
	CachedBalance       decimal.Decimal `json:"cached_balance"`
	CreditLimit         decimal.Decimal `json:"credit_limit"` // Single source of truth from customer profile
	ActiveHolds         decimal.Decimal `json:"active_holds"`
	AvailableBalance    decimal.Decimal `json:"available_balance"`
	Version             int             `json:"version"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

// LedgerAccount represents a double-entry balance sheet account.
type LedgerAccount struct {
	ID        uuid.UUID  `json:"id"`
	Code      string     `json:"code"`
	Name      string     `json:"name"`
	Type      string     `json:"type"` // asset | liability | equity | revenue | expense | wallet
	Currency  string     `json:"currency"`
	WalletID  *uuid.UUID `json:"wallet_id,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// LedgerTransaction represents a financial transaction grouping at least two balanced entries.
type LedgerTransaction struct {
	ID                      uuid.UUID     `json:"id"`
	Type                    string        `json:"type"` // topup | adjustment | charge | refund | reversal | hold_capture | transfer
	ReferenceType           *string       `json:"reference_type,omitempty"`
	ReferenceID             *uuid.UUID    `json:"reference_id,omitempty"`
	IdempotencyKey          *string       `json:"idempotency_key,omitempty"`
	Description             string        `json:"description"`
	Reason                  *string       `json:"reason,omitempty"`
	ReversalOfTransactionID *uuid.UUID    `json:"reversal_of_transaction_id,omitempty"`
	TenantOrgID             *uuid.UUID    `json:"tenant_org_id,omitempty"`
	ActorUserID             *uuid.UUID    `json:"actor_user_id,omitempty"`
	ActorOrgID              *uuid.UUID    `json:"actor_org_id,omitempty"`
	CreatedAt               time.Time     `json:"created_at"`
	Entries                 []LedgerEntry `json:"entries,omitempty"`
}

// LedgerEntry represents a single debit or credit line in a balanced transaction.
type LedgerEntry struct {
	ID            uuid.UUID       `json:"id"`
	TransactionID uuid.UUID       `json:"transaction_id"`
	AccountID     uuid.UUID       `json:"account_id"`
	AccountCode   string          `json:"account_code,omitempty"`
	AccountName   string          `json:"account_name,omitempty"`
	Direction     string          `json:"direction"` // debit | credit
	Amount        decimal.Decimal `json:"amount"`
	Currency      string          `json:"currency"`
	CreatedAt     time.Time       `json:"created_at"`
}

// Hold represents a temporary funds reservation that reduces available balance.
type Hold struct {
	ID             uuid.UUID       `json:"id"`
	WalletID       uuid.UUID       `json:"wallet_id"`
	Amount         decimal.Decimal `json:"amount"`
	Currency       string          `json:"currency"`
	Status         string          `json:"status"` // active | captured | released | expired
	ExpiresAt      time.Time       `json:"expires_at"`
	ReferenceType  *string         `json:"reference_type,omitempty"`
	ReferenceID    *uuid.UUID      `json:"reference_id,omitempty"`
	IdempotencyKey *string         `json:"idempotency_key,omitempty"`
	CapturedAmount decimal.Decimal `json:"captured_amount"`
	Version        int             `json:"version"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// WalletTopupRequest is the input for manual staff top-ups.
type WalletTopupRequest struct {
	Amount         decimal.Decimal `json:"amount"`
	Reason         string          `json:"reason"`
	Reference      string          `json:"reference"`
	IdempotencyKey string          `json:"idempotency_key"`
}

// WalletAdjustmentRequest is the input for manual staff debit/credit adjustments.
type WalletAdjustmentRequest struct {
	Amount         decimal.Decimal `json:"amount"`
	Direction      string          `json:"direction"` // credit (adds funds) | debit (deducts funds)
	Reason         string          `json:"reason"`
	Reference      string          `json:"reference"`
	IdempotencyKey string          `json:"idempotency_key"`
}

// WalletTransferRequest is the input for reseller-to-customer transfers.
type WalletTransferRequest struct {
	SourceWalletID      uuid.UUID       `json:"source_wallet_id"`
	DestinationWalletID uuid.UUID       `json:"destination_wallet_id"`
	Amount              decimal.Decimal `json:"amount"`
	Reason              string          `json:"reason"`
	Reference           string          `json:"reference"`
	IdempotencyKey      string          `json:"idempotency_key"`
}

// FreezeWalletRequest is the input for wallet freezing.
type FreezeWalletRequest struct {
	Reason string `json:"reason"`
}

// UnfreezeWalletRequest is the input for wallet unfreezing.
type UnfreezeWalletRequest struct {
	Reason string `json:"reason"`
}

// ReconciliationReport records the result of recomputing wallet balance from raw entries.
type ReconciliationReport struct {
	WalletID         uuid.UUID       `json:"wallet_id"`
	OrganizationID   uuid.UUID       `json:"organization_id"`
	OrganizationName string          `json:"organization_name"`
	Currency         string          `json:"currency"`
	CachedBalance    decimal.Decimal `json:"cached_balance"`
	ComputedBalance  decimal.Decimal `json:"computed_balance"`
	Difference       decimal.Decimal `json:"difference"`
	Status           string          `json:"status"` // matched | mismatch
	EntriesCount     int             `json:"entries_count"`
	CheckedAt        time.Time       `json:"checked_at"`
}

// StatementItem represents a running-balance statement line for a wallet.
type StatementItem struct {
	EntryID         uuid.UUID       `json:"entry_id"`
	TransactionID   uuid.UUID       `json:"transaction_id"`
	TransactionType string          `json:"transaction_type"`
	Description     string          `json:"description"`
	Reason          *string         `json:"reason,omitempty"`
	ReferenceType   *string         `json:"reference_type,omitempty"`
	ReferenceID     *uuid.UUID      `json:"reference_id,omitempty"`
	Debit           decimal.Decimal `json:"debit"`
	Credit          decimal.Decimal `json:"credit"`
	NetChange       decimal.Decimal `json:"net_change"`
	RunningBalance  decimal.Decimal `json:"running_balance"`
	Currency        string          `json:"currency"`
	CreatedAt       time.Time       `json:"created_at"`
}

// StatementFilter provides filtering parameters for wallet statements.
type StatementFilter struct {
	WalletID  uuid.UUID  `json:"wallet_id"`
	StartDate *time.Time `json:"start_date,omitempty"`
	EndDate   *time.Time `json:"end_date,omitempty"`
	Page      int        `json:"page"`
	PageSize  int        `json:"page_size"`
}

// TransactionFilter provides filtering parameters for ledger transactions explorer.
type TransactionFilter struct {
	WalletID        *uuid.UUID       `json:"wallet_id,omitempty"`
	TransactionType *string          `json:"type,omitempty"`
	StartDate       *time.Time       `json:"start_date,omitempty"`
	EndDate         *time.Time       `json:"end_date,omitempty"`
	MinAmount       *decimal.Decimal `json:"min_amount,omitempty"`
	MaxAmount       *decimal.Decimal `json:"max_amount,omitempty"`
	Search          string           `json:"search,omitempty"`
	Page            int              `json:"page"`
	PageSize        int              `json:"page_size"`
}
