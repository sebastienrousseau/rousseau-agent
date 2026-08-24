package audio_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/media/audio"
)

func TestKnownVoiceNoteMimeType(t *testing.T) {
	// Whitelist — every case here should return true.
	whitelist := []string{
		"audio/ogg", "audio/opus", "audio/ogg; codecs=opus", "audio/OGG",
		"audio/mpeg", "audio/mp3",
		"audio/mp4", "audio/x-m4a", "audio/aac",
		"audio/wav", "audio/x-wav",
		"audio/webm", "audio/flac",
	}
	for _, m := range whitelist {
		assert.Truef(t, audio.KnownVoiceNoteMimeType(m), "%q should be recognised", m)
	}
	// Blacklist — none of these are voice notes.
	rejects := []string{
		"", "text/plain", "video/mp4", "application/octet-stream",
	}
	for _, m := range rejects {
		assert.Falsef(t, audio.KnownVoiceNoteMimeType(m), "%q should NOT be recognised", m)
	}
}

func TestNoop_Transcribe(t *testing.T) {
	n := &audio.Noop{Text: "canned transcript", Language: "en"}
	res, err := n.Transcribe(context.Background(), []byte("ignored"), "audio/ogg")
	require.NoError(t, err)
	assert.Equal(t, "canned transcript", res.Text)
	assert.Equal(t, "en", res.Language)
	assert.Equal(t, "noop", n.Kind())
}

func TestWhisperCPP_MissingModelFileErrors(t *testing.T) {
	w := &audio.WhisperCPP{ModelFile: "/does/not/exist.bin"}
	_, err := w.Transcribe(context.Background(), []byte{0x00}, "audio/ogg")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "model")
}

func TestWhisperCPP_UnsupportedMimeType(t *testing.T) {
	w := &audio.WhisperCPP{ModelFile: "/tmp/model.bin"}
	_, err := w.Transcribe(context.Background(), []byte{0x00}, "video/mp4")
	assert.ErrorIs(t, err, audio.ErrUnsupportedMimeType)
}

func TestWhisperCPP_TooLarge(t *testing.T) {
	w := &audio.WhisperCPP{ModelFile: "/tmp/model.bin", MaxBytes: 10}
	_, err := w.Transcribe(context.Background(), make([]byte, 100), "audio/ogg")
	assert.ErrorIs(t, err, audio.ErrTooLarge)
}

func TestOpenAIAPI_MissingKeyErrors(t *testing.T) {
	o := &audio.OpenAIAPI{}
	_, err := o.Transcribe(context.Background(), []byte{0x00}, "audio/ogg")
	assert.Error(t, err)
}

func TestOpenAIAPI_SuccessRoundtrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/audio/transcriptions", r.URL.Path)
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		assert.True(t, strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data"))
		_ = r.ParseMultipartForm(1 << 20) //nolint:errcheck // best-effort test parse

		body, err := json.Marshal(map[string]string{
			"text":     "hello world",
			"language": "en",
		})
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body) //nolint:errcheck // test response write
	}))
	defer srv.Close()

	o := &audio.OpenAIAPI{APIKey: "test-key", BaseURL: srv.URL}
	res, err := o.Transcribe(context.Background(), []byte("fake-audio"), "audio/ogg")
	require.NoError(t, err)
	assert.Equal(t, "hello world", res.Text)
	assert.Equal(t, "en", res.Language)
}

func TestOpenAIAPI_5xxIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	o := &audio.OpenAIAPI{APIKey: "k", BaseURL: srv.URL}
	_, err := o.Transcribe(context.Background(), []byte{0x00}, "audio/ogg")
	assert.ErrorIs(t, err, audio.ErrBackendUnavailable)
}

func TestOpenAIAPI_4xxReturnsBodyInError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad"}}`)) //nolint:errcheck // test response write
	}))
	defer srv.Close()

	o := &audio.OpenAIAPI{APIKey: "k", BaseURL: srv.URL}
	_, err := o.Transcribe(context.Background(), []byte{0x00}, "audio/ogg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad")
}

func TestOpenAIAPI_TooLarge(t *testing.T) {
	o := &audio.OpenAIAPI{APIKey: "k", MaxBytes: 10}
	_, err := o.Transcribe(context.Background(), make([]byte, 100), "audio/ogg")
	assert.ErrorIs(t, err, audio.ErrTooLarge)
}

func TestOpenAIAPI_UnsupportedMimeType(t *testing.T) {
	o := &audio.OpenAIAPI{APIKey: "k"}
	_, err := o.Transcribe(context.Background(), []byte{0x00}, "video/mp4")
	assert.ErrorIs(t, err, audio.ErrUnsupportedMimeType)
}
