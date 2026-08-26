package imessage

import (
	"context"
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

func TestTranscribeAudio_NilTranscriberIsNoop(t *testing.T) {
	c, err := New(Config{BaseURL: "http://x", Password: "p"}, silentLogger())
	require.NoError(t, err)
	assert.Empty(t, c.transcribeAudio(context.Background(), []attachmentRecord{
		{GUID: "g", MimeType: "audio/mp4"},
	}))
}

func TestTranscribeAudio_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.True(t, strings.HasPrefix(r.URL.Path, "/api/v1/attachment/"))
		assert.True(t, strings.HasSuffix(r.URL.Path, "/download"))
		assert.Equal(t, "secret", r.URL.Query().Get("password"))
		_, _ = w.Write([]byte("VOICE")) //nolint:errcheck // test writer
	}))
	defer srv.Close()

	ft := &fakeTranscriber{reply: "hi"}
	c, err := New(Config{
		BaseURL: srv.URL, Password: "secret", Transcriber: ft, HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)

	got := c.transcribeAudio(context.Background(), []attachmentRecord{
		{GUID: "abc-guid", MimeType: "audio/x-caf"},
	})
	assert.Equal(t, "hi", got)
	assert.Equal(t, []byte("VOICE"), ft.audio)
	assert.Equal(t, "audio/x-caf", ft.mimetype)
}

func TestTranscribeAudio_NonAudioSkipped(t *testing.T) {
	ft := &fakeTranscriber{reply: "no"}
	c, err := New(Config{BaseURL: "http://x", Password: "p", Transcriber: ft}, silentLogger())
	require.NoError(t, err)
	got := c.transcribeAudio(context.Background(), []attachmentRecord{
		{GUID: "g", MimeType: "image/png"},
	})
	assert.Empty(t, got)
	assert.Empty(t, ft.mimetype)
}

func TestTranscribeAudio_HTTPErrorReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	ft := &fakeTranscriber{reply: "not used"}
	c, err := New(Config{
		BaseURL: srv.URL, Password: "p", Transcriber: ft, HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)

	assert.Empty(t, c.transcribeAudio(context.Background(), []attachmentRecord{
		{GUID: "g", MimeType: "audio/aac"},
	}))
}

func TestTranscribeAudio_MaxBytesRespected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, 200)) //nolint:errcheck // test writer
	}))
	defer srv.Close()

	ft := &fakeTranscriber{reply: "ok"}
	c, err := New(Config{
		BaseURL: srv.URL, Password: "p", Transcriber: ft, HTTPClient: srv.Client(),
		MaxAudioBytes: 25,
	}, silentLogger())
	require.NoError(t, err)

	_ = c.transcribeAudio(context.Background(), []attachmentRecord{
		{GUID: "g", MimeType: "audio/aac"},
	})
	assert.Len(t, ft.audio, 25)
}
