package client_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/mcp"
	"github.com/sebastienrousseau/rousseau-agent/internal/mcp/client"
)

// serveEnv makes the test binary act as an MCP server built on
// internal/mcp, so the client can be driven against the real server
// over a real stdio pipe without a separate build step.
const serveEnv = "ROUSSEAU_MCP_INTEROP_SERVE"

func TestMain(m *testing.M) {
	if os.Getenv(serveEnv) == "1" {
		s := mcp.NewServer("interop", "1.0.0", nil)
		s.MustRegister(mcp.ToolSpec{
			Name:        "echo",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Handler: func(_ context.Context, args json.RawMessage) ([]mcp.Content, error) {
				return mcp.TextContent(string(args)), nil
			},
		})
		if err := s.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Our client and our server agree on 2026-07-28 and work statelessly.
func TestInterop_ClientAndServerSpeak20260728(t *testing.T) {
	exe, err := os.Executable()
	require.NoError(t, err)
	cl, err := client.New(context.Background(), client.Config{
		Name:         "interop",
		Command:      exe,
		Env:          map[string]string{serveEnv: "1"},
		StartTimeout: 10 * time.Second,
		Logger:       discardLogger(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cl.Close() }) //nolint:errcheck // best-effort cleanup

	assert.Equal(t, mcp.ModernProtocolVersion, cl.ProtocolVersion())

	tools, err := cl.ListTools(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "echo", tools[0].Name)

	res, err := cl.CallTool(context.Background(), "echo", map[string]any{"msg": "hi"})
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	assert.JSONEq(t, `{"msg":"hi"}`, res.Content[0].Text)
}
