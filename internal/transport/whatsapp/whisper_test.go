//go:build !no_whatsmeow

package whatsapp

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtensionFor(t *testing.T) {
	cases := map[string]string{
		"audio/ogg; codecs=opus":   ".ogg",
		"audio/opus":               ".opus",
		"audio/mp3":                ".mp3",
		"audio/mpeg":               ".mp3",
		"audio/wav":                ".wav",
		"audio/aac":                ".aac",
		"audio/m4a":                ".m4a",
		"audio/mp4":                ".m4a",
		"application/octet-stream": ".bin",
	}
	for mime, want := range cases {
		assert.Equal(t, want, extensionFor(mime), "mime %q", mime)
	}
}

func TestNewWhisperTranscriber_Defaults(t *testing.T) {
	tr := NewWhisperTranscriber(WhisperConfig{})
	assert.Equal(t, "whisper", tr.cfg.Binary)
}

func TestNewWhisperTranscriber_KeepsExplicitBinary(t *testing.T) {
	tr := NewWhisperTranscriber(WhisperConfig{Binary: "/opt/whisper-cli"})
	assert.Equal(t, "/opt/whisper-cli", tr.cfg.Binary)
}

func TestWhisperTranscriber_EmptyAudioRejected(t *testing.T) {
	tr := NewWhisperTranscriber(WhisperConfig{})
	_, err := tr.Transcribe(context.Background(), nil, "audio/ogg")
	assert.Error(t, err)
}

func TestWhisperTranscriber_MissingBinarySurfacesError(t *testing.T) {
	tr := NewWhisperTranscriber(WhisperConfig{Binary: "/nonexistent/whisper-binary"})
	_, err := tr.Transcribe(context.Background(), []byte{0x01, 0x02}, "audio/ogg")
	assert.Error(t, err)
}

// TestWhisperTranscriber_WithScriptedBinary uses a shell script that
// mimics whisper's output-file convention. Verifies the transcript
// reader works end-to-end without needing a real whisper install.
func TestWhisperTranscriber_WithScriptedBinary(t *testing.T) {
	dir := t.TempDir()
	script := dir + "/fake-whisper"
	body := `#!/bin/sh
# Grab the --output-file value and write a fake transcript there.
while [ $# -gt 0 ]; do
  case "$1" in
    --output-file) shift; out="$1" ;;
  esac
  shift
done
if [ -z "$out" ]; then
  echo "no --output-file" >&2
  exit 1
fi
printf 'the transcribed text\n' > "${out}.txt"
`
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))

	tr := NewWhisperTranscriber(WhisperConfig{Binary: script})
	got, err := tr.Transcribe(context.Background(), []byte{0x00}, "audio/ogg")
	require.NoError(t, err)
	assert.Equal(t, "the transcribed text", got)
}
