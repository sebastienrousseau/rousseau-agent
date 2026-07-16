package cli

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// silentLogger is defined in chat_test.go.

func TestExecute_VersionRunsCleanly(t *testing.T) {
	// Ensure Execute wires through without erroring on a
	// zero-side-effect subcommand.
	opts := &Options{}
	root := NewRoot(opts)
	root.SetArgs([]string{"version"})
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(&bytes.Buffer{})
	err := root.ExecuteContext(context.Background())
	assert.NoError(t, err)
	assert.Contains(t, buf.String(), "rousseau")
}

func TestExecute_UnknownCommandErrors(t *testing.T) {
	opts := &Options{}
	root := NewRoot(opts)
	root.SetArgs([]string{"bogus-command"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	err := root.ExecuteContext(context.Background())
	assert.Error(t, err)
}

// TestExecute_HappyPath drives the top-level Execute() with argv
// pointed at the always-safe `version` subcommand. Uses os.Args
// swapping because Execute reads it directly (cobra default).
func TestExecute_HappyPath(t *testing.T) {
	prev := os.Args
	defer func() { os.Args = prev }()
	os.Args = []string{"rousseau", "version"}
	rc := Execute(context.Background())
	assert.Equal(t, 0, rc)
}

// TestExecute_ErrorPath drives Execute() with an unknown subcommand.
// It exercises the non-zero exit path and the stderr print.
func TestExecute_ErrorPath(t *testing.T) {
	prev := os.Args
	defer func() { os.Args = prev }()
	os.Args = []string{"rousseau", "unknown-command-xyz"}
	rc := Execute(context.Background())
	assert.Equal(t, 1, rc)
}

func TestNewLogger_LevelMapping(t *testing.T) {
	// Exercises every case in newLogger's level switch, plus JSON
	// vs. text handler selection.
	tests := []struct{ level, format string }{
		{"debug", "text"}, {"warn", "text"}, {"warning", "text"},
		{"error", "text"}, {"info", "text"}, {"", "json"},
	}
	for _, tc := range tests {
		var buf bytes.Buffer
		l := newLogger(tc.level, tc.format, &buf)
		assert.NotNil(t, l)
	}
}
