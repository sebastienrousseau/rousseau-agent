package whatsapp

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	waProto "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// logBuffer is a slog handler backed by a bytes.Buffer so tests can
// inspect the exact log lines onEvent emits.
type logBuffer struct{ buf bytes.Buffer }

func (l *logBuffer) newLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&l.buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (l *logBuffer) has(sub string) bool { return bytes.Contains(l.buf.Bytes(), []byte(sub)) }

func newClientWithLog(t *testing.T, logs *slog.Logger, send Sender) *Client {
	t.Helper()
	c, err := New(Config{StoreDSN: "x"}, logs)
	require.NoError(t, err)
	c.sender = send
	c.ownID = &types.JID{User: "15551234567", Server: "s.whatsapp.net", Device: 21}
	c.handler = transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil })
	return c
}

func TestOnEvent_ConnectedEmitsInfo(t *testing.T) {
	logs := &logBuffer{}
	c := newClientWithLog(t, logs.newLogger(), &fakeSender{})
	c.onEvent(&events.Connected{})
	assert.True(t, logs.has("whatsapp.connected"))
}

func TestOnEvent_DisconnectedEmitsWarn(t *testing.T) {
	logs := &logBuffer{}
	c := newClientWithLog(t, logs.newLogger(), &fakeSender{})
	c.onEvent(&events.Disconnected{})
	assert.True(t, logs.has("whatsapp.disconnected"))
}

func TestOnEvent_LoggedOutEmitsError(t *testing.T) {
	logs := &logBuffer{}
	c := newClientWithLog(t, logs.newLogger(), &fakeSender{})
	c.onEvent(&events.LoggedOut{Reason: 3})
	assert.True(t, logs.has("whatsapp.logged_out"))
	assert.True(t, logs.has("reason=3"))
}

func TestOnEvent_UnhandledTypesAreSilentlyDropped(t *testing.T) {
	logs := &logBuffer{}
	c := newClientWithLog(t, logs.newLogger(), &fakeSender{})
	// Types we don't switch on (e.g. events.HistorySync) must not
	// emit anything, and MUST not panic.
	assert.NotPanics(t, func() { c.onEvent(&events.HistorySync{}) })
	assert.False(t, logs.has("whatsapp.connected"))
}

func TestOnEvent_MessageDispatchesThroughSender(t *testing.T) {
	send := &fakeSender{}
	c := newClientWithLog(t, silentLogger(), send)
	c.handler = transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return "reply", nil
	})

	from := types.JID{User: "15551234567", Server: "s.whatsapp.net"}
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Sender: from, Chat: from.ToNonAD()},
		},
		Message: &waProto.Message{Conversation: proto.String("hello")},
	}
	c.onEvent(evt)

	send.mu.Lock()
	defer send.mu.Unlock()
	require.Len(t, send.sent, 1)
	assert.Contains(t, send.sent[0], "reply")
}

func TestHandleMessage_NilSenderShortCircuits(t *testing.T) {
	// A Client that has never been through Start — sender is nil.
	c, err := New(Config{StoreDSN: "x"}, silentLogger())
	require.NoError(t, err)
	assert.NotPanics(t, func() {
		c.handleMessage(&events.Message{Info: types.MessageInfo{}})
	})
}

func TestClient_DeliverForwardsSenderError(t *testing.T) {
	send := &fakeSender{sendErr: errCloudDown}
	c := newClientWithLog(t, silentLogger(), send)
	err := c.Deliver(context.Background(), "15551234567@s.whatsapp.net", "hi")
	assert.ErrorIs(t, err, errCloudDown)
}

var errCloudDown = &wrappedErr{msg: "cloud down"}

type wrappedErr struct{ msg string }

func (e *wrappedErr) Error() string { return e.msg }
