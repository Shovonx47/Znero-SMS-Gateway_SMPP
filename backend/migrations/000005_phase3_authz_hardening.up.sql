-- Phase 3 hardening (part 1): checker permission for import confirmation requests.
INSERT INTO permissions (code, module, resource, action, description) VALUES
    ('import.job.approve', 'import', 'job', 'approve', 'Review and approve/reject import confirmation change requests')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, 'import.job.approve'
FROM roles r
WHERE r.name = 'Super Admin'
ON CONFLICT DO NOTHING;

-- Part 2: platform-only permissions must never be held by reseller/customer roles. The old
-- "Org Admin" template granted every permission to every organization, so purge them.
DELETE FROM role_permissions rp
USING roles r, organizations o
WHERE rp.role_id = r.id
  AND r.organization_id = o.id
  AND o.type <> 'platform'
  AND (
        rp.permission_code LIKE 'provider.%'
     OR rp.permission_code LIKE 'import.%'
     OR rp.permission_code LIKE 'number.rule.%' AND rp.permission_code <> 'number.rule.view'
     OR rp.permission_code LIKE 'country.country.%' AND rp.permission_code <> 'country.country.view'
     OR rp.permission_code LIKE 'operator.operator.%' AND rp.permission_code <> 'operator.operator.view'
     OR rp.permission_code LIKE 'number.prefix.%' AND rp.permission_code <> 'number.prefix.view'
  );

-- Part 3: maker and checker role templates for the platform organization (no override permission).
INSERT INTO roles (organization_id, name, description, is_system)
SELECT o.id, v.name, v.descr, true
FROM organizations o
CROSS JOIN (VALUES
    ('Master Data Editor',   'Maker: edits master data; every change is routed to a pending change request'),
    ('Master Data Approver', 'Checker: reviews and approves or rejects master data change requests')
) AS v(name, descr)
WHERE o.type = 'platform'
ON CONFLICT (organization_id, name) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, c.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'platform'
JOIN (VALUES
    ('Master Data Editor','country.country.view'),('Master Data Editor','country.country.create'),('Master Data Editor','country.country.edit'),
    ('Master Data Editor','country.country.deactivate'),('Master Data Editor','country.country.archive'),('Master Data Editor','country.country.import'),
    ('Master Data Editor','operator.operator.view'),('Master Data Editor','operator.operator.create'),('Master Data Editor','operator.operator.edit'),
    ('Master Data Editor','operator.operator.deactivate'),('Master Data Editor','operator.operator.archive'),('Master Data Editor','operator.operator.import'),
    ('Master Data Editor','number.prefix.view'),('Master Data Editor','number.prefix.create'),('Master Data Editor','number.prefix.edit'),
    ('Master Data Editor','number.prefix.deactivate'),('Master Data Editor','number.prefix.archive'),('Master Data Editor','number.prefix.import'),
    ('Master Data Editor','number.rule.view'),('Master Data Editor','number.rule.create'),('Master Data Editor','number.rule.edit'),('Master Data Editor','number.rule.delete'),
    ('Master Data Editor','number.lookup.use'),('Master Data Editor','import.job.view'),('Master Data Editor','import.job.manage'),
    ('Master Data Approver','country.country.view'),('Master Data Approver','country.country.approve'),
    ('Master Data Approver','operator.operator.view'),('Master Data Approver','operator.operator.approve'),
    ('Master Data Approver','number.prefix.view'),('Master Data Approver','number.prefix.approve'),
    ('Master Data Approver','number.rule.view'),('Master Data Approver','number.rule.approve'),
    ('Master Data Approver','import.job.view'),('Master Data Approver','import.job.approve')
) AS c(role_name, code) ON c.role_name = r.name
WHERE EXISTS (SELECT 1 FROM permissions p WHERE p.code = c.code)
ON CONFLICT DO NOTHING;
