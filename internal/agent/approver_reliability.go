package agent

import (
	"context"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/reliability"
)

// RecordingApprover wraps another Approver and emits a Safety
// violation sample on every DecisionDeny. The paper's Safety
// axis (arXiv:2602.16666 §3.4) is a live-measurable dimension
// straight out of the approver decision stream — every denial
// is a policy violation the model attempted, so counting them
// is the compliance-rate ground truth.
//
// Design:
//
//   - Wraps rather than replaces so any existing chain
//     (RBAC → OPA → MultiParty → base) keeps working. The
//     wrapper sits on the outside of the chain — see
//     wrapWithReliability in cli/approver.go.
//   - Non-denials (DecisionAllow) do NOT emit a sample. That
//     would double-count against the "turn" safety sample
//     agent.Turn already emits.
//   - Severity defaults to "medium" — the sensible fallback
//     when the approver kind isn't known. Callers who want
//     "high" for RBAC-role-violations or "low" for pattern-
//     matches can construct their own wrappers with a
//     different Severity field.
//   - Nil recorder makes RecordingApprover a pure passthrough
//     via the NopRecorder contract.
type RecordingApprover struct {
	// Inner is the wrapped approver. Its Approve verdict is
	// returned verbatim; the recorder only observes.
	Inner Approver
	// Recorder receives Safety violation samples on denial.
	// Nil → no telemetry, wrapper is a pure passthrough.
	Recorder reliability.Recorder
	// Severity tags every emitted violation. Empty defaults
	// to "medium" per the paper's convention (low = 0.25,
	// medium = 0.5, high = 1.0 weight).
	Severity string
	// Constraint labels the policy that fired. Empty defaults
	// to "approver-deny". Used as Metadata["constraint"] so
	// operators can filter Prometheus counters by rule.
	Constraint string
}

// Approve satisfies the Approver interface. Delegates to Inner
// and emits a Safety violation sample when the verdict is
// DecisionDeny.
func (r *RecordingApprover) Approve(ctx context.Context, req ApprovalRequest) (Decision, string) {
	decision, reason := r.Inner.Approve(ctx, req)
	if decision != DecisionDeny || r.Recorder == nil {
		return decision, reason
	}
	severity := r.Severity
	if severity == "" {
		severity = "medium"
	}
	constraint := r.Constraint
	if constraint == "" {
		constraint = "approver-deny"
	}
	r.Recorder.Record(reliability.Sample{
		At:        time.Now(),
		Dimension: reliability.DimSafety,
		SubMetric: "violation",
		Value:     0,
		SessionID: req.SessionID,
		Metadata: map[string]string{
			"severity":   severity,
			"constraint": constraint,
			"tool":       req.ToolName,
			// Truncate reason so a large denial explanation
			// doesn't bloat the samples table.
			"reason": truncateReason(reason, 200),
		},
	})
	return decision, reason
}

func truncateReason(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Byte-truncation is fine here — reasons are ASCII in
	// practice and the sample store is not a UI surface.
	return s[:n] + "…"
}
