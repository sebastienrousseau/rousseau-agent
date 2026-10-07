package whatsapp

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

func TestResolveInbound_CarriesChatAndMessageID(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("15559990000", 0)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hi")
	evt.Info.ID = "3EB0ABC"
	res := ResolveInbound(evt, &own)
	require.Empty(t, res.Skip)
	assert.Equal(t, sender.ToNonAD().String(), res.Msg.Conversation)
	assert.Equal(t, "3EB0ABC", res.Msg.MessageID)
}

func TestDispatch_LongReplyIsChunked(t *testing.T) {
	own := jid("15551234567", 21)
	sender := jid("15551234567", 0)
	evt := msgEvent(sender, sender.ToNonAD(), false, false, "hi")
	long := strings.Repeat("0123456789 ", 12000) // 132000 bytes

	send := &fakeSender{}
	var seen transport.IncomingMessage
	Dispatch(context.Background(), DispatchInput{
		Event: evt, OwnID: &own, Sender: send,
		Handler: transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
			seen = m
			return long, nil
		}),
		Header: " ", Logger: silentLogger(),
	})
	assert.Equal(t, sender.ToNonAD().String(), seen.Conversation)
	require.Len(t, send.sent, 3)
	for _, part := range send.sent {
		assert.LessOrEqual(t, len(part), maxTextLen+1)
	}
}
