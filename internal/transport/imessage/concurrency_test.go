package imessage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// TestStart_SlowTurnDoesNotBlockOtherSenders: see the telegram twin.
func TestStart_SlowTurnDoesNotBlockOtherSenders(t *testing.T) {
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/message/text") {
			_, _ = w.Write([]byte(`{}`)) //nolint:errcheck // test fixture
			return
		}
		if fetches.Add(1) == 1 { // primeCursor
			_, _ = w.Write([]byte(`{"data":[{"guid":"g0","text":"old","dateCreated":0,"handle":{"address":"+9"},"chats":[{"guid":"c0"}]}]}`)) //nolint:errcheck // test fixture
			return
		}
		//nolint:errcheck // test fixture
		_, _ = w.Write([]byte(`{"data":[
			{"guid":"g2","text":"fast","dateCreated":2,"handle":{"address":"+2"},"chats":[{"guid":"c2"}]},
			{"guid":"g1","text":"slow","dateCreated":1,"handle":{"address":"+1"},"chats":[{"guid":"c1"}]},
			{"guid":"g0","text":"old","dateCreated":0,"handle":{"address":"+9"},"chats":[{"guid":"c0"}]}
		]}`))
	}))
	defer srv.Close()
	c := newTestClient(t, Config{BaseURL: srv.URL, HTTPClient: srv.Client(), PollInterval: 10 * time.Millisecond})

	fastDone := make(chan struct{})
	var sawFastWhileSlowRan, sawOld atomic.Bool
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		switch m.Body {
		case "old":
			sawOld.Store(true)
		case "fast":
			close(fastDone) // a second call would panic: the cursor must hold
		case "slow":
			select {
			case <-fastDone:
				sawFastWhileSlowRan.Store(true)
			case <-time.After(2 * time.Second):
			}
		}
		return "", nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx, handler) }()
	assert.Eventually(t, sawFastWhileSlowRan.Load, 3*time.Second, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond) // several more polls of the same page
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after cancel")
	}
	assert.False(t, sawOld.Load(), "the primed message is never handled")
}
