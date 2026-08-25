package hooks_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent/hooks"
)

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// writeShellHook writes an executable /bin/sh script to disk and
// returns its path. Skips the test on Windows (no /bin/sh).
func writeShellHook(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-based hook fixtures require POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "hook.sh")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700))
	return path
}

func TestSet_NoHooksReturnsAllow(t *testing.T) {
	s := hooks.New(nil, silentLogger())
	v, err := s.Run(context.Background(), hooks.EventPreToolUse, []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, hooks.DecisionAllow, v.Decision)
}

func TestSet_HookAllows(t *testing.T) {
	path := writeShellHook(t, `printf '{"decision":"allow"}'`)
	s := hooks.New(map[hooks.Event][]hooks.Config{
		hooks.EventPreToolUse: {{Name: "ok", Command: path}},
	}, silentLogger())

	v, err := s.Run(context.Background(), hooks.EventPreToolUse, []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, hooks.DecisionAllow, v.Decision)
}

func TestSet_HookDenies(t *testing.T) {
	path := writeShellHook(t, `printf '{"decision":"deny","reason":"blocked by test"}'`)
	s := hooks.New(map[hooks.Event][]hooks.Config{
		hooks.EventPreToolUse: {{Name: "deny", Command: path}},
	}, silentLogger())

	v, err := s.Run(context.Background(), hooks.EventPreToolUse, []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, hooks.DecisionDeny, v.Decision)
	assert.Equal(t, "blocked by test", v.Reason)
}

func TestSet_FirstDenyWins(t *testing.T) {
	allow := writeShellHook(t, `printf '{"decision":"allow"}'`)
	deny := writeShellHook(t, `printf '{"decision":"deny","reason":"first blocker"}'`)
	other := writeShellHook(t, `printf '{"decision":"deny","reason":"never reached"}'`)

	s := hooks.New(map[hooks.Event][]hooks.Config{
		hooks.EventPreToolUse: {
			{Name: "allow-first", Command: allow},
			{Name: "deny-second", Command: deny},
			{Name: "deny-third", Command: other},
		},
	}, silentLogger())

	v, err := s.Run(context.Background(), hooks.EventPreToolUse, []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, hooks.DecisionDeny, v.Decision)
	assert.Equal(t, "first blocker", v.Reason)
}

func TestSet_EmptyStdoutIsAllow(t *testing.T) {
	// exit 0 with no output — treated as "no objection".
	path := writeShellHook(t, `exit 0`)
	s := hooks.New(map[hooks.Event][]hooks.Config{
		hooks.EventPreToolUse: {{Name: "silent", Command: path}},
	}, silentLogger())

	v, err := s.Run(context.Background(), hooks.EventPreToolUse, []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, hooks.DecisionAllow, v.Decision)
}

func TestSet_FailingHookIsTreatedAsAllow(t *testing.T) {
	// exit 1 with garbage stdout — a broken hook must not lock the
	// daemon out of tool use. Set falls back to Allow after logging.
	path := writeShellHook(t, `echo "nonsense"; exit 1`)
	s := hooks.New(map[hooks.Event][]hooks.Config{
		hooks.EventPreToolUse: {{Name: "broken", Command: path}},
	}, silentLogger())

	v, err := s.Run(context.Background(), hooks.EventPreToolUse, []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, hooks.DecisionAllow, v.Decision)
}

func TestSet_HookTimeout(t *testing.T) {
	path := writeShellHook(t, `sleep 5`)
	s := hooks.New(map[hooks.Event][]hooks.Config{
		hooks.EventPreToolUse: {{Name: "slow", Command: path, Timeout: 100 * time.Millisecond}},
	}, silentLogger())

	v, err := s.Run(context.Background(), hooks.EventPreToolUse, []byte(`{}`))
	require.NoError(t, err)
	// Fail-open: timeout ⇒ Allow (with a WARN log we don't assert on).
	assert.Equal(t, hooks.DecisionAllow, v.Decision)
}

func TestSet_HookSeesPayloadOnStdin(t *testing.T) {
	// Write stdin to a file the test can inspect.
	captureFile := filepath.Join(t.TempDir(), "captured")
	path := writeShellHook(t, `cat > `+captureFile+`; printf '{"decision":"allow"}'`)

	s := hooks.New(map[hooks.Event][]hooks.Config{
		hooks.EventPreToolUse: {{Name: "capture", Command: path}},
	}, silentLogger())

	payload := []byte(`{"event":"pre_tool_use","tool_name":"bash"}`)
	_, err := s.Run(context.Background(), hooks.EventPreToolUse, payload)
	require.NoError(t, err)

	got, err := os.ReadFile(captureFile)
	require.NoError(t, err)
	assert.Equal(t, string(payload), string(got))
}

func TestMarshalPreToolUse_ShapesPayload(t *testing.T) {
	payload, err := hooks.MarshalPreToolUse("s1", "bash", []byte(`{"command":"ls"}`))
	require.NoError(t, err)
	assert.Contains(t, string(payload), `"event":"pre_tool_use"`)
	assert.Contains(t, string(payload), `"session_id":"s1"`)
	assert.Contains(t, string(payload), `"tool_name":"bash"`)
	assert.Contains(t, string(payload), `"input":{"command":"ls"}`)
}

func TestSet_UnconfiguredEventReturnsAllow(t *testing.T) {
	// Hook attached to pre_tool_use, but we call post_tool_use — must
	// return Allow.
	path := writeShellHook(t, `printf '{"decision":"deny"}'`)
	s := hooks.New(map[hooks.Event][]hooks.Config{
		hooks.EventPreToolUse: {{Name: "pretool-only", Command: path}},
	}, silentLogger())

	v, err := s.Run(context.Background(), hooks.EventPostToolUse, []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, hooks.DecisionAllow, v.Decision)
}
