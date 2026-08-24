package router_test

import (
	"context"
	"testing"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/llm/router"
)

func benchStub(name string) agent.Provider { return &stub{name: name} }

// BenchmarkComplete_ShortMessageMatch runs the router's decision
// path on a request that matches the first rule — the common case
// for chit-chat routing.
func BenchmarkComplete_ShortMessageMatch(b *testing.B) {
	r, err := router.New(router.Options{
		Default: "sonnet",
		Rules: []router.Rule{
			{MessageLenMax: 200, Use: "haiku"},
			{ToolUseCountMin: 3, Use: "opus"},
		},
		Providers: map[string]agent.Provider{
			"haiku":  benchStub("haiku"),
			"sonnet": benchStub("sonnet"),
			"opus":   benchStub("opus"),
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	req := agent.Request{
		Messages: []agent.Message{{
			Role: agent.RoleUser,
			Content: []agent.Content{{Kind: agent.ContentText, Text: "hi"}},
		}},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Complete(context.Background(), req)
	}
}

// BenchmarkComplete_DefaultFallthrough measures the worst-case rule
// walk — no rule matches, all N rules evaluated before falling
// through to the default. Baseline for "how expensive are extra rules".
func BenchmarkComplete_DefaultFallthrough(b *testing.B) {
	rules := make([]router.Rule, 10)
	for i := range rules {
		// All rules require a session ID prefix that won't match.
		rules[i] = router.Rule{SessionIDPrefix: "no-match-", Use: "haiku"}
	}
	r, err := router.New(router.Options{
		Default:   "sonnet",
		Rules:     rules,
		Providers: map[string]agent.Provider{"haiku": benchStub("haiku"), "sonnet": benchStub("sonnet")},
	})
	if err != nil {
		b.Fatal(err)
	}
	req := agent.Request{SessionID: "prod-abc"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Complete(context.Background(), req)
	}
}
