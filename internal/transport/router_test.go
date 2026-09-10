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
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
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

// ListBySender satisfies the widened SessionStore interface. The
// existing tests don't exercise the /sessions verb so a simple
// scan is fine; if new tests need better performance we can
// index on write.
func (m *memStore) ListBySender(_ context.Context, sender string, limit int) ([]state.Summary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []state.Summary
	for _, s := range m.sessions {
		if s.Sender != sender {
			continue
		}
		out = append(out, state.Summary{
			ID: s.ID, Title: s.Title, MessageCount: len(s.Messages),
			UpdatedAt: s.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		})
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Delete satisfies the widened SessionStore interface.
func (m *memStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
	return nil
}

// SearchBySender satisfies the widened SessionStore interface for
// the /find chat verb. Naive substring scan over titles + message
// bodies — the real store uses FTS5 / tsvector; this fake exists
// only so router tests wire without a real DB.
func (m *memStore) SearchBySender(_ context.Context, sender, query string, opts sqlitestore.SearchOptions) ([]sqlitestore.SearchHit, error) {
	if sender == "" || query == "" {
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []sqlitestore.SearchHit
	for _, s := range m.sessions {
		if s.Sender != sender {
			continue
		}
		out = append(out, sqlitestore.SearchHit{
			SessionID: s.ID,
			Title:     s.Title,
			Snippet:   query,
			UpdatedAt: s.UpdatedAt.UTC(),
		})
	}
	if opts.Limit > 0 && len(out) > opts.Limit {
		out = out[:opts.Limit]
	}
	return out, nil
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

// capturingRunner records the session state at Turn-time. Lets the
// mixed-content tests assert the exact Content slice the router
// appended before delegating to the agent loop.
type capturingRunner struct {
	seen []agent.Content
}

func (c *capturingRunner) Turn(_ context.Context, sess *agent.Session) (agent.Message, error) {
	if n := len(sess.Messages); n > 0 {
		c.seen = sess.Messages[n-1].Content
	}
	reply := agent.NewAssistantText("ok")
	sess.Append(reply)
	return reply, nil
}

func TestBuildUserMessage_TextAndImageBecomeMixedContent(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	got, ok := buildUserMessage(IncomingMessage{
		From: "wa:1",
		Body: "look at this",
		Attachments: []Attachment{
			{MediaType: "image/png", Data: png},
		},
	}, "whatsapp")
	require.True(t, ok)
	require.Len(t, got.Content, 2)
	assert.Equal(t, agent.ContentText, got.Content[0].Kind)
	assert.Equal(t, "look at this", got.Content[0].Text)
	assert.Equal(t, agent.ContentImage, got.Content[1].Kind)
	require.NotNil(t, got.Content[1].Image)
	assert.Equal(t, "image/png", got.Content[1].Image.MediaType)
	assert.Equal(t, "whatsapp", got.Content[1].Image.Source)
	assert.Equal(t, png, got.Content[1].Image.Data)
}

func TestBuildUserMessage_ImageOnlyOmitsTextBlock(t *testing.T) {
	got, ok := buildUserMessage(IncomingMessage{
		From: "wa:1",
		Attachments: []Attachment{
			{MediaType: "image/jpeg", Data: []byte{0xFF, 0xD8, 0xFF}},
		},
	}, "whatsapp")
	require.True(t, ok)
	require.Len(t, got.Content, 1, "no text → no text block")
	assert.Equal(t, agent.ContentImage, got.Content[0].Kind)
}

func TestBuildUserMessage_EmptyDataAttachmentSkipped(t *testing.T) {
	got, ok := buildUserMessage(IncomingMessage{
		From: "wa:1",
		Body: "just words",
		Attachments: []Attachment{
			{MediaType: "image/png", Data: nil}, // dropped
		},
	}, "whatsapp")
	require.True(t, ok)
	require.Len(t, got.Content, 1)
	assert.Equal(t, agent.ContentText, got.Content[0].Kind)
}

func TestBuildUserMessage_NothingToSayReturnsFalse(t *testing.T) {
	_, ok := buildUserMessage(IncomingMessage{From: "wa:1"}, "whatsapp")
	assert.False(t, ok, "empty body + no attachments → nothing to append")
}

func TestRouter_HandleImageAttachmentReachesRunner(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	store := newMemStore()
	jid := newMemJID()
	runner := &capturingRunner{}
	r := NewRouter(runner, store, jid, silentLogger(), RouterOptions{Transport: "whatsapp"})

	_, err := r.Handle(context.Background(), IncomingMessage{
		From:        "1234@s.whatsapp.net",
		Body:        "what is this?",
		Attachments: []Attachment{{MediaType: "image/png", Data: png}},
	})
	require.NoError(t, err)

	// The runner saw text + image on the last appended user message.
	require.Len(t, runner.seen, 2)
	assert.Equal(t, agent.ContentText, runner.seen[0].Kind)
	assert.Equal(t, agent.ContentImage, runner.seen[1].Kind)
	require.NotNil(t, runner.seen[1].Image)
	assert.Equal(t, "whatsapp", runner.seen[1].Image.Source, "transport name must attribute the image")
}
