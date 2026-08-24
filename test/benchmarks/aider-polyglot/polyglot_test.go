//go:build bench

// Aider Polyglot benchmark runner (225 tasks × 6 languages: C++, Go,
// Java, JavaScript, Python, Rust).
//
// Each task = a project directory + a task specification (english
// prompt) + a test command. Grading: apply the model's changes, run
// the test command, check exit status.
//
// This file is scaffolded — task discovery + result reporting are
// wired up; the per-task apply/test grading is marked TODO.
//
// Build with: go test -tags bench -timeout 6h ./test/benchmarks/aider-polyglot/...
package polyglot_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/test/benchmarks/common"
)

// TestAiderPolyglot runs every task in the Aider Polyglot corpus.
// Skips cleanly when the corpus isn't present.
func TestAiderPolyglot(t *testing.T) {
	ok, corpusDir, err := common.CorpusExists("polyglot-benchmark")
	if err != nil {
		t.Fatalf("check corpus: %v", err)
	}
	if !ok {
		t.Skipf("polyglot-benchmark corpus not present at %s — see test/benchmarks/README.md", corpusDir)
	}

	tasks, err := discoverTasks(corpusDir)
	if err != nil {
		t.Fatalf("discover tasks: %v", err)
	}
	if limit := common.Limit(); limit > 0 && limit < len(tasks) {
		tasks = tasks[:limit]
	}

	report := common.Report{
		Benchmark:       "aider-polyglot",
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
	t.Logf("Aider Polyglot: %d/%d passed (%.1f%%) — %s",
		report.TasksPassed, report.TasksTotal, report.PassRate*100, path)
}

// discoverTasks enumerates the corpus's per-language task directories.
// The Polyglot corpus layout is <lang>/<task_name>/ containing the
// exercise files and a hidden test spec.
//
// TODO: refine once the corpus is checked out and the exact layout
// confirmed. Current implementation lists every subdirectory two
// levels deep (lang/task).
func discoverTasks(corpusDir string) ([]string, error) {
	langs, err := os.ReadDir(corpusDir)
	if err != nil {
		return nil, err
	}
	var tasks []string
	for _, lang := range langs {
		if !lang.IsDir() || lang.Name()[0] == '.' {
			continue
		}
		langDir := filepath.Join(corpusDir, lang.Name())
		entries, err := os.ReadDir(langDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || e.Name()[0] == '.' {
				continue
			}
			tasks = append(tasks, lang.Name()+"/"+e.Name())
		}
	}
	sort.Strings(tasks)
	return tasks, nil
}

// runOne executes rousseau against one Polyglot task and grades it.
//
// TODO: implementation sketch:
//  1. Copy the task dir to a tmpdir.
//  2. Load the task's prompt (typically <task>/instructions.md or
//     <task>/.docs/instructions.md).
//  3. Invoke rousseau with the prompt + --add-dir <tmpdir>.
//  4. Once rousseau reports end_turn, run the task's test command
//     (usually specified in the language-specific harness — e.g.
//     `go test ./...` for Go, `pytest` for Python).
//  5. Task passes if the test command exits 0.
func runOne(t *testing.T, corpusDir, taskID string) common.TaskOutcome {
	t.Helper()
	_ = corpusDir
	start := time.Now()
	return common.TaskOutcome{
		TaskID:      taskID,
		Outcome:     "skipped",
		Reason:      "polyglot grading not yet implemented (see TODO in runOne)",
		DurationSec: time.Since(start).Seconds(),
	}
}
