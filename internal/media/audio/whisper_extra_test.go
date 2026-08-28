package audio_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/media/audio"
)

// The full WhisperCPP.Transcribe path can't be exercised without a
// real whisper binary + model in CI, but the config-error branches
// are all reachable and worth pinning.

func TestWhisperCPP_MissingBinaryReturnsUnavailable(t *testing.T) {
	tmp := t.TempDir()
	// Create a fake model file so the ModelFile check passes; the
	// binary lookup then fails and returns ErrBackendUnavailable.
	model := filepath.Join(tmp, "ggml.bin")
	require.NoError(t, os.WriteFile(model, []byte("fake"), 0o600))

	w := &audio.WhisperCPP{
		Binary:    "totally-not-a-real-binary-xxxyyy-2626",
		ModelFile: model,
	}
	_, err := w.Transcribe(context.Background(), []byte{0x00}, "audio/ogg")
	assert.ErrorIs(t, err, audio.ErrBackendUnavailable)
}

// extensionForMime is unexported but covered indirectly. We can pin
// its behaviour through Transcribe by observing the tempfile name in
// error output — for now, just exercise the diverse mime paths.
func TestWhisperCPP_VariousMimeTypesAcceptedByFilter(t *testing.T) {
	// If the mime type is accepted, the code proceeds to model-file
	// validation. All the whitelist entries should pass the filter.
	for _, m := range []string{
		"audio/ogg", "audio/opus", "audio/mpeg", "audio/mp3",
		"audio/mp4", "audio/wav", "audio/webm", "audio/flac",
		"audio/x-m4a", "audio/aac",
	} {
		w := &audio.WhisperCPP{}
		_, err := w.Transcribe(context.Background(), []byte{0x00}, m)
		// ModelFile is empty → error mentions "required", NOT the
		// mimetype-unsupported sentinel.
		assert.NotErrorIsf(t, err, audio.ErrUnsupportedMimeType,
			"mime %q should pass the filter", m)
	}
}
