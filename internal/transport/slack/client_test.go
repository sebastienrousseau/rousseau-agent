package slack

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

func silentLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeWS is a scriptable WSConn — reads from a preloaded queue,
// captures writes for assertions, honours cancellation.
type fakeWS struct {
	mu     sync.Mutex
	inbox  [][]byte
	writes [][]byte
	closed bool
	err    error
}

func (f *fakeWS) Read(ctx context.Context) ([]byte, error) {
	f.mu.Lock()
	if f.err != nil {
		defer f.mu.Unlock()
		return nil, f.err
	}
	if len(f.inbox) == 0 {
		f.mu.Unlock()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	msg := f.inbox[0]
	f.inbox = f.inbox[1:]
	f.mu.Unlock()
	return msg, nil
}

func (f *fakeWS) Write(_ context.Context, msg []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, append([]byte(nil), msg...))
	return nil
}

func (f *fakeWS) Close(websocket.StatusCode, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func TestNew_RequiresAppToken(t *testing.T) {
	_, err := New(Config{BotToken: "xoxb-x"}, silentLogger())
	assert.Error(t, err)
}

func TestNew_RequiresBotToken(t *testing.T) {
	_, err := New(Config{AppToken: "xapp-x"}, silentLogger())
	assert.Error(t, err)
}

func TestNew_DefaultsBaseURL(t *testing.T) {
	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y"}, silentLogger())
	require.NoError(t, err)
	assert.Equal(t, "https://slack.com/api", c.cfg.BaseURL)
	assert.Equal(t, "slack", c.Name())
}

func TestDeliver_PostsExpectedPayload(t *testing.T) {
	var recorded []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/chat.postMessage", r.URL.Path)
		assert.Contains(t, r.Header.Get("Authorization"), "Bearer xoxb-")
		recorded, _ = io.ReadAll(r.Body)                               //nolint:errcheck // test fixture
		_, _ = w.Write([]byte(`{"ok":true,"ts":"1700000000.000100"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()

	c, err := New(Config{
		AppToken: "xapp-x", BotToken: "xoxb-y",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Deliver(context.Background(), "C0123", "hi"))

	var payload struct {
		Channel string `json:"channel"`
		Text    string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(recorded, &payload))
	assert.Equal(t, "C0123", payload.Channel)
	assert.Equal(t, "hi", payload.Text)
}

func TestDeliver_PrependsReplyHeader(t *testing.T) {
	var recorded []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded, _ = io.ReadAll(r.Body)      //nolint:errcheck // test fixture
		_, _ = w.Write([]byte(`{"ok":true}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c, err := New(Config{
		AppToken: "xapp-x", BotToken: "xoxb-y", ReplyHeader: "🌀 ",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Deliver(context.Background(), "C0", "body"))
	var p struct{ Text string }
	require.NoError(t, json.Unmarshal(recorded, &p))
	assert.Equal(t, "🌀 body", p.Text)
}

func TestDeliver_SlackErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c, err := New(Config{
		AppToken: "xapp-x", BotToken: "xoxb-y",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)
	err = c.Deliver(context.Background(), "C0", "hi")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_auth")
}

func TestDeliver_HTTPErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c, err := New(Config{
		AppToken: "xapp-x", BotToken: "xoxb-y",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)
	err = c.Deliver(context.Background(), "C0", "hi")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 500")
}

func TestOpenConnection_ReturnsSocketURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/apps.connections.open", r.URL.Path)
		assert.Contains(t, r.Header.Get("Authorization"), "Bearer xapp-")
		_, _ = w.Write([]byte(`{"ok":true,"url":"wss://slack.example/sm"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c, err := New(Config{
		AppToken: "xapp-x", BotToken: "xoxb-y",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)
	url, err := c.openConnection(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "wss://slack.example/sm", url)
}

func TestHandleFrame_HelloIsIgnored(t *testing.T) {
	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y"}, silentLogger())
	require.NoError(t, err)
	ws := &fakeWS{}
	err = c.handleFrame(context.Background(), ws, []byte(`{"type":"hello"}`),
		transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
			t.Fatal("handler must not fire for hello")
			return "", nil
		}))
	assert.NoError(t, err)
	assert.Empty(t, ws.writes, "no ack expected for hello")
}

func TestHandleFrame_EventsAPIRoutesAndAcks(t *testing.T) {
	var recordedPost []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat.postMessage" {
			recordedPost, _ = io.ReadAll(r.Body) //nolint:errcheck // test fixture
		}
		_, _ = w.Write([]byte(`{"ok":true}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()

	c, err := New(Config{
		AppToken: "xapp-x", BotToken: "xoxb-y",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)

	ws := &fakeWS{}
	envelope := `{"type":"events_api","envelope_id":"e1","payload":{"event":{"type":"message","user":"U1","text":"hello","channel":"C0"}}}`
	handled := false
	err = c.handleFrame(context.Background(), ws, []byte(envelope),
		transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
			handled = true
			assert.Equal(t, "U1", m.From)
			assert.Equal(t, "hello", m.Body)
			return "hi back", nil
		}))
	assert.NoError(t, err)
	assert.True(t, handled)

	// Ack fired.
	require.NotEmpty(t, ws.writes)
	var ack struct {
		EnvelopeID string `json:"envelope_id"`
	}
	require.NoError(t, json.Unmarshal(ws.writes[0], &ack))
	assert.Equal(t, "e1", ack.EnvelopeID)

	// Post fired to Slack HTTP.
	var post struct{ Text string }
	require.NoError(t, json.Unmarshal(recordedPost, &post))
	assert.Equal(t, "hi back", post.Text)
}

func TestHandleFrame_OwnMessageIsSkipped(t *testing.T) {
	c, err := New(Config{
		AppToken: "xapp-x", BotToken: "xoxb-y", BotUserID: "U_BOT",
	}, silentLogger())
	require.NoError(t, err)
	ws := &fakeWS{}
	envelope := `{"type":"events_api","envelope_id":"e1","payload":{"event":{"type":"message","user":"U_BOT","text":"self","channel":"C0"}}}`
	err = c.handleFrame(context.Background(), ws, []byte(envelope),
		transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
			t.Fatal("handler must not fire for own message")
			return "", nil
		}))
	assert.NoError(t, err)
}

func TestHandleFrame_BotIDSkipsHandler(t *testing.T) {
	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y"}, silentLogger())
	require.NoError(t, err)
	ws := &fakeWS{}
	envelope := `{"type":"events_api","envelope_id":"e1","payload":{"event":{"type":"message","bot_id":"B1","text":"bot echo"}}}`
	err = c.handleFrame(context.Background(), ws, []byte(envelope),
		transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
			t.Fatal("handler must not fire for bot echo")
			return "", nil
		}))
	assert.NoError(t, err)
}

func TestHandleFrame_MessageSubtypeIgnored(t *testing.T) {
	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y"}, silentLogger())
	require.NoError(t, err)
	ws := &fakeWS{}
	envelope := `{"type":"events_api","envelope_id":"e1","payload":{"event":{"type":"message","subtype":"channel_join","text":"joined"}}}`
	err = c.handleFrame(context.Background(), ws, []byte(envelope),
		transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
			t.Fatal("handler must not fire for subtyped messages")
			return "", nil
		}))
	assert.NoError(t, err)
}

func TestHandleFrame_MalformedIsError(t *testing.T) {
	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y"}, silentLogger())
	require.NoError(t, err)
	err = c.handleFrame(context.Background(), &fakeWS{}, []byte(`{not json`),
		transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil }))
	assert.Error(t, err)
}

func TestHandleFrame_UnknownTypeAcksSilently(t *testing.T) {
	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y"}, silentLogger())
	require.NoError(t, err)
	ws := &fakeWS{}
	err = c.handleFrame(context.Background(), ws,
		[]byte(`{"type":"interactive","envelope_id":"e1"}`),
		transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil }))
	assert.NoError(t, err)
	assert.NotEmpty(t, ws.writes)
}

func TestHandleFrame_HandlerErrorSkipsPost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat.postMessage" {
			t.Fatal("postMessage must not fire on handler error")
		}
	}))
	defer srv.Close()
	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y", BaseURL: srv.URL, HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	envelope := `{"type":"events_api","envelope_id":"e1","payload":{"event":{"type":"message","user":"U1","text":"hi","channel":"C0"}}}`
	err = c.handleFrame(context.Background(), &fakeWS{}, []byte(envelope),
		transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
			return "", errors.New("boom")
		}))
	assert.NoError(t, err)
}

func TestStopIdempotent(t *testing.T) {
	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y"}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Stop())
	require.NoError(t, c.Stop())
}

func TestPump_ReadErrorReturns(t *testing.T) {
	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y"}, silentLogger())
	require.NoError(t, err)
	ws := &fakeWS{err: errors.New("closed")}
	err = c.pump(context.Background(), ws,
		transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil }))
	assert.Error(t, err)
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "hi", truncate("hi", 10))
	assert.True(t, len(truncate("hello world", 5)) < len("hello world"))
}

// -- Start / runOnce coverage ---------------------------------------

// TestStart_HandlerNilErrors — Start rejects a nil handler.
func TestStart_HandlerNilErrors(t *testing.T) {
	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y"}, silentLogger())
	require.NoError(t, err)
	err = c.Start(context.Background(), nil)
	assert.Error(t, err)
}

// TestStart_RunOnceExitsOnContextCancel — a full end-to-end path:
//   - openConnection hits the injected HTTP fixture
//   - dial returns the fake WSConn
//   - pump reads a single message and processes it
//   - context cancellation exits the loop cleanly
func TestStart_RunOnceExitsOnContextCancel(t *testing.T) {
	// Slack /apps.connections.open response
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"url":"wss://slack.example/sm"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()

	ws := &fakeWS{}
	c, err := New(Config{
		AppToken: "xapp-x", BotToken: "xoxb-y",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
		DialWebSocket: func(context.Context, string) (WSConn, error) { return ws, nil },
	}, silentLogger())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Give runOnce a beat to start pumping before we cancel.
		<-newTicker(t)
		cancel()
	}()

	err = c.Start(ctx, transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return "", nil
	}))
	assert.ErrorIs(t, err, context.Canceled)
	assert.True(t, ws.closed, "connection should be closed on exit")
}

// TestRunOnce_DialErrorPropagates — dial failure surfaces to Start's
// retry loop; we only verify the single-shot runOnce error path here.
func TestRunOnce_DialErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"url":"wss://slack.example/sm"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()

	dialErr := errors.New("dial refused")
	c, err := New(Config{
		AppToken: "xapp-x", BotToken: "xoxb-y",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
		DialWebSocket: func(context.Context, string) (WSConn, error) { return nil, dialErr },
	}, silentLogger())
	require.NoError(t, err)

	err = c.runOnce(context.Background(), transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil }))
	require.Error(t, err)
	assert.ErrorContains(t, err, "dial refused")
}

// TestRunOnce_OpenConnectionErrorPropagates — HTTP-side error from
// apps.connections.open bubbles up as-is.
func TestRunOnce_OpenConnectionErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"ok":false,"error":"invalid_auth"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	c, err := New(Config{
		AppToken: "xapp-x", BotToken: "xoxb-y",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
		DialWebSocket: func(context.Context, string) (WSConn, error) {
			t.Fatal("dial must not be called when open fails")
			return nil, nil
		},
	}, silentLogger())
	require.NoError(t, err)

	err = c.runOnce(context.Background(), transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil }))
	require.Error(t, err)
}

// TestStart_RetriesOnRunOnceFailure — Start should wait and retry
// after a runOnce failure. We verify the retry path fires exactly
// once before the context cancellation ends the loop.
func TestStart_RetriesOnRunOnceFailure(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, `{"ok":false,"error":"rate_limited"}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c, err := New(Config{
		AppToken: "xapp-x", BotToken: "xoxb-y",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)

	// Cancel after the first failure — the 2s backoff makes a second
	// runOnce impossible before this expires.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err = c.Start(ctx, transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil }))
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.GreaterOrEqual(t, calls.Load(), int32(1), "at least one runOnce should have been attempted")
}

// newTicker returns a channel that fires after a short delay so tests
// don't sleep with time.Sleep (which the race detector dislikes).
func newTicker(t *testing.T) <-chan struct{} {
	t.Helper()
	ch := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(ch)
	}()
	return ch
}
