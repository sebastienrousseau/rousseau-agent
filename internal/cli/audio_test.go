package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

func TestBuildAudioBackend_EmptyBackendIsNil(t *testing.T) {
	got, err := buildAudioBackend(config.MediaAudioConfig{})
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestBuildAudioBackend_WhisperCPPRequiresModelFile(t *testing.T) {
	_, err := buildAudioBackend(config.MediaAudioConfig{Backend: "whisper-cpp"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "model_file")
}

func TestBuildAudioBackend_OpenAIRequiresAPIKey(t *testing.T) {
	_, err := buildAudioBackend(config.MediaAudioConfig{Backend: "openai-api"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "api_key")
}

func TestBuildAudioBackend_UnknownBackendErrors(t *testing.T) {
	_, err := buildAudioBackend(config.MediaAudioConfig{Backend: "does-not-exist"})
	assert.Error(t, err)
}

func TestBuildAudioBackend_WhisperCPPHappyPath(t *testing.T) {
	got, err := buildAudioBackend(config.MediaAudioConfig{
		Backend:   "whisper-cpp",
		ModelFile: "/tmp/pretend-model.bin",
	})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "whisper-cpp", got.Kind())
}

func TestBuildAudioBackend_OpenAIHappyPath(t *testing.T) {
	got, err := buildAudioBackend(config.MediaAudioConfig{
		Backend: "openai-api",
		APIKey:  "sk-test",
	})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "openai-api", got.Kind())
}

func TestBuildTranscriberString_DisabledBackendReturnsNil(t *testing.T) {
	got, err := buildTranscriberString(config.MediaAudioConfig{})
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestBuildTranscriberString_HappyPath(t *testing.T) {
	got, err := buildTranscriberString(config.MediaAudioConfig{
		Backend: "openai-api",
		APIKey:  "sk-test",
	})
	require.NoError(t, err)
	require.NotNil(t, got)
}

func TestBuildTranscriberString_MisconfigPropagatesError(t *testing.T) {
	_, err := buildTranscriberString(config.MediaAudioConfig{Backend: "whisper-cpp"})
	assert.Error(t, err)
}
