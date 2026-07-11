package cli

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRoot_HasSubcommands(t *testing.T) {
	root := NewRoot(&Options{})
	names := map[string]bool{}
	for _, c := range root.Commands() {
		names[c.Name()] = true
	}
	assert.True(t, names["chat"])
	assert.True(t, names["version"])
}

func TestVersionCommand_WritesToStdout(t *testing.T) {
	cmd := newVersionCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	require.NoError(t, cmd.RunE(cmd, nil))
	assert.Contains(t, buf.String(), "rousseau")
}

func TestNewLogger_Levels(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error", "unknown"} {
		lg := newLogger(level, "text", &bytes.Buffer{})
		assert.NotNil(t, lg)
	}
}

func TestNewLogger_JSONFormat(t *testing.T) {
	buf := &bytes.Buffer{}
	lg := newLogger("info", "json", buf)
	lg.Info("hello", slog.String("k", "v"))
	assert.True(t, strings.HasPrefix(buf.String(), "{"))
}

func TestExecute_RunsRootWithHelp(t *testing.T) {
	// Execute with no args prints help and returns 0.
	rc := runWithArgs(t, []string{"--help"})
	assert.Equal(t, 0, rc)
}

func runWithArgs(t *testing.T, args []string) int {
	t.Helper()
	opts := &Options{}
	root := NewRoot(opts)
	root.SetArgs(args)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	err := root.ExecuteContext(context.Background())
	if err != nil {
		return 1
	}
	return 0
}
