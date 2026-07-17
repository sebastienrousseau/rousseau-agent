// Package main demonstrates the sub-agent parallelism primitive
// (internal/agent/subagent). Three independent research tasks run
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
	"log/slog"
	"os"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent/subagent"
	"github.com/sebastienrousseau/rousseau-agent/internal/llm/claudecli"
)

func main() {
	provider := claudecli.New(claudecli.Config{PermissionMode: "acceptEdits"})
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	parent := agent.NewSession("triage")

	tasks := []subagent.Task{
		{
			Prompt:   "Summarise every open PR on rousseau-agent that touches internal/agent/.",
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

	results, err := subagent.Spawn(context.Background(), parent, provider, tasks, policy, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "spawn: %v\n", err)
		os.Exit(1)
	}

	aggregated := subagent.DefaultAggregator{}.Aggregate(results, policy.AggregatorMaxBytes)
	fmt.Println(aggregated.ToolResult.Output)
}
