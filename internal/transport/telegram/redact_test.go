package telegram

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// fakeToken is not a real bot token; fakeTokenTail is the part a leak
// check looks for.
const (
	fakeToken     = "123456:FAKE-test-token"
	fakeTokenTail = "FAKE-test-token"
)

// syncBuffer is a goroutine-safe log sink.
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

func TestCall_NetworkErrorHidesToken(t *testing.T) {
	c, err := New(Config{Token: fakeToken, BaseURL: "http://127.0.0.1:1"}, silentLogger())
	require.NoError(t, err)
	err = c.Deliver(context.Background(), "1", "hi")
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), fakeTokenTail), "bot token leaked into the call error")
}

// A malformed BaseURL fails while the request is built; that parse
// error quotes the URL too.
func TestCall_BuildErrorHidesToken(t *testing.T) {
	c, err := New(Config{Token: fakeToken, BaseURL: "http://exa\x7fmple.invalid"}, silentLogger())
	require.NoError(t, err)
	err = c.Deliver(context.Background(), "1", "hi")
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), fakeTokenTail), "bot token leaked into the build error")
}

func TestStart_PollFailureLogHidesToken(t *testing.T) {
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	c, err := New(Config{Token: fakeToken, BaseURL: "http://127.0.0.1:1"}, logger)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.Start(ctx, transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { //nolint:errcheck // cancelled
			return "", nil
		}))
	}()
	require.Eventually(t, func() bool { return strings.Contains(logs.String(), "telegram.poll_failed") },
		5*time.Second, 10*time.Millisecond)
	cancel()
	<-done
	assert.False(t, strings.Contains(logs.String(), fakeTokenTail), "bot token leaked into telegram.poll_failed")
}

func TestDownloadFile_NetworkErrorHidesToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/file/") {
			// Drop the connection so the client sees a transport error.
			hj, ok := w.(http.Hijacker)
			if ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close() //nolint:errcheck // fixture
				}
			}
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"file_path":"voice/f.ogg"}}`)) //nolint:errcheck // fixture
	}))
	defer srv.Close()
	c, err := New(Config{Token: fakeToken, BaseURL: srv.URL, HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	_, _, err = c.downloadFile(context.Background(), "file-1")
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), fakeTokenTail), "bot token leaked into the download error")
}
