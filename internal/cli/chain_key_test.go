package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

const testChainKey = "0123456789abcdef0123456789abcdef"

// L-10: a chain key other users can read is refused.
func TestReadChainKey_RequiresOwnerOnlyMode(t *testing.T) {
	dir := t.TempDir()
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o660} {
		p := filepath.Join(dir, "k-"+mode.String())
		require.NoError(t, os.WriteFile(p, []byte(testChainKey+"\n"), 0o600))
		require.NoError(t, os.Chmod(p, mode))
		_, err := readChainKey(p)
		require.Error(t, err, "mode %v", mode)
		assert.Contains(t, err.Error(), "0600", "mode %v", mode)
	}
	for _, mode := range []os.FileMode{0o600, 0o400} {
		p := filepath.Join(dir, "ok-"+mode.String())
		require.NoError(t, os.WriteFile(p, []byte(testChainKey+"\n"), 0o600))
		require.NoError(t, os.Chmod(p, mode))
		key, err := readChainKey(p)
		require.NoError(t, err, "mode %v", mode)
		assert.Equal(t, testChainKey, string(key))
	}
}

// L-10: the configured chain key file is outside what the file tools
// may read.
func TestBuildFSGuard_DeniesConfiguredChainKey(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "audit-chain.key")
	require.NoError(t, os.WriteFile(keyPath, []byte(testChainKey), 0o600))
	g, err := buildFSGuard(config.FSConfig{}, config.AuditEgressConfig{ChainHMACKeyFile: keyPath})
	require.NoError(t, err)
	_, err = g.Resolve(keyPath)
	assert.Error(t, err, "the file tools must not read the chain key")
}

// L-10: the evidence fingerprint covers the key exactly as the daemon
// uses it (whitespace trimmed), so a trailing newline does not change
// it.
func TestChainKeyEvidence_FingerprintsTrimmedKey(t *testing.T) {
	dir := t.TempDir()
	withNL := filepath.Join(dir, "nl.key")
	require.NoError(t, os.WriteFile(withNL, []byte(testChainKey+"\n"), 0o600))
	bare := filepath.Join(dir, "bare.key")
	require.NoError(t, os.WriteFile(bare, []byte(testChainKey), 0o600))

	sum := sha256.Sum256([]byte(testChainKey))
	want := hex.EncodeToString(sum[:8])
	_, fp := chainKeyEvidence(withNL)
	assert.Equal(t, want, fp)
	_, fp = chainKeyEvidence(bare)
	assert.Equal(t, want, fp)
}

func TestChainKeyEvidence_UnusableKeyHasNoFingerprint(t *testing.T) {
	p := filepath.Join(t.TempDir(), "loose.key")
	require.NoError(t, os.WriteFile(p, []byte(testChainKey), 0o600))
	require.NoError(t, os.Chmod(p, 0o644))
	src, fp := chainKeyEvidence(p)
	assert.Empty(t, fp)
	assert.Contains(t, src, "not usable")
}
