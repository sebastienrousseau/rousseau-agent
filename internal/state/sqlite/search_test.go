package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/model"
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
	sess := model.NewSession("kubernetes primer")
	sess.Append(model.NewUserText("how do I debug a pod stuck in CrashLoopBackOff?"))
	require.NoError(t, s.Save(context.Background(), sess))

	hits, err := s.Search(context.Background(), "CrashLoopBackOff", SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	assert.Equal(t, sess.ID, hits[0].SessionID)
}

func TestSearch_NoMatchesReturnsEmpty(t *testing.T) {
	s := openSearchTestStore(t)
	sess := model.NewSession("empty")
	sess.Append(model.NewUserText("hello"))
	require.NoError(t, s.Save(context.Background(), sess))

	hits, err := s.Search(context.Background(), "kubernetes", SearchOptions{})
	require.NoError(t, err)
	assert.Empty(t, hits)
}

func TestRecentSessions(t *testing.T) {
	s := openSearchTestStore(t)
	for _, title := range []string{"first", "second", "third"} {
		sess := model.NewSession(title)
		require.NoError(t, s.Save(context.Background(), sess))
	}
	recent, err := s.RecentSessions(context.Background(), 2)
	require.NoError(t, err)
	assert.Len(t, recent, 2)
}

func TestSearch_HandlesFTS5PhraseSyntax(t *testing.T) {
	s := openSearchTestStore(t)
	sess := model.NewSession("phrase")
	sess.Append(model.NewUserText("the quick brown fox jumps"))
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
	sess := model.NewSession("chat")
	sess.Append(model.Message{Role: model.RoleUser, Content: []model.Content{
		{Kind: model.ContentText, Text: "why is my kubernetes pod pending"},
		{Kind: model.ContentImage, Image: &model.Image{MediaType: "image/png", Data: []byte("QUJDREVGR0hJSktMTU5PUFFSU1RVVldY")}},
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
