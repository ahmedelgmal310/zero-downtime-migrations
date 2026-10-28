package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/cli"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/fleet"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/pgtest"
)

func run(t *testing.T, ctx context.Context, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := cli.Run(ctx, args, cli.Env{
		Version: "v3", Stdout: &out, Stderr: &errb,
		Getenv: func(k string) string { return env[k] },
	})
	return code, out.String(), errb.String()
}

func TestBasics(t *testing.T) {
	ctx := t.Context()
	if code, _, e := run(t, ctx, nil); code != 2 || !strings.Contains(e, "usage") {
		t.Errorf("no args = %d %q", code, e)
	}
	if code, _, e := run(t, ctx, nil, "nope"); code != 2 || !strings.Contains(e, "unknown command") {
		t.Errorf("unknown = %d %q", code, e)
	}
	if code, out, _ := run(t, ctx, nil, "version"); code != 0 || strings.TrimSpace(out) != "v3" {
		t.Errorf("version = %d %q", code, out)
	}
	if code, out, _ := run(t, ctx, nil, "help"); code != 0 || !strings.Contains(out, "migrate") {
		t.Errorf("help = %d %q", code, out)
	}
	if code, _, _ := run(t, ctx, nil, "migrate", "-h"); code != 0 {
		t.Errorf("migrate -h = %d", code)
	}
	if code, _, e := run(t, ctx, nil, "migrate"); code != 1 || !strings.Contains(e, "--target is required") {
		t.Errorf("migrate without target = %d %q", code, e)
	}
	if code, _, e := run(t, ctx, nil, "check"); code != 1 || !strings.Contains(e, "--step is required") {
		t.Errorf("check without step = %d %q", code, e)
	}
	if code, _, _ := run(t, ctx, nil, "seed", "--rows", "notanumber"); code != 1 {
		t.Errorf("bad flag = %d", code)
	}
	bad := map[string]string{"DATABASE_URL": "postgres://%zz"}
	for _, cmd := range []string{"serve", "backfill", "seed", "lockwatch"} {
		if code, _, _ := run(t, ctx, bad, cmd); code != 1 {
			t.Errorf("%s with a bad DSN = %d, want 1", cmd, code)
		}
	}
	if code, _, _ := run(t, ctx, bad, "check", "--step", "x"); code != 1 {
		t.Error("check with a bad DSN must fail")
	}
}

// The whole tool chain against a real database, the way scripts/e2e.sh uses it.
func TestPipeline(t *testing.T) {
	dsn := pgtest.DSN(t)
	ctx := pgtest.Ctx(t)
	env := map[string]string{"DATABASE_URL": dsn}
	dir := t.TempDir()
	state, checks := filepath.Join(dir, "state.json"), filepath.Join(dir, "checks.jsonl")

	must := func(args ...string) string {
		t.Helper()
		code, out, e := run(t, ctx, env, args...)
		if code != 0 {
			t.Fatalf("zdm %v = %d\nstdout: %s\nstderr: %s", args, code, out, e)
		}
		return out
	}
	out := must("migrate", "--target", "1")
	if !strings.Contains(out, `"msg":"migration_applied"`) {
		t.Errorf("migrate log = %s", out)
	}
	must("seed", "--rows", "300")
	must("check", "--step", "seeded", "--seed-rows", "300", "--state", state, "--out", checks, "--require-phase", "baseline")
	must("migrate", "--target", "3")
	must("backfill", "--batch-size", "100", "--pause", "1ms")
	must("check", "--step", "backfilled", "--state", state, "--out", checks, "--require-backfill")

	// The contract guard refuses while an old version heartbeats.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := fleet.Beat(ctx, pool, "old-pod", "v2"); err != nil {
		t.Fatal(err)
	}
	code, out, e := run(t, ctx, env, "migrate", "--target", "6")
	if code != 1 || !strings.Contains(out, `"msg":"guard_refused"`) {
		t.Fatalf("contract with v2 alive = %d\n%s\n%s", code, out, e)
	}
	if err := fleet.Leave(ctx, pool, "old-pod"); err != nil {
		t.Fatal(err)
	}
	code, _, e = run(t, ctx, env, "migrate", "--target", "6", "--heartbeat-max-age", "1m")
	if code != 0 {
		t.Fatalf("contract with no instances = %d %s", code, e)
	}
	must("check", "--step", "contracted", "--state", state, "--out", checks, "--require-phase", "contracted", "--exact-rows", "300")

	code, _, e = run(t, ctx, env, "check", "--step", "wrong", "--state", state, "--exact-rows", "1")
	if code != 1 || !strings.Contains(e, "consistency check failed") {
		t.Errorf("failing check = %d %q", code, e)
	}

	b, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal(b, &st); err != nil || st["seed_checksum"] == "" || st["rows"].(float64) != 300 {
		t.Errorf("state = %s", b)
	}

	lctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	locks := filepath.Join(dir, "locks.jsonl")
	if code, _, e := run(t, lctx, env, "lockwatch", "--interval", "10ms", "--out", locks); code != 0 {
		t.Errorf("lockwatch = %d %s", code, e)
	}
}

func writeResults(t *testing.T, dir string, errors int) {
	t.Helper()
	files := map[string]string{
		"requests.log":    "R,1000,read,200,2.0,1,v1\nR,1500,write,201,3.0,1,v1\n",
		"timeline.jsonl":  `{"step":"1. steady","start_ms":900,"end_ms":2000}` + "\n",
		"checks.jsonl":    `{"step":"1. steady","phase":"baseline","rows":11,"seed_checksum":"abc"}` + "\n",
		"lockwatch.jsonl": `{"t":1200,"app_waiting":1,"app_max_wait_ms":42,"other_waiting":0,"other_max_wait_ms":0}` + "\n",
		"backfill.log":    `{"msg":"backfill_done","rows":10,"batches":1,"rows_per_sec":100}` + "\n",
		"k6-summary.json": `{"api_requests":2,"api_errors":` + string(rune('0'+errors)) + `,"dropped_iterations":0}`,
		"jobs/01.log":     "# step: 1. steady\n" + `{"msg":"migration_applied","version":2,"file":"00002_x.sql","duration_ms":5,"attempts":1}` + "\n",
		"jobs/02.log":     `{"msg":"migrate_noop"}` + "\n",
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReport(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	writeResults(t, dir, 0)
	md, js := filepath.Join(dir, "report.md"), filepath.Join(dir, "results.json")
	code, _, e := run(t, ctx, nil, "report", "--dir", dir, "--seed-rows", "10", "--final-rows", "11",
		"--markdown", md, "--json", js)
	if code != 0 {
		t.Fatalf("report = %d %s", code, e)
	}
	b, _ := os.ReadFile(md)
	if !strings.Contains(string(b), "`00002_x.sql`") || !strings.Contains(string(b), "42 ms") {
		t.Errorf("report.md:\n%s", b)
	}
	if _, err := os.Stat(js); err != nil {
		t.Error(err)
	}
	if code, out, _ := run(t, ctx, nil, "report", "--dir", dir); code != 0 || !strings.Contains(out, "### Gates") {
		t.Errorf("report to stdout = %d", code)
	}

	// The k6 totals disagree with the request log: the gate fails.
	writeResults(t, dir, 1)
	if code, _, e := run(t, ctx, nil, "report", "--dir", dir); code != 1 || !strings.Contains(e, "gates failed") {
		t.Errorf("failing report = %d %q", code, e)
	}
	if code, _, _ := run(t, ctx, nil, "report", "--dir", dir, "--no-gate"); code != 0 {
		t.Errorf("--no-gate = %d", code)
	}
	if code, _, _ := run(t, ctx, nil, "report", "--dir", filepath.Join(dir, "missing")); code != 1 {
		t.Errorf("missing dir = %d", code)
	}
	if err := os.WriteFile(filepath.Join(dir, "k6-summary.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := run(t, ctx, nil, "report", "--dir", dir); code != 1 {
		t.Errorf("broken summary = %d", code)
	}
}
