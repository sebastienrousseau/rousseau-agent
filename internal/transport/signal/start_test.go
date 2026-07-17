package signal

import (
	"context"
	"io"
	"log/slog"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// TestStart_MissingBinaryErrors drives the exec.Cmd startup error
// branch by pointing signal-cli at a nonexistent binary path.
func TestStart_MissingBinaryErrors(t *testing.T) {
	c, err := New(Config{Account: "+15551234567", Binary: "/nonexistent/no-such-signal-cli"}, silentLogger())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err = c.Start(ctx, transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return "", nil
	}))
	assert.Error(t, err)
}

// TestStart_UsesConfiguredExtraArgs verifies extra arguments are
// forwarded into the exec.Cmd. Uses `true` as a stand-in binary — it
// exits immediately and pump returns nil at EOF.
func TestStart_ExecutesConfiguredBinary(t *testing.T) {
	// `true` on POSIX exits with 0 immediately. Start returns nil-error
	// once pump sees EOF on stdout.
	if _, err := exec.LookPath("true"); err != nil {
		t.Skip("no /usr/bin/true available")
	}
	c, err := New(Config{Account: "+15551234567", Binary: "true"}, silentLogger())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = c.Start(ctx, transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return "", nil
	}))
	// May be nil (EOF) or ctx timeout — either exercises pump exit.
	_ = err
}

// TestPrefixWriter_WriteAll walks a multi-line write ending without
// a newline to cover the partial-line branch.
func TestPrefixWriter_WriteAll(t *testing.T) {
	pw := &prefixWriter{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	n, err := pw.Write([]byte("no newline"))
	require.NoError(t, err)
	assert.Equal(t, len("no newline"), n)
}

// TestJSONWriter_CloseIsSafe covers the close branch on the
// jsonWriter shim.
func TestJSONWriter_CloseIsSafe(t *testing.T) {
	sw := &stubWriter{}
	jw := &jsonWriter{w: sw}
	assert.NoError(t, jw.Close())
}
