package telegram

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

func newTestClient(t *testing.T, cfg Config) *Client {
	t.Helper()
	if cfg.Token == "" {
		cfg.Token = "tok"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	c, err := New(cfg, silentLogger())
	require.NoError(t, err)
	return c
}

// shortBodyServer answers every request with a Content-Length that
// overstates the bytes actually written, so the client's io.ReadAll
// fails with an unexpected EOF.
func shortBodyServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }() //nolint:errcheck // fixture
		writeTruncated(buf)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeTruncated(buf *bufio.ReadWriter) {
	_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 8192\r\n\r\nshort") //nolint:errcheck // fixture
	_ = buf.Flush()                                                                //nolint:errcheck // fixture
}

func TestNew_NilLoggerFallsBackToDefault(t *testing.T) {
	c, err := New(Config{Token: "t"}, nil)
	require.NoError(t, err)
	assert.NotNil(t, c.logger)
	assert.Equal(t, "telegram", c.Name())
	assert.Equal(t, 30*time.Second, c.cfg.PollTimeout)
}

// TestStart_RoutesUpdatesThenExitsMidPoll covers the update fan-out loop
// and the "poll failed because the context died" exit, which is distinct
// from the top-of-loop cancellation check.
func TestStart_RoutesUpdatesThenExitsMidPoll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var polls atomic.Int32
	sent := make(chan string, 1)
	inFlight := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// net/http only starts watching for client disconnects once the
		// request body has been drained, so drain it before blocking or
		// r.Context() will never be cancelled.
		reqBody, _ := io.ReadAll(r.Body) //nolint:errcheck // fixture
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			if polls.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":7,"message":{"date":1,"text":"ping","chat":{"id":42}}}]}`)) //nolint:errcheck // fixture
				return
			}
			once.Do(func() { close(inFlight) })
			<-r.Context().Done() // hang so the poll dies with the context
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			select {
			case sent <- string(reqBody):
			default:
			}
			_, _ = w.Write([]byte(`{"ok":true}`)) //nolint:errcheck // fixture
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, Config{BaseURL: srv.URL, HTTPClient: srv.Client(), PollTimeout: 50 * time.Millisecond})
	done := make(chan error, 1)
	go func() {
		done <- c.Start(ctx, transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
			return "pong:" + m.Body, nil
		}))
	}()

	select {
	case body := <-sent:
		assert.Contains(t, body, `"chat_id":42`)
		assert.Contains(t, body, `pong:ping`)
	case <-time.After(5 * time.Second):
		t.Fatal("update was never routed to the handler")
	}

	// Cancel only once a poll is genuinely in flight, so the loop exits
	// through the "poll failed because the context died" branch rather
	// than the top-of-loop check.
	select {
	case <-inFlight:
	case <-time.After(5 * time.Second):
		t.Fatal("second poll never started")
	}
	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after cancel")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Equal(t, int64(8), c.offset, "offset should advance past the consumed update")
}

// TestStart_ReturnsImmediatelyWhenStopped proves Stop() short-circuits
// the poll loop without issuing a request.
func TestStart_ReturnsImmediatelyWhenStopped(t *testing.T) {
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		polls.Add(1)
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`)) //nolint:errcheck // fixture
	}))
	defer srv.Close()

	c := newTestClient(t, Config{BaseURL: srv.URL, HTTPClient: srv.Client()})
	require.NoError(t, c.Stop())
	assert.NoError(t, c.Start(context.Background(), transport.HandlerFunc(
		func(context.Context, transport.IncomingMessage) (string, error) { return "", nil })))
	assert.Zero(t, polls.Load())
}

// TestStart_BackoffElapsesAndRepolls covers the 2s backoff arm of the
// retry select (the ctx.Done arm is covered elsewhere).
func TestStart_BackoffElapsesAndRepolls(t *testing.T) {
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		polls.Add(1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestClient(t, Config{BaseURL: srv.URL, HTTPClient: srv.Client(), PollTimeout: 10 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, c.Start(ctx, transport.HandlerFunc(
		func(context.Context, transport.IncomingMessage) (string, error) { return "", nil })), context.DeadlineExceeded)
	assert.GreaterOrEqual(t, polls.Load(), int32(2), "backoff should elapse and re-poll")
}

// TestRoute_DeliverFailureIsLogged proves a send failure does not panic
// or propagate — the update is simply dropped.
func TestRoute_DeliverFailureIsLogged(t *testing.T) {
	c := newTestClient(t, Config{BaseURL: "http://127.0.0.1:1", HTTPClient: &http.Client{Timeout: 2 * time.Second}})
	assert.NotPanics(t, func() {
		c.route(context.Background(), telegramUpdate{
			UpdateID: 1,
			Message:  &telegramMessage{Text: "hi", Chat: telegramChat{ID: 5}},
		}, transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
			return "reply", nil
		}))
	})
}

func TestCall_ErrorPaths(t *testing.T) {
	short := shortBodyServer(t)
	tests := []struct {
		name    string
		baseURL string
		payload any
		wantErr string
	}{
		{"unencodable payload", "http://127.0.0.1:1", make(chan int), "marshal sendMessage"},
		{"unbuildable request", "http://exa\x7fmple.invalid", map[string]any{}, "build request"},
		{"connection refused", "http://127.0.0.1:1", map[string]any{}, "telegram: sendMessage"},
		{"truncated response body", short.URL, map[string]any{}, "read body"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, Config{BaseURL: tc.baseURL})
			err := c.call(context.Background(), "sendMessage", tc.payload, nil)
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestDownloadFile_ErrorPaths(t *testing.T) {
	// getFileServer answers getFile with the supplied file_path and
	// delegates the /file/... download to dl.
	getFileServer := func(filePath string, dl http.HandlerFunc) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/file/bot") {
				dl(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":{"file_path":"` + filePath + `"}}`)) //nolint:errcheck // fixture
		}))
		t.Cleanup(srv.Close)
		return srv
	}

	t.Run("getFile transport failure", func(t *testing.T) {
		c := newTestClient(t, Config{BaseURL: "http://127.0.0.1:1", HTTPClient: &http.Client{Timeout: 2 * time.Second}})
		_, _, err := c.downloadFile(context.Background(), "f1")
		require.Error(t, err)
		assert.ErrorContains(t, err, "getFile")
	})

	t.Run("empty file_path", func(t *testing.T) {
		srv := getFileServer("", nil)
		c := newTestClient(t, Config{BaseURL: srv.URL, HTTPClient: srv.Client()})
		_, _, err := c.downloadFile(context.Background(), "f1")
		require.Error(t, err)
		assert.ErrorContains(t, err, "empty file_path")
	})

	t.Run("download request unbuildable", func(t *testing.T) {
		// A hostile getFile response can smuggle a control character into
		// file_path, which must fail request construction rather than be
		// pasted into a URL.
		srv := getFileServer(`a\u007fb.ogg`, nil)
		c := newTestClient(t, Config{BaseURL: srv.URL, HTTPClient: srv.Client()})
		_, _, err := c.downloadFile(context.Background(), "f1")
		require.Error(t, err)
		assert.ErrorContains(t, err, "build download request")
	})

	t.Run("download transport failure", func(t *testing.T) {
		srv := getFileServer("voice.ogg", nil)
		c := newTestClient(t, Config{BaseURL: srv.URL, HTTPClient: &http.Client{
			Timeout:   2 * time.Second,
			Transport: fileDownloadBlocker{base: srv.Client().Transport},
		}})
		_, _, err := c.downloadFile(context.Background(), "f1")
		require.Error(t, err)
		assert.ErrorContains(t, err, "download")
	})

	t.Run("download HTTP error", func(t *testing.T) {
		srv := getFileServer("voice.ogg", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "gone", http.StatusNotFound)
		})
		c := newTestClient(t, Config{BaseURL: srv.URL, HTTPClient: srv.Client()})
		_, _, err := c.downloadFile(context.Background(), "f1")
		require.Error(t, err)
		assert.ErrorContains(t, err, "HTTP 404")
	})

	t.Run("download body truncated", func(t *testing.T) {
		srv := getFileServer("voice.ogg", func(w http.ResponseWriter, _ *http.Request) {
			hj, ok := w.(http.Hijacker)
			if !ok {
				return
			}
			conn, buf, err := hj.Hijack()
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }() //nolint:errcheck // fixture
			writeTruncated(buf)
		})
		c := newTestClient(t, Config{BaseURL: srv.URL, HTTPClient: srv.Client()})
		_, _, err := c.downloadFile(context.Background(), "f1")
		require.Error(t, err)
		assert.ErrorContains(t, err, "read body")
	})
}

// TestDownloadFile_RejectsOversizedFile proves the 32 MiB guard trips
// before the daemon buffers an unbounded stream.
func TestDownloadFile_RejectsOversizedFile(t *testing.T) {
	const maxDownload = 32 * 1024 * 1024
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/file/bot") {
			_, _ = io.CopyN(w, zeroReader{}, maxDownload+16) //nolint:errcheck // fixture
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"file_path":"big.ogg"}}`)) //nolint:errcheck // fixture
	}))
	defer srv.Close()

	c := newTestClient(t, Config{BaseURL: srv.URL, HTTPClient: srv.Client()})
	_, _, err := c.downloadFile(context.Background(), "f1")
	require.Error(t, err)
	assert.ErrorContains(t, err, "exceeds")
}

// fileDownloadBlocker fails only the /file/bot… download leg, so the
// getFile call still succeeds and the download error path is reached.
type fileDownloadBlocker struct{ base http.RoundTripper }

func (f fileDownloadBlocker) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.Contains(r.URL.Path, "/file/bot") {
		return nil, errors.New("network down")
	}
	return f.base.RoundTrip(r)
}

// zeroReader is an infinite stream of NUL bytes.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
