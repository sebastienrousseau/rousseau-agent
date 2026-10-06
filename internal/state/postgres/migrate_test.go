package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/model"
)

// v1Schema is the Postgres session store as v0.0.12 leaves it.
const v1Schema = `
CREATE TABLE sessions (
    id TEXT PRIMARY KEY, title TEXT NOT NULL, payload TEXT NOT NULL,
    message_count INTEGER NOT NULL DEFAULT 0, created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL, sender TEXT NOT NULL DEFAULT '');
ALTER TABLE sessions ADD COLUMN search_vector tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
    setweight(to_tsvector('english', coalesce(payload, '')), 'B')) STORED;
CREATE INDEX idx_sessions_search_vector ON sessions USING GIN (search_vector);
CREATE TABLE jid_sessions (jid TEXT PRIMARY KEY, session_id TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now());
`

// isolatedDSN creates a fresh schema and returns a DSN whose
// search_path points at it, so a migration test cannot disturb the
// shared tables the other integration tests use.
func isolatedDSN(t *testing.T) string {
	t.Helper()
	base := requirePG(t)
	schemaName := "mig_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	db, err := sql.Open("pgx", base)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = db.ExecContext(ctx, `CREATE SCHEMA `+schemaName)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA `+schemaName+` CASCADE`) //nolint:errcheck // test cleanup
		_ = db.Close()                                                                    //nolint:errcheck // test cleanup
	})
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + "search_path=" + schemaName
}

func writePGV1(t *testing.T, dsn string, senders ...string) map[string]*model.Session {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer db.Close() //nolint:errcheck // test fixture
	ctx := context.Background()
	_, err = db.ExecContext(ctx, v1Schema)
	require.NoError(t, err)
	out := map[string]*model.Session{}
	at := time.Date(2026, 9, 1, 12, 0, 0, 123456000, time.UTC)
	// An empty session, as v1 stored one ("messages":null).
	_, err = db.ExecContext(ctx, `INSERT INTO sessions (id, title, payload, message_count, created_at, updated_at)
VALUES ('empty', 'empty', '{"id":"empty","title":"empty","messages":null}', 0, now(), now())`)
	require.NoError(t, err)
	for i, sender := range senders {
		sess := &model.Session{ID: fmt.Sprintf("s%d", i), Title: "chat " + sender, Sender: sender, CreatedAt: at, UpdatedAt: at}
		sess.Messages = []model.Message{
			{Role: model.RoleUser, CreatedAt: at, Content: []model.Content{{Kind: model.ContentText, Text: "helm chart for " + sender}}},
			{Role: model.RoleAssistant, CreatedAt: at, Content: []model.Content{{Kind: model.ContentText, Text: "bump it"}}},
		}
		payload, err := json.Marshal(sess)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `INSERT INTO sessions (id, title, payload, message_count, created_at, updated_at, sender)
VALUES ($1, $2, $3, $4, $5, $5, $6)`, sess.ID, sess.Title, string(payload), len(sess.Messages), at, sender)
		require.NoError(t, err)
		if sender != "" {
			_, err = db.ExecContext(ctx, `INSERT INTO jid_sessions (jid, session_id) VALUES ($1, $2)`, sender, sess.ID)
			require.NoError(t, err)
		}
		out[sess.ID] = sess
	}
	return out
}

func TestPGMigrate_V1ToV2AndBack(t *testing.T) {
	ctx := context.Background()
	dsn := isolatedDSN(t)
	want := writePGV1(t, dsn, "447700900123@s.whatsapp.net", "+447700900123", "")

	_, err := Open(ctx, dsn)
	require.ErrorIs(t, err, ErrNeedsMigration)

	dry, err := Migrate(ctx, dsn, MigrateOptions{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, 6, dry.Messages)
	require.Len(t, dry.Ambiguous, 1)

	_, err = Migrate(ctx, dsn, MigrateOptions{Map: map[string]string{"+447700900123": "signal"}})
	require.ErrorIs(t, err, ErrBackupRequired)

	rep, err := Migrate(ctx, dsn, MigrateOptions{Map: map[string]string{"+447700900123": "signal"}, BackupTaken: true})
	require.NoError(t, err)
	assert.Equal(t, 6, rep.Messages)
	assert.Len(t, rep.Keys, 2)

	again, err := Migrate(ctx, dsn, MigrateOptions{})
	require.NoError(t, err)
	assert.Equal(t, 2, again.FromVersion, "a second run is a no-op")
	_, err = MigrateDown(ctx, dsn, false)
	require.ErrorIs(t, err, ErrBackupRequired)

	s, err := Open(ctx, dsn)
	require.NoError(t, err)
	for id, w := range want {
		got, err := s.Load(ctx, id)
		require.NoError(t, err)
		wj, _ := json.Marshal(w.Messages)   //nolint:errcheck // fixture always marshals
		gj, _ := json.Marshal(got.Messages) //nolint:errcheck // loaded messages always marshal
		assert.JSONEq(t, string(wj), string(gj), id)
	}
	hits, err := s.SearchBySender(ctx, "signal:+447700900123", "helm", SearchOptions{})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, "s1", hits[0].SessionID)
	hits, err = s.Search(ctx, "content", SearchOptions{})
	require.NoError(t, err)
	assert.Empty(t, hits, "JSON keys are not indexed")

	sess := want["s0"]
	got, err := s.Load(ctx, sess.ID)
	require.NoError(t, err)
	got.Messages = append([]model.Message{model.NewUserText("[summary]")}, got.Messages[1:]...)
	require.NoError(t, s.Save(ctx, got), "a compressor rewrite after migration")
	got.Append(model.NewUserText("and now?"))
	require.NoError(t, s.Save(ctx, got))
	reloaded, err := s.Load(ctx, sess.ID)
	require.NoError(t, err)
	require.Len(t, reloaded.Messages, 3)
	assert.Equal(t, "[summary]", reloaded.Messages[0].Content[0].Text)
	assert.Equal(t, "and now?", reloaded.Messages[2].Content[0].Text)
	require.NoError(t, s.Close())

	down, err := MigrateDown(ctx, dsn, true)
	require.NoError(t, err)
	assert.Equal(t, 4, down.Sessions)
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer db.Close() //nolint:errcheck // test
	var jid string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT jid FROM jid_sessions WHERE session_id = 's1'`).Scan(&jid))
	assert.Equal(t, "+447700900123", jid)
	var payload string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT payload FROM sessions WHERE id = 's0'`).Scan(&payload))
	assert.Contains(t, payload, "and now?", "down keeps messages written after the upgrade")
}

func TestPGMigrate_Errors(t *testing.T) {
	ctx := context.Background()
	_, err := Migrate(ctx, "", MigrateOptions{})
	assert.ErrorContains(t, err, "empty DSN")
	_, err = MigrateDown(ctx, "", true)
	assert.ErrorContains(t, err, "empty DSN")

	dsn := isolatedDSN(t)
	writePGV1(t, dsn, "+447700900123")
	_, err = Migrate(ctx, dsn, MigrateOptions{Map: map[string]string{"+447700900123": "pigeon"}})
	assert.ErrorContains(t, err, "unknown transport")
	_, err = Migrate(ctx, dsn, MigrateOptions{BackupTaken: true})
	assert.ErrorIs(t, err, ErrAmbiguousSenders)
	_, err = MigrateDown(ctx, dsn, true)
	assert.ErrorContains(t, err, "not 2")
}

func TestPGMigrate_FailuresRollBack(t *testing.T) {
	ctx := context.Background()
	cases := map[string]string{
		"schema fails":         `CREATE TABLE session_messages (session_id TEXT, seq BIGINT)`,
		"session update fails": `CREATE FUNCTION no_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'sabotaged'; END $$; CREATE TRIGGER no_update BEFORE UPDATE ON sessions FOR EACH ROW EXECUTE FUNCTION no_update()`,
	}
	for name, sabotage := range cases {
		t.Run(name, func(t *testing.T) {
			dsn := isolatedDSN(t)
			writePGV1(t, dsn, "447700900123@s.whatsapp.net")
			db, err := sql.Open("pgx", dsn)
			require.NoError(t, err)
			defer db.Close() //nolint:errcheck // test
			_, err = db.ExecContext(ctx, sabotage)
			require.NoError(t, err)

			_, err = Migrate(ctx, dsn, MigrateOptions{BackupTaken: true})
			require.Error(t, err)
			v, err := peekVersion(ctx, db)
			require.NoError(t, err)
			assert.Equal(t, 1, v)
			var sender string
			require.NoError(t, db.QueryRowContext(ctx, `SELECT sender FROM sessions WHERE id = 's0'`).Scan(&sender))
			assert.Equal(t, "447700900123@s.whatsapp.net", sender)
		})
	}
}

func TestPGOpen_RefusesANewerSchema(t *testing.T) {
	ctx := context.Background()
	dsn := isolatedDSN(t)
	s, err := Open(ctx, dsn)
	require.NoError(t, err)
	require.NoError(t, setVersion(ctx, s.db, 99))
	require.NoError(t, s.Close())
	_, err = Open(ctx, dsn)
	assert.ErrorContains(t, err, "newer than this build")
}
