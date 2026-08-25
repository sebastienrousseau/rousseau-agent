package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/identity"
)

// This file drives the error branches of the sqlite package by
// *drifting the schema* rather than by mocking database/sql. SQLite
// keeps tables, views and indexes in one object namespace, so an
// index (or view) squatting on a table name makes `CREATE TABLE IF
// NOT EXISTS` fail instead of silently no-opping; a table that exists
// but lacks a projected column lets the constructor succeed and the
// query fail. Both mirror what real schema drift does in production.

// boomExpr raises SQLITE_ERROR ("integer overflow") when evaluated.
//
// Attaching it to a VIRTUAL generated column (added with ALTER TABLE,
// so existing rows are never re-evaluated at write time) makes the
// failure happen while the row is being read. Combined with an index
// that satisfies the query's ORDER BY -- so SQLite streams rows
// instead of materialising them in a sorter -- the first row comes
// back fine and a later one fails, which is what drives the
// `rows.Err()` branches. Queries that must sort or aggregate report
// the same failure from Query instead; those branches are noted in
// the package report rather than faked.
const boomExpr = `abs(-9223372036854775808)`

// driftDB opens a bare, file-backed SQLite database and applies ddl.
// File-backed (not ":memory:") so every pooled connection observes
// the same schema; MaxOpenConns(1) keeps PRAGMA state connection
// stable for the transaction tests.
func driftDB(t *testing.T, ddl string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "drift.db"))
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() }) //nolint:errcheck // test cleanup
	if ddl != "" {
		_, err = db.ExecContext(context.Background(), ddl)
		require.NoError(t, err)
	}
	return db
}

// openFileStore opens a real Store on a temp file and returns both the
// Store and its path so a test can close it, drift the file and
// reopen.
func openFileStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := Open(context.Background(), path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	return s, path
}

// closedStore returns a Store whose database handle is already shut.
func closedStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, s.db.Close())
	return s
}

func execRaw(t *testing.T, path, ddl string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }() //nolint:errcheck // test cleanup
	_, err = db.ExecContext(context.Background(), ddl)
	require.NoError(t, err)
}

// -- Open ---------------------------------------------------------------

func TestOpen_SchemaApplyFailsWhenIndexSquatsOnTableName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drifted.db")
	execRaw(t, path, `CREATE TABLE other (id TEXT); CREATE INDEX sessions ON other(id);`)

	_, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apply schema")
	assert.Contains(t, err.Error(), "already an index named sessions")
}

func TestOpen_EnsureSearchFailsWhenFTSTableIsDrifted(t *testing.T) {
	s, path := openFileStore(t)
	require.NoError(t, s.Close())

	// Replace the FTS5 virtual table with a plain table missing the
	// `title`/`body` columns: CREATE VIRTUAL TABLE IF NOT EXISTS then
	// no-ops and the backfill INSERT is what blows up.
	execRaw(t, path, `DROP TABLE sessions_fts; CREATE TABLE sessions_fts (session_id TEXT);`)

	_, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backfill fts")
}

// -- Save / Load / List / Delete ----------------------------------------

func TestStore_SaveRejectsUnmarshalableSession(t *testing.T) {
	s, _ := openFileStore(t)
	sess := agent.NewSession("bad")
	sess.Append(agent.Message{
		Role: agent.RoleAssistant,
		Content: []agent.Content{{
			Kind:    agent.ContentToolUse,
			ToolUse: &agent.ToolUse{ID: "t1", Name: "x", Input: json.RawMessage("{not json")},
		}},
	})

	err := s.Save(context.Background(), sess)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal session")
}

func TestStore_MutationsFailOnClosedDB(t *testing.T) {
	s := closedStore(t)
	ctx := context.Background()

	require.Error(t, s.Save(ctx, agent.NewSession("t")))
	_, err := s.Load(ctx, "id")
	require.Error(t, err)
	assert.NotErrorIs(t, err, sql.ErrNoRows)
	_, err = s.List(ctx, 0)
	require.Error(t, err)
	require.Error(t, s.Delete(ctx, "id"))
}

func TestStore_LoadRejectsCorruptPayload(t *testing.T) {
	s, _ := openFileStore(t)
	ctx := context.Background()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sessions (id, title, payload, message_count, created_at, updated_at)
VALUES ('corrupt', 'T', 'this is not json', 0, 'x', 'x')`)
	require.NoError(t, err)

	_, err = s.Load(ctx, "corrupt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal session")
}

func TestStore_ListScanFailsOnNonNumericMessageCount(t *testing.T) {
	s, _ := openFileStore(t)
	ctx := context.Background()
	// SQLite's INTEGER affinity stores an unconvertible string as TEXT,
	// so the projected column no longer scans into an int.
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sessions (id, title, payload, message_count, created_at, updated_at)
VALUES ('drift', 'T', '{}', 'not-a-number', 'x', 'x')`)
	require.NoError(t, err)

	_, err = s.List(ctx, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scan summary")
}

// -- Search / RecentSessions --------------------------------------------

const ftsDDL = `
CREATE VIRTUAL TABLE sessions_fts USING fts5(
    session_id UNINDEXED, title, body, tokenize = 'porter unicode61');`

func TestSearch_ScanFailsWhenTitleIsNull(t *testing.T) {
	db := driftDB(t, `
CREATE TABLE sessions (id TEXT PRIMARY KEY, title TEXT, payload TEXT, updated_at TEXT);
INSERT INTO sessions VALUES ('a', NULL, 'hello kubernetes world', '2026-01-02T00:00:00.000Z');`+
		ftsDDL+`
INSERT INTO sessions_fts (session_id, title, body) VALUES ('a', NULL, 'hello kubernetes world');`)

	_, err := (&Store{db: db}).Search(context.Background(), "kubernetes", SearchOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scan hit")
}

func TestRecentSessions_ScanFailsWhenPayloadIsNull(t *testing.T) {
	db := driftDB(t, `
CREATE TABLE sessions (id TEXT PRIMARY KEY, payload TEXT, updated_at TEXT);
INSERT INTO sessions VALUES ('a', NULL, '2026-01-02T00:00:00.000Z');`)

	_, err := (&Store{db: db}).RecentSessions(context.Background(), 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "converting NULL to string")
}

func TestRecentSessions_RejectsCorruptPayload(t *testing.T) {
	s, _ := openFileStore(t)
	ctx := context.Background()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sessions (id, title, payload, message_count, created_at, updated_at)
VALUES ('corrupt', 'T', '<<not json>>', 0, 'x', 'x')`)
	require.NoError(t, err)

	_, err = s.RecentSessions(ctx, 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal session")
}

// -- SessionCostStore ---------------------------------------------------

func TestNewSessionCostStore_FailsOnClosedDB(t *testing.T) {
	_, err := NewSessionCostStore(context.Background(), closedStore(t))
	assert.Error(t, err)
}

func TestSessionCostStore_QueriesFailOnClosedDB(t *testing.T) {
	s, _ := openFileStore(t)
	ctx := context.Background()
	c, err := NewSessionCostStore(ctx, s)
	require.NoError(t, err)
	require.NoError(t, s.db.Close())

	require.Error(t, c.Record(ctx, CostRecord{SessionID: "s"}))
	_, err = c.SumBySession(ctx, "s", 0)
	require.Error(t, err)
	_, err = c.TopSessions(ctx, 0, 5)
	require.Error(t, err)
}

func TestSessionCostStore_TopSessionsDefaultLimitAndWindow(t *testing.T) {
	s, _ := openFileStore(t)
	ctx := context.Background()
	c, err := NewSessionCostStore(ctx, s)
	require.NoError(t, err)

	now := time.Now().UTC()
	require.NoError(t, c.Record(ctx, CostRecord{SessionID: "recent", At: now, CostUSD: 2}))
	require.NoError(t, c.Record(ctx, CostRecord{SessionID: "ancient", At: now.Add(-72 * time.Hour), CostUSD: 9}))

	// limit <= 0 falls back to the built-in cap and returns everything.
	all, err := c.TopSessions(ctx, 0, 0)
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, "ancient", all[0].SessionID)

	// A non-zero window adds the `at >=` predicate.
	windowed, err := c.TopSessions(ctx, time.Hour, 0)
	require.NoError(t, err)
	require.Len(t, windowed, 1)
	assert.Equal(t, "recent", windowed[0].SessionID)
}

func TestSessionCostStore_TopSessionsScanFailsOnNullSessionID(t *testing.T) {
	db := driftDB(t, `
CREATE TABLE session_costs (session_id TEXT, at TEXT, cost_usd REAL,
    input_tokens INTEGER, output_tokens INTEGER, cache_read INTEGER, cache_creation INTEGER);
INSERT INTO session_costs VALUES (NULL, '2026-01-01T00:00:00.000Z', 1.5, 1, 1, 0, 0);`)

	_, err := (&SessionCostStore{db: db}).TopSessions(context.Background(), 0, 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scan top session")
}

// -- ClaudeSessionCache -------------------------------------------------

func TestClaudeSessionCache_DegradesOnClosedDB(t *testing.T) {
	s, _ := openFileStore(t)
	ctx := context.Background()
	c, err := NewClaudeSessionCache(ctx, s)
	require.NoError(t, err)
	require.NoError(t, s.db.Close())

	// A DB failure is not a "known session": IsKnown must answer false
	// rather than block the caller.
	assert.False(t, c.IsKnown("never-seen"))

	// Remember swallows the write failure but still updates the hot
	// cache, so the id reads back as known without touching the DB.
	c.Remember("hot-only")
	assert.True(t, c.IsKnown("hot-only"))
}

// -- CronStore ----------------------------------------------------------

func TestCronStore_ListFailsOnClosedDB(t *testing.T) {
	s, _ := openFileStore(t)
	ctx := context.Background()
	c, err := NewCronStore(ctx, s)
	require.NoError(t, err)
	require.NoError(t, s.db.Close())

	_, err = c.List(ctx)
	assert.Error(t, err)
}

func TestCronStore_ListScanFailsOnNullName(t *testing.T) {
	db := driftDB(t, `
CREATE TABLE cron_jobs (id TEXT, name TEXT, cron_expr TEXT, prompt TEXT,
    deliver_to TEXT, enabled INTEGER, created_at TEXT, last_run_at TEXT);
INSERT INTO cron_jobs VALUES ('j1', NULL, '@daily', 'p', 'd', 1, '2026-01-01T00:00:00.000Z', NULL);`)

	_, err := (&CronStore{db: db}).List(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "converting NULL to string")
}

// -- OAuthTokens --------------------------------------------------------

func TestOAuthTokens_ScanFailsOnNullProvider(t *testing.T) {
	db := driftDB(t, `
CREATE TABLE oauth_tokens (provider TEXT, account_id TEXT, ciphertext BLOB, updated_at TEXT);
INSERT INTO oauth_tokens VALUES (NULL, 'acct', X'00', '2026-01-01T00:00:00Z');`)
	o := &OAuthTokens{db: db}
	ctx := context.Background()

	_, err := o.List(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scan oauth row")

	err = o.Iterate(ctx, func(string, string, []byte) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scan oauth row")
}

func TestOAuthTokens_IterateStopsOnCallbackError(t *testing.T) {
	s, _ := openFileStore(t)
	ctx := context.Background()
	o, err := NewOAuthTokens(ctx, s)
	require.NoError(t, err)
	require.NoError(t, o.Put(ctx, "p1", "a1", []byte("one")))
	require.NoError(t, o.Put(ctx, "p2", "a2", []byte("two")))

	sentinel := errors.New("stop now")
	seen := 0
	err = o.Iterate(ctx, func(string, string, []byte) error {
		seen++
		return sentinel
	})
	assert.ErrorIs(t, err, sentinel)
	assert.Equal(t, 1, seen, "iteration must abort on the first callback error")
}

// -- RecallVectors ------------------------------------------------------

func TestRecallVectors_AllOnEmptyTableReturnsNil(t *testing.T) {
	s, _ := openFileStore(t)
	ctx := context.Background()
	rv, err := NewRecallVectors(ctx, s)
	require.NoError(t, err)

	rows, err := rv.All(ctx)
	require.NoError(t, err)
	assert.Nil(t, rows)
}

func TestRecallVectors_ScanFailsOnNullSessionID(t *testing.T) {
	db := driftDB(t, `
CREATE TABLE recall_vectors (id INTEGER PRIMARY KEY, session_id TEXT, message_id INTEGER,
    chunk_index INTEGER, role TEXT, text TEXT, embedding BLOB, created_at INTEGER, embedder TEXT);
INSERT INTO recall_vectors VALUES (1, NULL, 1, 0, 'user', 't', X'00', 0, 'e');`)

	_, err := (&RecallVectors{db: db}).All(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scan recall row")
}

// -- IdentityStore ------------------------------------------------------

func TestNewIdentityStore_FailsOnClosedDB(t *testing.T) {
	_, err := NewIdentityStore(context.Background(), closedStore(t))
	assert.Error(t, err)
}

func TestIdentityStore_OperationsFailOnClosedDB(t *testing.T) {
	s, _ := openFileStore(t)
	ctx := context.Background()
	r, err := NewIdentityStore(ctx, s)
	require.NoError(t, err)
	require.NoError(t, s.db.Close())

	_, err = r.Resolve(ctx, "wa", "1")
	require.Error(t, err)
	assert.NotErrorIs(t, err, identity.ErrNotLinked)

	_, err = r.Provision(ctx, "wa", "1", "Ada")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "begin")

	require.Error(t, r.Unlink(ctx, "wa", "1"))

	_, err = r.Get(ctx, "id-1")
	require.Error(t, err)
	assert.NotErrorIs(t, err, identity.ErrIdentityNotFound)

	_, err = r.HandlesFor(ctx, "id-1")
	assert.Error(t, err)
}

func TestIdentityStore_ProvisionFailsWhenIdentitiesTableMissing(t *testing.T) {
	db := driftDB(t, `
CREATE TABLE identity_handles (transport TEXT, sender TEXT, identity_id TEXT, verified_at TEXT,
    PRIMARY KEY (transport, sender));`)

	_, err := (&IdentityStore{db: db}).Provision(context.Background(), "wa", "1", "Ada")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insert identity")
}

func TestIdentityStore_ProvisionFailsWhenHandlesTableMissing(t *testing.T) {
	db := driftDB(t, `
CREATE TABLE identities (id TEXT PRIMARY KEY, primary_display TEXT, created_at TEXT);`)

	_, err := (&IdentityStore{db: db}).Provision(context.Background(), "wa", "1", "Ada")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insert handle")
}

func TestIdentityStore_ProvisionFailsOnDeferredConstraintAtCommit(t *testing.T) {
	// A deferred foreign key pointing at a table the handle row can
	// never satisfy defers the violation to COMMIT, which is exactly
	// how a drifted FK surfaces at runtime.
	db := driftDB(t, `
CREATE TABLE identities (id TEXT PRIMARY KEY, primary_display TEXT, created_at TEXT);
CREATE TABLE approved_senders (id TEXT PRIMARY KEY);
CREATE TABLE identity_handles (transport TEXT, sender TEXT, identity_id TEXT, verified_at TEXT,
    PRIMARY KEY (transport, sender),
    FOREIGN KEY (identity_id) REFERENCES approved_senders(id) DEFERRABLE INITIALLY DEFERRED);`)
	_, err := db.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`)
	require.NoError(t, err)

	_, err = (&IdentityStore{db: db}).Provision(context.Background(), "wa", "1", "Ada")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commit")

	// The whole transaction rolled back — no orphan identity row.
	var n int
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM identities`).Scan(&n))
	assert.Zero(t, n)
}

func TestIdentityStore_LinkFailsWhenHandlesAreReadOnly(t *testing.T) {
	db := driftDB(t, `
CREATE TABLE identities (id TEXT PRIMARY KEY, primary_display TEXT, created_at TEXT);
INSERT INTO identities VALUES ('id-1','Ada','2026-01-01T00:00:00.000Z');
CREATE TABLE handles_real (transport TEXT, sender TEXT, identity_id TEXT, verified_at TEXT);
CREATE VIEW identity_handles AS SELECT * FROM handles_real;`)

	err := (&IdentityStore{db: db}).Link(context.Background(), "id-1", "wa", "1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sqlite: link")
}

func TestIdentityStore_GetPropagatesHandlesForFailure(t *testing.T) {
	db := driftDB(t, `
CREATE TABLE identities (id TEXT PRIMARY KEY, primary_display TEXT, created_at TEXT);
INSERT INTO identities VALUES ('id-1','Ada','2026-01-01T00:00:00.000Z');`)

	_, err := (&IdentityStore{db: db}).Get(context.Background(), "id-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "handles for")
}

func TestIdentityStore_HandlesForScanFailsOnNullTransport(t *testing.T) {
	db := driftDB(t, `
CREATE TABLE identity_handles (transport TEXT, sender TEXT, identity_id TEXT, verified_at TEXT);
INSERT INTO identity_handles VALUES (NULL, '1', 'id-1', '2026-01-01T00:00:00.000Z');`)

	_, err := (&IdentityStore{db: db}).HandlesFor(context.Background(), "id-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scan handle")
}

// -- mid-iteration failures ---------------------------------------------

func TestStore_ListSurfacesMidIterationFailure(t *testing.T) {
	db := driftDB(t, `
CREATE TABLE sessions (id TEXT PRIMARY KEY, title TEXT, updated_at TEXT);
CREATE INDEX idx_sessions_updated_at ON sessions(updated_at DESC);
INSERT INTO sessions VALUES ('a','Alpha','2026-01-02T00:00:00.000Z');
INSERT INTO sessions VALUES ('b','Beta','2026-01-01T00:00:00.000Z');
ALTER TABLE sessions ADD COLUMN message_count INTEGER
  GENERATED ALWAYS AS (CASE WHEN id = 'b' THEN `+boomExpr+` ELSE 1 END) VIRTUAL;`)

	// The first row reads cleanly; the second blows up mid-scan. List
	// must report the failure rather than return a truncated list.
	out, err := (&Store{db: db}).List(context.Background(), 0)
	require.Error(t, err)
	assert.Nil(t, out)
	assert.Contains(t, err.Error(), "iterate summaries")
}

func TestRecallVectors_SurfacesMidIterationFailure(t *testing.T) {
	db := driftDB(t, `
CREATE TABLE recall_vectors (id INTEGER PRIMARY KEY, session_id TEXT, message_id INTEGER,
    chunk_index INTEGER, role TEXT, embedding BLOB, created_at INTEGER, embedder TEXT);
CREATE INDEX recall_vectors_created_idx ON recall_vectors(created_at);
INSERT INTO recall_vectors VALUES (1,'a',1,0,'user',X'00',1,'e');
INSERT INTO recall_vectors VALUES (2,'b',2,0,'user',X'00',2,'e');
ALTER TABLE recall_vectors ADD COLUMN text TEXT
  GENERATED ALWAYS AS (CASE WHEN id = 2 THEN `+boomExpr+` ELSE 'ok' END) VIRTUAL;`)

	out, err := (&RecallVectors{db: db}).All(context.Background())
	require.Error(t, err)
	assert.Nil(t, out)
	assert.Contains(t, err.Error(), "integer overflow")
}

func TestIdentityStore_HandlesForSurfacesMidIterationFailure(t *testing.T) {
	db := driftDB(t, `
CREATE TABLE identity_handles (sender TEXT, identity_id TEXT, verified_at TEXT);
CREATE INDEX idx_identity_handles_identity ON identity_handles(identity_id, verified_at);
INSERT INTO identity_handles VALUES ('1','id-1','2026-01-01T00:00:00.000Z');
INSERT INTO identity_handles VALUES ('2','id-1','2026-01-02T00:00:00.000Z');
ALTER TABLE identity_handles ADD COLUMN transport TEXT
  GENERATED ALWAYS AS (CASE WHEN sender = '2' THEN `+boomExpr+` ELSE 'wa' END) VIRTUAL;`)

	out, err := (&IdentityStore{db: db}).HandlesFor(context.Background(), "id-1")
	require.Error(t, err)
	assert.Nil(t, out)
	assert.Contains(t, err.Error(), "iterate handles")
}
