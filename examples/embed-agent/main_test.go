package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/pkg/agent"
)

// fakeProvider stands in for claudecli so the example can be driven
// end to end without the `claude` CLI, a network or credentials.
type fakeProvider struct {
	reply string
	err   error
	seen  agent.Request
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Complete(_ context.Context, req agent.Request) (agent.Response, error) {
	f.seen = req
	if f.err != nil {
		return agent.Response{}, f.err
	}
	return agent.Response{
		Message: agent.Message{
			Role:    agent.RoleAssistant,
			Content: []agent.Content{{Kind: agent.ContentText, Text: f.reply}},
		},
		StopReason: agent.StopEndTurn,
		Model:      "fake-1",
	}, nil
}

func TestDefaultProviderIsClaudeCLI(t *testing.T) {
	assert.Equal(t, "claudecli", defaultProvider().Name())
}

func TestRunPrintsAssistantReply(t *testing.T) {
	provider := &fakeProvider{reply: "ready"}
	var out, errOut bytes.Buffer

	code := run(context.Background(), provider, &out, &errOut)

	require.Equal(t, 0, code, errOut.String())
	assert.Equal(t, "assistant: ready\n", out.String())

	// The prompt and system prompt the example configures reach the
	// provider.
	assert.Equal(t, "You are a careful, concise coding assistant.", provider.seen.System)
	require.NotEmpty(t, provider.seen.Messages)
	last := provider.seen.Messages[len(provider.seen.Messages)-1]
	assert.Equal(t, "Reply with EXACTLY the word 'ready'.", last.Content[0].Text)

	// Both builtin tools were registered before the turn.
	names := make([]string, 0, len(provider.seen.Tools))
	for _, tl := range provider.seen.Tools {
		names = append(names, tl.Name)
	}
	assert.Contains(t, names, "read")
	assert.Contains(t, names, "grep")
}

func TestRunReportsTurnFailure(t *testing.T) {
	provider := &fakeProvider{err: errors.New("provider exploded")}
	var out, errOut bytes.Buffer

	code := run(context.Background(), provider, &out, &errOut)

	assert.Equal(t, 1, code)
	assert.Empty(t, out.String())
	assert.Contains(t, errOut.String(), "turn: ")
	assert.Contains(t, errOut.String(), "provider exploded")
}
