-- Phase 6: Wallets, Double-Entry Ledger, Credit Enforcement & Invoicing Basics Migration (UP)

-- 1. Wallets
CREATE TABLE wallets (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id         UUID NOT NULL UNIQUE REFERENCES organizations(id) ON DELETE RESTRICT,
    currency                CHAR(3) NOT NULL DEFAULT 'USD',
    status                  VARCHAR(32) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'frozen', 'closed')),
    account_type            VARCHAR(32) NOT NULL DEFAULT 'prepaid' CHECK (account_type IN ('prepaid', 'postpaid')),
    low_balance_threshold   NUMERIC(18,4) NOT NULL DEFAULT 0,
    cached_balance          NUMERIC(18,4) NOT NULL DEFAULT 0,
    version                 INT NOT NULL DEFAULT 1,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_wallets_org ON wallets(organization_id);
CREATE INDEX idx_wallets_status ON wallets(status);

-- 2. Ledger Accounts
CREATE TABLE ledger_accounts (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code                    VARCHAR(64) NOT NULL UNIQUE,
    name                    VARCHAR(128) NOT NULL,
    type                    VARCHAR(32) NOT NULL CHECK (type IN ('asset', 'liability', 'equity', 'revenue', 'expense', 'wallet')),
    currency                CHAR(3) NOT NULL DEFAULT 'USD',
    wallet_id               UUID UNIQUE REFERENCES wallets(id) ON DELETE RESTRICT,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ledger_accounts_wallet ON ledger_accounts(wallet_id);
CREATE INDEX idx_ledger_accounts_currency ON ledger_accounts(currency);

-- 3. Ledger Transactions
CREATE TABLE ledger_transactions (
    id                          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    type                        VARCHAR(32) NOT NULL CHECK (type IN ('topup', 'adjustment', 'charge', 'refund', 'reversal', 'hold_capture', 'transfer')),
    reference_type              VARCHAR(64),
    reference_id                UUID,
    idempotency_key             VARCHAR(128),
    description                 TEXT NOT NULL DEFAULT '',
    reason                      TEXT,
    reversal_of_transaction_id  UUID UNIQUE REFERENCES ledger_transactions(id) ON DELETE RESTRICT,
    tenant_org_id               UUID REFERENCES organizations(id) ON DELETE RESTRICT,
    actor_user_id               UUID REFERENCES users(id) ON DELETE SET NULL,
    actor_org_id                UUID REFERENCES organizations(id) ON DELETE SET NULL,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_ledger_tx_idempotency UNIQUE (tenant_org_id, type, idempotency_key),
    CONSTRAINT chk_ledger_tx_reason CHECK (
        type NOT IN ('adjustment', 'reversal') OR (reason IS NOT NULL AND length(trim(reason)) > 0)
    )
);

CREATE INDEX idx_ledger_tx_type ON ledger_transactions(type);
CREATE INDEX idx_ledger_tx_tenant ON ledger_transactions(tenant_org_id);
CREATE INDEX idx_ledger_tx_created ON ledger_transactions(created_at);
CREATE INDEX idx_ledger_tx_ref ON ledger_transactions(reference_type, reference_id);

-- 4. Ledger Entries
CREATE TABLE ledger_entries (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id          UUID NOT NULL REFERENCES ledger_transactions(id) ON DELETE RESTRICT,
    account_id              UUID NOT NULL REFERENCES ledger_accounts(id) ON DELETE RESTRICT,
    direction               VARCHAR(16) NOT NULL CHECK (direction IN ('debit', 'credit')),
    amount                  NUMERIC(18,4) NOT NULL CHECK (amount > 0),
    currency                CHAR(3) NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ledger_entries_tx ON ledger_entries(transaction_id);
CREATE INDEX idx_ledger_entries_account ON ledger_entries(account_id);
CREATE INDEX idx_ledger_entries_created ON ledger_entries(created_at);

-- Double-Entry Balance Verification Constraint Trigger
CREATE OR REPLACE FUNCTION verify_ledger_transaction_balanced()
RETURNS TRIGGER AS $$
DECLARE
    entry_count INT;
    net_balance NUMERIC(18,4);
    curr_count  INT;
BEGIN
    SELECT count(*),
           COALESCE(SUM(CASE WHEN direction = 'debit' THEN amount ELSE -amount END), 0),
           COUNT(DISTINCT currency)
    INTO entry_count, net_balance, curr_count
    FROM ledger_entries
    WHERE transaction_id = NEW.transaction_id;

    IF entry_count < 2 THEN
        RAISE EXCEPTION 'Double-entry violation: transaction % must have at least 2 entries (found %)', NEW.transaction_id, entry_count;
    END IF;

    IF net_balance <> 0 THEN
        RAISE EXCEPTION 'Double-entry violation: transaction % is unbalanced (net balance = %)', NEW.transaction_id, net_balance;
    END IF;

    IF curr_count <> 1 THEN
        RAISE EXCEPTION 'Double-entry violation: transaction % entries must all share the same currency', NEW.transaction_id;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER trg_verify_ledger_balanced
AFTER INSERT ON ledger_entries
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION verify_ledger_transaction_balanced();

-- Immutability Triggers on Ledger Tables (Append-Only)
CREATE OR REPLACE FUNCTION block_ledger_mutation()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'Ledger tables are strictly append-only: UPDATE and DELETE are prohibited';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_ledger_accounts_immutable
BEFORE UPDATE OR DELETE ON ledger_accounts
FOR EACH ROW EXECUTE FUNCTION block_ledger_mutation();

CREATE TRIGGER trg_ledger_transactions_immutable
BEFORE UPDATE OR DELETE ON ledger_transactions
FOR EACH ROW EXECUTE FUNCTION block_ledger_mutation();

CREATE TRIGGER trg_ledger_entries_immutable
BEFORE UPDATE OR DELETE ON ledger_entries
FOR EACH ROW EXECUTE FUNCTION block_ledger_mutation();

-- 5. Holds (Reserve then Settle)
CREATE TABLE holds (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    wallet_id               UUID NOT NULL REFERENCES wallets(id) ON DELETE RESTRICT,
    amount                  NUMERIC(18,4) NOT NULL CHECK (amount > 0),
    currency                CHAR(3) NOT NULL,
    status                  VARCHAR(32) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'captured', 'released', 'expired')),
    expires_at              TIMESTAMPTZ NOT NULL,
    reference_type          VARCHAR(64),
    reference_id            UUID,
    idempotency_key         VARCHAR(128),
    captured_amount         NUMERIC(18,4) NOT NULL DEFAULT 0 CHECK (captured_amount >= 0),
    version                 INT NOT NULL DEFAULT 1,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_holds_idempotency UNIQUE (wallet_id, idempotency_key)
);

CREATE INDEX idx_holds_wallet_status ON holds(wallet_id, status);
CREATE INDEX idx_holds_expires_at ON holds(status, expires_at);

-- 6. Billing Periods
CREATE TABLE billing_periods (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id         UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    period_start            DATE NOT NULL,
    period_end              DATE NOT NULL,
    status                  VARCHAR(32) NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_billing_periods_org_start UNIQUE (organization_id, period_start)
);

CREATE INDEX idx_billing_periods_org ON billing_periods(organization_id);

-- 7. Gap-Free Invoice Sequences
CREATE TABLE invoice_sequences (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    issuer_org_id           UUID NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    series                  VARCHAR(16) NOT NULL DEFAULT 'INV',
    year                    INT NOT NULL,
    current_val             BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT uq_invoice_sequences UNIQUE (issuer_org_id, series, year)
);

-- 8. Invoices
CREATE TABLE invoices (
    id                          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_org_id             UUID NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    billing_period_id           UUID REFERENCES billing_periods(id) ON DELETE SET NULL,
    currency                    CHAR(3) NOT NULL DEFAULT 'USD',
    status                      VARCHAR(32) NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'issued', 'paid', 'void')),
    invoice_number              VARCHAR(64) UNIQUE,
    issue_date                  DATE,
    due_date                    DATE,
    subtotal                    NUMERIC(18,4) NOT NULL DEFAULT 0,
    tax_label                   VARCHAR(64) NOT NULL DEFAULT 'Tax',
    tax_amount                  NUMERIC(18,4) NOT NULL DEFAULT 0,
    total                       NUMERIC(18,4) NOT NULL DEFAULT 0,
    amount_paid                 NUMERIC(18,4) NOT NULL DEFAULT 0,
    notes                       TEXT NOT NULL DEFAULT '',
    void_reason                 TEXT,
    credit_note_for_invoice_id  UUID REFERENCES invoices(id) ON DELETE RESTRICT,
    version                     INT NOT NULL DEFAULT 1,
    created_by                  UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_invoices_customer ON invoices(customer_org_id);
CREATE INDEX idx_invoices_status ON invoices(status);
CREATE INDEX idx_invoices_number ON invoices(invoice_number);

-- 9. Invoice Lines
CREATE TABLE invoice_lines (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id              UUID NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    line_number             INT NOT NULL,
    description             TEXT NOT NULL,
    quantity                NUMERIC(18,4) NOT NULL DEFAULT 1 CHECK (quantity > 0),
    unit_price              NUMERIC(18,4) NOT NULL CHECK (unit_price >= 0),
    amount                  NUMERIC(18,4) NOT NULL CHECK (amount >= 0),
    ledger_charge_id        UUID REFERENCES ledger_transactions(id) ON DELETE SET NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_invoice_lines_num UNIQUE (invoice_id, line_number)
);

CREATE INDEX idx_invoice_lines_invoice ON invoice_lines(invoice_id);

-- Immutability on Issued Invoices Trigger
CREATE OR REPLACE FUNCTION protect_issued_invoice()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.status IN ('issued', 'paid', 'void') THEN
        IF TG_OP = 'DELETE' THEN
            RAISE EXCEPTION 'Cannot delete invoice in status %', OLD.status;
        END IF;
        -- Allow transitions: issued -> paid, issued -> void, but financial line items/totals cannot be mutated
        IF (OLD.subtotal <> NEW.subtotal OR OLD.tax_amount <> NEW.tax_amount OR OLD.total <> NEW.total OR OLD.currency <> NEW.currency OR OLD.invoice_number <> NEW.invoice_number) THEN
            RAISE EXCEPTION 'Financial terms of issued invoice % are immutable; use a credit note instead', OLD.invoice_number;
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_protect_issued_invoice
BEFORE UPDATE OR DELETE ON invoices
FOR EACH ROW EXECUTE FUNCTION protect_issued_invoice();

-- Protect Invoice Lines of Issued Invoices
CREATE OR REPLACE FUNCTION protect_issued_invoice_lines()
RETURNS TRIGGER AS $$
DECLARE
    inv_status VARCHAR(32);
BEGIN
    IF TG_OP = 'DELETE' THEN
        SELECT status INTO inv_status FROM invoices WHERE id = OLD.invoice_id;
    ELSE
        SELECT status INTO inv_status FROM invoices WHERE id = NEW.invoice_id;
    END IF;

    IF inv_status IN ('issued', 'paid', 'void') THEN
        RAISE EXCEPTION 'Cannot add, update, or remove line items from an issued/paid/void invoice';
    END IF;

    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_protect_issued_invoice_lines
BEFORE INSERT OR UPDATE OR DELETE ON invoice_lines
FOR EACH ROW EXECUTE FUNCTION protect_issued_invoice_lines();

-- 10. Payments
CREATE TABLE payments (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id              UUID NOT NULL REFERENCES invoices(id) ON DELETE RESTRICT,
    customer_org_id         UUID NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    amount                  NUMERIC(18,4) NOT NULL CHECK (amount > 0),
    currency                CHAR(3) NOT NULL,
    payment_method          VARCHAR(64) NOT NULL,
    reference               VARCHAR(128) NOT NULL DEFAULT '',
    payment_date            TIMESTAMPTZ NOT NULL DEFAULT now(),
    ledger_transaction_id   UUID REFERENCES ledger_transactions(id) ON DELETE RESTRICT,
    notes                   TEXT NOT NULL DEFAULT '',
    idempotency_key         VARCHAR(128),
    created_by              UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_payments_invoice ON payments(invoice_id);
CREATE INDEX idx_payments_customer ON payments(customer_org_id);
CREATE UNIQUE INDEX idx_payments_idempotency ON payments(customer_org_id, idempotency_key) WHERE idempotency_key IS NOT NULL;

-- 11. Extend Change Requests Constraints for Maker-Checker
ALTER TABLE change_requests DROP CONSTRAINT IF EXISTS change_requests_entity_type_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_entity_type_check
    CHECK (entity_type IN (
        'country', 'operator', 'prefix', 'number_rule', 'import_job',
        'provider', 'provider_connection', 'provider_coverage', 'customer_profile', 'sender_id',
        'rate_card', 'rate', 'rate_assignment', 'reseller_cost_assignment',
        'wallet_topup', 'wallet_adjustment', 'wallet_unfreeze', 'invoice_void'
    ));

ALTER TABLE change_requests DROP CONSTRAINT IF EXISTS change_requests_operation_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_operation_check
    CHECK (operation IN (
        'create', 'update', 'deactivate', 'activate', 'archive', 'rollback',
        'confirm_import', 'status_change', 'secret_update', 'approve_sender_id',
        'topup', 'adjustment', 'unfreeze', 'void'
    ));

-- 12. Seed System Accounts
INSERT INTO ledger_accounts (code, name, type, currency) VALUES
    ('PLATFORM_REVENUE', 'Platform Revenue (USD)', 'revenue', 'USD'),
    ('CUSTOMER_FUNDS',   'Customer Funds Held (USD)', 'liability', 'USD'),
    ('ADJUSTMENTS',      'Adjustments / Discretionary (USD)', 'expense', 'USD'),
    ('SUSPENSE',          'Suspense / Clearing (USD)', 'asset', 'USD'),
    ('PLATFORM_REVENUE_EUR', 'Platform Revenue (EUR)', 'revenue', 'EUR'),
    ('CUSTOMER_FUNDS_EUR',   'Customer Funds Held (EUR)', 'liability', 'EUR'),
    ('ADJUSTMENTS_EUR',      'Adjustments / Discretionary (EUR)', 'expense', 'EUR'),
    ('SUSPENSE_EUR',          'Suspense / Clearing (EUR)', 'asset', 'EUR')
ON CONFLICT (code) DO NOTHING;

-- 13. Seed Permissions
INSERT INTO permissions (code, module, resource, action, description) VALUES
    ('wallet.wallet.view',       'wallet',  'wallet',     'view',       'View wallet balances, status, and summary'),
    ('wallet.topup.create',      'wallet',  'topup',      'create',     'Create a wallet top-up request'),
    ('wallet.adjustment.create', 'wallet',  'adjustment', 'create',     'Create a wallet balance adjustment request'),
    ('wallet.freeze',            'wallet',  'wallet',     'freeze',     'Freeze or request unfreezing of a wallet'),
    ('wallet.override',          'wallet',  'wallet',     'override',   'Bypass maker-checker workflow for topups and adjustments'),
    ('wallet.approve',           'wallet',  'wallet',     'approve',    'Approve or reject wallet change requests'),
    ('billing.invoice.view',     'billing', 'invoice',    'view',       'View invoices and line items'),
    ('billing.invoice.create',   'billing', 'invoice',    'create',     'Create and edit draft invoices'),
    ('billing.invoice.issue',    'billing', 'invoice',    'issue',      'Issue draft invoices to finalized status'),
    ('billing.invoice.void',     'billing', 'invoice',    'void',       'Void or request voiding of issued invoices'),
    ('billing.invoice.creditnote','billing','invoice',    'creditnote', 'Create credit notes against issued invoices'),
    ('billing.payment.create',   'billing', 'payment',    'create',     'Record payment against an invoice'),
    ('billing.payment.view',     'billing', 'payment',    'view',       'View payment records'),
    ('ledger.view',              'ledger',  'ledger',     'view',       'View double-entry ledger transactions and entries'),
    ('ledger.reconcile',         'ledger',  'ledger',     'reconcile',  'Run and view ledger balance reconciliation reports')
ON CONFLICT (code) DO NOTHING;

-- Grant all new permissions to Platform Super Admin
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'platform'
CROSS JOIN permissions p
WHERE r.name = 'Super Admin'
  AND p.module IN ('wallet', 'billing', 'ledger')
ON CONFLICT DO NOTHING;

-- Grant wallet/billing/ledger viewer to Platform Staff and Read-Only roles
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'platform'
CROSS JOIN permissions p
WHERE r.name IN ('Staff', 'Read-only')
  AND p.code IN ('wallet.wallet.view', 'billing.invoice.view', 'billing.payment.view', 'ledger.view')
ON CONFLICT DO NOTHING;

-- Grant maker permissions (without override/approve) to Platform Staff and Master Data Editor
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'platform'
CROSS JOIN permissions p
WHERE r.name IN ('Staff', 'Master Data Editor')
  AND p.code IN ('wallet.wallet.view', 'wallet.topup.create', 'wallet.adjustment.create', 'billing.invoice.create', 'billing.invoice.view', 'billing.payment.create', 'ledger.view')
ON CONFLICT DO NOTHING;

-- Grant approver permissions to Master Data Approver
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'platform'
CROSS JOIN permissions p
WHERE r.name = 'Master Data Approver'
  AND p.code IN ('wallet.wallet.view', 'wallet.approve', 'billing.invoice.view', 'billing.payment.view', 'ledger.view')
ON CONFLICT DO NOTHING;

-- Grant customer and reseller read-only permissions on wallet and invoices
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type IN ('reseller', 'customer')
CROSS JOIN permissions p
WHERE r.name = 'Org Admin'
  AND p.code IN ('wallet.wallet.view', 'billing.invoice.view', 'billing.payment.view', 'ledger.view')
ON CONFLICT DO NOTHING;
