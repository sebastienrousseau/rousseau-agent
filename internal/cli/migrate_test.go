package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// writeV1Store writes a v0.0.12-layout store holding one session per
// sender.
func writeV1Store(t *testing.T, path string, senders ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close() //nolint:errcheck // test fixture
	ctx := context.Background()
	_, err = db.ExecContext(ctx, `
CREATE TABLE sessions (id TEXT PRIMARY KEY, title TEXT NOT NULL, payload TEXT NOT NULL,
    message_count INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
    sender TEXT NOT NULL DEFAULT '', search_text TEXT NOT NULL DEFAULT '');
CREATE TABLE jid_sessions (jid TEXT PRIMARY KEY, session_id TEXT NOT NULL, created_at TEXT NOT NULL);
PRAGMA user_version = 1;`)
	require.NoError(t, err)
	for _, sender := range senders {
		sess := agent.NewSession("chat " + sender)
		sess.Sender = sender
		sess.Append(agent.NewUserText("hello from " + sender))
		payload, err := json.Marshal(sess)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `INSERT INTO sessions VALUES (?, ?, ?, 1, 'x', 'x', ?, '')`,
			sess.ID, sess.Title, string(payload), sender)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `INSERT INTO jid_sessions VALUES (?, ?, 'x')`, sender, sess.ID)
		require.NoError(t, err)
	}
}

// TestOpenStore_OlderStoreExits78 pins the operator contract: a daemon
// started on an unmigrated store stops with EX_CONFIG instead of
// restart-looping, unless state.auto_migrate is on.
func TestOpenStore_OlderStoreExits78(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.db")
	writeV1Store(t, path, "447700900123@s.whatsapp.net")

	_, err := openStore(ctx, config.StateConfig{Path: path})
	require.ErrorIs(t, err, sqlitestore.ErrNeedsMigration)
	assert.Equal(t, ExitNeedsOperator, exitCodeFor(err))

	st, err := openStore(ctx, config.StateConfig{Path: path, AutoMigrate: true})
	require.NoError(t, err)
	require.NoError(t, st.Close())
	backups, _ := filepath.Glob(path + ".pre-v2-*") //nolint:errcheck // pattern is valid
	assert.Len(t, backups, 1, "auto-migrate takes the same backup")
}

func TestOpenStore_AutoMigrateStillStopsOnAmbiguousKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	writeV1Store(t, path, "+447700900123")
	_, err := openStore(context.Background(), config.StateConfig{Path: path, AutoMigrate: true})
	require.ErrorIs(t, err, sqlitestore.ErrAmbiguousSenders)
	assert.Equal(t, ExitNeedsOperator, exitCodeFor(err))
}

func runCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := NewRoot(&Options{})
	root.SetArgs(append([]string{"--config", cfgPath}, args...))
	root.SetOut(&out)
	root.SetErr(&errOut)
	err := root.ExecuteContext(context.Background())
	return out.String() + errOut.String(), err
}

// TestMigrateCmd walks the documented operator flow: dry run, an
// ambiguous key refused, then the mapped upgrade; and erasure by a
// bare identifier afterwards.
func TestMigrateCmd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")
	cfgPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("state:\n  path: "+path+"\n"), 0o600))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeV1Store(t, path, "447700900123@s.whatsapp.net", "+447700900123")

	out, err := runCLI(t, cfgPath, "migrate", "--dry-run")
	require.NoError(t, err)
	assert.Contains(t, out, "dry run, nothing written")
	assert.Contains(t, out, "AMBIGUOUS  +447700900123 could be imessage or signal")
	assert.Contains(t, out, "447700900123@s.whatsapp.net -> whatsapp:447700900123@s.whatsapp.net (shape)")

	_, err = runCLI(t, cfgPath, "migrate")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nothing changed")

	_, err = runCLI(t, cfgPath, "migrate", "--map", "+447700900123=bogus")
	assert.ErrorContains(t, err, "unknown transport")

	out, err = runCLI(t, cfgPath, "migrate", "--map", "+447700900123=signal")
	require.NoError(t, err)
	assert.Contains(t, out, "migrated")
	assert.Contains(t, out, "+447700900123 -> signal:+447700900123 (map)")
	assert.Contains(t, out, "backup: "+path+".pre-v2-")

	out, err = runCLI(t, cfgPath, "migrate")
	require.NoError(t, err)
	assert.Contains(t, out, "already at schema version 2")

	out, err = runCLI(t, cfgPath, "session", "delete-by-sender", "+447700900123", "--yes")
	require.NoError(t, err)
	assert.Contains(t, out, "resolved +447700900123 to signal:+447700900123")
	assert.Contains(t, out, "erased signal:+447700900123: 1 session(s)")
}

func TestDeleteBySender_RefusesABareIDHeldOnTwoTransports(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")
	cfgPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("state:\n  path: "+path+"\n"), 0o600))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	t.Setenv("ANTHROPIC_API_KEY", "")
	st, err := sqlitestore.Open(ctx, path)
	require.NoError(t, err)
	for _, k := range []string{"signal:+447700900123", "imessage:+447700900123"} {
		s := agent.NewSession(k)
		s.Sender = k
		require.NoError(t, st.Save(ctx, s))
	}
	require.NoError(t, st.Close())

	_, err = runCLI(t, cfgPath, "session", "delete-by-sender", "+447700900123", "--yes")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "several transports")

	out, err := runCLI(t, cfgPath, "session", "delete-by-sender", "imessage:+447700900123", "--yes")
	require.NoError(t, err)
	assert.Contains(t, out, "erased imessage:+447700900123: 1 session(s)")
}

func TestMigrateCmd_Down(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")
	cfgPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("state:\n  path: "+path+"\n"), 0o600))
	writeV1Store(t, path, "447700900123@s.whatsapp.net")
	_, err := runCLI(t, cfgPath, "migrate")
	require.NoError(t, err)

	out, err := runCLI(t, cfgPath, "migrate", "--down")
	require.NoError(t, err)
	assert.Contains(t, out, "returned "+path+" to schema version 1: 1 session(s), 1 message(s)")

	_, err = runCLI(t, cfgPath, "migrate", "--map", "nonsense")
	assert.ErrorContains(t, err, "want <sender>=<transport>")
}

// TestMigrateCmd_FailureSaysNothingChanged pins that a run rolled back
// by its own verification never reports success.
func TestMigrateCmd_FailureSaysNothingChanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")
	cfgPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("state:\n  path: "+path+"\n"), 0o600))
	writeV1Store(t, path, "447700900123@s.whatsapp.net")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO sessions VALUES ('broken', 't', 'not json', 0, 'x', 'x', '', '')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	out, err := runCLI(t, cfgPath, "migrate")
	require.Error(t, err)
	assert.Contains(t, out, "NOT migrated (nothing changed)")
	assert.NotContains(t, out, "\nmigrated ")
}

func TestDoctorStateDiags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	writeV1Store(t, path, "447700900123@s.whatsapp.net")
	d := schemaDiag(path, 1)
	assert.Equal(t, "warn", d.Status)
	assert.Contains(t, d.Detail, "rousseau migrate")

	_, err := sqlitestore.Migrate(context.Background(), path, sqlitestore.MigrateOptions{})
	require.NoError(t, err)
	assert.Equal(t, "ok", schemaDiag(path, 1).Status)
	backups := backupDiags(path, time.Now())
	require.Len(t, backups, 1)
	assert.Contains(t, backups[0].Detail, ".pre-v2-")

	assert.Equal(t, "fail", schemaDiag(filepath.Join(t.TempDir(), "missing", "x.db"), 1).Status)
}

func TestMigrateCmd_PostgresAndDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("state:\n  driver: postgres\n  dsn: \"\"\n"), 0o600))
	_, err := runCLI(t, cfgPath, "migrate", "--dry-run")
	assert.ErrorContains(t, err, "empty DSN")
	_, err = runCLI(t, cfgPath, "migrate", "--down", "--backup-taken")
	assert.ErrorContains(t, err, "empty DSN")

	require.NoError(t, os.WriteFile(cfgPath, []byte("state:\n  driver: etcd\n"), 0o600))
	_, err = runCLI(t, cfgPath, "migrate")
	assert.ErrorContains(t, err, "unknown state driver")

	t.Setenv("HOME", dir)
	p, err := statePath("")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, ".local", "share", "rousseau", "sessions.db"), p)
	require.NoError(t, os.WriteFile(cfgPath, []byte("state:\n  path: "+filepath.Join(dir, "absent.db")+"\n"), 0o600))
	_, err = runCLI(t, cfgPath, "migrate")
	assert.ErrorContains(t, err, "no session store at")
	_, err = runCLI(t, cfgPath, "migrate", "--down")
	assert.Error(t, err)
}
