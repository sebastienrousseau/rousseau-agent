package whatsapp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WhisperConfig configures WhisperTranscriber.
type WhisperConfig struct {
	// Binary is the whisper.cpp CLI to invoke. Common names: "whisper",
	// "whisper-cli", "whisper-cpp". Empty defaults to "whisper".
	Binary string
	// Model is passed to `--model` (e.g. "base.en", "small", "medium").
	// Empty uses whisper's default model resolution.
	Model string
	// ModelPath is a filesystem path to a .bin model file, passed as
	// `--model <path>`. Takes precedence over Model.
	ModelPath string
	// Language is passed to `--language`. Empty auto-detects.
	Language string
	// ExtraArgs are appended before the input filename.
	ExtraArgs []string
}

// WhisperTranscriber shells out to whisper.cpp (or a compatible CLI)
// and returns the transcribed text. It writes the audio payload to a
// temporary file, invokes the binary with `--output-txt`, and reads
// the resulting `.txt` alongside.
type WhisperTranscriber struct {
	cfg WhisperConfig
}

// NewWhisperTranscriber constructs a WhisperTranscriber. It does not
// verify the binary exists; that check is deferred to Transcribe so
// unattended daemons surface the error in logs rather than at startup.
func NewWhisperTranscriber(cfg WhisperConfig) *WhisperTranscriber {
	if cfg.Binary == "" {
		cfg.Binary = "whisper"
	}
	return &WhisperTranscriber{cfg: cfg}
}

// Transcribe writes the audio to a temp file, runs whisper.cpp, and
// returns the plain-text transcription. mimetype is used to pick a
// sensible file extension so whisper's decoder recognises the format.
func (t *WhisperTranscriber) Transcribe(ctx context.Context, audio []byte, mimetype string) (string, error) {
	if len(audio) == 0 {
		return "", fmt.Errorf("whisper: empty audio payload")
	}

	dir, err := os.MkdirTemp("", "rousseau-whisper-*")
	if err != nil {
		return "", fmt.Errorf("whisper: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	ext := extensionFor(mimetype)
	input := filepath.Join(dir, "input"+ext)
	if err := os.WriteFile(input, audio, 0o600); err != nil {
		return "", fmt.Errorf("whisper: write audio: %w", err)
	}

	args := []string{"--output-txt", "--output-file", filepath.Join(dir, "output")}
	if t.cfg.ModelPath != "" {
		args = append(args, "--model", t.cfg.ModelPath)
	} else if t.cfg.Model != "" {
		args = append(args, "--model", t.cfg.Model)
	}
	if t.cfg.Language != "" {
		args = append(args, "--language", t.cfg.Language)
	}
	args = append(args, t.cfg.ExtraArgs...)
	args = append(args, input)

	cmd := exec.CommandContext(ctx, t.cfg.Binary, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("whisper: run %s: %w: %s", t.cfg.Binary, err, truncate(out.String(), 400))
	}

	txt, err := os.ReadFile(filepath.Join(dir, "output.txt"))
	if err != nil {
		// Fallback: whisper.cpp variants that write next to the input.
		txt2, err2 := os.ReadFile(input + ".txt")
		if err2 != nil {
			return "", fmt.Errorf("whisper: read transcript: %w (fallback: %v)", err, err2)
		}
		txt = txt2
	}
	return strings.TrimSpace(string(txt)), nil
}

func extensionFor(mimetype string) string {
	switch {
	case strings.Contains(mimetype, "ogg"):
		return ".ogg"
	case strings.Contains(mimetype, "opus"):
		return ".opus"
	case strings.Contains(mimetype, "mp3"), strings.Contains(mimetype, "mpeg"):
		return ".mp3"
	case strings.Contains(mimetype, "wav"):
		return ".wav"
	case strings.Contains(mimetype, "aac"):
		return ".aac"
	case strings.Contains(mimetype, "m4a"), strings.Contains(mimetype, "mp4"):
		return ".m4a"
	default:
		return ".bin"
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
