package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

func slackFrame(id, user, text string) []byte {
	return []byte(`{"type":"events_api","envelope_id":"` + id + `","payload":{"event":{"type":"message","user":"` +
		user + `","channel":"C1","text":"` + text + `"}}}`)
}

// TestStart_SlowTurnDoesNotBlockOtherSenders: see the telegram twin.
// The second user's frame is read and handled while the first turn is
// still running; both envelopes are acked straight away.
func TestStart_SlowTurnDoesNotBlockOtherSenders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"url":"wss://slack.example/sm"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	ws := &fakeWS{inbox: [][]byte{slackFrame("e1", "U1", "slow"), slackFrame("e2", "U2", "fast")}}
	c, err := New(Config{
		AppToken: "xapp-x", BotToken: "xoxb-y", BaseURL: srv.URL, HTTPClient: srv.Client(),
		DialWebSocket: func(context.Context, string) (WSConn, error) { return ws, nil },
	}, silentLogger())
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
	ws.mu.Lock()
	defer ws.mu.Unlock()
	assert.Len(t, ws.writes, 2, "both envelopes acked")
}
