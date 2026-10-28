// Package migrate applies the embedded goose migrations one version at a time
// with the safety rails a schema change under live traffic needs:
//
//   - every session has lock_timeout and statement_timeout, so a DDL statement
//     can never queue behind a long query (and block everything queued behind
//     it) for longer than lock_timeout;
//   - a lock timeout is retried with backoff, because each transactional
//     migration rolls back cleanly when it fails;
//   - a Postgres advisory lock serialises concurrent migrators;
//   - guarded (contract) migrations refuse to run while an app version that
//     still uses the old schema is alive in the fleet registry;
//   - migrations never run down automatically: a target below the current
//     version is a no-op, which keeps `helm rollback` of the app safe.
package migrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/db"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/fleet"
)

// Options configure a migration run.
type Options struct {
	Target           int64
	LockTimeout      time.Duration
	StatementTimeout time.Duration
	MaxAttempts      int           // attempts per migration on lock timeout
	Backoff          time.Duration // base backoff, multiplied by the attempt number
	HeartbeatMaxAge  time.Duration // how recent a heartbeat counts as "alive"
	SkipGuards       bool          // demo only: run guarded migrations unconditionally
	LockID           int64         // advisory lock id; 0 uses the goose default
}

// Defaults returns production-like defaults for Options.
func Defaults() Options {
	return Options{
		LockTimeout:      time.Second,
		StatementTimeout: 2 * time.Minute,
		MaxAttempts:      10,
		Backoff:          500 * time.Millisecond,
		HeartbeatMaxAge:  20 * time.Second,
	}
}

// Applied describes one migration that ran.
type Applied struct {
	Version  int64
	File     string
	Duration time.Duration
	Attempts int
}

// Result summarises a run.
type Result struct {
	From, To int64
	Applied  []Applied
}

// ErrGuard is returned when a guarded migration is refused.
var ErrGuard = errors.New("contract migration refused: retired app versions are still running")

// Run migrates the database behind dsn up to o.Target.
func Run(ctx context.Context, dsn string, fsys fs.FS, guards map[int64][]string, o Options, log *slog.Logger) (Result, error) {
	if o.MaxAttempts < 1 {
		o.MaxAttempts = 1
	}
	poolCfg, err := db.ParseConfig(dsn, db.Options{
		AppName: "zdm-migrate", LockTimeout: o.LockTimeout, StatementTimeout: o.StatementTimeout,
	})
	if err != nil {
		return Result{}, err
	}
	sqlDB := stdlib.OpenDB(*poolCfg.ConnConfig)
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(4)
	poolCfg.MaxConns = 1
	guardPool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return Result{}, fmt.Errorf("open guard pool: %w", err)
	}
	defer guardPool.Close()

	lockOpts := []lock.SessionLockerOption{lock.WithLockTimeout(1, 300)}
	if o.LockID != 0 {
		lockOpts = append(lockOpts, lock.WithLockID(o.LockID))
	}
	locker, err := lock.NewPostgresSessionLocker(lockOpts...)
	if err != nil {
		return Result{}, err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys,
		goose.WithSessionLocker(locker), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return Result{}, fmt.Errorf("load migrations: %w", err)
	}

	current, err := p.GetDBVersion(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("read schema version: %w", err)
	}
	res := Result{From: current, To: current}
	log.Info("migrate_start", "current", current, "target", o.Target,
		"lock_timeout", db.Millis(o.LockTimeout), "statement_timeout", db.Millis(o.StatementTimeout))
	if current >= o.Target {
		log.Info("migrate_noop", "current", current, "target", o.Target,
			"reason", "schema is at or ahead of the target; down migrations never run automatically")
		return res, nil
	}

	for _, src := range p.ListSources() {
		v := src.Version
		if v <= current {
			continue
		}
		if v > o.Target {
			break
		}
		if retired, ok := guards[v]; ok {
			if err := checkGuard(ctx, guardPool, v, retired, o, log); err != nil {
				return res, err
			}
		}
		a, err := applyNext(ctx, p, v, o, log)
		if err != nil {
			return res, err
		}
		res.Applied = append(res.Applied, a)
		res.To = v
	}
	log.Info("migrate_done", "from", res.From, "to", res.To, "applied", len(res.Applied))
	return res, nil
}

func applyNext(ctx context.Context, p *goose.Provider, v int64, o Options, log *slog.Logger) (Applied, error) {
	for attempt := 1; ; attempt++ {
		r, err := p.UpByOne(ctx)
		if err == nil {
			a := Applied{Version: r.Source.Version, File: path.Base(r.Source.Path), Duration: r.Duration, Attempts: attempt}
			if a.Version != v {
				return a, fmt.Errorf("applied version %d, expected %d", a.Version, v)
			}
			log.Info("migration_applied", "version", a.Version, "file", a.File,
				"duration_ms", ms(a.Duration), "attempts", attempt)
			return a, nil
		}
		if !db.IsLockTimeout(err) || attempt >= o.MaxAttempts {
			log.Error("migration_failed", "version", v, "attempts", attempt, "err", err)
			return Applied{Version: v, Attempts: attempt}, fmt.Errorf("migration %d: %w", v, err)
		}
		wait := o.Backoff * time.Duration(attempt)
		log.Warn("migration_lock_timeout_retry", "version", v, "attempt", attempt, "retry_in_ms", wait.Milliseconds())
		select {
		case <-ctx.Done():
			return Applied{Version: v, Attempts: attempt}, ctx.Err()
		case <-time.After(wait):
		}
	}
}

func checkGuard(ctx context.Context, q fleet.Querier, v int64, retired []string, o Options, log *slog.Logger) error {
	if o.SkipGuards {
		log.Warn("guard_skipped", "version", v, "retired", retired,
			"reason", "--unsafe-skip-guards: demo of what the guard prevents")
		return nil
	}
	alive, err := fleet.Alive(ctx, q, o.HeartbeatMaxAge)
	if err != nil {
		return fmt.Errorf("guard for migration %d: %w", v, err)
	}
	if blocking := fleet.Blocking(alive, retired); len(blocking) > 0 {
		log.Error("guard_refused", "version", v, "retired", retired, "alive", blocking)
		return fmt.Errorf("%w: migration %d needs %v retired, alive: %v", ErrGuard, v, retired, blocking)
	}
	log.Info("guard_passed", "version", v, "retired", retired, "alive", alive)
	return nil
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// Version returns the current schema version (0 for an empty database).
func Version(ctx context.Context, dsn string, fsys fs.FS) (int64, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return 0, err
	}
	sqlDB := stdlib.OpenDB(*cfg)
	defer sqlDB.Close()
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys, goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return 0, err
	}
	return p.GetDBVersion(ctx)
}
