package signal

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// TestStart_SlowTurnDoesNotBlockOtherSenders: see the telegram twin.
// A stand-in signal-cli emits two receive frames from two senders.
func TestStart_SlowTurnDoesNotBlockOtherSenders(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "signal-cli")
	frame := func(from, text string) string {
		return `{"jsonrpc":"2.0","method":"receive","params":{"envelope":{"sourceNumber":"` + from +
			`","timestamp":1,"dataMessage":{"message":"` + text + `"}}}}`
	}
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\ncat <<'EOF'\n"+
		frame("+15550001", "slow")+"\n"+frame("+15550002", "fast")+"\nEOF\nexec sleep 30\n"), 0o700)) //nolint:gosec // executable test fixture

	c, err := New(Config{Account: "+15559999", Binary: bin}, silentLogger())
	require.NoError(t, err)

	fastDone := make(chan struct{})
	var sawFastWhileSlowRan atomic.Bool
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		if m.Body == "fast" {
			close(fastDone)
			return "", nil
		}
		select {
		case <-fastDone:
			sawFastWhileSlowRan.Store(true)
		case <-time.After(2 * time.Second):
		}
		return "", nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx, handler) }()
	assert.Eventually(t, sawFastWhileSlowRan.Load, 3*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after cancel")
	}
}
