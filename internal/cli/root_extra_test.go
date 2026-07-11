package cli

import (
	"bytes"
	"context"
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
