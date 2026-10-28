package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/check"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/db"
)

// checkState carries what earlier checks established to the next one.
type checkState struct {
	SeedRows     int64  `json:"seed_rows"`
	SeedChecksum string `json:"seed_checksum"`
	Rows         int64  `json:"rows"`
}

func runCheck(ctx context.Context, args []string, env Env) error {
	fs := newFlags("check", env)
	dsn := dsnFlag(fs, env)
	step := fs.String("step", "", "name of the rollout step (required)")
	seedRows := fs.Int64("seed-rows", 0, "number of seeded rows (ids 1..n); taken from --state when 0")
	statePath := fs.String("state", "", "state file shared between checks (seed checksum, last row count)")
	out := fs.String("out", "", "append the JSON report to this file")
	exact := fs.Int64("exact-rows", 0, "require exactly this many rows")
	backfilled := fs.Bool("require-backfill", false, "require first_name/last_name on every row")
	phase := fs.String("require-phase", "", "require schema phase: baseline, expanded or contracted")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *step == "" {
		return errors.New("--step is required")
	}
	var st checkState
	if *statePath != "" {
		b, err := os.ReadFile(*statePath)
		switch {
		case err == nil:
			if err := json.Unmarshal(b, &st); err != nil {
				return fmt.Errorf("read state: %w", err)
			}
		case !errors.Is(err, os.ErrNotExist):
			return err
		}
	}
	if *seedRows > 0 {
		st.SeedRows = *seedRows
	}

	pool, err := db.Open(ctx, *dsn, db.Options{AppName: "zdm-check", MaxConns: 1})
	if err != nil {
		return err
	}
	defer pool.Close()
	r, err := check.Run(ctx, pool, *step, check.Expect{
		SeedRows: st.SeedRows, SeedChecksum: st.SeedChecksum, MinRows: st.Rows,
		ExactRows: *exact, RequireBackfill: *backfilled, RequirePhase: *phase,
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(env.Stdout, r.String())

	if *out != "" {
		if err := appendJSON(*out, r); err != nil {
			return err
		}
	}
	if *statePath != "" && r.OK() {
		if st.SeedChecksum == "" {
			st.SeedChecksum = r.SeedChecksum
		}
		st.Rows = r.Rows
		b, err := json.Marshal(st)
		if err != nil {
			return err
		}
		if err := os.WriteFile(*statePath, b, 0o600); err != nil {
			return err
		}
	}
	if !r.OK() {
		return fmt.Errorf("consistency check failed after %q: %v", *step, r.Problems)
	}
	return nil
}

func appendJSON(path string, v any) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(f).Encode(v); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
