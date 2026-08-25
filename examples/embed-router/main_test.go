package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// failing stands in for a provider whose upstream is down, so the
// example's per-request error branch is exercised.
type failing struct{ name string }

func (f *failing) Name() string { return f.name }
func (f *failing) Complete(context.Context, agent.Request) (agent.Response, error) {
	return agent.Response{}, errors.New("upstream unavailable")
}

func TestDefaultProviders(t *testing.T) {
	providers := defaultProviders()

	assert.Len(t, providers, 3)
	for _, name := range []string{"haiku", "sonnet", "opus"} {
		p, ok := providers[name]
		assert.True(t, ok, "missing provider %q", name)
		assert.Equal(t, name, p.Name())
	}
}

func TestRunRoutesEachBranch(t *testing.T) {
	var out, errOut bytes.Buffer

	code := run(context.Background(), &out, &errOut, defaultProviders())

	assert.Equal(t, 0, code)
	assert.Contains(t, out.String(), "short greeting")
	assert.Regexp(t, `short greeting\s+→ haiku`, out.String())
	assert.Regexp(t, `long question\s+→ sonnet`, out.String())
	assert.Regexp(t, `tool-heavy session\s+→ opus`, out.String())
}

func TestRunReportsProviderFailure(t *testing.T) {
	providers := defaultProviders()
	providers["haiku"] = &failing{name: "haiku"}
	var out, errOut bytes.Buffer

	code := run(context.Background(), &out, &errOut, providers)

	// One child failing does not abort the demo: the remaining
	// requests still route.
	assert.Equal(t, 0, code)
	assert.Contains(t, errOut.String(), "short greeting err:")
	assert.Contains(t, errOut.String(), "upstream unavailable")
	assert.Regexp(t, `long question\s+→ sonnet`, out.String())
}

func TestRunRejectsIncompleteFleet(t *testing.T) {
	var out, errOut bytes.Buffer

	// No providers at all: the router cannot resolve its default.
	code := run(context.Background(), &out, &errOut, nil)

	assert.Equal(t, 1, code)
	assert.Contains(t, errOut.String(), "router:")
}

func TestLongTextExceedsQuickChatThreshold(t *testing.T) {
	assert.Len(t, longText(), 500)
}

func TestWithToolUses(t *testing.T) {
	msgs := withToolUses(4)

	// user + 4 assistant tool_use turns + trailing user.
	assert.Len(t, msgs, 6)
	assert.Equal(t, agent.ContentToolUse, msgs[1].Content[0].Kind)
	assert.Equal(t, "bash", msgs[1].Content[0].ToolUse.Name)
}
