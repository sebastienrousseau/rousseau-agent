# Benchmarks

Reproducible benchmark harness for `rousseau-agent`. Three tracks land
here, each behind the `//go:build bench` tag so they never run as part
of the default `go test ./...` gate:

| Track | What it measures | Source | Grading |
|---|---|---|---|
| `swe-bench` | Real-world software-engineering issue resolution on 12 GitHub projects | [`princeton-nlp/SWE-bench`](https://github.com/princeton-nlp/SWE-bench) — Verified subset (500 tasks) | Apply generated patch, run the project's test suite, check the FAIL_TO_PASS + PASS_TO_PASS test lists |
| `aider-polyglot` | Multilingual code editing across C++/Go/Java/JS/Python/Rust (225 tasks, ~37 per language) | [`Aider-AI/polyglot-benchmark`](https://github.com/Aider-AI/polyglot-benchmark) | Apply generated changes, run per-task test command, check exit status |
| `terminal-bench` | Long-horizon shell work (agent-drives-terminal tasks) | [`laude-institute/terminal-bench`](https://github.com/laude-institute/terminal-bench) | Compare final terminal state to expected snapshot |

## Why three benchmarks, not one

SWE-Bench-Verified is the industry standard, but its 500-task set is
[Python-heavy and disproportionately rewards Python-monoculture
strategies](https://agentmarketcap.ai/blog/2026/04/06/aider-polyglot-leaderboard-2026-swe-bench-python-bias).
Aider Polyglot corrects for that; Terminal-Bench probes the failure
mode of any agent whose success on synthetic issues doesn't transfer
to open-ended shell work. Publishing all three is honest — no single
number can capture what a coding daemon actually does.

## Layout

```
test/benchmarks/
├── README.md              (this file)
├── common/                (shared runner utilities — session isolation, JSON output)
├── swe-bench/
│   └── swe_bench_test.go  //go:build bench — invokes rousseau against each task
├── aider-polyglot/
│   └── polyglot_test.go   //go:build bench — same shape, different corpus
├── terminal-bench/
│   └── terminal_test.go   //go:build bench — same shape, different grader
└── results/               (gitignored — JSON output per run)
```

## Reproducing locally

Each benchmark's test file skips itself unless (a) the benchmark repo
is checked out where the runner expects it, and (b) an
`ANTHROPIC_API_KEY` (or provider-specific credential) is set.

```bash
# One-time: clone the benchmark corpora next to the rousseau checkout.
# The runners look at ROUSSEAU_BENCH_DIR (default: ~/bench-corpora/) for
# each corpus directory.
export ROUSSEAU_BENCH_DIR="$HOME/bench-corpora"
mkdir -p "$ROUSSEAU_BENCH_DIR"
git clone https://github.com/princeton-nlp/SWE-bench    "$ROUSSEAU_BENCH_DIR/SWE-bench"
git clone https://github.com/Aider-AI/polyglot-benchmark "$ROUSSEAU_BENCH_DIR/polyglot-benchmark"
git clone https://github.com/laude-institute/terminal-bench "$ROUSSEAU_BENCH_DIR/terminal-bench"

# Run one benchmark (SWE-Bench Verified subset, first 10 tasks for a smoke pass):
export ROUSSEAU_BENCH_LIMIT=10
go test -tags bench -timeout 6h ./test/benchmarks/swe-bench/...

# Run the full bundle (slow — hours per benchmark; expect $-scale API costs):
unset ROUSSEAU_BENCH_LIMIT
go test -tags bench -timeout 24h ./test/benchmarks/...
```

Results land as JSON under `test/benchmarks/results/`:

```json
{
  "benchmark": "swe-bench",
  "corpus_commit": "abcd1234",
  "rousseau_version": "v0.0.1-dirty",
  "model": "claude-opus-4-6",
  "started_at": "2026-08-23T09:12:45Z",
  "ended_at":   "2026-08-23T15:04:33Z",
  "tasks_total": 500,
  "tasks_passed": 172,
  "tasks_failed": 328,
  "pass_rate": 0.344,
  "cost_usd": 128.44,
  "per_task": [
    {"task_id": "django__django-11400", "outcome": "passed", "duration_sec": 41.2, "cost_usd": 0.31},
    ...
  ]
}
```

## Publishing results

The CI workflow at [`../../.github/workflows/benchmarks.yml`](../../.github/workflows/benchmarks.yml)
runs the bundle weekly on `main` and uploads the JSON artefacts. Every
release entry in [`../../CHANGELOG.md`](../../CHANGELOG.md) should
include the latest three numbers (SWE-Bench / Aider Polyglot /
Terminal-Bench pass rates) sourced from the last run of the tag's
commit.

## Cost calibration

Rough per-benchmark cost estimates at 2026-08 prices, running the full
corpus with `claude-opus-4-6` and default `ExtraArgs`:

| Benchmark | Tasks | Est. duration | Est. cost |
|---|---:|---:|---:|
| SWE-Bench Verified | 500 | 6-12 h | $80-200 |
| Aider Polyglot | 225 | 3-6 h | $30-90 |
| Terminal-Bench | ~100 | 2-4 h | $20-60 |

Use `ROUSSEAU_BENCH_LIMIT=N` to cap the number of tasks. Use
`ROUSSEAU_BENCH_MODEL=haiku` (or another cheaper model) for
smoke-runs where the point is to prove the harness works, not to
publish a number.
