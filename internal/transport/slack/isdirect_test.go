package slack

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// dispatchSlack decodes a raw events_api payload (so the JSON tags are
// exercised) and returns what the handler saw.
func dispatchSlack(t *testing.T, raw string) transport.IncomingMessage {
	t.Helper()
	c := newTestClient(t, Config{})
	var payload eventsAPIPayload
	require.NoError(t, json.Unmarshal([]byte(raw), &payload))
	var seen transport.IncomingMessage
	called := false
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen, called = m, true
		return "", nil
	})
	require.NoError(t, c.dispatchEvent(context.Background(), payload, handler))
	require.True(t, called)
	return seen
}

func TestDispatchEvent_IMChannelIsDirect(t *testing.T) {
	m := dispatchSlack(t, `{"event":{"type":"message","user":"U1","text":"hi","channel":"D1","channel_type":"im"}}`)
	assert.True(t, m.IsDirect)
}

func TestDispatchEvent_ChannelAndMPIMAreNotDirect(t *testing.T) {
	m := dispatchSlack(t, `{"event":{"type":"message","user":"U1","text":"hi","channel":"C1","channel_type":"channel"}}`)
	assert.False(t, m.IsDirect)
	m = dispatchSlack(t, `{"event":{"type":"message","user":"U1","text":"hi","channel":"G1","channel_type":"mpim"}}`)
	assert.False(t, m.IsDirect)
}
