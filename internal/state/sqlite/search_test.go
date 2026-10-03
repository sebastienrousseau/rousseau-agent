package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

func openSearchTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	return s
}

func TestSearch_EmptyQueryErrors(t *testing.T) {
	s := openSearchTestStore(t)
	_, err := s.Search(context.Background(), "  ", SearchOptions{})
	assert.Error(t, err)
}

func TestSearch_FindsMatchInSessionPayload(t *testing.T) {
	s := openSearchTestStore(t)
	sess := agent.NewSession("kubernetes primer")
	sess.Append(agent.NewUserText("how do I debug a pod stuck in CrashLoopBackOff?"))
	require.NoError(t, s.Save(context.Background(), sess))

	hits, err := s.Search(context.Background(), "CrashLoopBackOff", SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	assert.Equal(t, sess.ID, hits[0].SessionID)
}

func TestSearch_NoMatchesReturnsEmpty(t *testing.T) {
	s := openSearchTestStore(t)
	sess := agent.NewSession("empty")
	sess.Append(agent.NewUserText("hello"))
	require.NoError(t, s.Save(context.Background(), sess))

	hits, err := s.Search(context.Background(), "kubernetes", SearchOptions{})
	require.NoError(t, err)
	assert.Empty(t, hits)
}

func TestRecentSessions(t *testing.T) {
	s := openSearchTestStore(t)
	for _, title := range []string{"first", "second", "third"} {
		sess := agent.NewSession(title)
		require.NoError(t, s.Save(context.Background(), sess))
	}
	recent, err := s.RecentSessions(context.Background(), 2)
	require.NoError(t, err)
	assert.Len(t, recent, 2)
}

func TestSearch_HandlesFTS5PhraseSyntax(t *testing.T) {
	s := openSearchTestStore(t)
	sess := agent.NewSession("phrase")
	sess.Append(agent.NewUserText("the quick brown fox jumps"))
	require.NoError(t, s.Save(context.Background(), sess))

	hits, err := s.Search(context.Background(), `"quick brown"`, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
}

// TestSearch_IndexesMessageTextOnly pins that the FTS index holds the
// conversation's text, not its JSON payload: JSON keys and base64
// image bytes used to be indexed, bloating the index and making /find
// match words like "role" in every session.
func TestSearch_IndexesMessageTextOnly(t *testing.T) {
	s := openSearchTestStore(t)
	ctx := context.Background()
	sess := agent.NewSession("chat")
	sess.Append(agent.Message{Role: agent.RoleUser, Content: []agent.Content{
		{Kind: agent.ContentText, Text: "why is my kubernetes pod pending"},
		{Kind: agent.ContentImage, Image: &agent.Image{MediaType: "image/png", Data: []byte("QUJDREVGR0hJSktMTU5PUFFSU1RVVldY")}},
	}})
	require.NoError(t, s.Save(ctx, sess))

	hits, err := s.Search(ctx, "kubernetes", SearchOptions{})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	for _, noise := range []string{"role", "content", "media_type", "UVVJTREVGR0hJSktMTU5PUFFSU1RVVldY"} {
		hits, err := s.Search(ctx, noise, SearchOptions{})
		require.NoError(t, err)
		assert.Empty(t, hits, "%q must not be indexed", noise)
	}
}

// TestSearch_MigratesPayloadIndexedDatabase pins the one-time upgrade
// of a database indexed the old way (raw payload): on open, sessions
// are re-indexed from their text.
func TestSearch_MigratesPayloadIndexedDatabase(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/s.db"
	s, err := Open(ctx, path)
	require.NoError(t, err)
	sess := agent.NewSession("chat")
	sess.Append(agent.NewUserText("tell me about helm charts"))
	require.NoError(t, s.Save(ctx, sess))
	// Recreate the pre-migration state: payload in the index, no
	// extracted text, schema version 0.
	for _, q := range []string{
		`UPDATE sessions SET search_text = ''`,
		`DELETE FROM sessions_fts`,
		`INSERT INTO sessions_fts (session_id, title, body) SELECT id, title, payload FROM sessions`,
		`PRAGMA user_version = 0`,
	} {
		_, err := s.db.ExecContext(ctx, q)
		require.NoError(t, err, q)
	}
	hits, err := s.Search(ctx, "role", SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, hits, "precondition: the old index matches JSON keys")
	require.NoError(t, s.Close())

	s, err = Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	hits, err = s.Search(ctx, "role", SearchOptions{})
	require.NoError(t, err)
	assert.Empty(t, hits, "migrated index no longer matches JSON keys")
	hits, err = s.Search(ctx, "helm", SearchOptions{})
	require.NoError(t, err)
	assert.Len(t, hits, 1, "text is still found after the migration")
}
