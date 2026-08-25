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

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	r, err := router.New(router.Options{
		Default: "sonnet",
		Rules: []router.Rule{
			// Quick chit-chat with no tools in the transcript: haiku.
			{Name: "quick-chat", MessageLenMax: 100, ToolUseCountMax: 0, Use: "haiku"},
			// Long or tool-heavy turns: opus.
			{Name: "complex", ToolUseCountMin: 3, Use: "opus"},
		},
		Providers: map[string]agent.Provider{
			"haiku":  &stub{name: "haiku"},
			"sonnet": &stub{name: "sonnet"},
			"opus":   &stub{name: "opus"},
		},
		Logger: logger,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "router:", err)
		os.Exit(1)
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
		resp, err := r.Complete(context.Background(), tc.req)
		if err != nil {
			fmt.Fprintln(os.Stderr, tc.name, "err:", err)
			continue
		}
		fmt.Printf("%-30s → %s\n", tc.name, resp.Model)
	}
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
