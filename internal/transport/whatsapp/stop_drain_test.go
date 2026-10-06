package whatsapp

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// Stop must wait for work handed to dispatch, matching the other
// transports: a turn mid-reply at SIGTERM finishes instead of being
// cut off. Before this dispatch used a bare `go f()` and Stop
// returned immediately.
func TestStop_DrainsInflightDispatch(t *testing.T) {
	logs := &logBuffer{}
	c := newClientWithLog(t, logs.newLogger(), &fakeSender{})

	// Mirror what Start installs, without a whatsmeow connection.
	c.mu.Lock()
	c.inflight = &transport.Inflight{}
	c.dispatch = c.inflight.Go
	dispatch := c.dispatch
	c.mu.Unlock()

	release := make(chan struct{})
	var finished atomic.Bool
	dispatch(func() {
		<-release
		finished.Store(true)
	})

	stopped := make(chan struct{})
	go func() {
		require.NoError(t, c.Stop())
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatal("Stop returned while dispatched work was still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return after in-flight work finished")
	}
	assert.True(t, finished.Load())
	assert.NoError(t, c.Stop(), "idempotent")
}
