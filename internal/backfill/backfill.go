// Package backfill fills first_name/last_name for rows written before the
// expand migration, in small keyset-paginated batches.
//
// Each batch is its own short transaction with lock_timeout and
// statement_timeout, touches at most BatchSize rows by primary-key range (so
// row locks are held for milliseconds and never escalate), and is followed by
// a pause that caps the write rate the backfill adds to the database. The job
// is idempotent: it only touches rows where first_name IS NULL, so it can be
// killed and restarted, or run again after a rollback, at any time.
package backfill

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/db"
)

// Options configure the backfill.
type Options struct {
	BatchSize        int
	Pause            time.Duration // sleep between batches (throttle)
	LockTimeout      time.Duration
	StatementTimeout time.Duration
	MaxRetries       int // per batch, on lock timeout
	ProgressEvery    int // log progress every N batches
}

// Defaults returns the options used in CI.
func Defaults() Options {
	return Options{
		BatchSize:        1000,
		Pause:            100 * time.Millisecond,
		LockTimeout:      time.Second,
		StatementTimeout: 10 * time.Second,
		MaxRetries:       10,
		ProgressEvery:    20,
	}
}

// Result summarises a backfill run.
type Result struct {
	Rows       int64
	Batches    int
	Retries    int
	Passes     int
	Duration   time.Duration
	MaxBatch   time.Duration
	RowsPerSec float64
}

// The split expressions are the SQL twin of names.Split, identical to the
// expand trigger.
const batchSQL = `UPDATE users
SET first_name = split_part(name, ' ', 1),
    last_name  = CASE WHEN strpos(name, ' ') > 0 THEN substr(name, strpos(name, ' ') + 1) ELSE '' END
WHERE id > $1 AND id <= $2 AND first_name IS NULL`

// Run backfills until no row with a NULL first_name is left.
func Run(ctx context.Context, pool *pgxpool.Pool, o Options, log *slog.Logger) (Result, error) {
	if o.BatchSize < 1 {
		return Result{}, errors.New("batch size must be positive")
	}
	start := time.Now()
	var res Result
	for {
		res.Passes++
		var maxID int64
		if err := pool.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM users`).Scan(&maxID); err != nil {
			return res, fmt.Errorf("read max id: %w", err)
		}
		log.Info("backfill_pass_start", "pass", res.Passes, "max_id", maxID, "batch_size", o.BatchSize,
			"pause_ms", o.Pause.Milliseconds())
		for lo := int64(0); lo < maxID; lo += int64(o.BatchSize) {
			hi := lo + int64(o.BatchSize)
			n, d, retries, err := runBatch(ctx, pool, lo, hi, o)
			res.Retries += retries
			if err != nil {
				return res, err
			}
			res.Rows += n
			res.Batches++
			res.MaxBatch = max(res.MaxBatch, d)
			if o.ProgressEvery > 0 && res.Batches%o.ProgressEvery == 0 {
				log.Info("backfill_progress", "rows", res.Rows, "batches", res.Batches,
					"last_id", hi, "max_id", maxID, "retries", res.Retries)
			}
			if o.Pause > 0 {
				select {
				case <-ctx.Done():
					return res, ctx.Err()
				case <-time.After(o.Pause):
				}
			}
		}
		var remaining int64
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE first_name IS NULL`).Scan(&remaining); err != nil {
			return res, fmt.Errorf("count remaining rows: %w", err)
		}
		if remaining == 0 {
			break
		}
		// Only possible if an old writer bypassed the trigger; go again.
		log.Warn("backfill_rows_remaining", "remaining", remaining, "pass", res.Passes)
		if res.Passes >= 5 {
			return res, fmt.Errorf("%d rows still not backfilled after %d passes", remaining, res.Passes)
		}
	}
	res.Duration = time.Since(start)
	if s := res.Duration.Seconds(); s > 0 {
		res.RowsPerSec = float64(res.Rows) / s
	}
	log.Info("backfill_done", "rows", res.Rows, "batches", res.Batches, "passes", res.Passes,
		"retries", res.Retries, "duration_ms", res.Duration.Milliseconds(),
		"rows_per_sec", int64(res.RowsPerSec), "max_batch_ms", float64(res.MaxBatch.Microseconds())/1000,
		"batch_size", o.BatchSize, "pause_ms", o.Pause.Milliseconds())
	return res, nil
}

func runBatch(ctx context.Context, pool *pgxpool.Pool, lo, hi int64, o Options) (int64, time.Duration, int, error) {
	for attempt := 0; ; attempt++ {
		t := time.Now()
		n, err := batchTx(ctx, pool, lo, hi, o)
		if err == nil {
			return n, time.Since(t), attempt, nil
		}
		if !db.IsLockTimeout(err) || attempt >= o.MaxRetries {
			return 0, 0, attempt, fmt.Errorf("batch (%d,%d]: %w", lo, hi, err)
		}
		select {
		case <-ctx.Done():
			return 0, 0, attempt, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 200 * time.Millisecond):
		}
	}
}

func batchTx(ctx context.Context, pool *pgxpool.Pool, lo, hi int64, o Options) (int64, error) {
	var n int64
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true), set_config('statement_timeout', $2, true)`,
			db.Millis(o.LockTimeout), db.Millis(o.StatementTimeout)); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, batchSQL, lo, hi)
		n = tag.RowsAffected()
		return err
	})
	return n, err
}
