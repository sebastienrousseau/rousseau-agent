package cli

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// fakeMCPServer writes a POSIX-shell MCP server that answers the two
// requests startMCPClients makes — initialize (id 1) and tools/list
// (id 2) — and returns its path.
//
// A script rather than a compiled helper keeps the test hermetic and
// fast: no `go build` at test time, no network, and the real
// line-delimited JSON-RPC stdio wire path still gets exercised.
func fakeMCPServer(t *testing.T, toolsJSON string) string {
	t.Helper()
	script := `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*)
      printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"fake","version":"0.1"}}}'
      ;;
    *'"method":"tools/list"'*)
      printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":` + toolsJSON + `}}'
      ;;
  esac
done
`
	path := filepath.Join(t.TempDir(), "fake-mcp-server.sh")
	require.NoError(t, os.WriteFile(path, []byte(script), 0o700)) //nolint:gosec // test fixture must be executable
	return path
}

func TestStartMCPClients_NilRegistryErrors(t *testing.T) {
	_, _, err := startMCPClients(context.Background(), config.MCPConfig{}, nil, silentLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil registry")
}

func TestStartMCPClients_NoClientsIsNoOp(t *testing.T) {
	clients, names, err := startMCPClients(context.Background(), config.MCPConfig{},
		tools.NewRegistry(), silentLogger())
	require.NoError(t, err)
	assert.Empty(t, clients)
	assert.Empty(t, names)
}

func TestStartMCPClients_SkipsEmptyCommand(t *testing.T) {
	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	clients, names, err := startMCPClients(context.Background(), config.MCPConfig{
		Clients: map[string]config.MCPClientConfig{"broken": {Command: ""}},
	}, tools.NewRegistry(), logger)
	require.NoError(t, err)
	assert.Empty(t, clients)
	assert.Empty(t, names)
	assert.Contains(t, buf.String(), "mcp.client.skip")
	assert.Contains(t, buf.String(), "empty command")
}

func TestStartMCPClients_FailSoftOnUnstartableServer(t *testing.T) {
	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	clients, names, err := startMCPClients(context.Background(), config.MCPConfig{
		Clients: map[string]config.MCPClientConfig{
			"ghost": {Command: filepath.Join(t.TempDir(), "definitely-not-a-binary")},
		},
	}, tools.NewRegistry(), logger)
	require.NoError(t, err, "one broken server must not take the daemon down")
	assert.Empty(t, clients)
	assert.Empty(t, names)
	assert.Contains(t, buf.String(), "mcp.client.start_failed")
}

func TestStartMCPClients_RegistersAdvertisedTools(t *testing.T) {
	registry := tools.NewRegistry()
	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	server := fakeMCPServer(t, `[{"name":"echo","description":"echoes","inputSchema":{"type":"object"}}]`)
	clients, names, err := startMCPClients(context.Background(), config.MCPConfig{
		Clients: map[string]config.MCPClientConfig{
			"fake": {
				Command:               server,
				Args:                  []string{},
				Env:                   map[string]string{"ROUSSEAU_TEST": "1"},
				StartTimeoutSeconds:   10,
				RequestTimeoutSeconds: 10,
			},
		},
	}, registry, logger)
	require.NoError(t, err)
	require.Len(t, clients, 1)
	assert.Equal(t, []string{"mcp:fake:echo"}, names)
	assert.Contains(t, buf.String(), "mcp.client.registered")

	closeMCPClients(clients, logger)
}

func TestStartMCPClients_PartialRegistrationClosesClient(t *testing.T) {
	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	// The same tool name twice: the second Register collides, so
	// RegisterClient returns an aggregated error and the whole server
	// is dropped rather than left half-wired.
	server := fakeMCPServer(t, `[{"name":"dup","inputSchema":{}},{"name":"dup","inputSchema":{}}]`)
	clients, names, err := startMCPClients(context.Background(), config.MCPConfig{
		Clients: map[string]config.MCPClientConfig{
			"dupes": {Command: server, StartTimeoutSeconds: 10, RequestTimeoutSeconds: 10},
		},
	}, tools.NewRegistry(), logger)
	require.NoError(t, err)
	assert.Empty(t, clients)
	assert.Empty(t, names)
	assert.Contains(t, buf.String(), "mcp.client.register_partial")
}

func TestStartMCPClients_IteratesInNameOrder(t *testing.T) {
	registry := tools.NewRegistry()
	server := fakeMCPServer(t, `[{"name":"t","inputSchema":{}}]`)
	clients, names, err := startMCPClients(context.Background(), config.MCPConfig{
		Clients: map[string]config.MCPClientConfig{
			"zulu":  {Command: server, StartTimeoutSeconds: 10, RequestTimeoutSeconds: 10},
			"alpha": {Command: server, StartTimeoutSeconds: 10, RequestTimeoutSeconds: 10},
		},
	}, registry, silentLogger())
	require.NoError(t, err)
	require.Len(t, clients, 2)
	assert.Equal(t, []string{"mcp:alpha:t", "mcp:zulu:t"}, names,
		"startup order must be deterministic so collisions are reproducible")
	closeMCPClients(clients, silentLogger())
}

func TestCloseMCPClients_LogsCloseFailuresAndSkipsNils(t *testing.T) {
	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	server := fakeMCPServer(t, `[{"name":"t","inputSchema":{}}]`)
	clients, _, err := startMCPClients(context.Background(), config.MCPConfig{
		Clients: map[string]config.MCPClientConfig{
			"fake": {Command: server, StartTimeoutSeconds: 10, RequestTimeoutSeconds: 10},
		},
	}, tools.NewRegistry(), silentLogger())
	require.NoError(t, err)
	require.Len(t, clients, 1)

	// A nil entry (a server that never started) must be stepped over,
	// and closing an already-closed client must not blow up.
	closeMCPClients(append(clients, nil), logger)
	closeMCPClients(clients, logger)
	assert.NotContains(t, buf.String(), "panic")
}

// TestAssembleDaemon_LogsMCPToolCount pins the summary log the daemon
// emits once every configured MCP server is wired.
func TestAssembleDaemon_LogsMCPToolCount(t *testing.T) {
	var buf syncBuffer
	opts := makeDaemonOpts(t)
	opts.Logger = slog.New(slog.NewTextHandler(&buf, nil))
	opts.Config.MCP = config.MCPConfig{
		Clients: map[string]config.MCPClientConfig{
			"fake": {
				Command:               fakeMCPServer(t, `[{"name":"echo","inputSchema":{}}]`),
				StartTimeoutSeconds:   10,
				RequestTimeoutSeconds: 10,
			},
		},
	}

	wiring, err := assembleDaemon(context.Background(), opts, nil)
	require.NoError(t, err)
	defer func() { _ = wiring.Cleanup() }() //nolint:errcheck // test cleanup

	assert.Contains(t, buf.String(), "mcp.clients.ready")
	assert.Len(t, wiring.MCPClients, 1)
	// Cleanup must close the subprocess, not leak it.
	require.NoError(t, wiring.Cleanup())
}

func TestMCPCmd_ServesUntilStdinEOF(t *testing.T) {
	opts := makeOpts(t)
	withStdio(t)

	cmd := newMCPCmd(opts)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, nil), "EOF on stdin is a clean shutdown")
}

func TestMCPCmd_AnswersToolsList(t *testing.T) {
	opts := makeOpts(t)
	stdout := withStdinLines(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	cmd := newMCPCmd(opts)
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.RunE(cmd, nil))

	out := readFileString(t, stdout)
	assert.Contains(t, out, "rousseau_list_sessions")
}

func TestMCPCmd_OpenStoreFailureSurfaces(t *testing.T) {
	withStdio(t)
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))

	opts := &Options{
		Config: &config.Config{State: config.StateConfig{Path: filepath.Join(blocker, "sessions.db")}},
		Logger: silentLogger(),
	}
	cmd := newMCPCmd(opts)
	cmd.SetContext(context.Background())
	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create state dir")
}

// withStdio points os.Stdin at an empty file and os.Stdout at a temp
// file for the duration of the test. `rousseau mcp` speaks over the
// process's real stdio, so tests have to swap it out to stay
// deterministic (and to keep the JSON-RPC frames out of test output).
func withStdio(t *testing.T) string {
	t.Helper()
	return withStdinLines(t)
}

func withStdinLines(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	inPath := filepath.Join(dir, "stdin")
	outPath := filepath.Join(dir, "stdout")
	body := ""
	if len(lines) > 0 {
		body = strings.Join(lines, "\n") + "\n"
	}
	require.NoError(t, os.WriteFile(inPath, []byte(body), 0o600))

	in, err := os.Open(inPath) //nolint:gosec // path is test-owned
	require.NoError(t, err)
	out, err := os.Create(outPath) //nolint:gosec // path is test-owned
	require.NoError(t, err)

	prevIn, prevOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = in, out
	t.Cleanup(func() {
		os.Stdin, os.Stdout = prevIn, prevOut
		_ = in.Close()  //nolint:errcheck // test cleanup
		_ = out.Close() //nolint:errcheck // test cleanup
	})
	return outPath
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	// Give the server's writes a moment to land on disk before the
	// deferred Close in the cleanup runs.
	deadline := time.Now().Add(time.Second)
	for {
		b, err := os.ReadFile(path) //nolint:gosec // path is test-owned
		require.NoError(t, err)
		if len(b) > 0 || time.Now().After(deadline) {
			return string(b)
		}
	}
}
