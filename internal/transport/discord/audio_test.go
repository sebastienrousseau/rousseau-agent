package discord

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

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// fakeTranscriber captures the call and returns a canned transcript.
type fakeTranscriber struct {
	mu       sync.Mutex
	audio    []byte
	mimetype string
	reply    string
	err      error
}

func (f *fakeTranscriber) Transcribe(_ context.Context, audio []byte, mimetype string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audio = append([]byte(nil), audio...)
	f.mimetype = mimetype
	return f.reply, f.err
}

func TestTranscribeAudio_NoAttachments_ReturnsEmpty(t *testing.T) {
	ft := &fakeTranscriber{reply: "hi"}
	c, err := New(Config{Token: "t", Transcriber: ft}, silentLogger())
	require.NoError(t, err)
	got := c.transcribeAudio(context.Background(), &discordMessage{})
	assert.Empty(t, got)
	assert.Nil(t, ft.audio, "should not have called transcriber")
}

func TestTranscribeAudio_NilTranscriber_NoNetwork(t *testing.T) {
	c, err := New(Config{Token: "t"}, silentLogger())
	require.NoError(t, err)
	msg := &discordMessage{Attachments: []discordAttachment{
		{URL: "http://should-not-hit/", ContentType: "audio/ogg"},
	}}
	assert.Empty(t, c.transcribeAudio(context.Background(), msg))
}

func TestTranscribeAudio_NonAudio_Skipped(t *testing.T) {
	ft := &fakeTranscriber{reply: "hi"}
	c, err := New(Config{Token: "t", Transcriber: ft}, silentLogger())
	require.NoError(t, err)
	msg := &discordMessage{Attachments: []discordAttachment{
		{URL: "http://irrelevant/", ContentType: "image/png"},
	}}
	assert.Empty(t, c.transcribeAudio(context.Background(), msg))
	assert.Empty(t, ft.mimetype, "transcriber should not have been called")
}

func TestTranscribeAudio_HappyPath(t *testing.T) {
	body := []byte("VOICE_BYTES")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		_, _ = w.Write(body) //nolint:errcheck // test writer
	}))
	defer srv.Close()

	ft := &fakeTranscriber{reply: "hello world"}
	c, err := New(Config{Token: "t", Transcriber: ft, HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	msg := &discordMessage{Attachments: []discordAttachment{
		{URL: srv.URL + "/att.ogg", ContentType: "audio/ogg"},
	}}
	transcript := c.transcribeAudio(context.Background(), msg)
	assert.Equal(t, "hello world", transcript)
	assert.Equal(t, body, ft.audio)
	assert.Equal(t, "audio/ogg", ft.mimetype)
}

func TestTranscribeAudio_DownloadFailure_ReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ft := &fakeTranscriber{reply: "should-not-be-returned"}
	c, err := New(Config{Token: "t", Transcriber: ft, HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	msg := &discordMessage{Attachments: []discordAttachment{
		{URL: srv.URL + "/att.ogg", ContentType: "audio/mpeg"},
	}}
	assert.Empty(t, c.transcribeAudio(context.Background(), msg))
	assert.Empty(t, ft.mimetype, "transcriber not called when download fails")
}

func TestTranscribeAudio_TranscribeFailure_ReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok")) //nolint:errcheck // test writer
	}))
	defer srv.Close()

	ft := &fakeTranscriber{err: assertingError("transcription broke")}
	c, err := New(Config{Token: "t", Transcriber: ft, HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)
	msg := &discordMessage{Attachments: []discordAttachment{
		{URL: srv.URL + "/att.ogg", ContentType: "audio/ogg"},
	}}
	assert.Empty(t, c.transcribeAudio(context.Background(), msg))
}

func TestTranscribeAudio_MaxBytesRespected(t *testing.T) {
	// Server sends 100 bytes; MaxAudioBytes caps at 10.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, 100)) //nolint:errcheck // test writer
	}))
	defer srv.Close()

	ft := &fakeTranscriber{reply: "ok"}
	c, err := New(Config{
		Token: "t", Transcriber: ft, HTTPClient: srv.Client(), MaxAudioBytes: 10,
	}, silentLogger())
	require.NoError(t, err)
	msg := &discordMessage{Attachments: []discordAttachment{
		{URL: srv.URL + "/att.ogg", ContentType: "audio/ogg"},
	}}
	_ = c.transcribeAudio(context.Background(), msg)
	assert.Len(t, ft.audio, 10, "download must be truncated to MaxAudioBytes")
}

// TestDispatch_AudioReplacesEmptyContent exercises the routing branch —
// a MESSAGE_CREATE with empty content but an audio attachment should
// deliver the transcript to the handler.
func TestDispatch_AudioReplacesEmptyContent(t *testing.T) {
	body := []byte("AUDIO")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/att."):
			_, _ = w.Write(body) //nolint:errcheck // test writer
		case strings.HasSuffix(r.URL.Path, "/messages"):
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	ft := &fakeTranscriber{reply: "hello via voice"}
	c, err := New(Config{
		Token: "t", BaseURL: srv.URL, HTTPClient: srv.Client(), Transcriber: ft,
	}, silentLogger())
	require.NoError(t, err)

	var got string
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		got = m.Body
		return "", nil
	})
	payload := discordMessage{
		ID:        "1",
		ChannelID: "ch",
		Author:    discordUser{ID: "u"},
		Attachments: []discordAttachment{
			{URL: srv.URL + "/att.ogg", ContentType: "audio/ogg"},
		},
	}
	raw, _ := json.Marshal(payload)
	frame := gatewayFrame{T: "MESSAGE_CREATE", D: raw}
	require.NoError(t, c.dispatch(context.Background(), frame, handler))
	assert.Equal(t, "hello via voice", got)
}

// assertingError is a shorthand error type (avoid importing errors just
// for one line).
type assertingError string

func (e assertingError) Error() string { return string(e) }
