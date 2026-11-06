package system

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotMigrated means the migration tables do not exist yet.
var ErrNotMigrated = errors.New("database migrations have not been applied")

// SchemaEntry is one row of the schema_info table.
type SchemaEntry struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	CreatedAt time.Time `json:"created_at"`
}

// MigrationState is the state recorded by golang-migrate in schema_migrations.
type MigrationState struct {
	Version int64 `json:"version"`
	Dirty   bool  `json:"dirty"`
}

// Repository reads system metadata from PostgreSQL.
type Repository interface {
	MigrationState(ctx context.Context) (MigrationState, error)
	SchemaInfo(ctx context.Context) ([]SchemaEntry, error)
}

// Querier is the subset of *pgxpool.Pool the repository needs.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type pgRepository struct{ db Querier }

// NewRepository returns the PostgreSQL-backed repository. All queries are static
// (no user input is ever interpolated into SQL).
func NewRepository(db Querier) Repository { return &pgRepository{db: db} }

const undefinedTable = "42P01"

func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == undefinedTable
}

func (r *pgRepository) MigrationState(ctx context.Context) (MigrationState, error) {
	var st MigrationState
	err := r.db.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations LIMIT 1`).Scan(&st.Version, &st.Dirty)
	switch {
	case isUndefinedTable(err), errors.Is(err, pgx.ErrNoRows):
		return MigrationState{}, ErrNotMigrated
	case err != nil:
		return MigrationState{}, err
	}
	return st, nil
}

func (r *pgRepository) SchemaInfo(ctx context.Context) ([]SchemaEntry, error) {
	rows, err := r.db.Query(ctx, `SELECT key, value, created_at FROM schema_info ORDER BY key`)
	if err != nil {
		if isUndefinedTable(err) {
			return nil, ErrNotMigrated
		}
		return nil, err
	}
	defer rows.Close()

	entries := []SchemaEntry{}
	for rows.Next() {
		var e SchemaEntry
		if err := rows.Scan(&e.Key, &e.Value, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		if isUndefinedTable(err) {
			return nil, ErrNotMigrated
		}
		return nil, err
	}
	return entries, nil
}
