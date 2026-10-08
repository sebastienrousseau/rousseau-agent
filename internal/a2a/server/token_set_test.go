package server

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestTokenSet_StoresDigestsOnly: the allowlist keeps SHA-256 digests,
// never the bearer tokens, and matches by constant-time comparison.
func TestTokenSet_StoresDigestsOnly(t *testing.T) {
	const tok = "test-token-digest-only"
	set := newTokenSet([]string{tok, "test-token-other"})
	assert.NotContains(t, fmt.Sprintf("%#v", set), tok, "plaintext token must not be retained")

	assert.True(t, set.contains(tok))
	assert.True(t, set.contains("test-token-other"))
	assert.False(t, set.contains(""))
	assert.False(t, set.contains("test-token"), "a prefix of a valid token must not match")
	assert.False(t, set.contains(tok+"x"))
}

func TestTokenSet_Empty(t *testing.T) {
	assert.False(t, newTokenSet(nil).contains(""))
}
