-- Phase 6: Wallets, Double-Entry Ledger, Credit Enforcement & Invoicing Basics Migration (DOWN)

DELETE FROM role_permissions WHERE permission_code IN (
    'wallet.wallet.view', 'wallet.topup.create', 'wallet.adjustment.create', 'wallet.freeze',
    'wallet.override', 'wallet.approve', 'billing.invoice.view', 'billing.invoice.create',
    'billing.invoice.issue', 'billing.invoice.void', 'billing.invoice.creditnote',
    'billing.payment.create', 'billing.payment.view', 'ledger.view', 'ledger.reconcile'
);

DELETE FROM permissions WHERE module IN ('wallet', 'billing', 'ledger');

DROP TRIGGER IF EXISTS trg_protect_issued_invoice_lines ON invoice_lines;
DROP FUNCTION IF EXISTS protect_issued_invoice_lines();

DROP TRIGGER IF EXISTS trg_protect_issued_invoice ON invoices;
DROP FUNCTION IF EXISTS protect_issued_invoice();

DROP TABLE IF EXISTS payments CASCADE;
DROP TABLE IF EXISTS invoice_lines CASCADE;
DROP TABLE IF EXISTS invoices CASCADE;
DROP TABLE IF EXISTS invoice_sequences CASCADE;
DROP TABLE IF EXISTS billing_periods CASCADE;

DROP TABLE IF EXISTS holds CASCADE;

DROP TRIGGER IF EXISTS trg_ledger_entries_immutable ON ledger_entries;
DROP TRIGGER IF EXISTS trg_ledger_transactions_immutable ON ledger_transactions;
DROP TRIGGER IF EXISTS trg_ledger_accounts_immutable ON ledger_accounts;
DROP FUNCTION IF EXISTS block_ledger_mutation();

DROP TRIGGER IF EXISTS trg_verify_ledger_balanced ON ledger_entries;
DROP FUNCTION IF EXISTS verify_ledger_transaction_balanced();

DROP TABLE IF EXISTS ledger_entries CASCADE;
DROP TABLE IF EXISTS ledger_transactions CASCADE;
DROP TABLE IF EXISTS ledger_accounts CASCADE;
DROP TABLE IF EXISTS wallets CASCADE;

ALTER TABLE change_requests DROP CONSTRAINT IF EXISTS change_requests_operation_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_operation_check
    CHECK (operation IN (
        'create', 'update', 'deactivate', 'activate', 'archive', 'rollback',
        'confirm_import', 'status_change', 'secret_update', 'approve_sender_id'
    ));

ALTER TABLE change_requests DROP CONSTRAINT IF EXISTS change_requests_entity_type_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_entity_type_check
    CHECK (entity_type IN (
        'country', 'operator', 'prefix', 'number_rule', 'import_job',
        'provider', 'provider_connection', 'provider_coverage', 'customer_profile', 'sender_id',
        'rate_card', 'rate', 'rate_assignment', 'reseller_cost_assignment'
    ));
