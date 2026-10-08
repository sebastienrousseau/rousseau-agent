package audit_egress

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nulShiftedPair returns two records whose fields differ only in
// where a NUL byte sits between Actor and Verb. Under the v1
// NUL-joined encoding they serialise to the same bytes.
func nulShiftedPair() (Record, Record) {
	at := time.Unix(1_700_000_000, 0).UTC()
	a := Record{At: at, Actor: "alice\x00run", Verb: "bash"}
	b := Record{At: at, Actor: "alice", Verb: "run\x00bash"}
	return a, b
}

func TestChainV2_NULShiftedPairHashesDifferently(t *testing.T) {
	a, b := nulShiftedPair()
	// v1 is ambiguous: this documents the bug the v2 encoding fixes.
	assert.Equal(t, canonicalHash(a), canonicalHash(b), "v1 joins with NUL and cannot tell the pair apart")

	ha, err := canonicalHashV2(a)
	require.NoError(t, err)
	hb, err := canonicalHashV2(b)
	require.NoError(t, err)
	assert.NotEqual(t, ha, hb, "v2 length-prefixes every field, so moving a boundary changes the hash")
}

func TestChainV2_HashInputIsVersionedAndLengthPrefixed(t *testing.T) {
	in, err := hashInputV2(Record{Verb: "run"})
	require.NoError(t, err)
	require.NotEmpty(t, in)
	assert.Equal(t, byte(ChainVersionV2), in[0], "the encoding starts with the version byte")
	assert.True(t, bytes.Contains(in, []byte{3, 'r', 'u', 'n'}), "a field is uvarint(len) followed by its bytes")
}

func TestChainedSink_StampsVersion2(t *testing.T) {
	cap := &captureSink{}
	c := NewChainedSink(cap)
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "start"}))
	got := cap.snapshot()
	require.Len(t, got, 1)
	assert.EqualValues(t, ChainVersionV2, got[0].Chain.Version)
	want, err := canonicalHashV2(got[0])
	require.NoError(t, err)
	assert.Equal(t, want, got[0].Chain.Hash)
}

// v1Records builds a valid legacy chain the way a pre-v2 daemon did.
func v1Records(n int) []Record {
	out := make([]Record, n)
	prev := ""
	for i := range out {
		r := Record{Verb: "legacy", Object: strings.Repeat("x", i)}
		r.Chain.Sequence = uint64(i)
		r.Chain.PrevHash = prev
		r.Chain.Hash = canonicalHash(r)
		prev = r.Chain.Hash
		out[i] = r
	}
	return out
}

func TestVerifyChain_MixedV1ThenV2Verifies(t *testing.T) {
	legacy := v1Records(3)
	tail := legacy[len(legacy)-1].Chain
	cap := &captureSink{}
	store := &memChainStore{seq: tail.Sequence, hash: tail.Hash, hasRow: true}
	c := NewChainedSink(cap, WithChainStore(store))
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "a"}))
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "b", Detail: map[string]any{"n": 1}}))

	upgraded := cap.snapshot()
	assert.EqualValues(t, ChainVersionV2, upgraded[0].Chain.Version)
	legacy = append(legacy, upgraded...)
	assert.NoError(t, VerifyChain(legacy), "a chain that upgrades from v1 to v2 must still verify")
}

func TestVerifyChain_RejectsDowngradeAfterV2(t *testing.T) {
	cap := &captureSink{}
	c := NewChainedSink(cap)
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "a"}))
	recs := cap.snapshot()
	next := Record{Verb: "b"}
	next.Chain.Sequence = 1
	next.Chain.PrevHash = recs[0].Chain.Hash
	next.Chain.Hash = canonicalHash(next) // a v1 record after a v2 one
	err := VerifyChain(append(recs, next))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version")
}

func TestVerifyChain_RejectsUnknownVersion(t *testing.T) {
	r := Record{Verb: "a"}
	r.Chain.Version = 3
	r.Chain.Hash = "00"
	err := VerifyChain([]Record{r})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version")
}

func TestVerifyChain_V2MutationBreaks(t *testing.T) {
	cap := &captureSink{}
	c := NewChainedSink(cap)
	require.NoError(t, c.Emit(context.Background(), Record{Actor: "alice", Verb: "run"}))
	recs := cap.snapshot()
	recs[0].Actor = "mallory"
	assert.Error(t, VerifyChain(recs))
}

func TestCanonicalDetailV2_RejectsInvalidUTF8(t *testing.T) {
	for name, d := range map[string]map[string]any{
		"value":  {"k": "ok\xff"},
		"key":    {"k\xfe": "v"},
		"nested": {"k": map[string]any{"n": []any{"a", "\xc3"}}},
		"struct": {"k": struct{ S string }{S: "\xff"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := canonicalDetailV2(d)
			assert.Error(t, err, "invalid UTF-8 must be rejected, not silently replaced")
		})
	}
}

func TestCanonicalDetailV2_ValidReplacementCharIsKept(t *testing.T) {
	// A literal U+FFFD is valid UTF-8 and must not be confused with
	// an invalid byte that json.Marshal would have replaced.
	b, err := canonicalDetailV2(map[string]any{"k": "�"})
	require.NoError(t, err)
	assert.Contains(t, string(b), "�")
}

func TestCanonicalDetailV2_UnmarshalableIsAnError(t *testing.T) {
	_, err := canonicalDetailV2(map[string]any{"c": make(chan int)})
	assert.Error(t, err)
}

func TestChainedSink_InvalidUTF8DetailEmitsDetailError(t *testing.T) {
	cap := &captureSink{}
	c := NewChainedSink(cap)
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "run", Detail: map[string]any{"cmd": "ls \xff"}}))
	got := cap.snapshot()
	require.Len(t, got, 1)
	_, hasCmd := got[0].Detail["cmd"]
	assert.False(t, hasCmd, "the unhashable detail must not be emitted")
	assert.Contains(t, got[0].Detail, "detail_error")
	assert.NoError(t, VerifyChain(got), "the substituted record must verify")
}

func TestVerifyChain_V2RecordWithInvalidDetailFails(t *testing.T) {
	cap := &captureSink{}
	c := NewChainedSink(cap)
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "run", Detail: map[string]any{"cmd": "ls"}}))
	recs := cap.snapshot()
	recs[0].Detail = map[string]any{"cmd": "ls\xff"}
	assert.Error(t, VerifyChain(recs))
}

func TestCanonicalDetailV2_StructFieldOrderIsCanonical(t *testing.T) {
	type ba struct {
		B int
		A string
	}
	got, err := canonicalDetailV2(map[string]any{"s": ba{B: 1, A: "x"}})
	require.NoError(t, err)
	assert.Equal(t, `{"s":{"A":"x","B":1}}`, string(got))
}

func TestOTLPMarshaller_EmitsChainVersion(t *testing.T) {
	cap := &captureSink{}
	c := NewChainedSink(cap)
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "start"}))
	body, err := marshalOTLPLogs(cap.snapshot())
	require.NoError(t, err)
	assert.Contains(t, string(body), `"rousseau.audit.chain.version"`)
}

// emitExport emits recs through a keyed chain and returns the OTLP
// JSON the sink would push.
func emitExport(t *testing.T, key []byte, recs ...Record) []byte {
	t.Helper()
	cap := &captureSink{}
	c := NewChainedSink(cap, WithChainHMACKey(key))
	for _, r := range recs {
		require.NoError(t, c.Emit(context.Background(), r))
	}
	body, err := marshalOTLPLogs(cap.snapshot())
	require.NoError(t, err)
	return body
}

func TestParseOTLPLogs_ExportVerifies(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	at := time.Unix(1_700_000_000, 123).UTC()
	body := emitExport(t, key,
		Record{
			At: at, Category: "tool_call", Actor: "alice", Verb: "run", Object: "bash", Result: "success",
			TraceID: "abc",
			Detail: map[string]any{
				"cmd": "ls <x> & y", "n": 12345678901234567, "f": 1.5,
				"nest": map[string]any{"z": 1, "a": []int{1, 2}},
			},
		},
		Record{At: at.Add(time.Second), Verb: "stop"},
	)
	recs, err := ParseOTLPLogs(bytes.NewReader(body))
	require.NoError(t, err)
	require.Len(t, recs, 2)
	assert.NoError(t, VerifyChainMAC(recs, key))

	tampered := bytes.Replace(body, []byte(`"alice"`), []byte(`"mallory"`), 1)
	require.NotEqual(t, body, tampered)
	recs, err = ParseOTLPLogs(bytes.NewReader(tampered))
	require.NoError(t, err)
	assert.Error(t, VerifyChain(recs))
}

func TestParseOTLPLogs_ConcatenatedPayloadsAndUnchainedSkipped(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	body := emitExport(t, key, Record{Verb: "a"})
	plain, err := marshalOTLPLogs([]Record{{Verb: "unchained"}})
	require.NoError(t, err)
	stream := append(append(append([]byte{}, body...), '\n'), plain...)
	recs, err := ParseOTLPLogs(bytes.NewReader(stream))
	require.NoError(t, err)
	require.Len(t, recs, 1)
	assert.Equal(t, "a", recs[0].Verb)
}

func TestParseOTLPLogs_Malformed(t *testing.T) {
	for name, in := range map[string]string{
		"truncated": `{"resourceLogs": [`,
		"badseq":    `{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"attributes":[{"key":"rousseau.audit.chain.hash","value":{"stringValue":"aa"}},{"key":"rousseau.audit.chain.sequence","value":{"stringValue":"x"}}]}]}]}]}`,
		"badver":    `{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"attributes":[{"key":"rousseau.audit.chain.hash","value":{"stringValue":"aa"}},{"key":"rousseau.audit.chain.version","value":{"stringValue":"300"}}]}]}]}]}`,
		"badtime":   `{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"timeUnixNano":"soon","attributes":[{"key":"rousseau.audit.chain.hash","value":{"stringValue":"aa"}}]}]}]}]}`,
		"baddetail": `{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"attributes":[{"key":"rousseau.audit.chain.hash","value":{"stringValue":"aa"}},{"key":"rousseau.audit.detail","value":{"stringValue":"[1"}}]}]}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseOTLPLogs(strings.NewReader(in))
			assert.Error(t, err)
		})
	}
}

func TestChainedSink_StampsAtBeforeHashing(t *testing.T) {
	// The OTLP sink fills a zero At with time.Now after the chain has
	// hashed the record, so the exported record would never verify.
	cap := &captureSink{}
	c := NewChainedSink(cap)
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "start"}))
	got := cap.snapshot()
	require.Len(t, got, 1)
	assert.False(t, got[0].At.IsZero(), "the chain must stamp At before it hashes")
	assert.NoError(t, VerifyChain(got))
}

func TestVerifyChainSegment_AnchorsOnFirstRecord(t *testing.T) {
	cap := &captureSink{}
	c := NewChainedSink(cap)
	for _, v := range []string{"a", "b", "c"} {
		require.NoError(t, c.Emit(context.Background(), Record{Verb: v}))
	}
	recs := cap.snapshot()[1:]
	assert.Error(t, VerifyChain(recs), "a window that does not start at genesis is not a full chain")
	assert.NoError(t, Verify(recs, VerifyOptions{Segment: true}))
}
