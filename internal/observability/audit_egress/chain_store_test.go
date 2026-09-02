package audit_egress

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memChainStore is an in-memory ChainStore for testing the
// resume path. Simulates SQLite semantics: Save persists, Load
// returns the most recent value. loadErr / saveErr let tests
// exercise the failure branches.
type memChainStore struct {
	mu      sync.Mutex
	seq     uint64
	hash    string
	hasRow  bool
	saves   int
	loadErr error
	saveErr error
}

func (m *memChainStore) Load(context.Context) (uint64, string, error) {
	if m.loadErr != nil {
		return 0, "", m.loadErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.hasRow {
		return 0, "", nil
	}
	return m.seq, m.hash, nil
}

func (m *memChainStore) Save(_ context.Context, seq uint64, hash string) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq = seq
	m.hash = hash
	m.hasRow = true
	m.saves++
	return nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// -- resume behaviour --

func TestChainedSink_FreshStoreStartsAtZero(t *testing.T) {
	// Empty store (no prior daemon) → chain starts at 0 with
	// empty PrevHash, matching the un-persisted default.
	inner := &captureSink{}
	store := &memChainStore{}
	c := NewChainedSink(inner, WithChainStore(store), WithChainLogger(discardLogger()))
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "start"}))

	got := inner.snapshot()
	require.Len(t, got, 1)
	assert.EqualValues(t, 0, got[0].Chain.Sequence)
	assert.Empty(t, got[0].Chain.PrevHash)
}

func TestChainedSink_ResumeFromStoredState(t *testing.T) {
	// Second-daemon-instance path: store holds
	// (sequence=41, hash=<h41>) from the previous run. Next
	// record MUST use Sequence=42, PrevHash=<h41> — the SIEM
	// sees one continuous chain across the restart.
	inner := &captureSink{}
	store := &memChainStore{seq: 41, hash: "0123456789abcdef", hasRow: true}

	c := NewChainedSink(inner, WithChainStore(store), WithChainLogger(discardLogger()))
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "post_restart"}))

	got := inner.snapshot()
	require.Len(t, got, 1)
	assert.EqualValues(t, 42, got[0].Chain.Sequence, "sequence must resume at last_stored+1")
	assert.Equal(t, "0123456789abcdef", got[0].Chain.PrevHash, "PrevHash must link to the pre-restart chain")
}

func TestChainedSink_ResumeThenEmitPersistsNewState(t *testing.T) {
	// Property: after Emit, the store carries the new
	// (sequence, hash). Confirms the Save-inside-critical-
	// section path.
	inner := &captureSink{}
	store := &memChainStore{seq: 5, hash: "prev-hash", hasRow: true}
	c := NewChainedSink(inner, WithChainStore(store), WithChainLogger(discardLogger()))

	require.NoError(t, c.Emit(context.Background(), Record{Verb: "step-6"}))
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "step-7"}))

	// store now reflects seq=7 (last emitted) + its hash.
	store.mu.Lock()
	assert.EqualValues(t, 7, store.seq)
	require.Len(t, inner.snapshot(), 2)
	assert.Equal(t, inner.snapshot()[1].Chain.Hash, store.hash)
	assert.Equal(t, 2, store.saves)
	store.mu.Unlock()
}

func TestChainedSink_ResumeErrorFallsBackToFreshChain(t *testing.T) {
	// Load-side failure (dead DB, corrupted row) must NOT
	// wedge the daemon. Chain starts fresh at 0; the SIEM sees
	// a chain break, which is the visible-and-alertable
	// alternative to a silent gap.
	inner := &captureSink{}
	store := &memChainStore{loadErr: errors.New("disk gone")}
	c := NewChainedSink(inner, WithChainStore(store), WithChainLogger(discardLogger()))
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "start-after-failure"}))

	got := inner.snapshot()
	require.Len(t, got, 1)
	assert.EqualValues(t, 0, got[0].Chain.Sequence)
	assert.Empty(t, got[0].Chain.PrevHash)
}

func TestChainedSink_SaveErrorDoesNotAbortEmit(t *testing.T) {
	// Save-side failure must NOT block the record from
	// reaching the wrapped sink. The chain advances in-memory
	// regardless — the operator's SIEM still gets the record;
	// the daemon just loses cross-restart continuity for that
	// specific one.
	inner := &captureSink{}
	store := &memChainStore{saveErr: errors.New("db locked")}
	c := NewChainedSink(inner, WithChainStore(store), WithChainLogger(discardLogger()))

	require.NoError(t, c.Emit(context.Background(), Record{Verb: "step-1"}))
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "step-2"}))

	got := inner.snapshot()
	require.Len(t, got, 2, "Emit must succeed even when persistence fails")
	assert.EqualValues(t, 0, got[0].Chain.Sequence)
	assert.EqualValues(t, 1, got[1].Chain.Sequence)
	assert.Equal(t, got[0].Chain.Hash, got[1].Chain.PrevHash,
		"in-memory chain still links even when Save fails")
}

func TestChainedSink_NoStoreDoesNotSave(t *testing.T) {
	// Property: the store is truly optional. Un-passed store
	// means zero DB writes even under load.
	inner := &captureSink{}
	c := NewChainedSink(inner, WithChainLogger(discardLogger()))
	for i := 0; i < 5; i++ {
		require.NoError(t, c.Emit(context.Background(), Record{Verb: "n"}))
	}
	assert.Len(t, inner.snapshot(), 5)
	// No store to inspect — the property is "didn't panic + emitted 5".
}

func TestChainedSink_ResumedChainVerifiesEndToEnd(t *testing.T) {
	// End-to-end: build a batch across two "daemon lifetimes"
	// separated by a rebuild of the ChainedSink from the same
	// store, then run VerifyChain over the concatenated
	// records. The whole thing must verify as one chain.
	inner := &captureSink{}
	store := &memChainStore{}
	c1 := NewChainedSink(inner, WithChainStore(store), WithChainLogger(discardLogger()))
	require.NoError(t, c1.Emit(context.Background(), Record{Verb: "pre-restart-1"}))
	require.NoError(t, c1.Emit(context.Background(), Record{Verb: "pre-restart-2"}))

	// Simulate restart: fresh ChainedSink, same underlying
	// state, same wrapped sink (SIEM keeps receiving into the
	// same stream).
	c2 := NewChainedSink(inner, WithChainStore(store), WithChainLogger(discardLogger()))
	require.NoError(t, c2.Emit(context.Background(), Record{Verb: "post-restart-1"}))
	require.NoError(t, c2.Emit(context.Background(), Record{Verb: "post-restart-2"}))

	all := inner.snapshot()
	require.Len(t, all, 4)
	assert.EqualValues(t, 0, all[0].Chain.Sequence)
	assert.EqualValues(t, 1, all[1].Chain.Sequence)
	assert.EqualValues(t, 2, all[2].Chain.Sequence, "restart must continue at seq=2, not restart at 0")
	assert.EqualValues(t, 3, all[3].Chain.Sequence)
	assert.NoError(t, VerifyChain(all), "concatenated pre+post-restart batch must verify as one chain")
}
