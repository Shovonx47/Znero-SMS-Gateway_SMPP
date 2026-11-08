package messaging

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// EnsurePartitions creates upcoming monthly partitions for messages and message_parts tables,
// along with safety default partitions so inserts never fail.
func EnsurePartitions(ctx context.Context, pool *pgxpool.Pool, monthsAhead int) error {
	if monthsAhead < 1 {
		monthsAhead = 3
	}

	// 1. Ensure default partitions exist
	defaultMsgSQL := `CREATE TABLE IF NOT EXISTS messages_default PARTITION OF messages DEFAULT;`
	if _, err := pool.Exec(ctx, defaultMsgSQL); err != nil {
		return fmt.Errorf("create messages_default partition: %w", err)
	}

	defaultPartsSQL := `CREATE TABLE IF NOT EXISTS message_parts_default PARTITION OF message_parts DEFAULT;`
	if _, err := pool.Exec(ctx, defaultPartsSQL); err != nil {
		return fmt.Errorf("create message_parts_default partition: %w", err)
	}

	// 2. Pre-create current and upcoming monthly partitions
	now := time.Now().UTC()
	startYear := now.Year()
	startMonth := now.Month()

	for i := 0; i < monthsAhead; i++ {
		monthDate := time.Date(startYear, startMonth+time.Month(i), 1, 0, 0, 0, 0, time.UTC)
		nextMonthDate := monthDate.AddDate(0, 1, 0)

		partitionSuffix := monthDate.Format("2006_01")
		fromDateStr := monthDate.Format("2006-01-02 15:04:05+00")
		toDateStr := nextMonthDate.Format("2006-01-02 15:04:05+00")

		msgSQL := fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS messages_%s PARTITION OF messages
			FOR VALUES FROM ('%s') TO ('%s');
		`, partitionSuffix, fromDateStr, toDateStr)
		if _, err := pool.Exec(ctx, msgSQL); err != nil {
			return fmt.Errorf("create partition messages_%s: %w", partitionSuffix, err)
		}

		partsSQL := fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS message_parts_%s PARTITION OF message_parts
			FOR VALUES FROM ('%s') TO ('%s');
		`, partitionSuffix, fromDateStr, toDateStr)
		if _, err := pool.Exec(ctx, partsSQL); err != nil {
			return fmt.Errorf("create partition message_parts_%s: %w", partitionSuffix, err)
		}
	}

	return nil
}

