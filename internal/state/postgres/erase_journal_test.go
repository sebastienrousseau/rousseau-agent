package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mirrors internal/state/sqlite/erase_journal_test.go on Postgres.

func journalFixture(t *testing.T) (*Store, *TurnJournal) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, isolatedDSN(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	j, err := NewTurnJournal(ctx, s)
	require.NoError(t, err)
	return s, j
}

// L-8: the turn journal stores a preview of the sender's message; a
// namespaced erasure removes that transport's row only.
func TestEraseSender_ClearsTurnJournal(t *testing.T) {
	ctx := context.Background()
	s, j := journalFixture(t)
	require.NoError(t, j.Begin(ctx, "signal", "+447700900001", "my secret plan"))
	require.NoError(t, j.Begin(ctx, "signal", "+447700900002", "someone else"))
	require.NoError(t, j.Begin(ctx, "telegram", "+447700900001", "other transport"))

	rep, err := s.EraseSender(ctx, "signal:+447700900001")
	require.NoError(t, err)
	q := `SELECT COUNT(*) FROM turns_inflight WHERE transport = $1 AND sender = $2`
	assert.Zero(t, count(t, s, q, "signal", "+447700900001"), "the erased sender's message preview must go")
	assert.Equal(t, int64(1), rep.Rows["turns_inflight"])
	assert.Equal(t, 1, count(t, s, q, "signal", "+447700900002"))
	assert.Equal(t, 1, count(t, s, q, "telegram", "+447700900001"), "another transport's row stays")
	assert.Empty(t, rep.Backups, "Postgres backups are operator pg_dumps, not files next to the store")
}

func TestEraseSender_BareKeyClearsTurnJournalOnEveryTransport(t *testing.T) {
	ctx := context.Background()
	s, j := journalFixture(t)
	require.NoError(t, j.Begin(ctx, "signal", "bob", "a"))
	require.NoError(t, j.Begin(ctx, "telegram", "bob", "b"))
	require.NoError(t, j.Begin(ctx, "telegram", "carol", "c"))

	rep, err := s.EraseSender(ctx, "bob")
	require.NoError(t, err)
	assert.Equal(t, int64(2), rep.Rows["turns_inflight"])
	assert.Equal(t, 1, count(t, s, `SELECT COUNT(*) FROM turns_inflight WHERE sender = 'carol'`))
}
