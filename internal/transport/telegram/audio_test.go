package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// fakeTranscriber records the (audio, mimetype) it was called with
// so the test can assert both.
type fakeTranscriber struct {
	got      []byte
	mimetype string
	reply    string
	err      error
	called   int
}

func (f *fakeTranscriber) Transcribe(_ context.Context, audio []byte, mimetype string) (string, error) {
	f.called++
	f.got = append([]byte(nil), audio...)
	f.mimetype = mimetype
	if f.err != nil {
		return "", f.err
	}
	return f.reply, nil
}

// telegramTestServer stands in for api.telegram.org: answers
// getFile with a canned path, then serves that path as the file body.
func telegramTestServer(t *testing.T, filePath string, body []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{"result": map[string]any{"file_path": filePath}}
			blob, _ := json.Marshal(resp) //nolint:errcheck // static test payload
			_, _ = w.Write(blob)          //nolint:errcheck // test writer
			return
		}
		if strings.Contains(r.URL.Path, "/file/bot") && strings.HasSuffix(r.URL.Path, "/"+filePath) {
			w.Header().Set("Content-Type", "audio/ogg")
			_, _ = w.Write(body) //nolint:errcheck // test writer
			return
		}
		http.NotFound(w, r)
	}))
}

// captureHandler stores every message it receives so tests can inspect them.
type captureHandler struct {
	msgs []transport.IncomingMessage
}

func (c *captureHandler) Handle(_ context.Context, msg transport.IncomingMessage) (string, error) {
	c.msgs = append(c.msgs, msg)
	return "", nil
}

// silentLogger is declared in client_test.go; reuse it here.

func TestTranscribeAudio_VoiceMessageProducesText(t *testing.T) {
	audioBytes := []byte("fake-opus-payload")
	srv := telegramTestServer(t, "voice/foo.oga", audioBytes)
	defer srv.Close()

	ft := &fakeTranscriber{reply: "hello world"}
	c, err := New(Config{
		Token:       "test",
		BaseURL:     srv.URL,
		Transcriber: ft,
	}, silentLogger())
	require.NoError(t, err)

	msg := &telegramMessage{
		Chat:  telegramChat{ID: 42},
		Date:  time.Now().Unix(),
		Voice: &telegramVoice{FileID: "voice-123", MimeType: "audio/ogg"},
	}
	text := c.transcribeAudio(context.Background(), msg)
	assert.Equal(t, "hello world", text)
	assert.Equal(t, 1, ft.called)
	assert.Equal(t, audioBytes, ft.got)
	assert.Equal(t, "audio/ogg", ft.mimetype)
}

func TestTranscribeAudio_AudioMessageProducesText(t *testing.T) {
	srv := telegramTestServer(t, "music/x.mp3", []byte("fake-mp3"))
	defer srv.Close()

	ft := &fakeTranscriber{reply: "song lyrics"}
	c, err := New(Config{Token: "t", BaseURL: srv.URL, Transcriber: ft}, silentLogger())
	require.NoError(t, err)

	msg := &telegramMessage{
		Chat:  telegramChat{ID: 42},
		Audio: &telegramAudio{FileID: "audio-456", MimeType: "audio/mpeg"},
	}
	text := c.transcribeAudio(context.Background(), msg)
	assert.Equal(t, "song lyrics", text)
	assert.Equal(t, "audio/mpeg", ft.mimetype)
}

func TestTranscribeAudio_NilTranscriberReturnsEmpty(t *testing.T) {
	c, err := New(Config{Token: "t"}, silentLogger())
	require.NoError(t, err)
	msg := &telegramMessage{Voice: &telegramVoice{FileID: "x"}}
	assert.Equal(t, "", c.transcribeAudio(context.Background(), msg))
}

func TestTranscribeAudio_NoAudioReturnsEmpty(t *testing.T) {
	c, err := New(Config{Token: "t", Transcriber: &fakeTranscriber{reply: "x"}}, silentLogger())
	require.NoError(t, err)
	// Message with neither Voice nor Audio.
	msg := &telegramMessage{Chat: telegramChat{ID: 1}}
	assert.Equal(t, "", c.transcribeAudio(context.Background(), msg))
}

func TestTranscribeAudio_EmptyFileIDReturnsEmpty(t *testing.T) {
	c, err := New(Config{Token: "t", Transcriber: &fakeTranscriber{reply: "x"}}, silentLogger())
	require.NoError(t, err)
	msg := &telegramMessage{Voice: &telegramVoice{FileID: ""}}
	assert.Equal(t, "", c.transcribeAudio(context.Background(), msg))
}

func TestTranscribeAudio_DownloadFailureIsSwallowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ft := &fakeTranscriber{reply: "unreached"}
	c, err := New(Config{Token: "t", BaseURL: srv.URL, Transcriber: ft}, silentLogger())
	require.NoError(t, err)
	msg := &telegramMessage{Voice: &telegramVoice{FileID: "x"}}
	assert.Equal(t, "", c.transcribeAudio(context.Background(), msg))
	assert.Equal(t, 0, ft.called, "transcriber must not be called when download fails")
}

func TestTranscribeAudio_TranscriberErrorIsSwallowed(t *testing.T) {
	srv := telegramTestServer(t, "voice/y.oga", []byte("fake"))
	defer srv.Close()

	c, err := New(Config{
		Token:       "t",
		BaseURL:     srv.URL,
		Transcriber: &fakeTranscriber{err: io.ErrUnexpectedEOF},
	}, silentLogger())
	require.NoError(t, err)
	msg := &telegramMessage{Voice: &telegramVoice{FileID: "x"}}
	assert.Equal(t, "", c.transcribeAudio(context.Background(), msg))
}

func TestTranscribeAudio_DefaultsToOggMimeWhenUnset(t *testing.T) {
	// Neither Voice.MimeType nor the download's Content-Type set —
	// code should default to audio/ogg (Telegram voice notes are Opus).
	// We force the download response's Content-Type to be empty by
	// explicitly clearing it — httptest otherwise sniffs the body.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			blob, _ := json.Marshal(map[string]any{"result": map[string]any{"file_path": "voice/z.oga"}}) //nolint:errcheck // static test payload
			_, _ = w.Write(blob)                                                                          //nolint:errcheck // test writer
			return
		}
		w.Header()["Content-Type"] = nil // explicit: emit no CT header
		_, _ = w.Write([]byte("data"))   //nolint:errcheck // test writer
	}))
	defer srv.Close()

	ft := &fakeTranscriber{reply: "ok"}
	c, err := New(Config{Token: "t", BaseURL: srv.URL, Transcriber: ft}, silentLogger())
	require.NoError(t, err)
	msg := &telegramMessage{Voice: &telegramVoice{FileID: "x"}}
	_ = c.transcribeAudio(context.Background(), msg)
	assert.Equal(t, "audio/ogg", ft.mimetype)
}

func TestRoute_AudioMessageEndsUpAtHandler(t *testing.T) {
	srv := telegramTestServer(t, "voice/z.oga", []byte("blob"))
	defer srv.Close()

	ft := &fakeTranscriber{reply: "transcript"}
	c, err := New(Config{Token: "t", BaseURL: srv.URL, Transcriber: ft}, silentLogger())
	require.NoError(t, err)

	h := &captureHandler{}
	c.route(context.Background(), telegramUpdate{
		UpdateID: 1,
		Message: &telegramMessage{
			Chat:  telegramChat{ID: 99},
			Date:  time.Now().Unix(),
			Voice: &telegramVoice{FileID: "vf"},
		},
	}, h)
	require.Len(t, h.msgs, 1)
	assert.Equal(t, "transcript", h.msgs[0].Body)
	assert.Equal(t, "99", h.msgs[0].From)
}

// Ensure Bot API URL construction works for the download endpoint —
// verifies we hit /file/bot<token>/<path>, not the JSON API.
func TestDownloadFile_UsesFileEndpoint(t *testing.T) {
	seen := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			blob, _ := json.Marshal(map[string]any{"result": map[string]any{"file_path": "documents/x.bin"}}) //nolint:errcheck // static test payload
			_, _ = w.Write(blob)                                                                              //nolint:errcheck // test writer
			return
		}
		_, _ = w.Write([]byte("body")) //nolint:errcheck // test writer
	}))
	defer srv.Close()

	c, err := New(Config{Token: "T", BaseURL: srv.URL}, silentLogger())
	require.NoError(t, err)

	body, _, err := c.downloadFile(context.Background(), "id")
	require.NoError(t, err)
	assert.Equal(t, []byte("body"), body)
	require.GreaterOrEqual(t, len(seen), 2)
	assert.Contains(t, seen[0], "/getFile")
	assert.Contains(t, seen[1], "/file/bot")
	assert.Contains(t, seen[1], "documents/x.bin")
}
