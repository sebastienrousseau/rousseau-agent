package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability/audit_egress"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// captureAuditSink records every emitted record — same shape
// as the router-side helper but kept package-local so the two
// test files don't cross-import.
type captureAuditSink struct {
	mu      sync.Mutex
	records []audit_egress.Record
}

func (c *captureAuditSink) Emit(_ context.Context, r audit_egress.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r)
	return nil
}
func (c *captureAuditSink) Close(context.Context) error { return nil }
func (c *captureAuditSink) snapshot() []audit_egress.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]audit_egress.Record, len(c.records))
	copy(out, c.records)
	return out
}

func TestRunTools_SuccessEmitsToolCallSuccessRecord(t *testing.T) {
	provider := &stubProvider{responses: []Response{
		{Message: Message{Role: RoleAssistant, Content: []Content{
			{Kind: ContentToolUse, ToolUse: &ToolUse{
				ID: "t1", Name: "read", Input: json.RawMessage(`{"path":"/x"}`),
			}},
		}}, StopReason: StopToolUse},
		{Message: NewAssistantText("done"), StopReason: StopEndTurn},
	}}
	registry := tools.NewRegistry()
	require.NoError(t, registry.Register(&covTool{name: "read", out: "hi"}))

	audit := &captureAuditSink{}
	ag := New(provider, registry, silentLogger(), Options{
		MaxIterations: 4, Approver: newAllowAllApprover(), AuditSink: audit,
	})

	sess := NewSession("t")
	sess.Append(NewUserText("go"))
	_, err := ag.Turn(context.Background(), sess)
	require.NoError(t, err)

	recs := audit.snapshot()
	require.Len(t, recs, 1)
	assert.Equal(t, "tool_call", recs[0].Category)
	assert.Equal(t, "run", recs[0].Verb)
	assert.Equal(t, "read", recs[0].Object)
	assert.Equal(t, "success", recs[0].Result)
	assert.Equal(t, sess.ID, recs[0].Detail["session_id"])
	assert.Contains(t, recs[0].Detail, "elapsed_ms")
}

func TestRunTools_ExecutionErrorEmitsErrorRecord(t *testing.T) {
	// Tool executed but returned an error — audit must land
	// with result="error" and the error string in Detail.
	provider := &stubProvider{responses: []Response{
		{Message: Message{Role: RoleAssistant, Content: []Content{
			{Kind: ContentToolUse, ToolUse: &ToolUse{
				ID: "t1", Name: "bash", Input: json.RawMessage(`{"cmd":"exit 1"}`),
			}},
		}}, StopReason: StopToolUse},
		{Message: NewAssistantText("noted"), StopReason: StopEndTurn},
	}}
	registry := tools.NewRegistry()
	require.NoError(t, registry.Register(&covTool{name: "bash", err: errors.New("boom")}))

	audit := &captureAuditSink{}
	ag := New(provider, registry, silentLogger(), Options{
		MaxIterations: 4, Approver: newAllowAllApprover(), AuditSink: audit,
	})

	sess := NewSession("t")
	sess.Append(NewUserText("run bash"))
	_, err := ag.Turn(context.Background(), sess)
	require.NoError(t, err)

	recs := audit.snapshot()
	require.Len(t, recs, 1)
	assert.Equal(t, "error", recs[0].Result)
	assert.Equal(t, "boom", recs[0].Detail["error"])
}

func TestRunTools_ApproverDenyEmitsDeniedRecord(t *testing.T) {
	// Approver denies before execution — audit MUST land so the
	// operator can alert on suspicious tool-use attempts.
	provider := &stubProvider{responses: []Response{
		{Message: Message{Role: RoleAssistant, Content: []Content{
			{Kind: ContentToolUse, ToolUse: &ToolUse{
				ID: "t1", Name: "bash", Input: json.RawMessage(`{"cmd":"rm -rf /"}`),
			}},
		}}, StopReason: StopToolUse},
		{Message: NewAssistantText("ok"), StopReason: StopEndTurn},
	}}
	registry := tools.NewRegistry()
	require.NoError(t, registry.Register(&covTool{name: "bash"}))

	audit := &captureAuditSink{}
	ag := New(provider, registry, silentLogger(), Options{
		MaxIterations: 4, Approver: &denyAllApprover{}, AuditSink: audit,
	})

	sess := NewSession("t")
	sess.Append(NewUserText("do bad thing"))
	_, err := ag.Turn(context.Background(), sess)
	require.NoError(t, err)

	recs := audit.snapshot()
	require.Len(t, recs, 1)
	assert.Equal(t, "deny", recs[0].Verb)
	assert.Equal(t, "denied", recs[0].Result)
	assert.Equal(t, "bash", recs[0].Object)
	assert.Contains(t, recs[0].Detail, "reason")
}

func TestRunTools_ActorFromSSOContext(t *testing.T) {
	// Property: when ctx carries an SSO identity, the audit
	// record's Actor names the subject. Otherwise "anonymous".
	provider := &stubProvider{responses: []Response{
		{Message: Message{Role: RoleAssistant, Content: []Content{
			{Kind: ContentToolUse, ToolUse: &ToolUse{
				ID: "t1", Name: "read", Input: json.RawMessage(`{}`),
			}},
		}}, StopReason: StopToolUse},
		{Message: NewAssistantText("done"), StopReason: StopEndTurn},
	}}
	registry := tools.NewRegistry()
	require.NoError(t, registry.Register(&covTool{name: "read", out: "hi"}))

	audit := &captureAuditSink{}
	ag := New(provider, registry, silentLogger(), Options{
		MaxIterations: 4, Approver: newAllowAllApprover(), AuditSink: audit,
	})

	ctx := sso.WithIdentity(context.Background(), sso.Identity{Subject: "okta|alice"})
	sess := NewSession("t")
	sess.Append(NewUserText("go"))
	_, err := ag.Turn(ctx, sess)
	require.NoError(t, err)

	recs := audit.snapshot()
	require.Len(t, recs, 1)
	assert.Equal(t, "okta|alice", recs[0].Actor)
}

func TestRunTools_AnonymousActorWhenCtxHasNoIdentity(t *testing.T) {
	// Explicit test for the fallback so future ctx-shape changes
	// can't quietly regress it.
	provider := &stubProvider{responses: []Response{
		{Message: Message{Role: RoleAssistant, Content: []Content{
			{Kind: ContentToolUse, ToolUse: &ToolUse{
				ID: "t1", Name: "read", Input: json.RawMessage(`{}`),
			}},
		}}, StopReason: StopToolUse},
		{Message: NewAssistantText("done"), StopReason: StopEndTurn},
	}}
	registry := tools.NewRegistry()
	require.NoError(t, registry.Register(&covTool{name: "read", out: "hi"}))

	audit := &captureAuditSink{}
	ag := New(provider, registry, silentLogger(), Options{
		MaxIterations: 4, Approver: newAllowAllApprover(), AuditSink: audit,
	})

	sess := NewSession("t")
	sess.Append(NewUserText("go"))
	_, err := ag.Turn(context.Background(), sess)
	require.NoError(t, err)

	recs := audit.snapshot()
	require.Len(t, recs, 1)
	assert.Equal(t, "anonymous", recs[0].Actor)
}

func TestRunTools_NilSinkIsSafe(t *testing.T) {
	// Property: opt-out sink means opt-out — no panic, no
	// side-effects.
	provider := &stubProvider{responses: []Response{
		{Message: Message{Role: RoleAssistant, Content: []Content{
			{Kind: ContentToolUse, ToolUse: &ToolUse{
				ID: "t1", Name: "read", Input: json.RawMessage(`{}`),
			}},
		}}, StopReason: StopToolUse},
		{Message: NewAssistantText("done"), StopReason: StopEndTurn},
	}}
	registry := tools.NewRegistry()
	require.NoError(t, registry.Register(&covTool{name: "read", out: "hi"}))

	ag := New(provider, registry, silentLogger(), Options{
		MaxIterations: 4, Approver: newAllowAllApprover(),
		// AuditSink omitted
	})

	require.NotPanics(t, func() {
		sess := NewSession("t")
		sess.Append(NewUserText("go"))
		_, err := ag.Turn(context.Background(), sess)
		require.NoError(t, err)
	})
}
