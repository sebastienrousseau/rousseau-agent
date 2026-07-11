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
	t.Cleanup(func() { _ = s.Close() })
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
