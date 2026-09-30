//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRestrictUmask pins that files the daemon creates (session store,
// WhatsApp device keys and message secrets, WAL files) are private to
// the user rather than world-readable.
func TestRestrictUmask(t *testing.T) {
	restore := restrictUmask()
	defer restore()
	dir := t.TempDir()
	f := filepath.Join(dir, "state.db")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0o666))
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.Mkdir(sub, 0o777))

	fi, err := os.Stat(f)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	di, err := os.Stat(sub)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), di.Mode().Perm())
}
