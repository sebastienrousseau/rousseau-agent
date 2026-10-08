package audit_egress

import (
	"encoding/binary"
	"strconv"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// decodeHashInputV2 splits a v2 hash input back into its fields. It
// fails on a wrong version byte, a truncated field or trailing bytes,
// so a successful decode that returns the original fields proves the
// encoding is injective for that input.
func decodeHashInputV2(t *testing.T, in []byte) [][]byte {
	t.Helper()
	require.NotEmpty(t, in)
	require.Equal(t, byte(ChainVersionV2), in[0])
	rest := in[1:]
	var out [][]byte
	for len(rest) > 0 {
		n, w := binary.Uvarint(rest)
		require.Positive(t, w, "bad length prefix")
		rest = rest[w:]
		require.LessOrEqual(t, n, uint64(len(rest)), "field overruns input")
		out = append(out, rest[:n])
		rest = rest[n:]
	}
	return out
}

// FuzzHashInputV2 checks the v2 encoding round-trips every field
// exactly (so no two records share an input) and that the canonical
// Detail is a fixed point that rejects invalid UTF-8.
func FuzzHashInputV2(f *testing.F) {
	f.Add("alice\x00run", "bash", "k", "v", uint64(0), int64(0))
	f.Add("alice", "run\x00bash", "cmd", "<ls> &  ", uint64(7), int64(1_700_000_000_000_000_000))
	f.Add("", "", "k\xff", "v", uint64(1), int64(-1))
	f.Add("a", "b", "k", "\xc3", ^uint64(0), int64(42))
	f.Fuzz(func(t *testing.T, actor, verb, key, val string, seq uint64, ns int64) {
		r := Record{At: time.Unix(0, ns), Actor: actor, Verb: verb, Detail: map[string]any{key: val}}
		r.Chain.Sequence = seq
		in, err := hashInputV2(r)
		if !utf8.ValidString(key) || !utf8.ValidString(val) {
			require.Error(t, err, "invalid UTF-8 in Detail must be rejected")
			return
		}
		require.NoError(t, err)
		fields := decodeHashInputV2(t, in)
		require.Len(t, fields, 10)
		require.Equal(t, strconv.FormatUint(seq, 10), string(fields[0]))
		require.Equal(t, strconv.FormatInt(r.At.UTC().UnixNano(), 10), string(fields[2]))
		require.Equal(t, actor, string(fields[4]))
		require.Equal(t, verb, string(fields[5]))

		d, err := parseDetail(string(fields[9]))
		require.NoError(t, err)
		require.Equal(t, val, d[key])
		again, err := canonicalDetailV2(d)
		require.NoError(t, err)
		require.Equal(t, string(fields[9]), string(again), "canonical Detail must be a fixed point")
	})
}
