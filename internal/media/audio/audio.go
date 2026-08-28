// Package audio transcribes voice-note audio to text. Two backends:
//
//   - [WhisperCPP] — shells out to a locally installed whisper.cpp
//     binary. No network egress; models baked into the container
//     image. Best for compliance-constrained deployments.
//   - [OpenAIAPI] — calls OpenAI's /v1/audio/transcriptions endpoint
//     with a Whisper model. Highest quality per dollar for
//     accented/noisy audio; requires OPENAI_API_KEY and outbound
//     access.
//   - [Noop] — a deterministic stub for tests.
//
// The transport adapters (WhatsApp, Telegram, iMessage, Signal) call
// [Transcribe] when they see an inbound audio message; the returned
// text is fed into the agent as if the user had typed it, with a
// "[transcribed from Ns voice note]" attribution appended.
//
// This package intentionally does not know about any transport —
// callers hand it a byte slice + mimetype and get text back.
package audio

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Backend transcribes audio bytes to text.
type Backend interface {
	// Kind identifies the backend for metrics and logs
	// ("whisper-cpp", "openai-api", "noop").
	Kind() string
	// Transcribe returns text for the given audio blob. mimetype is
	// the container's declared content type (e.g. "audio/ogg;
	// codecs=opus" for WhatsApp voice notes).
	Transcribe(ctx context.Context, audio []byte, mimetype string) (Result, error)
}

// Result carries the transcript plus provenance metadata.
type Result struct {
	// Text is the transcribed text with leading/trailing whitespace
	// trimmed. Empty when the audio was silence or the backend
	// couldn't produce anything.
	Text string
	// Language is the ISO 639-1 code the backend detected (or the
	// operator-supplied hint that was used). Empty when the backend
	// doesn't report language.
	Language string
	// Duration is how much audio was transcribed.
	Duration time.Duration
}

// ErrUnsupportedMimeType is returned when the backend recognises the
// mime type but declines to transcribe (e.g. some backends don't do
// video-with-audio; audio-only tracks must be pre-extracted).
var ErrUnsupportedMimeType = errors.New("audio: mimetype not supported by backend")

// ErrTooLarge is returned when audio exceeds the configured per-call
// size or duration ceiling.
var ErrTooLarge = errors.New("audio: exceeds size/duration cap")

// ErrBackendUnavailable is returned when a backend that requires an
// external binary (whisper-cpp) doesn't find it on $PATH, or a
// network backend gets a 5xx-shaped failure. Callers should fall
// back to another backend or drop the transcript silently.
var ErrBackendUnavailable = errors.New("audio: backend runtime unavailable")

// KnownVoiceNoteMimeType returns true for the mime types transports
// typically hand us as voice notes. Callers use this to short-circuit
// before spending resources on backend calls that will just
// ErrUnsupportedMimeType.
func KnownVoiceNoteMimeType(mimetype string) bool {
	if mimetype == "" {
		return false
	}
	base := strings.ToLower(strings.TrimSpace(mimetype))
	if i := strings.IndexAny(base, ";"); i > 0 {
		base = strings.TrimSpace(base[:i])
	}
	switch base {
	case "audio/ogg", "audio/opus", "audio/ogg; codecs=opus",
		"audio/mpeg", "audio/mp3", "audio/mp4",
		"audio/wav", "audio/x-wav", "audio/wave",
		"audio/webm", "audio/flac", "audio/x-m4a", "audio/aac":
		return true
	}
	return false
}
