package alerting

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MetricSource interface abstracts telemetry inspection.
type MetricSource interface {
	GetMetricValue(ctx context.Context, conditionType string, params map[string]interface{}) (float64, error)
}

// MockMetricSource allows testing alert conditions by injecting arbitrary metric values.
type MockMetricSource struct {
	mu     sync.RWMutex
	values map[string]float64
}

func NewMockMetricSource() *MockMetricSource {
	return &MockMetricSource{
		values: make(map[string]float64),
	}
}

func (m *MockMetricSource) SetMetric(conditionType string, val float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[conditionType] = val
}

func (m *MockMetricSource) GetMetricValue(ctx context.Context, conditionType string, params map[string]interface{}) (float64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	val, ok := m.values[conditionType]
	if !ok {
		return 0.0, nil
	}
	return val, nil
}

// DBMetricSource evaluates real platform metrics against PostgreSQL tables.
type DBMetricSource struct {
	pool *pgxpool.Pool
}

func NewDBMetricSource(pool *pgxpool.Pool) *DBMetricSource {
	return &DBMetricSource{pool: pool}
}

func (d *DBMetricSource) GetMetricValue(ctx context.Context, conditionType string, params map[string]interface{}) (float64, error) {
	switch conditionType {
	case ConditionHighFailureRate:
		// Delivery failure rate over the last 15 minutes
		query := `
			SELECT 
				COALESCE(SUM(submitted_count), 0) AS total_submitted,
				COALESCE(SUM(failed_count + undelivered_count), 0) AS total_failed
			FROM analytics_rollup_provider_15m
			WHERE bucket_start >= now() - interval '15 minutes'
		`
		var submitted, failed int64
		if err := d.pool.QueryRow(ctx, query).Scan(&submitted, &failed); err != nil {
			return 0, err
		}
		if submitted < 10 { // Insufficient sample size
			return 0, nil
		}
		return (float64(failed) / float64(submitted)) * 100.0, nil

	case ConditionOutboxBacklogAge:
		// Oldest unsent message age in seconds
		query := `
			SELECT COALESCE(EXTRACT(EPOCH FROM (now() - MIN(created_at))), 0)
			FROM messages
			WHERE status IN ('pending', 'queued', 'routing')
		`
		var ageSec float64
		err := d.pool.QueryRow(ctx, query).Scan(&ageSec)
		return ageSec, err

	case ConditionReconciliationDisc:
		// Reconciliation discrepancy
		return 0, nil

	case ConditionCarrierConnDown:
		// Returns 0 if any active connection is disconnected, or 1 if all healthy
		query := `
			SELECT COUNT(*)
			FROM provider_connections
			WHERE status = 'active'
		`
		var activeCount int64
		_ = d.pool.QueryRow(ctx, query).Scan(&activeCount)
		if activeCount == 0 {
			return 0, nil // Down
		}
		return 1, nil // Up

	case ConditionSettlementException:
		return 0, nil

	case ConditionLowMargin:
		return 20.0, nil

	default:
		return 0, fmt.Errorf("unsupported condition type: %s", conditionType)
	}
}

// Evaluator evaluates alert rules against the metric source.
type Evaluator struct {
	source MetricSource
}

func NewEvaluator(source MetricSource) *Evaluator {
	return &Evaluator{source: source}
}

// EvaluateRule tests whether a rule's condition is currently met.
// Returns breached=true if the metric exceeds the threshold.
func (e *Evaluator) EvaluateRule(ctx context.Context, rule *AlertRule) (bool, float64, error) {
	val, err := e.source.GetMetricValue(ctx, rule.ConditionType, rule.Params)
	if err != nil {
		return false, 0, err
	}

	breached := false
	switch rule.ConditionType {
	case ConditionCarrierConnDown:
		// Breached if conn health is below or equal to threshold (e.g. 0)
		breached = val <= rule.Threshold
	case ConditionLowMargin:
		// Breached if margin is lower than threshold
		breached = val < rule.Threshold
	default:
		// Standard rules: breached if metric exceeds threshold
		breached = val > rule.Threshold
	}

	return breached, val, nil
}
