//go:build bench

// Terminal-Bench runner: long-horizon shell work (agent drives an
// interactive terminal to a target end state).
//
// Each task = a starting environment (usually a Docker image or a
// scripted host) + a natural-language instruction + a verifier
// script or expected end-state snapshot. Grading: run the verifier
// after rousseau reports end_turn.
//
// This file is scaffolded — task discovery + result reporting are
// wired up; the per-task run/verify logic is TODO.
//
// Build with: go test -tags bench -timeout 6h ./test/benchmarks/terminal-bench/...
package terminalbench_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/test/benchmarks/common"
)

// TestTerminalBench runs every task in the Terminal-Bench corpus.
func TestTerminalBench(t *testing.T) {
	ok, corpusDir, err := common.CorpusExists("terminal-bench")
	if err != nil {
		t.Fatalf("check corpus: %v", err)
	}
	if !ok {
		t.Skipf("terminal-bench corpus not present at %s — see test/benchmarks/README.md", corpusDir)
	}

	tasks, err := discoverTasks(corpusDir)
	if err != nil {
		t.Fatalf("discover tasks: %v", err)
	}
	if limit := common.Limit(); limit > 0 && limit < len(tasks) {
		tasks = tasks[:limit]
	}

	report := common.Report{
		Benchmark:       "terminal-bench",
		RousseauVersion: "dev",
		Model:           common.Model(),
		StartedAt:       time.Now().UTC(),
		PerTask:         make([]common.TaskOutcome, 0, len(tasks)),
	}

	for _, taskID := range tasks {
		report.PerTask = append(report.PerTask, runOne(t, corpusDir, taskID))
	}

	report.EndedAt = time.Now().UTC()
	report.SummariseOutcomes()

	path, err := common.WriteReport(report)
	if err != nil {
		t.Fatalf("write report: %v", err)
	}
	t.Logf("Terminal-Bench: %d/%d passed (%.1f%%) — %s",
		report.TasksPassed, report.TasksTotal, report.PassRate*100, path)
}

// discoverTasks enumerates tasks in the corpus's `tasks/` directory
// (each task is a directory).
//
// TODO: refine based on actual corpus layout after checkout.
func discoverTasks(corpusDir string) ([]string, error) {
	tasksDir := filepath.Join(corpusDir, "tasks")
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return nil, err
	}
	var tasks []string
	for _, e := range entries {
		if !e.IsDir() || e.Name()[0] == '.' {
			continue
		}
		tasks = append(tasks, e.Name())
	}
	sort.Strings(tasks)
	return tasks, nil
}

// runOne executes rousseau against one Terminal-Bench task.
//
// TODO: implementation sketch:
//  1. Spin up the task's start environment (typically `docker compose
//     up` on a per-task compose file).
//  2. Feed the instruction to rousseau with `--add-dir /workspace`
//     pointing into the container's mounted workspace.
//  3. On end_turn, run the task's verifier script (usually
//     `verify.sh` inside the container).
//  4. Task passes if verifier exits 0.
//  5. Tear down the container.
func runOne(t *testing.T, corpusDir, taskID string) common.TaskOutcome {
	t.Helper()
	_ = corpusDir
	start := time.Now()
	return common.TaskOutcome{
		TaskID:      taskID,
		Outcome:     "skipped",
		Reason:      "terminal-bench grading not yet implemented (see TODO in runOne)",
		DurationSec: time.Since(start).Seconds(),
	}
}
