package audio_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/media/audio"
)

// stubRoundTripper answers every request without touching the
// network. It records the last request so tests can assert on the URL
// the backend chose (including the api.openai.com default).
type stubRoundTripper struct {
	err  error
	resp *http.Response
	last *http.Request
}

func (s *stubRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	// Drain the body so multipart assembly is genuinely exercised.
	body, _ := io.ReadAll(r.Body) //nolint:errcheck // best-effort capture
	clone := r.Clone(r.Context())
	clone.Body = io.NopCloser(bytes.NewReader(body))
	s.last = clone
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestOpenAIAPI_DefaultsToOfficialEndpointAndModel(t *testing.T) {
	rt := &stubRoundTripper{resp: jsonResponse(http.StatusOK, `{"text":"ok"}`)}

	o := &audio.OpenAIAPI{APIKey: "k", HTTPClient: &http.Client{Transport: rt}}
	res, err := o.Transcribe(context.Background(), []byte("aud"), "audio/ogg")
	require.NoError(t, err)
	assert.Equal(t, "ok", res.Text)

	require.NotNil(t, rt.last)
	assert.Equal(t, "https://api.openai.com/v1/audio/transcriptions", rt.last.URL.String())

	fields := parseMultipart(t, rt.last)
	assert.Equal(t, "whisper-1", fields["model"], "model defaults to whisper-1")
	assert.Equal(t, "json", fields["response_format"])
	assert.Equal(t, "aud", fields["file"], "audio bytes must reach the wire")
	assert.NotContains(t, fields, "language", "no hint configured → no language field")
}

func TestOpenAIAPI_SendsLanguageHintAndCustomModel(t *testing.T) {
	rt := &stubRoundTripper{resp: jsonResponse(http.StatusOK, `{"text":"salut"}`)}

	o := &audio.OpenAIAPI{
		APIKey:     "k",
		BaseURL:    "https://example.invalid/v1/", // trailing slash must be trimmed
		Model:      "gpt-4o-transcribe",
		Language:   "fr",
		HTTPClient: &http.Client{Transport: rt},
	}
	res, err := o.Transcribe(context.Background(), []byte("aud"), "audio/mp4")
	require.NoError(t, err)
	assert.Equal(t, "salut", res.Text)
	// Response carried no language → the configured hint is echoed back.
	assert.Equal(t, "fr", res.Language)

	assert.Equal(t, "https://example.invalid/v1/audio/transcriptions", rt.last.URL.String())
	fields := parseMultipart(t, rt.last)
	assert.Equal(t, "gpt-4o-transcribe", fields["model"])
	assert.Equal(t, "fr", fields["language"])
}

func TestOpenAIAPI_ResponseLanguageWinsOverHint(t *testing.T) {
	rt := &stubRoundTripper{resp: jsonResponse(http.StatusOK, `{"text":"hola","language":"es"}`)}
	o := &audio.OpenAIAPI{APIKey: "k", Language: "fr", HTTPClient: &http.Client{Transport: rt}}
	res, err := o.Transcribe(context.Background(), []byte("aud"), "audio/ogg")
	require.NoError(t, err)
	assert.Equal(t, "es", res.Language)
}

func TestOpenAIAPI_TransportErrorIsWrapped(t *testing.T) {
	sentinel := errors.New("dial tcp: connection refused")
	rt := &stubRoundTripper{err: sentinel}

	o := &audio.OpenAIAPI{APIKey: "k", HTTPClient: &http.Client{Transport: rt}}
	_, err := o.Transcribe(context.Background(), []byte("aud"), "audio/ogg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "audio/openai: do:")
	assert.ErrorIs(t, err, sentinel)
}

func TestOpenAIAPI_MalformedJSONBodyErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json at all")) //nolint:errcheck // test response write
	}))
	defer srv.Close()

	o := &audio.OpenAIAPI{APIKey: "k", BaseURL: srv.URL}
	_, err := o.Transcribe(context.Background(), []byte("aud"), "audio/ogg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode")
}

func TestOpenAIAPI_InvalidBaseURLFailsRequestConstruction(t *testing.T) {
	// A control character makes net/url reject the URL before any
	// socket is opened.
	o := &audio.OpenAIAPI{APIKey: "k", BaseURL: "http://example.invalid/\x7f"}
	_, err := o.Transcribe(context.Background(), []byte("aud"), "audio/ogg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "new request")
}

// parseMultipart decodes the request body the backend built and
// returns a name→value map (the file part is keyed "file").
func parseMultipart(t *testing.T, r *http.Request) map[string]string {
	t.Helper()
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	require.NoError(t, err)
	mr := multipart.NewReader(r.Body, params["boundary"])

	out := map[string]string{}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		data, err := io.ReadAll(part)
		require.NoError(t, err)
		out[part.FormName()] = string(data)
		if part.FormName() == "file" {
			assert.Truef(t, strings.HasPrefix(part.FileName(), "voice."),
				"file part should be named voice.<ext>, got %q", part.FileName())
		}
	}
	return out
}
