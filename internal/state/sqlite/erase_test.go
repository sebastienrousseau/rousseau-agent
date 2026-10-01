package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// TestEraseSender pins GDPR Article 17 erasure: every row tied to the
// sender (sessions and their FTS index, jid mapping, per-session costs
// and claude cache entries) goes, and nobody else's rows are touched.
func TestEraseSender(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	require.NoError(t, s.EnsureSearch(ctx))
	jm, err := NewJIDMap(ctx, s)
	require.NoError(t, err)
	_, err = NewSessionCostStore(ctx, s)
	require.NoError(t, err)
	_, err = NewClaudeSessionCache(ctx, s)
	require.NoError(t, err)

	mk := func(sender, text string) string {
		sess := agent.NewSession("chat")
		sess.Sender = sender
		sess.Append(agent.NewUserText(text))
		require.NoError(t, s.Save(ctx, sess))
		_, err := s.db.ExecContext(ctx, `INSERT INTO session_costs (session_id, at, provider, model) VALUES (?, '2026-01-01T00:00:00Z', 'p', 'm')`, sess.ID)
		require.NoError(t, err)
		_, err = s.db.ExecContext(ctx, `INSERT INTO claude_sessions (session_id, seen_at) VALUES (?, '2026-01-01T00:00:00Z')`, sess.ID)
		require.NoError(t, err)
		return sess.ID
	}
	alice1 := mk("alice", "my secret plan pineapple")
	alice2 := mk("alice", "second thread")
	bob := mk("bob", "bob talks pineapple too")
	require.NoError(t, jm.Put(ctx, "alice", alice1))
	require.NoError(t, jm.Put(ctx, "bob", bob))

	rep, err := s.EraseSender(ctx, "alice")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{alice1, alice2}, rep.SessionIDs)
	assert.Equal(t, int64(2), rep.Rows["sessions"])
	assert.Equal(t, int64(1), rep.Rows["jid_sessions"])
	assert.Equal(t, int64(2), rep.Rows["session_costs"])

	count := func(q string, args ...any) int {
		var n int
		require.NoError(t, s.db.QueryRowContext(ctx, q, args...).Scan(&n))
		return n
	}
	assert.Zero(t, count(`SELECT COUNT(*) FROM sessions WHERE sender = 'alice'`))
	assert.Zero(t, count(`SELECT COUNT(*) FROM sessions_fts WHERE sessions_fts MATCH 'secret'`), "FTS index purged")
	assert.Zero(t, count(`SELECT COUNT(*) FROM jid_sessions WHERE jid = 'alice'`))
	assert.Zero(t, count(`SELECT COUNT(*) FROM session_costs WHERE session_id IN (?, ?)`, alice1, alice2))
	assert.Zero(t, count(`SELECT COUNT(*) FROM claude_sessions WHERE session_id IN (?, ?)`, alice1, alice2))

	assert.Equal(t, 1, count(`SELECT COUNT(*) FROM sessions WHERE sender = 'bob'`), "other senders untouched")
	assert.Equal(t, 1, count(`SELECT COUNT(*) FROM sessions_fts WHERE sessions_fts MATCH 'pineapple'`))
	assert.Equal(t, 1, count(`SELECT COUNT(*) FROM jid_sessions WHERE jid = 'bob'`))

	rep, err = s.EraseSender(ctx, "alice")
	require.NoError(t, err)
	assert.Empty(t, rep.SessionIDs, "idempotent")
}
