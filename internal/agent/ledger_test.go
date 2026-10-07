package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

func toolUseMsg(id, name, input string) Message {
	return Message{Role: RoleAssistant, Content: []Content{{Kind: ContentToolUse, ToolUse: &ToolUse{ID: id, Name: name, Input: json.RawMessage(input)}}}}
}

func toolResultMsg(id string, isErr bool) Message {
	return Message{Role: RoleUser, Content: []Content{{Kind: ContentToolResult, ToolResult: &ToolResult{ToolUseID: id, Output: "x", IsError: isErr}}}}
}

func TestTurnLedger(t *testing.T) {
	assert.Nil(t, TurnLedger(nil))

	s := NewSession("x")
	s.Append(NewUserText("earlier"))
	s.Append(toolUseMsg("old", "bash", `{"command":"ls"}`))
	s.Append(toolResultMsg("old", false))
	s.Append(NewAssistantText("done"))
	s.Append(NewUserText("deploy staging"))
	s.Append(toolUseMsg("a", "bash", `{"command":"make deploy"}`))
	s.Append(toolResultMsg("a", false))
	s.Append(toolUseMsg("b", "write", `{"file_path":"/tmp/notes.md"}`))
	s.Append(toolResultMsg("b", true))
	s.Append(toolUseMsg("c", "edit", `{"file_path":"/tmp/x.go"}`))

	got := TurnLedger(s)
	require.Len(t, got, 3, "only the latest turn's calls; tool results do not start a turn")
	assert.Equal(t, LedgerEntry{Tool: "bash", Detail: "make deploy"}, got[0])
	assert.Equal(t, LedgerEntry{Tool: "write", Detail: "/tmp/notes.md", Failed: true}, got[1])
	assert.Equal(t, LedgerEntry{Tool: "edit", Detail: "/tmp/x.go", Failed: true}, got[2], "no result recorded counts as unfinished")

	empty := NewSession("y")
	empty.Append(NewUserText("hi"))
	assert.Empty(t, TurnLedger(empty))
}

// recordingCheckpoint snapshots the message count at each checkpoint
// and whether the ctx it got was live.
type recordingCheckpoint struct {
	mu     sync.Mutex
	counts []int
	ctxErr []error
	err    error
}

func (r *recordingCheckpoint) fn(ctx context.Context, s *Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts = append(r.counts, len(s.Messages))
	r.ctxErr = append(r.ctxErr, ctx.Err())
	return r.err
}

func toolRoundTrips(n int) *stubProvider {
	var rs []Response
	for i := 0; i < n; i++ {
		rs = append(rs, Response{
			Message:    Message{Role: RoleAssistant, Content: []Content{{Kind: ContentToolUse, ToolUse: &ToolUse{ID: "t", Name: "cat", Input: json.RawMessage(`{}`)}}}},
			StopReason: StopToolUse,
		})
	}
	rs = append(rs, Response{Message: Message{Role: RoleAssistant, Content: []Content{{Kind: ContentText, Text: "done"}}}, StopReason: StopEndTurn})
	return &stubProvider{responses: rs}
}

// Every complete iteration is checkpointed with the tool results in
// place, so a restart loses at most the iteration in flight.
func TestTurn_CheckpointsEveryToolIteration(t *testing.T) {
	cp := &recordingCheckpoint{}
	reg := tools.NewRegistry()
	reg.MustRegister(chattyTool{out: "ok"})
	a := New(toolRoundTrips(2), reg, silentLogger(), Options{Checkpoint: cp.fn})
	s := NewSession("x")
	s.Append(NewUserText("go"))

	_, err := a.Turn(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, []int{3, 5}, cp.counts, "after each tool_use/tool_result pair")
}

// A failing checkpoint is logged, never fatal to the turn.
func TestTurn_CheckpointErrorIsNotFatal(t *testing.T) {
	cp := &recordingCheckpoint{err: errors.New("disk full")}
	reg := tools.NewRegistry()
	reg.MustRegister(chattyTool{out: "ok"})
	a := New(toolRoundTrips(1), reg, silentLogger(), Options{Checkpoint: cp.fn})
	s := NewSession("x")
	s.Append(NewUserText("go"))
	final, err := a.Turn(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, "done", final.Content[0].Text)
	assert.Len(t, cp.counts, 1)
}

// cancelTool cancels the turn from inside its own execution, like a
// /cancel arriving while a tool runs.
type cancelTool struct{ cancel func() }

func (cancelTool) Name() string                { return "stop" }
func (cancelTool) Description() string         { return "cancels the turn" }
func (cancelTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (c cancelTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	c.cancel()
	<-ctx.Done()
	return "", ctx.Err()
}

// A turn cancelled while its tools run still checkpoints the
// (cancelled) results, with a live context.
func TestTurn_CancelledTurnStillCheckpoints(t *testing.T) {
	cp := &recordingCheckpoint{}
	ctx, cancel := context.WithCancel(context.Background())
	reg := tools.NewRegistry()
	reg.MustRegister(cancelTool{cancel: cancel})
	prov := &stubProvider{responses: []Response{{
		Message: Message{Role: RoleAssistant, Content: []Content{
			{Kind: ContentToolUse, ToolUse: &ToolUse{ID: "s1", Name: "stop", Input: json.RawMessage(`{}`)}},
			{Kind: ContentToolUse, ToolUse: &ToolUse{ID: "s2", Name: "stop", Input: json.RawMessage(`{}`)}},
		}},
		StopReason: StopToolUse,
	}}}
	a := New(prov, reg, silentLogger(), Options{Checkpoint: cp.fn})
	s := NewSession("x")
	s.Append(NewUserText("go"))

	_, err := a.Turn(ctx, s)
	require.Error(t, err)
	require.Len(t, cp.counts, 1)
	assert.Equal(t, 3, cp.counts[0], "user, tool_use, and both results (one cancelled)")
	assert.NoError(t, cp.ctxErr[0], "the checkpoint ctx is detached from the turn's cancellation")
}
