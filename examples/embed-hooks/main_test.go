package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunFirstDenyWins(t *testing.T) {
	var out, errOut bytes.Buffer

	code := run(context.Background(), &out, &errOut)

	require.Equal(t, 0, code, errOut.String())
	assert.Contains(t, out.String(), `bash ls -la`)
	assert.Regexp(t, `bash ls -la\s+→ allow\s+reason=""`, out.String())
	assert.Regexp(t, `bash rm -rf /tmp/example\s+→ deny\s+reason="rm -rf blocked by policy"`, out.String())
	assert.Empty(t, errOut.String())
}

func TestRunReportsSetupFailure(t *testing.T) {
	// os.MkdirTemp resolves its default directory through $TMPDIR;
	// pointing it at a path that does not exist fails the demo before
	// any hook runs.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))
	var out, errOut bytes.Buffer

	code := run(context.Background(), &out, &errOut)

	assert.Equal(t, 1, code)
	assert.Contains(t, errOut.String(), "embed-hooks: temp dir:")
}

func TestWriteScriptIsExecutable(t *testing.T) {
	dir := t.TempDir()

	path := writeScript(dir, "allow.sh", allowScript)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())

	body, err := os.ReadFile(path) //nolint:gosec // path is a test temp file
	require.NoError(t, err)
	assert.Equal(t, allowScript, string(body))
}

func TestWriteScriptPanicsWhenDirIsMissing(t *testing.T) {
	// writeScript is a fixture helper, so it panics rather than
	// threading an error nobody can act on back to the caller.
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	assert.Panics(t, func() { writeScript(missing, "allow.sh", allowScript) })
}
