package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTurnJournalSchema(t *testing.T) {
	assert.Contains(t, turnJournalSchema, "CREATE TABLE IF NOT EXISTS turns_inflight")
	assert.Contains(t, turnJournalSchema, "PRIMARY KEY (transport, sender)")
}

func TestTurnJournal(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, isolatedDSN(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	j, err := NewTurnJournal(ctx, s)
	require.NoError(t, err)
	_, err = NewTurnJournal(ctx, s)
	require.NoError(t, err, "schema apply is idempotent")

	require.NoError(t, j.Begin(ctx, "whatsapp", "alice", "first  draft\nof a plan"))
	require.NoError(t, j.Begin(ctx, "whatsapp", "alice", "revised plan"))
	require.NoError(t, j.Begin(ctx, "whatsapp", "bob", "bob's turn"))
	require.NoError(t, j.Begin(ctx, "telegram", "carol", "other transport"))
	require.NoError(t, j.End(ctx, "whatsapp", "bob"))

	turns, err := j.TakeInterrupted(ctx, "whatsapp")
	require.NoError(t, err)
	require.Len(t, turns, 1)
	assert.Equal(t, "alice", turns[0].Sender)
	assert.Equal(t, "revised plan", turns[0].Preview, "Begin upserts the latest preview")
	assert.False(t, turns[0].StartedAt.IsZero())

	turns, err = j.TakeInterrupted(ctx, "whatsapp")
	require.NoError(t, err)
	assert.Empty(t, turns, "taking clears the journal")

	turns, err = j.TakeInterrupted(ctx, "telegram")
	require.NoError(t, err)
	require.Len(t, turns, 1)
	assert.Equal(t, "carol", turns[0].Sender)
}

func TestTurnJournal_PreviewIsBounded(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, isolatedDSN(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	j, err := NewTurnJournal(ctx, s)
	require.NoError(t, err)
	require.NoError(t, j.Begin(ctx, "signal", "dave", strings.Repeat("é ", 200)))
	turns, err := j.TakeInterrupted(ctx, "signal")
	require.NoError(t, err)
	require.Len(t, turns, 1)
	assert.Equal(t, previewRunes+1, len([]rune(turns[0].Preview)))
	assert.True(t, strings.HasSuffix(turns[0].Preview, "…"))
}
