package main

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capture swaps os.Stdout and os.Stderr for pipes while fn runs and
// returns what was written to each. internal/cli.Execute writes to the
// process-level streams, so intercepting them is the only way to assert
// on the entry point's output.
func capture(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()

	outR, outW, err := os.Pipe()
	require.NoError(t, err)
	errR, errW, err := os.Pipe()
	require.NoError(t, err)

	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	t.Cleanup(func() { os.Stdout, os.Stderr = origOut, origErr })

	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() { b, _ := io.ReadAll(outR); outCh <- string(b) }()
	go func() { b, _ := io.ReadAll(errR); errCh <- string(b) }()

	fn()

	require.NoError(t, outW.Close())
	require.NoError(t, errW.Close())
	os.Stdout, os.Stderr = origOut, origErr

	return <-outCh, <-errCh
}

// withArgs points os.Args at the given command line for the duration of
// the test; Cobra reads the process arguments directly.
func withArgs(t *testing.T, args ...string) {
	t.Helper()
	orig := os.Args
	os.Args = append([]string{"rousseau"}, args...)
	t.Cleanup(func() { os.Args = orig })
}

func TestRunPrintsHelp(t *testing.T) {
	// Keep config discovery away from the developer's real home.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	withArgs(t)

	var code int
	stdout, stderr := capture(t, func() { code = run() })

	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "rousseau is a coding assistant")
	assert.Contains(t, stdout, "Available Commands:")
	assert.Empty(t, stderr)
}

func TestRunUnknownCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	withArgs(t, "definitely-not-a-command")

	var code int
	_, stderr := capture(t, func() { code = run() })

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "unknown command")
}
