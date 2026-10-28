package seed_test

import (
	"testing"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/names"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/pgtest"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/seed"
)

func TestSeed(t *testing.T) {
	pool, dsn := pgtest.Pool(t)
	ctx := pgtest.Ctx(t)
	if err := seed.Run(ctx, pool, 0); err == nil {
		t.Error("n=0: want error")
	}
	if err := seed.Run(ctx, pool, 10); err == nil {
		t.Error("no table: want error")
	}
	pgtest.Migrate(t, dsn, 1)
	if err := seed.Run(ctx, pool, 1000); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 50, 777, 1000} {
		var n string
		if err := pool.QueryRow(ctx, `SELECT name FROM users WHERE id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != names.SeedName(id) {
			t.Errorf("row %d = %q, want %q", id, n, names.SeedName(id))
		}
	}
	if err := seed.Run(ctx, pool, 10); err == nil {
		t.Error("seeding a non-empty table: want error")
	}
}
