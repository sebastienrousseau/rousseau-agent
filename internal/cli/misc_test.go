package cli

import (
	"bytes"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

// TestDisplayWindow_ZeroReturnsAll pins the tiny window formatter so
// the session-cost CLI shows "all" when the operator passes
// --since=0 (or omits it).
func TestDisplayWindow_ZeroReturnsAll(t *testing.T) {
	assert.Equal(t, "all", displayWindow(0))
	assert.Equal(t, "all", displayWindow(-1))
}

func TestDisplayWindow_PositiveEchoesDuration(t *testing.T) {
	assert.Equal(t, "24h0m0s", displayWindow(24*time.Hour))
	assert.Equal(t, "15m0s", displayWindow(15*time.Minute))
}

// TestCloseMCPClients_NilAndEmptyAreNoOps confirms the helper's
// documented no-op behaviour on nil / empty slices — the shape we
// see when the daemon starts with mcp.clients disabled.
func TestCloseMCPClients_NilAndEmptyAreNoOps(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	closeMCPClients(nil, logger)
	closeMCPClients(nil, logger)
	assert.Empty(t, buf.String(), "no clients → no log lines")
}

func TestBuildTranscriber_NilWhenAudioDisabled(t *testing.T) {
	opts := &Options{
		Config: &config.Config{
			Media:    config.MediaConfig{Audio: config.MediaAudioConfig{}},   // Backend=""
			WhatsApp: config.WhatsAppConfig{Voice: config.VoiceConfig{}},     // Enabled=false
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	assert.Nil(t, buildTranscriber(opts))
}

func TestBuildTranscriber_MediaAudioBackendWins(t *testing.T) {
	opts := &Options{
		Config: &config.Config{
			Media: config.MediaConfig{Audio: config.MediaAudioConfig{
				Backend:   "whisper-cpp",
				ModelFile: "/tmp/does-not-exist.bin", // process runs, we just need a non-nil constructor
			}},
			WhatsApp: config.WhatsAppConfig{Voice: config.VoiceConfig{
				Enabled: true,
				Binary:  "whisper", Model: "small",
			}},
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	got := buildTranscriber(opts)
	require.NotNil(t, got, "media.audio.backend should win over whatsapp.voice.enabled")
}

func TestBuildTranscriber_LegacyWhatsAppVoiceFallback(t *testing.T) {
	opts := &Options{
		Config: &config.Config{
			Media: config.MediaConfig{Audio: config.MediaAudioConfig{}}, // no new backend
			WhatsApp: config.WhatsAppConfig{Voice: config.VoiceConfig{
				Enabled: true, Binary: "whisper", Model: "tiny",
			}},
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	got := buildTranscriber(opts)
	require.NotNil(t, got, "legacy whatsapp.voice.enabled must still build a transcriber")
}
