package serve_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/fleet"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/pgtest"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/serve"
)

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func waitStatus(t *testing.T, url string, want int) {
	t.Helper()
	for range 200 {
		if code, _ := get(t, url); code == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%s never returned %d", url, want)
}

// The pod stays unready until the expand migration adds the columns v2
// needs, serves traffic, registers in the fleet and leaves it on shutdown.
func TestServeLifecycle(t *testing.T) {
	pool, dsn := pgtest.Pool(t)
	ctx := pgtest.Ctx(t)
	pgtest.Migrate(t, dsn, 1)

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String()
	cfg := serve.Defaults()
	cfg.DSN, cfg.Version, cfg.InstanceID = dsn, "v2", "pod-test"
	cfg.HeartbeatInterval, cfg.SchemaPoll, cfg.DrainDelay = 50*time.Millisecond, 50*time.Millisecond, 50*time.Millisecond

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- serve.Run(runCtx, cfg, ln, pgtest.Discard()) }()

	waitStatus(t, base+"/healthz", http.StatusOK)
	if code, _ := get(t, base+"/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("v2 on the baseline schema: readyz = %d, want 503", code)
	}
	pgtest.Migrate(t, dsn, 3)
	waitStatus(t, base+"/readyz", http.StatusOK)

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/users", strings.NewReader(`{"name":"Ada Lovelace"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || resp.Header.Get("X-App-Version") != "v2" {
		t.Fatalf("create = %d (version %q)", resp.StatusCode, resp.Header.Get("X-App-Version"))
	}

	alive, err := fleet.Alive(ctx, pool, time.Minute)
	if err != nil || alive["v2"] != 1 {
		t.Fatalf("fleet while serving = %v, %v", alive, err)
	}

	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("graceful shutdown did not finish")
	}
	if alive, _ := fleet.Alive(ctx, pool, time.Minute); alive["v2"] != 0 {
		t.Errorf("instance still registered after shutdown: %v", alive)
	}
}

func TestServeErrors(t *testing.T) {
	ctx := t.Context()
	cfg := serve.Defaults()
	cfg.DSN = "postgres://%zz"
	if err := serve.Run(ctx, cfg, nil, pgtest.Discard()); err == nil {
		t.Error("bad DSN: want error")
	}

	dsn := pgtest.DSN(t)
	cfg.DSN, cfg.Version = dsn, "v9"
	if err := serve.Run(ctx, cfg, nil, pgtest.Discard()); err == nil {
		t.Error("unknown version: want error")
	}
	cfg.Version, cfg.Addr = "v1", "256.0.0.1:99999"
	if err := serve.Run(ctx, cfg, nil, pgtest.Discard()); err == nil {
		t.Error("bad listen address: want error")
	}
}
