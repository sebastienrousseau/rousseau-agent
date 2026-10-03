package telegram

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

// TestStart_SlowTurnDoesNotBlockOtherSenders pins concurrent handling:
// while sender 1's turn is still running, sender 2's message (same
// poll batch) is handled. The receive loop used to call the handler
// inline, so one long turn stalled every other sender and their
// /cancel.
func TestStart_SlowTurnDoesNotBlockOtherSenders(t *testing.T) {
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			if polls.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"ok":true,"result":[` + //nolint:errcheck // test fixture
					`{"update_id":1,"message":{"message_id":1,"date":1,"text":"slow","chat":{"id":1,"type":"private"}}},` +
					`{"update_id":2,"message":{"message_id":2,"date":1,"text":"fast","chat":{"id":2,"type":"private"}}}]}`))
				return
			}
			time.Sleep(20 * time.Millisecond)
			_, _ = w.Write([]byte(`{"ok":true,"result":[]}`)) //nolint:errcheck // test fixture
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()

	c, err := New(Config{Token: "t", BaseURL: srv.URL, HTTPClient: srv.Client(), PollTimeout: time.Millisecond}, silentLogger())
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
	assert.Eventually(t, sawFastWhileSlowRan.Load, 3*time.Second, 5*time.Millisecond,
		"the fast sender must be handled while the slow turn is still running")
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after cancel")
	}
}
