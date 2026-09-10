package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/reliability"
)

// Regression tests for the RecordingApprover wrapper — Phase 2.3
// Safety instrumentation. Every branch of the emit-on-deny path is
// asserted so a future refactor cannot silently drop violation
// samples the aggregator's compliance / severity metrics depend on.

// approverStub is a controllable Approver for tests — returns whatever
// verdict + reason is configured.
type approverStub struct {
	decision Decision
	reason   string
	calls    int
	mu       sync.Mutex
}

func (a *approverStub) Approve(_ context.Context, _ ApprovalRequest) (Decision, string) {
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	return a.decision, a.reason
}

// captureRecorder captures every sample the wrapper emits so tests
// can assert on the exact metadata shape.
type captureRecorder struct {
	mu      sync.Mutex
	samples []reliability.Sample
}

func (c *captureRecorder) Record(s reliability.Sample) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samples = append(c.samples, s)
}

func (c *captureRecorder) got() []reliability.Sample {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]reliability.Sample, len(c.samples))
	copy(out, c.samples)
	return out
}

func TestRecordingApprover_AllowIsPassthrough(t *testing.T) {
	inner := &approverStub{decision: DecisionAllow, reason: ""}
	rec := &captureRecorder{}
	w := &RecordingApprover{Inner: inner, Recorder: rec}

	d, r := w.Approve(context.Background(), ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, DecisionAllow, d)
	assert.Empty(t, r)
	assert.Empty(t, rec.got(), "allow verdicts must not emit a violation sample")
	assert.Equal(t, 1, inner.calls)
}

func TestRecordingApprover_DenyEmitsViolationSample(t *testing.T) {
	inner := &approverStub{decision: DecisionDeny, reason: "rbac: role not permitted"}
	rec := &captureRecorder{}
	w := &RecordingApprover{
		Inner:      inner,
		Recorder:   rec,
		Severity:   "high",
		Constraint: "rbac-role",
	}

	d, r := w.Approve(context.Background(), ApprovalRequest{
		ToolName:  "bash",
		SessionID: "sess-42",
	})
	assert.Equal(t, DecisionDeny, d)
	assert.Equal(t, "rbac: role not permitted", r)

	samples := rec.got()
	require.Len(t, samples, 1)
	s := samples[0]
	assert.Equal(t, reliability.DimSafety, s.Dimension)
	assert.Equal(t, "violation", s.SubMetric)
	assert.Equal(t, 0.0, s.Value)
	assert.Equal(t, "sess-42", s.SessionID)
	assert.Equal(t, "high", s.Metadata["severity"])
	assert.Equal(t, "rbac-role", s.Metadata["constraint"])
	assert.Equal(t, "bash", s.Metadata["tool"])
	assert.Equal(t, "rbac: role not permitted", s.Metadata["reason"])
}

func TestRecordingApprover_DefaultSeverityAndConstraint(t *testing.T) {
	inner := &approverStub{decision: DecisionDeny, reason: "no"}
	rec := &captureRecorder{}
	// Severity + Constraint left empty → default to medium /
	// approver-deny per the wrapper's contract.
	w := &RecordingApprover{Inner: inner, Recorder: rec}

	w.Approve(context.Background(), ApprovalRequest{})
	samples := rec.got()
	require.Len(t, samples, 1)
	assert.Equal(t, "medium", samples[0].Metadata["severity"])
	assert.Equal(t, "approver-deny", samples[0].Metadata["constraint"])
}

func TestRecordingApprover_NilRecorderPassthrough(t *testing.T) {
	// Zero-value Recorder = pure passthrough (no telemetry, no panic).
	// Baseline invariant: pre-Phase-2.3 callers constructing the
	// wrapper without a recorder still get correct behaviour.
	inner := &approverStub{decision: DecisionDeny, reason: "no"}
	w := &RecordingApprover{Inner: inner, Recorder: nil}

	assert.NotPanics(t, func() {
		d, r := w.Approve(context.Background(), ApprovalRequest{})
		assert.Equal(t, DecisionDeny, d)
		assert.Equal(t, "no", r)
	})
}

func TestRecordingApprover_LongReasonIsTruncated(t *testing.T) {
	longReason := strings.Repeat("x", 500)
	inner := &approverStub{decision: DecisionDeny, reason: longReason}
	rec := &captureRecorder{}
	w := &RecordingApprover{Inner: inner, Recorder: rec}

	w.Approve(context.Background(), ApprovalRequest{})
	samples := rec.got()
	require.Len(t, samples, 1)
	reason := samples[0].Metadata["reason"]
	assert.Contains(t, reason, "…", "truncation marker must be appended")
	assert.LessOrEqual(t, len(reason), 210, "truncated reason must be at most ~200 + ellipsis")
}

func TestTruncateReason(t *testing.T) {
	assert.Equal(t, "abc", truncateReason("abc", 10))
	assert.Equal(t, "abc", truncateReason("abc", 3))
	assert.Equal(t, "ab…", truncateReason("abcdef", 2))
}
