-- Down migration for 000013: Per-Customer Billing Terms

-- Restore holds status constraint (converting any refunded back to released prior to re-constraining)
UPDATE holds SET status = 'released' WHERE status = 'refunded';
ALTER TABLE holds DROP CONSTRAINT IF EXISTS holds_status_check;
ALTER TABLE holds ADD CONSTRAINT holds_status_check
    CHECK (status IN ('active', 'captured', 'released', 'expired'));

-- Remove snapshot columns from messages
ALTER TABLE messages
    DROP COLUMN IF EXISTS billing_mode_snapshot,
    DROP COLUMN IF EXISTS dlr_wait_hours_snapshot,
    DROP COLUMN IF EXISTS no_dlr_policy_snapshot,
    DROP COLUMN IF EXISTS failed_after_accept_policy_snapshot;

-- Remove per-customer billing terms from customer_profiles
ALTER TABLE customer_profiles
    DROP COLUMN IF EXISTS billing_mode,
    DROP COLUMN IF EXISTS dlr_wait_hours,
    DROP COLUMN IF EXISTS no_dlr_policy,
    DROP COLUMN IF EXISTS failed_after_accept_policy;

