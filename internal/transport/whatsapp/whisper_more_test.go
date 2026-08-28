//go:build !no_whatsmeow

package whatsapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeScript drops an executable /bin/sh script in dir and returns
// its path. Used to stand in for whisper.cpp without installing it.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755))
	return path
}

// argRecorder is a script that dumps its argv, one per line, to a
// file the test can read back — so we can assert on the command line
// whisper is actually invoked with rather than on a mock.
const argRecorderBody = `
printf '%s\n' "$@" > "$ARGS_FILE"
while [ $# -gt 0 ]; do
  case "$1" in
    --output-file) shift; out="$1" ;;
  esac
  shift
done
printf 'transcript\n' > "${out}.txt"
`

func runRecorder(t *testing.T, cfg WhisperConfig, mimetype string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	cfg.Binary = writeScript(t, dir, "fake-whisper", argRecorderBody)
	t.Setenv("ARGS_FILE", argsFile)

	got, err := NewWhisperTranscriber(cfg).Transcribe(context.Background(), []byte{0x00, 0x01}, mimetype)
	require.NoError(t, err)

	raw, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	return got, strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func TestWhisperTranscriber_ModelPathWinsOverModel(t *testing.T) {
	got, args := runRecorder(t, WhisperConfig{Model: "base.en", ModelPath: "/models/small.bin"}, "audio/ogg")
	assert.Equal(t, "transcript", got)
	assert.Contains(t, args, "/models/small.bin")
	assert.NotContains(t, args, "base.en")
}

func TestWhisperTranscriber_ModelNameUsedWhenNoPath(t *testing.T) {
	_, args := runRecorder(t, WhisperConfig{Model: "base.en"}, "audio/ogg")
	assert.Contains(t, args, "--model")
	assert.Contains(t, args, "base.en")
}

func TestWhisperTranscriber_LanguageAndExtraArgsForwarded(t *testing.T) {
	_, args := runRecorder(t, WhisperConfig{Language: "en", ExtraArgs: []string{"--threads", "2"}}, "audio/wav")
	assert.Contains(t, args, "--language")
	assert.Contains(t, args, "en")
	assert.Contains(t, args, "--threads")
	// The input file is always last, and its extension is derived
	// from the mimetype.
	assert.True(t, strings.HasSuffix(args[len(args)-1], ".wav"), "got %q", args[len(args)-1])
}

func TestWhisperTranscriber_NoModelFlagWhenUnconfigured(t *testing.T) {
	_, args := runRecorder(t, WhisperConfig{}, "audio/ogg")
	assert.NotContains(t, args, "--model")
	assert.NotContains(t, args, "--language")
}

// TestWhisperTranscriber_FallsBackToTranscriptNextToInput covers the
// whisper.cpp variants that ignore --output-file and write
// "<input>.txt" instead.
func TestWhisperTranscriber_FallsBackToTranscriptNextToInput(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "fake-whisper", `
for last; do :; done
printf '  written beside the input  \n' > "${last}.txt"
`)
	got, err := NewWhisperTranscriber(WhisperConfig{Binary: script}).
		Transcribe(context.Background(), []byte{0x00}, "audio/ogg")
	require.NoError(t, err)
	assert.Equal(t, "written beside the input", got)
}

// TestWhisperTranscriber_NoTranscriptAnywhereErrors: the binary exits
// 0 but produces nothing, so both read attempts fail and the joined
// error is surfaced.
func TestWhisperTranscriber_NoTranscriptAnywhereErrors(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "silent-whisper", "exit 0\n")
	_, err := NewWhisperTranscriber(WhisperConfig{Binary: script}).
		Transcribe(context.Background(), []byte{0x00}, "audio/ogg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "whisper: read transcript")
}

// TestWhisperTranscriber_NonZeroExitIncludesTruncatedOutput checks
// that a failing binary's diagnostics reach the caller and that
// runaway output is capped rather than logged in full.
func TestWhisperTranscriber_NonZeroExitIncludesTruncatedOutput(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "angry-whisper", `
i=0
while [ $i -lt 100 ]; do printf 'ggml error line %d\n' "$i"; i=$((i+1)); done >&2
exit 3
`)
	_, err := NewWhisperTranscriber(WhisperConfig{Binary: script}).
		Transcribe(context.Background(), []byte{0x00}, "audio/ogg")
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "whisper: run")
	assert.Contains(t, msg, "ggml error line 0")
	assert.Contains(t, msg, "…", "long stderr must be truncated")
	assert.Less(t, len(msg), 600)
}

// TestWhisperTranscriber_UnusableTempDirErrors points TMPDIR at a
// path that does not exist, so os.MkdirTemp fails before any work.
func TestWhisperTranscriber_UnusableTempDirErrors(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does", "not", "exist"))
	_, err := NewWhisperTranscriber(WhisperConfig{Binary: "/bin/true"}).
		Transcribe(context.Background(), []byte{0x00}, "audio/ogg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "whisper: temp dir")
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "abc", truncate("abc", 5))
	assert.Equal(t, "abc", truncate("abc", 3))
	assert.Equal(t, "ab…", truncate("abcd", 2))
}
