package postgres

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openAuditChainStateTest opens an AuditChainState on the CI/dev
// Postgres, applies the schema, and truncates so each test starts
// clean. Guarded on ROUSSEAU_TEST_POSTGRES_URL like the other
// integration tests in this package.
func openAuditChainStateTest(t *testing.T) (*AuditChainState, context.Context) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, requirePG(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	a, err := NewAuditChainState(ctx, store)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `TRUNCATE TABLE audit_chain_state`)
	require.NoError(t, err)
	return a, ctx
}

// -- unit-only (no DB) --

func TestNewAuditChainState_SchemaIdempotent(t *testing.T) {
	// Compile-check the const we ship — an accidental rename or
	// missing CHECK would silently allow multi-row corruption.
	assert.Contains(t, auditChainSchema, "CREATE TABLE IF NOT EXISTS audit_chain_state")
	assert.Contains(t, auditChainSchema, "id            INTEGER PRIMARY KEY CHECK (id = 1)")
	assert.Contains(t, auditChainSchema, "last_sequence BIGINT NOT NULL DEFAULT 0")
	assert.Contains(t, auditChainSchema, "TIMESTAMPTZ NOT NULL DEFAULT NOW()")
}

// -- integration --

func TestIntegration_AuditChain_LoadEmptyReturnsZeroAndNil(t *testing.T) {
	// Load-bearing: a fresh install / just-truncated row must
	// return (0, "", nil) so the ChainedSink starts a fresh
	// chain rather than crashing on ErrNoRows. Matches the
	// SQLite driver's contract.
	a, ctx := openAuditChainStateTest(t)
	seq, hash, err := a.Load(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), seq)
	assert.Empty(t, hash)
}

func TestIntegration_AuditChain_SaveThenLoadRoundtrip(t *testing.T) {
	a, ctx := openAuditChainStateTest(t)
	require.NoError(t, a.Save(ctx, 42, "sha256:abcdef"))

	seq, hash, err := a.Load(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(42), seq)
	assert.Equal(t, "sha256:abcdef", hash)
}

func TestIntegration_AuditChain_SaveOverwritesInPlace(t *testing.T) {
	// Save on the id=1 row must be an atomic UPSERT (ON CONFLICT
	// DO UPDATE), not an INSERT that appends. Pinned so a
	// well-meaning refactor to a "history table" would fail
	// this test rather than silently corrupt chain replay.
	a, ctx := openAuditChainStateTest(t)
	require.NoError(t, a.Save(ctx, 1, "h1"))
	require.NoError(t, a.Save(ctx, 2, "h2"))
	require.NoError(t, a.Save(ctx, 3, "h3"))

	seq, hash, err := a.Load(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(3), seq, "latest Save wins")
	assert.Equal(t, "h3", hash)
}

func TestIntegration_AuditChain_HandlesLargeSequence(t *testing.T) {
	// Regression pin: uint64 → BIGINT (signed 64-bit) round-trip
	// must survive sequences above 2^31 without silent
	// truncation. Uses 2^40 as a "clearly past INT32 max" but
	// still safe under INT64 max value.
	a, ctx := openAuditChainStateTest(t)
	const big = uint64(1) << 40
	require.NoError(t, a.Save(ctx, big, "big-hash"))
	seq, hash, err := a.Load(ctx)
	require.NoError(t, err)
	assert.Equal(t, big, seq)
	assert.Equal(t, "big-hash", hash)
}

func TestIntegration_AuditChain_ConcurrentSavesConverge(t *testing.T) {
	// The daemon's audit sink is a single-writer contract today,
	// but under multi-replica HA two daemons will point at the
	// same audit_chain_state row. Verify that concurrent Saves
	// (a) don't panic / error (b) leave the row in one of the
	// candidate states (last-writer-wins). SIEM chain-break
	// detection is the operator-visible safety net for the
	// race — no code here needs to "resolve" the conflict.
	a, ctx := openAuditChainStateTest(t)

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			assert.NoError(t, a.Save(ctx, uint64(i+1), "h"))
		}(i)
	}
	wg.Wait()

	seq, hash, err := a.Load(ctx)
	require.NoError(t, err)
	assert.Greater(t, seq, uint64(0), "one of the saves must have won")
	assert.LessOrEqual(t, seq, uint64(n))
	assert.Equal(t, "h", hash)
}

func TestIntegration_AuditChain_CheckConstraintPreventsSecondRow(t *testing.T) {
	// The single-row invariant is enforced by CHECK (id = 1).
	// Prove it: manual INSERT with id=2 must fail. Without this
	// pin a refactor that widens the PK would silently allow
	// two active chains — a much harder bug to notice than an
	// insert-time constraint violation.
	a, ctx := openAuditChainStateTest(t)
	// Direct DB access via the shared connection.
	store, err := Open(ctx, requirePG(t))
	require.NoError(t, err)
	defer func() { _ = store.Close() }() //nolint:errcheck // test cleanup

	_, err = store.db.ExecContext(ctx,
		`INSERT INTO audit_chain_state (id, last_sequence, last_hash) VALUES (2, 0, '')`)
	assert.Error(t, err, "CHECK (id = 1) must reject id=2")

	// The normal path still works.
	require.NoError(t, a.Save(ctx, 1, "h"))
}
