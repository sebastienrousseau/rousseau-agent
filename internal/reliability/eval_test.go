package reliability

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for RunEval + the surrounding fixture flow.
// Locks in the paper's contract that outcome consistency + prompt
// robustness both emit samples through the same Recorder as live
// traffic so `rousseau reliability` sees a unified stream.

// recordingEvalRunner captures every prompt the harness runs and
// returns a configurable reply. Concurrency-safe (RunEval runs
// fixtures sequentially today, but the runner interface makes no
// guarantee).
type recordingEvalRunner struct {
	mu      sync.Mutex
	prompts []string
	reply   string
	err     error
	// deterministicMode uses fixture-index-based replies so a
	// test can assert "run i returned X."
	perPrompt map[string]string
}

func (r *recordingEvalRunner) Run(_ context.Context, prompt string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prompts = append(r.prompts, prompt)
	if r.err != nil {
		return "", r.err
	}
	if v, ok := r.perPrompt[prompt]; ok {
		return v, nil
	}
	return r.reply, nil
}

func (r *recordingEvalRunner) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.prompts))
	copy(out, r.prompts)
	return out
}

type samplingRecorder struct {
	mu      sync.Mutex
	samples []Sample
}

func (s *samplingRecorder) Record(sm Sample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.samples = append(s.samples, sm)
}

func (s *samplingRecorder) all() []Sample {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Sample, len(s.samples))
	copy(out, s.samples)
	return out
}

// -- Basic contracts -------------------------------------------------

func TestRunEval_NilRunnerErrors(t *testing.T) {
	_, err := RunEval(context.Background(), nil, EvalConfig{Runner: nil})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runner is required")
}

func TestRunEval_EmptyFixturesReturnsEmpty(t *testing.T) {
	got, err := RunEval(context.Background(), nil,
		EvalConfig{Runner: EvalRunnerFunc(func(_ context.Context, _ string) (string, error) { return "ok", nil })})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestRunEval_DefaultRepeatsIsFive(t *testing.T) {
	runner := &recordingEvalRunner{reply: "ok"}
	rec := &samplingRecorder{}
	_, err := RunEval(context.Background(),
		[]EvalFixture{{ID: "f1", Prompt: "do X"}},
		EvalConfig{Runner: runner, Recorder: rec})
	require.NoError(t, err)
	// K=5 base repeats + 0 paraphrases = 5 runner invocations.
	assert.Len(t, runner.got(), 5, "default Repeats must be 5 per the paper")
}

// -- Outcome consistency --------------------------------------------

func TestRunEval_OutcomeSamplesEmitted(t *testing.T) {
	runner := &recordingEvalRunner{reply: "success text"}
	rec := &samplingRecorder{}

	results, err := RunEval(context.Background(),
		[]EvalFixture{{ID: "f1", Prompt: "X", ExpectSubstring: "success"}},
		EvalConfig{Runner: runner, Recorder: rec, Repeats: 3})
	require.NoError(t, err)
	require.Len(t, results, 1)

	// Every base run must have emitted an outcome sample bucketed
	// by "eval:<fixture-id>", tagged Metadata[source]=eval.
	outcomeCount := 0
	for _, s := range rec.all() {
		if s.SubMetric == "outcome" && s.Bucket == "eval:f1" {
			outcomeCount++
			assert.Equal(t, "eval", s.Metadata["source"])
			assert.Equal(t, "base", s.Metadata["variant"])
			assert.Equal(t, 1.0, s.Value, "'success text' matched ExpectSubstring 'success' → outcome=1")
		}
	}
	assert.Equal(t, 3, outcomeCount)

	assert.Equal(t, 3, results[0].BasePasses)
	assert.InDelta(t, 1.0, results[0].OutcomeAccuracy, 1e-9)
}

func TestRunEval_JudgeFailuresRecordZero(t *testing.T) {
	runner := &recordingEvalRunner{reply: "unrelated"}
	rec := &samplingRecorder{}

	results, err := RunEval(context.Background(),
		[]EvalFixture{{ID: "f1", Prompt: "X", ExpectSubstring: "expected"}},
		EvalConfig{Runner: runner, Recorder: rec, Repeats: 3})
	require.NoError(t, err)

	// Every reply "unrelated" fails the "expected" substring →
	// 3 outcome samples all with value 0.
	zeroCount := 0
	for _, s := range rec.all() {
		if s.SubMetric == "outcome" && s.Value == 0 {
			zeroCount++
		}
	}
	assert.Equal(t, 3, zeroCount)
	assert.Zero(t, results[0].BasePasses)
	assert.InDelta(t, 0.0, results[0].OutcomeAccuracy, 1e-9)
}

func TestRunEval_RunnerErrorRecordsFailureAndAccumulatesError(t *testing.T) {
	runner := &recordingEvalRunner{err: errors.New("provider timeout")}
	rec := &samplingRecorder{}

	results, err := RunEval(context.Background(),
		[]EvalFixture{{ID: "f1", Prompt: "X"}},
		EvalConfig{Runner: runner, Recorder: rec, Repeats: 2})
	require.NoError(t, err)
	require.Len(t, results, 1)

	// Both runs failed at the runner level → both emit outcome=0
	// with an "error" metadata field.
	for _, s := range rec.all() {
		if s.SubMetric == "outcome" {
			assert.Equal(t, 0.0, s.Value)
			assert.Contains(t, s.Metadata["error"], "provider timeout")
		}
	}
	assert.Len(t, results[0].Errors, 2)
	assert.Zero(t, results[0].BasePasses)
}

// -- Prompt robustness ----------------------------------------------

func TestRunEval_PromptRobustnessSampleEmitted(t *testing.T) {
	// Base prompt "how do I X" always passes (reply contains
	// "yes"). Paraphrases half-fail.
	runner := &recordingEvalRunner{
		perPrompt: map[string]string{
			"how do I X":          "yes",
			"what is the way to X": "yes",
			"tell me how to X":     "nope", // fails the substring judge
		},
		reply: "yes",
	}
	rec := &samplingRecorder{}

	results, err := RunEval(context.Background(),
		[]EvalFixture{{
			ID:              "f1",
			Prompt:          "how do I X",
			Paraphrases:     []string{"what is the way to X", "tell me how to X"},
			ExpectSubstring: "yes",
		}},
		EvalConfig{Runner: runner, Recorder: rec, Repeats: 1})
	require.NoError(t, err)
	require.Len(t, results, 1)

	// Base: 1/1 passes. Paraphrases: 1/2. |0.5 - 1.0| = 0.5.
	assert.InDelta(t, 1.0, results[0].OutcomeAccuracy, 1e-9)
	assert.InDelta(t, 0.5, results[0].PromptDelta, 1e-9)

	// A single prompt sample recorded with the delta.
	var promptSample *Sample
	for i := range rec.all() {
		s := &rec.all()[i]
		if s.SubMetric == "prompt" {
			promptSample = s
			break
		}
	}
	require.NotNil(t, promptSample)
	assert.Equal(t, DimRobustness, promptSample.Dimension)
	assert.InDelta(t, 0.5, promptSample.Value, 1e-9)
	assert.Equal(t, "eval:f1", promptSample.Bucket)
	assert.Equal(t, "eval", promptSample.Metadata["source"])
}

func TestRunEval_NoParaphrasesSkipsPromptAxis(t *testing.T) {
	runner := &recordingEvalRunner{reply: "ok"}
	rec := &samplingRecorder{}

	results, err := RunEval(context.Background(),
		[]EvalFixture{{ID: "f1", Prompt: "X"}},
		EvalConfig{Runner: runner, Recorder: rec, Repeats: 1})
	require.NoError(t, err)
	assert.Zero(t, results[0].PromptDelta,
		"no paraphrases → PromptDelta stays at zero-value")

	for _, s := range rec.all() {
		assert.NotEqual(t, "prompt", s.SubMetric,
			"no paraphrases → no prompt sample")
	}
}

func TestRunEval_ParaphraseRunnerErrorLoggedButNotFatal(t *testing.T) {
	runner := &recordingEvalRunner{
		perPrompt: map[string]string{
			"base": "ok",
			"para": "", // caused by err=nil,reply="" per test setup below
		},
	}
	// Force the paraphrase run to error.
	runner.err = nil
	// Base run judged by non-empty fallback → passes. Paraphrase
	// runner errors before returning.
	runnerWithErr := EvalRunnerFunc(func(_ context.Context, prompt string) (string, error) {
		if prompt == "para" {
			return "", errors.New("paraphrase blew up")
		}
		return "ok", nil
	})
	rec := &samplingRecorder{}

	results, err := RunEval(context.Background(),
		[]EvalFixture{{ID: "f1", Prompt: "base", Paraphrases: []string{"para"}}},
		EvalConfig{Runner: runnerWithErr, Recorder: rec, Repeats: 1})
	require.NoError(t, err, "one failed paraphrase must not abort the run")

	require.NotEmpty(t, results[0].Errors)
	assert.Contains(t, results[0].Errors[0], "paraphrase 1")
	assert.Contains(t, results[0].Errors[0], "paraphrase blew up")
}

// -- buildJudge ------------------------------------------------------

func TestBuildJudge_RegexPriorityOverSubstring(t *testing.T) {
	// Both fields set → regex wins per the doc comment.
	f := EvalFixture{ExpectRegex: "^[a-z]+$", ExpectSubstring: "SUCCESS"}
	j := buildJudge(f)
	assert.True(t, j("lowercase"))
	assert.False(t, j("HAS SUCCESS BUT UPPERCASE"))
}

func TestBuildJudge_SubstringOnly(t *testing.T) {
	j := buildJudge(EvalFixture{ExpectSubstring: "yes"})
	assert.True(t, j("the answer is yes"))
	assert.False(t, j("no"))
}

func TestBuildJudge_NonEmptyFallback(t *testing.T) {
	j := buildJudge(EvalFixture{})
	assert.True(t, j("anything at all"))
	assert.False(t, j(""))
	assert.False(t, j("   \n  "))
}

func TestBuildJudge_MalformedRegexRejectsEverything(t *testing.T) {
	// A fixture with a broken regex should fail-safe (reject all
	// replies) rather than silently accept everything.
	j := buildJudge(EvalFixture{ExpectRegex: "["})
	assert.False(t, j("anything"))
	assert.False(t, j(""))
}

// -- Small helpers --------------------------------------------------

func TestTruncateEvalError(t *testing.T) {
	assert.Equal(t, "short", truncateEvalError("short", 10))
	assert.Equal(t, "abc…", truncateEvalError("abcdef", 3))
	assert.Equal(t, "…", truncateEvalError("abc", 0))
}

func TestAbsFloat(t *testing.T) {
	assert.Equal(t, 0.5, absFloat(0.5))
	assert.Equal(t, 0.5, absFloat(-0.5))
	assert.Equal(t, 0.0, absFloat(0))
}

// -- Time injection --------------------------------------------------

func TestRunEval_UsesInjectedClock(t *testing.T) {
	fixedNow := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	rec := &samplingRecorder{}
	runner := &recordingEvalRunner{reply: "ok"}

	_, err := RunEval(context.Background(),
		[]EvalFixture{{ID: "f1", Prompt: "X"}},
		EvalConfig{
			Runner:   runner,
			Recorder: rec,
			Repeats:  1,
			Now:      func() time.Time { return fixedNow },
		})
	require.NoError(t, err)

	require.NotEmpty(t, rec.all())
	for _, s := range rec.all() {
		assert.Equal(t, fixedNow, s.At, "every sample must be stamped by the injected clock")
	}
}

// -- Real-world fixture parse smoke test -----------------------------

// The eval fixture format is stable JSON/YAML — verify a
// representative document round-trips through structs without loss
// or panic. (Actual YAML parsing is delegated to the CLI's yaml
// dependency; here we just exercise the struct semantics.)
func TestEvalFixture_JSONFieldsExposed(t *testing.T) {
	f := EvalFixture{
		ID:              "example",
		Prompt:          "Explain rebase",
		Paraphrases:     []string{"how do I rebase", "what's a rebase"},
		ExpectSubstring: "commit",
	}
	// Judge with a substring match — the standard "does the reply
	// mention the expected concept" smoke test.
	j := buildJudge(f)
	assert.True(t, j("rebase moves commit pointers"))
	assert.False(t, j("I don't know"))

	// Struct fields accessible for JSON/YAML tooling.
	assert.NotEmpty(t, f.ID)
	assert.NotEmpty(t, f.Prompt)
	assert.Len(t, f.Paraphrases, 2)
	assert.True(t, strings.HasPrefix(f.ExpectSubstring, "commit"))
}
