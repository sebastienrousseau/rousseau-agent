// Package main demonstrates the sub-agent parallelism primitive
// (pkg/agent/subagent). Three independent research tasks run
// concurrently against a shared parent session with bounded
// concurrency + per-task timeout + total-token budget, then the
// aggregator condenses the results into a single content block the
// parent's next completion can consume.
//
// Run with:
//
//	go run ./examples/embed-subagent
//
// The claudecli provider requires the `claude` CLI on $PATH. Substitute
// any other agent.Provider (anthropic, bedrock, vertex, openai) for a
// different backend.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/pkg/agent"
	"github.com/sebastienrousseau/rousseau-agent/pkg/agent/subagent"
	"github.com/sebastienrousseau/rousseau-agent/pkg/llm/claudecli"
)

func main() { os.Exit(run(context.Background(), defaultProvider(), os.Stdout, os.Stderr)) }

// defaultProvider shells out to the local `claude` CLI. Substitute any
// other agent.Provider for a different backend.
func defaultProvider() agent.Provider {
	return claudecli.New(claudecli.Config{PermissionMode: "acceptEdits"})
}

// run fans the parent session out into three sub-agents and prints the
// aggregated result, returning the process exit code. The provider is a
// parameter so the fan-out can be pointed at any backend; main does
// nothing but call os.Exit so that tests can drive run directly.
func run(ctx context.Context, provider agent.Provider, out, errOut io.Writer) int {
	logger := slog.New(slog.NewJSONHandler(errOut, nil))
	parent := agent.NewSession("triage")

	tasks := []subagent.Task{
		{
			Prompt:   "Summarise every open PR on rousseau-agent that touches pkg/agent/.",
			Timeout:  90 * time.Second,
			MaxTurns: 6,
		},
		{
			Prompt:   "List the last three CVEs that affected any Go module in this repo.",
			Timeout:  90 * time.Second,
			MaxTurns: 4,
		},
		{
			Prompt:   "Skim README.md and produce three suggestions for improving the intro.",
			Timeout:  60 * time.Second,
			MaxTurns: 3,
		},
	}

	policy := subagent.Policy{
		MaxConcurrent:      2, // stay polite; both hit the same provider
		PerTaskTimeout:     90 * time.Second,
		BudgetTokens:       15000,    // aggregate ceiling across all three
		AggregatorMaxBytes: 8 * 1024, // 8 KiB combined output
	}

	results, err := subagent.Spawn(ctx, parent, provider, tasks, policy, logger)
	if err != nil {
		fmt.Fprintf(errOut, "spawn: %v\n", err)
		return 1
	}

	aggregated := subagent.DefaultAggregator{}.Aggregate(results, policy.AggregatorMaxBytes)
	fmt.Fprintln(out, aggregated.ToolResult.Output)
	return 0
}
