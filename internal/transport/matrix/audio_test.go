package matrix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeTranscriber struct {
	mu       sync.Mutex
	audio    []byte
	mimetype string
	reply    string
}

func (f *fakeTranscriber) Transcribe(_ context.Context, audio []byte, mimetype string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audio = append([]byte(nil), audio...)
	f.mimetype = mimetype
	return f.reply, nil
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func TestParseMXC(t *testing.T) {
	server, mediaID, ok := parseMXC("mxc://matrix.org/abc123")
	require.True(t, ok)
	assert.Equal(t, "matrix.org", server)
	assert.Equal(t, "abc123", mediaID)

	for _, bad := range []string{
		"",
		"http://matrix.org/abc",
		"mxc://",
		"mxc://onlyserver",
		"mxc:///no-server",
		"mxc://server/",
	} {
		_, _, ok := parseMXC(bad)
		assert.False(t, ok, bad)
	}
}

func TestTranscribeAudio_NilTranscriberIsNoop(t *testing.T) {
	c, err := New(Config{HomeserverURL: "http://x", AccessToken: "t"}, silentLogger())
	require.NoError(t, err)
	got := c.transcribeAudio(context.Background(), mustJSON(t, map[string]any{
		"msgtype": "m.audio",
		"url":     "mxc://x/abc",
	}))
	assert.Empty(t, got)
}

func TestTranscribeAudio_NonAudioMsgTypeSkipped(t *testing.T) {
	ft := &fakeTranscriber{reply: "no"}
	c, err := New(Config{HomeserverURL: "http://x", AccessToken: "t", Transcriber: ft}, silentLogger())
	require.NoError(t, err)
	got := c.transcribeAudio(context.Background(), mustJSON(t, map[string]any{
		"msgtype": "m.image",
		"url":     "mxc://x/abc",
	}))
	assert.Empty(t, got)
	assert.Empty(t, ft.mimetype)
}

func TestTranscribeAudio_HappyPathViaV1Endpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Authenticated v1 endpoint must include the bearer token.
		assert.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
		assert.True(t, strings.HasPrefix(r.URL.Path, "/_matrix/client/v1/media/download/"))
		assert.Contains(t, r.URL.Path, "matrix.org")
		assert.Contains(t, r.URL.Path, "abc123")
		_, _ = w.Write([]byte("VOICE")) //nolint:errcheck // test writer
	}))
	defer srv.Close()

	ft := &fakeTranscriber{reply: "hello"}
	c, err := New(Config{
		HomeserverURL: srv.URL, AccessToken: "tok", Transcriber: ft, HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)

	got := c.transcribeAudio(context.Background(), mustJSON(t, map[string]any{
		"msgtype": "m.audio",
		"url":     "mxc://matrix.org/abc123",
		"info":    map[string]string{"mimetype": "audio/ogg"},
	}))
	assert.Equal(t, "hello", got)
	assert.Equal(t, []byte("VOICE"), ft.audio)
	assert.Equal(t, "audio/ogg", ft.mimetype)
}

func TestTranscribeAudio_FallsBackToV3EndpointOn404(t *testing.T) {
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		if strings.HasPrefix(r.URL.Path, "/_matrix/client/v1/") {
			http.NotFound(w, r)
			return
		}
		assert.True(t, strings.HasPrefix(r.URL.Path, "/_matrix/media/v3/download/"))
		_, _ = w.Write([]byte("legacy")) //nolint:errcheck // test writer
	}))
	defer srv.Close()

	ft := &fakeTranscriber{reply: "ok"}
	c, err := New(Config{
		HomeserverURL: srv.URL, AccessToken: "t", Transcriber: ft, HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)

	got := c.transcribeAudio(context.Background(), mustJSON(t, map[string]any{
		"msgtype": "m.audio",
		"url":     "mxc://matrix.org/abc",
	}))
	assert.Equal(t, "ok", got)
	assert.Len(t, hits, 2, "must try v1 then fall back to v3")
	assert.Equal(t, "audio/ogg", ft.mimetype, "empty info.mimetype defaults to audio/ogg")
}

func TestTranscribeAudio_HTTPErrorReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ft := &fakeTranscriber{reply: "should not fire"}
	c, err := New(Config{
		HomeserverURL: srv.URL, AccessToken: "t", Transcriber: ft, HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)

	got := c.transcribeAudio(context.Background(), mustJSON(t, map[string]any{
		"msgtype": "m.audio",
		"url":     "mxc://x/abc",
	}))
	assert.Empty(t, got)
}

func TestTranscribeAudio_MalformedMXCSkipped(t *testing.T) {
	ft := &fakeTranscriber{reply: "no"}
	c, err := New(Config{HomeserverURL: "http://x", AccessToken: "t", Transcriber: ft}, silentLogger())
	require.NoError(t, err)
	got := c.transcribeAudio(context.Background(), mustJSON(t, map[string]any{
		"msgtype": "m.audio",
		"url":     "not-mxc",
	}))
	assert.Empty(t, got)
	assert.Empty(t, ft.mimetype)
}
