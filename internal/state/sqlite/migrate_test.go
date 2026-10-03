package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// v1Schema is the session store exactly as v0.0.12 leaves it on disk.
const v1Schema = `
CREATE TABLE sessions (
    id TEXT PRIMARY KEY, title TEXT NOT NULL, payload TEXT NOT NULL,
    message_count INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
    sender TEXT NOT NULL DEFAULT '', search_text TEXT NOT NULL DEFAULT '');
CREATE INDEX idx_sessions_updated_at ON sessions(updated_at DESC);
CREATE INDEX idx_sessions_sender_updated ON sessions(sender, updated_at DESC);
CREATE TABLE jid_sessions (jid TEXT PRIMARY KEY, session_id TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE VIRTUAL TABLE sessions_fts USING fts5(session_id UNINDEXED, title, body, tokenize = 'porter unicode61');
CREATE TRIGGER sessions_fts_ad AFTER DELETE ON sessions BEGIN
    DELETE FROM sessions_fts WHERE session_id = OLD.id;
END;
PRAGMA user_version = 1;
`

type v1Fixture struct {
	path     string
	sessions map[string]*agent.Session
	payloads map[string]string
}

func fixedTime(min int) time.Time {
	return time.Date(2026, 9, 1, 12, min, 0, 123456789, time.UTC)
}

func msg(role agent.Role, text string, min int) agent.Message {
	return agent.Message{Role: role, Content: []agent.Content{{Kind: agent.ContentText, Text: text}}, CreatedAt: fixedTime(min)}
}

// writeV1 builds a v1 store holding one session per sender, plus a
// jid mapping for each non-empty sender.
func writeV1(t *testing.T, senders ...string) *v1Fixture {
	t.Helper()
	f := &v1Fixture{path: filepath.Join(t.TempDir(), "sessions.db"),
		sessions: map[string]*agent.Session{}, payloads: map[string]string{}}
	db, err := sql.Open("sqlite", f.path)
	require.NoError(t, err)
	defer db.Close() //nolint:errcheck // test fixture
	ctx := context.Background()
	_, err = db.ExecContext(ctx, v1Schema)
	require.NoError(t, err)
	// Two empty sessions, stored both ways v1 wrote them.
	for i, raw := range []string{"null", "[]"} {
		if len(senders) == 0 {
			break // callers without senders want an empty store
		}
		id := fmt.Sprintf("empty-%d", i)
		_, err = db.ExecContext(ctx, `INSERT INTO sessions VALUES (?, 'empty', ?, 0, 'x', 'x', '', '')`,
			id, `{"id":"`+id+`","title":"empty","messages":`+raw+`,"created_at":"2026-09-01T12:00:00Z","updated_at":"2026-09-01T12:00:00Z"}`)
		require.NoError(t, err)
	}
	for i, sender := range senders {
		sess := &agent.Session{ID: "s" + string(rune('a'+i)), Title: "chat " + sender, Sender: sender,
			CreatedAt: fixedTime(0), UpdatedAt: fixedTime(i + 1)}
		sess.Messages = []agent.Message{
			msg(agent.RoleUser, "[rousseau:compressed] (summary of prior 9 messages): deploys", 1),
			msg(agent.RoleUser, "what about the helm chart for "+sender, 2),
			{Role: agent.RoleUser, CreatedAt: fixedTime(3), Content: []agent.Content{
				{Kind: agent.ContentText, Text: "see screenshot"},
				{Kind: agent.ContentImage, Image: &agent.Image{MediaType: "image/png", Data: []byte{1, 2, 3}}},
			}},
			msg(agent.RoleAssistant, "bump the chart version", 4),
		}
		payload, err := json.Marshal(sess)
		require.NoError(t, err)
		f.sessions[sess.ID], f.payloads[sess.ID] = sess, string(payload)
		_, err = db.ExecContext(ctx, `INSERT INTO sessions VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			sess.ID, sess.Title, string(payload), len(sess.Messages),
			sess.CreatedAt.Format("2006-01-02T15:04:05.000Z"), sess.UpdatedAt.Format("2006-01-02T15:04:05.000Z"),
			sender, searchText(sess))
		require.NoError(t, err)
		if sender != "" {
			_, err = db.ExecContext(ctx, `INSERT INTO jid_sessions VALUES (?, ?, 'now')`, sender, sess.ID)
			require.NoError(t, err)
		}
	}
	return f
}

func versionOf(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close() //nolint:errcheck // test
	v, err := userVersion(context.Background(), db)
	require.NoError(t, err)
	return v
}

func clock() time.Time { return time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC) }

func TestOpen_RefusesAStoreThatNeedsMigration(t *testing.T) {
	f := writeV1(t, "447700900123@s.whatsapp.net")
	_, err := Open(context.Background(), f.path)
	require.ErrorIs(t, err, ErrNeedsMigration)
	assert.Contains(t, err.Error(), "rousseau migrate")
	assert.Equal(t, 1, versionOf(t, f.path))
}

func TestOpen_UpgradesAnEmptyOlderStoreInPlace(t *testing.T) {
	f := writeV1(t)
	s, err := Open(context.Background(), f.path)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	assert.Equal(t, schemaVersion, versionOf(t, f.path))
}

// TestMigrate_V1ToV2 pins the upgrade: a dry run writes nothing,
// ambiguous keys stop a real run before it touches anything, and the
// mapped run splits every payload into rows, namespaces every key, and
// loads each session exactly as it was.
func TestMigrate_V1ToV2(t *testing.T) {
	ctx := context.Background()
	f := writeV1(t, "447700900123@s.whatsapp.net", "+447700900123", "U0123ABCD", "")

	dry, err := Migrate(ctx, f.path, MigrateOptions{DryRun: true, Now: clock})
	require.NoError(t, err)
	assert.Equal(t, 1, dry.FromVersion)
	assert.Equal(t, 6, dry.Sessions)
	assert.Equal(t, 16, dry.Messages)
	require.Len(t, dry.Ambiguous, 1)
	assert.Equal(t, "+447700900123", dry.Ambiguous[0].Sender)
	assert.Equal(t, []string{"imessage", "signal"}, dry.Ambiguous[0].Candidates)
	assert.Empty(t, dry.Backup)
	assert.Equal(t, 1, versionOf(t, f.path), "a dry run writes nothing")

	_, err = Migrate(ctx, f.path, MigrateOptions{Now: clock})
	require.ErrorIs(t, err, ErrAmbiguousSenders)
	assert.Equal(t, 1, versionOf(t, f.path))
	matches, _ := filepath.Glob(f.path + ".pre-v2-*") //nolint:errcheck // pattern is valid
	assert.Empty(t, matches, "refused before the backup")

	rep, err := Migrate(ctx, f.path, MigrateOptions{Now: clock, Map: map[string]string{"+447700900123": "signal"}})
	require.NoError(t, err)
	assert.Equal(t, 2, versionOf(t, f.path))
	assert.Equal(t, 16, rep.Messages)
	assert.ElementsMatch(t, []KeyChange{
		{From: "447700900123@s.whatsapp.net", To: "whatsapp:447700900123@s.whatsapp.net", Rule: "shape"},
		{From: "+447700900123", To: "signal:+447700900123", Rule: "map"},
		{From: "U0123ABCD", To: "slack:U0123ABCD", Rule: "shape"},
	}, rep.Keys)

	info, err := os.Stat(rep.Backup)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.Equal(t, 1, versionOf(t, rep.Backup), "the backup is the untouched v1 store")

	s, err := Open(ctx, f.path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	for id, want := range f.sessions {
		got, err := s.Load(ctx, id)
		require.NoError(t, err)
		wantMsgs, _ := json.Marshal(want.Messages) //nolint:errcheck // fixture always marshals
		gotMsgs, _ := json.Marshal(got.Messages)   //nolint:errcheck // loaded messages always marshal
		assert.JSONEq(t, string(wantMsgs), string(gotMsgs), id)
		assert.True(t, want.UpdatedAt.Equal(got.UpdatedAt), id)
		if want.Sender != "" {
			assert.Contains(t, got.Sender, ":"+want.Sender, "%s loads with its namespaced key", id)
		}
	}
	listed, err := s.ListBySender(ctx, "signal:+447700900123", 0)
	require.NoError(t, err)
	assert.Len(t, listed, 1)
	jm, err := NewJIDMap(ctx, s)
	require.NoError(t, err)
	id, ok, err := jm.Get(ctx, "whatsapp:447700900123@s.whatsapp.net")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "sa", id)
	hits, err := s.SearchBySender(ctx, "slack:U0123ABCD", "helm", SearchOptions{})
	require.NoError(t, err)
	assert.Len(t, hits, 1)
	hits, err = s.Search(ctx, "media_type", SearchOptions{})
	require.NoError(t, err)
	assert.Empty(t, hits, "JSON keys are not indexed")

	again, err := Migrate(ctx, f.path, MigrateOptions{Now: clock})
	require.NoError(t, err)
	assert.Equal(t, 2, again.FromVersion, "a second run is a no-op")
	assert.Empty(t, again.Backup)
}

// TestMigrateDown_RoundTrip pins the rollback path: down restores each
// payload byte for byte and the bare keys, and up works again after.
func TestMigrateDown_RoundTrip(t *testing.T) {
	ctx := context.Background()
	f := writeV1(t, "447700900123@s.whatsapp.net", "")
	_, err := Migrate(ctx, f.path, MigrateOptions{Now: clock})
	require.NoError(t, err)

	rep, err := MigrateDown(ctx, f.path, clock)
	require.NoError(t, err)
	assert.Equal(t, 4, rep.Sessions)
	assert.Equal(t, 1, versionOf(t, f.path))
	assert.FileExists(t, rep.Backup)

	db, err := sql.Open("sqlite", f.path)
	require.NoError(t, err)
	defer db.Close() //nolint:errcheck // test
	for id, want := range f.payloads {
		var got string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT payload FROM sessions WHERE id = ?`, id).Scan(&got))
		assert.Equal(t, want, got, "payload of %s restored byte for byte", id)
	}
	var jid string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT jid FROM jid_sessions`).Scan(&jid))
	assert.Equal(t, "447700900123@s.whatsapp.net", jid)
	var tables int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('session_messages', 'messages_fts', 'titles_fts')`).Scan(&tables))
	assert.Zero(t, tables)

	_, err = Migrate(ctx, f.path, MigrateOptions{Now: func() time.Time { return clock().Add(time.Hour) }})
	require.NoError(t, err, "up again after down")
	assert.Equal(t, 2, versionOf(t, f.path))
}

func TestMigrateDown_RefusesKeysThatWouldCollide(t *testing.T) {
	ctx := context.Background()
	f := writeV1(t)
	s, err := Open(ctx, f.path)
	require.NoError(t, err)
	for _, k := range []string{"signal:+447700900123", "imessage:+447700900123"} {
		sess := agent.NewSession(k)
		sess.Sender = k
		require.NoError(t, s.Save(ctx, sess))
	}
	require.NoError(t, s.Close())

	_, err = MigrateDown(ctx, f.path, clock)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "would both become")
	assert.Equal(t, 2, versionOf(t, f.path))
}

// TestMigrate_FailureLeavesTheStoreUntouched pins atomicity: a session
// that cannot be decoded aborts the run and rolls back every change.
func TestMigrate_FailureLeavesTheStoreUntouched(t *testing.T) {
	ctx := context.Background()
	f := writeV1(t, "447700900123@s.whatsapp.net")
	db, err := sql.Open("sqlite", f.path)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO sessions VALUES ('broken', 't', 'not json', 0, 'x', 'x', '', '')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = Migrate(ctx, f.path, MigrateOptions{Now: clock})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unreadable payload")
	assert.Equal(t, 1, versionOf(t, f.path))

	db, err = sql.Open("sqlite", f.path)
	require.NoError(t, err)
	defer db.Close() //nolint:errcheck // test
	var n int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE name = 'session_messages'`).Scan(&n))
	assert.Zero(t, n, "the v2 tables were rolled back")
	var sender string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT sender FROM sessions WHERE id = 'sa'`).Scan(&sender))
	assert.Equal(t, "447700900123@s.whatsapp.net", sender, "keys were rolled back")
}

// TestMigrate_StoreOlderThanSenderTracking covers the oldest layout:
// no sender column at all.
func TestMigrate_StoreOlderThanSenderTracking(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
CREATE TABLE sessions (id TEXT PRIMARY KEY, title TEXT NOT NULL, payload TEXT NOT NULL,
    message_count INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
INSERT INTO sessions VALUES ('legacy-id', 'legacy', '{"id":"legacy-id","messages":[{"role":"user","content":[{"kind":"text","text":"hello"}],"created_at":"2026-01-01T00:00:00Z"}]}', 1,
    '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z');`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	rep, err := Migrate(ctx, path, MigrateOptions{Now: clock})
	require.NoError(t, err)
	assert.Equal(t, 0, rep.FromVersion)
	s, err := Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	got, err := s.Load(ctx, "legacy-id")
	require.NoError(t, err)
	assert.Equal(t, []string{"hello"}, texts(got))
}

func TestSenderKeys(t *testing.T) {
	ctx := context.Background()
	s := openV2(t)
	_, err := NewJIDMap(ctx, s)
	require.NoError(t, err)
	for _, k := range []string{"signal:+447700900123", "imessage:+447700900123", "+447700900999"} {
		sess := agent.NewSession(k)
		sess.Sender = k
		require.NoError(t, s.Save(ctx, sess))
	}
	got, err := s.SenderKeys(ctx, "+447700900123")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"signal:+447700900123", "imessage:+447700900123"}, got)
	got, err = s.SenderKeys(ctx, "+447700900999")
	require.NoError(t, err)
	assert.Equal(t, []string{"+447700900999"}, got, "a legacy bare key matches itself")
	got, err = s.SenderKeys(ctx, "nobody")
	require.NoError(t, err)
	assert.Empty(t, got)

	// Without a jid_sessions table, sessions alone answer.
	bare := openV2(t)
	sess := agent.NewSession("x")
	sess.Sender = "slack:U0123ABCD"
	require.NoError(t, bare.Save(ctx, sess))
	got, err = bare.SenderKeys(ctx, "U0123ABCD")
	require.NoError(t, err)
	assert.Equal(t, []string{"slack:U0123ABCD"}, got)
}

func TestOpen_RefusesANewerSchema(t *testing.T) {
	f := writeV1(t)
	db, err := sql.Open("sqlite", f.path)
	require.NoError(t, err)
	_, err = db.Exec(`PRAGMA user_version = 99`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	_, err = Open(context.Background(), f.path)
	assert.ErrorContains(t, err, "newer than this build")
}

func TestMigrate_Errors(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	junk := filepath.Join(dir, "junk.db")
	require.NoError(t, os.WriteFile(junk, []byte("this is not a database, just text padding it out"), 0o600))
	_, err := Migrate(ctx, junk, MigrateOptions{})
	assert.Error(t, err, "not a database")

	empty := filepath.Join(dir, "empty.db")
	_, err = Migrate(ctx, empty, MigrateOptions{})
	assert.ErrorContains(t, err, "nothing to migrate")

	f := writeV1(t, "+447700900123")
	_, err = Migrate(ctx, f.path, MigrateOptions{Map: map[string]string{"+447700900123": "pigeon"}})
	assert.ErrorContains(t, err, "unknown transport")

	require.NoError(t, os.WriteFile(f.path+".pre-v2-20261002T090000Z", nil, 0o600))
	_, err = Migrate(ctx, f.path, MigrateOptions{Now: clock, Map: map[string]string{"+447700900123": "signal"}})
	assert.ErrorContains(t, err, "already exists")
	assert.Equal(t, 1, versionOf(t, f.path))

	_, err = MigrateDown(ctx, f.path, clock)
	assert.ErrorContains(t, err, "not 2")
	_, err = MigrateDown(ctx, junk, clock)
	assert.Error(t, err)
}

// TestMigrate_FailuresMidTransactionRollBack sabotages each write the
// migration makes and checks every failure leaves version 1 and the
// original keys in place.
func TestMigrate_FailuresMidTransactionRollBack(t *testing.T) {
	cases := map[string]string{
		"message insert fails": `CREATE TABLE session_messages (session_id TEXT, seq INTEGER);`,
		"session update fails": `CREATE TRIGGER no_update BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT, 'sabotaged'); END;`,
		"mapping rewrite fails": `ALTER TABLE jid_sessions RENAME TO jid_real;
CREATE VIEW jid_sessions AS SELECT * FROM jid_real;`,
		"title index fails": `CREATE TABLE titles_fts (session_id TEXT);`,
	}
	for name, sabotage := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := writeV1(t, "447700900123@s.whatsapp.net")
			db, err := sql.Open("sqlite", f.path)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, sabotage)
			require.NoError(t, err)
			require.NoError(t, db.Close())

			_, err = Migrate(ctx, f.path, MigrateOptions{Now: clock})
			require.Error(t, err)
			assert.Equal(t, 1, versionOf(t, f.path))
			db, err = sql.Open("sqlite", f.path)
			require.NoError(t, err)
			defer db.Close() //nolint:errcheck // test
			var sender string
			require.NoError(t, db.QueryRowContext(ctx, `SELECT sender FROM sessions WHERE id = 'sa'`).Scan(&sender))
			assert.Equal(t, "447700900123@s.whatsapp.net", sender)
		})
	}
}

func TestMigrateDown_FailureRollsBack(t *testing.T) {
	ctx := context.Background()
	f := writeV1(t, "447700900123@s.whatsapp.net")
	_, err := Migrate(ctx, f.path, MigrateOptions{Now: clock})
	require.NoError(t, err)
	db, err := sql.Open("sqlite", f.path)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE TRIGGER no_update BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT, 'sabotaged'); END;`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = MigrateDown(ctx, f.path, clock)
	assert.ErrorContains(t, err, "sabotaged")
	assert.Equal(t, 2, versionOf(t, f.path))
}
