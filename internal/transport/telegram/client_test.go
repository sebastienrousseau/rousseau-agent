package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

func silentLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestNew_RequiresToken(t *testing.T) {
	_, err := New(Config{}, silentLogger())
	assert.Error(t, err)
}

func TestNew_DefaultsBaseURL(t *testing.T) {
	c, err := New(Config{Token: "t"}, silentLogger())
	require.NoError(t, err)
	assert.Equal(t, "https://api.telegram.org", c.cfg.BaseURL)
	assert.Equal(t, "telegram", c.Name())
}

func TestNew_KeepsExplicitBaseURL(t *testing.T) {
	c, err := New(Config{Token: "t", BaseURL: "http://local"}, silentLogger())
	require.NoError(t, err)
	assert.Equal(t, "http://local", c.cfg.BaseURL)
}

func TestDeliver_BadChatID(t *testing.T) {
	c, err := New(Config{Token: "t"}, silentLogger())
	require.NoError(t, err)
	err = c.Deliver(context.Background(), "not-a-number", "hi")
	assert.Error(t, err)
}

func TestDeliver_PostsExpectedPayload(t *testing.T) {
	var recorded []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test fixture
		recorded = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()

	c, err := New(Config{Token: "bot-token", BaseURL: srv.URL, HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Deliver(context.Background(), "42", "hello world"))

	var payload struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(recorded, &payload))
	assert.Equal(t, int64(42), payload.ChatID)
	assert.Equal(t, "hello world", payload.Text)
}

func TestDeliver_PrependsReplyHeader(t *testing.T) {
	var recorded []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded, _ = io.ReadAll(r.Body)      //nolint:errcheck // test fixture
		_, _ = w.Write([]byte(`{"ok":true}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()

	c, err := New(Config{Token: "t", BaseURL: srv.URL, ReplyHeader: "💎 ", HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Deliver(context.Background(), "1", "body"))

	var payload struct {
		Text string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(recorded, &payload))
	assert.Equal(t, "💎 body", payload.Text)
}

func TestCall_HTTPErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"ok":false,"description":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	c, err := New(Config{Token: "t", BaseURL: srv.URL, HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	err = c.Deliver(context.Background(), "1", "hi")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 401")
}

func TestStopIdempotent(t *testing.T) {
	c, err := New(Config{Token: "t"}, silentLogger())
	require.NoError(t, err)
	require.NoError(t, c.Stop())
	require.NoError(t, c.Stop())
}

func TestRoute_IgnoresEmptyMessages(t *testing.T) {
	c, err := New(Config{Token: "t"}, silentLogger())
	require.NoError(t, err)
	handler := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		t.Fatal("handler must not fire for empty message")
		return "", nil
	})
	c.route(context.Background(), telegramUpdate{}, handler)
	c.route(context.Background(), telegramUpdate{Message: &telegramMessage{Text: ""}}, handler)
}

func TestRoute_InvokesHandlerAndReplies(t *testing.T) {
	var recorded []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded, _ = io.ReadAll(r.Body)      //nolint:errcheck // test fixture
		_, _ = w.Write([]byte(`{"ok":true}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()

	c, err := New(Config{Token: "t", BaseURL: srv.URL, HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)

	var seen transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen = m
		return "hi back", nil
	})
	c.route(context.Background(), telegramUpdate{
		UpdateID: 7,
		Message: &telegramMessage{
			MessageID: 1, Date: 1_700_000_000, Text: "hello",
			Chat: telegramChat{ID: 100, Type: "private"},
		},
	}, handler)
	assert.Equal(t, "100", seen.From)
	assert.Equal(t, "hello", seen.Body)

	var payload struct {
		Text string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(recorded, &payload))
	assert.Equal(t, "hi back", payload.Text)
}

func TestRoute_HandlerErrorSkipsSend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("sendMessage should not be called on handler error")
	}))
	defer srv.Close()

	c, err := New(Config{Token: "t", BaseURL: srv.URL, HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	handler := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return "", errors.New("boom")
	})
	c.route(context.Background(), telegramUpdate{
		Message: &telegramMessage{Text: "hi", Chat: telegramChat{ID: 1, Type: "private"}, Date: 1},
	}, handler)
}

func TestGetUpdates_OffsetAdvances(t *testing.T) {
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // test fixture
		if call == 1 {
			assert.NotContains(t, string(body), `"offset"`)
			_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":42,"message":{"text":"hi","chat":{"id":1}}}]}`)) //nolint:errcheck // test fixture
			return
		}
		assert.Contains(t, string(body), `"offset":43`)
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()

	c, err := New(Config{Token: "t", BaseURL: srv.URL, HTTPClient: srv.Client(), PollTimeout: time.Millisecond}, silentLogger())
	require.NoError(t, err)
	updates, err := c.getUpdates(context.Background())
	require.NoError(t, err)
	assert.Len(t, updates, 1)
	updates, err = c.getUpdates(context.Background())
	require.NoError(t, err)
	assert.Empty(t, updates)
}

// TestCall_MalformedResponseErrors exercises the JSON-decode failure
// branch when the server sends non-JSON.
func TestCall_MalformedResponseErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()

	c, err := New(Config{Token: "t", BaseURL: srv.URL, HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	var out struct{}
	err = c.call(context.Background(), "test", map[string]any{}, &out)
	assert.Error(t, err)
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "hi", truncate("hi", 10))
	assert.True(t, len(truncate(string(bytes.Repeat([]byte{'a'}, 20)), 5)) < 20)
}
