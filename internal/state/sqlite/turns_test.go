package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTurnJournal pins the interrupted-turn record: a started turn is
// listed until it ends, per transport, and taking the list clears it.
func TestTurnJournal(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	j, err := NewTurnJournal(ctx, s)
	require.NoError(t, err)

	require.NoError(t, j.Begin(ctx, "whatsapp", "a@s.whatsapp.net", "deploy the staging branch please"))
	require.NoError(t, j.Begin(ctx, "whatsapp", "b@s.whatsapp.net", "hi"))
	require.NoError(t, j.Begin(ctx, "telegram", "42", "x"))
	require.NoError(t, j.End(ctx, "whatsapp", "b@s.whatsapp.net"))

	got, err := j.TakeInterrupted(ctx, "whatsapp")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "a@s.whatsapp.net", got[0].Sender)
	assert.Equal(t, "deploy the staging branch please", got[0].Preview)

	again, err := j.TakeInterrupted(ctx, "whatsapp")
	require.NoError(t, err)
	assert.Empty(t, again, "taking clears the record")
	tg, err := j.TakeInterrupted(ctx, "telegram")
	require.NoError(t, err)
	assert.Len(t, tg, 1, "other transports keep theirs")
}

func TestTurnJournal_PreviewIsBounded(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	j, err := NewTurnJournal(ctx, s)
	require.NoError(t, err)
	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'x'
	}
	require.NoError(t, j.Begin(ctx, "whatsapp", "a", string(long)))
	got, err := j.TakeInterrupted(ctx, "whatsapp")
	require.NoError(t, err)
	assert.LessOrEqual(t, len([]rune(got[0].Preview)), 121)
}
