// Command seedroutingsample loads or cleans up clearly labeled development sample
// route plans, entries, customer assignments, and routing rules through the service layer.
// Strictly forbidden from running outside APP_ENV=development.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/znero-international/znero/backend/internal/audit"
	"github.com/znero-international/znero/backend/internal/config"
	"github.com/znero-international/znero/backend/internal/masterdata/approval"
	"github.com/znero-international/znero/backend/internal/routing"
	"github.com/znero-international/znero/backend/pkg/database"
	"github.com/znero-international/znero/backend/pkg/tenant"
)

func main() {
	cleanup := flag.Bool("cleanup", false, "Remove all DEV SAMPLE route plans, entries, assignments, and rules")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	if cfg.Env != config.EnvDevelopment {
		fmt.Fprintf(os.Stderr, "FATAL: Routing sample seed is strictly prohibited outside APP_ENV=development. Current environment: %q\n", cfg.Env)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := database.Connect(ctx, database.Config{
		URL:            cfg.Database.URL,
		MaxConns:       5,
		ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Database connection failed: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	if *cleanup {
		fmt.Println("=== Cleaning up DEV SAMPLE routing plans, entries, assignments, and rules ===")
		if err := runCleanup(ctx, pool); err != nil {
			fmt.Fprintf(os.Stderr, "Cleanup failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Cleanup completed successfully.")
		return
	}

	fmt.Println("=== Seeding DEV SAMPLE routing plans, entries, assignments, and rules via service layer ===")
	if err := runSeed(ctx, pool); err != nil {
		fmt.Fprintf(os.Stderr, "Seed failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("DEV SAMPLE Phase 7 routing seed completed successfully.")
}

func runCleanup(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	stmts := []string{
		`DELETE FROM customer_route_assignment_history WHERE assignment_id IN (
			SELECT a.id FROM customer_route_assignments a
			JOIN route_plans p ON a.plan_id = p.id
			WHERE p.name LIKE 'DEV SAMPLE%'
		)`,
		`DELETE FROM customer_route_assignments WHERE plan_id IN (
			SELECT id FROM route_plans WHERE name LIKE 'DEV SAMPLE%'
		)`,
		`DELETE FROM routing_rule_history WHERE rule_id IN (
			SELECT id FROM routing_rules WHERE name LIKE 'DEV SAMPLE%'
		)`,
		`DELETE FROM routing_rules WHERE name LIKE 'DEV SAMPLE%'`,
		`DELETE FROM route_plan_entry_history WHERE entry_id IN (
			SELECT e.id FROM route_plan_entries e
			JOIN route_plans p ON e.plan_id = p.id
			WHERE p.name LIKE 'DEV SAMPLE%'
		)`,
		`DELETE FROM route_plan_entries WHERE plan_id IN (
			SELECT id FROM route_plans WHERE name LIKE 'DEV SAMPLE%'
		)`,
		`DELETE FROM route_plan_history WHERE plan_id IN (
			SELECT id FROM route_plans WHERE name LIKE 'DEV SAMPLE%'
		)`,
		`DELETE FROM route_plans WHERE name LIKE 'DEV SAMPLE%'`,
	}

	for _, s := range stmts {
		tag, err := tx.Exec(ctx, s)
		if err != nil {
			return fmt.Errorf("cleanup statement failed: %w", err)
		}
		fmt.Printf("Affected %d rows running: %s\n", tag.RowsAffected(), s[:min(35, len(s))])
	}

	return tx.Commit(ctx)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func runSeed(ctx context.Context, pool *pgxpool.Pool) error {
	_ = runCleanup(ctx, pool)

	var platformOrgID uuid.UUID
	var platformOrgPath string
	if err := pool.QueryRow(ctx, `SELECT id, path FROM organizations WHERE type = 'platform' LIMIT 1`).Scan(&platformOrgID, &platformOrgPath); err != nil {
		return fmt.Errorf("platform organization not found: %w", err)
	}

	var actorUserID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE organization_id = $1 AND status = 'active' ORDER BY created_at ASC LIMIT 1`, platformOrgID).Scan(&actorUserID); err != nil {
		return fmt.Errorf("active platform user not found: %w", err)
	}

	platformTC := &tenant.Context{
		ActorUserID:     actorUserID,
		ActorOrgID:      platformOrgID,
		ActorOrgType:    "platform",
		ActorOrgPath:    platformOrgPath,
		IsPlatformAdmin: true,
		Permissions: map[string]bool{
			"routing.plan.view":          true,
			"routing.plan.create":        true,
			"routing.plan.edit":          true,
			"routing.plan.activate":      true,
			"routing.plan.deactivate":    true,
			"routing.plan.archive":       true,
			"routing.plan.approve":       true,
			"routing.entry.view":         true,
			"routing.entry.create":       true,
			"routing.entry.edit":         true,
			"routing.entry.approve":      true,
			"routing.assignment.view":    true,
			"routing.assignment.create":  true,
			"routing.assignment.edit":    true,
			"routing.assignment.approve": true,
			"routing.rule.view":          true,
			"routing.rule.create":        true,
			"routing.rule.edit":          true,
			"routing.rule.activate":      true,
			"routing.rule.approve":       true,
			"routing.simulate":           true,
			"routing.killswitch":         true,
			"routing.snapshot.view":      true,
		},
	}

	auditSvc := audit.NewService(audit.NewRepository(pool))
	approvalSvc := approval.NewService(approval.NewRepository(pool), auditSvc, pool)
	routingRepo := routing.NewRepository(pool)
	routingEngine := routing.NewEngine(pool, nil, nil, nil)
	routingSvc := routing.NewService(routingRepo, approvalSvc, auditSvc, pool, routingEngine)

	// Fetch 3 active provider connections
	rows, err := pool.Query(ctx, `
		SELECT c.id FROM provider_connections c
		JOIN providers p ON c.provider_id = p.id
		WHERE c.status = 'active' AND p.status = 'active'
		ORDER BY c.created_at ASC
		LIMIT 3
	`)
	if err != nil {
		return fmt.Errorf("query connections: %w", err)
	}
	defer rows.Close()

	var connIDs []uuid.UUID
	for rows.Next() {
		var cid uuid.UUID
		if err := rows.Scan(&cid); err != nil {
			return err
		}
		connIDs = append(connIDs, cid)
	}
	if len(connIDs) < 3 {
		return fmt.Errorf("need at least 3 active connections in development database, found %d", len(connIDs))
	}

	// Find customer BlueStar SMS Global (Dev)
	var blueStarOrgID uuid.UUID
	err = pool.QueryRow(ctx, `SELECT id FROM organizations WHERE name LIKE 'BlueStar SMS Global%' LIMIT 1`).Scan(&blueStarOrgID)
	if err != nil {
		return fmt.Errorf("customer organization BlueStar not found: %w", err)
	}

	// Find country US or GB for specific assignment
	var sampleCountryID uuid.UUID
	err = pool.QueryRow(ctx, `SELECT id FROM countries WHERE iso2 IN ('US', 'GB') ORDER BY iso2 ASC LIMIT 1`).Scan(&sampleCountryID)
	if err != nil {
		return fmt.Errorf("sample country not found: %w", err)
	}

	// 1. Create Route Plan: "DEV SAMPLE - Standard Priority"
	plan1, err := routingSvc.CreatePlan(ctx, platformTC, &routing.RoutePlan{
		Name:        "DEV SAMPLE - Standard Priority",
		Code:        "DEV-STD-PRIORITY",
		Description: "Standard priority-based routing plan with tiered failover (DEV SAMPLE)",
		Strategy:    routing.StrategyPriority,
		Notes:       "DEV SAMPLE - for local testing",
	})
	if err != nil {
		return fmt.Errorf("create plan 1: %w", err)
	}
	fmt.Printf("Created route plan 1: %s (%s)\n", plan1.Name, plan1.ID)

	// Add 3 entries to plan 1
	// Connection 1: Priority 1
	_, _, err = routingSvc.CreateEntry(ctx, platformTC, &routing.RoutePlanEntry{
		PlanID:                  plan1.ID,
		ConnectionID:            connIDs[0],
		ScopeType:               "global",
		Priority:                1,
		Weight:                  100,
		DestinationNumberFormat: "e164",
		Status:                  "active",
	})
	if err != nil {
		return fmt.Errorf("add entry 1 to plan 1: %w", err)
	}

	// Connection 2: Priority 2 with sender ID constraint (alphanumeric only)
	_, _, err = routingSvc.CreateEntry(ctx, platformTC, &routing.RoutePlanEntry{
		PlanID:                  plan1.ID,
		ConnectionID:            connIDs[1],
		ScopeType:               "global",
		Priority:                2,
		Weight:                  100,
		AllowedSenderIDTypes:    []string{"alphanumeric"},
		DestinationNumberFormat: "e164",
		Status:                  "active",
	})
	if err != nil {
		return fmt.Errorf("add entry 2 to plan 1: %w", err)
	}

	// Connection 3: Priority 3
	_, _, err = routingSvc.CreateEntry(ctx, platformTC, &routing.RoutePlanEntry{
		PlanID:                  plan1.ID,
		ConnectionID:            connIDs[2],
		ScopeType:               "global",
		Priority:                3,
		Weight:                  100,
		DestinationNumberFormat: "e164",
		Status:                  "active",
	})
	if err != nil {
		return fmt.Errorf("add entry 3 to plan 1: %w", err)
	}

	// Activate plan 1
	plan1Active, _, err := routingSvc.RequestPlanActivation(ctx, platformTC, plan1.ID)
	if err != nil {
		return fmt.Errorf("activate plan 1: %w", err)
	}
	fmt.Printf("Activated route plan 1: %s (status=%s)\n", plan1Active.Name, plan1Active.Status)

	// 2. Create Route Plan: "DEV SAMPLE - Least Cost Global"
	minMargin := decimal.NewFromFloat(0.0020)
	plan2, err := routingSvc.CreatePlan(ctx, platformTC, &routing.RoutePlan{
		Name:        "DEV SAMPLE - Least Cost Global",
		Code:        "DEV-LEAST-COST",
		Description: "Least-cost routing plan with minimum margin override 0.0020 (DEV SAMPLE)",
		Strategy:    routing.StrategyLeastCost,
		Notes:       "DEV SAMPLE - for local testing",
	})
	if err != nil {
		return fmt.Errorf("create plan 2: %w", err)
	}
	fmt.Printf("Created route plan 2: %s (%s)\n", plan2.Name, plan2.ID)

	// Add 3 entries to plan 2 with min_margin_override
	for i := 0; i < 3; i++ {
		_, _, err = routingSvc.CreateEntry(ctx, platformTC, &routing.RoutePlanEntry{
			PlanID:                  plan2.ID,
			ConnectionID:            connIDs[i],
			ScopeType:               "global",
			Priority:                1,
			Weight:                  100,
			MinMarginOverride:       &minMargin,
			DestinationNumberFormat: "e164",
			Status:                  "active",
		})
		if err != nil {
			return fmt.Errorf("add entry %d to plan 2: %w", i+1, err)
		}
	}

	// Activate plan 2
	plan2Active, _, err := routingSvc.RequestPlanActivation(ctx, platformTC, plan2.ID)
	if err != nil {
		return fmt.Errorf("activate plan 2: %w", err)
	}
	fmt.Printf("Activated route plan 2: %s (status=%s)\n", plan2Active.Name, plan2Active.Status)

	// 3. Assign plan 1 to BlueStar SMS Global (Dev) as default (country_id = null, priority = 1)
	asgn1, _, err := routingSvc.CreateAssignment(ctx, platformTC, &routing.CustomerRouteAssignment{
		CustomerOrgID: blueStarOrgID,
		PlanID:        plan1.ID,
		CountryID:     nil,
		Priority:      1,
		Status:        "active",
	})
	if err != nil {
		return fmt.Errorf("create default assignment: %w", err)
	}
	fmt.Printf("Assigned default plan %s to customer %s (assignment %s)\n", plan1.Name, blueStarOrgID, asgn1.ID)

	// 4. Assign plan 2 to BlueStar SMS Global (Dev) for sample country (priority = 1)
	asgn2, _, err := routingSvc.CreateAssignment(ctx, platformTC, &routing.CustomerRouteAssignment{
		CustomerOrgID: blueStarOrgID,
		PlanID:        plan2.ID,
		CountryID:     &sampleCountryID,
		Priority:      1,
		Status:        "active",
	})
	if err != nil {
		return fmt.Errorf("create country assignment: %w", err)
	}
	fmt.Printf("Assigned country plan %s to customer %s for country %s (assignment %s)\n", plan2.Name, blueStarOrgID, sampleCountryID, asgn2.ID)

	// 5. Upsert 1 routing rule: "DEV SAMPLE - Prefer FastRoute for OTP"
	rule, _, err := routingSvc.CreateRule(ctx, platformTC, &routing.RoutingRule{
		Name:               "DEV SAMPLE - Prefer FastRoute for OTP",
		Priority:           1,
		Status:             "active",
		Conditions:         map[string]interface{}{"traffic_profile": "otp"},
		ActionType:         "prefer",
		ActionConnectionID: &connIDs[0],
	})
	if err != nil {
		return fmt.Errorf("create routing rule: %w", err)
	}
	fmt.Printf("Created routing rule: %s (%s)\n", rule.Name, rule.ID)

	// Reload snapshot
	routing.TryReload(ctx, pool)
	fmt.Println("Routing snapshot reloaded successfully.")

	return nil
}
