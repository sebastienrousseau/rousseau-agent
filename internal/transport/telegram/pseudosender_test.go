package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// routeRaw decodes a raw Bot API update (so the JSON tags are
// exercised), routes it, and reports whether the handler fired along
// with the debug log.
func routeRaw(t *testing.T, raw string) (called bool, logs string) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c, err := New(Config{Token: "t", BaseURL: "http://127.0.0.1:1"}, logger)
	require.NoError(t, err)
	var u telegramUpdate
	require.NoError(t, json.Unmarshal([]byte(raw), &u))
	c.route(context.Background(), u, transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		called = true
		return "", nil
	}))
	return called, buf.String()
}

// Anonymous group admins all post as @GroupAnonymousBot, so their
// shared from.id would let any anonymous admin of any group act as
// whoever that ID was allow-listed for.
func TestRoute_DropsAnonymousGroupAdmin(t *testing.T) {
	called, logs := routeRaw(t, `{"update_id":1,"message":{"message_id":1,"text":"hi",
		"chat":{"id":-1001,"type":"supergroup"},
		"from":{"id":1087968824,"is_bot":true,"username":"GroupAnonymousBot"},
		"sender_chat":{"id":-1001,"type":"supergroup"}}}`)
	assert.False(t, called, "anonymous admin must not reach the handler")
	assert.Contains(t, logs, "telegram.pseudo_sender_dropped")
}

// A post sent "as a channel" carries from.id 136817688 (@Channel_Bot)
// for every channel on Telegram.
func TestRoute_DropsSendAsChannel(t *testing.T) {
	called, logs := routeRaw(t, `{"update_id":1,"message":{"message_id":1,"text":"hi",
		"chat":{"id":-1001,"type":"supergroup"},
		"from":{"id":136817688,"is_bot":true,"username":"Channel_Bot"},
		"sender_chat":{"id":-1002,"type":"channel"}}}`)
	assert.False(t, called, "send-as-channel post must not reach the handler")
	assert.Contains(t, logs, "telegram.pseudo_sender_dropped")
}

// The pseudo-sender IDs are dropped even without sender_chat.
func TestRoute_DropsPseudoSenderIDWithoutSenderChat(t *testing.T) {
	called, _ := routeRaw(t, `{"update_id":1,"message":{"message_id":1,"text":"hi",
		"chat":{"id":-1001,"type":"supergroup"},
		"from":{"id":1087968824,"is_bot":true}}}`)
	assert.False(t, called)
}

func TestRoute_RealGroupMemberStillRoutes(t *testing.T) {
	called, _ := routeRaw(t, `{"update_id":1,"message":{"message_id":1,"text":"hi",
		"chat":{"id":-1001,"type":"supergroup"},
		"from":{"id":4242,"is_bot":false}}}`)
	assert.True(t, called)
}
