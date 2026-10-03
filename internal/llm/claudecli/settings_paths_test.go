package claudecli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// TestSetSettings_ReachesBothPaths pins that the policy-bridge settings
// installed after construction reach the one-shot and streaming argv.
func TestSetSettings_ReachesBothPaths(t *testing.T) {
	p := New(Config{})
	p.SetSettings(`{"hooks":{}}`)
	var argv []string
	p.run = func(cmd *exec.Cmd) ([]byte, error) {
		argv = cmd.Args
		return []byte(`{"type":"result","result":"ok","stop_reason":"end_turn"}`), nil
	}
	_, err := p.Complete(context.Background(), agent.Request{Messages: []agent.Message{agent.NewUserText("hi")}})
	require.NoError(t, err)
	i := indexOf(argv, "--settings")
	require.GreaterOrEqual(t, i, 0)
	assert.Equal(t, `{"hooks":{}}`, argv[i+1])

	stream := p.buildStreamArgs(agent.Request{})
	assert.Contains(t, stream, "--settings")
}

func TestCompleteStructured_Errors(t *testing.T) {
	ctx := context.Background()
	q := agent.Request{Messages: []agent.Message{agent.NewUserText("q")}}
	obj := map[string]any{"type": "object"}
	mk := func(out string, err error) *Provider {
		p := New(Config{})
		p.run = func(*exec.Cmd) ([]byte, error) { return []byte(out), err }
		return p
	}

	_, err := mk("", nil).CompleteStructured(ctx, agent.Request{}, obj)
	assert.Error(t, err, "no user content")

	_, err = mk("", nil).CompleteStructured(ctx, q, map[string]any{"bad": func() {}})
	assert.ErrorContains(t, err, "schema")

	_, err = mk("auth expired", errors.New("exit status 1")).CompleteStructured(ctx, q, obj)
	assert.ErrorContains(t, err, "auth expired")

	_, err = mk("plain text", nil).CompleteStructured(ctx, q, obj)
	assert.ErrorContains(t, err, "no JSON")

	_, err = mk("{not json", nil).CompleteStructured(ctx, q, obj)
	assert.ErrorContains(t, err, "parse")

	_, err = mk(`{"is_error":true,"subtype":"error_max_turns","result":"gave up"}`, nil).CompleteStructured(ctx, q, obj)
	assert.ErrorContains(t, err, "error_max_turns")
}

func TestClaudeConfigDir_DefaultsToHome(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir, err := claudeConfigDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".claude"), dir)
}

func TestForkSession_Errors(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfgDir)
	p := New(Config{})

	// A directory where the source transcript should be is unreadable.
	src := sessionFilePathDefault("src")
	require.NoError(t, os.MkdirAll(src, 0o700))
	assert.ErrorContains(t, p.ForkSession("src", "dst"), "fork: read")

	// A directory where the destination should be cannot be written.
	ok := sessionFilePathDefault("ok")
	require.NoError(t, os.WriteFile(ok, []byte("{}\n"), 0o600))
	require.NoError(t, os.MkdirAll(sessionFilePathDefault("taken"), 0o700))
	assert.ErrorContains(t, p.ForkSession("ok", "taken"), "fork: write")
}
