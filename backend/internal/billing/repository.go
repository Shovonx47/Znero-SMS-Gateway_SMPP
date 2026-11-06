package billing

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
	CreateInvoice(ctx context.Context, tx pgx.Tx, inv Invoice, lines []InvoiceLine) (*Invoice, error)
	GetInvoiceByID(ctx context.Context, id uuid.UUID) (*Invoice, error)
	GetInvoiceByIDForUpdate(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*Invoice, error)
	UpdateDraftInvoice(ctx context.Context, tx pgx.Tx, inv Invoice, lines []InvoiceLine) (*Invoice, error)
	DeleteDraftInvoice(ctx context.Context, tx pgx.Tx, id uuid.UUID) error
	ListInvoices(ctx context.Context, tc *tenant.Context, filter InvoiceFilter) ([]Invoice, int, error)

	GetNextInvoiceNumber(ctx context.Context, tx pgx.Tx, issuerOrgID uuid.UUID, series string, year int) (string, error)
	IssueInvoice(ctx context.Context, tx pgx.Tx, id uuid.UUID, invoiceNumber string, issueDate, dueDate time.Time, expectedVersion int) error
	UpdateInvoiceStatusAndPaid(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string, voidReason *string, amountPaid decimal.Decimal, expectedVersion int) error

	CreatePayment(ctx context.Context, tx pgx.Tx, p Payment) (*Payment, error)
	GetPaymentByIdempotency(ctx context.Context, tx pgx.Tx, customerOrgID uuid.UUID, key string) (*Payment, error)
	ListPayments(ctx context.Context, tc *tenant.Context, invoiceID, customerOrgID *uuid.UUID, page, pageSize int) ([]Payment, int, error)
	GetPaymentByID(ctx context.Context, id uuid.UUID) (*Payment, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

func (r *pgRepository) CreateInvoice(ctx context.Context, tx pgx.Tx, inv Invoice, lines []InvoiceLine) (*Invoice, error) {
	query := `
		INSERT INTO invoices (
			customer_org_id, billing_period_id, currency, status, invoice_number,
			issue_date, due_date, subtotal, tax_label, tax_amount, total,
			amount_paid, notes, void_reason, credit_note_for_invoice_id, created_by
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16
		) RETURNING id, customer_org_id, billing_period_id, currency, status, invoice_number,
		            issue_date, due_date, subtotal, tax_label, tax_amount, total, amount_paid,
		            notes, void_reason, credit_note_for_invoice_id, version, created_by, created_at, updated_at
	`
	initialStatus := "draft"
	var out Invoice
	err := tx.QueryRow(ctx, query,
		inv.CustomerOrgID, inv.BillingPeriodID, inv.Currency, initialStatus, inv.InvoiceNumber,
		inv.IssueDate, inv.DueDate, inv.Subtotal, inv.TaxLabel, inv.TaxAmount, inv.Total,
		inv.AmountPaid, inv.Notes, inv.VoidReason, inv.CreditNoteForInvoiceID, inv.CreatedBy,
	).Scan(
		&out.ID, &out.CustomerOrgID, &out.BillingPeriodID, &out.Currency, &out.Status, &out.InvoiceNumber,
		&out.IssueDate, &out.DueDate, &out.Subtotal, &out.TaxLabel, &out.TaxAmount, &out.Total,
		&out.AmountPaid, &out.Notes, &out.VoidReason, &out.CreditNoteForInvoiceID, &out.Version,
		&out.CreatedBy, &out.CreatedAt, &out.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert invoice: %w", err)
	}

	var insertedLines []InvoiceLine
	for idx, line := range lines {
		lineNum := idx + 1
		lineQuery := `
			INSERT INTO invoice_lines (invoice_id, line_number, description, quantity, unit_price, amount, ledger_charge_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING id, invoice_id, line_number, description, quantity, unit_price, amount, ledger_charge_id, created_at
		`
		var l InvoiceLine
		err := tx.QueryRow(ctx, lineQuery,
			out.ID, lineNum, line.Description, line.Quantity, line.UnitPrice, line.Amount, line.LedgerChargeID,
		).Scan(
			&l.ID, &l.InvoiceID, &l.LineNumber, &l.Description, &l.Quantity, &l.UnitPrice, &l.Amount, &l.LedgerChargeID, &l.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("insert invoice line: %w", err)
		}
		insertedLines = append(insertedLines, l)
	}
	out.Lines = insertedLines

	// If initial creation was requested with a non-draft status (e.g. credit notes issued immediately), transition status now that lines exist
	if inv.Status != "draft" {
		if _, err := tx.Exec(ctx, "UPDATE invoices SET status = $1 WHERE id = $2", inv.Status, out.ID); err != nil {
			return nil, fmt.Errorf("set invoice status: %w", err)
		}
		out.Status = inv.Status
	}

	out.OutstandingBalance = out.Total.Sub(out.AmountPaid)
	return &out, nil
}

func (r *pgRepository) GetInvoiceByID(ctx context.Context, id uuid.UUID) (*Invoice, error) {
	query := `
		SELECT i.id, i.customer_org_id, o.name, i.billing_period_id, i.currency, i.status, i.invoice_number,
		       i.issue_date, i.due_date, i.subtotal, i.tax_label, i.tax_amount, i.total, i.amount_paid,
		       i.notes, i.void_reason, i.credit_note_for_invoice_id, orig.invoice_number,
		       i.version, i.created_by, i.created_at, i.updated_at
		FROM invoices i
		JOIN organizations o ON o.id = i.customer_org_id
		LEFT JOIN invoices orig ON orig.id = i.credit_note_for_invoice_id
		WHERE i.id = $1
	`
	var out Invoice
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&out.ID, &out.CustomerOrgID, &out.CustomerOrgName, &out.BillingPeriodID, &out.Currency, &out.Status, &out.InvoiceNumber,
		&out.IssueDate, &out.DueDate, &out.Subtotal, &out.TaxLabel, &out.TaxAmount, &out.Total, &out.AmountPaid,
		&out.Notes, &out.VoidReason, &out.CreditNoteForInvoiceID, &out.CreditNoteForInvoiceNumber,
		&out.Version, &out.CreatedBy, &out.CreatedAt, &out.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvoiceNotFound
		}
		return nil, fmt.Errorf("get invoice by id: %w", err)
	}

	lines, err := r.getLinesForInvoice(ctx, out.ID)
	if err != nil {
		return nil, err
	}
	out.Lines = lines
	out.OutstandingBalance = out.Total.Sub(out.AmountPaid)

	return &out, nil
}

func (r *pgRepository) GetInvoiceByIDForUpdate(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*Invoice, error) {
	query := `
		SELECT i.id, i.customer_org_id, o.name, i.billing_period_id, i.currency, i.status, i.invoice_number,
		       i.issue_date, i.due_date, i.subtotal, i.tax_label, i.tax_amount, i.total, i.amount_paid,
		       i.notes, i.void_reason, i.credit_note_for_invoice_id, orig.invoice_number,
		       i.version, i.created_by, i.created_at, i.updated_at
		FROM invoices i
		JOIN organizations o ON o.id = i.customer_org_id
		LEFT JOIN invoices orig ON orig.id = i.credit_note_for_invoice_id
		WHERE i.id = $1
		FOR UPDATE OF i
	`
	var out Invoice
	err := tx.QueryRow(ctx, query, id).Scan(
		&out.ID, &out.CustomerOrgID, &out.CustomerOrgName, &out.BillingPeriodID, &out.Currency, &out.Status, &out.InvoiceNumber,
		&out.IssueDate, &out.DueDate, &out.Subtotal, &out.TaxLabel, &out.TaxAmount, &out.Total, &out.AmountPaid,
		&out.Notes, &out.VoidReason, &out.CreditNoteForInvoiceID, &out.CreditNoteForInvoiceNumber,
		&out.Version, &out.CreatedBy, &out.CreatedAt, &out.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvoiceNotFound
		}
		return nil, fmt.Errorf("get invoice for update: %w", err)
	}

	lines, err := r.getLinesForInvoice(ctx, out.ID)
	if err != nil {
		return nil, err
	}
	out.Lines = lines
	out.OutstandingBalance = out.Total.Sub(out.AmountPaid)

	return &out, nil
}

func (r *pgRepository) getLinesForInvoice(ctx context.Context, invoiceID uuid.UUID) ([]InvoiceLine, error) {
	query := `
		SELECT id, invoice_id, line_number, description, quantity, unit_price, amount, ledger_charge_id, created_at
		FROM invoice_lines
		WHERE invoice_id = $1
		ORDER BY line_number ASC
	`
	rows, err := r.pool.Query(ctx, query, invoiceID)
	if err != nil {
		return nil, fmt.Errorf("query invoice lines: %w", err)
	}
	defer rows.Close()

	var lines []InvoiceLine
	for rows.Next() {
		var l InvoiceLine
		if err := rows.Scan(
			&l.ID, &l.InvoiceID, &l.LineNumber, &l.Description, &l.Quantity, &l.UnitPrice,
			&l.Amount, &l.LedgerChargeID, &l.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan invoice line: %w", err)
		}
		lines = append(lines, l)
	}
	return lines, nil
}

func (r *pgRepository) UpdateDraftInvoice(ctx context.Context, tx pgx.Tx, inv Invoice, lines []InvoiceLine) (*Invoice, error) {
	query := `
		UPDATE invoices
		SET currency = $1, due_date = $2, subtotal = $3, tax_label = $4, tax_amount = $5,
		    total = $6, notes = $7, version = version + 1, updated_at = now()
		WHERE id = $8 AND version = $9 AND status = 'draft'
		RETURNING id, customer_org_id, billing_period_id, currency, status, invoice_number,
		          issue_date, due_date, subtotal, tax_label, tax_amount, total, amount_paid,
		          notes, void_reason, credit_note_for_invoice_id, version, created_by, created_at, updated_at
	`
	var out Invoice
	err := tx.QueryRow(ctx, query,
		inv.Currency, inv.DueDate, inv.Subtotal, inv.TaxLabel, inv.TaxAmount,
		inv.Total, inv.Notes, inv.ID, inv.Version,
	).Scan(
		&out.ID, &out.CustomerOrgID, &out.BillingPeriodID, &out.Currency, &out.Status, &out.InvoiceNumber,
		&out.IssueDate, &out.DueDate, &out.Subtotal, &out.TaxLabel, &out.TaxAmount, &out.Total,
		&out.AmountPaid, &out.Notes, &out.VoidReason, &out.CreditNoteForInvoiceID, &out.Version,
		&out.CreatedBy, &out.CreatedAt, &out.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("optimistic lock conflict or invoice not in draft status")
		}
		return nil, fmt.Errorf("update draft invoice: %w", err)
	}

	// Replace lines
	_, err = tx.Exec(ctx, "DELETE FROM invoice_lines WHERE invoice_id = $1", out.ID)
	if err != nil {
		return nil, fmt.Errorf("delete old lines: %w", err)
	}

	var insertedLines []InvoiceLine
	for idx, line := range lines {
		lineNum := idx + 1
		lineQuery := `
			INSERT INTO invoice_lines (invoice_id, line_number, description, quantity, unit_price, amount, ledger_charge_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING id, invoice_id, line_number, description, quantity, unit_price, amount, ledger_charge_id, created_at
		`
		var l InvoiceLine
		err := tx.QueryRow(ctx, lineQuery,
			out.ID, lineNum, line.Description, line.Quantity, line.UnitPrice, line.Amount, line.LedgerChargeID,
		).Scan(
			&l.ID, &l.InvoiceID, &l.LineNumber, &l.Description, &l.Quantity, &l.UnitPrice, &l.Amount, &l.LedgerChargeID, &l.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("insert replacement line: %w", err)
		}
		insertedLines = append(insertedLines, l)
	}
	out.Lines = insertedLines
	out.OutstandingBalance = out.Total.Sub(out.AmountPaid)

	return &out, nil
}

func (r *pgRepository) DeleteDraftInvoice(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	query := "DELETE FROM invoices WHERE id = $1 AND status = 'draft'"
	tag, err := tx.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("delete draft invoice: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("cannot delete: invoice not found or not in draft status")
	}
	return nil
}

func (r *pgRepository) ListInvoices(ctx context.Context, tc *tenant.Context, filter InvoiceFilter) ([]Invoice, int, error) {
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

	if !tc.IsPlatformAdmin && !tc.IsPlatformOrg() {
		conditions = append(conditions, fmt.Sprintf("o.path LIKE $%d || '%%'", argIdx))
		args = append(args, tc.ActorOrgPath)
		argIdx++
	}

	if filter.CustomerOrgID != nil {
		conditions = append(conditions, fmt.Sprintf("i.customer_org_id = $%d", argIdx))
		args = append(args, *filter.CustomerOrgID)
		argIdx++
	}

	if filter.Status != nil && *filter.Status != "" {
		conditions = append(conditions, fmt.Sprintf("i.status = $%d", argIdx))
		args = append(args, *filter.Status)
		argIdx++
	}

	if filter.Currency != nil && *filter.Currency != "" {
		conditions = append(conditions, fmt.Sprintf("i.currency = $%d", argIdx))
		args = append(args, *filter.Currency)
		argIdx++
	}

	if filter.StartDate != nil {
		conditions = append(conditions, fmt.Sprintf("COALESCE(i.issue_date, i.created_at::date) >= $%d", argIdx))
		args = append(args, *filter.StartDate)
		argIdx++
	}

	if filter.EndDate != nil {
		conditions = append(conditions, fmt.Sprintf("COALESCE(i.issue_date, i.created_at::date) <= $%d", argIdx))
		args = append(args, *filter.EndDate)
		argIdx++
	}

	if filter.Search != "" {
		conditions = append(conditions, fmt.Sprintf("(o.name ILIKE $%d OR COALESCE(i.invoice_number, '') ILIKE $%d)", argIdx, argIdx))
		args = append(args, "%"+filter.Search+"%")
		argIdx++
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	countQuery := fmt.Sprintf(`
		SELECT count(*)
		FROM invoices i
		JOIN organizations o ON o.id = i.customer_org_id
		%s
	`, whereClause)

	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count invoices: %w", err)
	}

	selectQuery := fmt.Sprintf(`
		SELECT i.id, i.customer_org_id, o.name, i.billing_period_id, i.currency, i.status, i.invoice_number,
		       i.issue_date, i.due_date, i.subtotal, i.tax_label, i.tax_amount, i.total, i.amount_paid,
		       i.notes, i.void_reason, i.credit_note_for_invoice_id, orig.invoice_number,
		       i.version, i.created_by, i.created_at, i.updated_at
		FROM invoices i
		JOIN organizations o ON o.id = i.customer_org_id
		LEFT JOIN invoices orig ON orig.id = i.credit_note_for_invoice_id
		%s
		ORDER BY COALESCE(i.issue_date, i.created_at::date) DESC, i.created_at DESC
		LIMIT $%d OFFSET $%d
	`, whereClause, argIdx, argIdx+1)

	args = append(args, filter.PageSize, offset)
	rows, err := r.pool.Query(ctx, selectQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list invoices: %w", err)
	}
	defer rows.Close()

	var invoices []Invoice
	for rows.Next() {
		var inv Invoice
		if err := rows.Scan(
			&inv.ID, &inv.CustomerOrgID, &inv.CustomerOrgName, &inv.BillingPeriodID, &inv.Currency, &inv.Status, &inv.InvoiceNumber,
			&inv.IssueDate, &inv.DueDate, &inv.Subtotal, &inv.TaxLabel, &inv.TaxAmount, &inv.Total, &inv.AmountPaid,
			&inv.Notes, &inv.VoidReason, &inv.CreditNoteForInvoiceID, &inv.CreditNoteForInvoiceNumber,
			&inv.Version, &inv.CreatedBy, &inv.CreatedAt, &inv.UpdatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan invoice: %w", err)
		}
		inv.OutstandingBalance = inv.Total.Sub(inv.AmountPaid)
		invoices = append(invoices, inv)
	}

	return invoices, total, nil
}

func (r *pgRepository) GetNextInvoiceNumber(ctx context.Context, tx pgx.Tx, issuerOrgID uuid.UUID, series string, year int) (string, error) {
	if series == "" {
		series = "INV"
	}
	// Gap-free sequence counter with row locking
	ensureQuery := `
		INSERT INTO invoice_sequences (issuer_org_id, series, year, current_val)
		VALUES ($1, $2, $3, 0)
		ON CONFLICT (issuer_org_id, series, year) DO NOTHING
	`
	if _, err := tx.Exec(ctx, ensureQuery, issuerOrgID, series, year); err != nil {
		return "", fmt.Errorf("ensure invoice sequence: %w", err)
	}

	lockQuery := `
		SELECT current_val
		FROM invoice_sequences
		WHERE issuer_org_id = $1 AND series = $2 AND year = $3
		FOR UPDATE
	`
	var curVal int64
	if err := tx.QueryRow(ctx, lockQuery, issuerOrgID, series, year).Scan(&curVal); err != nil {
		return "", fmt.Errorf("lock invoice sequence: %w", err)
	}

	nextVal := curVal + 1
	updateQuery := `
		UPDATE invoice_sequences
		SET current_val = $1
		WHERE issuer_org_id = $2 AND series = $3 AND year = $4
	`
	if _, err := tx.Exec(ctx, updateQuery, nextVal, issuerOrgID, series, year); err != nil {
		return "", fmt.Errorf("increment invoice sequence: %w", err)
	}

	return fmt.Sprintf("%s-%d-%06d", series, year, nextVal), nil
}

func (r *pgRepository) IssueInvoice(ctx context.Context, tx pgx.Tx, id uuid.UUID, invoiceNumber string, issueDate, dueDate time.Time, expectedVersion int) error {
	query := `
		UPDATE invoices
		SET status = 'issued', invoice_number = $1, issue_date = $2, due_date = $3,
		    version = version + 1, updated_at = now()
		WHERE id = $4 AND version = $5 AND status = 'draft'
	`
	tag, err := tx.Exec(ctx, query, invoiceNumber, issueDate, dueDate, id, expectedVersion)
	if err != nil {
		return fmt.Errorf("issue invoice: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("optimistic lock conflict or invoice not in draft status")
	}
	return nil
}

func (r *pgRepository) UpdateInvoiceStatusAndPaid(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string, voidReason *string, amountPaid decimal.Decimal, expectedVersion int) error {
	query := `
		UPDATE invoices
		SET status = $1, void_reason = $2, amount_paid = $3, version = version + 1, updated_at = now()
		WHERE id = $4 AND version = $5
	`
	tag, err := tx.Exec(ctx, query, status, voidReason, amountPaid, id, expectedVersion)
	if err != nil {
		return fmt.Errorf("update invoice status and paid: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("optimistic lock conflict or invoice not found")
	}
	return nil
}

func (r *pgRepository) CreatePayment(ctx context.Context, tx pgx.Tx, p Payment) (*Payment, error) {
	query := `
		INSERT INTO payments (
			invoice_id, customer_org_id, amount, currency, payment_method,
			reference, payment_date, ledger_transaction_id, notes, idempotency_key, created_by
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, invoice_id, customer_org_id, amount, currency, payment_method,
		          reference, payment_date, ledger_transaction_id, notes, idempotency_key, created_by, created_at
	`
	var out Payment
	err := tx.QueryRow(ctx, query,
		p.InvoiceID, p.CustomerOrgID, p.Amount, p.Currency, p.PaymentMethod,
		p.Reference, p.PaymentDate, p.LedgerTransactionID, p.Notes, p.IdempotencyKey, p.CreatedBy,
	).Scan(
		&out.ID, &out.InvoiceID, &out.CustomerOrgID, &out.Amount, &out.Currency, &out.PaymentMethod,
		&out.Reference, &out.PaymentDate, &out.LedgerTransactionID, &out.Notes, &out.IdempotencyKey, &out.CreatedBy, &out.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert payment: %w", err)
	}
	return &out, nil
}

func (r *pgRepository) GetPaymentByIdempotency(ctx context.Context, tx pgx.Tx, customerOrgID uuid.UUID, key string) (*Payment, error) {
	query := `
		SELECT id, invoice_id, customer_org_id, amount, currency, payment_method,
		       reference, payment_date, ledger_transaction_id, notes, idempotency_key, created_by, created_at
		FROM payments
		WHERE customer_org_id = $1 AND idempotency_key = $2
		LIMIT 1
	`
	var p Payment
	var row pgx.Row
	if tx != nil {
		row = tx.QueryRow(ctx, query, customerOrgID, key)
	} else {
		row = r.pool.QueryRow(ctx, query, customerOrgID, key)
	}
	err := row.Scan(
		&p.ID, &p.InvoiceID, &p.CustomerOrgID, &p.Amount, &p.Currency, &p.PaymentMethod,
		&p.Reference, &p.PaymentDate, &p.LedgerTransactionID, &p.Notes, &p.IdempotencyKey, &p.CreatedBy, &p.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get payment by idempotency: %w", err)
	}
	return &p, nil
}

func (r *pgRepository) ListPayments(ctx context.Context, tc *tenant.Context, invoiceID, customerOrgID *uuid.UUID, page, pageSize int) ([]Payment, int, error) {
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

	if !tc.IsPlatformAdmin && !tc.IsPlatformOrg() {
		conditions = append(conditions, fmt.Sprintf("o.path LIKE $%d || '%%'", argIdx))
		args = append(args, tc.ActorOrgPath)
		argIdx++
	}

	if invoiceID != nil {
		conditions = append(conditions, fmt.Sprintf("p.invoice_id = $%d", argIdx))
		args = append(args, *invoiceID)
		argIdx++
	}

	if customerOrgID != nil {
		conditions = append(conditions, fmt.Sprintf("p.customer_org_id = $%d", argIdx))
		args = append(args, *customerOrgID)
		argIdx++
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	countQuery := fmt.Sprintf(`
		SELECT count(*)
		FROM payments p
		JOIN organizations o ON o.id = p.customer_org_id
		%s
	`, whereClause)

	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count payments: %w", err)
	}

	selectQuery := fmt.Sprintf(`
		SELECT p.id, p.invoice_id, COALESCE(i.invoice_number, ''), p.customer_org_id, p.amount, p.currency,
		       p.payment_method, p.reference, p.payment_date, p.ledger_transaction_id, p.notes,
		       p.created_by, p.created_at
		FROM payments p
		JOIN organizations o ON o.id = p.customer_org_id
		LEFT JOIN invoices i ON i.id = p.invoice_id
		%s
		ORDER BY p.payment_date DESC
		LIMIT $%d OFFSET $%d
	`, whereClause, argIdx, argIdx+1)

	args = append(args, pageSize, offset)
	rows, err := r.pool.Query(ctx, selectQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list payments: %w", err)
	}
	defer rows.Close()

	var payments []Payment
	for rows.Next() {
		var p Payment
		if err := rows.Scan(
			&p.ID, &p.InvoiceID, &p.InvoiceNumber, &p.CustomerOrgID, &p.Amount, &p.Currency,
			&p.PaymentMethod, &p.Reference, &p.PaymentDate, &p.LedgerTransactionID, &p.Notes,
			&p.CreatedBy, &p.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan payment: %w", err)
		}
		payments = append(payments, p)
	}

	return payments, total, nil
}

func (r *pgRepository) GetPaymentByID(ctx context.Context, id uuid.UUID) (*Payment, error) {
	query := `
		SELECT p.id, p.invoice_id, COALESCE(i.invoice_number, ''), p.customer_org_id, p.amount, p.currency,
		       p.payment_method, p.reference, p.payment_date, p.ledger_transaction_id, p.notes,
		       p.created_by, p.created_at
		FROM payments p
		LEFT JOIN invoices i ON i.id = p.invoice_id
		WHERE p.id = $1
	`
	var p Payment
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&p.ID, &p.InvoiceID, &p.InvoiceNumber, &p.CustomerOrgID, &p.Amount, &p.Currency,
		&p.PaymentMethod, &p.Reference, &p.PaymentDate, &p.LedgerTransactionID, &p.Notes,
		&p.CreatedBy, &p.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPaymentNotFound
		}
		return nil, fmt.Errorf("get payment by id: %w", err)
	}
	return &p, nil
}
