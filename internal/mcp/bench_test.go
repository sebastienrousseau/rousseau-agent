package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

// BenchmarkServe_ToolsList measures the round-trip of the most
// common query an MCP host issues on connect.
func BenchmarkServe_ToolsList(b *testing.B) {
	s := NewServer("rousseau-bench", "0.0.0", silentLogger())
	s.MustRegister(ToolSpec{
		Name: "noop", InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(context.Context, json.RawMessage) ([]Content, error) {
			return TextContent(""), nil
		},
	})
	req := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		in := bytes.NewReader(req)
		out := &bytes.Buffer{}
		_ = s.Serve(context.Background(), in, out)
	}
}

// BenchmarkClassifyLine — the parser called for every NDJSON line
// claudecli's streaming output emits.
func BenchmarkClassifyLine_TextDelta(b *testing.B) {
	// Nothing to test — classifyLine is in claudecli. Kept as a
	// placeholder note so future contributors know to keep the parser
	// benchmarks close to their source.
	b.Skip("classifyLine lives in internal/llm/claudecli/stream_test.go")
}
