// Package db opens PostgreSQL connections with explicit session settings.
//
// An empty DSN means "use the libpq environment" (PGHOST, PGUSER, PGPASSWORD,
// PGDATABASE, ...), which is how the Kubernetes manifests pass credentials.
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Options are applied to every connection in the pool as session parameters.
type Options struct {
	AppName          string
	LockTimeout      time.Duration // 0 leaves the server default
	StatementTimeout time.Duration // 0 leaves the server default
	MaxConns         int32         // 0 keeps the pgx default
}

// ParseConfig builds a pool config from a DSN and Options.
func ParseConfig(dsn string, o Options) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database config: %w", err)
	}
	rp := cfg.ConnConfig.RuntimeParams
	if o.AppName != "" {
		rp["application_name"] = o.AppName
	}
	if o.LockTimeout > 0 {
		rp["lock_timeout"] = Millis(o.LockTimeout)
	}
	if o.StatementTimeout > 0 {
		rp["statement_timeout"] = Millis(o.StatementTimeout)
	}
	if o.MaxConns > 0 {
		cfg.MaxConns = o.MaxConns
	}
	return cfg, nil
}

// Open creates a pool and waits until the database answers a ping.
func Open(ctx context.Context, dsn string, o Options) (*pgxpool.Pool, error) {
	cfg, err := ParseConfig(dsn, o)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// Millis formats a duration the way Postgres expects for *_timeout settings.
func Millis(d time.Duration) string {
	return fmt.Sprintf("%dms", d.Milliseconds())
}

// SQLState returns the SQLSTATE code of a Postgres error, or "".
func SQLState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// Postgres error codes the tools react to.
const (
	CodeLockNotAvailable = "55P03" // lock_timeout expired
	CodeQueryCanceled    = "57014" // statement_timeout expired
	CodeUndefinedColumn  = "42703"
)

// IsLockTimeout reports whether err is a lock_timeout expiry, which is safe
// to retry for transactional statements.
func IsLockTimeout(err error) bool {
	return SQLState(err) == CodeLockNotAvailable
}
