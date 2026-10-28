package report_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/check"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/lockwatch"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/report"
)

const requestLog = `k6 starting
R,1000,read,200,2.50,1,v1
R,1500,write,201,4.00,1,v1
R,2100,read,200,3.00,1,v1
R,2600,read,200,9.00,1,v2
R,3100,write,201,5.00,1,v2
R,3600,read,200,1.00,1,v2
`

func inputs(t *testing.T) report.Inputs {
	t.Helper()
	reqs, err := report.ParseRequests(strings.NewReader(requestLog))
	if err != nil {
		t.Fatal(err)
	}
	return report.Inputs{
		Requests: reqs,
		Steps: []report.Step{
			{Name: "steady", StartMs: 900, EndMs: 2500},
			{Name: "upgrade", StartMs: 2500, EndMs: 4000},
		},
		Locks: []lockwatch.Sample{{TimeMs: 2700, AppWaiting: 2, AppMaxWaitMs: 120, OtherWaiting: 1, OtherMaxWaitMs: 300}},
		Migrations: []report.StepLog{{Step: "upgrade", Events: []report.Event{
			{"msg": "migrate_start"},
			{"msg": "migration_applied", "version": 2.0, "file": "00002_expand_split_name.sql", "duration_ms": 12.5, "attempts": 1.0},
		}}},
		Backfill: []report.Event{{"msg": "backfill_done", "rows": 1000.0, "batches": 10.0, "batch_size": 100.0,
			"duration_ms": 2000.0, "rows_per_sec": 500.0, "max_batch_ms": 7.5, "retries": 0.0, "passes": 1.0, "pause_ms": 100.0}},
		Checks:     []check.Report{{Step: "steady", Phase: "baseline", Rows: 10, SeedChecksum: "abcdef0123456789"}},
		K6Requests: 6, SeedRows: 8, FinalRows: 10, SLOp95Ms: 50,
	}
}

func TestBuildPasses(t *testing.T) {
	res := report.Build(inputs(t))
	if !res.OK() {
		t.Fatalf("gates = %+v", res.Gates)
	}
	if len(res.Steps) != 2 || res.Steps[0].Latency.Requests != 3 || res.Steps[1].Latency.Requests != 3 {
		t.Fatalf("steps = %+v", res.Steps)
	}
	up := res.Steps[1]
	if up.Latency.P95 != 9 || up.AppMaxLockMs != 120 || up.OtherMaxLockMs != 300 || up.AppLockSamples != 1 {
		t.Errorf("upgrade step = %+v", up)
	}
	if res.Steps[0].Latency.Served["v1"] != 1 {
		t.Errorf("served = %v", res.Steps[0].Latency.Served)
	}
	if res.Total.Writes != 2 || res.Total.Reads != 4 || res.Total.RPS <= 0 {
		t.Errorf("total = %+v", res.Total)
	}
	if len(res.Migrations) != 1 || res.Migrations[0].DurationMs != 12.5 || res.Backfill == nil || res.Backfill.RowsPerSec != 500 {
		t.Errorf("migrations=%+v backfill=%+v", res.Migrations, res.Backfill)
	}

	var md bytes.Buffer
	if err := res.Markdown(&md); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"| steady |", "120 ms", "`00002_expand_split_name.sql`", "500 rows/s", "`abcdef012345`", "| zero failed requests | pass |", "v1 100%"} {
		if !strings.Contains(md.String(), want) {
			t.Errorf("markdown lacks %q:\n%s", want, md.String())
		}
	}
}

func TestBuildFails(t *testing.T) {
	in := inputs(t)
	in.Requests[3].OK, in.Requests[3].Status = false, 500
	in.SLOp95Ms = 3
	in.K6Dropped = 2
	in.FinalRows = 11
	in.Checks[0].Problems = []string{"broken"}
	res := report.Build(in)
	if res.OK() {
		t.Fatal("gates passed on a failing run")
	}
	failed := 0
	for _, g := range res.Gates {
		if !g.OK {
			failed++
		}
	}
	if failed != 6 {
		t.Errorf("failed gates = %d, want 6: %+v", failed, res.Gates)
	}
	var md bytes.Buffer
	_ = res.Markdown(&md)
	if !strings.Contains(md.String(), "**FAIL**") {
		t.Error("markdown does not show failures")
	}
	if (report.Result{}).OK() {
		t.Error("an empty result must not pass")
	}
}

func TestParse(t *testing.T) {
	if _, err := report.ParseRequests(strings.NewReader("R,1,read,200\n")); err == nil {
		t.Error("short line: want error")
	}
	if _, err := report.ParseRequests(strings.NewReader("R,x,read,200,1,1,v1\n")); err == nil {
		t.Error("bad number: want error")
	}
	steps, err := report.ParseJSONLines[report.Step](strings.NewReader("noise\n{\"step\":\"a\",\"start_ms\":1,\"end_ms\":2}\n\n"))
	if err != nil || len(steps) != 1 || steps[0].Name != "a" {
		t.Errorf("steps = %+v, %v", steps, err)
	}
	if _, err := report.ParseJSONLines[report.Step](strings.NewReader("{broken\n")); err == nil {
		t.Error("broken JSON: want error")
	}
	e := report.Event{"msg": "x", "n": 2.0, "s": "y"}
	if e.Msg() != "x" || e.Num("n") != 2 || e.Str("s") != "y" || e.Num("missing") != 0 {
		t.Errorf("event accessors broken: %v", e)
	}
}

func TestPercentile(t *testing.T) {
	d := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	for p, want := range map[float64]float64{50: 5, 95: 10, 99: 10, 10: 1, 0: 1} {
		if got := report.Percentile(d, p); got != want {
			t.Errorf("p%g = %g, want %g", p, got, want)
		}
	}
	if report.Percentile(nil, 50) != 0 {
		t.Error("empty input")
	}
	if got := report.ServedString(map[string]float64{"v2": 0.25, "v1": 0.75}); got != "v1 75% · v2 25%" {
		t.Errorf("ServedString = %q", got)
	}
	if report.ServedString(nil) != "-" {
		t.Error("empty ServedString")
	}
}
