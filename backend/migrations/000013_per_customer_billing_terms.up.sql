-- =============================================================================
-- Migration 000013: Per-Customer Billing Terms & Message Terms Snapshot
-- =============================================================================

-- 1. Add per-customer billing terms to customer_profiles
ALTER TABLE customer_profiles
    ADD COLUMN billing_mode VARCHAR(32) NOT NULL DEFAULT 'on_delivery'
        CONSTRAINT chk_customer_profiles_billing_mode CHECK (billing_mode IN ('on_provider_accept', 'on_delivery')),
    ADD COLUMN dlr_wait_hours INT NOT NULL DEFAULT 24
        CONSTRAINT chk_customer_profiles_dlr_wait_hours CHECK (dlr_wait_hours >= 1 AND dlr_wait_hours <= 168),
    ADD COLUMN no_dlr_policy VARCHAR(32) NOT NULL DEFAULT 'retain_until_resolution'
        CONSTRAINT chk_customer_profiles_no_dlr_policy CHECK (no_dlr_policy IN ('retain_until_resolution', 'immediate_release', 'capture_at_ttl')),
    ADD COLUMN failed_after_accept_policy VARCHAR(32) NOT NULL DEFAULT 'keep_charge'
        CONSTRAINT chk_customer_profiles_failed_after_accept_policy CHECK (failed_after_accept_policy IN ('keep_charge', 'refund'));

-- Backfill existing customer profiles with the active global billing trigger setting (if present)
DO $$
DECLARE
    glob_trig TEXT;
BEGIN
    SELECT trim(both '"' FROM value::text)
    INTO glob_trig
    FROM system_settings
    WHERE key = 'billing.sms.customer_trigger';

    IF glob_trig IN ('on_provider_accept', 'on_delivery') THEN
        UPDATE customer_profiles SET billing_mode = glob_trig;
    END IF;
END $$;

-- 2. Add immutable billing terms snapshot columns to partitioned messages table
-- (PostgreSQL automatically propagates ALTER TABLE ADD COLUMN to all child partitions.)
ALTER TABLE messages
    ADD COLUMN billing_mode_snapshot VARCHAR(32) NOT NULL DEFAULT 'on_delivery'
        CONSTRAINT chk_messages_billing_mode_snapshot CHECK (billing_mode_snapshot IN ('on_provider_accept', 'on_delivery')),
    ADD COLUMN dlr_wait_hours_snapshot INT NOT NULL DEFAULT 24
        CONSTRAINT chk_messages_dlr_wait_hours_snapshot CHECK (dlr_wait_hours_snapshot >= 1 AND dlr_wait_hours_snapshot <= 168),
    ADD COLUMN no_dlr_policy_snapshot VARCHAR(32) NOT NULL DEFAULT 'retain_until_resolution'
        CONSTRAINT chk_messages_no_dlr_policy_snapshot CHECK (no_dlr_policy_snapshot IN ('retain_until_resolution', 'immediate_release', 'capture_at_ttl')),
    ADD COLUMN failed_after_accept_policy_snapshot VARCHAR(32) NOT NULL DEFAULT 'keep_charge'
        CONSTRAINT chk_messages_failed_after_accept_policy_snapshot CHECK (failed_after_accept_policy_snapshot IN ('keep_charge', 'refund'));

-- 3. Allow holds to transition to 'refunded' when failed_after_accept_policy = 'refund'
ALTER TABLE holds DROP CONSTRAINT IF EXISTS holds_status_check;
ALTER TABLE holds ADD CONSTRAINT holds_status_check
    CHECK (status IN ('active', 'captured', 'released', 'expired', 'refunded'));

