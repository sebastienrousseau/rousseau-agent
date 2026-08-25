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
	"io"
	"os"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

func main() { os.Exit(run(context.Background(), os.Stdout, os.Stderr)) }

// run executes the demo and returns the process exit code. main does
// nothing but call os.Exit so that tests can drive run directly.
func run(ctx context.Context, out, errOut io.Writer) int {
	if err := demo(ctx, out); err != nil {
		fmt.Fprintln(errOut, "embed-cost:", err)
		return 1
	}
	return 0
}

func demo(ctx context.Context, out io.Writer) error {
	base, err := sqlitestore.Open(ctx, ":memory:")
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = base.Close() }()

	costs, err := sqlitestore.NewSessionCostStore(ctx, base)
	if err != nil {
		return fmt.Errorf("session cost store: %w", err)
	}
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
		if err := rec.Record(ctx, agent.CostEvent{
			SessionID: c.sessionID,
			Provider:  "anthropic",
			Model:     c.model,
			Usage:     c.usage,
		}); err != nil {
			return fmt.Errorf("record %s: %w", c.sessionID, err)
		}
	}

	// Per-session summary.
	for _, id := range []string{"s1", "s2"} {
		sum, err := costs.SumBySession(ctx, id, 0)
		if err != nil {
			return fmt.Errorf("sum %s: %w", id, err)
		}
		fmt.Fprintf(out, "%s: %d completions, in=%d out=%d cache-r=%d cost=$%.4f\n",
			id, sum.CompletionCount, sum.InputTokens, sum.OutputTokens, sum.CacheReadTokens, sum.CostUSD)
	}

	// Top-N over all history.
	top, err := costs.TopSessions(ctx, 0, 10)
	if err != nil {
		return fmt.Errorf("top sessions: %w", err)
	}
	fmt.Fprintln(out, "\ntop by cost:")
	for i, r := range top {
		fmt.Fprintf(out, "  %d. %s: $%.4f (%d completions)\n", i+1, r.SessionID, r.CostUSD, r.CompletionCount)
	}
	return nil
}
