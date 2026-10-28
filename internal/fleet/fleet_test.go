package fleet_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/fleet"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/pgtest"
)

func TestBlocking(t *testing.T) {
	alive := map[string]int{"v1": 2, "v2": 0, "v3": 3}
	if got := fleet.Blocking(alive, []string{"v1", "v2"}); !reflect.DeepEqual(got, []string{"v1=2"}) {
		t.Errorf("Blocking = %v", got)
	}
	if got := fleet.Blocking(alive, []string{"v0"}); got != nil {
		t.Errorf("Blocking = %v, want nil", got)
	}
}

func TestRegistry(t *testing.T) {
	pool, dsn := pgtest.Pool(t)
	ctx := pgtest.Ctx(t)
	pgtest.Migrate(t, dsn, 1)

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		fleet.Run(runCtx, pool, "pod-a", "v2", 20*time.Millisecond, pgtest.Discard())
		close(done)
	}()
	if err := fleet.Beat(ctx, pool, "pod-b", "v3"); err != nil {
		t.Fatal(err)
	}
	var alive map[string]int
	for range 100 {
		var err error
		if alive, err = fleet.Alive(ctx, pool, time.Minute); err != nil {
			t.Fatal(err)
		}
		if alive["v2"] == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !reflect.DeepEqual(alive, map[string]int{"v2": 1, "v3": 1}) {
		t.Fatalf("Alive = %v", alive)
	}
	stop()
	<-done

	if err := fleet.Leave(ctx, pool, "pod-a"); err != nil {
		t.Fatal(err)
	}
	// A stale heartbeat does not count as alive.
	if _, err := pool.Exec(ctx, `UPDATE app_instances SET last_seen = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if alive, err := fleet.Alive(ctx, pool, 20*time.Second); err != nil || len(alive) != 0 {
		t.Errorf("Alive with stale heartbeats = %v, %v", alive, err)
	}

	// Errors surface when the table does not exist.
	pool2, _ := pgtest.Pool(t)
	if err := fleet.Beat(ctx, pool2, "x", "v1"); err == nil {
		t.Error("Beat without the table: want error")
	}
	if err := fleet.Leave(ctx, pool2, "x"); err == nil {
		t.Error("Leave without the table: want error")
	}
	if _, err := fleet.Alive(ctx, pool2, time.Second); err == nil {
		t.Error("Alive without the table: want error")
	}
	runCtx2, stop2 := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop2()
	fleet.Run(runCtx2, pool2, "x", "v1", 10*time.Millisecond, pgtest.Discard()) // logs and keeps going
}
