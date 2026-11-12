-- Migration 000015 Down: Revert Phase 9 Visibility Foundation

DROP TABLE IF EXISTS export_jobs CASCADE;
DROP TABLE IF EXISTS user_preferences CASCADE;
DROP TABLE IF EXISTS saved_filters CASCADE;
DROP TABLE IF EXISTS settings_registry CASCADE;
DROP TABLE IF EXISTS incident_timeline CASCADE;
DROP TABLE IF EXISTS incidents CASCADE;
DROP TABLE IF EXISTS alerts CASCADE;
DROP TABLE IF EXISTS alert_rules CASCADE;
DROP TABLE IF EXISTS notification_preferences CASCADE;
DROP TABLE IF EXISTS notifications CASCADE;
DROP TABLE IF EXISTS service_heartbeats CASCADE;
DROP TABLE IF EXISTS analytics_rollup_destination_15m CASCADE;
DROP TABLE IF EXISTS analytics_rollup_provider_15m CASCADE;
DROP TABLE IF EXISTS analytics_rollup_customer_15m CASCADE;
DROP TABLE IF EXISTS analytics_cursors CASCADE;

DELETE FROM role_permissions WHERE permission_code IN (
    'analytics.dashboard.view', 'analytics.noc.view', 'analytics.report.view',
    'analytics.export.create', 'analytics.export.view',
    'notification.view', 'notification.manage',
    'alert.view', 'alert.manage',
    'incident.view', 'incident.manage',
    'setting.view', 'setting.manage'
);

DELETE FROM permissions WHERE code IN (
    'analytics.dashboard.view', 'analytics.noc.view', 'analytics.report.view',
    'analytics.export.create', 'analytics.export.view',
    'notification.view', 'notification.manage',
    'alert.view', 'alert.manage',
    'incident.view', 'incident.manage',
    'setting.view', 'setting.manage'
);

