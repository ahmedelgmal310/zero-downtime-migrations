package backfill_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/backfill"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/check"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/pgtest"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/seed"
)

func TestBackfill(t *testing.T) {
	pool, dsn := pgtest.Pool(t)
	ctx := pgtest.Ctx(t)
	pgtest.Migrate(t, dsn, 1)
	if err := seed.Run(ctx, pool, 2500); err != nil {
		t.Fatal(err)
	}
	pgtest.Migrate(t, dsn, 3)

	before, err := check.Run(ctx, pool, "before", check.Expect{SeedRows: 2500})
	if err != nil {
		t.Fatal(err)
	}
	if before.NotBackfilled != 2500 {
		t.Fatalf("not backfilled before = %d", before.NotBackfilled)
	}

	o := backfill.Defaults()
	o.BatchSize, o.Pause, o.ProgressEvery = 300, time.Millisecond, 2
	var logs bytes.Buffer
	res, err := backfill.Run(ctx, pool, o, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows != 2500 || res.Batches != 9 || res.Passes != 1 || res.RowsPerSec <= 0 {
		t.Errorf("result = %+v", res)
	}
	for _, msg := range []string{"backfill_pass_start", "backfill_progress", "backfill_done"} {
		if !strings.Contains(logs.String(), `"msg":"`+msg+`"`) {
			t.Errorf("log lacks %s", msg)
		}
	}

	after, err := check.Run(ctx, pool, "after", check.Expect{
		SeedRows: 2500, SeedChecksum: before.SeedChecksum, RequireBackfill: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !after.OK() {
		t.Fatalf("after backfill: %v", after.Problems)
	}

	// Idempotent: a second run touches nothing.
	res, err = backfill.Run(ctx, pool, o, pgtest.Discard())
	if err != nil || res.Rows != 0 {
		t.Errorf("second run = %+v, %v", res, err)
	}
}

func TestBackfillErrors(t *testing.T) {
	pool, dsn := pgtest.Pool(t)
	ctx := pgtest.Ctx(t)
	o := backfill.Defaults()
	o.BatchSize = 0
	if _, err := backfill.Run(ctx, pool, o, pgtest.Discard()); err == nil {
		t.Error("batch size 0: want error")
	}
	o.BatchSize = 10
	if _, err := backfill.Run(ctx, pool, o, pgtest.Discard()); err == nil {
		t.Error("no users table: want error")
	}

	pgtest.Migrate(t, dsn, 1)
	if err := seed.Run(ctx, pool, 100); err != nil {
		t.Fatal(err)
	}
	pgtest.Migrate(t, dsn, 3)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := backfill.Run(cctx, pool, o, pgtest.Discard()); err == nil {
		t.Error("cancelled context: want error")
	}
}
