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
	require.Len(t, samples, 3, "successful Turn must emit three samples: latency + safety turn + fault stratification")

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
	require.Len(t, samples, 3, "failed Turn must still emit all three samples so the reliability window sees the failure")

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
		a.recordReliabilitySamples(nil, time.Now(), errors.New("boom"))
	})
	// Samples still emitted with empty SessionID + Bucket.
	samples := rec.get()
	require.Len(t, samples, 3)
	for _, s := range samples {
		assert.Empty(t, s.SessionID, "nil session yields empty SessionID")
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
