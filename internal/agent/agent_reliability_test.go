package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/reliability"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// Regression tests for Turn's reliability-instrumentation seam.
// Every branch of recordReliabilitySamples is asserted so a future
// refactor cannot silently drop the samples the CLI + persistent
// store depend on.

// recordingRecorder captures every sample the agent emits so tests
// can assert on the exact shape of what got recorded. Concurrency-
// safe because Turn's async goroutines (progress emitters) may fire
// concurrently with the reliability record.
type recordingRecorder struct {
	mu      sync.Mutex
	samples []reliability.Sample
}

func (r *recordingRecorder) Record(s reliability.Sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.samples = append(r.samples, s)
}

func (r *recordingRecorder) get() []reliability.Sample {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]reliability.Sample, len(r.samples))
	copy(out, r.samples)
	return out
}

func TestTurn_RecordsReliabilitySamplesOnSuccess(t *testing.T) {
	rec := &recordingRecorder{}
	prov := &stubProvider{
		responses: []Response{
			{
				Message:    Message{Role: RoleAssistant, Content: []Content{{Kind: ContentText, Text: "hi"}}},
				StopReason: StopEndTurn,
			},
		},
	}
	a := New(prov, tools.NewRegistry(), silentLogger(), Options{Reliability: rec})
	// NewSession takes a Title; ID is auto-generated. Capture it
	// after construction so the assertion tracks whatever the
	// session-store's ID scheme is today.
	s := NewSession("test-title")
	sessID := s.ID
	require.NotEmpty(t, sessID, "session ID must be auto-populated")
	s.Append(NewUserText("hello"))

	_, err := a.Turn(context.Background(), s)
	require.NoError(t, err)

	samples := rec.get()
	require.Len(t, samples, 5, "successful Turn must emit five samples: latency + safety turn + fault stratification + tokens + tool-call count")

	// Latency sample: consistency dimension, resource_cv_latency,
	// value ≥ 0ms, bucketed + tagged by session id.
	var latency, safety *reliability.Sample
	for i := range samples {
		s := &samples[i]
		switch s.SubMetric {
		case "resource_cv_latency":
			latency = s
		case "turn":
			safety = s
		}
	}
	require.NotNil(t, latency, "latency sample not emitted")
	require.NotNil(t, safety, "safety turn sample not emitted")

	assert.Equal(t, reliability.DimConsistency, latency.Dimension)
	assert.GreaterOrEqual(t, latency.Value, 0.0)
	assert.Equal(t, sessID, latency.SessionID)
	assert.Equal(t, sessID, latency.Bucket, "bucket must equal session id so per-session CV rolls up correctly")

	assert.Equal(t, reliability.DimSafety, safety.Dimension)
	assert.Equal(t, 1.0, safety.Value, "clean completion must record safety turn = 1")
	assert.Equal(t, sessID, safety.SessionID)
}

func TestTurn_RecordsSafetyZeroOnError(t *testing.T) {
	rec := &recordingRecorder{}
	// Empty session → ErrEmptySession from turn().
	a := New(&stubProvider{}, tools.NewRegistry(), silentLogger(), Options{Reliability: rec})
	_, err := a.Turn(context.Background(), NewSession("sess-fail"))
	require.ErrorIs(t, err, ErrEmptySession)

	samples := rec.get()
	require.Len(t, samples, 5, "failed Turn must still emit all samples so the reliability window sees the failure")

	for _, s := range samples {
		if s.SubMetric == "turn" {
			assert.Equal(t, 0.0, s.Value,
				"a turn that returned an error must record safety turn = 0")
			return
		}
	}
	t.Fatal("safety turn sample not found among emitted samples")
}

func TestTurn_NilRecorderIsSafe(t *testing.T) {
	// Zero-value Options has nil Reliability. Turn must not panic
	// or attempt to nil-deref. Baseline invariant — every
	// pre-Phase-2.3 caller keeps working unchanged.
	prov := &stubProvider{
		responses: []Response{
			{
				Message:    Message{Role: RoleAssistant, Content: []Content{{Kind: ContentText, Text: "hi"}}},
				StopReason: StopEndTurn,
			},
		},
	}
	a := New(prov, tools.NewRegistry(), silentLogger(), Options{})
	s := NewSession("x")
	s.Append(NewUserText("hello"))
	assert.NotPanics(t, func() {
		_, err := a.Turn(context.Background(), s)
		assert.NoError(t, err)
	})
}

func TestTurn_HandlesNilSession(t *testing.T) {
	// recordReliabilitySamples must not panic on a nil session —
	// it's called from a defer-like position after emitTerminal
	// (already nil-safe). Direct-call test since Turn itself
	// would deref the session earlier.
	rec := &recordingRecorder{}
	a := New(&stubProvider{}, tools.NewRegistry(), silentLogger(), Options{Reliability: rec})
	assert.NotPanics(t, func() {
		a.recordReliabilitySamples(nil, time.Now(), errors.New("boom"), nil, Message{})
	})
	// Samples still emitted with empty SessionID + Bucket.
	samples := rec.get()
	require.Len(t, samples, 3)
	for _, s := range samples {
		assert.Empty(t, s.SessionID, "nil session yields empty SessionID")
	}
}

// -- turnStats accumulation -----------------------------------------

// TestTurn_AccumulatesTokensAndToolCalls verifies the two new
// Consistency C_res sub-metrics (tokens, tool_calls) are emitted
// with values matching the actual per-turn accumulation across
// every provider.Complete round-trip.
func TestTurn_AccumulatesTokensAndToolCalls(t *testing.T) {
	rec := &recordingRecorder{}
	// Two provider round-trips: first triggers a tool call
	// (StopToolUse with one tool_use content block), second
	// completes the turn (StopEndTurn). Total tokens = 30+70 =
	// 100 in + 20+40 = 60 out. Tool calls = 1.
	registry := tools.NewRegistry()
	require.NoError(t, registry.Register(&stubTool{name: "echo", out: "pong"}))
	prov := &stubProvider{
		responses: []Response{
			{
				Message: Message{
					Role: RoleAssistant,
					Content: []Content{
						{Kind: ContentToolUse, ToolUse: &ToolUse{ID: "t1", Name: "echo", Input: []byte(`{"in":"ping"}`)}},
					},
				},
				StopReason: StopToolUse,
				Usage:      Usage{InputTokens: 30, OutputTokens: 20},
			},
			{
				Message: Message{
					Role:    RoleAssistant,
					Content: []Content{{Kind: ContentText, Text: "done"}},
				},
				StopReason: StopEndTurn,
				Usage:      Usage{InputTokens: 70, OutputTokens: 40},
			},
		},
	}
	a := New(prov, registry, silentLogger(), Options{Reliability: rec})
	s := NewSession("t-tokens")
	s.Append(NewUserText("go"))

	_, err := a.Turn(context.Background(), s)
	require.NoError(t, err)

	var tokens, calls *reliability.Sample
	for i := range rec.get() {
		s := &rec.get()[i]
		switch s.SubMetric {
		case "resource_cv_tokens":
			tokens = s
		case "resource_cv_calls":
			calls = s
		}
	}
	require.NotNil(t, tokens)
	require.NotNil(t, calls)
	assert.Equal(t, 160.0, tokens.Value, "accumulator must sum input+output tokens across every round-trip")
	assert.Equal(t, 1.0, calls.Value, "tool_use content blocks in a StopToolUse response count as tool calls")
}

// -- confidence elicitation ------------------------------------------

func TestParseConfidence_TableDriven(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		want  float64
		found bool
	}{
		{"canonical form", "reply body\n<confidence>0.85</confidence>", 0.85, true},
		{"missing tag", "reply body without tag", 0, false},
		{"integer 1", "<confidence>1</confidence>", 1.0, true},
		{"integer 0", "<confidence>0</confidence>", 0.0, true},
		{"decimal without leading zero", "<confidence>.95</confidence>", 0.95, true},
		{"whitespace tolerated", "<confidence>  0.5  </confidence>", 0.5, true},
		{"malformed value", "<confidence>abc</confidence>", 0, false},
		{"multiple tags — last wins", "<confidence>0.3</confidence> then <confidence>0.9</confidence>", 0.9, true},
		{"clamped above 1", "<confidence>1.5</confidence>", 1.0, true},
		{"clamped below 0", "<confidence>-0.3</confidence>", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseConfidence(tc.text)
			assert.Equal(t, tc.found, ok)
			if tc.found {
				assert.InDelta(t, tc.want, got, 1e-9)
			}
		})
	}
}

func TestFinalText_ConcatenatesTextBlocks(t *testing.T) {
	m := Message{
		Role: RoleAssistant,
		Content: []Content{
			{Kind: ContentText, Text: "first"},
			{Kind: ContentToolUse, ToolUse: &ToolUse{Name: "x"}},
			{Kind: ContentText, Text: "second"},
		},
	}
	assert.Equal(t, "first\nsecond", finalText(m))
}

func TestFinalText_EmptyMessage(t *testing.T) {
	assert.Empty(t, finalText(Message{}))
}

func TestTurn_ConfidenceElicitationAppendsPromptAndEmitsPair(t *testing.T) {
	rec := &recordingRecorder{}
	prov := &stubProvider{
		responses: []Response{
			{
				Message: Message{
					Role: RoleAssistant,
					Content: []Content{
						{Kind: ContentText, Text: "here you go\n<confidence>0.75</confidence>"},
					},
				},
				StopReason: StopEndTurn,
			},
		},
	}
	a := New(prov, tools.NewRegistry(), silentLogger(), Options{
		Reliability:                 rec,
		EnableConfidenceElicitation: true,
	})
	s := NewSession("conf-test")
	s.Append(NewUserText("go"))
	_, err := a.Turn(context.Background(), s)
	require.NoError(t, err)

	// A pair sample must have fired with value=0.75 and outcome=1.
	var pair *reliability.Sample
	for i := range rec.get() {
		if rec.get()[i].SubMetric == "pair" {
			pair = &rec.get()[i]
			break
		}
	}
	require.NotNil(t, pair, "confidence elicitation on + <confidence> tag present must emit a Predictability pair sample")
	assert.Equal(t, reliability.DimPredictability, pair.Dimension)
	assert.InDelta(t, 0.75, pair.Value, 1e-9)
	assert.Equal(t, "1", pair.Metadata["outcome"], "clean turn = outcome 1")
}

func TestTurn_ConfidenceElicitationOffDoesNotEmitPair(t *testing.T) {
	rec := &recordingRecorder{}
	prov := &stubProvider{
		responses: []Response{
			{
				Message: Message{
					Role: RoleAssistant,
					Content: []Content{
						{Kind: ContentText, Text: "hi <confidence>0.9</confidence>"},
					},
				},
				StopReason: StopEndTurn,
			},
		},
	}
	// EnableConfidenceElicitation left false — even if the model
	// happens to emit the tag (verbatim from a prior conversation
	// / hardcoded), we must NOT emit a pair sample.
	a := New(prov, tools.NewRegistry(), silentLogger(), Options{Reliability: rec})
	s := NewSession("no-conf")
	s.Append(NewUserText("go"))
	_, err := a.Turn(context.Background(), s)
	require.NoError(t, err)

	for _, s := range rec.get() {
		assert.NotEqual(t, "pair", s.SubMetric,
			"pair sample must only fire when EnableConfidenceElicitation is true")
	}
}

func TestTurn_ConfidenceElicitationMissingTagIsSilent(t *testing.T) {
	// Model didn't emit the tag despite the instruction → no
	// pair sample, no error. Some turns (tool-heavy, aborted)
	// legitimately don't reach the closing instruction.
	rec := &recordingRecorder{}
	prov := &stubProvider{
		responses: []Response{
			{
				Message: Message{
					Role:    RoleAssistant,
					Content: []Content{{Kind: ContentText, Text: "done"}},
				},
				StopReason: StopEndTurn,
			},
		},
	}
	a := New(prov, tools.NewRegistry(), silentLogger(), Options{
		Reliability:                 rec,
		EnableConfidenceElicitation: true,
	})
	s := NewSession("no-tag")
	s.Append(NewUserText("go"))
	_, err := a.Turn(context.Background(), s)
	require.NoError(t, err)

	for _, s := range rec.get() {
		assert.NotEqual(t, "pair", s.SubMetric,
			"missing tag means no pair sample — legitimate for tool-heavy turns")
	}
}

// -- fault classifier ------------------------------------------------

// TestFaultObservedFromError_TableDriven locks in the heuristic that
// separates "provider network glitch" from "agent logic error" so the
// Robustness fault-ratio has a meaningful "faulted" stratum.
func TestFaultObservedFromError_TableDriven(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, "false"},
		{"generic non-fault", errors.New("model refused"), "false"},
		{"provider timeout", errors.New("provider: timeout"), "true"},
		{"deadline exceeded", errors.New("context deadline exceeded"), "true"},
		{"connection refused", errors.New("dial tcp: connection refused"), "true"},
		{"no such host DNS failure", errors.New("no such host: api.example"), "true"},
		{"tool exec exit status", errors.New("tool: exit status 127"), "true"},
		{"subprocess crash", errors.New("subprocess killed"), "true"},
		{"MCP EOF", errors.New("MCP: read frame: EOF"), "true"},
		{"case-insensitive", errors.New("Provider: TIMEOUT dialing api"), "true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, faultObservedFromError(tc.err))
		})
	}
}
