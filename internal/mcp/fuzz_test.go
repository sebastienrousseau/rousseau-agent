package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

// FuzzServe pushes arbitrary bytes through the JSON-RPC frame reader.
// A well-behaved server must never panic on hostile input; it may
// return a parse-error envelope or drop the frame.
func FuzzServe(f *testing.F) {
	f.Add([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n"))
	f.Add([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"x","version":"1"}}}` + "\n"))
	f.Add([]byte(`{"jsonrpc":"2.0"}` + "\n"))
	f.Add([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"))
	f.Add([]byte(`{malformed`))
	f.Add([]byte(``))
	f.Add([]byte(`{"jsonrpc":"1.0","method":"tools/list"}`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		s := NewServer("rousseau-fuzz", "0.0.0", silentLogger())
		s.MustRegister(ToolSpec{
			Name: "echo",
			Handler: func(context.Context, json.RawMessage) ([]Content, error) {
				return TextContent(""), nil
			},
		})
		// Ensure the input ends with a newline so the scanner sees a frame.
		in := bytes.NewReader(append(raw, '\n'))
		out := &bytes.Buffer{}
		_ = s.Serve(context.Background(), in, out)
	})
}
