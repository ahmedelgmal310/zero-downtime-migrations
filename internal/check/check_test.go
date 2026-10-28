package check_test

import (
	"strings"
	"testing"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/backfill"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/check"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/pgtest"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/seed"
)

func TestPhasesAndChecksum(t *testing.T) {
	pool, dsn := pgtest.Pool(t)
	ctx := pgtest.Ctx(t)

	if _, err := check.Run(ctx, pool, "no table", check.Expect{}); err == nil {
		t.Error("want error without a users table")
	}

	pgtest.Migrate(t, dsn, 1)
	if err := seed.Run(ctx, pool, 500); err != nil {
		t.Fatal(err)
	}
	base, err := check.Run(ctx, pool, "baseline", check.Expect{SeedRows: 500, RequirePhase: check.PhaseBaseline})
	if err != nil || !base.OK() || base.Phase != check.PhaseBaseline || base.Rows != 500 || base.SeedChecksum == "" {
		t.Fatalf("baseline = %+v, %v", base, err)
	}

	pgtest.Migrate(t, dsn, 3)
	exp, err := check.Run(ctx, pool, "expanded", check.Expect{SeedRows: 500, SeedChecksum: base.SeedChecksum, MinRows: 500})
	if err != nil || !exp.OK() || exp.Phase != check.PhaseExpanded || exp.NotBackfilled != 500 {
		t.Fatalf("expanded = %+v, %v", exp, err)
	}
	if r, _ := check.Run(ctx, pool, "too early", check.Expect{RequireBackfill: true}); r.OK() {
		t.Error("RequireBackfill must fail before the backfill")
	}

	if _, err := backfill.Run(ctx, pool, backfill.Options{BatchSize: 1000}, pgtest.Discard()); err != nil {
		t.Fatal(err)
	}
	pgtest.Migrate(t, dsn, 6)
	con, err := check.Run(ctx, pool, "contracted", check.Expect{
		SeedRows: 500, SeedChecksum: base.SeedChecksum, ExactRows: 500, RequirePhase: check.PhaseContracted,
	})
	if err != nil || !con.OK() || con.Phase != check.PhaseContracted {
		t.Fatalf("contracted = %+v, %v", con, err)
	}
	// The checksum is computed on the logical full name, so it survives the
	// change of representation unchanged.
	if con.SeedChecksum != base.SeedChecksum {
		t.Errorf("checksum changed across the contract: %s vs %s", con.SeedChecksum, base.SeedChecksum)
	}
	if !strings.Contains(con.String(), "OK") {
		t.Errorf("String() = %s", con.String())
	}
}

func TestDetectsProblems(t *testing.T) {
	pool, dsn := pgtest.Pool(t)
	ctx := pgtest.Ctx(t)
	pgtest.Migrate(t, dsn, 1)
	if err := seed.Run(ctx, pool, 100); err != nil {
		t.Fatal(err)
	}
	base, err := check.Run(ctx, pool, "baseline", check.Expect{SeedRows: 100})
	if err != nil {
		t.Fatal(err)
	}
	pgtest.Migrate(t, dsn, 3)

	// Corrupt one row behind the trigger's back (what a buggy writer could do).
	if _, err := pool.Exec(ctx, `SET session_replication_role = replica;
		UPDATE users SET first_name = 'Wrong', last_name = 'Person' WHERE id = 7;
		UPDATE users SET name = 'Changed Name' WHERE id = 8;
		SET session_replication_role = origin`); err != nil {
		t.Fatal(err)
	}
	r, err := check.Run(ctx, pool, "corrupted", check.Expect{
		SeedRows: 100, SeedChecksum: base.SeedChecksum, MinRows: 1000, ExactRows: 99, RequirePhase: check.PhaseContracted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.OK() || r.Inconsistent != 1 {
		t.Fatalf("report = %+v", r)
	}
	want := []string{"disagree", "seed checksum changed", "went down", "want exactly", "schema phase"}
	all := strings.Join(r.Problems, "\n")
	for _, w := range want {
		if !strings.Contains(all, w) {
			t.Errorf("problems lack %q:\n%s", w, all)
		}
	}
	if !strings.Contains(r.String(), "FAIL") {
		t.Errorf("String() = %s", r.String())
	}

	// Missing seed rows are reported too.
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = 3`); err != nil {
		t.Fatal(err)
	}
	r, _ = check.Run(ctx, pool, "deleted", check.Expect{SeedRows: 100})
	if r.OK() || !strings.Contains(strings.Join(r.Problems, " "), "seed rows") {
		t.Errorf("deleted seed row not reported: %v", r.Problems)
	}
}
