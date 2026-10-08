package imessage

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// fakePassword is not a real BlueBubbles password.
const fakePassword = "fake-bb-password"

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func unreachableClient(t *testing.T, logger *slog.Logger) *Client {
	t.Helper()
	c, err := New(Config{
		BaseURL:      "http://127.0.0.1:1",
		Password:     fakePassword,
		PollInterval: 5 * time.Millisecond,
	}, logger)
	require.NoError(t, err)
	return c
}

func TestDeliver_NetworkErrorHidesPassword(t *testing.T) {
	err := unreachableClient(t, silentLogger()).Deliver(context.Background(), "iMessage;-;+1", "hi")
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), fakePassword), "password leaked into the send error")
}

// A malformed BaseURL fails while the request is built; that parse
// error quotes the URL too.
func TestDeliver_BuildErrorHidesPassword(t *testing.T) {
	c, err := New(Config{BaseURL: "http://exa\x7fmple.invalid", Password: fakePassword}, silentLogger())
	require.NoError(t, err)
	err = c.Deliver(context.Background(), "iMessage;-;+1", "hi")
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), fakePassword), "password leaked into the build error")
}

func TestFetchMessages_NetworkErrorHidesPassword(t *testing.T) {
	_, err := unreachableClient(t, silentLogger()).fetchMessages(context.Background(), 1)
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), fakePassword), "password leaked into the poll error")
}

func TestDownloadAttachment_NetworkErrorHidesPassword(t *testing.T) {
	_, err := unreachableClient(t, silentLogger()).downloadAttachment(context.Background(), "att-1")
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), fakePassword), "password leaked into the download error")
}

func TestStart_PollFailureLogHidesPassword(t *testing.T) {
	logs := &syncBuffer{}
	c := unreachableClient(t, slog.New(slog.NewTextHandler(logs, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.Start(ctx, transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { //nolint:errcheck // cancelled
			return "", nil
		}))
	}()
	require.Eventually(t, func() bool { return strings.Contains(logs.String(), "imessage.poll_failed") },
		5*time.Second, 5*time.Millisecond)
	cancel()
	<-done
	assert.False(t, strings.Contains(logs.String(), fakePassword), "password leaked into the logs")
}
