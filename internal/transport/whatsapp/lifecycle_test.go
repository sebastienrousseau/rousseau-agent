package whatsapp

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	waProto "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// injectedClient constructs a Client with the sender/downloader/ownID
// pre-populated, sidestepping Start (which needs a live whatsmeow
// socket). Used by every lifecycle test to exercise Deliver +
// handleMessage + onEvent without touching the network.
func injectedClient(t *testing.T, sender Sender, downloader Downloader) *Client {
	t.Helper()
	c, err := New(Config{StoreDSN: "x"}, silentLogger())
	require.NoError(t, err)
	own := &types.JID{User: "15551234567", Server: "s.whatsapp.net", Device: 21}
	c.sender = sender
	c.downloader = downloader
	c.ownID = own
	return c
}

func TestDeliver_UsesSender(t *testing.T) {
	send := &fakeSender{}
	c := injectedClient(t, send, nil)
	require.NoError(t, c.Deliver(context.Background(), "15551234567@s.whatsapp.net", "hi"))
	require.Len(t, send.sent, 1)
	assert.Equal(t, DefaultReplyHeader+"hi", send.sent[0])
}

func TestDeliver_BadJIDErrors(t *testing.T) {
	send := &fakeSender{}
	c := injectedClient(t, send, nil)
	err := c.Deliver(context.Background(), "", "hi")
	assert.Error(t, err)
	assert.Empty(t, send.sent)
}

func TestDeliver_SenderErrorSurfaces(t *testing.T) {
	send := &fakeSender{sendErr: errors.New("network")}
	c := injectedClient(t, send, nil)
	err := c.Deliver(context.Background(), "15551234567@s.whatsapp.net", "hi")
	assert.Error(t, err)
}

func TestHandleMessage_UsesInjectedSender(t *testing.T) {
	send := &fakeSender{}
	c := injectedClient(t, send, nil)
	c.handler = transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return "reply body", nil
	})
	sender := types.JID{User: "15551234567", Server: "s.whatsapp.net"}
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Sender: sender, Chat: sender.ToNonAD()},
		},
		Message: &waProto.Message{Conversation: proto.String("hello")},
	}
	c.handleMessage(evt)
	require.Len(t, send.sent, 1)
	assert.Contains(t, send.sent[0], "reply body")
}

func TestHandleMessage_NilSenderIsNoop(t *testing.T) {
	c, err := New(Config{StoreDSN: "x"}, silentLogger())
	require.NoError(t, err)
	assert.NotPanics(t, func() { c.handleMessage(nil) })
}

func TestOnEvent_MessageRoutesThroughHandler(t *testing.T) {
	send := &fakeSender{}
	c := injectedClient(t, send, nil)
	invoked := false
	c.handler = transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		invoked = true
		return "", nil
	})
	sender := types.JID{User: "15551234567", Server: "s.whatsapp.net"}
	c.onEvent(&events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{Sender: sender, Chat: sender.ToNonAD()}},
		Message: &waProto.Message{Conversation: proto.String("hi")},
	})
	assert.True(t, invoked)
}

func TestOnEvent_LifecycleEvents(t *testing.T) {
	c := injectedClient(t, &fakeSender{}, nil)
	// Each variant hits a distinct log-level branch; the invariant we
	// care about is "never panic on any known event type."
	assert.NotPanics(t, func() { c.onEvent(&events.Connected{}) })
	assert.NotPanics(t, func() { c.onEvent(&events.Disconnected{}) })
	assert.NotPanics(t, func() { c.onEvent(&events.LoggedOut{Reason: 0}) })
	// Unknown events are dropped, must not panic.
	assert.NotPanics(t, func() { c.onEvent("not an event type we handle") })
}

func TestStop_IdempotentWithInjectedSender(t *testing.T) {
	c := injectedClient(t, &fakeSender{}, nil)
	require.NoError(t, c.Stop())
	require.NoError(t, c.Stop())
}
