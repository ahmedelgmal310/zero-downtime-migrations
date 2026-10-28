// Package fleet records which app versions are running. Every API pod upserts
// a heartbeat row into app_instances; the migrator reads it to refuse a
// contract migration while an older release could still use the old schema.
//
// The registry lives in the database rather than in the Kubernetes API so the
// guard also holds for writers outside the cluster (cron jobs, a second
// cluster) and needs no RBAC for the migration Job.
package fleet

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is satisfied by *pgxpool.Pool, *pgx.Conn and pgx.Tx.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Beat upserts the heartbeat for one instance.
func Beat(ctx context.Context, pool *pgxpool.Pool, instanceID, version string) error {
	_, err := pool.Exec(ctx, `INSERT INTO app_instances (instance_id, version)
		VALUES ($1, $2)
		ON CONFLICT (instance_id) DO UPDATE SET version = EXCLUDED.version, last_seen = now()`,
		instanceID, version)
	if err != nil {
		return fmt.Errorf("heartbeat: %w", err)
	}
	return nil
}

// Leave removes the instance from the registry on graceful shutdown.
func Leave(ctx context.Context, pool *pgxpool.Pool, instanceID string) error {
	_, err := pool.Exec(ctx, `DELETE FROM app_instances WHERE instance_id = $1`, instanceID)
	if err != nil {
		return fmt.Errorf("leave registry: %w", err)
	}
	return nil
}

// Run heartbeats every interval until ctx is cancelled. Errors are logged, not
// fatal: a missed heartbeat only makes the guard more conservative.
func Run(ctx context.Context, pool *pgxpool.Pool, instanceID, version string, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := Beat(ctx, pool, instanceID, version); err != nil && ctx.Err() == nil {
			log.Warn("heartbeat failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Alive returns the number of live instances per version, where live means a
// heartbeat within the last maxAge.
func Alive(ctx context.Context, q Querier, maxAge time.Duration) (map[string]int, error) {
	rows, err := q.Query(ctx, `SELECT version, count(*) FROM app_instances
		WHERE last_seen > now() - make_interval(secs => $1)
		GROUP BY version`, maxAge.Seconds())
	if err != nil {
		return nil, fmt.Errorf("read app_instances: %w", err)
	}
	out := map[string]int{}
	var v string
	var n int
	_, err = pgx.ForEachRow(rows, []any{&v, &n}, func() error {
		out[v] = n
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read app_instances: %w", err)
	}
	return out, nil
}

// Blocking returns the live versions from retired, sorted, with their counts
// formatted as "v1=2".
func Blocking(alive map[string]int, retired []string) []string {
	var out []string
	for v, n := range alive {
		if n > 0 && slices.Contains(retired, v) {
			out = append(out, fmt.Sprintf("%s=%d", v, n))
		}
	}
	sort.Strings(out)
	return out
}
