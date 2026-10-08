package transport

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// countingRunner records how often a turn ran and what the session
// ended with when it started.
type countingRunner struct {
	calls   int
	lastWas agent.Role
	reply   agent.Message
	err     error
}

func (c *countingRunner) Turn(_ context.Context, sess *agent.Session) (agent.Message, error) {
	c.calls++
	c.lastWas = sess.Messages[len(sess.Messages)-1].Role
	if c.err != nil {
		return agent.Message{}, c.err
	}
	sess.Append(c.reply)
	return c.reply, nil
}

// seedCheckpoint stores a session for sender "a" on transport
// "whatsapp" whose turn stopped after a tool iteration.
func seedCheckpoint(t *testing.T, store *memStore, jid *memJID) *agent.Session {
	t.Helper()
	sess := agent.NewSession("chat")
	sess.Append(agent.NewUserText("clean the build"))
	sess.Append(agent.Message{Role: agent.RoleAssistant, Content: []agent.Content{
		{Kind: agent.ContentToolUse, ToolUse: &agent.ToolUse{ID: "t1", Name: "bash", Input: json.RawMessage(`{}`)}},
	}})
	sess.Append(agent.Message{Role: agent.RoleUser, Content: []agent.Content{
		{Kind: agent.ContentToolResult, ToolResult: &agent.ToolResult{ToolUseID: "t1", Output: "ok"}},
	}})
	require.NoError(t, store.Save(context.Background(), sess))
	require.NoError(t, jid.Put(context.Background(), "whatsapp:a", sess.ID))
	return sess
}

func TestRouter_ResumeContinuesFromCheckpoint(t *testing.T) {
	store, jid, j := newMemStore(), newMemJID(), &memJournal{}
	sess := seedCheckpoint(t, store, jid)
	runner := &countingRunner{reply: agent.NewAssistantText("build cleaned")}
	r := NewRouter(runner, store, jid, silentLogger(), RouterOptions{Transport: "whatsapp", TurnJournal: j})

	reply, err := r.Resume(context.Background(), "a")
	require.NoError(t, err)
	assert.Equal(t, "build cleaned", reply)
	assert.Equal(t, 1, runner.calls)
	assert.Equal(t, agent.RoleUser, runner.lastWas, "the model continues after the last tool result")
	assert.Len(t, sess.Messages, 4)
	assert.Equal(t, []string{"whatsapp|a|(resumed after restart)"}, j.begun, "a resumed turn is journalled too")
	assert.Equal(t, []string{"whatsapp|a"}, j.ended)
}

// A session that already ends with the reply means only delivery was
// lost: the reply is returned without running the model again.
func TestRouter_ResumeRedeliversFinishedReply(t *testing.T) {
	store, jid := newMemStore(), newMemJID()
	sess := seedCheckpoint(t, store, jid)
	sess.Append(agent.NewAssistantText("already done"))
	runner := &countingRunner{err: errors.New("must not run")}
	r := NewRouter(runner, store, jid, silentLogger(), RouterOptions{Transport: "whatsapp"})

	reply, err := r.Resume(context.Background(), "a")
	require.NoError(t, err)
	assert.Equal(t, "already done", reply)
	assert.Zero(t, runner.calls)
}

func TestRouter_ResumeNothingToResume(t *testing.T) {
	store, jid := newMemStore(), newMemJID()
	r := NewRouter(&countingRunner{}, store, jid, silentLogger(), RouterOptions{Transport: "whatsapp"})
	_, err := r.Resume(context.Background(), "nobody")
	assert.ErrorIs(t, err, ErrNothingToResume)

	empty := agent.NewSession("chat")
	require.NoError(t, store.Save(context.Background(), empty))
	require.NoError(t, jid.Put(context.Background(), "whatsapp:b", empty.ID))
	_, err = r.Resume(context.Background(), "b")
	assert.ErrorIs(t, err, ErrNothingToResume)
}

func TestRouter_ResumeLookupFailures(t *testing.T) {
	store, jid := newMemStore(), newMemJID()
	require.NoError(t, jid.Put(context.Background(), "whatsapp:a", "gone"))
	r := NewRouter(&countingRunner{}, store, jid, silentLogger(), RouterOptions{Transport: "whatsapp"})
	_, err := r.Resume(context.Background(), "a")
	require.Error(t, err, "a mapping to a missing session is an error")
	assert.NotErrorIs(t, err, ErrNothingToResume)

	jid.err = errors.New("db down")
	_, err = r.Resume(context.Background(), "a")
	assert.ErrorContains(t, err, "db down")
}

// A resume that fails still saves whatever the turn appended.
func TestRouter_ResumeFailureSavesAndReports(t *testing.T) {
	base := newMemStore()
	store := &snapshotStore{memStore: base}
	jid := newMemJID()
	seedCheckpoint(t, base, jid)
	r := NewRouter(&countingRunner{err: errors.New("provider down")}, store, jid, silentLogger(), RouterOptions{Transport: "whatsapp"})
	_, err := r.Resume(context.Background(), "a")
	require.ErrorContains(t, err, "router: resume: provider down")
	assert.Len(t, store.saves, 1, "progress is saved even when the resumed turn fails")
}
