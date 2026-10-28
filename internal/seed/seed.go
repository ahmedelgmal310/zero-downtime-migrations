// Package seed bulk-loads deterministic users with COPY, so the table is big
// enough for the backfill and the index build to take measurable time.
package seed

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/names"
)

// Run inserts rows 1..n into an empty users table. Seeded ids must be 1..n so
// the load test can compute the expected name of any seeded id.
func Run(ctx context.Context, pool *pgxpool.Pool, n int64) error {
	if n < 1 {
		return errors.New("seed: row count must be positive")
	}
	var existing int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&existing); err != nil {
		return fmt.Errorf("seed: count rows: %w", err)
	}
	if existing != 0 {
		return fmt.Errorf("seed: users already has %d rows", existing)
	}
	var i int64
	src := pgx.CopyFromFunc(func() ([]any, error) {
		if i >= n {
			return nil, nil
		}
		i++
		return []any{names.SeedName(i)}, nil
	})
	copied, err := pool.CopyFrom(ctx, pgx.Identifier{"users"}, []string{"name"}, src)
	if err != nil {
		return fmt.Errorf("seed: copy: %w", err)
	}
	if copied != n {
		return fmt.Errorf("seed: copied %d rows, want %d", copied, n)
	}
	var minID, maxID int64
	if err := pool.QueryRow(ctx, `SELECT min(id), max(id) FROM users`).Scan(&minID, &maxID); err != nil {
		return fmt.Errorf("seed: read id range: %w", err)
	}
	if minID != 1 || maxID != n {
		return fmt.Errorf("seed: ids %d..%d, want 1..%d (table must be freshly created)", minID, maxID, n)
	}
	if _, err := pool.Exec(ctx, `ANALYZE users`); err != nil {
		return fmt.Errorf("seed: analyze: %w", err)
	}
	return nil
}
