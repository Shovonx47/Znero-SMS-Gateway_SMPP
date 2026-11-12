package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"github.com/znero-international/znero/backend/internal/ledger"
)

// ReconciliationService checks ledger-messaging invariants and reports exceptions.
type ReconciliationService struct {
	pool      *pgxpool.Pool
	repo      Repository
	ledgerSvc *ledger.Service
	logger    *slog.Logger
}

// NewReconciliationService creates a new ReconciliationService.
func NewReconciliationService(
	pool *pgxpool.Pool,
	repo Repository,
	ledgerSvc *ledger.Service,
	logger *slog.Logger,
) *ReconciliationService {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReconciliationService{
		pool:      pool,
		repo:      repo,
		ledgerSvc: ledgerSvc,
		logger:    logger,
	}
}

// RunReconciliation runs an on-demand audit and records the summary in messaging_reconciliation_runs.
func (s *ReconciliationService) RunReconciliation(ctx context.Context, initiatedBy *uuid.UUID) (*ReconciliationReport, error) {
	startedAt := time.Now().UTC()
	report := &ReconciliationReport{
		Discrepancies: make([]map[string]interface{}, 0),
		LedgerMatches: true,
	}

	// 1. Total counts by message status
	rows, err := s.pool.Query(ctx, `
		SELECT status, COUNT(*)
		FROM messages
		GROUP BY status
	`)
	if err != nil {
		return nil, fmt.Errorf("query message counts by status: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			continue
		}
		report.MessagesChecked += count
		switch status {
		case StatusDelivered, StatusUndelivered, StatusExpired, StatusFailed, StatusRejected:
			report.TerminalCount += count
		case StatusAccepted, StatusQueued, StatusSubmitting, StatusSubmitted:
			report.InFlightCount += count
		case StatusUnknown:
			report.UnresolvedUnknown += count
		}
	}

	// 2. Invariant Check: Terminal messages must NOT have active holds
	activeHoldRows, err := s.pool.Query(ctx, `
		SELECT m.id, m.customer_org_id, m.status, h.id, h.status, h.amount
		FROM messages m
		JOIN holds h ON h.id = m.hold_id
		WHERE m.status IN ('delivered', 'undelivered', 'failed', 'expired', 'rejected')
		  AND h.status = 'active'
		LIMIT 100
	`)
	if err == nil {
		defer activeHoldRows.Close()
		for activeHoldRows.Next() {
			var msgID, custID, holdID uuid.UUID
			var msgStatus, holdStatus string
			var amt decimal.Decimal
			if err := activeHoldRows.Scan(&msgID, &custID, &msgStatus, &holdID, &holdStatus, &amt); err == nil {
				report.Discrepancies = append(report.Discrepancies, map[string]interface{}{
					"type":       "terminal_message_active_hold",
					"severity":   "critical",
					"message_id": msgID.String(),
					"customer":   custID.String(),
					"msg_status": msgStatus,
					"hold_id":    holdID.String(),
					"amount":     amt.String(),
					"detail":     "Terminal message has an active ledger hold that was neither captured nor released",
				})
			}
		}
	}

	// 3. Invariant Check: Non-delivered terminal messages must NOT have captured holds
	// UNLESS permitted by snapshotted billing terms:
	//   - no_dlr_policy_snapshot = 'capture_at_ttl' on expired no-DLR messages, OR
	//   - billing_mode_snapshot = 'on_provider_accept' AND failed_after_accept_policy_snapshot = 'keep_charge' after submission
	capturedRows, err := s.pool.Query(ctx, `
		SELECT m.id, m.customer_org_id, m.status, h.id, h.status, h.amount
		FROM messages m
		JOIN holds h ON h.id = m.hold_id
		WHERE m.status IN ('undelivered', 'failed', 'expired', 'rejected')
		  AND h.status = 'captured'
		  AND NOT (
		      (m.status = 'expired' AND m.no_dlr_policy_snapshot = 'capture_at_ttl' AND COALESCE(m.failure_reason, '') LIKE 'no DLR received%')
		      OR (m.billing_mode_snapshot = 'on_provider_accept' AND m.failed_after_accept_policy_snapshot = 'keep_charge' AND m.submitted_at IS NOT NULL)
		  )
		LIMIT 100
	`)
	if err == nil {
		defer capturedRows.Close()
		for capturedRows.Next() {
			var msgID, custID, holdID uuid.UUID
			var msgStatus, holdStatus string
			var amt decimal.Decimal
			if err := capturedRows.Scan(&msgID, &custID, &msgStatus, &holdID, &holdStatus, &amt); err == nil {
				report.Discrepancies = append(report.Discrepancies, map[string]interface{}{
					"type":       "failed_message_captured_hold",
					"severity":   "critical",
					"message_id": msgID.String(),
					"customer":   custID.String(),
					"msg_status": msgStatus,
					"hold_id":    holdID.String(),
					"amount":     amt.String(),
					"detail":     "Non-delivered terminal message has a captured hold; customer billed for failed message",
				})
			}
		}
	}

	// 4. Invariant Check: Captured hold totals vs expected chargeable messages
	var expectedCapturedTotal, capturedTotal decimal.Decimal
	_ = s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(sell_price), 0)
		FROM messages
		WHERE hold_id IS NOT NULL
		  AND (
		      status = 'delivered'
		      OR (status IN ('submitted', 'unknown') AND billing_mode_snapshot = 'on_provider_accept' AND submitted_at IS NOT NULL)
		      OR (status = 'expired' AND no_dlr_policy_snapshot = 'capture_at_ttl' AND billing_mode_snapshot = 'on_delivery' AND COALESCE(failure_reason, '') LIKE 'no DLR received%')
		      OR (status IN ('undelivered', 'failed', 'expired') AND billing_mode_snapshot = 'on_provider_accept' AND failed_after_accept_policy_snapshot = 'keep_charge' AND submitted_at IS NOT NULL)
		  )
	`).Scan(&expectedCapturedTotal)

	_ = s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(h.amount), 0)
		FROM holds h
		JOIN messages m ON m.hold_id = h.id
		WHERE h.status = 'captured'
	`).Scan(&capturedTotal)

	diff := expectedCapturedTotal.Sub(capturedTotal).Abs()
	if diff.GreaterThan(decimal.NewFromFloat(0.01)) {
		report.LedgerMatches = false
		report.Discrepancies = append(report.Discrepancies, map[string]interface{}{
			"type":            "ledger_totals_mismatch",
			"severity":        "warning",
			"delivered_total": expectedCapturedTotal.String(),
			"captured_total":  capturedTotal.String(),
			"difference":      diff.String(),
			"detail":          "Total expected captured message value does not match total captured ledger amount",
		})
	}

	// 5. Unmatched DLRs count
	_ = s.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM unmatched_dlrs
		WHERE match_status = 'unmatched'
	`).Scan(&report.UnmatchedDLRsCount)

	report.DiscrepancyCount = len(report.Discrepancies)
	completedAt := time.Now().UTC()

	// Persist run
	summaryBytes, _ := json.Marshal(report)
	_ = s.repo.RecordReconciliationRun(ctx, ReconciliationRun{
		ID:               uuid.New(),
		StartedAt:        startedAt,
		CompletedAt:      &completedAt,
		MessagesChecked:  report.MessagesChecked,
		DiscrepancyCount: report.DiscrepancyCount,
		SummaryJSON:      summaryBytes,
		InitiatedBy:      initiatedBy,
	})

	return report, nil
}
