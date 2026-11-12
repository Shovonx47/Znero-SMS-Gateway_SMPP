-- Repair script for BUG-016 / BUG-046:
-- Idempotently purges platform-only permissions (including system.info.view)
-- from non-platform organization roles, and customer-forbidden permissions from
-- customer-organization roles.
--
-- Target database MUST be explicitly selected by caller (e.g. znero_test).
-- Safe to re-run repeatedly; executes inside a single transaction.

BEGIN;

-- 1. Purge platform-only permissions held by reseller or customer organization roles.
DELETE FROM role_permissions rp
USING roles r, organizations o
WHERE rp.role_id = r.id
  AND r.organization_id = o.id
  AND o.type <> 'platform'
  AND (
    -- Exact platform-only codes (rbac.platformOnlyExact)
    rp.permission_code IN (
      'customer.profile.approve',
      'customer.profile.override',
      'customer.senderid.approve',
      'customer.senderid.override',
      'rate.sellcard.approve',
      'rate.sellcard.override',
      'rate.sellrate.approve',
      'rate.sellrate.override',
      'rate.assignment.create',
      'rate.assignment.update',
      'rate.assignment.approve',
      'rate.assignment.override',
      'wallet.override',
      'wallet.approve',
      'billing.invoice.void',
      'billing.invoice.creditnote',
      'ledger.reconcile',
      'message.dlr_mapping.manage',
      'message.queue.view',
      'message.reconcile',
      'system.info.view'
    )
    -- Namespace platform-only prefixes (rbac.platformOnlyPrefixes, allowing *.view on master data)
    OR rp.permission_code LIKE 'provider.%'
    OR rp.permission_code LIKE 'import.%'
    OR rp.permission_code LIKE 'rate.buycard.%'
    OR rp.permission_code LIKE 'rate.buyrate.%'
    OR rp.permission_code LIKE 'rate.margin.%'
    OR rp.permission_code LIKE 'routing.%'
    OR rp.permission_code LIKE 'message.platform.%'
    OR rp.permission_code LIKE 'customer.smppsession.%'
    OR rp.permission_code LIKE 'inbound.number.%'
    OR ((rp.permission_code LIKE 'country.country.%'
      OR rp.permission_code LIKE 'operator.operator.%'
      OR rp.permission_code LIKE 'number.prefix.%'
      OR rp.permission_code LIKE 'number.rule.%')
      AND rp.permission_code NOT LIKE '%.view')
  );

-- 2. Purge customer-forbidden commercial/financial write permissions from customer roles.
DELETE FROM role_permissions rp
USING roles r, organizations o
WHERE rp.role_id = r.id
  AND r.organization_id = o.id
  AND o.type = 'customer'
  AND rp.permission_code IN (
    'customer.profile.create',
    'customer.profile.edit',
    'customer.profile.approve',
    'customer.profile.override',
    'customer.smppaccount.create',
    'customer.smppaccount.edit',
    'customer.smppaccount.revoke',
    'customer.senderid.edit',
    'customer.senderid.approve',
    'customer.senderid.override',
    'customer.senderid.import',
    'rate.sellcard.create',
    'rate.sellcard.update',
    'rate.sellcard.archive',
    'rate.sellrate.create',
    'rate.sellrate.update',
    'rate.assignment.create',
    'rate.assignment.update',
    'rate.assignment.read',
    'rate.import.upload',
    'rate.import.confirm',
    'wallet.topup.create',
    'wallet.adjustment.create',
    'wallet.freeze',
    'billing.invoice.create',
    'billing.invoice.issue',
    'billing.payment.create'
  );

COMMIT;
