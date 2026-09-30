package toolgate

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func startServer(t *testing.T, decide DecideFunc) *Server {
	t.Helper()
	s, err := Listen(decide, 2*time.Second, quiet())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	go s.Serve(ctx)
	t.Cleanup(func() { cancel(); _ = s.Close() }) //nolint:errcheck // test cleanup
	return s
}

const bashEvent = `{"session_id":"s-1","hook_event_name":"PreToolUse","tool_name":"Bash",` +
	`"tool_input":{"command":"rm -rf /"},"permission_mode":"bypassPermissions"}`

func TestRunHook_DenyBlocksWithReason(t *testing.T) {
	var got Request
	s := startServer(t, func(_ context.Context, req Request) Response {
		got = req
		return Response{Allow: false, Reason: "rm is not allowed"}
	})
	var stderr bytes.Buffer
	code := RunHook(strings.NewReader(bashEvent), &stderr, s.Path(), time.Second)
	assert.Equal(t, ExitBlock, code)
	assert.Contains(t, stderr.String(), "rm is not allowed")
	assert.Equal(t, "s-1", got.SessionID)
	assert.Equal(t, "Bash", got.ToolName)
	assert.JSONEq(t, `{"command":"rm -rf /"}`, string(got.ToolInput))
}

func TestRunHook_AllowLetsToolRun(t *testing.T) {
	s := startServer(t, func(context.Context, Request) Response { return Response{Allow: true} })
	var stderr bytes.Buffer
	assert.Equal(t, ExitAllow, RunHook(strings.NewReader(bashEvent), &stderr, s.Path(), time.Second))
	assert.Empty(t, stderr.String())
}

// TestRunHook_FailsClosed pins that every hook-side failure blocks the
// tool rather than letting it run ungoverned.
func TestRunHook_FailsClosed(t *testing.T) {
	var stderr bytes.Buffer
	assert.Equal(t, ExitBlock, RunHook(strings.NewReader(bashEvent), &stderr,
		"/nonexistent/gate.sock", time.Second), "unreachable socket")
	assert.Equal(t, ExitBlock, RunHook(strings.NewReader("not json"), &stderr,
		"/nonexistent/gate.sock", time.Second), "malformed input")

	slow := startServer(t, func(ctx context.Context, _ Request) Response {
		<-ctx.Done()
		return Response{Allow: true}
	})
	start := time.Now()
	assert.Equal(t, ExitBlock, RunHook(strings.NewReader(bashEvent), &stderr,
		slow.Path(), 100*time.Millisecond), "no decision in time")
	assert.Less(t, time.Since(start), time.Second)
}

func TestServer_MalformedRequestIsDenied(t *testing.T) {
	called := false
	s := startServer(t, func(context.Context, Request) Response { called = true; return Response{Allow: true} })
	var stderr bytes.Buffer
	assert.Equal(t, ExitBlock, RunHook(strings.NewReader(`{"session_id":"x"}`), &stderr, s.Path(), time.Second))
	assert.False(t, called, "a request without a tool name never reaches the policy")
}

func TestServer_CloseRemovesSocketDir(t *testing.T) {
	s, err := Listen(func(context.Context, Request) Response { return Response{} }, time.Second, quiet())
	require.NoError(t, err)
	info, err := os.Stat(s.dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "socket dir must be private")
	require.NoError(t, s.Close())
	_, err = os.Stat(s.dir)
	assert.True(t, os.IsNotExist(err))
	_ = s.Close() //nolint:errcheck // second Close is a no-op
}

func TestHookSettings(t *testing.T) {
	js, err := HookSettings("/usr/local/bin/rousseau hook pre-tool-use --socket /tmp/x.sock", 600)
	require.NoError(t, err)
	var v struct {
		Hooks struct {
			PreToolUse []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Type, Command string
					Timeout       int
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal([]byte(js), &v))
	require.Len(t, v.Hooks.PreToolUse, 1)
	assert.Equal(t, "*", v.Hooks.PreToolUse[0].Matcher)
	assert.Equal(t, "command", v.Hooks.PreToolUse[0].Hooks[0].Type)
	assert.Equal(t, 600, v.Hooks.PreToolUse[0].Hooks[0].Timeout)
}
