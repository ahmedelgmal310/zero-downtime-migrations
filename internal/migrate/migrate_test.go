package migrate_test

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/db"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/fleet"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/migrate"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/pgtest"
	"github.com/ahmedelgmal310/zero-downtime-migrations/migrations"
)

func opts(target int64) migrate.Options {
	o := migrate.Defaults()
	o.Target, o.LockID = target, pgtest.LockID()
	return o
}

func TestPhases(t *testing.T) {
	pool, dsn := pgtest.Pool(t)
	ctx := pgtest.Ctx(t)
	log := pgtest.Discard()

	res, err := migrate.Run(ctx, dsn, migrations.FS, migrations.RetiredBefore, opts(migrations.VersionBaseline), log)
	if err != nil {
		t.Fatal(err)
	}
	if res.From != 0 || res.To != 1 || len(res.Applied) != 1 || res.Applied[0].File != "00001_baseline.sql" {
		t.Fatalf("baseline result = %+v", res)
	}

	res, err = migrate.Run(ctx, dsn, migrations.FS, migrations.RetiredBefore, opts(migrations.VersionExpand), log)
	if err != nil {
		t.Fatal(err)
	}
	if res.To != 3 || len(res.Applied) != 2 || res.Applied[0].Attempts != 1 {
		t.Fatalf("expand result = %+v", res)
	}

	// A lower target never runs down migrations.
	res, err = migrate.Run(ctx, dsn, migrations.FS, migrations.RetiredBefore, opts(migrations.VersionBaseline), log)
	if err != nil || len(res.Applied) != 0 || res.To != 3 {
		t.Fatalf("rollback target: res=%+v err=%v", res, err)
	}
	if v, err := migrate.Version(ctx, dsn, migrations.FS); err != nil || v != 3 {
		t.Fatalf("Version = %d, %v", v, err)
	}

	// The contract guard refuses while a retired version heartbeats.
	if err := fleet.Beat(ctx, pool, "pod-v1", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := fleet.Beat(ctx, pool, "pod-v3", "v3"); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	_, err = migrate.Run(ctx, dsn, migrations.FS, migrations.RetiredBefore, opts(migrations.VersionContract),
		slog.New(slog.NewJSONHandler(&logs, nil)))
	if !errors.Is(err, migrate.ErrGuard) {
		t.Fatalf("contract with v1 alive: err = %v, want ErrGuard", err)
	}
	if !strings.Contains(logs.String(), `"msg":"guard_refused"`) || !strings.Contains(err.Error(), "v1=1") {
		t.Errorf("guard refusal not explained: %v\n%s", err, logs.String())
	}
	if v, _ := migrate.Version(ctx, dsn, migrations.FS); v != 5 {
		t.Errorf("migrations before the guarded one should apply; version = %d, want 5", v)
	}
	var hasName bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'users' AND column_name = 'name')`).Scan(&hasName); err != nil || !hasName {
		t.Fatalf("name column must still exist after a refused contract (exists=%v err=%v)", hasName, err)
	}

	// Once v1 is gone, the contract runs.
	if err := fleet.Leave(ctx, pool, "pod-v1"); err != nil {
		t.Fatal(err)
	}
	res, err = migrate.Run(ctx, dsn, migrations.FS, migrations.RetiredBefore, opts(migrations.VersionContract), log)
	if err != nil || res.To != 6 {
		t.Fatalf("contract: res=%+v err=%v", res, err)
	}
}

func TestSkipGuards(t *testing.T) {
	pool, dsn := pgtest.Pool(t)
	ctx := pgtest.Ctx(t)
	pgtest.Migrate(t, dsn, 5)
	if err := fleet.Beat(ctx, pool, "pod-v1", "v1"); err != nil {
		t.Fatal(err)
	}
	o := opts(6)
	o.SkipGuards = true
	if _, err := migrate.Run(ctx, dsn, migrations.FS, migrations.RetiredBefore, o, pgtest.Discard()); err != nil {
		t.Fatal(err)
	}
}

// A migration that cannot get its lock within lock_timeout is retried and
// succeeds once the blocker is gone; with no retries left it fails cleanly.
func TestLockTimeoutRetry(t *testing.T) {
	pool, dsn := pgtest.Pool(t)
	ctx := pgtest.Ctx(t)
	pgtest.Migrate(t, dsn, 1)

	hold := func(d time.Duration) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `LOCK TABLE users IN ACCESS SHARE MODE`); err != nil {
			t.Fatal(err)
		}
		go func() {
			time.Sleep(d)
			_ = tx.Rollback(ctx)
		}()
	}

	o := opts(2)
	o.LockTimeout, o.MaxAttempts, o.Backoff = 100*time.Millisecond, 1, 10*time.Millisecond
	hold(3 * time.Second)
	_, err := migrate.Run(ctx, dsn, migrations.FS, migrations.RetiredBefore, o, pgtest.Discard())
	if !db.IsLockTimeout(err) {
		t.Fatalf("single attempt under a held lock: err = %v, want lock timeout", err)
	}
	waitUnlocked(t, pool)

	// The migration file sets lock_timeout = 1s itself, so each attempt waits
	// about a second; the blocker outlives the first two attempts.
	o.MaxAttempts, o.Backoff = 30, 50*time.Millisecond
	hold(2500 * time.Millisecond)
	res, err := migrate.Run(ctx, dsn, migrations.FS, migrations.RetiredBefore, o, pgtest.Discard())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 || res.Applied[0].Attempts < 2 {
		t.Fatalf("want one migration applied after retries, got %+v", res.Applied)
	}
}

func waitUnlocked(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := pgtest.Ctx(t)
	for range 100 {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks l JOIN pg_class c ON c.oid = l.relation
			WHERE c.relname = 'users' AND c.relnamespace = current_schema()::regnamespace`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("users lock never released")
}

func TestBadDSN(t *testing.T) {
	ctx := t.Context()
	if _, err := migrate.Run(ctx, "postgres://%zz", migrations.FS, nil, opts(1), pgtest.Discard()); err == nil {
		t.Error("want error for a malformed DSN")
	}
	if _, err := migrate.Version(ctx, "postgres://%zz", migrations.FS); err == nil {
		t.Error("want error for a malformed DSN")
	}
}

func TestConnectWaitGivesUp(t *testing.T) {
	o := opts(1)
	o.ConnectWait = 1500 * time.Millisecond
	start := time.Now()
	_, err := migrate.Run(t.Context(), "postgres://nobody@127.0.0.1:1/x?connect_timeout=1", migrations.FS, nil, o, pgtest.Discard())
	if err == nil || !strings.Contains(err.Error(), "connect") {
		t.Fatalf("err = %v, want a connect error", err)
	}
	if time.Since(start) < time.Second {
		t.Error("gave up without retrying")
	}
}
