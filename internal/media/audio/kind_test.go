package audio_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/media/audio"
)

// The Kind() methods are trivial but part of the public interface —
// their strings appear as Prometheus label values, so pin them.

func TestWhisperCPP_Kind(t *testing.T) {
	assert.Equal(t, "whisper-cpp", (&audio.WhisperCPP{}).Kind())
}

func TestOpenAIAPI_Kind(t *testing.T) {
	assert.Equal(t, "openai-api", (&audio.OpenAIAPI{}).Kind())
}

func TestNoop_Kind(t *testing.T) {
	assert.Equal(t, "noop", (&audio.Noop{}).Kind())
}
