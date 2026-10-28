package report

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
)

// Latency holds request statistics for a window.
type Latency struct {
	Requests int64   `json:"requests"`
	Reads    int64   `json:"reads"`
	Writes   int64   `json:"writes"`
	Errors   int64   `json:"errors"`
	P50      float64 `json:"p50_ms"`
	P95      float64 `json:"p95_ms"`
	P99      float64 `json:"p99_ms"`
	Max      float64 `json:"max_ms"`
	RPS      float64 `json:"rps"`
	// Served maps app version -> share of responses (0..1).
	Served map[string]float64 `json:"served_by"`
}

// StepResult is one row of the results table.
type StepResult struct {
	Step
	DurationS       float64 `json:"duration_s"`
	Latency         Latency `json:"latency"`
	AppMaxLockMs    float64 `json:"app_max_lock_wait_ms"`
	OtherMaxLockMs  float64 `json:"other_max_lock_wait_ms"`
	AppLockSamples  int     `json:"app_lock_wait_samples"`
	SLOp95Satisfied bool    `json:"slo_p95_ok"`
}

// Migration is one applied migration.
type Migration struct {
	Step       string  `json:"step"`
	Version    int64   `json:"version"`
	File       string  `json:"file"`
	DurationMs float64 `json:"duration_ms"`
	Attempts   int     `json:"attempts"`
}

// Backfill summarises the backfill job.
type Backfill struct {
	Rows       int64   `json:"rows"`
	Batches    int64   `json:"batches"`
	BatchSize  int64   `json:"batch_size"`
	PauseMs    int64   `json:"pause_ms"`
	DurationMs int64   `json:"duration_ms"`
	RowsPerSec int64   `json:"rows_per_sec"`
	MaxBatchMs float64 `json:"max_batch_ms"`
	Retries    int64   `json:"lock_retries"`
	Passes     int64   `json:"passes"`
}

// Gate is one pass/fail criterion.
type Gate struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Result is the full report.
type Result struct {
	Steps      []StepResult `json:"steps"`
	Total      Latency      `json:"total"`
	Migrations []Migration  `json:"migrations"`
	Backfill   *Backfill    `json:"backfill,omitempty"`
	Checks     []CheckRow   `json:"checks"`
	Gates      []Gate       `json:"gates"`
	SLOp95Ms   float64      `json:"slo_p95_ms"`
	K6Dropped  int64        `json:"k6_dropped_iterations"`
}

// CheckRow is a consistency check result.
type CheckRow struct {
	Step          string `json:"step"`
	Phase         string `json:"phase"`
	Rows          int64  `json:"rows"`
	SeedChecksum  string `json:"seed_checksum"`
	Inconsistent  int64  `json:"inconsistent"`
	NotBackfilled int64  `json:"not_backfilled"`
	OK            bool   `json:"ok"`
	Problems      string `json:"problems,omitempty"`
}

// OK reports whether every gate passed.
func (r Result) OK() bool {
	for _, g := range r.Gates {
		if !g.OK {
			return false
		}
	}
	return len(r.Gates) > 0
}

// Build computes the report and evaluates the gates.
func Build(in Inputs) Result {
	res := Result{SLOp95Ms: in.SLOp95Ms, K6Dropped: in.K6Dropped}
	res.Total = latency(in.Requests)

	allSLO := true
	for _, s := range in.Steps {
		var reqs []Request
		for _, q := range in.Requests {
			if q.TimeMs >= s.StartMs && q.TimeMs < s.EndMs {
				reqs = append(reqs, q)
			}
		}
		sr := StepResult{Step: s, DurationS: float64(s.EndMs-s.StartMs) / 1000, Latency: latency(reqs)}
		for _, l := range in.Locks {
			if l.TimeMs >= s.StartMs && l.TimeMs < s.EndMs {
				sr.AppMaxLockMs = math.Max(sr.AppMaxLockMs, l.AppMaxWaitMs)
				sr.OtherMaxLockMs = math.Max(sr.OtherMaxLockMs, l.OtherMaxWaitMs)
				if l.AppWaiting > 0 {
					sr.AppLockSamples++
				}
			}
		}
		sr.SLOp95Satisfied = sr.Latency.Requests > 0 && sr.Latency.P95 <= in.SLOp95Ms
		allSLO = allSLO && sr.SLOp95Satisfied
		res.Steps = append(res.Steps, sr)
	}

	for _, sl := range in.Migrations {
		for _, e := range sl.Events {
			if e.Msg() != "migration_applied" {
				continue
			}
			res.Migrations = append(res.Migrations, Migration{
				Step: sl.Step, Version: int64(e.Num("version")), File: e.Str("file"),
				DurationMs: e.Num("duration_ms"), Attempts: int(e.Num("attempts")),
			})
		}
	}
	for _, e := range in.Backfill {
		if e.Msg() == "backfill_done" {
			res.Backfill = &Backfill{
				Rows: int64(e.Num("rows")), Batches: int64(e.Num("batches")), BatchSize: int64(e.Num("batch_size")),
				PauseMs: int64(e.Num("pause_ms")), DurationMs: int64(e.Num("duration_ms")),
				RowsPerSec: int64(e.Num("rows_per_sec")), MaxBatchMs: e.Num("max_batch_ms"),
				Retries: int64(e.Num("retries")), Passes: int64(e.Num("passes")),
			}
		}
	}

	checksOK := len(in.Checks) > 0
	for _, c := range in.Checks {
		res.Checks = append(res.Checks, CheckRow{
			Step: c.Step, Phase: c.Phase, Rows: c.Rows, SeedChecksum: c.SeedChecksum,
			Inconsistent: c.Inconsistent, NotBackfilled: c.NotBackfilled, OK: c.OK(),
			Problems: strings.Join(c.Problems, "; "),
		})
		checksOK = checksOK && c.OK()
	}

	var acked int64
	for _, q := range in.Requests {
		if q.Op == "write" && q.OK {
			acked++
		}
	}
	res.Gates = []Gate{
		{"zero failed requests", res.Total.Errors == 0 && res.Total.Requests > 0,
			fmt.Sprintf("%d failed of %d requests", res.Total.Errors, res.Total.Requests)},
		{fmt.Sprintf("p95 <= %g ms in every step", in.SLOp95Ms), allSLO && len(in.Steps) > 0, worstP95(res.Steps)},
		{"consistency check after every step", checksOK, fmt.Sprintf("%d checks", len(in.Checks))},
		{"request log matches k6 totals", in.K6Requests == res.Total.Requests && in.K6Errors == res.Total.Errors,
			fmt.Sprintf("log %d/%d errors, k6 %d/%d errors", res.Total.Requests, res.Total.Errors, in.K6Requests, in.K6Errors)},
		{"no dropped iterations (load generator kept up)", in.K6Dropped == 0, fmt.Sprintf("%d dropped", in.K6Dropped)},
	}
	if in.FinalRows > 0 {
		want := in.SeedRows + acked
		res.Gates = append(res.Gates, Gate{"every acknowledged write is stored", in.FinalRows == want,
			fmt.Sprintf("%d rows = %d seeded + %d acknowledged writes (want %d)", in.FinalRows, in.SeedRows, acked, want)})
	}
	return res
}

func worstP95(steps []StepResult) string {
	worst, name := 0.0, ""
	for _, s := range steps {
		if s.Latency.P95 >= worst {
			worst, name = s.Latency.P95, s.Name
		}
	}
	return fmt.Sprintf("worst p95 %.1f ms (%s)", worst, name)
}

func latency(reqs []Request) Latency {
	l := Latency{Served: map[string]float64{}}
	if len(reqs) == 0 {
		return l
	}
	d := make([]float64, 0, len(reqs))
	versions := map[string]int{}
	minT, maxT := reqs[0].TimeMs, reqs[0].TimeMs
	for _, q := range reqs {
		l.Requests++
		switch q.Op {
		case "read":
			l.Reads++
		case "write":
			l.Writes++
		}
		if !q.OK {
			l.Errors++
		}
		d = append(d, q.Duration)
		if q.Version != "" && q.Version != "-" {
			versions[q.Version]++
		}
		minT, maxT = min(minT, q.TimeMs), max(maxT, q.TimeMs)
	}
	sort.Float64s(d)
	l.P50, l.P95, l.P99, l.Max = Percentile(d, 50), Percentile(d, 95), Percentile(d, 99), d[len(d)-1]
	if span := float64(maxT-minT) / 1000; span > 0 {
		l.RPS = float64(l.Requests) / span
	}
	var served int
	for _, n := range versions {
		served += n
	}
	for v, n := range versions {
		l.Served[v] = float64(n) / float64(served)
	}
	return l
}

// Percentile returns the nearest-rank percentile of sorted values.
func Percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	rank = min(max(rank, 1), len(sorted))
	return sorted[rank-1]
}

// ServedString renders the version mix, e.g. "v1 40% · v2 60%".
func ServedString(m map[string]float64) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %.0f%%", k, m[k]*100))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " · ")
}
