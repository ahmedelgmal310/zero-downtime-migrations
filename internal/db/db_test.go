package db_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/db"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/pgtest"
)

func TestParseConfig(t *testing.T) {
	cfg, err := db.ParseConfig("postgres://u@localhost/x", db.Options{
		AppName: "zdm-test", LockTimeout: 1500 * time.Millisecond, StatementTimeout: time.Minute, MaxConns: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	rp := cfg.ConnConfig.RuntimeParams
	if rp["application_name"] != "zdm-test" || rp["lock_timeout"] != "1500ms" || rp["statement_timeout"] != "60000ms" {
		t.Errorf("runtime params = %v", rp)
	}
	if cfg.MaxConns != 7 {
		t.Errorf("MaxConns = %d", cfg.MaxConns)
	}
	if _, err := db.ParseConfig("postgres://%zz", db.Options{}); err == nil {
		t.Error("want parse error for a malformed DSN")
	}
}

func TestSQLState(t *testing.T) {
	lock := fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: db.CodeLockNotAvailable})
	if !db.IsLockTimeout(lock) {
		t.Error("IsLockTimeout(wrapped 55P03) = false")
	}
	if db.IsLockTimeout(errors.New("other")) || db.SQLState(nil) != "" {
		t.Error("non-Postgres errors must have no SQLSTATE")
	}
}

func TestOpen(t *testing.T) {
	dsn := pgtest.DSN(t)
	ctx := pgtest.Ctx(t)
	pool, err := db.Open(ctx, dsn, db.Options{AppName: "zdm-test", LockTimeout: 250 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var lt, app string
	if err := pool.QueryRow(ctx, `SELECT current_setting('lock_timeout'), current_setting('application_name')`).Scan(&lt, &app); err != nil {
		t.Fatal(err)
	}
	if lt != "250ms" || app != "zdm-test" {
		t.Errorf("lock_timeout=%q application_name=%q", lt, app)
	}

	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := db.Open(cctx, "postgres://nobody@127.0.0.1:1/x?connect_timeout=1", db.Options{}); err == nil {
		t.Error("want an error for an unreachable database")
	}
}
