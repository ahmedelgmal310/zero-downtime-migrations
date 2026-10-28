package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/db"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/names"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/pgtest"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/store"
)

func mustStore(t *testing.T, version string, pool *pgxpool.Pool) store.Store {
	t.Helper()
	s, err := store.New(version, pool)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func row(t *testing.T, pool *pgxpool.Pool, id int64) (name, first, last *string) {
	t.Helper()
	err := pool.QueryRow(context.Background(), `SELECT name, first_name, last_name FROM users WHERE id = $1`, id).
		Scan(&name, &first, &last)
	if err != nil {
		t.Fatal(err)
	}
	return name, first, last
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestUnknownVersion(t *testing.T) {
	if _, err := store.New("v9", nil); err == nil {
		t.Error("want error for unknown version")
	}
}

// Every release, in every schema phase it must support, round-trips names.
func TestReleasesAcrossPhases(t *testing.T) {
	pool, dsn := pgtest.Pool(t)
	ctx := pgtest.Ctx(t)

	pgtest.Migrate(t, dsn, 1)
	v1 := mustStore(t, "v1", pool)
	u1, err := v1.Create(ctx, "Grace Hopper")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := v1.Get(ctx, u1.ID); err != nil || got.Name != "Grace Hopper" || got.FirstName != nil {
		t.Fatalf("v1 Get = %+v, %v", got, err)
	}
	if _, err := v1.Get(ctx, 999999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("v1 Get(missing) err = %v", err)
	}
	if _, err := v1.SearchByLastName(ctx, "x", 1); !errors.Is(err, store.ErrUnsupported) {
		t.Errorf("v1 search err = %v", err)
	}

	// v2 is not compatible with the baseline schema.
	v2 := mustStore(t, "v2", pool)
	if missing, err := store.MissingColumns(ctx, pool, v2.RequiredColumns()); err != nil || len(missing) != 2 {
		t.Fatalf("missing columns before expand = %v, %v", missing, err)
	}

	pgtest.Migrate(t, dsn, 3)
	// Rows from before the expand are not backfilled yet: v2 still answers
	// correctly because it reads the old column.
	if got, err := v2.Get(ctx, u1.ID); err != nil || got.Name != "Grace Hopper" || deref(got.LastName) != "Hopper" {
		t.Fatalf("v2 Get(pre-expand row) = %+v, %v", got, err)
	}
	u2, err := v2.Create(ctx, "Mary Ann Smith")
	if err != nil {
		t.Fatal(err)
	}
	if n, f, l := row(t, pool, u2.ID); deref(n) != "Mary Ann Smith" || deref(f) != "Mary" || deref(l) != "Ann Smith" {
		t.Errorf("v2 dual write stored (%s, %s, %s)", deref(n), deref(f), deref(l))
	}
	if _, err := v2.SearchByLastName(ctx, "x", 1); !errors.Is(err, store.ErrUnsupported) {
		t.Errorf("v2 search err = %v", err)
	}
	if _, err := v2.Get(ctx, 999999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("v2 Get(missing) err = %v", err)
	}

	// Old code after a rollback: the trigger derives the split columns.
	u3, err := v1.Create(ctx, "Cher")
	if err != nil {
		t.Fatal(err)
	}
	if _, f, l := row(t, pool, u3.ID); deref(f) != "Cher" || deref(l) != "" {
		t.Errorf("trigger split of a v1 write = (%s, %s)", deref(f), deref(l))
	}

	// v3 writes only the new columns; the trigger keeps "name" for v1/v2.
	v3 := mustStore(t, "v3", pool)
	u4, err := v3.Create(ctx, "Tim Berners Lee")
	if err != nil {
		t.Fatal(err)
	}
	if n, _, _ := row(t, pool, u4.ID); deref(n) != "Tim Berners Lee" {
		t.Errorf("trigger join of a v3 write = %s", deref(n))
	}
	if got, err := v1.Get(ctx, u4.ID); err != nil || got.Name != "Tim Berners Lee" {
		t.Errorf("v1 reading a v3 row = %+v, %v", got, err)
	}

	// Updates from either side stay in sync.
	if _, err := pool.Exec(ctx, `UPDATE users SET name = 'Ada King Lovelace' WHERE id = $1`, u1.ID); err != nil {
		t.Fatal(err)
	}
	if _, f, l := row(t, pool, u1.ID); deref(f) != "Ada" || deref(l) != "King Lovelace" {
		t.Errorf("old-code update re-derived (%s, %s)", deref(f), deref(l))
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET last_name = 'Byron' WHERE id = $1`, u1.ID); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := row(t, pool, u1.ID); deref(n) != "Ada Byron" {
		t.Errorf("new-code update re-derived name = %s", deref(n))
	}

	pgtest.Migrate(t, dsn, 6)
	if got, err := v3.Get(ctx, u2.ID); err != nil || got.Name != "Mary Ann Smith" {
		t.Fatalf("v3 after contract = %+v, %v", got, err)
	}
	if _, err := v3.Create(ctx, "Linus Torvalds"); err != nil {
		t.Fatal(err)
	}
	found, err := v3.SearchByLastName(ctx, "Torvalds", 10)
	if err != nil || len(found) != 1 || found[0].Name != "Linus Torvalds" {
		t.Fatalf("v3 search = %+v, %v", found, err)
	}
	if _, err := v3.Get(ctx, 999999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("v3 Get(missing) err = %v", err)
	}
	// This is what the contract guard prevents: old code on the new schema.
	if _, err := v1.Get(ctx, u2.ID); db.SQLState(err) != db.CodeUndefinedColumn {
		t.Errorf("v1 after contract: err = %v, want undefined column", err)
	}
	if missing, _ := store.MissingColumns(ctx, pool, v1.RequiredColumns()); len(missing) != 1 || missing[0] != "name" {
		t.Errorf("v1 missing columns after contract = %v", missing)
	}
}

// The SQL split in the trigger and the backfill must equal names.Split.
func TestSQLSplitMatchesGo(t *testing.T) {
	pool, dsn := pgtest.Pool(t)
	ctx := pgtest.Ctx(t)
	pgtest.Migrate(t, dsn, 3)
	v1 := mustStore(t, "v1", pool)
	for _, n := range []string{"Ada Lovelace", "Mary Ann Smith", "Cher", "A  B", "Ünal Çelik", "x y z w"} {
		u, err := v1.Create(ctx, n)
		if err != nil {
			t.Fatal(err)
		}
		wantF, wantL := names.Split(n)
		if _, f, l := row(t, pool, u.ID); deref(f) != wantF || deref(l) != wantL {
			t.Errorf("SQL split(%q) = (%q, %q), Go = (%q, %q)", n, deref(f), deref(l), wantF, wantL)
		}
	}
}
