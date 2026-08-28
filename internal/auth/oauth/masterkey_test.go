package oauth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveMasterKey_EnvFirst(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	raw, err := GenerateKey()
	require.NoError(t, err)
	t.Setenv(EnvMasterKey, EncodeHexKey(raw))

	got, err := ResolveMasterKey(false)
	require.NoError(t, err)
	assert.Equal(t, raw, got)
	// File must not have been created.
	_, err = os.Stat(filepath.Join(dir, "rousseau", "token.key"))
	assert.True(t, os.IsNotExist(err))
}

func TestResolveMasterKey_EnvBadHexErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv(EnvMasterKey, "not-hex")
	_, err := ResolveMasterKey(false)
	assert.Error(t, err)
}

func TestResolveMasterKey_FileWhenEnvEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv(EnvMasterKey, "")

	raw, err := GenerateKey()
	require.NoError(t, err)
	path := filepath.Join(dir, "rousseau", "token.key")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(EncodeHexKey(raw)), 0o600))

	got, err := ResolveMasterKey(false)
	require.NoError(t, err)
	assert.Equal(t, raw, got)
}

func TestResolveMasterKey_ModeCheck(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv(EnvMasterKey, "")

	raw, err := GenerateKey()
	require.NoError(t, err)
	path := filepath.Join(dir, "rousseau", "token.key")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(EncodeHexKey(raw)), 0o644))

	_, err = ResolveMasterKey(false)
	assert.ErrorContains(t, err, "0600")
}

func TestResolveMasterKey_GeneratesWhenAllowed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv(EnvMasterKey, "")

	got, err := ResolveMasterKey(true)
	require.NoError(t, err)
	assert.Len(t, got, KeySize)

	// File was written with mode 0600.
	info, err := os.Stat(filepath.Join(dir, "rousseau", "token.key"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// Second call reuses the same file.
	again, err := ResolveMasterKey(false)
	require.NoError(t, err)
	assert.Equal(t, got, again)
}

func TestResolveMasterKey_MissingErrorsWhenNotAllowed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv(EnvMasterKey, "")

	_, err := ResolveMasterKey(false)
	assert.Error(t, err)
}
