// Package pgtest gives each integration test its own PostgreSQL schema.
//
// Tests run against $ZDM_TEST_DSN. Without it they are skipped, unless
// $ZDM_REQUIRE_DB is set (CI), in which case they fail: coverage numbers must
// never silently come from a run that skipped the database tests.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/migrate"
	"github.com/ahmedelgmal310/zero-downtime-migrations/migrations"
)

// DSN returns a DSN whose search_path is a fresh schema dropped at cleanup.
func DSN(t testing.TB) string {
	t.Helper()
	base := os.Getenv("ZDM_TEST_DSN")
	if base == "" {
		if os.Getenv("ZDM_REQUIRE_DB") != "" {
			t.Fatal("ZDM_REQUIRE_DB is set but ZDM_TEST_DSN is empty")
		}
		t.Skip("ZDM_TEST_DSN not set; skipping PostgreSQL integration test")
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	schema := "t_" + hex.EncodeToString(b)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		a, err := pgxpool.New(ctx, base)
		if err != nil {
			return
		}
		defer a.Close()
		_, _ = a.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	})

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse ZDM_TEST_DSN: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

// Pool opens a pool on a fresh schema.
func Pool(t testing.TB) (*pgxpool.Pool, string) {
	t.Helper()
	dsn := DSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, dsn
}

// Migrate applies the embedded migrations up to target (guards skipped).
func Migrate(t testing.TB, dsn string, target int64) {
	t.Helper()
	o := migrate.Defaults()
	o.Target, o.SkipGuards, o.LockID = target, true, LockID()
	if _, err := migrate.Run(Ctx(t), dsn, migrations.FS, migrations.RetiredBefore, o, Discard()); err != nil {
		t.Fatalf("migrate to %d: %v", target, err)
	}
}

// LockID returns a random advisory lock id, so parallel test packages do not
// serialise on the migrator's database-wide lock.
func LockID() int64 {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return int64(b[0])<<24 | int64(b[1])<<16 | int64(b[2])<<8 | int64(b[3]) + 1
}

// Discard is a logger that drops everything.
func Discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// Ctx returns a context that is cancelled when the test ends.
func Ctx(t testing.TB) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}
