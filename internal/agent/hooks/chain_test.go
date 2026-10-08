package hooks_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent/hooks"
)

// capturedInput reads a PreToolUse payload a hook wrote to path and
// returns its input field.
func capturedInput(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var p hooks.PreToolUsePayload
	require.NoError(t, json.Unmarshal(raw, &p))
	return string(p.Input)
}

func preToolPayload(t *testing.T, input string) []byte {
	t.Helper()
	p, err := hooks.MarshalPreToolUse("s1", "bash", json.RawMessage(input))
	require.NoError(t, err)
	return p
}

func TestHooks_SecondHookDenyAfterModifyWins(t *testing.T) {
	seen := filepath.Join(t.TempDir(), "seen")
	modify := writeShellHook(t, `printf '{"decision":"modify","modified":{"command":"ls"}}'`)
	scanner := writeShellHook(t, `cat > `+seen+`; printf '{"decision":"deny","reason":"scanner says no"}'`)
	s := hooks.New(map[hooks.Event][]hooks.Config{
		hooks.EventPreToolUse: {
			{Name: "rewriter", Command: modify},
			{Name: "scanner", Command: scanner},
		},
	}, silentLogger())

	v, err := s.Run(context.Background(), hooks.EventPreToolUse, preToolPayload(t, `{"command":"rm -rf /"}`))
	require.NoError(t, err)
	assert.Equal(t, hooks.DecisionDeny, v.Decision, "a later deny must win over an earlier modify")
	assert.Equal(t, "scanner says no", v.Reason)
	assert.JSONEq(t, `{"command":"ls"}`, capturedInput(t, seen), "the scanner must see the modified input")
}

func TestHooks_ModifyChainsToNextHook(t *testing.T) {
	seen := filepath.Join(t.TempDir(), "seen")
	first := writeShellHook(t, `printf '{"decision":"modify","modified":{"command":"ls"}}'`)
	second := writeShellHook(t, `cat > `+seen+`; printf '{"decision":"modify","modified":{"command":"ls -l"}}'`)
	third := writeShellHook(t, `printf '{"decision":"allow"}'`)
	s := hooks.New(map[hooks.Event][]hooks.Config{
		hooks.EventPreToolUse: {
			{Name: "first", Command: first},
			{Name: "second", Command: second},
			{Name: "third", Command: third},
		},
	}, silentLogger())

	v, err := s.Run(context.Background(), hooks.EventPreToolUse, preToolPayload(t, `{"command":"x"}`))
	require.NoError(t, err)
	assert.Equal(t, hooks.DecisionModify, v.Decision)
	assert.JSONEq(t, `{"command":"ls -l"}`, string(v.Modified), "the last modification is the final input")
	assert.JSONEq(t, `{"command":"ls"}`, capturedInput(t, seen))
}

func TestHooks_FailClosedOnError(t *testing.T) {
	tests := []struct {
		name string
		cfg  hooks.Config
	}{
		{"non-zero exit", hooks.Config{Name: "broken", Command: writeShellHook(t, `exit 3`), FailClosed: true}},
		{"bad verdict", hooks.Config{Name: "garbled", Command: writeShellHook(t, `printf 'not json'`), FailClosed: true}},
		{"timeout", hooks.Config{Name: "slow", Command: writeShellHook(t, `sleep 5`), Timeout: 100 * time.Millisecond, FailClosed: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := hooks.New(map[hooks.Event][]hooks.Config{hooks.EventPreToolUse: {tc.cfg}}, silentLogger())
			v, err := s.Run(context.Background(), hooks.EventPreToolUse, preToolPayload(t, `{}`))
			require.NoError(t, err)
			assert.Equal(t, hooks.DecisionDeny, v.Decision)
			assert.Contains(t, v.Reason, tc.cfg.Name)
			assert.Contains(t, v.Reason, "fail_closed")
		})
	}
}

func TestHooks_FailOpenByDefault(t *testing.T) {
	broken := writeShellHook(t, `exit 3`)
	modify := writeShellHook(t, `printf '{"decision":"modify","modified":{"a":1}}'`)
	s := hooks.New(map[hooks.Event][]hooks.Config{
		hooks.EventPreToolUse: {
			{Name: "broken", Command: broken},
			{Name: "rewriter", Command: modify},
		},
	}, silentLogger())

	v, err := s.Run(context.Background(), hooks.EventPreToolUse, preToolPayload(t, `{}`))
	require.NoError(t, err)
	assert.Equal(t, hooks.DecisionModify, v.Decision, "a failing default hook is skipped, later hooks still run")
	assert.JSONEq(t, `{"a":1}`, string(v.Modified))
}

func TestHooks_ModifyWithoutPayloadIsAllow(t *testing.T) {
	path := writeShellHook(t, `printf '{"decision":"modify"}'`)
	s := hooks.New(map[hooks.Event][]hooks.Config{
		hooks.EventPreToolUse: {{Name: "empty", Command: path}},
	}, silentLogger())
	v, err := s.Run(context.Background(), hooks.EventPreToolUse, preToolPayload(t, `{}`))
	require.NoError(t, err)
	assert.Equal(t, hooks.DecisionAllow, v.Decision)
}
