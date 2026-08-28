package cli

import (
	"fmt"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/media/audio"
)

// buildAudioBackend translates config.MediaAudioConfig into an
// [audio.Backend]. Returns nil for the "disabled" case (empty backend
// name) so transports see a nil Transcriber and skip audio messages
// entirely.
//
// Returns an error only when the config is internally inconsistent
// (backend named but its required fields missing) — that's an
// operator-visible misconfiguration worth surfacing at daemon start,
// not swallowing.
func buildAudioBackend(cfg config.MediaAudioConfig) (audio.Backend, error) {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	switch cfg.Backend {
	case "":
		return nil, nil
	case "whisper-cpp":
		if cfg.ModelFile == "" {
			return nil, fmt.Errorf("media.audio.backend=whisper-cpp requires model_file")
		}
		return &audio.WhisperCPP{
			Binary:    cfg.Binary,
			ModelFile: cfg.ModelFile,
			Language:  cfg.Language,
			Timeout:   timeout,
			MaxBytes:  cfg.MaxBytes,
		}, nil
	case "openai-api":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("media.audio.backend=openai-api requires api_key")
		}
		return &audio.OpenAIAPI{
			APIKey:   cfg.APIKey,
			BaseURL:  cfg.BaseURL,
			Model:    cfg.Model,
			Language: cfg.Language,
			Timeout:  timeout,
			MaxBytes: cfg.MaxBytes,
		}, nil
	default:
		return nil, fmt.Errorf("media.audio.backend=%q — expected whisper-cpp | openai-api | \"\"", cfg.Backend)
	}
}

// buildTranscriberString wraps buildAudioBackend so callers can hand
// the return value directly to the older-shape Transcriber slot on
// each transport's Config (returns (string, error) instead of
// (Result, error)).
func buildTranscriberString(cfg config.MediaAudioConfig) (*audio.TranscriberString, error) {
	backend, err := buildAudioBackend(cfg)
	if err != nil {
		return nil, err
	}
	if backend == nil {
		return nil, nil // disabled
	}
	return audio.NewTranscriberString(backend), nil
}
