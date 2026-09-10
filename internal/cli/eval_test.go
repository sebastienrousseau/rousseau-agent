package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/reliability"
)

// Regression tests for the `rousseau eval` CLI wrapper.

// -- loadEvalFixtures -----------------------------------------------

func TestLoadEvalFixtures_TopLevelList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixtures.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
- id: alpha
  prompt: do X
  expect_substring: X
- id: beta
  prompt: do Y
  paraphrases:
    - please do Y
    - kindly Y
`), 0o600))

	got, err := loadEvalFixtures(path)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "alpha", got[0].ID)
	assert.Equal(t, "X", got[0].ExpectSubstring)
	assert.Equal(t, "beta", got[1].ID)
	assert.Len(t, got[1].Paraphrases, 2)
}

func TestLoadEvalFixtures_WrappedForm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixtures.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
version: 1
description: A test suite
fixtures:
  - id: alpha
    prompt: do X
`), 0o600))

	got, err := loadEvalFixtures(path)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "alpha", got[0].ID)
}

func TestLoadEvalFixtures_MissingFileErrors(t *testing.T) {
	_, err := loadEvalFixtures("/definitely/does/not/exist/rousseau-eval.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read fixture")
}

func TestLoadEvalFixtures_EmptyDocumentErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.yaml")
	require.NoError(t, os.WriteFile(path, []byte("# just a comment\n"), 0o600))

	_, err := loadEvalFixtures(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no entries")
}

func TestLoadEvalFixtures_MalformedYAMLErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("- id: x\n  prompt: [unclosed"), 0o600))

	_, err := loadEvalFixtures(path)
	require.Error(t, err)
}

// -- selectEvalRunner -----------------------------------------------

func TestSelectEvalRunner_DryRunReturnsStub(t *testing.T) {
	var stderr bytes.Buffer
	r, err := selectEvalRunner(nil, true, false, &stderr)
	require.NoError(t, err)
	require.NotNil(t, r)

	// Deterministic stub reply.
	reply, err := r.Run(context.Background(), "any prompt")
	require.NoError(t, err)
	assert.Equal(t, "ok", reply)
}

func TestSelectEvalRunner_NoFlagsErrorsWithGuidance(t *testing.T) {
	var stderr bytes.Buffer
	_, err := selectEvalRunner(nil, false, false, &stderr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--dry-run")
	assert.Contains(t, err.Error(), "--confirm-cost")
}

func TestSelectEvalRunner_BothFlagsIsMutuallyExclusive(t *testing.T) {
	var stderr bytes.Buffer
	_, err := selectEvalRunner(nil, true, true, &stderr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestSelectEvalRunner_ConfirmCostWithoutConfigErrors(t *testing.T) {
	var stderr bytes.Buffer
	_, err := selectEvalRunner(nil, false, true, &stderr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loaded config")
}

func TestSelectEvalRunner_ConfirmCostWithBrokenConfigErrors(t *testing.T) {
	// Provider that will fail buildProvider (e.g. bedrock with no
	// region set) — the runner surfaces the underlying construction
	// error rather than swallowing it.
	var stderr bytes.Buffer
	opts := &Options{Config: &config.Config{Provider: "bedrock"}}
	_, err := selectEvalRunner(opts, false, true, &stderr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "construct provider")
}

// TestProviderEvalRunner_ExtractsTextParts freezes the assumption
// that the runner concatenates every `text` content block on the
// provider reply and skips tool-use / thinking. A future provider
// change that adds a new Content kind must decide explicitly whether
// to include it in the eval-judged output.
func TestProviderEvalRunner_ExtractsTextParts(t *testing.T) {
	fake := fakeProvider{
		name: "fake",
		reply: agent.Message{
			Content: []agent.Content{
				{Kind: agent.ContentText, Text: "one"},
				{Kind: agent.ContentToolUse, Text: "should be skipped"},
				{Kind: agent.ContentText, Text: "two"},
			},
		},
	}
	r := newProviderEvalRunner(&fake)
	got, err := r.Run(context.Background(), "hello")
	require.NoError(t, err)
	assert.Equal(t, "one\ntwo", got)
	assert.Equal(t, "hello", fake.lastPrompt)
	assert.Empty(t, fake.lastReq.SessionID, "eval runs must NOT reuse session state — measures naked model behaviour")
	assert.Empty(t, fake.lastReq.Tools, "eval runs must NOT expose tools — measures naked model behaviour")
}

func TestProviderEvalRunner_PropagatesProviderError(t *testing.T) {
	boom := errors.New("provider went boom")
	fake := &fakeProvider{name: "fake", err: boom}
	r := newProviderEvalRunner(fake)
	_, err := r.Run(context.Background(), "hi")
	require.ErrorIs(t, err, boom)
}

// fakeProvider satisfies the local agentProvider interface. Records
// the last request so tests can freeze the "single-turn, no
// context" contract.
type fakeProvider struct {
	name       string
	reply      agent.Message
	err        error
	lastPrompt string
	lastReq    agent.Request
}

func (f fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Complete(_ context.Context, req agent.Request) (agent.Response, error) {
	f.lastReq = req
	if len(req.Messages) > 0 && len(req.Messages[0].Content) > 0 {
		f.lastPrompt = req.Messages[0].Content[0].Text
	}
	if f.err != nil {
		return agent.Response{}, f.err
	}
	return agent.Response{Message: f.reply}, nil
}

// -- openEvalRecorder -----------------------------------------------

func TestOpenEvalRecorder_NoConfigReturnsNop(t *testing.T) {
	rec, closer := openEvalRecorder(nil)
	assert.IsType(t, reliability.NopRecorder{}, rec)
	assert.Nil(t, closer)
}

// -- renderEval -----------------------------------------------------

func TestRenderEval_HumanFormat(t *testing.T) {
	results := []reliability.EvalResult{
		{FixtureID: "alpha", BasePasses: 5, BaseAttempts: 5, OutcomeAccuracy: 1.0},
		{FixtureID: "beta", BasePasses: 3, BaseAttempts: 5, OutcomeAccuracy: 0.6, ParaphraseTotal: 2, ParaphrasePasses: 1, PromptDelta: 0.1},
	}
	var buf bytes.Buffer
	require.NoError(t, renderEval(&buf, results, false))

	out := buf.String()
	assert.Contains(t, out, "2 fixture(s)")
	assert.Contains(t, out, "alpha")
	assert.Contains(t, out, "base=5/5")
	assert.Contains(t, out, "outcome_acc=1.00")
	assert.Contains(t, out, "beta")
	assert.Contains(t, out, "base=3/5")
	assert.Contains(t, out, "Δ=0.10")
}

func TestRenderEval_JSONFormat(t *testing.T) {
	results := []reliability.EvalResult{
		{FixtureID: "alpha", BasePasses: 5, BaseAttempts: 5, OutcomeAccuracy: 1.0},
	}
	var buf bytes.Buffer
	require.NoError(t, renderEval(&buf, results, true))

	var got []reliability.EvalResult
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "alpha", got[0].FixtureID)
	assert.InDelta(t, 1.0, got[0].OutcomeAccuracy, 1e-9)
}

func TestRenderEval_ErrorsSurfaceAsExitCode(t *testing.T) {
	// A fixture with errors → renderEval returns a non-nil error
	// so cobra exits non-zero. Human mode still prints the human
	// summary + error indicator inline.
	results := []reliability.EvalResult{
		{FixtureID: "flaky", BasePasses: 4, BaseAttempts: 5, Errors: []string{"provider timed out"}},
	}
	var buf bytes.Buffer
	err := renderEval(&buf, results, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 fixture(s) had errors")

	out := buf.String()
	assert.Contains(t, out, "(1 error)")
	assert.Contains(t, out, "provider timed out")
}

func TestRenderEval_JSONErrorReturnsExitError(t *testing.T) {
	results := []reliability.EvalResult{
		{FixtureID: "f", Errors: []string{"boom"}},
	}
	var buf bytes.Buffer
	err := renderEval(&buf, results, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "had errors")

	// JSON body still writes.
	var body []reliability.EvalResult
	require.NoError(t, json.Unmarshal(buf.Bytes(), &body))
	require.Len(t, body, 1)
	assert.Equal(t, "f", body[0].FixtureID)
}

// -- helpers --------------------------------------------------------

func TestPlural(t *testing.T) {
	assert.Equal(t, "", plural(1))
	assert.Equal(t, "s", plural(0))
	assert.Equal(t, "s", plural(2))
}

func TestAnyEvalErrors(t *testing.T) {
	assert.False(t, anyEvalErrors(nil))
	assert.False(t, anyEvalErrors([]reliability.EvalResult{{FixtureID: "x"}}))
	assert.True(t, anyEvalErrors([]reliability.EvalResult{{FixtureID: "x", Errors: []string{"e"}}}))
}

func TestCountEvalErrors(t *testing.T) {
	results := []reliability.EvalResult{
		{FixtureID: "a"},
		{FixtureID: "b", Errors: []string{"e"}},
		{FixtureID: "c", Errors: []string{"e1", "e2"}},
	}
	assert.Equal(t, 2, countEvalErrors(results), "fixtures-with-errors, not total-error-count")
}

// -- Command wiring -------------------------------------------------

func TestNewEvalCmd_Registered(t *testing.T) {
	root := NewRoot(&Options{})
	var found bool
	for _, sub := range root.Commands() {
		if sub.Name() == "eval" {
			found = true
			break
		}
	}
	assert.True(t, found, "NewRoot must register the eval subcommand")
}

func TestNewEvalCmd_HasExpectedFlags(t *testing.T) {
	cmd := newEvalCmd(&Options{})
	for _, flag := range []string{"fixture", "repeats", "json", "dry-run"} {
		assert.NotNil(t, cmd.Flags().Lookup(flag), "missing --%s flag", flag)
	}
}

// -- End-to-end: dry-run through the whole stack --------------------

// TestEval_EndToEndDryRun exercises the full CLI RunE path with
// a real fixture file, --dry-run runner, and a no-op recorder.
// Verifies the composition (loadFixtures → selectRunner →
// RunEval → renderEval) is stable.
func TestEval_EndToEndDryRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixtures.yaml")
	require.NoError(t, os.WriteFile(path, []byte(strings.Join([]string{
		"- id: greet",
		"  prompt: say hi",
		"  expect_substring: ok",
		"- id: silent",
		"  prompt: refuse",
	}, "\n")), 0o600))

	fixtures, err := loadEvalFixtures(path)
	require.NoError(t, err)
	runner, err := selectEvalRunner(nil, true, false, &bytes.Buffer{})
	require.NoError(t, err)

	results, err := reliability.RunEval(context.Background(), fixtures, reliability.EvalConfig{
		Runner:  runner,
		Repeats: 3,
	})
	require.NoError(t, err)
	require.Len(t, results, 2)

	// greet: stub returns "ok" → expects "ok" substring → all 3 pass
	assert.Equal(t, 3, results[0].BasePasses)
	assert.InDelta(t, 1.0, results[0].OutcomeAccuracy, 1e-9)
	// silent: no expect field, non-empty fallback → "ok" is non-empty → all pass
	assert.Equal(t, 3, results[1].BasePasses)
}
