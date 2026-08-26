package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/pkg/agent"
)

// fakeProvider stands in for claudecli so the fan-out can be driven end
// to end without the `claude` CLI, a network or credentials. It echoes
// the prompt it was given so the test can prove each task ran.
type fakeProvider struct {
	mu      sync.Mutex
	prompts []string
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Complete(_ context.Context, req agent.Request) (agent.Response, error) {
	prompt := req.Messages[len(req.Messages)-1].Content[0].Text
	f.mu.Lock()
	f.prompts = append(f.prompts, prompt)
	f.mu.Unlock()
	return agent.Response{
		Message: agent.Message{
			Role:    agent.RoleAssistant,
			Content: []agent.Content{{Kind: agent.ContentText, Text: "answered: " + prompt}},
		},
		StopReason: agent.StopEndTurn,
		Model:      "fake-1",
		Usage:      agent.Usage{InputTokens: 10, OutputTokens: 5},
	}, nil
}

func TestDefaultProviderIsClaudeCLI(t *testing.T) {
	assert.Equal(t, "claudecli", defaultProvider().Name())
}

func TestRunAggregatesEveryTask(t *testing.T) {
	provider := &fakeProvider{}
	var out, errOut bytes.Buffer

	code := run(context.Background(), provider, &out, &errOut)

	require.Equal(t, 0, code, errOut.String())
	got := out.String()

	// One numbered summary per task, in input order.
	assert.Equal(t, 3, strings.Count(got, "answered: "))
	assert.Contains(t, got, "Summarise every open PR")
	assert.Contains(t, got, "List the last three CVEs")
	assert.Contains(t, got, "Skim README.md")

	provider.mu.Lock()
	defer provider.mu.Unlock()
	assert.Len(t, provider.prompts, 3)
}

func TestRunReportsSpawnFailure(t *testing.T) {
	var out, errOut bytes.Buffer

	// No provider to fan out to: Spawn refuses before dispatching.
	code := run(context.Background(), nil, &out, &errOut)

	assert.Equal(t, 1, code)
	assert.Empty(t, out.String())
	assert.Contains(t, errOut.String(), "spawn: ")
	assert.Contains(t, errOut.String(), "nil provider")
}
