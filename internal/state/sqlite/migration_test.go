package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // register driver

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpen_LegacyDBGainsSenderColumn is a regression pin for a
// real production incident: opening a pre-lifecycle-verbs
// sessions.db failed at boot with "no such column: sender"
// because schema.sql's CREATE INDEX ON sessions(sender,…) ran
// BEFORE the runtime ensureSenderColumn migration.
//
// The fix moves the sender-index into ensureSenderColumn so
// schema.sql stays boot-safe on legacy DBs. This test
// simulates the exact failure shape: create a sessions table
// WITHOUT the sender column, then call Open — it must succeed
// and the column + index must be present afterwards.
func TestOpen_LegacyDBGainsSenderColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Bootstrap a legacy-shape sessions table (no sender column).
	// Mirrors what a pre-lifecycle-verbs deployment has on disk.
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `
CREATE TABLE sessions (
    id             TEXT PRIMARY KEY,
    title          TEXT NOT NULL,
    payload        TEXT NOT NULL,
    message_count  INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
)`)
	require.NoError(t, err)
	// Seed a legacy row so we can prove Save / ListBySender
	// coexist with pre-migration data.
	_, err = db.Exec(`INSERT INTO sessions VALUES ('legacy-id', 'legacy', '{}', 0, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	// Now open via Store.Open — must succeed and migrate in
	// place. Before the fix this returned "SQL logic error: no
	// such column: sender".
	store, err := Open(context.Background(), path)
	require.NoError(t, err, "Open must migrate a legacy DB without erroring")
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	// Verify the sender column exists post-migration by
	// exercising the query path that uses it.
	_, err = store.ListBySender(context.Background(), "any", 0)
	assert.NoError(t, err, "ListBySender must work after the migration ran")

	// Sanity: the legacy row survived intact (migration is
	// additive — never destroys existing data). Load only
	// reads the payload column (which for this synthetic
	// legacy row is "{}"), so we assert the row is retrievable
	// without a "not found" error rather than checking payload
	// fields.
	_, err = store.Load(context.Background(), "legacy-id")
	require.NoError(t, err, "legacy row must survive the migration")
}
