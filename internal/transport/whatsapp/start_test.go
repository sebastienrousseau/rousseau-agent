//go:build !no_whatsmeow

package whatsapp

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// lockedBuffer is an io.Writer safe to read from the test goroutine
// while Start writes to it from its own.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedBuffer) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Len()
}

// stubSeams replaces the Start-path seams for the duration of a test
// and restores the production implementations afterwards.
func stubSeams(t *testing.T, newClient func(context.Context, Config) (*whatsmeow.Client, error),
	qrChan func(context.Context, *whatsmeow.Client) (<-chan whatsmeow.QRChannelItem, error),
	connect func(*whatsmeow.Client) error, out io.Writer,
) {
	t.Helper()
	oldClient, oldQR, oldConnect, oldOut := newWMClient, wmQRChannel, wmConnect, qrOut
	t.Cleanup(func() { newWMClient, wmQRChannel, wmConnect, qrOut = oldClient, oldQR, oldConnect, oldOut })
	if newClient != nil {
		newWMClient = newClient
	}
	if qrChan != nil {
		wmQRChannel = qrChan
	}
	if connect != nil {
		wmConnect = connect
	}
	if out != nil {
		qrOut = out
	}
}

func noopHandler() transport.Handler {
	return transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return "", nil
	})
}

// --- newWMClient (the real implementation) ---------------------------

func TestNewWMClient_OpensTempStoreAndYieldsUnpairedClient(t *testing.T) {
	wm, err := newWMClient(context.Background(), Config{StoreDSN: tempStoreDSN(t), LogLevel: "ERROR"})
	require.NoError(t, err)
	require.NotNil(t, wm)
	t.Cleanup(wm.Disconnect)
	// A brand-new store has never been paired, so there is no JID.
	assert.Nil(t, wm.Store.ID)
}

func TestNewWMClient_UnopenableStoreErrors(t *testing.T) {
	_, err := newWMClient(context.Background(),
		Config{StoreDSN: "file:/proc/self/definitely-not-a-dir/wa.db", LogLevel: "ERROR"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "whatsapp: open store")
}

// TestNewWMClient_DeviceLookupErrors drops the device table out from
// under an already-upgraded store, so sqlstore.New succeeds (schema
// version is current) but GetFirstDevice cannot read.
func TestNewWMClient_DeviceLookupErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wa.db")
	dsn := "file:" + path + "?_pragma=foreign_keys(1)"
	container, err := sqlstore.New(context.Background(), "sqlite", dsn, waLog.Noop)
	require.NoError(t, err)
	require.NoError(t, container.Close())

	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	_, err = db.Exec("DROP TABLE whatsmeow_device")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = newWMClient(context.Background(), Config{StoreDSN: dsn, LogLevel: "ERROR"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "whatsapp: get device")
}

// --- Start ------------------------------------------------------------

func TestStart_RejectsSecondCall(t *testing.T) {
	c, err := New(Config{StoreDSN: "x"}, silentLogger())
	require.NoError(t, err)
	c.wm = whatsmeow.NewClient(unpairedDevice(t), waLog.Noop)
	t.Cleanup(c.wm.Disconnect)

	err = c.Start(context.Background(), noopHandler())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

func TestStart_ClientConstructionErrorPropagates(t *testing.T) {
	boom := errors.New("whatsapp: open store: disk on fire")
	stubSeams(t, func(context.Context, Config) (*whatsmeow.Client, error) { return nil, boom }, nil, nil, nil)

	c, err := New(Config{StoreDSN: "x"}, silentLogger())
	require.NoError(t, err)
	assert.ErrorIs(t, c.Start(context.Background(), noopHandler()), boom)
}

func TestStart_QRChannelErrorIsWrapped(t *testing.T) {
	wm := whatsmeow.NewClient(unpairedDevice(t), waLog.Noop)
	t.Cleanup(wm.Disconnect)
	stubSeams(t,
		func(context.Context, Config) (*whatsmeow.Client, error) { return wm, nil },
		func(context.Context, *whatsmeow.Client) (<-chan whatsmeow.QRChannelItem, error) {
			return nil, errors.New("already connected")
		}, nil, nil)

	c, err := New(Config{StoreDSN: "x"}, silentLogger())
	require.NoError(t, err)
	err = c.Start(context.Background(), noopHandler())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "whatsapp: qr channel")

	// The client was still wired up before the failure, so the send
	// surface is available to Deliver / Stop.
	assert.NotNil(t, c.sender)
	assert.NotNil(t, c.downloader)
}

func TestStart_ConnectErrorDuringPairingIsWrapped(t *testing.T) {
	wm := whatsmeow.NewClient(unpairedDevice(t), waLog.Noop)
	t.Cleanup(wm.Disconnect)
	ch := make(chan whatsmeow.QRChannelItem)
	close(ch)
	stubSeams(t,
		func(context.Context, Config) (*whatsmeow.Client, error) { return wm, nil },
		func(context.Context, *whatsmeow.Client) (<-chan whatsmeow.QRChannelItem, error) {
			var ro <-chan whatsmeow.QRChannelItem = ch
			return ro, nil
		},
		func(*whatsmeow.Client) error { return errors.New("dial tcp: refused") }, nil)

	c, err := New(Config{StoreDSN: "x"}, silentLogger())
	require.NoError(t, err)
	err = c.Start(context.Background(), noopHandler())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "whatsapp: connect")
}

// TestStart_PairingLoopRendersQRThenPairs drives the QR pump with a
// scripted channel: a code (rendered to qrOut), a success, and an
// unrecognised event. Start then blocks until the context is
// cancelled and returns via Stop.
func TestStart_PairingLoopRendersQRThenPairs(t *testing.T) {
	wm := whatsmeow.NewClient(unpairedDevice(t), waLog.Noop)
	t.Cleanup(wm.Disconnect)

	ch := make(chan whatsmeow.QRChannelItem, 3)
	ch <- whatsmeow.QRChannelItem{Event: "code", Code: "pair-me"}
	ch <- whatsmeow.QRChannelItem{Event: "success"}
	ch <- whatsmeow.QRChannelItem{Event: "timeout"}
	close(ch)

	out := &lockedBuffer{}
	logs := &logBuffer{}
	connected := make(chan struct{})
	stubSeams(t,
		func(context.Context, Config) (*whatsmeow.Client, error) { return wm, nil },
		func(context.Context, *whatsmeow.Client) (<-chan whatsmeow.QRChannelItem, error) {
			var ro <-chan whatsmeow.QRChannelItem = ch
			return ro, nil
		},
		func(*whatsmeow.Client) error { close(connected); return nil }, out)

	c, err := New(Config{StoreDSN: "x"}, logs.newLogger())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx, noopHandler()) }()

	<-connected
	require.Eventually(t, func() bool { return out.Len() > 0 }, 2*time.Second, 5*time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}

	assert.True(t, logs.has("whatsapp.qr_ready"))
	assert.True(t, logs.has("whatsapp.paired"))
	assert.True(t, logs.has("whatsapp.qr_event"))
	assert.True(t, c.stopped, "Start must Stop the client on context cancellation")
}

// pairedClient returns a whatsmeow client whose store already carries
// a JID, i.e. a device that has completed pairing in an earlier run.
func pairedClient(t *testing.T) *whatsmeow.Client {
	t.Helper()
	device := unpairedDevice(t)
	jid := types.JID{User: "15551234567", Server: types.DefaultUserServer, Device: 21}
	device.ID = &jid
	wm := whatsmeow.NewClient(device, waLog.Noop)
	t.Cleanup(wm.Disconnect)
	return wm
}

func TestStart_PairedDeviceConnectErrorIsWrapped(t *testing.T) {
	stubSeams(t,
		func(context.Context, Config) (*whatsmeow.Client, error) { return pairedClient(t), nil },
		func(context.Context, *whatsmeow.Client) (<-chan whatsmeow.QRChannelItem, error) {
			t.Error("QR channel must not be requested for an already-paired device")
			return nil, nil
		},
		func(*whatsmeow.Client) error { return errors.New("dial tcp: refused") }, nil)

	c, err := New(Config{StoreDSN: "x"}, silentLogger())
	require.NoError(t, err)
	err = c.Start(context.Background(), noopHandler())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "whatsapp: connect")
}

func TestStart_PairedDeviceRunsUntilContextCancelled(t *testing.T) {
	wm := pairedClient(t)
	connected := make(chan struct{})
	stubSeams(t,
		func(context.Context, Config) (*whatsmeow.Client, error) { return wm, nil },
		nil,
		func(*whatsmeow.Client) error { close(connected); return nil }, nil)

	c, err := New(Config{StoreDSN: "x"}, silentLogger())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx, noopHandler()) }()

	<-connected
	// Own JID is adopted from the paired store, so inbound events can
	// be attributed to this account.
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.ownID != nil
	}, 2*time.Second, 5*time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}
	assert.Equal(t, "15551234567", c.ownID.User)
	assert.True(t, c.stopped)
}

func TestNew_NilLoggerFallsBackToDefault(t *testing.T) {
	c, err := New(Config{StoreDSN: "x"}, nil)
	require.NoError(t, err)
	assert.NotNil(t, c.logger)
}

// TestWMQRChannel_RealClient exercises the production QR-channel seam
// against a real whatsmeow client. GetQRChannel only registers an
// event handler — it opens no socket — so this stays hermetic.
func TestWMQRChannel_RealClient(t *testing.T) {
	unpaired := whatsmeow.NewClient(unpairedDevice(t), waLog.Noop)
	t.Cleanup(unpaired.Disconnect)
	ch, err := wmQRChannel(context.Background(), unpaired)
	require.NoError(t, err)
	assert.NotNil(t, ch)

	// An already-paired store has nothing to pair, so whatsmeow
	// refuses to hand out a QR channel.
	_, err = wmQRChannel(context.Background(), pairedClient(t))
	assert.ErrorIs(t, err, whatsmeow.ErrQRStoreContainsID)
}
