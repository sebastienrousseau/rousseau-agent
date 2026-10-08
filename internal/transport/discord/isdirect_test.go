package discord

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

func handleDiscord(t *testing.T, guild string) transport.IncomingMessage {
	t.Helper()
	c, err := New(Config{Token: "t", BaseURL: "http://127.0.0.1:1"}, silentLogger())
	require.NoError(t, err)
	var seen transport.IncomingMessage
	called := false
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen, called = m, true
		return "", nil
	})
	m := discordMessage{ID: "M1", ChannelID: "C1", GuildID: guild, Author: discordUser{ID: "U1"}, Content: "hi"}
	require.NoError(t, c.handleMessage(context.Background(), m, handler))
	require.True(t, called)
	return seen
}

func TestHandleMessage_DMIsDirect(t *testing.T) {
	assert.True(t, handleDiscord(t, "").IsDirect)
}

func TestHandleMessage_GuildChannelIsNotDirect(t *testing.T) {
	assert.False(t, handleDiscord(t, "G1").IsDirect)
}
