package matrix

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// TestStart_SlowTurnDoesNotBlockOtherSenders: see the telegram twin.
// Two senders arrive in one /sync batch; the second is handled while
// the first turn is still running.
func TestStart_SlowTurnDoesNotBlockOtherSenders(t *testing.T) {
	var syncs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/sync") {
			if syncs.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"next_batch":"b1","rooms":{"join":{"!r:x":{"timeline":{"events":[` + //nolint:errcheck // test fixture
					`{"type":"m.room.message","sender":"@a:x","origin_server_ts":1,"content":{"msgtype":"m.text","body":"slow"}},` +
					`{"type":"m.room.message","sender":"@b:x","origin_server_ts":2,"content":{"msgtype":"m.text","body":"fast"}}]}}}}}`))
				return
			}
			time.Sleep(20 * time.Millisecond)
			_, _ = w.Write([]byte(`{"next_batch":"b2","rooms":{"join":{}}}`)) //nolint:errcheck // test fixture
			return
		}
		_, _ = w.Write([]byte(`{}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()

	c, err := New(Config{HomeserverURL: srv.URL, AccessToken: "t", HTTPClient: srv.Client(), PollTimeout: time.Millisecond}, nil)
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
