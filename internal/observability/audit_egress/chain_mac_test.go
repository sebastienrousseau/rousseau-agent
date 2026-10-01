package audit_egress

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rechain recomputes every hash after an edit, which is all an
// attacker with write access needs to defeat the unkeyed chain.
func rechain(records []Record) {
	prev := ""
	for i := range records {
		records[i].Chain.PrevHash = prev
		records[i].Chain.Hash = canonicalHash(records[i])
		prev = records[i].Chain.Hash
	}
}

// TestVerifyChainMAC_DetectsRecomputedChain pins why the MAC exists:
// an edited, fully re-hashed chain passes VerifyChain, but not the
// keyed check, because the attacker does not hold the key.
func TestVerifyChainMAC_DetectsRecomputedChain(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	cap := &captureSink{}
	c := NewChainedSink(cap, WithChainHMACKey(key))
	for _, v := range []string{"a", "b", "c"} {
		require.NoError(t, c.Emit(context.Background(), Record{Verb: v}))
	}
	records := cap.snapshot()
	for _, r := range records {
		require.NotEmpty(t, r.Chain.MAC)
	}
	require.NoError(t, VerifyChainMAC(records, key))

	records[1].Verb = "TAMPERED"
	rechain(records)
	assert.NoError(t, VerifyChain(records), "the unkeyed chain cannot tell")
	err := VerifyChainMAC(records, key)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "index 1")

	assert.Error(t, VerifyChainMAC(cap.snapshot(), []byte("wrong-key-wrong-key-wrong-key-xx")))
}

func TestChainedSink_NoKeyLeavesMACEmpty(t *testing.T) {
	cap := &captureSink{}
	c := NewChainedSink(cap)
	require.NoError(t, c.Emit(context.Background(), Record{Verb: "a"}))
	assert.Empty(t, cap.snapshot()[0].Chain.MAC, "unchanged wire format without a key")
}
