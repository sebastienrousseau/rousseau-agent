package cli

import (
	"context"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent/approval"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability/audit_egress"
)

// approvalAuditAdapter adapts the app-level audit sink to the
// approval package's [approval.AuditEmitter] contract so
// pending-approval lifecycle events land in the same tamper-
// evident chain as the rest of the daemon's audit trail.
//
// Kept as a tiny type in cli so the approval package doesn't
// import audit_egress directly — matches the same layering the
// SSO / agent-tool-call emissions use.
type approvalAuditAdapter struct {
	sink audit_egress.Sink
}

func newApprovalAuditAdapter(sink audit_egress.Sink) *approvalAuditAdapter {
	return &approvalAuditAdapter{sink: sink}
}

// EmitApprovalRequest satisfies [approval.AuditEmitter].
func (a *approvalAuditAdapter) EmitApprovalRequest(ctx context.Context, rec approval.PendingRecord) {
	if a == nil || a.sink == nil {
		return
	}
	_ = a.sink.Emit(ctx, audit_egress.Record{ //nolint:errcheck // best-effort; sink counters authoritative
		Category: "approval",
		Actor:    rec.Requester,
		Verb:     "request",
		Object:   rec.Tool,
		Result:   "pending",
		Detail: map[string]any{
			"token":            rec.Token,
			"needed_approvals": rec.NeededCount,
			"expires_at":       rec.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			"session_id":       rec.SessionID,
		},
	})
}

// EmitApprovalVote satisfies [approval.AuditEmitter].
func (a *approvalAuditAdapter) EmitApprovalVote(ctx context.Context, token, approver string, verdict approval.Verdict, counted bool) {
	if a == nil || a.sink == nil {
		return
	}
	result := "counted"
	if !counted {
		// Self-approve attempts land here — a security-
		// interesting audit event even though the vote is
		// rejected.
		result = "not_counted"
	}
	_ = a.sink.Emit(ctx, audit_egress.Record{ //nolint:errcheck // best-effort
		Category: "approval",
		Actor:    approver,
		Verb:     string(verdict),
		Object:   token,
		Result:   result,
	})
}

// EmitApprovalResolved satisfies [approval.AuditEmitter].
func (a *approvalAuditAdapter) EmitApprovalResolved(ctx context.Context, rec approval.PendingRecord, verdict approval.Verdict) {
	if a == nil || a.sink == nil {
		return
	}
	_ = a.sink.Emit(ctx, audit_egress.Record{ //nolint:errcheck // best-effort
		Category: "approval",
		Actor:    rec.Requester,
		Verb:     "resolve",
		Object:   rec.Tool,
		Result:   string(verdict),
		Detail: map[string]any{
			"token":            rec.Token,
			"votes_counted":    len(rec.Votes),
			"needed_approvals": rec.NeededCount,
			"session_id":       rec.SessionID,
		},
	})
}

// Compile-time interface satisfaction.
var _ approval.AuditEmitter = (*approvalAuditAdapter)(nil)
