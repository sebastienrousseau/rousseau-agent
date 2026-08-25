package router_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/llm/router"
)

// stub is a minimal agent.Provider that returns a canned response
// tagged with its own name so tests can assert which child was called.
type stub struct{ name string }

func (s *stub) Name() string { return s.name }

func (s *stub) Complete(_ context.Context, _ agent.Request) (agent.Response, error) {
	return agent.Response{
		Message: agent.Message{
			Role:    agent.RoleAssistant,
			Content: []agent.Content{{Kind: agent.ContentText, Text: s.name}},
		},
		StopReason: agent.StopEndTurn,
		Model:      s.name,
	}, nil
}

func TestNew_RequiresDefault(t *testing.T) {
	_, err := router.New(router.Options{
		Providers: map[string]agent.Provider{"a": &stub{}},
	})
	assert.Error(t, err)
}

func TestNew_RequiresDefaultInProviders(t *testing.T) {
	_, err := router.New(router.Options{
		Default:   "missing",
		Providers: map[string]agent.Provider{"a": &stub{}},
	})
	assert.Error(t, err)
}

func TestNew_RequiresRuleUseInProviders(t *testing.T) {
	_, err := router.New(router.Options{
		Default:   "a",
		Providers: map[string]agent.Provider{"a": &stub{}},
		Rules:     []router.Rule{{Use: "unknown"}},
	})
	assert.Error(t, err)
}

func TestNew_RejectsEmptyRuleUse(t *testing.T) {
	_, err := router.New(router.Options{
		Default:   "a",
		Providers: map[string]agent.Provider{"a": &stub{}},
		Rules:     []router.Rule{{Use: ""}},
	})
	assert.Error(t, err)
}

func TestComplete_EmptyRulesFallsThroughToDefault(t *testing.T) {
	r, err := router.New(router.Options{
		Default: "sonnet",
		Providers: map[string]agent.Provider{
			"sonnet": &stub{name: "sonnet"},
			"haiku":  &stub{name: "haiku"},
		},
	})
	require.NoError(t, err)

	resp, err := r.Complete(context.Background(), agent.Request{})
	require.NoError(t, err)
	assert.Equal(t, "sonnet", resp.Model)
}

func TestComplete_FirstMatchWins(t *testing.T) {
	r, err := router.New(router.Options{
		Default: "sonnet",
		Providers: map[string]agent.Provider{
			"sonnet": &stub{name: "sonnet"},
			"haiku":  &stub{name: "haiku"},
			"opus":   &stub{name: "opus"},
		},
		Rules: []router.Rule{
			// Both this rule and the next COULD match a message-len=50
			// request, so we check first-match-wins semantics.
			{Name: "short-cheap", MessageLenMax: 200, Use: "haiku"},
			{Name: "any-mid", Use: "sonnet"},
		},
	})
	require.NoError(t, err)

	shortReq := agent.Request{
		Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.Content{
			{Kind: agent.ContentText, Text: "hi"},
		}}},
	}
	resp, err := r.Complete(context.Background(), shortReq)
	require.NoError(t, err)
	assert.Equal(t, "haiku", resp.Model)
}

func TestComplete_LongMessageBypassesShortRule(t *testing.T) {
	r, err := router.New(router.Options{
		Default: "sonnet",
		Providers: map[string]agent.Provider{
			"sonnet": &stub{name: "sonnet"},
			"haiku":  &stub{name: "haiku"},
		},
		Rules: []router.Rule{{MessageLenMax: 50, Use: "haiku"}},
	})
	require.NoError(t, err)

	longText := make([]byte, 500)
	for i := range longText {
		longText[i] = 'x'
	}
	req := agent.Request{
		Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.Content{
			{Kind: agent.ContentText, Text: string(longText)},
		}}},
	}
	resp, err := r.Complete(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "sonnet", resp.Model, "long message must skip the short-only rule")
}

func TestComplete_ToolUseCountThreshold(t *testing.T) {
	r, err := router.New(router.Options{
		Default: "sonnet",
		Providers: map[string]agent.Provider{
			"sonnet": &stub{name: "sonnet"},
			"opus":   &stub{name: "opus"},
		},
		Rules: []router.Rule{{ToolUseCountMin: 3, Use: "opus"}},
	})
	require.NoError(t, err)

	// Build a session with 3 tool_use blocks embedded in assistant turns.
	msgs := []agent.Message{
		{Role: agent.RoleUser, Content: []agent.Content{{Kind: agent.ContentText, Text: "go"}}},
	}
	for i := 0; i < 3; i++ {
		msgs = append(msgs, agent.Message{
			Role: agent.RoleAssistant,
			Content: []agent.Content{{
				Kind:    agent.ContentToolUse,
				ToolUse: &agent.ToolUse{Name: "bash", Input: []byte(`{}`)},
			}},
		})
	}
	msgs = append(msgs, agent.Message{
		Role:    agent.RoleUser,
		Content: []agent.Content{{Kind: agent.ContentText, Text: "continue"}},
	})

	resp, err := r.Complete(context.Background(), agent.Request{Messages: msgs})
	require.NoError(t, err)
	assert.Equal(t, "opus", resp.Model, "3 tool_use blocks should trip the opus rule")
}

func TestComplete_SessionIDPrefixRouting(t *testing.T) {
	r, err := router.New(router.Options{
		Default: "sonnet",
		Providers: map[string]agent.Provider{
			"sonnet": &stub{name: "sonnet"},
			"cheap":  &stub{name: "cheap"},
		},
		Rules: []router.Rule{{SessionIDPrefix: "test-", Use: "cheap"}},
	})
	require.NoError(t, err)

	resp, err := r.Complete(context.Background(), agent.Request{SessionID: "test-abc"})
	require.NoError(t, err)
	assert.Equal(t, "cheap", resp.Model)

	resp, err = r.Complete(context.Background(), agent.Request{SessionID: "prod-xyz"})
	require.NoError(t, err)
	assert.Equal(t, "sonnet", resp.Model)
}

func TestName_ConstantIdentifier(t *testing.T) {
	r, err := router.New(router.Options{
		Default:   "a",
		Providers: map[string]agent.Provider{"a": &stub{name: "a"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "router", r.Name())
}
