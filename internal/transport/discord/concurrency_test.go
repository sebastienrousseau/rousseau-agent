package discord

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

func discordMsg(id, author, text string) []byte {
	return []byte(`{"op":0,"t":"MESSAGE_CREATE","d":{"id":"` + id + `","channel_id":"C1","author":{"id":"` +
		author + `"},"content":"` + text + `"}}`)
}

// TestStart_SlowTurnDoesNotBlockOtherSenders: see the telegram twin.
func TestStart_SlowTurnDoesNotBlockOtherSenders(t *testing.T) {
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`)) //nolint:errcheck // test fixture
	}))
	defer rest.Close()
	ws := &fakeWS{inbox: [][]byte{discordMsg("M1", "U1", "slow"), discordMsg("M2", "U2", "fast")}}
	c, err := New(Config{
		Token: "bot", BaseURL: rest.URL, HTTPClient: rest.Client(),
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
}
