package audit_egress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureSink is an in-memory Sink for chain tests. It records
// every emitted record in order so tests can inspect the
// stamped ChainInfo without going through OTLP.
type captureSink struct {
	mu      sync.Mutex
	records []Record
	emitErr error
	closed  bool
}

func (c *captureSink) Emit(_ context.Context, r Record) error {
	if c.emitErr != nil {
		return c.emitErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r)
	return nil
}

func (c *captureSink) Close(context.Context) error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (c *captureSink) snapshot() []Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Record, len(c.records))
	copy(out, c.records)
	return out
}

// -- ChainedSink construction --

func TestNewChainedSink_NilInnerReturnsNil(t *testing.T) {
	// Fail-at-call-site guard: passing a nil sink is a wiring
	// bug — the constructor makes it visible rather than
	// deferring to the first Emit panic.
	assert.Nil(t, NewChainedSink(nil))
}

// -- Emit stamps chain metadata --

func TestChainedSink_FirstRecordStartsAtZero(t *testing.T) {
	cap := &captureSink{}
	c := NewChainedSink(cap)

	require.NoError(t, c.Emit(context.Background(), Record{Verb: "start"}))

	got := cap.snapshot()
	require.Len(t, got, 1)
	assert.EqualValues(t, 0, got[0].Chain.Sequence)
	assert.Empty(t, got[0].Chain.PrevHash, "first record's PrevHash must be empty")
	assert.NotEmpty(t, got[0].Chain.Hash)
}

func TestChainedSink_SecondRecordPrevHashLinksFirst(t *testing.T) {
	cap := &captureSink{}
	c := NewChainedSink(cap)

	require.NoError(t, c.Emit(context.Background(), Record{Verb: "a"}))
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "b"}))

	got := cap.snapshot()
	require.Len(t, got, 2)
	assert.Equal(t, got[0].Chain.Hash, got[1].Chain.PrevHash,
		"record 2's PrevHash must equal record 1's Hash")
	assert.EqualValues(t, 1, got[1].Chain.Sequence)
}

func TestChainedSink_HashDependsOnEveryField(t *testing.T) {
	// Load-bearing property: every user-supplied field flows into
	// the hash. Mutating ANY of them post-emit must break
	// VerifyChain. This test walks the fields one by one.
	cap := &captureSink{}
	c := NewChainedSink(cap)
	base := Record{
		At:       time.Unix(1_700_000_000, 0).UTC(),
		Category: "tool_call", Actor: "okta|alice",
		Verb: "run", Object: "bash", Result: "success",
		TraceID: "trace-42",
		Detail:  map[string]any{"cmd": "ls -la"},
	}
	require.NoError(t, c.Emit(context.Background(), base))
	original := cap.snapshot()[0].Chain.Hash

	// For each field: mint a fresh ChainedSink, mutate that
	// field, and confirm the hash differs. Fresh sink so
	// Sequence + PrevHash are equal across runs — only the
	// user-supplied field varies.
	mutations := []struct {
		name string
		fn   func(*Record)
	}{
		{"At", func(r *Record) { r.At = r.At.Add(time.Second) }},
		{"Category", func(r *Record) { r.Category = "auth" }},
		{"Actor", func(r *Record) { r.Actor = "okta|bob" }},
		{"Verb", func(r *Record) { r.Verb = "read" }},
		{"Object", func(r *Record) { r.Object = "write" }},
		{"Result", func(r *Record) { r.Result = "denied" }},
		{"TraceID", func(r *Record) { r.TraceID = "trace-99" }},
		{"Detail", func(r *Record) { r.Detail = map[string]any{"cmd": "rm -rf /"} }},
	}
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			fresh := &captureSink{}
			c := NewChainedSink(fresh)
			rec := base
			m.fn(&rec)
			require.NoError(t, c.Emit(context.Background(), rec))
			got := fresh.snapshot()[0].Chain.Hash
			assert.NotEqual(t, original, got,
				"mutation of %s must produce a different hash", m.name)
		})
	}
}

func TestChainedSink_DetailKeyOrderIndependent(t *testing.T) {
	// canonicalDetail sorts keys, so two equivalent Details in
	// different insertion orders MUST hash the same. Guards
	// against Go's random map iteration breaking chain integrity
	// across restarts.
	a := &captureSink{}
	b := &captureSink{}
	ca, cb := NewChainedSink(a), NewChainedSink(b)

	recA := Record{
		At: time.Unix(1, 0).UTC(),
		Detail: map[string]any{
			"first":  1,
			"second": "two",
			"third":  true,
		},
	}
	recB := Record{
		At: time.Unix(1, 0).UTC(),
		Detail: map[string]any{
			"third":  true,
			"first":  1,
			"second": "two",
		},
	}
	require.NoError(t, ca.Emit(context.Background(), recA))
	require.NoError(t, cb.Emit(context.Background(), recB))

	assert.Equal(t, a.snapshot()[0].Chain.Hash, b.snapshot()[0].Chain.Hash,
		"same-content, different-insertion-order Detail must hash identically")
}

func TestChainedSink_ConcurrentEmitsProduceMonotonicSequences(t *testing.T) {
	// Property: even under concurrent Emit, Sequence values are
	// unique and cover [0, N) exactly. The chain's HASH order
	// may not match caller intent (there's no per-goroutine
	// serialisation) but the sink's internal invariant — one
	// record per Sequence — must hold.
	cap := &captureSink{}
	c := NewChainedSink(cap)

	const n = 200
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			require.NoError(t, c.Emit(context.Background(), Record{Verb: fmt.Sprintf("v-%d", i)}))
		}(i)
	}
	wg.Wait()

	got := cap.snapshot()
	require.Len(t, got, n)
	seen := make(map[uint64]bool, n)
	for _, r := range got {
		seen[r.Chain.Sequence] = true
	}
	for i := uint64(0); i < n; i++ {
		assert.Contains(t, seen, i, "sequence %d must have been used exactly once", i)
	}
}

func TestChainedSink_EmitErrorAdvancesChain(t *testing.T) {
	// Design decision: a wrapped-sink error still advances the
	// chain — the operator's SIEM saw an attempted record, and
	// the wrapped sink's dropped-record counter is the
	// authoritative delivery signal. Alternative (skip on error)
	// would make chain integrity depend on delivery, which
	// undermines the whole "verify offline" property.
	cap := &captureSink{emitErr: errors.New("sink offline")}
	c := NewChainedSink(cap)

	err := c.Emit(context.Background(), Record{Verb: "start"})
	assert.Error(t, err, "wrapped-sink error must surface")

	// Sequence advanced despite error.
	cap.emitErr = nil
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "next"}))
	got := cap.snapshot()
	require.Len(t, got, 1, "only the second (non-error) Emit landed")
	assert.EqualValues(t, 1, got[0].Chain.Sequence,
		"sequence must have advanced past the failed emit")
}

func TestChainedSink_ClosePropagates(t *testing.T) {
	cap := &captureSink{}
	c := NewChainedSink(cap)
	require.NoError(t, c.Close(context.Background()))
	assert.True(t, cap.closed)
}

// -- VerifyChain --

func TestVerifyChain_EmptyIsValid(t *testing.T) {
	assert.NoError(t, VerifyChain(nil))
	assert.NoError(t, VerifyChain([]Record{}))
}

func TestVerifyChain_UntamperedBatchVerifies(t *testing.T) {
	cap := &captureSink{}
	c := NewChainedSink(cap)
	for i := 0; i < 10; i++ {
		require.NoError(t, c.Emit(context.Background(), Record{
			At:     time.Unix(int64(i), 0),
			Verb:   fmt.Sprintf("v-%d", i),
			Detail: map[string]any{"i": i},
		}))
	}
	assert.NoError(t, VerifyChain(cap.snapshot()))
}

func TestVerifyChain_MutationBreaksVerify(t *testing.T) {
	cap := &captureSink{}
	c := NewChainedSink(cap)
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "a"}))
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "b"}))
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "c"}))

	records := cap.snapshot()
	records[1].Verb = "TAMPERED"

	err := VerifyChain(records)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hash mismatch")
	assert.Contains(t, err.Error(), "index 1")
}

func TestVerifyChain_SequenceGapBreaks(t *testing.T) {
	cap := &captureSink{}
	c := NewChainedSink(cap)
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "a"}))
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "b"}))
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "c"}))

	records := cap.snapshot()
	// Drop the middle record — sequence gap.
	records = append(records[:1], records[2:]...)

	err := VerifyChain(records)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sequence gap")
}

func TestVerifyChain_ReorderBreaks(t *testing.T) {
	cap := &captureSink{}
	c := NewChainedSink(cap)
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "a"}))
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "b"}))
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "c"}))

	records := cap.snapshot()
	records[1], records[2] = records[2], records[1]

	err := VerifyChain(records)
	require.Error(t, err, "reordered batch must not verify")
}

func TestVerifyChain_InsertBreaks(t *testing.T) {
	// A forged record spliced into the middle of a real chain
	// MUST NOT verify — the forger doesn't know the real
	// PrevHash the following record expects.
	cap := &captureSink{}
	c := NewChainedSink(cap)
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "a"}))
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "c"}))

	records := cap.snapshot()
	forged := Record{Verb: "b-forged", Chain: ChainInfo{
		Sequence: 1,
		PrevHash: records[0].Chain.Hash,
	}}
	forged.Chain.Hash = canonicalHash(forged)
	// Splice forged as new index 1, shift real index 1 to 2.
	records = append([]Record{records[0], forged, records[1]}[:3], records[2:]...)

	err := VerifyChain(records)
	require.Error(t, err, "spliced record with wrong sequence for later record must not verify")
}

// -- OTLP wire representation --

func TestOTLPMarshaller_EmitsChainAttrsWhenPresent(t *testing.T) {
	cap := &captureSink{}
	c := NewChainedSink(cap)
	require.NoError(t, c.Emit(context.Background(), Record{
		At: time.Unix(1_700_000_000, 0), Verb: "start",
	}))

	body, err := marshalOTLPLogs(cap.snapshot())
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	// Dig for the three chain attribute keys — presence check
	// only; exact positions inside the OTLP envelope can shift.
	s := string(body)
	assert.Contains(t, s, "rousseau.audit.chain.sequence")
	assert.Contains(t, s, "rousseau.audit.chain.hash")
	assert.Contains(t, s, "rousseau.audit.chain.prev_hash")
}

func TestOTLPMarshaller_OmitsChainAttrsWhenAbsent(t *testing.T) {
	// Unchained record (no ChainedSink) → chain.* attrs must
	// NOT appear. Property: existing operators who never opt in
	// see the pre-chain wire shape byte-for-byte.
	body, err := marshalOTLPLogs([]Record{{Verb: "x"}})
	require.NoError(t, err)
	s := string(body)
	assert.NotContains(t, s, "rousseau.audit.chain")
}

// -- canonicalDetail --

func TestCanonicalDetail_EmptyIsBraces(t *testing.T) {
	assert.Equal(t, "{}", canonicalDetail(nil))
	assert.Equal(t, "{}", canonicalDetail(map[string]any{}))
}

func TestCanonicalDetail_UnmarshalableFallsBackToStringForm(t *testing.T) {
	// json.Marshal fails on channels / functions — the canonical
	// hasher must not panic, so it falls back to fmt-based
	// string rendering. Reproducible-hash contract: the same
	// bad-input map must produce the same hash across runs.
	ch := make(chan int)
	m := map[string]any{"chan": ch}
	first := canonicalDetail(m)
	second := canonicalDetail(m)
	assert.Equal(t, first, second, "unmarshalable-value fallback must still be deterministic")
}
