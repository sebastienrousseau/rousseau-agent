package matrix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

func newTestClient(t *testing.T, cfg Config) *Client {
	t.Helper()
	if cfg.HomeserverURL == "" {
		cfg.HomeserverURL = "http://127.0.0.1:1"
	}
	if cfg.AccessToken == "" {
		cfg.AccessToken = "tok"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 2 * time.Second}
	}
	c, err := New(cfg, silentLogger())
	require.NoError(t, err)
	return c
}

func nopHandler() transport.Handler {
	return transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return "", nil
	})
}

// truncatedServer answers with a Content-Length that overstates the
// bytes actually sent, so the client's io.ReadAll fails.
func truncatedServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()                                            //nolint:errcheck // fixture
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 8192\r\n\r\nshort") //nolint:errcheck // fixture
		_ = buf.Flush()                                                                //nolint:errcheck // fixture
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNew_NilLoggerFallsBackToDefault(t *testing.T) {
	c, err := New(Config{HomeserverURL: "http://x", AccessToken: "t"}, nil)
	require.NoError(t, err)
	assert.NotNil(t, c.logger)
	assert.Equal(t, "matrix", c.Name())
}

// TestStart_ExitsWhenSyncDiesWithContext covers the "sync failed because
// the context was cancelled mid-flight" exit, which is distinct from the
// top-of-loop cancellation check.
func TestStart_ExitsWhenSyncDiesWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inFlight := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(inFlight) })
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := newTestClient(t, Config{HomeserverURL: srv.URL, HTTPClient: srv.Client()})
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx, nopHandler()) }()

	select {
	case <-inFlight:
	case <-time.After(5 * time.Second):
		t.Fatal("sync never started")
	}
	cancel()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after cancel")
	}
}

// TestStart_BackoffElapsesAndResyncs covers the 2s backoff arm of the
// retry select.
func TestStart_BackoffElapsesAndResyncs(t *testing.T) {
	var syncs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		syncs.Add(1)
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	c := newTestClient(t, Config{HomeserverURL: srv.URL, HTTPClient: srv.Client(), PollTimeout: 10 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, c.Start(ctx, nopHandler()), context.DeadlineExceeded)
	assert.GreaterOrEqual(t, syncs.Load(), int32(2), "backoff should elapse and re-sync")
}

// TestRoute_SkipsUnroutableEvents walks the per-event guards: a
// non-m.text content, an empty handler reply, and a delivery failure all
// leave the loop running for the remaining events.
func TestRoute_SkipsUnroutableEvents(t *testing.T) {
	var sends atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sends.Add(1)
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	c := newTestClient(t, Config{HomeserverURL: srv.URL, HTTPClient: srv.Client()})

	var seen []string
	resp := &syncResponse{Rooms: roomsData{Join: map[string]joinedRoom{
		"!room:example.org": {Timeline: timeline{Events: []timelineEvent{
			{Type: "m.room.message", Sender: "@a:x", Content: json.RawMessage(`{"msgtype":"m.image","body":"pic"}`)},
			{Type: "m.room.message", Sender: "@a:x", Content: json.RawMessage(`{"msgtype":"m.text","body":"quiet"}`)},
			{Type: "m.room.message", Sender: "@a:x", Content: json.RawMessage(`{"msgtype":"m.text","body":"loud"}`)},
		}}},
	}}}

	c.route(context.Background(), resp, transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen = append(seen, m.Body)
		if m.Body == "quiet" {
			return "", nil
		}
		return "reply", nil
	}))

	assert.Equal(t, []string{"quiet", "loud"}, seen, "non-text content never reaches the handler")
	assert.Equal(t, int32(1), sends.Load(), "only the non-empty reply attempts a send")
}

func TestDoGET_UnbuildableRequest(t *testing.T) {
	c := newTestClient(t, Config{})
	err := c.doGET(context.Background(), "http://exa\x7fmple.invalid/x", nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, "build request")
}

func TestDoPUT_ErrorPaths(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		body     any
		wantErr  string
	}{
		{"unencodable body", "http://127.0.0.1:1/x", make(chan int), "marshal"},
		{"unbuildable request", "http://exa\x7fmple.invalid/x", map[string]any{}, "build request"},
		{"connection refused", "http://127.0.0.1:1/x", map[string]any{}, "matrix: PUT"},
	}
	c := newTestClient(t, Config{})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := c.doPUT(context.Background(), tc.endpoint, tc.body, nil)
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestDo_ResponseErrorPaths(t *testing.T) {
	truncated := truncatedServer(t)

	badJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"next_batch":`)) //nolint:errcheck // fixture
	}))
	defer badJSON.Close()

	t.Run("truncated body", func(t *testing.T) {
		c := newTestClient(t, Config{HomeserverURL: truncated.URL, HTTPClient: truncated.Client()})
		_, err := c.sync(context.Background())
		require.Error(t, err)
		assert.ErrorContains(t, err, "matrix: read")
	})

	t.Run("malformed json", func(t *testing.T) {
		c := newTestClient(t, Config{HomeserverURL: badJSON.URL, HTTPClient: badJSON.Client()})
		_, err := c.sync(context.Background())
		require.Error(t, err)
		assert.ErrorContains(t, err, "matrix: parse")
	})

	t.Run("nil out skips decode", func(t *testing.T) {
		c := newTestClient(t, Config{HomeserverURL: badJSON.URL, HTTPClient: badJSON.Client()})
		endpoint := badJSON.URL + "/_matrix/client/v3/rooms/r/send/m.room.message/1"
		assert.NoError(t, c.doPUT(context.Background(), endpoint, map[string]any{"a": "b"}, nil))
	})

	t.Run("sync transport failure", func(t *testing.T) {
		c := newTestClient(t, Config{HomeserverURL: "http://127.0.0.1:1"})
		_, err := c.sync(context.Background())
		require.Error(t, err)
		assert.True(t, strings.Contains(err.Error(), "matrix: GET"), err.Error())
	})
}

// TestStart_RoutesSyncedEventThenStopsCleanly pins down the loop's
// happy path deterministically: one sync response is routed to the
// handler, and Stop() (called from the handler) ends the loop at the
// top of the next iteration with no error. The pre-existing
// cancel-based test races the in-flight sync and cannot guarantee it.
func TestStart_RoutesSyncedEventThenStopsCleanly(t *testing.T) {
	var syncs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if syncs.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"next_batch":"s1","rooms":{"join":{"!r:example.org":{"timeline":{"events":[` + //nolint:errcheck // fixture
				`{"type":"m.room.message","sender":"@alice:example.org","origin_server_ts":1700000000000,` +
				`"content":{"msgtype":"m.text","body":"hello there"}}]}}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"next_batch":"s2"}`)) //nolint:errcheck // fixture
	}))
	defer srv.Close()

	c := newTestClient(t, Config{HomeserverURL: srv.URL, HTTPClient: srv.Client(), PollTimeout: 10 * time.Millisecond})

	var got []string
	err := c.Start(context.Background(), transport.HandlerFunc(
		func(_ context.Context, m transport.IncomingMessage) (string, error) {
			got = append(got, m.From+": "+m.Body)
			_ = c.Stop() // end the loop on the next iteration
			return "", nil
		}))

	assert.NoError(t, err, "Stop() is a clean shutdown, not a cancellation")
	assert.Equal(t, []string{"@alice:example.org: hello there"}, got)
	assert.EqualValues(t, 1, syncs.Load(), "the loop must not sync again after Stop")
}
