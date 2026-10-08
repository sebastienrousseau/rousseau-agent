package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func journalFixture(t *testing.T) (*Store, *TurnJournal, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.db")
	s, err := Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	j, err := NewTurnJournal(ctx, s)
	require.NoError(t, err)
	return s, j, path
}

func journalRows(t *testing.T, s *Store, transport, sender string) int {
	t.Helper()
	var n int
	require.NoError(t, s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM turns_inflight WHERE transport = ? AND sender = ?`, transport, sender).Scan(&n))
	return n
}

// L-8: the turn journal stores a preview of the sender's message; a
// namespaced erasure removes that transport's row only.
func TestEraseSender_ClearsTurnJournal(t *testing.T) {
	ctx := context.Background()
	s, j, _ := journalFixture(t)
	require.NoError(t, j.Begin(ctx, "signal", "+447700900001", "my secret plan"))
	require.NoError(t, j.Begin(ctx, "signal", "+447700900002", "someone else"))
	require.NoError(t, j.Begin(ctx, "telegram", "+447700900001", "other transport"))

	rep, err := s.EraseSender(ctx, "signal:+447700900001")
	require.NoError(t, err)
	assert.Zero(t, journalRows(t, s, "signal", "+447700900001"), "the erased sender's message preview must go")
	assert.Equal(t, int64(1), rep.Rows["turns_inflight"])
	assert.Equal(t, 1, journalRows(t, s, "signal", "+447700900002"))
	assert.Equal(t, 1, journalRows(t, s, "telegram", "+447700900001"), "another transport's row stays")
}

func TestEraseSender_BareKeyClearsTurnJournalOnEveryTransport(t *testing.T) {
	ctx := context.Background()
	s, j, _ := journalFixture(t)
	require.NoError(t, j.Begin(ctx, "signal", "bob", "a"))
	require.NoError(t, j.Begin(ctx, "telegram", "bob", "b"))
	require.NoError(t, j.Begin(ctx, "telegram", "carol", "c"))

	rep, err := s.EraseSender(ctx, "bob")
	require.NoError(t, err)
	assert.Equal(t, int64(2), rep.Rows["turns_inflight"])
	assert.Equal(t, 1, journalRows(t, s, "telegram", "carol"))
}

// L-8: migration backups next to the database still hold the erased
// rows; the report names them so the operator can delete them.
func TestEraseSender_ReportsMigrationBackups(t *testing.T) {
	ctx := context.Background()
	s, _, path := journalFixture(t)
	dir := filepath.Dir(path)
	want := []string{path + ".pre-down-20260102T000000Z", path + ".pre-v2-20260101T000000Z"}
	for _, p := range append([]string{filepath.Join(dir, "other.db.pre-v2-20260101T000000Z")}, want...) {
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
	}
	rep, err := s.EraseSender(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, want, rep.Backups)
}

func TestEraseSender_NoBackupsForMemoryStore(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	rep, err := s.EraseSender(context.Background(), "alice")
	require.NoError(t, err)
	assert.Empty(t, rep.Backups)
}
