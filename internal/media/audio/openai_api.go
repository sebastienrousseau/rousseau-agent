package audio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// OpenAIAPI calls OpenAI's /v1/audio/transcriptions endpoint with a
// Whisper model. Best for high-accented / noisy audio; requires an
// API key and outbound network access to api.openai.com (or the
// configured BaseURL for OpenAI-compatible providers).
type OpenAIAPI struct {
	APIKey  string
	BaseURL string // defaults to https://api.openai.com/v1
	Model   string // defaults to "whisper-1"
	// Language is the ISO 639-1 hint sent as the `language` form
	// field. Empty lets the model detect.
	Language string
	// Timeout bounds a single request. Zero uses 90 seconds.
	Timeout time.Duration
	// MaxBytes caps the audio blob size. Zero uses OpenAI's own
	// documented ceiling (25 MiB).
	MaxBytes int
	// HTTPClient is injectable for tests. Nil uses http.DefaultClient
	// with the Timeout applied per-request via ctx.
	HTTPClient *http.Client
}

// Kind returns "openai-api".
func (*OpenAIAPI) Kind() string { return "openai-api" }

// Transcribe uploads audio to OpenAI's transcription endpoint and
// returns the parsed text.
func (o *OpenAIAPI) Transcribe(ctx context.Context, audio []byte, mimetype string) (Result, error) {
	if !KnownVoiceNoteMimeType(mimetype) {
		return Result{}, fmt.Errorf("%w: %s", ErrUnsupportedMimeType, mimetype)
	}
	if o.APIKey == "" {
		return Result{}, errors.New("audio/openai: APIKey is required")
	}
	maxBytes := o.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 25 * 1024 * 1024
	}
	if len(audio) > maxBytes {
		return Result{}, ErrTooLarge
	}

	base := o.BaseURL
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	model := o.Model
	if model == "" {
		model = "whisper-1"
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var buf bytes.Buffer
	contentType, err := writeTranscriptionForm(&buf, audio, mimetype, model, o.Language)
	if err != nil {
		return Result{}, err
	}

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, strings.TrimRight(base, "/")+"/audio/transcriptions", &buf)
	if err != nil {
		return Result{}, fmt.Errorf("audio/openai: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+o.APIKey)
	req.Header.Set("Content-Type", contentType)

	start := time.Now()
	client := o.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("audio/openai: do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	elapsed := time.Since(start)

	if resp.StatusCode >= 500 {
		return Result{}, fmt.Errorf("%w: openai returned %d", ErrBackendUnavailable, resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096)) //nolint:errcheck // best-effort error context
		return Result{}, fmt.Errorf("audio/openai: %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		Text     string `json:"text"`
		Language string `json:"language"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Result{}, fmt.Errorf("audio/openai: decode: %w", err)
	}
	lang := payload.Language
	if lang == "" {
		lang = o.Language
	}
	return Result{
		Text:     strings.TrimSpace(payload.Text),
		Language: lang,
		Duration: elapsed,
	}, nil
}

// writeTranscriptionForm builds the multipart body for OpenAI's
// /audio/transcriptions endpoint into w and returns the Content-Type
// header (which carries the generated boundary).
//
// This is a separate function rather than inline in Transcribe for two
// reasons. It separates body construction from HTTP concerns, which is
// worth doing on its own merits. And it takes an io.Writer rather than
// owning a bytes.Buffer, which makes the write-failure paths reachable
// from tests -- against a bytes.Buffer sink none of these errors can
// ever fire, so inline they were six permanently-dead branches that no
// test could legitimately exercise.
func writeTranscriptionForm(w io.Writer, audio []byte, mimetype, model, language string) (string, error) {
	mw := multipart.NewWriter(w)

	fw, err := mw.CreateFormFile("file", "voice"+extensionForMime(mimetype))
	if err != nil {
		return "", fmt.Errorf("audio/openai: create file field: %w", err)
	}
	if _, err := io.Copy(fw, bytes.NewReader(audio)); err != nil {
		return "", fmt.Errorf("audio/openai: copy audio: %w", err)
	}
	if err := mw.WriteField("model", model); err != nil {
		return "", fmt.Errorf("audio/openai: model field: %w", err)
	}
	if err := mw.WriteField("response_format", "json"); err != nil {
		return "", fmt.Errorf("audio/openai: format field: %w", err)
	}
	if language != "" {
		if err := mw.WriteField("language", language); err != nil {
			return "", fmt.Errorf("audio/openai: language field: %w", err)
		}
	}
	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("audio/openai: close multipart: %w", err)
	}
	return mw.FormDataContentType(), nil
}
