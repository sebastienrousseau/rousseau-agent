package claudecli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// BenchmarkClassifyLine_TextDelta covers the stream-json parser called
// once per NDJSON line the claude CLI emits.
func BenchmarkClassifyLine_TextDelta(b *testing.B) {
	line := json.RawMessage(`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = classifyLine(line)
	}
}

// BenchmarkParseStream_LongTranscript exercises the end-to-end
// scanner + classifier over a realistic streaming transcript.
func BenchmarkParseStream_LongTranscript(b *testing.B) {
	var body strings.Builder
	body.WriteString(`{"type":"system","subtype":"init"}` + "\n")
	for i := 0; i < 100; i++ {
		body.WriteString(`{"type":"assistant","message":{"content":[{"type":"text","text":"chunk"}]}}` + "\n")
	}
	body.WriteString(`{"type":"result","result":"done","stop_reason":"end_turn"}` + "\n")
	raw := body.String()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		events := make(chan agent.StreamEvent, 200)
		// Drain events off-thread so parseStream never blocks.
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range events {
			}
		}()
		_, _ = parseStream(strings.NewReader(raw), events)
		close(events)
		<-done
	}
}
