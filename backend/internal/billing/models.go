package billing

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

var (
	ErrInvoiceNotFound          = errors.New("invoice not found")
	ErrInvoiceNotDraft          = errors.New("invoice is not in draft status; modification prohibited")
	ErrInvoiceAlreadyIssued     = errors.New("invoice is already issued")
	ErrInvoiceAlreadyPaid       = errors.New("invoice is already fully paid")
	ErrInvoiceAlreadyVoid       = errors.New("invoice is already void")
	ErrInvoiceCannotVoid        = errors.New("only issued invoices can be voided")
	ErrInvoiceCannotPay         = errors.New("cannot record payment: invoice is void, draft, or already paid")
	ErrPaymentExceedsTotal      = errors.New("payment amount exceeds outstanding invoice balance")
	ErrPaymentNotFound          = errors.New("payment record not found")
	ErrLineItemsRequired        = errors.New("invoice must have at least one line item")
	ErrNegativeAmount           = errors.New("amount must be strictly positive")
	ErrAmountExceedsMaximum     = errors.New("amount exceeds maximum allowed bound of 1,000,000.0000")
	ErrCrossTenantAccess        = errors.New("access denied: resource is outside tenant boundary")
	ErrPermissionDenied         = errors.New("permission denied")
	ErrVoidReasonRequired       = errors.New("reason is mandatory to void an issued invoice")
	ErrCreditNoteReasonRequired = errors.New("reason is mandatory to issue a credit note")
)

var MaxTransactionAmount = decimal.NewFromInt(1_000_000)

type Invoice struct {
	ID                         uuid.UUID       `json:"id"`
	CustomerOrgID              uuid.UUID       `json:"customer_org_id"`
	CustomerOrgName            string          `json:"customer_org_name,omitempty"`
	BillingPeriodID            *uuid.UUID      `json:"billing_period_id,omitempty"`
	Currency                   string          `json:"currency"`
	Status                     string          `json:"status"` // draft | issued | paid | void
	InvoiceNumber              *string         `json:"invoice_number,omitempty"`
	IssueDate                  *time.Time      `json:"issue_date,omitempty"`
	DueDate                    *time.Time      `json:"due_date,omitempty"`
	Subtotal                   decimal.Decimal `json:"subtotal"`
	TaxLabel                   string          `json:"tax_label"`
	TaxAmount                  decimal.Decimal `json:"tax_amount"`
	Total                      decimal.Decimal `json:"total"`
	AmountPaid                 decimal.Decimal `json:"amount_paid"`
	OutstandingBalance         decimal.Decimal `json:"outstanding_balance"`
	Notes                      string          `json:"notes"`
	VoidReason                 *string         `json:"void_reason,omitempty"`
	CreditNoteForInvoiceID     *uuid.UUID      `json:"credit_note_for_invoice_id,omitempty"`
	CreditNoteForInvoiceNumber *string         `json:"credit_note_for_invoice_number,omitempty"`
	Version                    int             `json:"version"`
	CreatedBy                  *uuid.UUID      `json:"created_by,omitempty"`
	CreatedAt                  time.Time       `json:"created_at"`
	UpdatedAt                  time.Time       `json:"updated_at"`
	Lines                      []InvoiceLine   `json:"lines,omitempty"`
}

type InvoiceLine struct {
	ID             uuid.UUID       `json:"id"`
	InvoiceID      uuid.UUID       `json:"invoice_id"`
	LineNumber     int             `json:"line_number"`
	Description    string          `json:"description"`
	Quantity       decimal.Decimal `json:"quantity"`
	UnitPrice      decimal.Decimal `json:"unit_price"`
	Amount         decimal.Decimal `json:"amount"`
	LedgerChargeID *uuid.UUID      `json:"ledger_charge_id,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

type CreateLineRequest struct {
	Description    string          `json:"description"`
	Quantity       decimal.Decimal `json:"quantity"`
	UnitPrice      decimal.Decimal `json:"unit_price"`
	LedgerChargeID *uuid.UUID      `json:"ledger_charge_id,omitempty"`
}

type CreateDraftInvoiceRequest struct {
	CustomerOrgID   uuid.UUID           `json:"customer_org_id"`
	BillingPeriodID *uuid.UUID          `json:"billing_period_id,omitempty"`
	Currency        string              `json:"currency"`
	DueDate         *time.Time          `json:"due_date,omitempty"`
	TaxLabel        string              `json:"tax_label"`
	TaxAmount       decimal.Decimal     `json:"tax_amount"`
	Notes           string              `json:"notes"`
	Lines           []CreateLineRequest `json:"lines"`
}

type UpdateDraftInvoiceRequest struct {
	Currency  *string              `json:"currency,omitempty"`
	DueDate   *time.Time           `json:"due_date,omitempty"`
	TaxLabel  *string              `json:"tax_label,omitempty"`
	TaxAmount *decimal.Decimal     `json:"tax_amount,omitempty"`
	Notes     *string              `json:"notes,omitempty"`
	Lines     *[]CreateLineRequest `json:"lines,omitempty"`
}

type Payment struct {
	ID                  uuid.UUID       `json:"id"`
	InvoiceID           uuid.UUID       `json:"invoice_id"`
	InvoiceNumber       string          `json:"invoice_number,omitempty"`
	CustomerOrgID       uuid.UUID       `json:"customer_org_id"`
	Amount              decimal.Decimal `json:"amount"`
	Currency            string          `json:"currency"`
	PaymentMethod       string          `json:"payment_method"`
	Reference           string          `json:"reference"`
	PaymentDate         time.Time       `json:"payment_date"`
	LedgerTransactionID *uuid.UUID      `json:"ledger_transaction_id,omitempty"`
	Notes               string          `json:"notes"`
	IdempotencyKey      *string         `json:"idempotency_key,omitempty"`
	CreatedBy           *uuid.UUID      `json:"created_by,omitempty"`
	CreatedAt           time.Time       `json:"created_at"`
}

type RecordPaymentRequest struct {
	Amount         decimal.Decimal `json:"amount"`
	PaymentMethod  string          `json:"payment_method"` // Free text: e.g. "Bank Transfer"
	Reference      string          `json:"reference"`
	PaymentDate    *time.Time      `json:"payment_date,omitempty"`
	Notes          string          `json:"notes"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
}

type InvoiceFilter struct {
	CustomerOrgID *uuid.UUID `json:"customer_org_id,omitempty"`
	Status        *string    `json:"status,omitempty"`
	Currency      *string    `json:"currency,omitempty"`
	StartDate     *time.Time `json:"start_date,omitempty"`
	EndDate       *time.Time `json:"end_date,omitempty"`
	Search        string     `json:"search,omitempty"`
	Page          int        `json:"page"`
	PageSize      int        `json:"page_size"`
}
