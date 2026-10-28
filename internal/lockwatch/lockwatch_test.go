package lockwatch_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/db"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/lockwatch"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/pgtest"
)

// An API query queued behind an exclusive lock is seen and timed.
func TestSeesAPIQueryWaiting(t *testing.T) {
	pool, dsn := pgtest.Pool(t)
	ctx := pgtest.Ctx(t)
	pgtest.Migrate(t, dsn, 1)

	api, err := db.Open(ctx, dsn, db.Options{AppName: lockwatch.AppName, MaxConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE users IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan error, 1)
	go func() {
		_, err := api.Exec(ctx, `SELECT count(*) FROM users`)
		blocked <- err
	}()

	var out bytes.Buffer
	wctx, stop := context.WithCancel(ctx)
	watched := make(chan float64, 1)
	go func() {
		m, _ := lockwatch.Run(wctx, pool, 20*time.Millisecond, &out)
		watched <- m
	}()
	time.Sleep(600 * time.Millisecond)
	_ = tx.Rollback(ctx)
	if err := <-blocked; err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	stop()
	maxWait := <-watched

	if maxWait < 200 {
		t.Fatalf("max API lock wait = %.1f ms, want >= 200 ms; samples:\n%s", maxWait, out.String())
	}
	first := strings.SplitN(out.String(), "\n", 2)[0]
	var s lockwatch.Sample
	if err := json.Unmarshal([]byte(first), &s); err != nil || s.AppWaiting < 1 || s.TimeMs == 0 {
		t.Fatalf("first sample %q: %+v %v", first, s, err)
	}
}

func TestTakeError(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://nobody@127.0.0.1:1/x?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := lockwatch.Take(t.Context(), pool); err == nil {
		t.Error("want error from an unreachable database")
	}
	if _, err := lockwatch.Run(t.Context(), pool, time.Millisecond, &bytes.Buffer{}); err == nil {
		t.Error("Run must return the sampling error")
	}
}
