package transport

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

type memStore struct {
	mu       sync.Mutex
	sessions map[string]*agent.Session
	saveErr  error
	loadErr  error
}

func newMemStore() *memStore {
	return &memStore{sessions: map[string]*agent.Session{}}
}

func (m *memStore) Save(_ context.Context, s *agent.Session) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.ID] = s
	return nil
}

func (m *memStore) Load(_ context.Context, id string) (*agent.Session, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return s, nil
}

type memJID struct {
	mu   sync.Mutex
	data map[string]string
	err  error
}

func newMemJID() *memJID { return &memJID{data: map[string]string{}} }

func (j *memJID) Get(_ context.Context, jid string) (string, bool, error) {
	if j.err != nil {
		return "", false, j.err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	id, ok := j.data[jid]
	return id, ok, nil
}

func (j *memJID) Put(_ context.Context, jid, id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.data[jid] = id
	return nil
}

type stubRunner struct {
	reply agent.Message
	err   error
}

func (s *stubRunner) Turn(_ context.Context, sess *agent.Session) (agent.Message, error) {
	if s.err != nil {
		return agent.Message{}, s.err
	}
	sess.Append(s.reply)
	return s.reply, nil
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRouter_HandleFirstMessageCreatesSession(t *testing.T) {
	store := newMemStore()
	jid := newMemJID()
	runner := &stubRunner{reply: agent.NewAssistantText("hi back")}

	r := NewRouter(runner, store, jid, silentLogger(), RouterOptions{})

	reply, err := r.Handle(context.Background(), IncomingMessage{
		From: "1234@s.whatsapp.net", Body: "hi", At: time.Now(),
	})
	require.NoError(t, err)
	assert.Equal(t, "hi back", reply)

	id, ok, _ := jid.Get(context.Background(), "1234@s.whatsapp.net") //nolint:errcheck // ok covers the failure path
	assert.True(t, ok)
	assert.NotEmpty(t, id)
	assert.Len(t, store.sessions, 1)
}

func TestRouter_ReusesExistingSession(t *testing.T) {
	store := newMemStore()
	jid := newMemJID()
	runner := &stubRunner{reply: agent.NewAssistantText("ok")}
	r := NewRouter(runner, store, jid, silentLogger(), RouterOptions{})

	_, err := r.Handle(context.Background(), IncomingMessage{From: "x", Body: "a"})
	require.NoError(t, err)
	_, err = r.Handle(context.Background(), IncomingMessage{From: "x", Body: "b"})
	require.NoError(t, err)
	assert.Len(t, store.sessions, 1) // reused, not created
}

func TestRouter_AllowlistBlocks(t *testing.T) {
	store := newMemStore()
	jid := newMemJID()
	runner := &stubRunner{reply: agent.NewAssistantText("x")}

	r := NewRouter(runner, store, jid, silentLogger(), RouterOptions{
		Allowlist: []string{"allowed"},
	})

	reply, err := r.Handle(context.Background(), IncomingMessage{From: "not-allowed", Body: "hi"})
	require.NoError(t, err)
	assert.Empty(t, reply)
	assert.Empty(t, store.sessions)
}

func TestRouter_AllowlistPasses(t *testing.T) {
	store := newMemStore()
	jid := newMemJID()
	runner := &stubRunner{reply: agent.NewAssistantText("yes")}

	r := NewRouter(runner, store, jid, silentLogger(), RouterOptions{
		Allowlist: []string{"allowed"},
	})

	reply, err := r.Handle(context.Background(), IncomingMessage{From: "allowed", Body: "hi"})
	require.NoError(t, err)
	assert.Equal(t, "yes", reply)
}

func TestRouter_RunnerError(t *testing.T) {
	store := newMemStore()
	jid := newMemJID()
	runner := &stubRunner{err: errors.New("boom")}

	r := NewRouter(runner, store, jid, silentLogger(), RouterOptions{})
	_, err := r.Handle(context.Background(), IncomingMessage{From: "x", Body: "hi"})
	assert.Error(t, err)
}

func TestRouter_JIDMapperError(t *testing.T) {
	store := newMemStore()
	jid := newMemJID()
	jid.err = errors.New("db down")

	r := NewRouter(&stubRunner{}, store, jid, silentLogger(), RouterOptions{})
	_, err := r.Handle(context.Background(), IncomingMessage{From: "x", Body: "hi"})
	assert.Error(t, err)
}

func TestRouter_StaleMappingRecovers(t *testing.T) {
	store := newMemStore()
	jid := newMemJID()
	// Pre-seed a mapping to a session that doesn't exist.
	require.NoError(t, jid.Put(context.Background(), "x", "ghost-session"))

	runner := &stubRunner{reply: agent.NewAssistantText("hi")}
	r := NewRouter(runner, store, jid, silentLogger(), RouterOptions{})

	reply, err := r.Handle(context.Background(), IncomingMessage{From: "x", Body: "hello"})
	require.NoError(t, err)
	assert.Equal(t, "hi", reply)
	assert.Len(t, store.sessions, 1) // recovered by creating a new one
}

func TestHandlerFunc(t *testing.T) {
	called := false
	var fn HandlerFunc = func(_ context.Context, msg IncomingMessage) (string, error) {
		called = true
		return msg.Body, nil
	}
	reply, err := fn.Handle(context.Background(), IncomingMessage{Body: "echo"})
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "echo", reply)
}

func TestFirstText_PrefersText(t *testing.T) {
	m := agent.Message{
		Content: []agent.Content{
			{Kind: agent.ContentToolUse},
			{Kind: agent.ContentText, Text: "hello"},
		},
	}
	assert.Equal(t, "hello", firstText(m))
}

func TestFirstText_EmptyReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", firstText(agent.Message{}))
}
