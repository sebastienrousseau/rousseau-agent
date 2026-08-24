package audio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// WhisperCPP shells out to a locally installed whisper.cpp binary.
// Prefer this backend for compliance-constrained deployments where
// audio must not leave the daemon's network namespace.
//
// The container image should include:
//   - the `whisper-cli` (or `main`) binary from ggerganov/whisper.cpp
//   - a model file (ggml-base.en.bin, ggml-small.en.bin, …)
//
// See docs/voice.md for the Dockerfile snippet.
type WhisperCPP struct {
	// Binary is the whisper.cpp executable. Empty resolves via $PATH
	// (tries "whisper-cli" then "whisper" then "main" — historical
	// names).
	Binary string
	// ModelFile is the ggml-*.bin model. Required.
	ModelFile string
	// Language is the ISO 639-1 hint sent with -l. Empty lets
	// whisper.cpp auto-detect.
	Language string
	// Timeout bounds a single transcription. Zero uses 60 seconds
	// (voice notes up to ~2 minutes at real time on modest hardware).
	Timeout time.Duration
	// MaxBytes caps the input audio. Zero uses 16 MiB (roughly 30
	// minutes of Opus at 96kbps).
	MaxBytes int
}

// Kind returns "whisper-cpp".
func (*WhisperCPP) Kind() string { return "whisper-cpp" }

// Transcribe writes the audio to a temp file, invokes whisper.cpp,
// and returns the concatenated output.
func (w *WhisperCPP) Transcribe(ctx context.Context, audio []byte, mimetype string) (Result, error) {
	if !KnownVoiceNoteMimeType(mimetype) {
		return Result{}, fmt.Errorf("%w: %s", ErrUnsupportedMimeType, mimetype)
	}
	maxBytes := w.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 16 * 1024 * 1024
	}
	if len(audio) > maxBytes {
		return Result{}, ErrTooLarge
	}
	if w.ModelFile == "" {
		return Result{}, errors.New("audio/whisper-cpp: ModelFile is required")
	}
	if _, err := os.Stat(w.ModelFile); err != nil {
		return Result{}, fmt.Errorf("audio/whisper-cpp: model: %w", err)
	}
	bin := w.Binary
	if bin == "" {
		for _, candidate := range []string{"whisper-cli", "whisper", "main"} {
			if p, err := exec.LookPath(candidate); err == nil {
				bin = p
				break
			}
		}
	} else if _, err := exec.LookPath(bin); err != nil {
		// Explicit binary that doesn't exist on PATH — treat the
		// same as "not installed" so callers can fall back to
		// another backend rather than surface a raw exec error.
		return Result{}, ErrBackendUnavailable
	}
	if bin == "" {
		return Result{}, ErrBackendUnavailable
	}

	timeout := w.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Stage audio to a temp file — whisper.cpp reads from a file, not stdin.
	tmp, err := os.CreateTemp("", "rousseau-audio-*"+extensionForMime(mimetype))
	if err != nil {
		return Result{}, fmt.Errorf("audio/whisper-cpp: tempfile: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() //nolint:errcheck // best-effort cleanup
	if _, err := tmp.Write(audio); err != nil {
		_ = tmp.Close() //nolint:errcheck // best-effort cleanup
		return Result{}, fmt.Errorf("audio/whisper-cpp: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Result{}, fmt.Errorf("audio/whisper-cpp: close: %w", err)
	}

	args := []string{
		"-m", w.ModelFile,
		"-f", tmp.Name(),
		"-otxt",     // text output only (no timestamps)
		"-nt",       // no timestamps in output
		"-np",       // no progress output
	}
	if w.Language != "" {
		args = append(args, "-l", w.Language)
	}

	// #nosec G204 -- operator-provided binary path.
	cmd := exec.CommandContext(callCtx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("audio/whisper-cpp: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	elapsed := time.Since(start)

	// whisper.cpp -otxt writes to <infile>.txt alongside the input.
	txtPath := tmp.Name() + ".txt"
	defer func() { _ = os.Remove(txtPath) }() //nolint:errcheck // best-effort cleanup
	if data, err := os.ReadFile(txtPath); err == nil && len(data) > 0 {
		return Result{
			Text:     strings.TrimSpace(string(data)),
			Language: w.Language,
			Duration: elapsed,
		}, nil
	}
	// Fallback: some builds emit to stdout.
	return Result{
		Text:     strings.TrimSpace(stdout.String()),
		Language: w.Language,
		Duration: elapsed,
	}, nil
}

func extensionForMime(mime string) string {
	base := strings.ToLower(strings.TrimSpace(mime))
	if i := strings.IndexAny(base, ";"); i > 0 {
		base = strings.TrimSpace(base[:i])
	}
	switch base {
	case "audio/ogg", "audio/opus", "audio/ogg; codecs=opus":
		return ".ogg"
	case "audio/mp4", "audio/x-m4a", "audio/aac":
		return ".m4a"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return ".wav"
	case "audio/webm":
		return ".webm"
	case "audio/flac":
		return ".flac"
	}
	return filepath.Ext(base) // best-effort last-resort
}
