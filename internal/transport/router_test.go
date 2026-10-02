package transport

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
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

// TestRouter_IdleSessionRotates pins the stale-thread guard: a message
// arriving after SessionIdleTimeout lands in a fresh session (so "yes"
// cannot approve a days-old question), the reply names the previous
// session, and /resume can still reach it.
func TestRouter_IdleSessionRotates(t *testing.T) {
	store := newMemStore()
	jid := newMemJID()
	runner := &stubRunner{reply: agent.NewAssistantText("ok")}
	r := NewRouter(runner, store, jid, silentLogger(), RouterOptions{SessionIdleTimeout: time.Hour})
	ctx := context.Background()

	_, err := r.Handle(ctx, IncomingMessage{From: "x", Body: "want the rating?"})
	require.NoError(t, err)
	oldID, _, _ := jid.Get(ctx, "x") //nolint:errcheck // asserted below via store
	require.NotEmpty(t, oldID)

	// Within the window: same session, no notice.
	r.now = func() time.Time { return time.Now().Add(59 * time.Minute) }
	reply, err := r.Handle(ctx, IncomingMessage{From: "x", Body: "still here"})
	require.NoError(t, err)
	assert.Equal(t, "ok", reply)
	sameID, _, _ := jid.Get(ctx, "x") //nolint:errcheck // equality is the assertion
	assert.Equal(t, oldID, sameID)

	// Past the window: fresh session, notice names the old one.
	r.now = func() time.Time { return time.Now().Add(3 * time.Hour) }
	reply, err = r.Handle(ctx, IncomingMessage{From: "x", Body: "yes"})
	require.NoError(t, err)
	newID, _, _ := jid.Get(ctx, "x") //nolint:errcheck // inequality is the assertion
	assert.NotEqual(t, oldID, newID)
	assert.Contains(t, reply, "/resume "+shortSessionID(oldID))
	assert.True(t, strings.HasSuffix(reply, "ok"), reply)
	fresh := store.sessions[newID].Messages
	require.NotEmpty(t, fresh)
	assert.Equal(t, "yes", fresh[0].Content[0].Text, "fresh session starts at the new message, not the stale thread")
	assert.Contains(t, store.sessions, oldID, "previous session is kept for /resume")
}

func TestRouter_IdleRotationDisabledByZero(t *testing.T) {
	store := newMemStore()
	jid := newMemJID()
	runner := &stubRunner{reply: agent.NewAssistantText("ok")}
	r := NewRouter(runner, store, jid, silentLogger(), RouterOptions{})
	ctx := context.Background()

	_, err := r.Handle(ctx, IncomingMessage{From: "x", Body: "a"})
	require.NoError(t, err)
	r.now = func() time.Time { return time.Now().Add(30 * 24 * time.Hour) }
	reply, err := r.Handle(ctx, IncomingMessage{From: "x", Body: "b"})
	require.NoError(t, err)
	assert.Equal(t, "ok", reply)
	assert.Len(t, store.sessions, 1)
}

// TestRouter_ResumeOfIdleSessionSticks pins that /resume counts as
// activity: resuming a session older than SessionIdleTimeout must not
// rotate away on the very next message (the notice would otherwise
// point at a /resume that can never stick).
func TestRouter_ResumeOfIdleSessionSticks(t *testing.T) {
	store := newMemStore()
	jid := newMemJID()
	runner := &stubRunner{reply: agent.NewAssistantText("ok")}
	r := NewRouter(runner, store, jid, silentLogger(), RouterOptions{SessionIdleTimeout: time.Hour})
	ctx := context.Background()

	_, err := r.Handle(ctx, IncomingMessage{From: "x", Body: "old thread"})
	require.NoError(t, err)
	oldID, _, _ := jid.Get(ctx, "x") //nolint:errcheck // asserted via equality below

	r.now = func() time.Time { return time.Now().Add(3 * time.Hour) }
	_, err = r.Handle(ctx, IncomingMessage{From: "x", Body: "new topic"}) // rotates
	require.NoError(t, err)

	reply, err := r.Handle(ctx, IncomingMessage{From: "x", Body: "/resume " + shortSessionID(oldID)})
	require.NoError(t, err)
	require.Contains(t, reply, "resumed session")

	reply, err = r.Handle(ctx, IncomingMessage{From: "x", Body: "continue"})
	require.NoError(t, err)
	assert.Equal(t, "ok", reply, "no rotation notice right after /resume")
	gotID, _, _ := jid.Get(ctx, "x") //nolint:errcheck // equality is the assertion
	assert.Equal(t, oldID, gotID)
}

func TestRouter_AllowedAndSSOEnabled(t *testing.T) {
	r := NewRouter(&stubRunner{}, newMemStore(), newMemJID(), silentLogger(),
		RouterOptions{Allowlist: []string{"a"}})
	assert.True(t, r.Allowed(context.Background(), "a"))
	assert.False(t, r.Allowed(context.Background(), "b"))
	assert.False(t, r.SSOEnabled(), "no SSO directory configured")
}

// TestRouter_SaveForksProviderHistory pins that /save hands the
// provider the (source, snapshot) ids so providers that keep their own
// history (claudecli) can copy it; a failure is reported, not hidden.
func TestRouter_SaveForksProviderHistory(t *testing.T) {
	var gotFrom, gotTo string
	r := NewRouter(&stubRunner{reply: agent.NewAssistantText("ok")}, newMemStore(), newMemJID(), silentLogger(),
		RouterOptions{ForkSession: func(_ context.Context, from, to string) error {
			gotFrom, gotTo = from, to
			return nil
		}})
	ctx := context.Background()
	_, err := r.Handle(ctx, IncomingMessage{From: "x", Body: "hello"})
	require.NoError(t, err)
	current, _, _ := r.jidMap.Get(ctx, "x") //nolint:errcheck // equality is the assertion

	reply, err := r.Handle(ctx, IncomingMessage{From: "x", Body: "/save mine"})
	require.NoError(t, err)
	assert.Equal(t, current, gotFrom)
	assert.NotEmpty(t, gotTo)
	assert.NotEqual(t, gotFrom, gotTo)
	assert.Contains(t, reply, shortSessionID(gotTo))

	r2 := NewRouter(&stubRunner{reply: agent.NewAssistantText("ok")}, newMemStore(), newMemJID(), silentLogger(),
		RouterOptions{ForkSession: func(context.Context, string, string) error { return errors.New("disk full") }})
	_, err = r2.Handle(ctx, IncomingMessage{From: "x", Body: "hello"})
	require.NoError(t, err)
	reply, err = r2.Handle(ctx, IncomingMessage{From: "x", Body: "/save"})
	require.NoError(t, err)
	assert.Contains(t, reply, "without the model's history")
}

type memJournal struct {
	mu    sync.Mutex
	begun []string
	ended []string
}

func (m *memJournal) Begin(_ context.Context, transport, sender, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.begun = append(m.begun, transport+"|"+sender+"|"+body)
	return nil
}

func (m *memJournal) End(_ context.Context, transport, sender string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ended = append(m.ended, transport+"|"+sender)
	return nil
}

// TestRouter_JournalsAgentTurns pins the interrupted-turn record: an
// agent turn is journalled before it runs and cleared after, so a
// restart in between can be reported to the sender. Synchronous
// commands are not journalled.
func TestRouter_JournalsAgentTurns(t *testing.T) {
	j := &memJournal{}
	runner := &stubRunner{reply: agent.NewAssistantText("ok")}
	r := NewRouter(runner, newMemStore(), newMemJID(), silentLogger(),
		RouterOptions{Transport: "whatsapp", TurnJournal: j})
	_, err := r.Handle(context.Background(), IncomingMessage{From: "a", Body: "deploy staging"})
	require.NoError(t, err)
	_, err = r.Handle(context.Background(), IncomingMessage{From: "a", Body: "/version"})
	require.NoError(t, err)
	assert.Equal(t, []string{"whatsapp|a|deploy staging"}, j.begun)
	assert.Equal(t, []string{"whatsapp|a"}, j.ended)
}
