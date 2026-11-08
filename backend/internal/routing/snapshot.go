package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	snapshotPtr atomic.Pointer[RoutingSnapshot]
	snapshotMu  sync.Mutex // protects concurrent reloads
)

// CurrentSnapshot returns the current in-memory snapshot.
func CurrentSnapshot() *RoutingSnapshot {
	return snapshotPtr.Load()
}

// SwapSnapshot atomically replaces the current snapshot.
func SwapSnapshot(snap *RoutingSnapshot) {
	snapshotPtr.Store(snap)
}

// GetCoverageStatus retrieves coverage status for a provider, country, and optional operator.
func (s *RoutingSnapshot) GetCoverageStatus(providerID, countryID uuid.UUID, operatorID *uuid.UUID) string {
	if s.Coverage == nil {
		return "inactive"
	}
	if operatorID != nil && *operatorID != uuid.Nil {
		key := fmt.Sprintf("%s:%s:%s", providerID, countryID, *operatorID)
		if st, ok := s.Coverage[key]; ok {
			return st
		}
	}
	key := fmt.Sprintf("%s:%s", providerID, countryID)
	if st, ok := s.Coverage[key]; ok {
		return st
	}
	return "inactive"
}

// BuildSnapshot loads all routing data from DB into an immutable snapshot.
// No queries happen during Resolve() - everything is in this struct.
func BuildSnapshot(ctx context.Context, pool *pgxpool.Pool) (*RoutingSnapshot, error) {
	if pool == nil {
		return nil, fmt.Errorf("nil database pool")
	}

	snap := &RoutingSnapshot{
		BuildAt:           time.Now().UTC(),
		Overrides:         make(map[uuid.UUID]ConnectionOverride),
		Connections:       make(map[uuid.UUID]SnapshotConnection),
		Coverage:          make(map[string]string),
		CustomerProfiles:  make(map[uuid.UUID]SnapshotCustomerProfile),
		ApprovedSenderIDs: make(map[string]SnapshotSenderID),
	}

	// 1. Load active plans
	planRows, err := pool.Query(ctx, `
		SELECT id, name, code, COALESCE(description, ''), strategy, status, COALESCE(notes, ''),
		       created_by, created_at, updated_at, version
		FROM route_plans
		WHERE status = 'active'
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("load route plans: %w", err)
	}
	defer planRows.Close()

	plansMap := make(map[uuid.UUID]*RoutePlan)
	for planRows.Next() {
		var p RoutePlan
		if err := planRows.Scan(
			&p.ID, &p.Name, &p.Code, &p.Description, &p.Strategy, &p.Status, &p.Notes,
			&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt, &p.Version,
		); err != nil {
			return nil, fmt.Errorf("scan route plan: %w", err)
		}
		plansMap[p.ID] = &p
	}
	if err := planRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate route plans: %w", err)
	}

	// 2. Load active entries for active plans
	entryRows, err := pool.Query(ctx, `
		SELECT e.id, e.plan_id, e.connection_id, e.scope_type, e.scope_country_id, e.scope_operator_id,
		       COALESCE(e.scope_prefix, ''), e.priority, e.weight, e.min_margin_override,
		       e.allowed_sender_id_types, e.destination_number_format, e.allow_tested_coverage,
		       e.status, e.version, e.created_at, e.updated_at,
		       c.name AS connection_name, p.name AS provider_name
		FROM route_plan_entries e
		JOIN route_plans rp ON rp.id = e.plan_id
		JOIN provider_connections c ON c.id = e.connection_id
		JOIN providers p ON p.id = c.provider_id
		WHERE rp.status = 'active' AND e.status != 'archived'
		ORDER BY e.priority ASC, e.id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("load route plan entries: %w", err)
	}
	defer entryRows.Close()

	for entryRows.Next() {
		var e RoutePlanEntry
		if err := entryRows.Scan(
			&e.ID, &e.PlanID, &e.ConnectionID, &e.ScopeType, &e.ScopeCountryID, &e.ScopeOperatorID,
			&e.ScopePrefix, &e.Priority, &e.Weight, &e.MinMarginOverride,
			&e.AllowedSenderIDTypes, &e.DestinationNumberFormat, &e.AllowTestedCoverage,
			&e.Status, &e.Version, &e.CreatedAt, &e.UpdatedAt,
			&e.ConnectionName, &e.ProviderName,
		); err != nil {
			return nil, fmt.Errorf("scan route plan entry: %w", err)
		}
		if p, ok := plansMap[e.PlanID]; ok {
			p.Entries = append(p.Entries, e)
		}
	}
	if err := entryRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate route plan entries: %w", err)
	}

	for _, p := range plansMap {
		snap.Plans = append(snap.Plans, *p)
	}

	// 3. Load active customer route assignments
	assignRows, err := pool.Query(ctx, `
		SELECT a.id, a.customer_org_id, a.plan_id, a.country_id, a.priority,
		       a.effective_from, a.effective_to, a.status, a.version, a.created_by,
		       a.created_at, a.updated_at,
		       o.name AS customer_name, rp.name AS plan_name, COALESCE(c.name, '') AS country_name
		FROM customer_route_assignments a
		JOIN organizations o ON o.id = a.customer_org_id
		JOIN route_plans rp ON rp.id = a.plan_id
		LEFT JOIN countries c ON c.id = a.country_id
		WHERE a.status = 'active' AND rp.status = 'active'
		ORDER BY a.priority ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("load customer route assignments: %w", err)
	}
	defer assignRows.Close()

	for assignRows.Next() {
		var a CustomerRouteAssignment
		if err := assignRows.Scan(
			&a.ID, &a.CustomerOrgID, &a.PlanID, &a.CountryID, &a.Priority,
			&a.EffectiveFrom, &a.EffectiveTo, &a.Status, &a.Version, &a.CreatedBy,
			&a.CreatedAt, &a.UpdatedAt,
			&a.CustomerName, &a.PlanName, &a.CountryName,
		); err != nil {
			return nil, fmt.Errorf("scan customer route assignment: %w", err)
		}
		snap.Assignments = append(snap.Assignments, a)
	}
	if err := assignRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate customer route assignments: %w", err)
	}

	// 4. Load active routing rules
	ruleRows, err := pool.Query(ctx, `
		SELECT id, name, priority, status, effective_from, effective_to, conditions,
		       action_type, action_connection_id, COALESCE(action_block_reason_code, ''),
		       version, created_by, created_at, updated_at
		FROM routing_rules
		WHERE status = 'active'
		ORDER BY priority ASC, id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("load routing rules: %w", err)
	}
	defer ruleRows.Close()

	for ruleRows.Next() {
		var r RoutingRule
		var condBytes []byte
		if err := ruleRows.Scan(
			&r.ID, &r.Name, &r.Priority, &r.Status, &r.EffectiveFrom, &r.EffectiveTo,
			&condBytes, &r.ActionType, &r.ActionConnectionID, &r.ActionBlockReason,
			&r.Version, &r.CreatedBy, &r.CreatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan routing rule: %w", err)
		}
		if len(condBytes) > 0 {
			_ = json.Unmarshal(condBytes, &r.Conditions)
		}
		if r.Conditions == nil {
			r.Conditions = make(map[string]interface{})
		}
		snap.Rules = append(snap.Rules, r)
	}
	if err := ruleRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate routing rules: %w", err)
	}

	// 5. Load connection overrides (kill switch)
	overrideRows, err := pool.Query(ctx, `
		SELECT id, connection_id, state, reason, set_by, set_at
		FROM provider_connection_overrides
	`)
	if err != nil {
		return nil, fmt.Errorf("load connection overrides: %w", err)
	}
	defer overrideRows.Close()

	for overrideRows.Next() {
		var o ConnectionOverride
		if err := overrideRows.Scan(
			&o.ID, &o.ConnectionID, &o.State, &o.Reason, &o.SetBy, &o.SetAt,
		); err != nil {
			return nil, fmt.Errorf("scan connection override: %w", err)
		}
		snap.Overrides[o.ConnectionID] = o
	}
	if err := overrideRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate connection overrides: %w", err)
	}

	// 6. Load connections and providers status with their active buy rate cards
	connRows, err := pool.Query(ctx, `
		SELECT c.id, c.provider_id, p.status AS provider_status, c.status AS connection_status,
		       rc.id AS buy_card_id
		FROM provider_connections c
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN rate_cards rc ON rc.provider_id = p.id AND rc.kind = 'provider_buy' AND rc.status = 'active' AND NOT rc.archived
		WHERE NOT c.archived AND NOT p.archived
	`)
	if err != nil {
		return nil, fmt.Errorf("load provider connections: %w", err)
	}
	defer connRows.Close()

	for connRows.Next() {
		var c SnapshotConnection
		var buyCardID *uuid.UUID
		if err := connRows.Scan(&c.ID, &c.ProviderID, &c.ProviderStatus, &c.ConnectionStatus, &buyCardID); err != nil {
			return nil, fmt.Errorf("scan provider connection: %w", err)
		}
		c.BuyCardID = buyCardID
		if _, killed := snap.Overrides[c.ID]; killed {
			c.KillSwitched = true
		}
		snap.Connections[c.ID] = c
	}
	if err := connRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate provider connections: %w", err)
	}

	// 7. Load coverage map
	covRows, err := pool.Query(ctx, `
		SELECT provider_id, country_id, operator_id, status
		FROM provider_coverage
		WHERE NOT archived
	`)
	if err != nil {
		return nil, fmt.Errorf("load provider coverage: %w", err)
	}
	defer covRows.Close()

	for covRows.Next() {
		var providerID, countryID uuid.UUID
		var operatorID *uuid.UUID
		var status string
		if err := covRows.Scan(&providerID, &countryID, &operatorID, &status); err != nil {
			return nil, fmt.Errorf("scan provider coverage: %w", err)
		}
		if operatorID != nil && *operatorID != uuid.Nil {
			snap.Coverage[fmt.Sprintf("%s:%s:%s", providerID, countryID, *operatorID)] = status
		}
		// Also country-wide coverage
		countryKey := fmt.Sprintf("%s:%s", providerID, countryID)
		if existing, ok := snap.Coverage[countryKey]; !ok || (existing != "active" && status == "active") {
			snap.Coverage[countryKey] = status
		}
	}
	if err := covRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate provider coverage: %w", err)
	}

	// 8. Load customer profiles
	custRows, err := pool.Query(ctx, `
		SELECT o.id, o.status, COALESCE(cp.account_type, 'prepaid'), COALESCE(cp.allowed_sender_types, '{}')
		FROM organizations o
		LEFT JOIN customer_profiles cp ON cp.organization_id = o.id
		WHERE o.type = 'customer'
	`)
	if err == nil {
		defer custRows.Close()
		for custRows.Next() {
			var p SnapshotCustomerProfile
			if err := custRows.Scan(&p.OrgID, &p.OrgStatus, &p.AccountType, &p.AllowedSenderTypes); err == nil {
				snap.CustomerProfiles[p.OrgID] = p
			}
		}
	}

	// 9. Load approved sender IDs
	senderRows, err := pool.Query(ctx, `
		SELECT id, organization_id, sender_id, type, country_id, status
		FROM sender_ids
		WHERE status = 'approved'
	`)
	if err == nil {
		defer senderRows.Close()
		for senderRows.Next() {
			var s SnapshotSenderID
			if err := senderRows.Scan(&s.ID, &s.CustomerOrgID, &s.SenderID, &s.Type, &s.CountryID, &s.Status); err == nil {
				if s.CountryID != nil {
					snap.ApprovedSenderIDs[fmt.Sprintf("%s:%s:%s", s.CustomerOrgID, s.SenderID, *s.CountryID)] = s
				}
				snap.ApprovedSenderIDs[fmt.Sprintf("%s:%s", s.CustomerOrgID, s.SenderID)] = s
			}
		}
	}

	return snap, nil
}

// Reload reloads the snapshot from the DB and stores it. Returns error if failed.
func Reload(ctx context.Context, pool *pgxpool.Pool) (*RoutingSnapshot, error) {
	snapshotMu.Lock()
	defer snapshotMu.Unlock()

	snap, err := BuildSnapshot(ctx, pool)
	if err != nil {
		return nil, err
	}
	SwapSnapshot(snap)
	return snap, nil
}

// TryReload rebuilds the snapshot. On failure, keeps the last good one and logs loudly.
func TryReload(ctx context.Context, pool *pgxpool.Pool) {
	snapshotMu.Lock()
	defer snapshotMu.Unlock()

	snap, err := BuildSnapshot(ctx, pool)
	if err != nil {
		slog.Error("routing snapshot reload failed, keeping last good snapshot", "error", err)
		return
	}
	SwapSnapshot(snap)
	slog.Info("routing snapshot reloaded", "plans", len(snap.Plans), "rules", len(snap.Rules), "built_at", snap.BuildAt)
}
