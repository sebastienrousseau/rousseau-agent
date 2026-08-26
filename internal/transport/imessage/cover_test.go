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
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

func newTestClient(t *testing.T, cfg Config) *Client {
	t.Helper()
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://127.0.0.1:1"
	}
	if cfg.Password == "" {
		cfg.Password = "pw"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 2 * time.Second}
	}
	c, err := New(cfg, silentLogger())
	require.NoError(t, err)
	return c
}

func TestNew_NilLoggerFallsBackToDefault(t *testing.T) {
	c, err := New(Config{BaseURL: "http://x", Password: "p"}, nil)
	require.NoError(t, err)
	assert.NotNil(t, c.logger)
}

// TestStart_ReturnsWhenStoppedMidLoop proves Stop() ends the poll loop
// at the top of the next iteration, without a context cancellation.
func TestStart_ReturnsWhenStoppedMidLoop(t *testing.T) {
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		polls.Add(1)
		_, _ = w.Write([]byte(`{"data":[]}`)) //nolint:errcheck // test setup
	}))
	defer srv.Close()

	c := newTestClient(t, Config{
		BaseURL:      srv.URL,
		HTTPClient:   srv.Client(),
		PollInterval: 5 * time.Millisecond,
	})

	done := make(chan error, 1)
	go func() {
		done <- c.Start(context.Background(), transport.HandlerFunc(
			func(context.Context, transport.IncomingMessage) (string, error) { return "", nil }))
	}()

	require.Eventually(t, func() bool { return polls.Load() >= 2 }, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, c.Stop())

	select {
	case err := <-done:
		assert.NoError(t, err, "Stop() is a clean shutdown, not a cancellation")
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after Stop")
	}
}

func TestDeliver_ErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		wantErr string
	}{
		{"unbuildable request", "http://exa\x7fmple.invalid", "imessage: build"},
		{"connection refused", "http://127.0.0.1:1", "imessage: post"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, Config{BaseURL: tc.baseURL})
			err := c.Deliver(context.Background(), "chat;-;+1", "hi")
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// TestPollOnce_SendFailureIsLoggedAndLoopContinues proves a failed reply
// does not abort the remaining messages or the cursor advance.
func TestPollOnce_SendFailureIsLoggedAndLoopContinues(t *testing.T) {
	var sends atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/message/text") {
			sends.Add(1)
			http.Error(w, "applescript blew up", http.StatusInternalServerError)
			return
		}
		//nolint:errcheck // test setup
		_, _ = w.Write([]byte(`{"data":[
			{"guid":"g2","text":"second","dateCreated":2,"handle":{"address":"+1"},"chats":[{"guid":"c1"}]},
			{"guid":"g1","text":"first","dateCreated":1,"handle":{"address":"+1"},"chats":[{"guid":"c1"}]}
		]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, Config{BaseURL: srv.URL, HTTPClient: srv.Client()})
	var seen []string
	require.NoError(t, c.pollOnce(context.Background(), transport.HandlerFunc(
		func(_ context.Context, m transport.IncomingMessage) (string, error) {
			seen = append(seen, m.Body)
			return "reply", nil
		})))

	assert.Equal(t, []string{"first", "second"}, seen, "handler sees oldest-first")
	assert.Equal(t, int32(2), sends.Load(), "both replies are attempted despite failures")
	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Equal(t, "g2", c.lastID, "cursor still advances when sends fail")
}

func TestFetchMessages_ErrorPaths(t *testing.T) {
	truncated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()                                            //nolint:errcheck // test cleanup
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 8192\r\n\r\nshort") //nolint:errcheck // test writer
		_ = buf.Flush()                                                                //nolint:errcheck // test writer
	}))
	defer truncated.Close()

	badJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data": "not-a-list"}`)) //nolint:errcheck // test setup
	}))
	defer badJSON.Close()

	tests := []struct {
		name    string
		baseURL string
		client  *http.Client
		wantErr string
	}{
		{"unbuildable request", "http://exa\x7fmple.invalid", nil, "imessage: build"},
		{"connection refused", "http://127.0.0.1:1", nil, "imessage: get"},
		{"truncated body", truncated.URL, truncated.Client(), "imessage: read"},
		{"malformed json", badJSON.URL, badJSON.Client(), "imessage: parse"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, Config{BaseURL: tc.baseURL, HTTPClient: tc.client})
			_, err := c.fetchMessages(context.Background(), 5)
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}
