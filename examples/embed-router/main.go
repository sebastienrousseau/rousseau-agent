// Package main demonstrates the multi-model routing provider
// (internal/llm/router). Two stub providers stand in for real LLMs
// so the example runs without an API key; the routing decisions are
// what matters. Swap the stubs for [pkg/llm/anthropic.New] et al.
// in a real deployment.
//
// Run with:
//
//	go run ./examples/embed-router
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/llm/router"
)

// stub is a stand-in for a real llm provider — returns a fixed
// message tagged with its own name so we can see which child fired.
type stub struct{ name string }

func (s *stub) Name() string { return s.name }
func (s *stub) Complete(_ context.Context, _ agent.Request) (agent.Response, error) {
	return agent.Response{
		Message: agent.Message{
			Role:    agent.RoleAssistant,
			Content: []agent.Content{{Kind: agent.ContentText, Text: "reply from " + s.name}},
		},
		StopReason: agent.StopEndTurn,
		Model:      s.name,
	}, nil
}

func main() { os.Exit(run(context.Background(), os.Stdout, os.Stderr, defaultProviders())) }

// defaultProviders is the fleet the router picks between. A real
// deployment builds these with anthropic.New / openai.New / … instead.
func defaultProviders() map[string]agent.Provider {
	return map[string]agent.Provider{
		"haiku":  &stub{name: "haiku"},
		"sonnet": &stub{name: "sonnet"},
		"opus":   &stub{name: "opus"},
	}
}

// run composes the router, drives one request per branch and returns
// the process exit code. main does nothing but call os.Exit so that
// tests can drive run directly.
func run(ctx context.Context, out, errOut io.Writer, providers map[string]agent.Provider) int {
	logger := slog.New(slog.NewTextHandler(out, nil))

	r, err := router.New(router.Options{
		Default: "sonnet",
		// Rules are evaluated in order and the first match wins, so
		// the narrow rule has to come first: quick-chat's
		// ToolUseCountMax of 0 disables that filter rather than
		// requiring "no tools", and would otherwise swallow every
		// short turn including the tool-heavy ones.
		Rules: []router.Rule{
			// Tool-heavy turns: opus.
			{Name: "complex", ToolUseCountMin: 3, Use: "opus"},
			// Quick chit-chat: haiku.
			{Name: "quick-chat", MessageLenMax: 100, Use: "haiku"},
		},
		Providers: providers,
		Logger:    logger,
	})
	if err != nil {
		fmt.Fprintln(errOut, "router:", err)
		return 1
	}

	// Three test requests exercising each branch.
	tests := []struct {
		name string
		req  agent.Request
	}{
		{
			name: "short greeting",
			req:  agent.Request{Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.Content{{Kind: agent.ContentText, Text: "hi"}}}}},
		},
		{
			name: "long question",
			req:  agent.Request{Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.Content{{Kind: agent.ContentText, Text: longText()}}}}},
		},
		{
			name: "tool-heavy session",
			req:  agent.Request{Messages: withToolUses(4)},
		},
	}

	for _, tc := range tests {
		resp, err := r.Complete(ctx, tc.req)
		if err != nil {
			fmt.Fprintln(errOut, tc.name, "err:", err)
			continue
		}
		fmt.Fprintf(out, "%-30s → %s\n", tc.name, resp.Model)
	}
	return 0
}

func longText() string {
	b := make([]byte, 500)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

func withToolUses(n int) []agent.Message {
	msgs := []agent.Message{{Role: agent.RoleUser, Content: []agent.Content{{Kind: agent.ContentText, Text: "start"}}}}
	for i := 0; i < n; i++ {
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
	return msgs
}
