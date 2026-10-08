package telegram

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// routeChat routes one text message from a chat of type chatType and
// returns what the handler saw.
func routeChat(t *testing.T, chatType string, chatID int64) transport.IncomingMessage {
	t.Helper()
	c, err := New(Config{Token: "t", BaseURL: "http://127.0.0.1:1"}, silentLogger())
	require.NoError(t, err)
	var seen transport.IncomingMessage
	called := false
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen, called = m, true
		return "", nil
	})
	c.route(context.Background(), telegramUpdate{Message: &telegramMessage{
		MessageID: 1, Text: "hi",
		Chat: telegramChat{ID: chatID, Type: chatType},
		From: &telegramUser{ID: 42},
	}}, handler)
	require.True(t, called, "handler must fire")
	return seen
}

func TestRoute_PrivateChatIsDirect(t *testing.T) {
	assert.True(t, routeChat(t, "private", 42).IsDirect)
}

func TestRoute_GroupChatIsNotDirect(t *testing.T) {
	assert.False(t, routeChat(t, "group", -100).IsDirect)
	assert.False(t, routeChat(t, "supergroup", -1001).IsDirect)
}
