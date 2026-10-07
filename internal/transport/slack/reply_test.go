package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

type postedMessage struct {
	Channel  string `json:"channel"`
	Text     string `json:"text"`
	ThreadTS string `json:"thread_ts"`
}

func recordingClient(t *testing.T) (*Client, *[]postedMessage) {
	t.Helper()
	var posts []postedMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat.postMessage" {
			raw, _ := io.ReadAll(r.Body) //nolint:errcheck // test fixture
			var p postedMessage
			_ = json.Unmarshal(raw, &p) //nolint:errcheck // test fixture
			posts = append(posts, p)
		}
		_, _ = w.Write([]byte(`{"ok":true}`)) //nolint:errcheck // test fixture
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y", BaseURL: srv.URL, HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	return c, &posts
}

// A message inside a thread carries its ids and is answered in the
// same thread; a top-level message gets a top-level reply.
func TestDispatchEvent_ThreadIDsAndThreadedReply(t *testing.T) {
	c, posts := recordingClient(t)
	var seen transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen = m
		return "in thread", nil
	})
	payload := eventsAPIPayload{Event: slackEvent{
		Type: "message", User: "U1", Text: "q", Channel: "C0", TS: "2.000", ThreadTS: "1.000",
	}}
	require.NoError(t, c.dispatchEvent(context.Background(), payload, handler))
	assert.Equal(t, "C0", seen.Conversation)
	assert.Equal(t, "2.000", seen.MessageID)
	assert.Equal(t, "1.000", seen.Thread)
	require.Len(t, *posts, 1)
	assert.Equal(t, "1.000", (*posts)[0].ThreadTS)

	*posts = nil
	payload.Event.ThreadTS = ""
	require.NoError(t, c.dispatchEvent(context.Background(), payload, handler))
	assert.Empty(t, seen.Thread)
	require.Len(t, *posts, 1)
	assert.Empty(t, (*posts)[0].ThreadTS, "top-level message gets a top-level reply")
}

// A reply over Slack's text ceiling goes out as several messages in
// order, each within the cap.
func TestDispatchEvent_LongReplyIsChunked(t *testing.T) {
	c, posts := recordingClient(t)
	long := strings.Repeat("word ", 2000) // 10000 bytes
	handler := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return long, nil
	})
	payload := eventsAPIPayload{Event: slackEvent{Type: "message", User: "U1", Text: "q", Channel: "C0"}}
	require.NoError(t, c.dispatchEvent(context.Background(), payload, handler))
	require.Len(t, *posts, 3)
	var joined []string
	for _, p := range *posts {
		assert.LessOrEqual(t, len(p.Text), maxTextLen)
		assert.Equal(t, "C0", p.Channel)
		joined = append(joined, p.Text)
	}
	assert.Equal(t, strings.Fields(long), strings.Fields(strings.Join(joined, " ")), "no words lost")
}

func TestDispatchEvent_EmptyReplyPostsNothing(t *testing.T) {
	c, posts := recordingClient(t)
	handler := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return "", nil
	})
	payload := eventsAPIPayload{Event: slackEvent{Type: "message", User: "U1", Text: "q", Channel: "C0"}}
	require.NoError(t, c.dispatchEvent(context.Background(), payload, handler))
	assert.Empty(t, *posts)
}
