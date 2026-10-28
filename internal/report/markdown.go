package report

import (
	"fmt"
	"io"
)

// Markdown writes the report as GitHub-flavoured Markdown.
func (r Result) Markdown(w io.Writer) error {
	p := &printer{w: w}
	p.f("### Load and latency per step\n\n")
	p.f("| Step | Duration | Requests (reads / writes) | Failed | p50 | p95 | p99 | max | Max lock wait (API queries) | Served by |\n")
	p.f("|---|---:|---:|---:|---:|---:|---:|---:|---:|---|\n")
	for _, s := range r.Steps {
		l := s.Latency
		p.f("| %s | %.0f s | %d (%d / %d) | %d | %.1f ms | %.1f ms | %.1f ms | %.1f ms | %s | %s |\n",
			s.Name, s.DurationS, l.Requests, l.Reads, l.Writes, l.Errors, l.P50, l.P95, l.P99, l.Max,
			lockCell(s.AppMaxLockMs), ServedString(l.Served))
	}
	t := r.Total
	p.f("| **whole run** | | **%d (%d / %d)** | **%d** | %.1f ms | %.1f ms | %.1f ms | %.1f ms | | |\n\n",
		t.Requests, t.Reads, t.Writes, t.Errors, t.P50, t.P95, t.P99, t.Max)
	p.f("Load: %.1f req/s average over the run. SLO gate: p95 <= %g ms per step. "+
		"Lock wait is sampled every 50 ms from `pg_stat_activity`; \"none\" means no API query was seen waiting on a lock.\n\n",
		t.RPS, r.SLOp95Ms)

	if len(r.Migrations) > 0 {
		p.f("### Migrations (Helm pre-upgrade hook Job)\n\n")
		p.f("| Release step | Version | Migration | Duration | Attempts |\n|---|---:|---|---:|---:|\n")
		for _, m := range r.Migrations {
			p.f("| %s | %d | `%s` | %.1f ms | %d |\n", m.Step, m.Version, m.File, m.DurationMs, m.Attempts)
		}
		p.f("\n")
	}

	if b := r.Backfill; b != nil {
		p.f("### Backfill (Kubernetes Job)\n\n")
		p.f("| Rows | Batches (size) | Throttle | Duration | Throughput | Slowest batch | Lock-timeout retries |\n")
		p.f("|---:|---:|---:|---:|---:|---:|---:|\n")
		p.f("| %d | %d (%d) | %d ms pause | %.1f s | %d rows/s | %.1f ms | %d |\n\n",
			b.Rows, b.Batches, b.BatchSize, b.PauseMs, float64(b.DurationMs)/1000, b.RowsPerSec, b.MaxBatchMs, b.Retries)
	}

	if len(r.Checks) > 0 {
		p.f("### Data consistency after each step\n\n")
		p.f("| After step | Schema phase | Rows | Seed rows checksum (md5) | name vs split mismatches | Not backfilled | Result |\n")
		p.f("|---|---|---:|---|---:|---:|---|\n")
		for _, c := range r.Checks {
			status := "pass"
			if !c.OK {
				status = "**FAIL**: " + c.Problems
			}
			p.f("| %s | %s | %d | `%s` | %d | %d | %s |\n",
				c.Step, c.Phase, c.Rows, short(c.SeedChecksum), c.Inconsistent, c.NotBackfilled, status)
		}
		p.f("\n")
	}

	p.f("### Gates\n\n| Gate | Result | Detail |\n|---|---|---|\n")
	for _, g := range r.Gates {
		status := "pass"
		if !g.OK {
			status = "**FAIL**"
		}
		p.f("| %s | %s | %s |\n", g.Name, status, g.Detail)
	}
	return p.err
}

func lockCell(ms float64) string {
	if ms == 0 {
		return "none"
	}
	return fmt.Sprintf("%.0f ms", ms)
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

type printer struct {
	w   io.Writer
	err error
}

func (p *printer) f(format string, args ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format, args...)
	}
}
