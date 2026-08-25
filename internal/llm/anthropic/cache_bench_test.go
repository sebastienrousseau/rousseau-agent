package anthropic

import (
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

// BenchmarkApplyCacheMarkers runs on every completion; hot enough
// that per-call allocation matters.
func BenchmarkApplyCacheMarkers(b *testing.B) {
	msgs := make([]sdk.MessageParam, 8)
	for i := range msgs {
		msgs[i] = sdk.NewUserMessage(sdk.NewTextBlock("sample turn content — long enough to look realistic"))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		applyCacheMarkers(msgs, 2)
	}
}
