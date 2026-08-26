package slack

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

func noopHandler() transport.Handler {
	return transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return "", nil
	})
}

func newTestClient(t *testing.T, cfg Config) *Client {
	t.Helper()
	if cfg.AppToken == "" {
		cfg.AppToken = "xapp-1"
	}
	if cfg.BotToken == "" {
		cfg.BotToken = "xoxb-1"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 2 * time.Second}
	}
	c, err := New(cfg, silentLogger())
	require.NoError(t, err)
	return c
}

func TestNew_NilLoggerFallsBackToDefault(t *testing.T) {
	c, err := New(Config{AppToken: "xapp-1", BotToken: "xoxb-1"}, nil)
	require.NoError(t, err)
	assert.NotNil(t, c.logger)
	assert.Equal(t, "slack", c.Name())
}

func TestStart_ReturnsImmediatelyWhenStopped(t *testing.T) {
	var dials atomic.Int32
	c := newTestClient(t, Config{
		DialWebSocket: func(context.Context, string) (WSConn, error) {
			dials.Add(1)
			return nil, errors.New("should not dial")
		},
	})
	require.NoError(t, c.Stop())
	assert.NoError(t, c.Start(context.Background(), noopHandler()))
	assert.Zero(t, dials.Load())
}

// TestStart_BackoffElapsesAndReconnects exercises the 2s backoff arm of
// the retry select rather than the ctx.Done arm.
func TestStart_BackoffElapsesAndReconnects(t *testing.T) {
	var opens atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		opens.Add(1)
		_, _ = w.Write([]byte(`{"ok":false,"error":"ratelimited"}`)) //nolint:errcheck // fixture
	}))
	defer srv.Close()

	c := newTestClient(t, Config{BaseURL: srv.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	err := c.Start(ctx, noopHandler())
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.GreaterOrEqual(t, opens.Load(), int32(2), "backoff should elapse and reconnect")
}

func TestStop_ClosesActiveConnection(t *testing.T) {
	c := newTestClient(t, Config{})
	ws := &fakeWS{}
	c.mu.Lock()
	c.conn = ws
	c.mu.Unlock()

	require.NoError(t, c.Stop())
	assert.True(t, ws.closed)
	c.mu.Lock()
	assert.Nil(t, c.conn)
	c.mu.Unlock()
}

func TestOpenConnection_NotOKSurfacesSlackError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`)) //nolint:errcheck // fixture
	}))
	defer srv.Close()

	c := newTestClient(t, Config{BaseURL: srv.URL})
	_, err := c.openConnection(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "invalid_auth")
}

// TestPump_AcksFramesAndSurvivesMalformedOnes drives the loop body: a
// good envelope is acked, a malformed one is logged but does not abort
// the pump, and the read error finally ends it.
func TestPump_AcksFramesAndSurvivesMalformedOnes(t *testing.T) {
	ws := &fakeWS{inbox: [][]byte{
		[]byte(`{"type":"hello"}`),
		[]byte(`{"type":"events_api","envelope_id":"env-1","payload":{"event":{"type":"message","subtype":"channel_join"}}}`),
		[]byte(`{{{ not json`),
	}}
	c := newTestClient(t, Config{})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	assert.Error(t, c.pump(ctx, ws, noopHandler()))

	ws.mu.Lock()
	defer ws.mu.Unlock()
	require.Len(t, ws.writes, 1, "only the events_api envelope carries an envelope_id")
	var ack ackEnvelope
	require.NoError(t, json.Unmarshal(ws.writes[0], &ack))
	assert.Equal(t, "env-1", ack.EnvelopeID)
}

func TestDispatchEvent_MalformedPayload(t *testing.T) {
	tests := []struct {
		name  string
		frame string
	}{
		{"payload is a string", `{"type":"events_api","payload":"nope"}`},
		{"event is an array", `{"type":"events_api","payload":{"event":[1]}}`},
	}
	c := newTestClient(t, Config{})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := c.handleFrame(context.Background(), &fakeWS{}, []byte(tc.frame), noopHandler())
			require.Error(t, err)
			assert.ErrorContains(t, err, "parse payload")
		})
	}
}

func TestPost_MarshalFailureSurfaces(t *testing.T) {
	c := newTestClient(t, Config{BaseURL: "http://127.0.0.1:1"})
	// A channel value cannot be encoded as JSON.
	err := c.post(context.Background(), "chat.postMessage", "xoxb-1", make(chan int), nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, "marshal chat.postMessage")
}

func TestPost_TransportErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		wantErr string
	}{
		{"unbuildable request", "http://exa\x7fmple.invalid", "build chat.postMessage"},
		{"connection refused", "http://127.0.0.1:1", "slack: chat.postMessage"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, Config{BaseURL: tc.baseURL})
			err := c.Deliver(context.Background(), "C1", "hi")
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// TestPost_BodyReadFailure hijacks the connection and announces more
// bytes than it sends, so io.ReadAll on the response body fails with an
// unexpected EOF.
func TestPost_BodyReadFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }() //nolint:errcheck // fixture
		writeShortBody(buf)
	}))
	defer srv.Close()

	c := newTestClient(t, Config{BaseURL: srv.URL})
	err := c.Deliver(context.Background(), "C1", "hi")
	require.Error(t, err)
	assert.ErrorContains(t, err, "read")
}

func writeShortBody(buf *bufio.ReadWriter) {
	_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 4096\r\n\r\nshort") //nolint:errcheck // fixture
	_ = buf.Flush()                                                                //nolint:errcheck // fixture
}

// TestPost_NilResultSkipsDecode proves the helper tolerates callers that
// do not care about the response body.
func TestPost_NilResultSkipsDecode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`this is not json`)) //nolint:errcheck // fixture
	}))
	defer srv.Close()

	c := newTestClient(t, Config{BaseURL: srv.URL})
	assert.NoError(t, c.post(context.Background(), "chat.postMessage", "xoxb-1", nil, nil))
}

// TestDefaultDial_HandshakeRejectedClosesBody points the real dialer at
// a server that refuses the upgrade, so websocket.Dial returns both a
// response (whose body must be closed) and an error.
func TestDefaultDial_HandshakeRejectedClosesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len("nope")))
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("nope")) //nolint:errcheck // fixture
	}))
	defer srv.Close()

	conn, err := defaultDial(context.Background(), "ws"+srv.URL[len("http"):])
	require.Error(t, err)
	assert.Nil(t, conn)
}
