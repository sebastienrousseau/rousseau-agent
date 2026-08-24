// Package main demonstrates the per-session cost recorder
// (internal/pricing + internal/state/sqlite). Every "completion"
// records its usage + estimated USD into an in-memory SQLite;
// the example then queries by session and by top-cost.
//
// Run with:
//
//	go run ./examples/embed-cost
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

func main() {
	ctx := context.Background()

	base, err := sqlitestore.Open(ctx, ":memory:")
	must(err)
	defer func() { _ = base.Close() }()

	costs, err := sqlitestore.NewSessionCostStore(ctx, base)
	must(err)
	rec := sqlitestore.NewCostRecorder(costs, nil) // nil → DefaultTable

	// Simulate three completions across two sessions.
	completions := []struct {
		sessionID string
		model     string
		usage     agent.Usage
	}{
		{"s1", "claude-sonnet-4-6", agent.Usage{InputTokens: 5000, OutputTokens: 800, CacheReadInputTokens: 12000}},
		{"s1", "claude-sonnet-4-6", agent.Usage{InputTokens: 200, OutputTokens: 300, CacheReadInputTokens: 12000}},
		{"s2", "claude-opus-4-6", agent.Usage{InputTokens: 1200, OutputTokens: 5000, CacheCreationEphemeral1h: 3400}},
	}

	for _, c := range completions {
		must(rec.Record(ctx, agent.CostEvent{
			SessionID: c.sessionID,
			Provider:  "anthropic",
			Model:     c.model,
			Usage:     c.usage,
		}))
	}

	// Per-session summary.
	for _, id := range []string{"s1", "s2"} {
		sum, err := costs.SumBySession(ctx, id, 0)
		must(err)
		fmt.Printf("%s: %d completions, in=%d out=%d cache-r=%d cost=$%.4f\n",
			id, sum.CompletionCount, sum.InputTokens, sum.OutputTokens, sum.CacheReadTokens, sum.CostUSD)
	}

	// Top-N over all history.
	top, err := costs.TopSessions(ctx, 0, 10)
	must(err)
	fmt.Println("\ntop by cost:")
	for i, r := range top {
		fmt.Printf("  %d. %s: $%.4f (%d completions)\n", i+1, r.SessionID, r.CostUSD, r.CompletionCount)
	}

	_ = time.Now // silence unused import if the example is trimmed
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
