// Package cli implements the zdm command: one binary that is the API server,
// the migration Job, the backfill Job and the e2e tooling.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/backfill"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/db"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/lockwatch"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/migrate"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/seed"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/serve"
	"github.com/ahmedelgmal310/zero-downtime-migrations/migrations"
)

// Env is what a command can touch, injectable for tests.
type Env struct {
	Version string // app release baked in at build time
	Stdout  io.Writer
	Stderr  io.Writer
	Getenv  func(string) string
}

const usage = `usage: zdm <command> [flags]

commands:
  serve       run the API (release %s)
  migrate     apply migrations up to --target (Helm pre-upgrade hook)
  backfill    batched, throttled backfill of first_name/last_name
  seed        bulk-load deterministic users
  check       data consistency check for one rollout step
  lockwatch   sample lock waits from pg_stat_activity until interrupted
  report      build the results table and enforce the gates
  version     print the app release
`

// Run executes a command and returns the process exit code.
func Run(ctx context.Context, args []string, env Env) int {
	if env.Getenv == nil {
		env.Getenv = os.Getenv
	}
	if len(args) == 0 {
		fmt.Fprintf(env.Stderr, usage, env.Version)
		return 2
	}
	log := slog.New(slog.NewJSONHandler(env.Stdout, nil)).With("cmd", args[0])
	var err error
	switch args[0] {
	case "serve":
		err = runServe(ctx, args[1:], env, log)
	case "migrate":
		err = runMigrate(ctx, args[1:], env, log)
	case "backfill":
		err = runBackfill(ctx, args[1:], env, log)
	case "seed":
		err = runSeed(ctx, args[1:], env, log)
	case "check":
		err = runCheck(ctx, args[1:], env)
	case "lockwatch":
		err = runLockwatch(ctx, args[1:], env, log)
	case "report":
		err = runReport(args[1:], env)
	case "version":
		fmt.Fprintln(env.Stdout, env.Version)
	case "help", "-h", "--help":
		fmt.Fprintf(env.Stdout, usage, env.Version)
	default:
		fmt.Fprintf(env.Stderr, "unknown command %q\n"+usage, args[0], env.Version)
		return 2
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(env.Stderr, "zdm %s: %v\n", args[0], err)
		return 1
	}
	return 0
}

func newFlags(name string, env Env) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	return fs
}

// dsnFlag registers --dsn. Empty means libpq environment variables.
func dsnFlag(fs *flag.FlagSet, env Env) *string {
	return fs.String("dsn", env.Getenv("DATABASE_URL"), "PostgreSQL DSN (default: $DATABASE_URL, else PG* environment variables)")
}

func runServe(ctx context.Context, args []string, env Env, log *slog.Logger) error {
	cfg := serve.Defaults()
	fs := newFlags("serve", env)
	dsn := dsnFlag(fs, env)
	fs.StringVar(&cfg.Addr, "addr", cfg.Addr, "listen address")
	fs.DurationVar(&cfg.DrainDelay, "drain-delay", cfg.DrainDelay, "keep serving after SIGTERM with keep-alives disabled")
	fs.DurationVar(&cfg.HeartbeatInterval, "heartbeat", cfg.HeartbeatInterval, "fleet registry heartbeat interval")
	fs.DurationVar(&cfg.SchemaPoll, "schema-poll", cfg.SchemaPoll, "schema compatibility re-check interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg.DSN, cfg.Version = *dsn, env.Version
	cfg.InstanceID = env.Getenv("POD_NAME")
	if cfg.InstanceID == "" {
		h, _ := os.Hostname()
		cfg.InstanceID = h
	}
	return serve.Run(ctx, cfg, nil, log.With("version", env.Version))
}

func runMigrate(ctx context.Context, args []string, env Env, log *slog.Logger) error {
	o := migrate.Defaults()
	fs := newFlags("migrate", env)
	dsn := dsnFlag(fs, env)
	fs.Int64Var(&o.Target, "target", 0, "schema version to migrate up to (required)")
	fs.DurationVar(&o.LockTimeout, "lock-timeout", o.LockTimeout, "lock_timeout for every migration session")
	fs.DurationVar(&o.StatementTimeout, "statement-timeout", o.StatementTimeout, "statement_timeout for every migration session")
	fs.IntVar(&o.MaxAttempts, "max-attempts", o.MaxAttempts, "attempts per migration when lock_timeout expires")
	fs.DurationVar(&o.Backoff, "backoff", o.Backoff, "retry backoff base (multiplied by the attempt number)")
	fs.DurationVar(&o.HeartbeatMaxAge, "heartbeat-max-age", o.HeartbeatMaxAge, "a fleet heartbeat newer than this counts as a running instance")
	fs.BoolVar(&o.SkipGuards, "unsafe-skip-guards", false, "DEMO ONLY: apply contract migrations while old versions run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if o.Target < 1 {
		return errors.New("--target is required")
	}
	_, err := migrate.Run(ctx, *dsn, migrations.FS, migrations.RetiredBefore, o, log)
	return err
}

func runBackfill(ctx context.Context, args []string, env Env, log *slog.Logger) error {
	o := backfill.Defaults()
	fs := newFlags("backfill", env)
	dsn := dsnFlag(fs, env)
	fs.IntVar(&o.BatchSize, "batch-size", o.BatchSize, "rows per batch (one short transaction each)")
	fs.DurationVar(&o.Pause, "pause", o.Pause, "sleep between batches (throttle)")
	fs.DurationVar(&o.LockTimeout, "lock-timeout", o.LockTimeout, "lock_timeout per batch")
	fs.DurationVar(&o.StatementTimeout, "statement-timeout", o.StatementTimeout, "statement_timeout per batch")
	fs.IntVar(&o.ProgressEvery, "progress-every", o.ProgressEvery, "log progress every N batches")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pool, err := db.Open(ctx, *dsn, db.Options{AppName: "zdm-backfill", MaxConns: 2})
	if err != nil {
		return err
	}
	defer pool.Close()
	_, err = backfill.Run(ctx, pool, o, log)
	return err
}

func runSeed(ctx context.Context, args []string, env Env, log *slog.Logger) error {
	fs := newFlags("seed", env)
	dsn := dsnFlag(fs, env)
	rows := fs.Int64("rows", 200000, "rows to insert")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pool, err := db.Open(ctx, *dsn, db.Options{AppName: "zdm-seed"})
	if err != nil {
		return err
	}
	defer pool.Close()
	t := time.Now()
	if err := seed.Run(ctx, pool, *rows); err != nil {
		return err
	}
	log.Info("seed_done", "rows", *rows, "duration_ms", time.Since(t).Milliseconds())
	return nil
}

func runLockwatch(ctx context.Context, args []string, env Env, log *slog.Logger) error {
	fs := newFlags("lockwatch", env)
	dsn := dsnFlag(fs, env)
	interval := fs.Duration("interval", 50*time.Millisecond, "sampling interval")
	out := fs.String("out", "", "JSON lines output file (default stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pool, err := db.Open(ctx, *dsn, db.Options{AppName: "zdm-lockwatch", MaxConns: 1})
	if err != nil {
		return err
	}
	defer pool.Close()
	w := env.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		w = f
	}
	maxWait, err := lockwatch.Run(ctx, pool, *interval, w)
	log.Info("lockwatch_done", "app_max_wait_ms", maxWait)
	return err
}
