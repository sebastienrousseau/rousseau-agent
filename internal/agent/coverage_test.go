package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// covTool implements tools.Tool for testing runTools branches.
type covTool struct {
	name   string
	desc   string
	out    string
	err    error
	called int
}

func (s *covTool) Name() string { return s.name }
func (s *covTool) Description() string {
	if s.desc == "" {
		return s.name
	}
	return s.desc
}
func (s *covTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (s *covTool) Execute(context.Context, json.RawMessage) (string, error) {
	s.called++
	return s.out, s.err
}

// TestTurn_ToolLoopThenEnd walks a two-iteration Turn: first
// response is tool_use, second is end_turn. Exercises the runTools
// path + append of tool results + loop continuation.
func TestTurn_ToolLoopThenEnd(t *testing.T) {
	provider := &stubProvider{responses: []Response{
		{
			Message: Message{Role: RoleAssistant, Content: []Content{
				{Kind: ContentToolUse, ToolUse: &ToolUse{
					ID: "t1", Name: "read", Input: json.RawMessage(`{"path":"/x"}`),
				}},
			}},
			StopReason: StopToolUse,
		},
		{
			Message:    NewAssistantText("done"),
			StopReason: StopEndTurn,
		},
	}}
	registry := tools.NewRegistry()
	require.NoError(t, registry.Register(&covTool{name: "read", out: "hello world"}))

	ag := New(provider, registry,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		Options{MaxIterations: 4, Approver: newAllowAllApprover()})

	sess := NewSession("t")
	sess.Append(NewUserText("read the file"))
	reply, err := ag.Turn(context.Background(), sess)
	require.NoError(t, err)
	assert.Equal(t, "done", reply.Content[0].Text)
}

// TestTurn_ToolDeniedByApprover verifies the deny branch: runTools
// records a tool_result marked as an error and continues the loop.
func TestTurn_ToolDeniedByApprover(t *testing.T) {
	provider := &stubProvider{responses: []Response{
		{
			Message: Message{Role: RoleAssistant, Content: []Content{
				{Kind: ContentToolUse, ToolUse: &ToolUse{
					ID: "t1", Name: "bash", Input: json.RawMessage(`{"cmd":"rm -rf /"}`),
				}},
			}},
			StopReason: StopToolUse,
		},
		{
			Message:    NewAssistantText("ok, backing off"),
			StopReason: StopEndTurn,
		},
	}}
	registry := tools.NewRegistry()
	require.NoError(t, registry.Register(&covTool{name: "bash", out: "should not run"}))

	ag := New(provider, registry,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		Options{MaxIterations: 4, Approver: &denyAllApprover{}})

	sess := NewSession("t")
	sess.Append(NewUserText("do bad thing"))
	_, err := ag.Turn(context.Background(), sess)
	require.NoError(t, err)
	// A tool_result content with IsError=true was appended.
	found := false
	for _, m := range sess.Messages {
		for _, c := range m.Content {
			if c.Kind == ContentToolResult && c.ToolResult != nil && c.ToolResult.IsError {
				found = true
			}
		}
	}
	assert.True(t, found, "denied tool should append an is_error tool_result")
}

// TestTurn_UnknownToolErrors covers the ErrToolNotFound branch.
func TestTurn_UnknownToolErrors(t *testing.T) {
	provider := &stubProvider{responses: []Response{{
		Message: Message{Role: RoleAssistant, Content: []Content{
			{Kind: ContentToolUse, ToolUse: &ToolUse{
				ID: "t1", Name: "nonexistent",
				Input: json.RawMessage(`{}`),
			}},
		}},
		StopReason: StopToolUse,
	}}}
	ag := New(provider, tools.NewRegistry(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		Options{MaxIterations: 4, Approver: newAllowAllApprover()})
	sess := NewSession("t")
	sess.Append(NewUserText("call unknown"))
	_, err := ag.Turn(context.Background(), sess)
	assert.ErrorIs(t, err, ErrToolNotFound)
}

// TestTurn_ToolExecutionErrorAppendsResult covers the tool-error
// branch — Execute returns an error, run loop records a tool_result
// with IsError=true and continues.
func TestTurn_ToolExecutionErrorAppendsResult(t *testing.T) {
	provider := &stubProvider{responses: []Response{
		{
			Message: Message{Role: RoleAssistant, Content: []Content{
				{Kind: ContentToolUse, ToolUse: &ToolUse{
					ID: "t1", Name: "read", Input: json.RawMessage(`{"path":"/x"}`),
				}},
			}},
			StopReason: StopToolUse,
		},
		{
			Message:    NewAssistantText("ok"),
			StopReason: StopEndTurn,
		},
	}}
	registry := tools.NewRegistry()
	require.NoError(t, registry.Register(&covTool{name: "read", err: errors.New("permission denied")}))

	ag := New(provider, registry,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		Options{MaxIterations: 4, Approver: newAllowAllApprover()})
	sess := NewSession("t")
	sess.Append(NewUserText("read /x"))
	_, err := ag.Turn(context.Background(), sess)
	require.NoError(t, err)
	found := false
	for _, m := range sess.Messages {
		for _, c := range m.Content {
			if c.Kind == ContentToolResult && c.ToolResult != nil && c.ToolResult.IsError {
				found = true
				assert.Contains(t, c.ToolResult.Output, "permission denied")
			}
		}
	}
	assert.True(t, found)
}

// TestTurn_MaxIterations covers the ErrMaxIterations branch — every
// response is tool_use so the loop hits its cap.
func TestTurn_MaxIterations2(t *testing.T) {
	provider := &stubProvider{responses: []Response{
		{Message: Message{Role: RoleAssistant, Content: []Content{
			{Kind: ContentToolUse, ToolUse: &ToolUse{ID: "t", Name: "read", Input: json.RawMessage(`{}`)}}}}, StopReason: StopToolUse},
		{Message: Message{Role: RoleAssistant, Content: []Content{
			{Kind: ContentToolUse, ToolUse: &ToolUse{ID: "t", Name: "read", Input: json.RawMessage(`{}`)}}}}, StopReason: StopToolUse},
	}}
	registry := tools.NewRegistry()
	require.NoError(t, registry.Register(&covTool{name: "read", out: "ok"}))
	ag := New(provider, registry,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		Options{MaxIterations: 2, Approver: newAllowAllApprover()})
	sess := NewSession("t")
	sess.Append(NewUserText("call read"))
	_, err := ag.Turn(context.Background(), sess)
	assert.ErrorIs(t, err, ErrMaxIterations)
}

// TestTurn_NonToolStopReasonReturnsMessage covers the branch where
// StopReason is neither end_turn nor tool_use — the loop returns the
// message as-is (truncated / other).
func TestTurn_NonToolStopReasonReturnsMessage(t *testing.T) {
	provider := &stubProvider{responses: []Response{{
		Message:    NewAssistantText("truncated"),
		StopReason: StopMaxTokens,
	}}}
	ag := New(provider, tools.NewRegistry(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		Options{MaxIterations: 4, Approver: newAllowAllApprover()})
	sess := NewSession("t")
	sess.Append(NewUserText("hi"))
	reply, err := ag.Turn(context.Background(), sess)
	require.NoError(t, err)
	assert.Equal(t, "truncated", reply.Content[0].Text)
}

// TestTurn_CompressorErrorNonFatal ensures a Compressor error is
// logged but doesn't fail the turn.
func TestTurn_CompressorErrorNonFatal(t *testing.T) {
	provider := &stubProvider{responses: []Response{{
		Message: NewAssistantText("hello"), StopReason: StopEndTurn,
	}}}
	ag := New(provider, tools.NewRegistry(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		Options{
			MaxIterations: 4,
			Approver:      newAllowAllApprover(),
			Compressor:    CompressorFunc(func(context.Context, *Session) (bool, error) { return false, errors.New("oops") }),
		})
	sess := NewSession("t")
	sess.Append(NewUserText("hi"))
	_, err := ag.Turn(context.Background(), sess)
	assert.NoError(t, err)
}

// TestSystemPrompt_ComposesEveryProvider covers the multi-provider
// branch of systemPrompt.
func TestSystemPrompt_ComposesEveryProvider(t *testing.T) {
	provider := &stubProvider{responses: []Response{{
		Message: NewAssistantText("ok"), StopReason: StopEndTurn,
	}}}
	skills := SkillsProviderFunc(func(*Session) string { return "SKILLS APPENDIX" })
	recall := RecallProviderFunc(func(context.Context, *Session) string { return "RECALL APPENDIX" })
	ag := New(provider, tools.NewRegistry(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		Options{
			MaxIterations:  4,
			Approver:       newAllowAllApprover(),
			SystemPrompt:   "BASE",
			SkillsProvider: skills,
			RecallProvider: recall,
		})
	sess := NewSession("t")
	sess.Append(NewUserText("hi"))
	_, err := ag.Turn(context.Background(), sess)
	require.NoError(t, err)
}

// SkillsProviderFunc + RecallProviderFunc are function-typed
// stand-ins used to drive the SystemPrompt composition branches
// without wiring the real providers.
type SkillsProviderFunc func(*Session) string

func (f SkillsProviderFunc) SystemAppendix(s *Session) string { return f(s) }

type RecallProviderFunc func(context.Context, *Session) string

func (f RecallProviderFunc) SystemAppendix(ctx context.Context, s *Session) string {
	return f(ctx, s)
}

// denyAllApprover fires the deny branch of runTools.
type denyAllApprover struct{}

func (denyAllApprover) Approve(context.Context, ApprovalRequest) (Decision, string) {
	return DecisionDeny, "policy denies bash"
}

func newAllowAllApprover() Approver { return &allowAllApprover{} }

type allowAllApprover struct{}

func (allowAllApprover) Approve(context.Context, ApprovalRequest) (Decision, string) {
	return DecisionAllow, ""
}
