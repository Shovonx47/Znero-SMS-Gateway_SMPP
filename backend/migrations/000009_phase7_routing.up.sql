-- Phase 7: Routing Engine (Decision Only), Route Plans, Rules, Simulator, and Kill Switch Migration (UP)

-- Route Plans
CREATE TABLE route_plans (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  code TEXT NOT NULL UNIQUE,
  description TEXT,
  strategy TEXT NOT NULL CHECK (strategy IN ('priority','least_cost','weighted')),
  status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','active','deactivated','archived')),
  notes TEXT,
  created_by UUID NOT NULL REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  version INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX idx_route_plans_code ON route_plans(code);
CREATE INDEX idx_route_plans_status ON route_plans(status);

CREATE TABLE route_plan_history (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  plan_id UUID NOT NULL REFERENCES route_plans(id),
  changed_by UUID NOT NULL REFERENCES users(id),
  changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  change_type TEXT NOT NULL,
  snapshot JSONB NOT NULL
);

CREATE INDEX idx_route_plan_history_plan ON route_plan_history(plan_id);

-- Route Plan Entries
CREATE TABLE route_plan_entries (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  plan_id UUID NOT NULL REFERENCES route_plans(id),
  connection_id UUID NOT NULL REFERENCES provider_connections(id),
  scope_type TEXT NOT NULL DEFAULT 'global' CHECK (scope_type IN ('global','country','operator','prefix')),
  scope_country_id UUID REFERENCES countries(id),
  scope_operator_id UUID REFERENCES operators(id),
  scope_prefix TEXT,
  priority INTEGER NOT NULL DEFAULT 1,
  weight INTEGER NOT NULL DEFAULT 100 CHECK (weight BETWEEN 1 AND 100),
  min_margin_override NUMERIC(18,6),
  allowed_sender_id_types TEXT[] NOT NULL DEFAULT '{}',
  destination_number_format TEXT NOT NULL DEFAULT 'e164',
  allow_tested_coverage BOOLEAN NOT NULL DEFAULT false,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled','archived')),
  version INTEGER NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_route_plan_entries_plan ON route_plan_entries(plan_id);
CREATE INDEX idx_route_plan_entries_connection ON route_plan_entries(connection_id);
CREATE INDEX idx_route_plan_entries_status ON route_plan_entries(status);

CREATE TABLE route_plan_entry_history (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  entry_id UUID NOT NULL REFERENCES route_plan_entries(id),
  changed_by UUID NOT NULL REFERENCES users(id),
  changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  change_type TEXT NOT NULL,
  snapshot JSONB NOT NULL
);

CREATE INDEX idx_route_plan_entry_history_entry ON route_plan_entry_history(entry_id);

-- Customer Route Assignments
CREATE TABLE customer_route_assignments (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  customer_org_id UUID NOT NULL REFERENCES organizations(id),
  plan_id UUID NOT NULL REFERENCES route_plans(id),
  country_id UUID REFERENCES countries(id),
  priority INTEGER NOT NULL DEFAULT 1,
  effective_from TIMESTAMPTZ,
  effective_to TIMESTAMPTZ,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled','archived')),
  version INTEGER NOT NULL DEFAULT 1,
  created_by UUID NOT NULL REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (customer_org_id, plan_id, country_id)
);

CREATE INDEX idx_customer_route_assignments_customer ON customer_route_assignments(customer_org_id);
CREATE INDEX idx_customer_route_assignments_plan ON customer_route_assignments(plan_id);

CREATE TABLE customer_route_assignment_history (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  assignment_id UUID NOT NULL REFERENCES customer_route_assignments(id),
  changed_by UUID NOT NULL REFERENCES users(id),
  changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  change_type TEXT NOT NULL,
  snapshot JSONB NOT NULL
);

CREATE INDEX idx_customer_route_assignment_history_assignment ON customer_route_assignment_history(assignment_id);

-- Routing Rules
CREATE TABLE routing_rules (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  priority INTEGER NOT NULL DEFAULT 1,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled','archived')),
  effective_from TIMESTAMPTZ,
  effective_to TIMESTAMPTZ,
  conditions JSONB NOT NULL DEFAULT '{}',
  action_type TEXT NOT NULL CHECK (action_type IN ('block','force','avoid','prefer')),
  action_connection_id UUID REFERENCES provider_connections(id),
  action_block_reason_code TEXT,
  version INTEGER NOT NULL DEFAULT 1,
  created_by UUID NOT NULL REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_routing_rules_priority ON routing_rules(priority);
CREATE INDEX idx_routing_rules_status ON routing_rules(status);

CREATE TABLE routing_rule_history (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  rule_id UUID NOT NULL REFERENCES routing_rules(id),
  changed_by UUID NOT NULL REFERENCES users(id),
  changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  change_type TEXT NOT NULL,
  snapshot JSONB NOT NULL
);

CREATE INDEX idx_routing_rule_history_rule ON routing_rule_history(rule_id);

-- Provider Connection Overrides (Kill Switch)
CREATE TABLE provider_connection_overrides (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  connection_id UUID NOT NULL UNIQUE REFERENCES provider_connections(id),
  state TEXT NOT NULL CHECK (state IN ('disabled')),
  reason TEXT NOT NULL,
  set_by UUID NOT NULL REFERENCES users(id),
  set_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_provider_connection_overrides_conn ON provider_connection_overrides(connection_id);

-- Extend change_requests constraints for maker-checker routing entity types
ALTER TABLE change_requests DROP CONSTRAINT IF EXISTS change_requests_entity_type_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_entity_type_check
    CHECK (entity_type IN (
        'country', 'operator', 'prefix', 'number_rule', 'import_job',
        'provider', 'provider_connection', 'provider_coverage', 'customer_profile', 'sender_id',
        'rate_card', 'rate', 'rate_assignment', 'reseller_cost_assignment',
        'wallet_topup', 'wallet_adjustment', 'wallet_unfreeze', 'invoice_void',
        'route_plan_activate', 'route_plan_deactivate', 'route_plan_entry_change', 'route_assignment_change', 'routing_rule_change', 'connection_reenable'
    ));

ALTER TABLE change_requests DROP CONSTRAINT IF EXISTS change_requests_operation_check;
ALTER TABLE change_requests ADD CONSTRAINT change_requests_operation_check
    CHECK (operation IN (
        'create', 'update', 'deactivate', 'activate', 'archive', 'rollback',
        'confirm_import', 'status_change', 'secret_update', 'approve_sender_id',
        'topup', 'adjustment', 'unfreeze', 'void', 'reenable', 'enable'
    ));

-- Register permissions
INSERT INTO permissions (code, module, resource, action, description) VALUES
  ('routing.plan.view', 'routing', 'plan', 'view', 'View route plans and history'),
  ('routing.plan.create', 'routing', 'plan', 'create', 'Create route plans'),
  ('routing.plan.edit', 'routing', 'plan', 'edit', 'Edit route plans'),
  ('routing.plan.activate', 'routing', 'plan', 'activate', 'Activate route plans (submit approval)'),
  ('routing.plan.deactivate', 'routing', 'plan', 'deactivate', 'Deactivate route plans (submit approval)'),
  ('routing.plan.archive', 'routing', 'plan', 'archive', 'Archive route plans'),
  ('routing.plan.approve', 'routing', 'plan', 'approve', 'Approve route plan status changes'),
  ('routing.entry.view', 'routing', 'entry', 'view', 'View route plan entries'),
  ('routing.entry.create', 'routing', 'entry', 'create', 'Create route plan entries'),
  ('routing.entry.edit', 'routing', 'entry', 'edit', 'Edit or remove route plan entries'),
  ('routing.entry.approve', 'routing', 'entry', 'approve', 'Approve route plan entry changes'),
  ('routing.assignment.view', 'routing', 'assignment', 'view', 'View customer route assignments'),
  ('routing.assignment.create', 'routing', 'assignment', 'create', 'Create customer route assignments'),
  ('routing.assignment.edit', 'routing', 'assignment', 'edit', 'Edit customer route assignments'),
  ('routing.assignment.approve', 'routing', 'assignment', 'approve', 'Approve customer route assignment changes'),
  ('routing.rule.view', 'routing', 'rule', 'view', 'View routing rules'),
  ('routing.rule.create', 'routing', 'rule', 'create', 'Create routing rules'),
  ('routing.rule.edit', 'routing', 'rule', 'edit', 'Edit routing rules'),
  ('routing.rule.activate', 'routing', 'rule', 'activate', 'Activate/deactivate routing rules'),
  ('routing.rule.approve', 'routing', 'rule', 'approve', 'Approve routing rule changes'),
  ('routing.simulate', 'routing', 'simulate', 'execute', 'Run routing simulator'),
  ('routing.killswitch', 'routing', 'connection', 'killswitch', 'Manage provider connection kill switch overrides'),
  ('routing.snapshot.view', 'routing', 'snapshot', 'view', 'View routing snapshot status and coverage report'),
  ('routing.override', 'routing', 'routing', 'override', 'Bypass maker-checker approval across all routing entities (Platform Super Admin only)'),
  ('routing.entry.override', 'routing', 'entry', 'override', 'Bypass maker-checker approval for route plan entry changes on active plans only')
ON CONFLICT (code) DO NOTHING;

-- Assign ALL routing permissions to platform Super Admin / Platform Admin roles only. No reseller or customer roles get any routing permissions.
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'platform'
CROSS JOIN permissions p
WHERE r.name IN ('Super Admin', 'Platform Admin')
  AND p.module = 'routing'
ON CONFLICT DO NOTHING;

-- Create dedicated Routing Maker and Routing Checker system role templates for the platform organization
INSERT INTO roles (organization_id, name, description, is_system)
SELECT o.id, v.name, v.descr, true
FROM organizations o
CROSS JOIN (VALUES
    ('Routing Maker',   'Maker: creates and edits route plans, entries, customer assignments, and routing rules; changes require approval'),
    ('Routing Checker', 'Checker: reviews and approves or rejects routing change requests and manages emergency kill switch')
) AS v(name, descr)
WHERE o.type = 'platform'
ON CONFLICT (organization_id, name) DO NOTHING;

-- Ensure Master Data Editor and Master Data Approver do NOT hold routing.* permissions
DELETE FROM role_permissions rp
USING roles r
WHERE rp.role_id = r.id
  AND r.name IN ('Master Data Editor', 'Master Data Approver')
  AND rp.permission_code LIKE 'routing.%';

-- Grant maker permissions (strictly WITHOUT routing.override, routing.entry.override, or *.approve) to Routing Maker
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'platform'
CROSS JOIN permissions p
WHERE r.name = 'Routing Maker'
  AND p.code IN (
      'routing.plan.view', 'routing.plan.create', 'routing.plan.edit', 'routing.plan.activate', 'routing.plan.deactivate', 'routing.plan.archive',
      'routing.entry.view', 'routing.entry.create', 'routing.entry.edit',
      'routing.assignment.view', 'routing.assignment.create', 'routing.assignment.edit',
      'routing.rule.view', 'routing.rule.create', 'routing.rule.edit', 'routing.rule.activate',
      'routing.simulate',
      'routing.snapshot.view',
      'provider.provider.view', 'provider.connection.view', 'customer.profile.view',
      'country.country.view', 'operator.operator.view'
  )
ON CONFLICT DO NOTHING;

-- Grant checker permissions (strictly WITHOUT routing.override or routing.entry.override) to Routing Checker
INSERT INTO role_permissions (role_id, permission_code)
SELECT r.id, p.code
FROM roles r
JOIN organizations o ON o.id = r.organization_id AND o.type = 'platform'
CROSS JOIN permissions p
WHERE r.name = 'Routing Checker'
  AND p.code IN (
      'routing.plan.view', 'routing.plan.approve',
      'routing.entry.view', 'routing.entry.approve',
      'routing.assignment.view', 'routing.assignment.approve',
      'routing.rule.view', 'routing.rule.approve', 'routing.rule.activate',
      'routing.simulate',
      'routing.snapshot.view',
      'routing.killswitch',
      'provider.provider.view', 'provider.connection.view', 'customer.profile.view',
      'country.country.view', 'operator.operator.view'
  )
ON CONFLICT DO NOTHING;

