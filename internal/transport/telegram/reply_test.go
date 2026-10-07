package telegram

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

type sent struct {
	ChatID   int64  `json:"chat_id"`
	Text     string `json:"text"`
	ThreadID int64  `json:"message_thread_id"`
}

func recordingClient(t *testing.T, header string) (*Client, *[]sent) {
	t.Helper()
	var sends []sent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			raw, _ := io.ReadAll(r.Body) //nolint:errcheck // test fixture
			var s sent
			_ = json.Unmarshal(raw, &s) //nolint:errcheck // test fixture
			sends = append(sends, s)
		}
		_, _ = w.Write([]byte(`{"ok":true}`)) //nolint:errcheck // test fixture
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{Token: "t", BaseURL: srv.URL, HTTPClient: srv.Client(), ReplyHeader: header}, silentLogger())
	require.NoError(t, err)
	return c, &sends
}

// A forum-topic message carries chat, message and topic ids, and the
// reply stays in the topic.
func TestRoute_TopicMessageRepliesInTopic(t *testing.T) {
	c, sends := recordingClient(t, "")
	var seen transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen = m
		return "topic reply", nil
	})
	c.route(context.Background(), telegramUpdate{Message: &telegramMessage{
		MessageID: 55, MessageThreadID: 7, IsTopicMessage: true, Date: 1, Text: "q",
		Chat: telegramChat{ID: -100, Type: "supergroup"}, From: &telegramUser{ID: 42},
	}}, handler)
	assert.Equal(t, "-100", seen.Conversation)
	assert.Equal(t, "55", seen.MessageID)
	assert.Equal(t, "7", seen.Thread)
	require.Len(t, *sends, 1)
	assert.Equal(t, int64(-100), (*sends)[0].ChatID)
	assert.Equal(t, int64(7), (*sends)[0].ThreadID)
}

// A plain reply inside a group also carries message_thread_id but is
// not a topic; it must not be treated as one.
func TestRoute_NonTopicThreadIDIgnored(t *testing.T) {
	c, sends := recordingClient(t, "")
	var seen transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen = m
		return "ok", nil
	})
	c.route(context.Background(), telegramUpdate{Message: &telegramMessage{
		MessageID: 56, MessageThreadID: 9, Date: 1, Text: "q", Chat: telegramChat{ID: 5},
	}}, handler)
	assert.Empty(t, seen.Thread)
	require.Len(t, *sends, 1)
	assert.Zero(t, (*sends)[0].ThreadID)
}

func TestRoute_LongReplyIsChunkedWithHeaderOnEach(t *testing.T) {
	c, sends := recordingClient(t, "🤖 ")
	long := strings.Repeat("abcdefghij ", 1000) // 11000 bytes
	handler := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return long, nil
	})
	c.route(context.Background(), telegramUpdate{Message: &telegramMessage{
		MessageID: 1, Date: 1, Text: "q", Chat: telegramChat{ID: 5},
	}}, handler)
	require.Len(t, *sends, 3)
	for _, s := range *sends {
		assert.True(t, strings.HasPrefix(s.Text, "🤖 "))
		assert.LessOrEqual(t, len(s.Text), maxTextLen+len("🤖 "))
	}
}

func TestRoute_EmptyReplySendsNothing(t *testing.T) {
	c, sends := recordingClient(t, "")
	handler := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil })
	c.route(context.Background(), telegramUpdate{Message: &telegramMessage{MessageID: 1, Date: 1, Text: "q", Chat: telegramChat{ID: 5}}}, handler)
	assert.Empty(t, *sends)
}
