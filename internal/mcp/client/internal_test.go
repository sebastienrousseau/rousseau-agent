package client

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise the unexported helpers (splitEnv, mergeEnv,
// marshalArgs, buildRequest, boundedBuffer). They live in the same
// package (no _test suffix) so they can reach them.

func TestSplitEnv_KeyValue(t *testing.T) {
	k, v, ok := splitEnv("PATH=/usr/bin")
	assert.True(t, ok)
	assert.Equal(t, "PATH", k)
	assert.Equal(t, "/usr/bin", v)
}

func TestSplitEnv_EmptyValue(t *testing.T) {
	k, v, ok := splitEnv("EMPTY=")
	assert.True(t, ok)
	assert.Equal(t, "EMPTY", k)
	assert.Equal(t, "", v)
}

func TestSplitEnv_NoEquals(t *testing.T) {
	k, v, ok := splitEnv("NOEQUALS")
	assert.False(t, ok)
	assert.Equal(t, "NOEQUALS", k)
	assert.Equal(t, "", v)
}

func TestMergeEnv_EmptyOverridesReturnsBase(t *testing.T) {
	base := []string{"A=1", "B=2"}
	got := mergeEnv(base, nil)
	assert.Equal(t, base, got)
}

func TestMergeEnv_OverridesReplace(t *testing.T) {
	got := mergeEnv([]string{"PATH=/x", "HOME=/y"}, map[string]string{"HOME": "/rousseau"})
	assert.Contains(t, got, "PATH=/x")
	assert.Contains(t, got, "HOME=/rousseau")
	assert.NotContains(t, got, "HOME=/y")
}

func TestMergeEnv_EmptyValueUnsets(t *testing.T) {
	got := mergeEnv([]string{"A=1", "B=2"}, map[string]string{"A": ""})
	assert.NotContains(t, got, "A=1")
	assert.NotContains(t, got, "A=")
	assert.Contains(t, got, "B=2")
}

func TestMarshalArgs_NilBecomesEmptyObject(t *testing.T) {
	raw, err := marshalArgs(nil)
	assert.NoError(t, err)
	assert.Equal(t, "{}", string(raw))
}

func TestMarshalArgs_EmptyRawBecomesEmptyObject(t *testing.T) {
	raw, err := marshalArgs(json.RawMessage{})
	assert.NoError(t, err)
	assert.Equal(t, "{}", string(raw))
}

func TestMarshalArgs_RawJSONPassthrough(t *testing.T) {
	raw, err := marshalArgs(json.RawMessage(`{"k":"v"}`))
	assert.NoError(t, err)
	assert.Equal(t, `{"k":"v"}`, string(raw))
}

func TestMarshalArgs_MarshalsStruct(t *testing.T) {
	raw, err := marshalArgs(map[string]string{"foo": "bar"})
	assert.NoError(t, err)
	assert.Contains(t, string(raw), `"foo":"bar"`)
}

func TestBuildRequest_EmbedsID(t *testing.T) {
	env, err := buildRequest(42, "some/method", nil)
	assert.NoError(t, err)
	assert.Equal(t, "2.0", env.JSONRPC)
	assert.Equal(t, "some/method", env.Method)
	assert.Contains(t, string(env.ID), "42")
	assert.Empty(t, env.Params)
}

func TestBuildRequest_MarshalsParams(t *testing.T) {
	env, err := buildRequest(1, "m", map[string]string{"k": "v"})
	assert.NoError(t, err)
	assert.Contains(t, string(env.Params), `"k":"v"`)
}

func TestBoundedBuffer_LimitedToMaxBytes(t *testing.T) {
	buf := newBoundedBuffer(10)
	// Write more than max; only the tail should survive.
	_, err := buf.Write([]byte(strings.Repeat("A", 5)))
	assert.NoError(t, err)
	_, err = buf.Write([]byte(strings.Repeat("B", 20)))
	assert.NoError(t, err)
	tail := buf.Tail()
	assert.Len(t, tail, 10, "buffer must respect max cap")
	assert.Equal(t, strings.Repeat("B", 10), tail)
}

func TestClient_NameGetter(t *testing.T) {
	// The Name() getter is trivial but part of the public API surface.
	c := &Client{name: "test-name"}
	assert.Equal(t, "test-name", c.Name())
}

func TestBuildRequest_ParamsMarshalFailureSurfaces(t *testing.T) {
	// A channel cannot be JSON-marshalled — buildRequest must
	// surface the error rather than silently produce a malformed
	// Envelope that the server would reject with a confusing
	// "parse error" downstream.
	_, err := buildRequest(1, "m", make(chan struct{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal params for m")
}

func TestMergeEnv_UnsetRemovesPreExistingEntry(t *testing.T) {
	// The "" override for a key present in base means "remove it
	// from the merged env" — matches exec.Cmd's own convention.
	// Distinct from the "override replaces" branch: here the
	// override key never survives into the output.
	got := mergeEnv([]string{"KEEP=1", "GONE=old"}, map[string]string{"GONE": ""})
	assert.Contains(t, got, "KEEP=1")
	// Neither the base entry nor the empty-value override should
	// appear:
	for _, e := range got {
		assert.NotContains(t, e, "GONE=", "GONE must be unset, not present with any value")
	}
}

func TestMergeEnv_NoOverridesReturnsBase(t *testing.T) {
	// Fast path: empty overrides returns base unchanged (no
	// allocation, no copy). Prevents a regression where a caller
	// might see a re-ordered / re-allocated env when they passed
	// nil overrides.
	base := []string{"A=1", "B=2"}
	got := mergeEnv(base, nil)
	assert.Equal(t, base, got)
}
