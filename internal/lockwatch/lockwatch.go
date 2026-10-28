// Package lockwatch samples pg_stat_activity for sessions waiting on a heavy
// lock. During a migration this is the number that matters: every query
// queued behind a DDL statement shows up here, and how long the oldest one has
// waited is the extra latency a user sees.
package lockwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Sample is one observation with at least one waiting session. API pods
// connect with application_name "zdm-api"; everything else (the migrator,
// the backfill, CREATE INDEX CONCURRENTLY waiting for old snapshots) is
// reported separately, because only the API waits are user-facing.
type Sample struct {
	TimeMs         int64   `json:"t"` // unix milliseconds
	AppWaiting     int     `json:"app_waiting"`
	AppMaxWaitMs   float64 `json:"app_max_wait_ms"`
	OtherWaiting   int     `json:"other_waiting"`
	OtherMaxWaitMs float64 `json:"other_max_wait_ms"`
}

// AppName is the application_name of API connections.
const AppName = "zdm-api"

const sampleSQL = `SELECT
  count(*) FILTER (WHERE application_name = $1)::int,
  coalesce(max(extract(epoch FROM clock_timestamp() - query_start) * 1000) FILTER (WHERE application_name = $1), 0)::float8,
  count(*) FILTER (WHERE application_name <> $1)::int,
  coalesce(max(extract(epoch FROM clock_timestamp() - query_start) * 1000) FILTER (WHERE application_name <> $1), 0)::float8
FROM pg_stat_activity
WHERE wait_event_type = 'Lock' AND pid <> pg_backend_pid() AND datname = current_database()`

// Take returns the current lock-wait state.
func Take(ctx context.Context, pool *pgxpool.Pool) (Sample, error) {
	var s Sample
	err := pool.QueryRow(ctx, sampleSQL, AppName).Scan(&s.AppWaiting, &s.AppMaxWaitMs, &s.OtherWaiting, &s.OtherMaxWaitMs)
	if err != nil {
		return s, fmt.Errorf("sample locks: %w", err)
	}
	s.TimeMs = time.Now().UnixMilli()
	return s, nil
}

// Run samples every interval until ctx is cancelled and writes every sample
// with a waiting session to w as JSON lines. It returns the largest wait seen by an API query.
func Run(ctx context.Context, pool *pgxpool.Pool, interval time.Duration, w io.Writer) (float64, error) {
	enc := json.NewEncoder(w)
	t := time.NewTicker(interval)
	defer t.Stop()
	var maxWait float64
	for {
		select {
		case <-ctx.Done():
			return maxWait, nil
		case <-t.C:
		}
		s, err := Take(ctx, pool)
		if err != nil {
			if ctx.Err() != nil {
				return maxWait, nil
			}
			return maxWait, err
		}
		if s.AppWaiting == 0 && s.OtherWaiting == 0 {
			continue
		}
		maxWait = max(maxWait, s.AppMaxWaitMs)
		if err := enc.Encode(s); err != nil {
			return maxWait, err
		}
	}
}
