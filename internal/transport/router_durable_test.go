package transport

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// snapshotStore records a copy of every saved session, so a test sees
// what reached storage rather than the live object a turn mutates.
type snapshotStore struct {
	*memStore
	mu    sync.Mutex
	saves []int // message count at each save
}

func (s *snapshotStore) Save(ctx context.Context, sess *agent.Session) error {
	s.mu.Lock()
	s.saves = append(s.saves, len(sess.Messages))
	s.mu.Unlock()
	cp := *sess
	cp.Messages = append([]agent.Message(nil), sess.Messages...)
	return s.memStore.Save(ctx, &cp)
}

func (s *snapshotStore) lastSaved(t *testing.T) *agent.Session {
	t.Helper()
	s.memStore.mu.Lock()
	defer s.memStore.mu.Unlock()
	require.Len(t, s.sessions, 1)
	for _, sess := range s.sessions {
		return sess
	}
	return nil
}

// toolThenFail runs one tool iteration and then fails, like a turn cut
// off by a provider error or a timeout after a side effect.
type toolThenFail struct{}

func (toolThenFail) Turn(_ context.Context, sess *agent.Session) (agent.Message, error) {
	sess.Append(agent.Message{Role: agent.RoleAssistant, Content: []agent.Content{
		{Kind: agent.ContentToolUse, ToolUse: &agent.ToolUse{ID: "t1", Name: "bash", Input: json.RawMessage(`{"command":"rm -rf build"}`)}},
	}})
	sess.Append(agent.Message{Role: agent.RoleUser, Content: []agent.Content{
		{Kind: agent.ContentToolResult, ToolResult: &agent.ToolResult{ToolUseID: "t1", Output: "removed"}},
	}})
	return agent.Message{}, errors.New("provider: overloaded")
}

// A failed turn still saves the sender's message and the tool calls
// that ran, so the next turn's model sees the side effects.
func TestRouter_FailedTurnKeepsMessageAndSideEffects(t *testing.T) {
	store := &snapshotStore{memStore: newMemStore()}
	r := NewRouter(toolThenFail{}, store, newMemJID(), silentLogger(), RouterOptions{})
	_, err := r.Handle(context.Background(), IncomingMessage{From: "a", Body: "clean the build"})
	require.Error(t, err)

	saved := store.lastSaved(t)
	require.Len(t, saved.Messages, 3, "user message, tool call and its result all reached storage")
	assert.Equal(t, "clean the build", saved.Messages[0].Content[0].Text)
	ledger := agent.TurnLedger(saved)
	require.Len(t, ledger, 1)
	assert.Equal(t, "bash", ledger[0].Tool)
	assert.False(t, ledger[0].Failed)
}

// The sender's message is saved before the turn runs, so a crash
// mid-turn cannot lose it.
func TestRouter_SavesUserMessageBeforeTurn(t *testing.T) {
	store := &snapshotStore{memStore: newMemStore()}
	r := NewRouter(&stubRunner{reply: agent.NewAssistantText("ok")}, store, newMemJID(), silentLogger(), RouterOptions{})
	_, err := r.Handle(context.Background(), IncomingMessage{From: "a", Body: "hi"})
	require.NoError(t, err)
	// New empty session, then the user message, then the finished turn.
	assert.Equal(t, []int{0, 1, 2}, store.saves)
}

// A cancelled turn still saves: the save is detached from the turn's
// context.
func TestRouter_CancelledTurnStillSaves(t *testing.T) {
	store := &snapshotStore{memStore: newMemStore()}
	ctx, cancel := context.WithCancel(context.Background())
	runner := &cancellingRunner{cancel: cancel}
	r := NewRouter(runner, store, newMemJID(), silentLogger(), RouterOptions{})
	_, err := r.Handle(ctx, IncomingMessage{From: "a", Body: "long job"})
	require.Error(t, err)
	assert.Len(t, store.lastSaved(t).Messages, 2)
}

type cancellingRunner struct{ cancel func() }

func (c *cancellingRunner) Turn(ctx context.Context, sess *agent.Session) (agent.Message, error) {
	sess.Append(agent.NewAssistantText("partial"))
	c.cancel()
	return agent.Message{}, ctx.Err()
}

// The memStore ignores ctx; this guards that saveSession really detaches.
func TestRouter_SaveSessionDetachesFromCancel(t *testing.T) {
	store := &ctxCheckingStore{memStore: newMemStore()}
	r := NewRouter(&stubRunner{}, store, newMemJID(), silentLogger(), RouterOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.saveSession(ctx, agent.NewSession("x"))
	assert.NoError(t, store.sawErr)
}

type ctxCheckingStore struct {
	*memStore
	sawErr error
}

func (c *ctxCheckingStore) Save(ctx context.Context, s *agent.Session) error {
	c.sawErr = ctx.Err()
	return c.memStore.Save(ctx, s)
}
