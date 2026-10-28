package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/check"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/lockwatch"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/report"
)

// Files in the results directory, written by scripts/e2e.sh and the load test.
const (
	fileRequests  = "requests.log"
	fileTimeline  = "timeline.jsonl"
	fileLocks     = "lockwatch.jsonl"
	fileChecks    = "checks.jsonl"
	fileBackfill  = "backfill.log"
	fileK6Summary = "k6-summary.json"
	dirJobs       = "jobs" // one migrator log per release step, first line "# step: <name>"
)

type k6Summary struct {
	Requests int64 `json:"api_requests"`
	Errors   int64 `json:"api_errors"`
	Dropped  int64 `json:"dropped_iterations"`
}

func runReport(args []string, env Env) error {
	fs := newFlags("report", env)
	dir := fs.String("dir", "e2e-results", "results directory")
	slo := fs.Float64("slo-p95-ms", 100, "p95 latency SLO per step, in ms")
	seedRows := fs.Int64("seed-rows", 0, "number of seeded rows")
	finalRows := fs.Int64("final-rows", 0, "row count after the load stopped (0: skip the write-durability gate)")
	md := fs.String("markdown", "", "write Markdown here (default stdout)")
	js := fs.String("json", "", "write JSON here")
	noGate := fs.Bool("no-gate", false, "always exit 0 (demo runs that expect failures)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	in, err := loadInputs(*dir)
	if err != nil {
		return err
	}
	in.SLOp95Ms, in.SeedRows, in.FinalRows = *slo, *seedRows, *finalRows
	res := report.Build(in)

	w := env.Stdout
	if *md != "" {
		f, err := os.Create(*md)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		w = f
	}
	if err := res.Markdown(w); err != nil {
		return err
	}
	if *js != "" {
		b, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*js, b, 0o600); err != nil {
			return err
		}
	}
	if !res.OK() && !*noGate {
		var failed []string
		for _, g := range res.Gates {
			if !g.OK {
				failed = append(failed, g.Name+" ("+g.Detail+")")
			}
		}
		return fmt.Errorf("gates failed: %s", strings.Join(failed, "; "))
	}
	return nil
}

func loadInputs(dir string) (report.Inputs, error) {
	var in report.Inputs
	var err error
	if in.Requests, err = readWith(filepath.Join(dir, fileRequests), report.ParseRequests); err != nil {
		return in, err
	}
	if in.Steps, err = readWith(filepath.Join(dir, fileTimeline), report.ParseJSONLines[report.Step]); err != nil {
		return in, err
	}
	if in.Checks, err = readWith(filepath.Join(dir, fileChecks), report.ParseJSONLines[check.Report]); err != nil {
		return in, err
	}
	if in.Locks, err = readOptional(filepath.Join(dir, fileLocks), report.ParseJSONLines[lockwatch.Sample]); err != nil {
		return in, err
	}
	if in.Backfill, err = readOptional(filepath.Join(dir, fileBackfill), report.ParseJSONLines[report.Event]); err != nil {
		return in, err
	}
	b, err := os.ReadFile(filepath.Join(dir, fileK6Summary))
	if err != nil {
		return in, err
	}
	var s k6Summary
	if err := json.Unmarshal(b, &s); err != nil {
		return in, fmt.Errorf("%s: %w", fileK6Summary, err)
	}
	in.K6Requests, in.K6Errors, in.K6Dropped = s.Requests, s.Errors, s.Dropped

	jobs, err := filepath.Glob(filepath.Join(dir, dirJobs, "*.log"))
	if err != nil {
		return in, err
	}
	sort.Strings(jobs)
	for _, p := range jobs {
		sl, err := readStepLog(p)
		if err != nil {
			return in, err
		}
		in.Migrations = append(in.Migrations, sl)
	}
	return in, nil
}

func readStepLog(path string) (report.StepLog, error) {
	f, err := os.Open(path)
	if err != nil {
		return report.StepLog{}, err
	}
	defer func() { _ = f.Close() }()
	br := bufio.NewReader(f)
	first, err := br.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return report.StepLog{}, err
	}
	sl := report.StepLog{Step: strings.TrimSpace(strings.TrimPrefix(first, "# step:"))}
	if !strings.HasPrefix(first, "# step:") {
		sl.Step = strings.TrimSuffix(filepath.Base(path), ".log")
		br = bufio.NewReader(io.MultiReader(strings.NewReader(first), br))
	}
	sl.Events, err = report.ParseJSONLines[report.Event](br)
	return sl, err
}

func readWith[T any](path string, parse func(io.Reader) ([]T, error)) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	v, err := parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return v, nil
}

func readOptional[T any](path string, parse func(io.Reader) ([]T, error)) ([]T, error) {
	v, err := readWith(path, parse)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return v, err
}
