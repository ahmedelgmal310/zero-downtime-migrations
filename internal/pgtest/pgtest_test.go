package pgtest_test

import (
	"testing"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/pgtest"
)

func TestIsolatedSchema(t *testing.T) {
	pool, dsn := pgtest.Pool(t)
	pgtest.Migrate(t, dsn, 1)
	var schema string
	if err := pool.QueryRow(pgtest.Ctx(t), `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if len(schema) != 14 || schema[:2] != "t_" {
		t.Errorf("current_schema = %q, want a per-test schema", schema)
	}
	if a, b := pgtest.LockID(), pgtest.LockID(); a == b || a <= 0 {
		t.Errorf("LockID() = %d, %d", a, b)
	}
}
