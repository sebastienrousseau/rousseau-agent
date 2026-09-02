package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openJIDMapTest opens a JIDMap against the CI/dev Postgres and
// truncates the jid_sessions table so each test starts from a
// clean slate. Guarded on ROUSSEAU_TEST_POSTGRES_URL like the
// sessions + cron integration tests.
func openJIDMapTest(t *testing.T) *JIDMap {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, requirePG(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	jm, err := NewJIDMap(ctx, store)
	require.NoError(t, err)
	// TRUNCATE proves the schema was applied AND resets state.
	_, err = store.db.ExecContext(ctx, `TRUNCATE TABLE jid_sessions`)
	require.NoError(t, err)
	return jm
}

// -- unit-only (no DB) --

func TestNewJIDMap_SchemaIdempotent(t *testing.T) {
	// Compile-check the const we ship — an accidental rename would
	// cause the daemon to fail schema-apply on upgrade. Runs on any
	// workstation regardless of Postgres availability.
	assert.Contains(t, jidMapSchema, "CREATE TABLE IF NOT EXISTS jid_sessions")
	assert.Contains(t, jidMapSchema, "TIMESTAMPTZ")
	assert.Contains(t, jidMapSchema, "jid        TEXT PRIMARY KEY")
}

// -- integration --

func TestIntegration_JIDMapPutAndGet(t *testing.T) {
	jm := openJIDMapTest(t)
	ctx := context.Background()

	require.NoError(t, jm.Put(ctx, "1234@s.whatsapp.net", "sess-1"))

	id, ok, err := jm.Get(ctx, "1234@s.whatsapp.net")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "sess-1", id)
}

func TestIntegration_JIDMapGetMissing(t *testing.T) {
	jm := openJIDMapTest(t)
	ctx := context.Background()

	_, ok, err := jm.Get(ctx, "missing")
	require.NoError(t, err)
	assert.False(t, ok, "unmapped jid must return ok=false, not ErrNoRows")
}

func TestIntegration_JIDMapPutReplaces(t *testing.T) {
	// Upsert semantics — the transport may rebind the same JID to a
	// fresh session (e.g. after wipe or re-pair). Callers rely on
	// this being a silent replace, not a duplicate-key error.
	jm := openJIDMapTest(t)
	ctx := context.Background()

	require.NoError(t, jm.Put(ctx, "x", "a"))
	require.NoError(t, jm.Put(ctx, "x", "b"))

	id, ok, err := jm.Get(ctx, "x")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "b", id)
}

func TestIntegration_JIDMapPreservesJIDString(t *testing.T) {
	// TEXT PRIMARY KEY must preserve the exact operator-visible
	// string — no case-folding, no whitespace-trimming. Two JIDs
	// that differ only by case are distinct keys.
	jm := openJIDMapTest(t)
	ctx := context.Background()

	require.NoError(t, jm.Put(ctx, "Abc@s.whatsapp.net", "upper"))
	require.NoError(t, jm.Put(ctx, "abc@s.whatsapp.net", "lower"))

	got, ok, err := jm.Get(ctx, "Abc@s.whatsapp.net")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "upper", got)

	got, ok, err = jm.Get(ctx, "abc@s.whatsapp.net")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "lower", got)
}
