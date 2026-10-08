package claudecli

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// The claude child runs its own Bash tool, so it must not inherit the
// daemon's secrets: only its own settings, the baseline and explicit
// passthrough reach it.
func TestStream_ChildEnvironmentIsScrubbed(t *testing.T) {
	t.Setenv("ROUSSEAU_TELEGRAM_TOKEN", "do-not-leak-telegram")
	t.Setenv("OPENAI_API_KEY", "do-not-leak-openai")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-kept")
	t.Setenv("CLAUDE_CONFIG_DIR", "/tmp/claude-cfg")
	t.Setenv("MY_TOOL_HOME", "/opt/tool")
	cli := newFakeCLI(t, ndjson(resultLine), "", 0)
	p := New(Config{Binary: cli.path, EnvPassthrough: []string{"MY_TOOL_*"}})

	evs, rep, err := p.Stream(context.Background(), agent.Request{
		SessionID: "sess-env",
		Messages:  []agent.Message{agent.NewUserText("hi")},
	})
	require.NoError(t, err)
	_, report := collect(t, evs, rep)
	require.NoError(t, report.Err)

	// Assert on individual variables only: a failure message must
	// never print the environment, which holds the runner's secrets.
	env := parseEnv(cli.env(t))
	assert.False(t, env.has("ROUSSEAU_TELEGRAM_TOKEN"), "ROUSSEAU_TELEGRAM_TOKEN reached the claude child")
	assert.False(t, env.has("OPENAI_API_KEY"), "OPENAI_API_KEY reached the claude child")
	assert.False(t, env.has("GITHUB_TOKEN"), "GITHUB_TOKEN reached the claude child")
	assert.True(t, env.is("ANTHROPIC_API_KEY", "sk-ant-kept"), "ANTHROPIC_API_KEY must pass through")
	assert.True(t, env.is("CLAUDE_CONFIG_DIR", "/tmp/claude-cfg"), "CLAUDE_CONFIG_DIR must pass through")
	assert.True(t, env.is("MY_TOOL_HOME", "/opt/tool"), "EnvPassthrough must pass through")
	assert.True(t, env.has("HOME"), "claude needs HOME for ~/.claude")
}

// Every invocation path builds its command through p.command.
func TestCommand_SetsScrubbedEnv(t *testing.T) {
	t.Setenv("ROUSSEAU_SLACK_BOT_TOKEN", "xoxb-secret")
	p := New(Config{Binary: "/bin/true"})
	cmd := p.command(context.Background(), []string{"--print"})
	require.NotNil(t, cmd.Env, "a nil Env would inherit everything")
	assert.False(t, slices.ContainsFunc(cmd.Env, func(kv string) bool { return strings.HasPrefix(kv, "ROUSSEAU_SLACK_BOT_TOKEN=") }),
		"ROUSSEAU_SLACK_BOT_TOKEN reached the claude command")
	assert.Equal(t, []string{"/bin/true", "--print"}, cmd.Args)
}

// envSet is a parsed environment that tests query by name, so no
// assertion ever formats the whole environment into a failure message.
type envSet map[string]string

func parseEnv(raw string) envSet {
	out := envSet{}
	for _, line := range strings.Split(raw, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}
	return out
}

func (e envSet) has(k string) bool { _, ok := e[k]; return ok }

func (e envSet) is(k, v string) bool { return e[k] == v }
