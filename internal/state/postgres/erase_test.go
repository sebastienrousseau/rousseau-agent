package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/model"
)

// eraseFixture opens a store in an isolated schema with every
// extension table erasure touches, and returns a seeder that saves a
// session for sender with one cost row and one claude cache row.
func eraseFixture(t *testing.T) (*Store, func(sender, text string) string) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, isolatedDSN(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	for _, mk := range []func(context.Context, *Store) error{
		func(ctx context.Context, s *Store) error { _, err := NewJIDMap(ctx, s); return err },
		func(ctx context.Context, s *Store) error { _, err := NewSessionCostStore(ctx, s); return err },
		func(ctx context.Context, s *Store) error { _, err := NewClaudeSessionCache(ctx, s); return err },
		func(ctx context.Context, s *Store) error { _, err := NewIdentityStore(ctx, s); return err },
		func(ctx context.Context, s *Store) error { _, err := NewSSOBindings(ctx, s); return err },
		func(ctx context.Context, s *Store) error { _, err := NewCronStore(ctx, s); return err },
		func(ctx context.Context, s *Store) error {
			_, err := NewReliabilitySampleStore(ctx, s, nil)
			return err
		},
	} {
		require.NoError(t, mk(ctx, s))
	}
	seed := func(sender, text string) string {
		sess := model.NewSession("chat")
		sess.Sender = sender
		sess.Append(model.NewUserText(text))
		require.NoError(t, s.Save(ctx, sess))
		_, err := s.db.ExecContext(ctx, `INSERT INTO session_costs (session_id, provider, model) VALUES ($1, 'p', 'm')`, sess.ID)
		require.NoError(t, err)
		_, err = s.db.ExecContext(ctx, `INSERT INTO claude_sessions (session_id) VALUES ($1)`, sess.ID)
		require.NoError(t, err)
		return sess.ID
	}
	return s, seed
}

func count(t *testing.T, s *Store, q string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, s.db.QueryRowContext(context.Background(), q, args...).Scan(&n))
	return n
}

// Every row tied to the sender goes, and nobody else's rows are
// touched.
func TestEraseSender(t *testing.T) {
	ctx := context.Background()
	s, seed := eraseFixture(t)
	jm, err := NewJIDMap(ctx, s)
	require.NoError(t, err)

	alice1 := seed("alice", "my secret plan pineapple")
	alice2 := seed("alice", "second thread")
	bob := seed("bob", "bob talks pineapple too")
	require.NoError(t, jm.Put(ctx, "alice", alice1))
	require.NoError(t, jm.Put(ctx, "bob", bob))

	rep, err := s.EraseSender(ctx, "alice")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{alice1, alice2}, rep.SessionIDs)
	assert.Equal(t, int64(2), rep.Rows["sessions"])
	assert.Equal(t, int64(1), rep.Rows["jid_sessions"])
	assert.Equal(t, int64(2), rep.Rows["session_costs"])
	assert.Equal(t, int64(2), rep.Rows["claude_sessions"])

	assert.Zero(t, count(t, s, `SELECT COUNT(*) FROM sessions WHERE sender = 'alice'`))
	assert.Zero(t, count(t, s, `SELECT COUNT(*) FROM session_messages WHERE session_id IN ($1, $2)`, alice1, alice2))
	assert.Zero(t, count(t, s, `SELECT COUNT(*) FROM session_costs WHERE session_id = $1`, alice1))
	assert.Equal(t, 1, count(t, s, `SELECT COUNT(*) FROM sessions WHERE sender = 'bob'`))
	assert.Equal(t, 1, count(t, s, `SELECT COUNT(*) FROM session_costs WHERE session_id = $1`, bob))
	assert.Equal(t, 1, count(t, s, `SELECT COUNT(*) FROM jid_sessions`))
	_, err = s.Load(ctx, bob)
	require.NoError(t, err)

	// Idempotent.
	rep, err = s.EraseSender(ctx, "alice")
	require.NoError(t, err)
	assert.Empty(t, rep.SessionIDs)

	_, err = s.EraseSender(ctx, "  ")
	assert.Error(t, err)
}

// A namespaced key scopes the transport-keyed tables to that
// transport.
func TestEraseSender_NamespacedKeyScopesTransportTables(t *testing.T) {
	ctx := context.Background()
	s, seed := eraseFixture(t)
	ids, err := NewIdentityStore(ctx, s)
	require.NoError(t, err)
	_, err = ids.Provision(ctx, "signal", "+44", "Alice")
	require.NoError(t, err)
	_, err = ids.Provision(ctx, "telegram", "+44", "Alice")
	require.NoError(t, err)
	seed("signal:+44", "hello")

	rep, err := s.EraseSender(ctx, "signal:+44")
	require.NoError(t, err)
	assert.Len(t, rep.SessionIDs, 1)
	assert.Equal(t, int64(1), rep.Rows["identity_handles"])
	assert.Equal(t, 1, count(t, s, `SELECT COUNT(*) FROM identity_handles WHERE transport = 'telegram'`))
}

// Tables a deployment never created are skipped rather than failing
// the erasure.
func TestEraseSender_MissingTablesAreSkipped(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, isolatedDSN(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	sess := model.NewSession("chat")
	sess.Sender = "carol"
	require.NoError(t, s.Save(ctx, sess))

	rep, err := s.EraseSender(ctx, "carol")
	require.NoError(t, err)
	assert.Equal(t, []string{sess.ID}, rep.SessionIDs)
	assert.Equal(t, int64(1), rep.Rows["sessions"])
	_, hasJID := rep.Rows["jid_sessions"]
	assert.False(t, hasJID)
}

// Idle sessions go with their per-session rows and jid mappings;
// fresh ones stay.
func TestEraseIdleSessions(t *testing.T) {
	ctx := context.Background()
	s, seed := eraseFixture(t)
	jm, err := NewJIDMap(ctx, s)
	require.NoError(t, err)
	old := seed("alice", "old")
	fresh := seed("alice", "fresh")
	require.NoError(t, jm.Put(ctx, "alice", old))
	_, err = s.db.ExecContext(ctx, `UPDATE sessions SET updated_at = NOW() - INTERVAL '10 days' WHERE id = $1`, old)
	require.NoError(t, err)

	rep, err := s.EraseIdleSessions(ctx, time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, []string{old}, rep.SessionIDs)
	assert.Equal(t, int64(1), rep.Rows["jid_sessions"])
	assert.Zero(t, count(t, s, `SELECT COUNT(*) FROM session_costs WHERE session_id = $1`, old))
	_, err = s.Load(ctx, fresh)
	require.NoError(t, err)
	_, err = s.Load(ctx, old)
	assert.Error(t, err)
}

func TestSenderKeys(t *testing.T) {
	ctx := context.Background()
	s, seed := eraseFixture(t)
	jm, err := NewJIDMap(ctx, s)
	require.NoError(t, err)
	seed("signal:+44", "a")
	seed("telegram:+44", "b")
	seed("whatsapp:1@s.whatsapp.net", "c")
	require.NoError(t, jm.Put(ctx, "matrix:@alice:matrix.org", "x"))

	keys, err := s.SenderKeys(ctx, "+44")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"signal:+44", "telegram:+44"}, keys)

	keys, err = s.SenderKeys(ctx, "@alice:matrix.org")
	require.NoError(t, err)
	assert.Equal(t, []string{"matrix:@alice:matrix.org"}, keys, "bare part is everything after the first colon")

	keys, err = s.SenderKeys(ctx, "whatsapp:1@s.whatsapp.net")
	require.NoError(t, err)
	assert.Equal(t, []string{"whatsapp:1@s.whatsapp.net"}, keys)

	keys, err = s.SenderKeys(ctx, "nobody")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

// Without a jid_sessions table SenderKeys still answers from
// sessions.
func TestSenderKeys_WithoutJIDTable(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, isolatedDSN(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	sess := model.NewSession("chat")
	sess.Sender = "signal:+1"
	require.NoError(t, s.Save(ctx, sess))
	keys, err := s.SenderKeys(ctx, "+1")
	require.NoError(t, err)
	assert.Equal(t, []string{"signal:+1"}, keys)
}
