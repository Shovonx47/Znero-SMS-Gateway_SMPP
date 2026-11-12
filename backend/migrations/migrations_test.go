package migrations_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/znero-international/znero/backend/internal/testkit"
)

type migFile struct {
	version int
	name    string
	sql     string
}

func loadMigrations(t *testing.T, direction string) []migFile {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	re := regexp.MustCompile(`^(\d+)_.*\.` + direction + `\.sql$`)
	var out []migFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := re.FindStringSubmatch(e.Name())
		if len(m) != 2 {
			continue
		}
		ver, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("parse version from %s: %v", e.Name(), err)
		}
		raw, err := os.ReadFile(filepath.Join(".", e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out = append(out, migFile{
			version: ver,
			name:    e.Name(),
			sql:     string(raw),
		})
	}
	if direction == "up" {
		sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	} else {
		sort.Slice(out, func(i, j int) bool { return out[i].version > out[j].version })
	}
	if len(out) == 0 {
		t.Fatalf("no %s migrations found", direction)
	}
	return out
}

// TestMigrationsUpDownReapply applies migrations 1..latest into an isolated scratch
// schema inside znero_test, rolls all of them back to 1 (and down past 1), and
// reapplies 1..latest cleanly.
func TestMigrationsUpDownReapply(t *testing.T) {
	dbURL := testkit.RequireIntegrationDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	scratchSchema := "mig_test_" + testkit.RandHex(4)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s", scratchSchema)); err != nil {
		t.Fatalf("create scratch schema: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer ccancel()
		_, _ = pool.Exec(cctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", scratchSchema))
	})

	// Set search_path to scratchSchema first, falling back to public for extensions (btree_gist).
	if _, err := conn.Exec(ctx, fmt.Sprintf("SET search_path TO %s, public", scratchSchema)); err != nil {
		t.Fatalf("set search_path: %v", err)
	}

	ups := loadMigrations(t, "up")
	downs := loadMigrations(t, "down")
	if len(ups) != len(downs) {
		t.Fatalf("mismatch between up (%d) and down (%d) migration files", len(ups), len(downs))
	}

	// Pass 1: Apply 1..latest
	for _, m := range ups {
		if strings.TrimSpace(m.sql) == "" {
			continue
		}
		if _, err := conn.Exec(ctx, m.sql); err != nil {
			t.Fatalf("UP pass 1 failed at %s: %v", m.name, err)
		}
	}

	// Verify key tables exist in scratchSchema
	var tableCount int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_type = 'BASE TABLE'`, scratchSchema).Scan(&tableCount); err != nil {
		t.Fatalf("count tables after UP pass 1: %v", err)
	}
	if tableCount < 20 {
		t.Fatalf("expected at least 20 base tables after UP pass 1, got %d", tableCount)
	}

	// Pass 2: Roll back latest..2 (to 1), verify schema_info still exists, then roll back 1
	for _, m := range downs {
		if m.version == 1 {
			var infoCount int
			if err := conn.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s.schema_info", scratchSchema)).Scan(&infoCount); err != nil {
				t.Fatalf("expected schema_info at version 1: %v", err)
			}
		}
		if strings.TrimSpace(m.sql) == "" {
			continue
		}
		if _, err := conn.Exec(ctx, m.sql); err != nil {
			t.Fatalf("DOWN rollback failed at %s: %v", m.name, err)
		}
	}

	var leftoverTables int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_type = 'BASE TABLE'`, scratchSchema).Scan(&leftoverTables); err != nil {
		t.Fatalf("count leftover tables: %v", err)
	}
	if leftoverTables != 0 {
		// Log which tables were left behind
		rows, _ := conn.Query(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema = $1`, scratchSchema)
		var names []string
		for rows.Next() {
			var n string
			_ = rows.Scan(&n)
			names = append(names, n)
		}
		rows.Close()
		t.Fatalf("expected 0 tables after full DOWN rollback, still have %d: %v", leftoverTables, names)
	}

	// Pass 3: Reapply 1..latest
	for _, m := range ups {
		if strings.TrimSpace(m.sql) == "" {
			continue
		}
		if _, err := conn.Exec(ctx, m.sql); err != nil {
			t.Fatalf("UP reapply failed at %s: %v", m.name, err)
		}
	}

	var reapplyCount int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_type = 'BASE TABLE'`, scratchSchema).Scan(&reapplyCount); err != nil {
		t.Fatalf("count tables after reapply: %v", err)
	}
	if reapplyCount != tableCount {
		t.Fatalf("reapply table count %d != initial up count %d", reapplyCount, tableCount)
	}
}
