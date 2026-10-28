// Package report turns the raw e2e artifacts (k6 request log, step timeline,
// lock-wait samples, migrator/backfill logs, consistency checks) into the
// per-step results table and enforces the gates.
package report

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/check"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/lockwatch"
)

// Request is one API request recorded by the load test.
type Request struct {
	TimeMs   int64
	Op       string // "read" or "write"
	Status   int
	Duration float64 // ms
	OK       bool    // status and body checks passed
	Version  string  // X-App-Version of the pod that answered
}

// Step is one window of the rollout timeline.
type Step struct {
	Name    string `json:"step"`
	StartMs int64  `json:"start_ms"`
	EndMs   int64  `json:"end_ms"`
	Note    string `json:"note,omitempty"`
}

// Event is a structured log line from the migrator or the backfill.
type Event map[string]any

// Msg returns the slog message of the event.
func (e Event) Msg() string { s, _ := e["msg"].(string); return s }

// Num returns a numeric field (0 if missing).
func (e Event) Num(k string) float64 { f, _ := e[k].(float64); return f }

// Str returns a string field ("" if missing).
func (e Event) Str(k string) string { s, _ := e[k].(string); return s }

// ParseRequests reads the k6 request log. Lines look like
// "R,<unix_ms>,<op>,<status>,<duration_ms>,<ok>,<version>"; anything else
// (k6's own log lines) is ignored.
func ParseRequests(r io.Reader) ([]Request, error) {
	var out []Request
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		t := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(t, "R,") {
			continue
		}
		f := strings.Split(t, ",")
		if len(f) != 7 {
			return nil, fmt.Errorf("request log line %d: want 7 fields, got %d", line, len(f))
		}
		ts, err1 := strconv.ParseInt(f[1], 10, 64)
		st, err2 := strconv.Atoi(f[3])
		d, err3 := strconv.ParseFloat(f[4], 64)
		if err1 != nil || err2 != nil || err3 != nil {
			return nil, fmt.Errorf("request log line %d: malformed %q", line, t)
		}
		out = append(out, Request{TimeMs: ts, Op: f[2], Status: st, Duration: d, OK: f[5] == "1", Version: f[6]})
	}
	return out, sc.Err()
}

// ParseJSONLines decodes one JSON value per non-empty line into T. Lines that
// are not JSON objects (for example plain-text log output) are skipped.
func ParseJSONLines[T any](r io.Reader) ([]T, error) {
	var out []T
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		t := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(t, "{") {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(t), &v); err != nil {
			return nil, fmt.Errorf("decode %q: %w", t, err)
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

// StepLog holds the structured log of one Job run.
type StepLog struct {
	Step   string
	Events []Event
}

// Inputs are the parsed artifacts of one e2e run.
type Inputs struct {
	Requests   []Request
	Steps      []Step
	Locks      []lockwatch.Sample
	Migrations []StepLog // migrator log events, per release step
	Backfill   []Event
	Checks     []check.Report
	// Totals reported by k6 itself, to cross-check the request log.
	K6Requests int64
	K6Errors   int64
	K6Dropped  int64
	SeedRows   int64
	FinalRows  int64
	SLOp95Ms   float64
}
