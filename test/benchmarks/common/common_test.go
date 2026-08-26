package common

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBenchDir_EnvOverrideWins(t *testing.T) {
	t.Setenv("ROUSSEAU_BENCH_DIR", "/corpora/elsewhere")
	got, err := BenchDir()
	require.NoError(t, err)
	assert.Equal(t, "/corpora/elsewhere", got)
}

func TestBenchDir_DefaultsUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ROUSSEAU_BENCH_DIR", "")
	t.Setenv("HOME", home)
	got, err := BenchDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, "bench-corpora"), got)
}

func TestBenchDir_UnresolvableHome(t *testing.T) {
	t.Setenv("ROUSSEAU_BENCH_DIR", "")
	// os.UserHomeDir reports an error when $HOME is unset on unix.
	require.NoError(t, os.Unsetenv("HOME"))
	t.Cleanup(func() { _ = os.Setenv("HOME", t.TempDir()) })

	got, err := BenchDir()
	require.Error(t, err)
	assert.Empty(t, got)
	assert.Contains(t, err.Error(), "resolve home dir")
}

func TestCorpusExists(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ROUSSEAU_BENCH_DIR", root)
	require.NoError(t, os.Mkdir(filepath.Join(root, "swe-bench"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "not-a-dir"), []byte("x"), 0o644))

	t.Run("present directory", func(t *testing.T) {
		ok, path, err := CorpusExists("swe-bench")
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, filepath.Join(root, "swe-bench"), path)
	})

	t.Run("absent is not an error", func(t *testing.T) {
		// Runners rely on this: a missing corpus means "skip cleanly",
		// not "fail the benchmark".
		ok, path, err := CorpusExists("aider-polyglot")
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Equal(t, filepath.Join(root, "aider-polyglot"), path)
	})

	t.Run("a file is not a corpus", func(t *testing.T) {
		ok, _, err := CorpusExists("not-a-dir")
		require.NoError(t, err)
		assert.False(t, ok, "a regular file must not count as a corpus directory")
	})
}

func TestCorpusExists_StatFailurePropagates(t *testing.T) {
	// A path component that is a regular file makes Stat fail with
	// ENOTDIR rather than IsNotExist, which must surface as an error.
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
	t.Setenv("ROUSSEAU_BENCH_DIR", blocker)

	ok, _, err := CorpusExists("anything")
	require.Error(t, err)
	assert.False(t, ok)
}

func TestCorpusExists_BenchDirFailurePropagates(t *testing.T) {
	t.Setenv("ROUSSEAU_BENCH_DIR", "")
	require.NoError(t, os.Unsetenv("HOME"))
	t.Cleanup(func() { _ = os.Setenv("HOME", t.TempDir()) })

	ok, path, err := CorpusExists("swe-bench")
	require.Error(t, err)
	assert.False(t, ok)
	assert.Empty(t, path)
}

func TestLimit(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		want      int
	}{
		{"unset means no cap", "", 0},
		{"positive value", "25", 25},
		{"zero", "0", 0},
		{"negative falls back to no cap", "-5", 0},
		{"garbage falls back to no cap", "many", 0},
		{"float falls back to no cap", "1.5", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ROUSSEAU_BENCH_LIMIT", tc.env)
			assert.Equal(t, tc.want, Limit())
		})
	}
}

func TestModel(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Setenv("ROUSSEAU_BENCH_MODEL", "")
		assert.Equal(t, "claude-opus-4-6", Model())
	})
	t.Run("override", func(t *testing.T) {
		t.Setenv("ROUSSEAU_BENCH_MODEL", "haiku")
		assert.Equal(t, "haiku", Model())
	})
}

func TestResultsDir_CreatesRelativeToCwd(t *testing.T) {
	t.Chdir(t.TempDir())
	dir, err := ResultsDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("test", "benchmarks", "results"), dir)
	info, statErr := os.Stat(dir)
	require.NoError(t, statErr)
	assert.True(t, info.IsDir())
}

func TestResultsDir_MkdirFailure(t *testing.T) {
	// A regular file where the directory needs to go makes MkdirAll fail.
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "test"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "test", "benchmarks"), []byte("x"), 0o644))
	t.Chdir(root)

	dir, err := ResultsDir()
	require.Error(t, err)
	assert.Empty(t, dir)
	assert.Contains(t, err.Error(), "mkdir")
}

func TestWriteReport_RoundTrips(t *testing.T) {
	t.Chdir(t.TempDir())
	started := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

	path, err := WriteReport(Report{
		Benchmark:   "swe-bench",
		Model:       "haiku",
		StartedAt:   started,
		TasksTotal:  4,
		TasksPassed: 3,
		PerTask:     []TaskOutcome{{TaskID: "t1", Outcome: "passed"}},
	})
	require.NoError(t, err)

	// The filename encodes the benchmark and the UTC start instant, so
	// concurrent runs of different benchmarks cannot collide.
	assert.Equal(t,
		filepath.Join("test", "benchmarks", "results", "swe-bench-2026-03-04T05-06-07Z.json"),
		path)

	blob, err := os.ReadFile(path) //nolint:gosec // path is test-controlled
	require.NoError(t, err)
	var got Report
	require.NoError(t, json.Unmarshal(blob, &got))
	assert.Equal(t, "swe-bench", got.Benchmark)
	assert.Equal(t, "haiku", got.Model)
	assert.InDelta(t, 0.75, got.PassRate, 1e-9, "pass rate is derived when the caller leaves it zero")
	require.Len(t, got.PerTask, 1)
	assert.Equal(t, "t1", got.PerTask[0].TaskID)
}

func TestWriteReport_KeepsCallerSuppliedPassRate(t *testing.T) {
	t.Chdir(t.TempDir())
	path, err := WriteReport(Report{
		Benchmark:   "aider-polyglot",
		StartedAt:   time.Unix(0, 0).UTC(),
		TasksTotal:  10,
		TasksPassed: 1,
		PassRate:    0.9, // deliberately inconsistent: caller wins
	})
	require.NoError(t, err)

	blob, err := os.ReadFile(path) //nolint:gosec // path is test-controlled
	require.NoError(t, err)
	var got Report
	require.NoError(t, json.Unmarshal(blob, &got))
	assert.InDelta(t, 0.9, got.PassRate, 1e-9)
}

func TestWriteReport_RequiresBenchmarkName(t *testing.T) {
	path, err := WriteReport(Report{})
	require.Error(t, err)
	assert.Empty(t, path)
	assert.Contains(t, err.Error(), "missing Benchmark")
}

func TestWriteReport_ResultsDirFailurePropagates(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "test"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "test", "benchmarks"), []byte("x"), 0o644))
	t.Chdir(root)

	path, err := WriteReport(Report{Benchmark: "swe-bench"})
	require.Error(t, err)
	assert.Empty(t, path)
}

func TestWriteReport_UnwritablePath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	results := filepath.Join(root, "test", "benchmarks", "results")
	require.NoError(t, os.MkdirAll(results, 0o755))
	require.NoError(t, os.Chmod(results, 0o500)) // r-x: MkdirAll succeeds, WriteFile fails
	t.Cleanup(func() { _ = os.Chmod(results, 0o755) })
	t.Chdir(root)

	path, err := WriteReport(Report{Benchmark: "swe-bench", StartedAt: time.Unix(0, 0).UTC()})
	require.Error(t, err)
	assert.Empty(t, path)
	assert.Contains(t, err.Error(), "write")
}

func TestReport_SummariseOutcomes(t *testing.T) {
	r := Report{PerTask: []TaskOutcome{
		{Outcome: "passed", CostUSD: 0.10},
		{Outcome: "passed", CostUSD: 0.20},
		{Outcome: "failed", CostUSD: 0.05},
		{Outcome: "error", CostUSD: 0.05},   // error counts as failed
		{Outcome: "skipped", CostUSD: 0},    // skipped is neither pass nor fail
		{Outcome: "bizarre", CostUSD: 0.01}, // unknown outcomes are counted in the total only
	}}
	r.SummariseOutcomes()

	assert.Equal(t, 6, r.TasksTotal)
	assert.Equal(t, 2, r.TasksPassed)
	assert.Equal(t, 2, r.TasksFailed, "error must roll up with failed")
	assert.Equal(t, 1, r.TasksSkipped)
	assert.InDelta(t, 0.41, r.CostUSD, 1e-9, "cost accumulates across every outcome kind")
	assert.InDelta(t, 2.0/6.0, r.PassRate, 1e-9)
}

func TestReport_SummariseOutcomes_EmptyLeavesRateZero(t *testing.T) {
	// Guards the division: no tasks must not produce NaN.
	r := Report{}
	r.SummariseOutcomes()
	assert.Zero(t, r.TasksTotal)
	assert.Zero(t, r.PassRate)
}

func TestReport_SummariseOutcomes_IsIdempotent(t *testing.T) {
	// Counters are reset each call, so summarising twice must not
	// double the totals -- but note CostUSD accumulates by design.
	r := Report{PerTask: []TaskOutcome{{Outcome: "passed"}, {Outcome: "failed"}}}
	r.SummariseOutcomes()
	first := r.TasksTotal
	firstPassed := r.TasksPassed
	r.SummariseOutcomes()
	assert.Equal(t, first, r.TasksTotal)
	assert.Equal(t, firstPassed, r.TasksPassed)
}
