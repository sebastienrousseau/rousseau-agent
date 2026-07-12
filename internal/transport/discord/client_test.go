package discord

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

func silentLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fakeWS struct {
	mu     sync.Mutex
	inbox  [][]byte
	writes [][]byte
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

func (f *fakeWS) Close(websocket.StatusCode, string) error { return nil }

func TestNew_RequiresToken(t *testing.T) {
	_, err := New(Config{}, silentLogger())
	assert.Error(t, err)
}

func TestNew_DefaultsGatewayAndBase(t *testing.T) {
	c, err := New(Config{Token: "t"}, silentLogger())
	require.NoError(t, err)
	assert.Contains(t, c.cfg.GatewayURL, "wss://")
	assert.Contains(t, c.cfg.BaseURL, "discord.com/api")
	assert.Equal(t, "discord", c.Name())
}

func TestDeliver_PostsExpectedPayload(t *testing.T) {
	var recorded []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/channels/C0/messages")
		assert.Contains(t, r.Header.Get("Authorization"), "Bot ")
		recorded, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(Config{Token: "t", BaseURL: srv.URL, HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Deliver(context.Background(), "C0", "hi"))

	var p struct{ Content string }
	require.NoError(t, json.Unmarshal(recorded, &p))
	assert.Equal(t, "hi", p.Content)
}

func TestDeliver_PrependsReplyHeader(t *testing.T) {
	var recorded []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c, err := New(Config{Token: "t", ReplyHeader: "🎮 ", BaseURL: srv.URL, HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Deliver(context.Background(), "C0", "body"))
	var p struct{ Content string }
	require.NoError(t, json.Unmarshal(recorded, &p))
	assert.Equal(t, "🎮 body", p.Content)
}

func TestDeliver_HTTPErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"code":50007,"message":"cannot send messages"}`, http.StatusForbidden)
	}))
	defer srv.Close()
	c, err := New(Config{Token: "t", BaseURL: srv.URL, HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	err = c.Deliver(context.Background(), "C0", "hi")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 403")
}

func TestHandleFrame_HelloSendsIdentify(t *testing.T) {
	c, err := New(Config{Token: "t"}, silentLogger())
	require.NoError(t, err)
	ws := &fakeWS{}
	err = c.handleFrame(context.Background(), ws,
		[]byte(`{"op":10,"d":{"heartbeat_interval":41250}}`),
		transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil }))
	assert.NoError(t, err)
	require.Len(t, ws.writes, 1)
	var identify struct {
		Op int `json:"op"`
		D  struct {
			Token   string `json:"token"`
			Intents int    `json:"intents"`
		} `json:"d"`
	}
	require.NoError(t, json.Unmarshal(ws.writes[0], &identify))
	assert.Equal(t, 2, identify.Op)
	assert.Equal(t, "t", identify.D.Token)
	assert.NotZero(t, identify.D.Intents)
}

func TestHandleFrame_ReadyRecordsSelfID(t *testing.T) {
	c, err := New(Config{Token: "t"}, silentLogger())
	require.NoError(t, err)
	err = c.handleFrame(context.Background(), &fakeWS{},
		[]byte(`{"op":0,"t":"READY","d":{"user":{"id":"U_BOT"}}}`),
		transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil }))
	require.NoError(t, err)
	assert.Equal(t, "U_BOT", c.selfID)
}

func TestHandleFrame_MessageCreateRoutesAndPosts(t *testing.T) {
	var recorded []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/channels/C1/messages" {
			recorded, _ = io.ReadAll(r.Body)
		}
	}))
	defer srv.Close()

	c, err := New(Config{Token: "t", BaseURL: srv.URL, HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)

	handled := false
	err = c.handleFrame(context.Background(), &fakeWS{},
		[]byte(`{"op":0,"t":"MESSAGE_CREATE","d":{"channel_id":"C1","content":"hi","author":{"id":"U1"}}}`),
		transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
			handled = true
			assert.Equal(t, "U1", m.From)
			assert.Equal(t, "hi", m.Body)
			return "hello", nil
		}))
	assert.NoError(t, err)
	assert.True(t, handled)
	var post struct{ Content string }
	require.NoError(t, json.Unmarshal(recorded, &post))
	assert.Equal(t, "hello", post.Content)
}

func TestHandleFrame_BotMessagesAreSkipped(t *testing.T) {
	c, err := New(Config{Token: "t"}, silentLogger())
	require.NoError(t, err)
	err = c.handleFrame(context.Background(), &fakeWS{},
		[]byte(`{"op":0,"t":"MESSAGE_CREATE","d":{"channel_id":"C1","content":"echo","author":{"id":"U2","bot":true}}}`),
		transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
			t.Fatal("handler must not fire for bot author")
			return "", nil
		}))
	assert.NoError(t, err)
}

func TestHandleFrame_EmptyContentIgnored(t *testing.T) {
	c, err := New(Config{Token: "t"}, silentLogger())
	require.NoError(t, err)
	err = c.handleFrame(context.Background(), &fakeWS{},
		[]byte(`{"op":0,"t":"MESSAGE_CREATE","d":{"channel_id":"C1","content":"","author":{"id":"U1"}}}`),
		transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
			t.Fatal("handler must not fire for empty content")
			return "", nil
		}))
	assert.NoError(t, err)
}

func TestHandleFrame_HeartbeatAckNoop(t *testing.T) {
	c, err := New(Config{Token: "t"}, silentLogger())
	require.NoError(t, err)
	ws := &fakeWS{}
	err = c.handleFrame(context.Background(), ws, []byte(`{"op":11}`),
		transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil }))
	assert.NoError(t, err)
	assert.Empty(t, ws.writes)
}

func TestHandleFrame_UnknownEventDispatchIgnored(t *testing.T) {
	c, err := New(Config{Token: "t"}, silentLogger())
	require.NoError(t, err)
	err = c.handleFrame(context.Background(), &fakeWS{},
		[]byte(`{"op":0,"t":"TYPING_START","d":{"user_id":"U1"}}`),
		transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
			t.Fatal("handler must not fire for TYPING_START")
			return "", nil
		}))
	assert.NoError(t, err)
}

func TestHandleFrame_MalformedIsError(t *testing.T) {
	c, err := New(Config{Token: "t"}, silentLogger())
	require.NoError(t, err)
	err = c.handleFrame(context.Background(), &fakeWS{}, []byte(`{not json`),
		transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil }))
	assert.Error(t, err)
}

func TestHandleFrame_HandlerErrorSkipsPost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("postMessage must not fire on handler error")
	}))
	defer srv.Close()
	c, err := New(Config{Token: "t", BaseURL: srv.URL, HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	err = c.handleFrame(context.Background(), &fakeWS{},
		[]byte(`{"op":0,"t":"MESSAGE_CREATE","d":{"channel_id":"C1","content":"hi","author":{"id":"U1"}}}`),
		transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
			return "", errors.New("boom")
		}))
	assert.NoError(t, err)
}

func TestHandleFrame_SeqIsTracked(t *testing.T) {
	c, err := New(Config{Token: "t"}, silentLogger())
	require.NoError(t, err)
	err = c.handleFrame(context.Background(), &fakeWS{},
		[]byte(`{"op":11,"s":42}`),
		transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil }))
	require.NoError(t, err)
	assert.Equal(t, int64(42), c.seq)
}

func TestStopIdempotent(t *testing.T) {
	c, err := New(Config{Token: "t"}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Stop())
	require.NoError(t, c.Stop())
}

func TestPump_ReadErrorReturns(t *testing.T) {
	c, err := New(Config{Token: "t"}, silentLogger())
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
