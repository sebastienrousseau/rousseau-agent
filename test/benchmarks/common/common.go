// Package common holds the shared runner utilities used by every
// benchmark subpackage under test/benchmarks/*. Kept small on purpose:
// each benchmark's specifics (task discovery, grading) live in its own
// package.
//
// This file is intentionally NOT behind the `bench` build tag so it
// remains type-checked by the default `go build ./...` even when the
// runners themselves are excluded from the standard test suite.
package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Runner is the interface every benchmark subpackage implements.
// It's here so the CI orchestration can treat all three (SWE-Bench,
// Aider Polyglot, Terminal-Bench) uniformly.
type Runner interface {
	// Name is the short identifier ("swe-bench", "aider-polyglot", etc.).
	Name() string
	// Discover returns the task IDs the runner would process given the
	// current corpus location and any ROUSSEAU_BENCH_* limits. Empty
	// slice means "corpus not present; skip this benchmark".
	Discover() ([]string, error)
	// RunOne processes a single task and returns its outcome.
	RunOne(taskID string) TaskOutcome
}

// TaskOutcome captures per-task results emitted to the results JSON.
type TaskOutcome struct {
	TaskID      string        `json:"task_id"`
	Outcome     string        `json:"outcome"` // "passed" | "failed" | "error" | "skipped"
	Reason      string        `json:"reason,omitempty"`
	DurationSec float64       `json:"duration_sec"`
	CostUSD     float64       `json:"cost_usd"`
	Duration    time.Duration `json:"-"`
}

// Report is the top-level shape written to test/benchmarks/results/.
type Report struct {
	Benchmark       string        `json:"benchmark"`
	CorpusCommit    string        `json:"corpus_commit,omitempty"`
	RousseauVersion string        `json:"rousseau_version"`
	Model           string        `json:"model"`
	StartedAt       time.Time     `json:"started_at"`
	EndedAt         time.Time     `json:"ended_at"`
	TasksTotal      int           `json:"tasks_total"`
	TasksPassed     int           `json:"tasks_passed"`
	TasksFailed     int           `json:"tasks_failed"`
	TasksSkipped    int           `json:"tasks_skipped"`
	PassRate        float64       `json:"pass_rate"`
	CostUSD         float64       `json:"cost_usd"`
	PerTask         []TaskOutcome `json:"per_task"`
}

// BenchDir returns the directory under which every benchmark corpus is
// expected to live. Defaults to ~/bench-corpora — override via
// ROUSSEAU_BENCH_DIR.
func BenchDir() (string, error) {
	if v := os.Getenv("ROUSSEAU_BENCH_DIR"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("common: resolve home dir: %w", err)
	}
	return filepath.Join(home, "bench-corpora"), nil
}

// CorpusExists reports whether the named corpus directory is present
// under BenchDir. Runners use this to skip cleanly when the operator
// hasn't cloned the corpus.
func CorpusExists(name string) (bool, string, error) {
	root, err := BenchDir()
	if err != nil {
		return false, "", err
	}
	full := filepath.Join(root, name)
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return false, full, nil
		}
		return false, full, err
	}
	return info.IsDir(), full, nil
}

// Limit returns the operator-set task cap from ROUSSEAU_BENCH_LIMIT, or
// 0 for "no cap". Runners use this for smoke passes.
func Limit() int {
	v := os.Getenv("ROUSSEAU_BENCH_LIMIT")
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// Model returns the LLM model to benchmark against. Defaults to
// claude-opus-4-6 (rousseau's default provider recommendation). Set
// ROUSSEAU_BENCH_MODEL for cheaper smoke runs (e.g. "haiku").
func Model() string {
	if v := os.Getenv("ROUSSEAU_BENCH_MODEL"); v != "" {
		return v
	}
	return "claude-opus-4-6"
}

// ResultsDir returns test/benchmarks/results/ under the current working
// directory (the caller must ensure it runs from the repo root). Creates
// the directory if absent.
func ResultsDir() (string, error) {
	dir := filepath.Join("test", "benchmarks", "results")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("common: mkdir %s: %w", dir, err)
	}
	return dir, nil
}

// WriteReport marshals r to JSON and writes it to
// test/benchmarks/results/<benchmark>-<UTC-date>.json.
func WriteReport(r Report) (string, error) {
	if r.Benchmark == "" {
		return "", errors.New("common: report missing Benchmark")
	}
	dir, err := ResultsDir()
	if err != nil {
		return "", err
	}
	// Compute pass rate + roll-up counters if the caller didn't.
	if r.PassRate == 0 && r.TasksTotal > 0 {
		r.PassRate = float64(r.TasksPassed) / float64(r.TasksTotal)
	}
	name := fmt.Sprintf("%s-%s.json", r.Benchmark, r.StartedAt.UTC().Format("2006-01-02T15-04-05Z"))
	path := filepath.Join(dir, name)
	blob, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", fmt.Errorf("common: marshal report: %w", err)
	}
	if err := os.WriteFile(path, blob, 0o644); err != nil { //nolint:gosec // reports are non-sensitive
		return "", fmt.Errorf("common: write %s: %w", path, err)
	}
	return path, nil
}

// SummariseOutcomes updates the roll-up counters in place from PerTask.
func (r *Report) SummariseOutcomes() {
	r.TasksTotal = len(r.PerTask)
	r.TasksPassed = 0
	r.TasksFailed = 0
	r.TasksSkipped = 0
	for _, t := range r.PerTask {
		switch t.Outcome {
		case "passed":
			r.TasksPassed++
		case "failed", "error":
			r.TasksFailed++
		case "skipped":
			r.TasksSkipped++
		}
		r.CostUSD += t.CostUSD
	}
	if r.TasksTotal > 0 {
		r.PassRate = float64(r.TasksPassed) / float64(r.TasksTotal)
	}
}
