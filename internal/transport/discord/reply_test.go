package discord

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

func TestHandleMessage_IDsAndChunkedReply(t *testing.T) {
	var contents []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/channels/C1/messages" {
			raw, _ := io.ReadAll(r.Body) //nolint:errcheck // test fixture
			var p struct{ Content string }
			_ = json.Unmarshal(raw, &p) //nolint:errcheck // test fixture
			contents = append(contents, p.Content)
		}
	}))
	defer srv.Close()
	c, err := New(Config{Token: "t", BaseURL: srv.URL, HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)

	long := strings.Repeat("line of text\n", 400) // 5200 bytes
	var seen transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen = m
		return long, nil
	})
	m := discordMessage{ID: "M9", ChannelID: "C1", GuildID: "G1", Author: discordUser{ID: "U1"}, Content: "hi"}
	require.NoError(t, c.handleMessage(context.Background(), m, handler))

	assert.Equal(t, "C1", seen.Conversation)
	assert.Equal(t, "M9", seen.MessageID)
	require.Len(t, contents, 3)
	for _, part := range contents {
		assert.LessOrEqual(t, len(part), maxTextLen)
	}
	assert.Equal(t, strings.Fields(long), strings.Fields(strings.Join(contents, "\n")), "no words lost")
}

func TestHandleMessage_EmptyReplyPostsNothing(t *testing.T) {
	posted := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { posted = true }))
	defer srv.Close()
	c, err := New(Config{Token: "t", BaseURL: srv.URL, HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	handler := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil })
	m := discordMessage{ID: "M9", ChannelID: "C1", Author: discordUser{ID: "U1"}, Content: "hi"}
	require.NoError(t, c.handleMessage(context.Background(), m, handler))
	assert.False(t, posted)
}
