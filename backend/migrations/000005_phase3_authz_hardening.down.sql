DELETE FROM role_permissions WHERE role_id IN (SELECT id FROM roles WHERE name IN ('Master Data Editor','Master Data Approver') AND is_system);
DELETE FROM roles WHERE name IN ('Master Data Editor','Master Data Approver') AND is_system;
DELETE FROM role_permissions WHERE permission_code = 'import.job.approve';
DELETE FROM permissions WHERE code = 'import.job.approve';
-- NOTE: the purge of platform-only permissions from reseller/customer roles is intentionally not reverted.
