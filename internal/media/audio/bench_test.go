package audio_test

import (
	"testing"

	"github.com/sebastienrousseau/rousseau-agent/internal/media/audio"
)

// BenchmarkKnownVoiceNoteMimeType exercises the mime-type filter
// that runs on every inbound audio message (transport dispatchers
// short-circuit here before spending money on a Whisper call).
func BenchmarkKnownVoiceNoteMimeType_Hit(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = audio.KnownVoiceNoteMimeType("audio/ogg; codecs=opus")
	}
}

func BenchmarkKnownVoiceNoteMimeType_Miss(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = audio.KnownVoiceNoteMimeType("video/mp4")
	}
}
