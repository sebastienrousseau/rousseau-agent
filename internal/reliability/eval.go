package reliability

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// EvalFixture describes a task the synthetic-eval harness runs
// against the agent. Fixtures are the closest live daemons get to
// the paper's "same task K times" and "paraphrase cluster"
// perturbations (arXiv:2602.16666 §3.1, §3.2) — because real
// users never send the same task 5 times in a row.
//
// A fixture yields two axes:
//
//   Outcome consistency (C_out): repeat Prompt K times. Each run
//     produces a success/failure sample bucketed by ID. The
//     aggregator's outcomeConsistency() then computes the
//     normalised-variance form from the paper.
//
//   Prompt robustness (R_prompt): every entry in Paraphrases is
//     a semantically-equivalent restatement of Prompt. The
//     harness runs each once, computes Δaccuracy vs the base
//     bucket, and emits a `prompt` sample per cluster. When
//     Paraphrases is empty the axis is skipped for this fixture.
type EvalFixture struct {
	// ID uniquely identifies the fixture. Used as the sample
	// Bucket so per-fixture variance rolls up correctly.
	ID string `yaml:"id" json:"id"`
	// Prompt is the base user turn the runner submits.
	Prompt string `yaml:"prompt" json:"prompt"`
	// Paraphrases are semantically-equivalent variants of Prompt.
	// Populate to exercise Robustness R_prompt; empty skips the
	// axis.
	Paraphrases []string `yaml:"paraphrases,omitempty" json:"paraphrases,omitempty"`
	// ExpectRegex, when non-empty, judges a response as successful
	// when the response matches the regex. Mutually exclusive with
	// ExpectSubstring — the runner uses whichever is set (regex
	// wins if both).
	ExpectRegex string `yaml:"expect_regex,omitempty" json:"expect_regex,omitempty"`
	// ExpectSubstring, when non-empty, judges a response as
	// successful when the response contains the substring
	// (case-sensitive). Simpler alternative to ExpectRegex.
	ExpectSubstring string `yaml:"expect_substring,omitempty" json:"expect_substring,omitempty"`
}

// EvalRunner is the abstraction the harness uses to actually
// invoke the agent. Kept as an interface so the CLI can plug in:
//
//   - stub runner (deterministic canned responses for CI / demo)
//   - provider runner (real LLM invocation via configured Provider)
//   - custom runner (tenant-specific harness with per-fixture
//     tool policies, etc.)
//
// The runner is single-shot — one prompt in, one response out.
// The harness sequences repeats + paraphrases itself.
type EvalRunner interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// EvalRunnerFunc adapts an ordinary function to EvalRunner.
type EvalRunnerFunc func(ctx context.Context, prompt string) (string, error)

// Run satisfies EvalRunner.
func (f EvalRunnerFunc) Run(ctx context.Context, prompt string) (string, error) {
	return f(ctx, prompt)
}

// EvalConfig tunes the harness. Zero-value picks paper-recommended
// defaults per field.
type EvalConfig struct {
	// Repeats is K in the paper — how many times to run each base
	// Prompt for outcome consistency. Zero uses 5 (the paper's
	// default).
	Repeats int
	// Runner does the actual model invocation. Required — the
	// harness returns an error if Runner is nil.
	Runner EvalRunner
	// Recorder receives every eval sample so `rousseau
	// reliability` sees the same data as live traffic. Nil uses
	// NopRecorder — samples are computed for the summary but
	// nothing persists.
	Recorder Recorder
	// Now is the injectable clock — tests substitute a fixed
	// time. Nil uses time.Now.
	Now func() time.Time
}

// EvalResult is the per-fixture roll-up the harness returns for
// operator display. Individual samples still land in the Recorder;
// this is the "what happened" summary the CLI renders.
type EvalResult struct {
	FixtureID       string
	BasePasses      int     // successful base-prompt runs of K
	BaseAttempts    int     // K
	ParaphraseTotal int     // len(Paraphrases)
	ParaphrasePasses int    // paraphrase runs that judged success
	OutcomeAccuracy  float64 // BasePasses / BaseAttempts
	PromptDelta      float64 // |Acc_paraphrases - OutcomeAccuracy|; 0 when no paraphrases
	Errors           []string
}

// RunEval executes the fixture set through cfg.Runner, emits
// reliability samples per the paper (outcome per repeat, prompt
// delta per paraphrase cluster), and returns per-fixture results
// for CLI display. Failure to run one fixture does not abort the
// suite — errors accumulate into EvalResult.Errors and the run
// continues.
func RunEval(ctx context.Context, fixtures []EvalFixture, cfg EvalConfig) ([]EvalResult, error) {
	if cfg.Runner == nil {
		return nil, fmt.Errorf("reliability: eval runner is required")
	}
	if cfg.Repeats <= 0 {
		cfg.Repeats = 5
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	rec := cfg.Recorder
	if rec == nil {
		rec = NopRecorder{}
	}

	results := make([]EvalResult, 0, len(fixtures))
	for _, f := range fixtures {
		results = append(results, runOneFixture(ctx, f, cfg, rec))
	}
	return results, nil
}

// runOneFixture is the per-fixture inner loop — extracted so the
// outer RunEval stays legible.
func runOneFixture(ctx context.Context, f EvalFixture, cfg EvalConfig, rec Recorder) EvalResult {
	res := EvalResult{FixtureID: f.ID, BaseAttempts: cfg.Repeats}
	judge := buildJudge(f)

	// K repeats of the base prompt — Outcome consistency signal.
	for i := 0; i < cfg.Repeats; i++ {
		reply, err := cfg.Runner.Run(ctx, f.Prompt)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("base run %d: %v", i+1, err))
			// Failed runs still emit an outcome sample as 0 —
			// the paper's variance calc treats them as failures.
			rec.Record(Sample{
				At:        cfg.Now(),
				Dimension: DimConsistency,
				SubMetric: "outcome",
				Value:     0,
				Bucket:    "eval:" + f.ID,
				Metadata: map[string]string{
					"source":  "eval",
					"variant": "base",
					"error":   truncateEvalError(err.Error(), 200),
				},
			})
			continue
		}
		ok := judge(reply)
		val := 0.0
		if ok {
			val = 1
			res.BasePasses++
		}
		rec.Record(Sample{
			At:        cfg.Now(),
			Dimension: DimConsistency,
			SubMetric: "outcome",
			Value:     val,
			Bucket:    "eval:" + f.ID,
			Metadata:  map[string]string{"source": "eval", "variant": "base"},
		})
	}
	if res.BaseAttempts > 0 {
		res.OutcomeAccuracy = float64(res.BasePasses) / float64(res.BaseAttempts)
	}

	// Paraphrase pass — Robustness R_prompt signal.
	res.ParaphraseTotal = len(f.Paraphrases)
	if res.ParaphraseTotal == 0 {
		return res
	}
	for i, p := range f.Paraphrases {
		reply, err := cfg.Runner.Run(ctx, p)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("paraphrase %d: %v", i+1, err))
			continue
		}
		if judge(reply) {
			res.ParaphrasePasses++
		}
	}
	paraphraseAcc := float64(res.ParaphrasePasses) / float64(res.ParaphraseTotal)
	res.PromptDelta = absFloat(paraphraseAcc - res.OutcomeAccuracy)
	// One prompt sample per fixture — the delta is the paper's
	// definition; the aggregator's promptRobustness converts a
	// stream of these into R_prompt.
	rec.Record(Sample{
		At:        cfg.Now(),
		Dimension: DimRobustness,
		SubMetric: "prompt",
		Value:     res.PromptDelta,
		Bucket:    "eval:" + f.ID,
		Metadata:  map[string]string{"source": "eval"},
	})

	return res
}

// buildJudge returns a function that says whether a reply
// satisfies the fixture. Priority: ExpectRegex > ExpectSubstring >
// non-empty. Non-empty is the "did the model at least respond"
// fallback — usually enough for smoke-tests where the fixture
// author hasn't defined a strict success criterion.
func buildJudge(f EvalFixture) func(string) bool {
	if f.ExpectRegex != "" {
		re, err := regexp.Compile(f.ExpectRegex)
		if err != nil {
			// Compile failure = every run fails. Better than
			// silently accepting every response.
			return func(string) bool { return false }
		}
		return func(reply string) bool { return re.MatchString(reply) }
	}
	if f.ExpectSubstring != "" {
		return func(reply string) bool { return strings.Contains(reply, f.ExpectSubstring) }
	}
	return func(reply string) bool { return strings.TrimSpace(reply) != "" }
}

func truncateEvalError(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
