package toolgate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
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

func TestListen_Errors(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	_, err := Listen(nil, time.Second, nil)
	assert.ErrorContains(t, err, "toolgate: socket dir")

	// A socket path past the ~108-byte unix limit cannot be bound.
	long := filepath.Join(t.TempDir(), strings.Repeat("d", 120))
	require.NoError(t, os.MkdirAll(long, 0o700))
	t.Setenv("TMPDIR", long)
	_, err = Listen(nil, time.Second, nil)
	assert.ErrorContains(t, err, "toolgate: listen")
	entries, rerr := os.ReadDir(long)
	require.NoError(t, rerr)
	assert.Empty(t, entries, "a failed listen removes its socket dir")
}

func TestReadLine(t *testing.T) {
	// Longer than the reader's buffer: fragments are joined.
	big := strings.Repeat("a", 100) + "\n"
	line, err := readLine(bufio.NewReaderSize(strings.NewReader(big), 16), 1024)
	require.NoError(t, err)
	assert.Equal(t, big, string(line))

	line, err = readLine(bufio.NewReader(strings.NewReader("no newline")), 1024)
	require.NoError(t, err)
	assert.Equal(t, "no newline", string(line))

	_, err = readLine(bufio.NewReaderSize(strings.NewReader(big), 16), 50)
	assert.ErrorContains(t, err, "too large")

	_, err = readLine(bufio.NewReader(strings.NewReader("")), 1024)
	assert.ErrorIs(t, err, io.EOF)
}

func TestRunHook_OversizedInputBlocks(t *testing.T) {
	var stderr bytes.Buffer
	huge := strings.NewReader(strings.Repeat("x", maxRequestBytes+1))
	assert.Equal(t, ExitBlock, RunHook(huge, &stderr, "/nonexistent/gate.sock", time.Second))
	assert.Contains(t, stderr.String(), "unreadable tool call")
}

func TestRunHook_DenyWithoutReason(t *testing.T) {
	s := startServer(t, func(context.Context, Request) Response { return Response{Allow: false} })
	var stderr bytes.Buffer
	assert.Equal(t, ExitBlock, RunHook(strings.NewReader(bashEvent), &stderr, s.Path(), time.Second))
	assert.Contains(t, stderr.String(), "denied by policy")
}

func TestRunHook_MalformedDecisionBlocks(t *testing.T) {
	// Not t.TempDir(): macOS's is too long for a unix socket path.
	dir, err := os.MkdirTemp("", "tg-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) }) //nolint:errcheck // test cleanup
	path := filepath.Join(dir, "g.sock")
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() }) //nolint:errcheck // test cleanup
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer conn.Close()                                         //nolint:errcheck // test server
		_, _ = bufio.NewReader(conn).ReadBytes('\n')               //nolint:errcheck // request is irrelevant
		_, _ = conn.Write([]byte("{\"allow\":true,\"reason\":\n")) //nolint:errcheck // deliberately truncated JSON
	}()
	var stderr bytes.Buffer
	assert.Equal(t, ExitBlock, RunHook(strings.NewReader(bashEvent), &stderr, path, time.Second))
	assert.Contains(t, stderr.String(), "malformed decision")
}

// TestServer_SilentClientIsCutOff (L-23): a client that connects and
// never sends a line must not pin a handler goroutine, which would
// also wedge Close (it waits for in-flight handlers).
func TestServer_SilentClientIsCutOff(t *testing.T) {
	prev := requestReadTimeout
	requestReadTimeout = 100 * time.Millisecond
	t.Cleanup(func() { requestReadTimeout = prev })

	s, err := Listen(func(context.Context, Request) Response { return Response{Allow: true} }, time.Second, quiet())
	require.NoError(t, err)
	go s.Serve(context.Background())

	conn, err := net.Dial("unix", s.Path())
	require.NoError(t, err)
	defer conn.Close() //nolint:errcheck // test cleanup

	reply := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(conn).ReadString('\n') //nolint:errcheck // EOF also ends the wait
		reply <- line
	}()
	select {
	case line := <-reply:
		assert.Contains(t, line, `"allow":false`, "a request never sent is denied")
	case <-time.After(3 * time.Second):
		t.Fatal("server never cut off a silent client")
	}

	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close wedged behind a silent client")
	}
}
