package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// TestSearchBySender_IsScopedToTheSender pins /find's isolation: a
// sender only ever sees hits from their own sessions, and an empty
// sender sees nothing rather than everything.
func TestSearchBySender_IsScopedToTheSender(t *testing.T) {
	s := openSearchTestStore(t)
	ctx := context.Background()
	for _, who := range []string{"alice", "bob"} {
		sess := agent.NewSession(who + " chat")
		sess.Sender = who
		sess.Append(agent.NewUserText("notes on the quarterly budget"))
		require.NoError(t, s.Save(ctx, sess))
	}

	hits, err := s.SearchBySender(ctx, "alice", "budget", SearchOptions{})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, "alice chat", hits[0].Title)
	assert.Contains(t, hits[0].Snippet, "budget")

	hits, err = s.SearchBySender(ctx, "", "budget", SearchOptions{})
	require.NoError(t, err)
	assert.Empty(t, hits, "no sender, no results")

	hits, err = s.SearchBySender(ctx, "carol", "budget", SearchOptions{Limit: 5, SnippetChars: 64})
	require.NoError(t, err)
	assert.Empty(t, hits)

	_, err = s.SearchBySender(ctx, "alice", "   ", SearchOptions{})
	assert.ErrorContains(t, err, "empty search query")
}

// TestClosedStore_Errors pins that the v0.0.12 storage paths surface a
// database failure instead of reporting success.
func TestClosedStore_Errors(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	j, err := NewTurnJournal(ctx, s)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	_, err = NewTurnJournal(ctx, s)
	assert.ErrorContains(t, err, "turn journal")
	assert.ErrorContains(t, j.Begin(ctx, "wa", "a", "hi"), "turn journal begin")
	assert.ErrorContains(t, j.End(ctx, "wa", "a"), "turn journal end")
	_, err = j.TakeInterrupted(ctx, "wa")
	assert.ErrorContains(t, err, "turn journal")

	_, err = s.EraseSender(ctx, "a")
	assert.ErrorContains(t, err, "erase")
	_, err = s.EraseIdleSessions(ctx, time.Now())
	assert.ErrorContains(t, err, "erase")
	_, err = s.SearchBySender(ctx, "a", "x", SearchOptions{})
	assert.ErrorContains(t, err, "search by sender")
}

func TestEraseSender_RejectsEmptySender(t *testing.T) {
	s := openSearchTestStore(t)
	_, err := s.EraseSender(context.Background(), "  ")
	assert.ErrorContains(t, err, "empty sender")
}
