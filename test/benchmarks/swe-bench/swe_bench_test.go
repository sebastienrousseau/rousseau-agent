//go:build bench

// SWE-Bench Verified benchmark runner.
//
// Runs rousseau-agent against the 500-task Verified subset. Each task
// is: a repository at a known commit + an issue description +
// FAIL_TO_PASS / PASS_TO_PASS test ID lists. Grading is "generate a
// patch, apply it, run the test suite, check the pass lists match."
//
// This file is scaffolded: task discovery + result reporting are
// wired up; the actual per-task grading is marked TODO and returns
// "skipped" so the harness runs end-to-end before we plug in the
// SWE-Bench-specific patch/apply/test logic.
//
// Build with: go test -tags bench -timeout 6h ./test/benchmarks/swe-bench/...
package swebench_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/test/benchmarks/common"
)

// TestSWEBenchVerified runs the Verified subset. Skips cleanly when
// the corpus isn't checked out; caps to ROUSSEAU_BENCH_LIMIT when set.
func TestSWEBenchVerified(t *testing.T) {
	ok, corpusDir, err := common.CorpusExists("SWE-bench")
	if err != nil {
		t.Fatalf("check corpus: %v", err)
	}
	if !ok {
		t.Skipf("SWE-bench corpus not present at %s — see test/benchmarks/README.md", corpusDir)
	}

	tasks, err := discoverVerifiedTasks(corpusDir)
	if err != nil {
		t.Fatalf("discover tasks: %v", err)
	}
	if limit := common.Limit(); limit > 0 && limit < len(tasks) {
		tasks = tasks[:limit]
	}

	report := common.Report{
		Benchmark:       "swe-bench",
		RousseauVersion: rousseauVersion(),
		Model:           common.Model(),
		StartedAt:       time.Now().UTC(),
		PerTask:         make([]common.TaskOutcome, 0, len(tasks)),
	}
	report.CorpusCommit = corpusCommit(t, corpusDir)

	for _, taskID := range tasks {
		out := runOne(t, corpusDir, taskID)
		report.PerTask = append(report.PerTask, out)
	}

	report.EndedAt = time.Now().UTC()
	report.SummariseOutcomes()

	path, err := common.WriteReport(report)
	if err != nil {
		t.Fatalf("write report: %v", err)
	}
	t.Logf("SWE-Bench Verified: %d/%d passed (%.1f%%) — %s",
		report.TasksPassed, report.TasksTotal, report.PassRate*100, path)
}

// discoverVerifiedTasks reads the Verified subset's task metadata from
// the corpus. Format: SWE-bench/swebench/harness/test_spec/verified.json
// contains an array of task_ids.
//
// TODO: adjust path when the actual corpus layout is confirmed after
// the first checkout. The current fallback treats the corpus as
// containing a `verified/tasks/*.json` layout.
func discoverVerifiedTasks(corpusDir string) ([]string, error) {
	// Primary: the maintained verified-set JSON file.
	primary := filepath.Join(corpusDir, "swebench", "harness", "test_spec", "verified.json")
	if data, err := os.ReadFile(primary); err == nil { //nolint:gosec // operator-provided path
		var payload struct {
			Tasks []struct {
				InstanceID string `json:"instance_id"`
			} `json:"tasks"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return nil, fmt.Errorf("parse %s: %w", primary, err)
		}
		out := make([]string, len(payload.Tasks))
		for i, t := range payload.Tasks {
			out[i] = t.InstanceID
		}
		return out, nil
	}

	// Fallback: enumerate every JSON task file under verified/tasks/.
	fallbackDir := filepath.Join(corpusDir, "verified", "tasks")
	entries, err := os.ReadDir(fallbackDir)
	if err != nil {
		return nil, fmt.Errorf("neither %s nor %s is readable: %w",
			primary, fallbackDir, err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		out = append(out, name[:len(name)-len(".json")])
	}
	return out, nil
}

// runOne executes rousseau-agent against a single SWE-Bench task and
// grades the outcome.
//
// TODO: fill in the actual grading. Sketch:
//  1. Read the task spec (base_commit, problem_statement,
//     FAIL_TO_PASS, PASS_TO_PASS) from the corpus.
//  2. Materialise the repo at base_commit in a tmpdir.
//  3. Invoke rousseau (either via `rousseau chat --json` with a
//     hard-coded prompt, or via the pkg/agent library) with the
//     problem_statement.
//  4. Capture the diff rousseau produced.
//  5. Apply the diff, run the project's test suite.
//  6. Compare pass/fail lists to FAIL_TO_PASS / PASS_TO_PASS.
//  7. Track cost by summing rousseau's per-turn usage.
func runOne(t *testing.T, corpusDir, taskID string) common.TaskOutcome {
	t.Helper()
	_ = corpusDir // silence unused until grading is wired
	start := time.Now()
	return common.TaskOutcome{
		TaskID:      taskID,
		Outcome:     "skipped",
		Reason:      "swe-bench grading not yet implemented (see TODO in runOne)",
		DurationSec: time.Since(start).Seconds(),
	}
}

// rousseauVersion returns whatever version string the local rousseau
// binary reports, or "dev" when we can't resolve one.
func rousseauVersion() string {
	// TODO: shell out to `rousseau version --format=json` once that
	// output shape is stable, or read via `pkg/agent`'s version const
	// (once one exists).
	return "dev"
}

// corpusCommit returns the corpus repo's HEAD commit for provenance
// tracking. Falls back to "" on any error.
func corpusCommit(t *testing.T, corpusDir string) string {
	t.Helper()
	head := filepath.Join(corpusDir, ".git", "HEAD")
	data, err := os.ReadFile(head)
	if err != nil {
		return ""
	}
	// Direct-hash HEAD or symbolic ref — handle both cheaply.
	line := string(data)
	if len(line) < 4 {
		return ""
	}
	if line[:4] != "ref:" {
		if len(line) >= 40 {
			return line[:40]
		}
		return ""
	}
	// symbolic: resolve one hop.
	refPath := filepath.Join(corpusDir, ".git", filepath.FromSlash(trimRefLine(line[5:])))
	rd, err := os.ReadFile(refPath)
	if err != nil {
		return ""
	}
	rs := string(rd)
	if len(rs) < 40 {
		return ""
	}
	return rs[:40]
}

func trimRefLine(s string) string {
	for i, r := range s {
		if r == '\n' || r == '\r' {
			return s[:i]
		}
	}
	return s
}

// Sanity-check that the imports and types line up. Not a real test.
func TestHarnessCompiles(t *testing.T) {
	if _, err := common.BenchDir(); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("BenchDir failed unexpectedly: %v", err)
	}
}
