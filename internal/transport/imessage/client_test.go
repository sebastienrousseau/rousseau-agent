package imessage

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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

func silentLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestNew_RequiresBaseURL(t *testing.T) {
	_, err := New(Config{Password: "p"}, silentLogger())
	assert.Error(t, err)
}

func TestNew_RequiresPassword(t *testing.T) {
	_, err := New(Config{BaseURL: "http://x"}, silentLogger())
	assert.Error(t, err)
}

func TestNew_DefaultsPageSizeAndPoll(t *testing.T) {
	c, err := New(Config{BaseURL: "http://x", Password: "p"}, silentLogger())
	require.NoError(t, err)
	assert.NotZero(t, c.cfg.PollInterval)
	assert.Equal(t, 25, c.cfg.PageSize)
	assert.Equal(t, "imessage", c.Name())
}

func TestDeliver_PostsExpectedPayload(t *testing.T) {
	var recorded []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/api/v1/message/text")
		assert.Contains(t, r.URL.RawQuery, "password=p")
		recorded, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"status":200}`))
	}))
	defer srv.Close()

	c, err := New(Config{BaseURL: srv.URL, Password: "p", HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Deliver(context.Background(), "iMessage;-;+14155551234", "hi"))

	var payload struct {
		ChatGUID string `json:"chatGuid"`
		Message  string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(recorded, &payload))
	assert.Equal(t, "iMessage;-;+14155551234", payload.ChatGUID)
	assert.Equal(t, "hi", payload.Message)
}

func TestDeliver_PrependsReplyHeader(t *testing.T) {
	var recorded []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Password: "p", ReplyHeader: "🍏 ", HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Deliver(context.Background(), "chat1", "body"))
	var p struct{ Message string }
	require.NoError(t, json.Unmarshal(recorded, &p))
	assert.Equal(t, "🍏 body", p.Message)
}

func TestDeliver_HTTPErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Password: "p", HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	err = c.Deliver(context.Background(), "chat1", "hi")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 401")
}

func TestFetchMessages_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
      {"guid":"m1","text":"hi","isFromMe":false,"dateCreated":1700000000000,"handle":{"address":"+14155551234"},"chats":[{"guid":"chat1"}]}
    ]}`))
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Password: "p", HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	msgs, err := c.fetchMessages(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "m1", msgs[0].GUID)
	assert.Equal(t, "hi", msgs[0].Text)
	assert.False(t, msgs[0].IsFromMe)
}

func TestPrimeCursor_RecordsNewestID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"guid":"newest","text":"x"}]}`))
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Password: "p", HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.primeCursor(context.Background()))
	assert.Equal(t, "newest", c.lastID)
}

func TestPrimeCursor_EmptyResponseNoop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Password: "p", HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.primeCursor(context.Background()))
	assert.Empty(t, c.lastID)
}

func TestPollOnce_RoutesFreshMessagesOldestFirst(t *testing.T) {
	// Response contains three messages, all fresh (cursor is empty).
	// BlueBubbles orders newest-first; we should forward oldest-first.
	body := `{"data":[
    {"guid":"m3","text":"third","isFromMe":false,"dateCreated":3,"handle":{"address":"+3"},"chats":[{"guid":"c"}]},
    {"guid":"m2","text":"second","isFromMe":false,"dateCreated":2,"handle":{"address":"+2"},"chats":[{"guid":"c"}]},
    {"guid":"m1","text":"first","isFromMe":false,"dateCreated":1,"handle":{"address":"+1"},"chats":[{"guid":"c"}]}
  ]}`
	var (
		posted   [][]byte
		postedMu sync.Mutex
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/message/text" {
			b, _ := io.ReadAll(r.Body)
			postedMu.Lock()
			posted = append(posted, b)
			postedMu.Unlock()
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c, err := New(Config{BaseURL: srv.URL, Password: "p", HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)

	var seen []string
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen = append(seen, m.Body)
		return "ok:" + m.Body, nil
	})
	require.NoError(t, c.pollOnce(context.Background(), handler))
	assert.Equal(t, []string{"first", "second", "third"}, seen)
	assert.Equal(t, "m3", c.lastID)

	// One post per handler reply.
	postedMu.Lock()
	assert.Len(t, posted, 3)
	postedMu.Unlock()
}

func TestPollOnce_SkipsAlreadyHandled(t *testing.T) {
	body := `{"data":[
    {"guid":"m3","text":"third","isFromMe":false,"chats":[{"guid":"c"}]},
    {"guid":"m2","text":"second","isFromMe":false,"chats":[{"guid":"c"}]},
    {"guid":"m1","text":"first","isFromMe":false,"chats":[{"guid":"c"}]}
  ]}`
	var invoked int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/message/text" {
			atomic.AddInt32(&invoked, 1)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c, err := New(Config{BaseURL: srv.URL, Password: "p", HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	c.lastID = "m2" // already saw m2 → only m3 is fresh

	var seen []string
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen = append(seen, m.Body)
		return "", nil
	})
	require.NoError(t, c.pollOnce(context.Background(), handler))
	assert.Equal(t, []string{"third"}, seen)
}

func TestPollOnce_SkipsSelfMessages(t *testing.T) {
	body := `{"data":[
    {"guid":"m1","text":"hello","isFromMe":true,"chats":[{"guid":"c"}]}
  ]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Password: "p", HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	handler := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		t.Fatal("handler must not fire for self message")
		return "", nil
	})
	require.NoError(t, c.pollOnce(context.Background(), handler))
}

func TestPollOnce_SkipsEmptyBody(t *testing.T) {
	body := `{"data":[
    {"guid":"m1","text":"","isFromMe":false,"chats":[{"guid":"c"}]}
  ]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Password: "p", HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	handler := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		t.Fatal("handler must not fire for empty body")
		return "", nil
	})
	require.NoError(t, c.pollOnce(context.Background(), handler))
}

func TestPollOnce_HandlerErrorContinues(t *testing.T) {
	body := `{"data":[{"guid":"m1","text":"hi","isFromMe":false,"chats":[{"guid":"c"}]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/message/text" {
			t.Fatal("send must not fire on handler error")
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Password: "p", HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	handler := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return "", errors.New("boom")
	})
	require.NoError(t, c.pollOnce(context.Background(), handler))
}

func TestExtractText(t *testing.T) {
	assert.Equal(t, "hi", extractText(messageRecord{Text: "hi"}))
	assert.Equal(t, "", extractText(messageRecord{}))
}

func TestStopIdempotent(t *testing.T) {
	c, err := New(Config{BaseURL: "http://x", Password: "p"}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Stop())
	require.NoError(t, c.Stop())
}

func TestStart_HandlerNilErrors(t *testing.T) {
	c, err := New(Config{BaseURL: "http://x", Password: "p"}, silentLogger())
	require.NoError(t, err)
	err = c.Start(context.Background(), nil)
	assert.Error(t, err)
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "hi", truncate("hi", 10))
	assert.True(t, len(truncate("hello world", 5)) < len("hello world"))
}
