package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

func silentLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestNew_RequiresHomeserver(t *testing.T) {
	_, err := New(Config{AccessToken: "t"}, silentLogger())
	assert.Error(t, err)
}

func TestNew_RequiresToken(t *testing.T) {
	_, err := New(Config{HomeserverURL: "http://x"}, silentLogger())
	assert.Error(t, err)
}

func TestNew_DefaultsPollTimeout(t *testing.T) {
	c, err := New(Config{HomeserverURL: "http://x", AccessToken: "t"}, silentLogger())
	require.NoError(t, err)
	assert.NotZero(t, c.cfg.PollTimeout)
	assert.Equal(t, "matrix", c.Name())
}

func TestDeliver_PostsExpectedPayload(t *testing.T) {
	var (
		recorded []byte
		mu       sync.Mutex
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		recorded = body
		mu.Unlock()
		assert.Contains(t, r.URL.Path, "/rooms/!abc")
		assert.Contains(t, r.Header.Get("Authorization"), "Bearer ")
		_, _ = w.Write([]byte(`{"event_id":"$evt"}`))
	}))
	defer srv.Close()

	c, err := New(Config{
		HomeserverURL: srv.URL, AccessToken: "t",
		HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Deliver(context.Background(), "!abc:matrix.org", "hi"))

	mu.Lock()
	defer mu.Unlock()
	var payload struct {
		MsgType string `json:"msgtype"`
		Body    string `json:"body"`
	}
	require.NoError(t, json.Unmarshal(recorded, &payload))
	assert.Equal(t, "m.text", payload.MsgType)
	assert.Equal(t, "hi", payload.Body)
}

func TestDeliver_PrependsReplyHeader(t *testing.T) {
	var recorded []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c, err := New(Config{
		HomeserverURL: srv.URL, AccessToken: "t", ReplyHeader: "🌀 ",
		HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Deliver(context.Background(), "!x:matrix.org", "body"))
	var p struct{ Body string }
	require.NoError(t, json.Unmarshal(recorded, &p))
	assert.Equal(t, "🌀 body", p.Body)
}

func TestDeliver_HTTPErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"errcode":"M_FORBIDDEN"}`, http.StatusForbidden)
	}))
	defer srv.Close()
	c, err := New(Config{HomeserverURL: srv.URL, AccessToken: "t", HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	err = c.Deliver(context.Background(), "!x:matrix.org", "hi")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 403")
}

func TestSync_TracksSinceCursor(t *testing.T) {
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		if call == 1 {
			assert.NotContains(t, r.URL.RawQuery, "since=")
			_, _ = w.Write([]byte(`{"next_batch":"s1"}`))
			return
		}
		assert.Contains(t, r.URL.RawQuery, "since=s1")
		_, _ = w.Write([]byte(`{"next_batch":"s2"}`))
	}))
	defer srv.Close()

	c, err := New(Config{HomeserverURL: srv.URL, AccessToken: "t", HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	_, err = c.sync(context.Background())
	require.NoError(t, err)
	_, err = c.sync(context.Background())
	require.NoError(t, err)
}

func TestRoute_InvokesHandlerAndReplies(t *testing.T) {
	var recorded []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			recorded, _ = io.ReadAll(r.Body)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c, err := New(Config{
		HomeserverURL: srv.URL, AccessToken: "t",
		UserID: "@bot:matrix.org", HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)

	var seen transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen = m
		return "hi back", nil
	})
	resp := &syncResponse{
		Rooms: roomsData{
			Join: map[string]joinedRoom{
				"!abc:matrix.org": {Timeline: timeline{Events: []timelineEvent{{
					Type: "m.room.message", Sender: "@user:matrix.org",
					OriginServerTS: 1_700_000_000_000,
					Content:        json.RawMessage(`{"msgtype":"m.text","body":"hello"}`),
				}}}},
			},
		},
	}
	c.route(context.Background(), resp, handler)

	assert.Equal(t, "@user:matrix.org", seen.From)
	assert.Equal(t, "hello", seen.Body)

	var payload struct{ Body string }
	require.NoError(t, json.Unmarshal(recorded, &payload))
	assert.Equal(t, "hi back", payload.Body)
}

func TestRoute_SkipsOwnMessages(t *testing.T) {
	c, err := New(Config{HomeserverURL: "http://x", AccessToken: "t", UserID: "@bot:matrix.org"}, silentLogger())
	require.NoError(t, err)
	handler := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		t.Fatal("handler must not fire for own message")
		return "", nil
	})
	resp := &syncResponse{
		Rooms: roomsData{Join: map[string]joinedRoom{
			"!abc:matrix.org": {Timeline: timeline{Events: []timelineEvent{{
				Type: "m.room.message", Sender: "@bot:matrix.org",
				Content: json.RawMessage(`{"msgtype":"m.text","body":"self"}`),
			}}}},
		}},
	}
	c.route(context.Background(), resp, handler)
}

func TestRoute_IgnoresNonMessageEvents(t *testing.T) {
	c, err := New(Config{HomeserverURL: "http://x", AccessToken: "t"}, silentLogger())
	require.NoError(t, err)
	handler := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		t.Fatal("handler must not fire for non-message events")
		return "", nil
	})
	resp := &syncResponse{
		Rooms: roomsData{Join: map[string]joinedRoom{
			"!x:matrix.org": {Timeline: timeline{Events: []timelineEvent{
				{Type: "m.room.member", Sender: "@u:matrix.org", Content: json.RawMessage(`{}`)},
			}}},
		}},
	}
	c.route(context.Background(), resp, handler)
}

func TestRoute_HandlerErrorSkipsSend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			t.Fatal("send must not be invoked on handler error")
		}
	}))
	defer srv.Close()

	c, err := New(Config{HomeserverURL: srv.URL, AccessToken: "t", HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	handler := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return "", errors.New("boom")
	})
	resp := &syncResponse{
		Rooms: roomsData{Join: map[string]joinedRoom{
			"!x:matrix.org": {Timeline: timeline{Events: []timelineEvent{
				{Type: "m.room.message", Sender: "@u:matrix.org", Content: json.RawMessage(`{"msgtype":"m.text","body":"hi"}`)},
			}}},
		}},
	}
	c.route(context.Background(), resp, handler)
}

func TestExtractBody_TextMessage(t *testing.T) {
	assert.Equal(t, "hi",
		extractBody(json.RawMessage(`{"msgtype":"m.text","body":"hi"}`)))
}

func TestExtractBody_NonTextIsEmpty(t *testing.T) {
	assert.Empty(t,
		extractBody(json.RawMessage(`{"msgtype":"m.image","url":"mxc://…"}`)))
}

func TestExtractBody_MalformedIsEmpty(t *testing.T) {
	assert.Empty(t, extractBody(json.RawMessage(`{not json`)))
}

func TestStopIdempotent(t *testing.T) {
	c, err := New(Config{HomeserverURL: "http://x", AccessToken: "t"}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Stop())
	require.NoError(t, c.Stop())
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "hi", truncate("hi", 10))
	assert.True(t, strings.HasSuffix(truncate("hello world", 5), "…"))
}
