//go:build !no_whatsmeow

package whatsapp

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// logBuffer is a slog handler backed by a bytes.Buffer so tests can
// inspect the exact log lines onEvent emits. Writes and reads are
// mutex-guarded because some tests exercise handlers that emit from
// their own goroutines (e.g. the keepalive reconnect path).
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) newLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (l *logBuffer) has(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return bytes.Contains(l.buf.Bytes(), []byte(sub))
}

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

// --- KeepAlive handling ------------------------------------------------

// swapKeepaliveThreshold overrides keepaliveMissThreshold for the
// duration of a test and restores it via t.Cleanup.
func swapKeepaliveThreshold(t *testing.T, v int) {
	t.Helper()
	prev := keepaliveMissThreshold
	keepaliveMissThreshold = v
	t.Cleanup(func() { keepaliveMissThreshold = prev })
}

// swapForceReconnect substitutes wmForceReconnect with a fake that
// signals when it fires. Returns the signal channel and installs a
// cleanup to restore the original seam.
func swapForceReconnect(t *testing.T, retErr error) chan struct{} {
	t.Helper()
	prev := wmForceReconnect
	fired := make(chan struct{}, 4)
	wmForceReconnect = func(_ *whatsmeow.Client) error {
		fired <- struct{}{}
		return retErr
	}
	t.Cleanup(func() { wmForceReconnect = prev })
	return fired
}

func TestOnEvent_KeepAliveTimeout_LogsAndCountsWithoutTrippingBelowThreshold(t *testing.T) {
	swapKeepaliveThreshold(t, 3)
	fired := swapForceReconnect(t, nil)

	logs := &logBuffer{}
	c := newClientWithLog(t, logs.newLogger(), &fakeSender{})
	c.wm = &whatsmeow.Client{} // non-nil so the guard doesn't short-circuit

	c.onEvent(&events.KeepAliveTimeout{})
	c.onEvent(&events.KeepAliveTimeout{})

	assert.True(t, logs.has("whatsapp.keepalive_timeout"))
	assert.True(t, logs.has("consecutive=1"))
	assert.True(t, logs.has("consecutive=2"))
	assert.False(t, logs.has("whatsapp.keepalive_reconnect"))

	select {
	case <-fired:
		t.Fatal("forceReconnect fired before threshold")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestOnEvent_KeepAliveTimeout_TriggersReconnectAtThreshold(t *testing.T) {
	swapKeepaliveThreshold(t, 2)
	fired := swapForceReconnect(t, nil)

	logs := &logBuffer{}
	c := newClientWithLog(t, logs.newLogger(), &fakeSender{})
	c.wm = &whatsmeow.Client{}

	c.onEvent(&events.KeepAliveTimeout{})
	c.onEvent(&events.KeepAliveTimeout{})

	select {
	case <-fired:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("forceReconnect did not fire at threshold")
	}

	// The reconnect helper resets the miss counter and clears the
	// in-flight flag. Give the goroutine a moment to run its post-
	// reconnect bookkeeping before asserting on state.
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return !c.reconnecting && c.keepaliveMisses == 0
	}, time.Second, 10*time.Millisecond)

	assert.True(t, logs.has("whatsapp.keepalive_reconnect"))
	assert.True(t, logs.has("whatsapp.reconnect_issued"))
}

func TestOnEvent_KeepAliveTimeout_ReconnectFailureIsLogged(t *testing.T) {
	swapKeepaliveThreshold(t, 1)
	swapForceReconnect(t, &wrappedErr{msg: "dial refused"})

	logs := &logBuffer{}
	c := newClientWithLog(t, logs.newLogger(), &fakeSender{})
	c.wm = &whatsmeow.Client{}

	c.onEvent(&events.KeepAliveTimeout{})

	require.Eventually(t, func() bool {
		return logs.has("whatsapp.reconnect_failed")
	}, time.Second, 10*time.Millisecond)

	// Even on failure the flags must clear so the next burst can try
	// again — a stuck reconnecting=true would wedge the daemon.
	c.mu.Lock()
	defer c.mu.Unlock()
	assert.False(t, c.reconnecting)
	assert.Equal(t, 0, c.keepaliveMisses)
}

func TestOnEvent_KeepAliveTimeout_DoesNotFireOverlappingReconnects(t *testing.T) {
	swapKeepaliveThreshold(t, 1)
	// A slow reconnect that blocks until we release it — lets us
	// prove the overlap guard holds while one attempt is still in
	// flight.
	release := make(chan struct{})
	fired := make(chan struct{}, 4)
	prev := wmForceReconnect
	wmForceReconnect = func(_ *whatsmeow.Client) error {
		fired <- struct{}{}
		<-release
		return nil
	}
	t.Cleanup(func() { wmForceReconnect = prev; close(release) })

	c := newClientWithLog(t, silentLogger(), &fakeSender{})
	c.wm = &whatsmeow.Client{}

	c.onEvent(&events.KeepAliveTimeout{}) // triggers reconnect
	// Wait for the first attempt to enter the fake before firing more.
	<-fired
	c.onEvent(&events.KeepAliveTimeout{})
	c.onEvent(&events.KeepAliveTimeout{})

	select {
	case <-fired:
		t.Fatal("overlapping reconnect fired while first was still in flight")
	case <-time.After(80 * time.Millisecond):
	}
}

func TestOnEvent_KeepAliveRestored_ResetsCounterAndLogsWhenDegraded(t *testing.T) {
	swapKeepaliveThreshold(t, 10) // high, so timeouts don't reconnect
	swapForceReconnect(t, nil)

	logs := &logBuffer{}
	c := newClientWithLog(t, logs.newLogger(), &fakeSender{})
	c.wm = &whatsmeow.Client{}

	c.onEvent(&events.KeepAliveTimeout{})
	c.onEvent(&events.KeepAliveTimeout{})
	c.onEvent(&events.KeepAliveRestored{})

	assert.True(t, logs.has("whatsapp.keepalive_restored"))
	assert.True(t, logs.has("recovered_after=2"))

	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Equal(t, 0, c.keepaliveMisses)
}

func TestOnEvent_KeepAliveRestored_StaysSilentOnHealthyConnection(t *testing.T) {
	logs := &logBuffer{}
	c := newClientWithLog(t, logs.newLogger(), &fakeSender{})

	c.onEvent(&events.KeepAliveRestored{})

	assert.False(t, logs.has("whatsapp.keepalive_restored"))
}

func TestOnEvent_ConnectedResetsKeepaliveCounter(t *testing.T) {
	swapKeepaliveThreshold(t, 10)
	swapForceReconnect(t, nil)

	c := newClientWithLog(t, silentLogger(), &fakeSender{})
	c.wm = &whatsmeow.Client{}

	c.onEvent(&events.KeepAliveTimeout{})
	c.onEvent(&events.KeepAliveTimeout{})
	c.onEvent(&events.Connected{})

	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Equal(t, 0, c.keepaliveMisses)
}
