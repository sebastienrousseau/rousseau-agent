package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openAuditChainState(t *testing.T) *AuditChainState {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup
	a, err := NewAuditChainState(ctx, store)
	require.NoError(t, err)
	return a
}

func TestAuditChainState_LoadEmptyReturnsZeros(t *testing.T) {
	// Fresh install / never-emitted: Load returns (0, "", nil)
	// so ChainedSink treats it as "start a fresh chain".
	a := openAuditChainState(t)
	seq, hash, err := a.Load(context.Background())
	require.NoError(t, err)
	assert.Zero(t, seq)
	assert.Empty(t, hash)
}

func TestAuditChainState_SaveThenLoadRoundtrips(t *testing.T) {
	a := openAuditChainState(t)
	ctx := context.Background()

	require.NoError(t, a.Save(ctx, 42, "0123456789abcdef"))

	seq, hash, err := a.Load(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 42, seq)
	assert.Equal(t, "0123456789abcdef", hash)
}

func TestAuditChainState_ReSaveOverwritesTail(t *testing.T) {
	// Every Emit calls Save. Newer values replace older ones —
	// there's only ever one row per active chain. Load must
	// return the latest.
	a := openAuditChainState(t)
	ctx := context.Background()

	require.NoError(t, a.Save(ctx, 1, "hash-1"))
	require.NoError(t, a.Save(ctx, 2, "hash-2"))
	require.NoError(t, a.Save(ctx, 3, "hash-3"))

	seq, hash, err := a.Load(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 3, seq)
	assert.Equal(t, "hash-3", hash)
}

func TestAuditChainState_SingleRowInvariantEnforced(t *testing.T) {
	// The schema's CHECK (id = 1) prevents inserting a second
	// row. Save uses INSERT ... ON CONFLICT so this is not
	// externally reachable via the API, but a manual insert
	// MUST be rejected — regression guard for anyone tempted
	// to widen the store to multi-chain.
	a := openAuditChainState(t)
	ctx := context.Background()

	_, err := a.db.ExecContext(ctx,
		`INSERT INTO audit_chain_state (id, last_sequence, last_hash, updated_at) VALUES (2, 0, '', '')`)
	require.Error(t, err, "second row must be rejected by the id=1 CHECK constraint")
}

func TestAuditChainState_CorruptedNegativeSequenceReturnsFreshChain(t *testing.T) {
	// Defensive: a corrupted row (negative sequence) MUST NOT
	// wedge the daemon. Load returns (0, "") so ChainedSink
	// starts fresh — SIEM sees a chain break, operator sees
	// the row.
	a := openAuditChainState(t)
	ctx := context.Background()
	_, err := a.db.ExecContext(ctx,
		`INSERT INTO audit_chain_state (id, last_sequence, last_hash, updated_at) VALUES (1, -1, 'broken', ?)`,
		"2026-01-01T00:00:00.000Z")
	require.NoError(t, err)

	seq, hash, err := a.Load(ctx)
	require.NoError(t, err, "corrupted row must not error")
	assert.Zero(t, seq)
	assert.Empty(t, hash)
}
