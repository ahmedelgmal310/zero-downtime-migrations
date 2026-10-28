// Package check verifies data consistency between the old and new
// representation of a user's name after every step of the rollout.
//
// It works with whatever schema phase the database is in:
//
//	baseline:   name
//	expanded:   name, first_name, last_name (NULLs allowed until backfilled)
//	contracted: first_name, last_name
//
// and checks:
//   - no row has name and the split columns disagreeing;
//   - after the backfill, no row has NULL split columns;
//   - the logical full name of every seeded row (ids 1..SeedRows) hashes to
//     the same checksum as right after seeding, so no migration, backfill or
//     rollback changed existing data;
//   - the row count never goes down.
package check

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/store"
)

// Report is the result of one check.
type Report struct {
	Step          string   `json:"step"`
	Phase         string   `json:"phase"`
	Rows          int64    `json:"rows"`
	SeedRows      int64    `json:"seed_rows"`
	SeedChecksum  string   `json:"seed_checksum"`
	Inconsistent  int64    `json:"inconsistent"`
	NotBackfilled int64    `json:"not_backfilled"`
	Problems      []string `json:"problems,omitempty"`
}

// OK reports whether no problem was found.
func (r Report) OK() bool { return len(r.Problems) == 0 }

// Expect holds what the caller knows should be true at this step.
type Expect struct {
	SeedRows        int64
	SeedChecksum    string // empty: record the checksum, do not compare
	MinRows         int64  // rows must not go below this (previous count)
	ExactRows       int64  // >0: the row count must match exactly
	RequireBackfill bool   // split columns must be fully populated
	RequirePhase    string // optional: "baseline", "expanded" or "contracted"
}

// Phases.
const (
	PhaseBaseline   = "baseline"
	PhaseExpanded   = "expanded"
	PhaseContracted = "contracted"
)

// SQL for the logical full name, mirroring names.Join.
const joinSQL = `CASE WHEN coalesce(last_name, '') = '' THEN first_name ELSE first_name || ' ' || last_name END`

// Run executes the checks.
func Run(ctx context.Context, pool *pgxpool.Pool, step string, e Expect) (Report, error) {
	r := Report{Step: step, SeedRows: e.SeedRows}
	cols, err := columns(ctx, pool)
	if err != nil {
		return r, err
	}
	hasName := slices.Contains(cols, "name")
	hasSplit := slices.Contains(cols, "first_name") && slices.Contains(cols, "last_name")
	switch {
	case hasName && !hasSplit:
		r.Phase = PhaseBaseline
	case hasName && hasSplit:
		r.Phase = PhaseExpanded
	case hasSplit:
		r.Phase = PhaseContracted
	default:
		return r, errors.New("users table has neither name nor first_name/last_name")
	}

	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&r.Rows); err != nil {
		return r, fmt.Errorf("count rows: %w", err)
	}

	logical := "name"
	if r.Phase == PhaseContracted {
		logical = joinSQL
	}
	if r.Phase == PhaseExpanded {
		// A row whose split columns are populated must agree with name.
		q := `SELECT count(*) FILTER (WHERE first_name IS NOT NULL AND name IS DISTINCT FROM ` + joinSQL + `),
		             count(*) FILTER (WHERE first_name IS NULL OR last_name IS NULL)
		      FROM users`
		if err := pool.QueryRow(ctx, q).Scan(&r.Inconsistent, &r.NotBackfilled); err != nil {
			return r, fmt.Errorf("compare representations: %w", err)
		}
	}
	if r.Phase == PhaseContracted {
		q := `SELECT count(*) FILTER (WHERE first_name IS NULL OR last_name IS NULL) FROM users`
		if err := pool.QueryRow(ctx, q).Scan(&r.NotBackfilled); err != nil {
			return r, fmt.Errorf("count NULL split columns: %w", err)
		}
	}

	if e.SeedRows > 0 {
		q := `SELECT count(*), coalesce(md5(string_agg(id::text || ':' || ` + logical + `, ',' ORDER BY id)), '')
		      FROM users WHERE id <= $1`
		var n int64
		if err := pool.QueryRow(ctx, q, e.SeedRows).Scan(&n, &r.SeedChecksum); err != nil {
			return r, fmt.Errorf("checksum seed rows: %w", err)
		}
		if n != e.SeedRows {
			r.problem("seed rows: found %d, want %d", n, e.SeedRows)
		}
	}

	if e.SeedChecksum != "" && r.SeedChecksum != e.SeedChecksum {
		r.problem("seed checksum changed: %s, want %s", r.SeedChecksum, e.SeedChecksum)
	}
	if r.Inconsistent > 0 {
		r.problem("%d rows where name and first_name/last_name disagree", r.Inconsistent)
	}
	if (e.RequireBackfill || r.Phase == PhaseContracted) && r.NotBackfilled > 0 {
		r.problem("%d rows with NULL first_name/last_name", r.NotBackfilled)
	}
	if r.Rows < e.MinRows {
		r.problem("row count went down: %d < %d", r.Rows, e.MinRows)
	}
	if e.ExactRows > 0 && r.Rows != e.ExactRows {
		r.problem("row count %d, want exactly %d (seed + acknowledged writes)", r.Rows, e.ExactRows)
	}
	if e.RequirePhase != "" && r.Phase != e.RequirePhase {
		r.problem("schema phase %s, want %s", r.Phase, e.RequirePhase)
	}
	return r, nil
}

func (r *Report) problem(format string, args ...any) {
	r.Problems = append(r.Problems, fmt.Sprintf(format, args...))
}

func columns(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	all := []string{"id", "name", "first_name", "last_name"}
	missing, err := store.MissingColumns(ctx, pool, all)
	if err != nil {
		return nil, err
	}
	var have []string
	for _, c := range all {
		if !slices.Contains(missing, c) {
			have = append(have, c)
		}
	}
	return have, nil
}

// String renders a one-line summary.
func (r Report) String() string {
	status := "OK"
	if !r.OK() {
		status = "FAIL: " + strings.Join(r.Problems, "; ")
	}
	return fmt.Sprintf("%s phase=%s rows=%d inconsistent=%d not_backfilled=%d seed_checksum=%s %s",
		r.Step, r.Phase, r.Rows, r.Inconsistent, r.NotBackfilled, r.SeedChecksum, status)
}
